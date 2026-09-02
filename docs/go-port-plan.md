# hermes-hands → Go port — implementation plan

Status: **planning only.** No `.go` file exists yet. This document is the sole
deliverable of the planning pass. Implementation happens on this same branch
(`go-port`), one commit per milestone (M0–M9).

Verified against the bash source at commit `4883541` (branch `main`) on
2026-09-02. Where this plan and the seed spec
(`scratchpad/go-port-seed-plan.md`) disagree, **the bash source wins** and the
divergence is listed in §2.

---

## 1. Overview

| | |
|---|---|
| **Goal** | Replace the bash bundle with one static Go binary (`CGO_ENABLED=0`), no `bash`/`curl`/`jq` runtime deps, cross-compilable to linux/darwin/windows × amd64/arm64. |
| **Rationale** | Real `encoding/json` kills the `jq` + `awk` brace-matching envelope hack; real SSE reader ready for split-runtime (#63966); real `go test`; single-file distribution with no interpreter. |
| **Module path** | `github.com/Zesales/hermes-hands` |
| **What stays byte-identical** | The wire contract (§3, all 17 items): endpoint paths, headers, request/response JSON shapes, ID-minting formulas, the envelope extraction + recovery ladder, the results object, the denylist patterns, the redaction regex set, the repo-jail rule, the CLI surface (flags, subcommands, REPL `/commands`), the banner/prompt strings, all env/config keys and their defaults. |
| **What changes** | Distribution (`build.sh` bundler → `go build`); `install.sh` fetches a platform binary; CI runs Go tooling; `edit_file` gains literal (non-regex) replace semantics (§2 #18, flagged); the dead `hh_render()` is dropped. |
| **Still a bridge** | `hermes-hands` remains the **outbound-only** stopgap until Hermes split-runtime [PR #63966](https://github.com/NousResearch/hermes-agent/pull/63966) merges. The `{"calls":[…],"final":…}` envelope is deliberately the shape #63966 expects; the local tool dispatcher (`internal/dispatch`, `internal/shell`) is the part that does not change when #63966 lands — only the transport in front of it swaps from "poll a run" to "read an SSE stream". |

**M0 prerequisite (blocking):** Go is **not installed** on this host
(`command -v go` fails; not in `/usr/local/go`, `/usr/lib/go`, `~/go/bin`,
`/snap/bin`). Implementation cannot start until the user **explicitly
authorizes and installs a pinned Go toolchain** (exact `major.minor.patch`; see
§5 open question). The global package-safety policy forbids the agent from
installing it unprompted.

---

## 2. Corrections to the seed spec

Every place the real source differs from `scratchpad/go-port-seed-plan.md`.
Ordered; each cites `file:function` / line.

1. **`dist/hermes-hands` is NOT committed.** `.gitignore` contains `/dist/`;
   `git ls-files` has no `dist/` entry. The on-disk `dist/hermes-hands` is a
   local build artifact. Seed §0 ("`dist/hermes-hands` committed") is wrong —
   M8 has nothing to `git rm` there.

2. **Results object shape** (seed §6 guessed `{tool,exit,output,context}`).
   Actual — `lib/loop.sh:hh_loop_run` lines 107–110:
   `{"results":[ {"tool":<str>, "args":<obj>, "exit_code":<int>, "output":<scrubbed str>} (+ "context":<scrubbed str> only when non-empty) ]}`.
   The key is **`exit_code`** (not `exit`); **`args` is echoed back**; `context`
   is conditional on being non-empty.

3. **`.usage` is never read.** `lib/api.sh:hh_api_ask` reads only `.status`,
   `.output`, `.session_id` from run status. Seed §1 ("read `.output`,
   `.session_id`, `.usage`") — drop `.usage`; nothing consumes it.

4. **Session-reject retry keeps the `X-Hermes-Session-*` headers** (seed §3
   said "retry once fresh"). `lib/api.sh:hh_api_ask` lines 96–101: on
   `400|404|422` only the request **body** `session_id` is removed
   (`drop_sess=1`); `X-Hermes-Session-Id` and `X-Hermes-Session-Key` headers
   are still sent (gated only on the record having them). No new id is minted —
   the same session record is reused; the drop is per-HTTP-call and in-memory.
   A persistent rejection re-drops every round and **latches the local recap**
   (`recap=1`, never reset — `lib/loop.sh` line 45).

5. **`401|403` on `POST /v1/runs` fails immediately** — no retry, no
   session-drop — with `HTTP <code> at /v1/runs - HERMES_API_KEY rejected.`
   (`lib/api.sh:hh_api_ask` line 108). Seed §3 only described the 400/404/422
   branch.

6. **Retry backoff is a fixed `sleep 2`**, not exponential and not the poll
   interval, in both `hh_api_check` and `hh_api_ask`'s POST loop. The poll loop
   sleeps `HH_API_POLL_INTERVAL` (`lib/api.sh` lines 64, 119, 148).

7. **`HERMES_API_RETRIES` (default 3) yields 4 attempts** — the guard is
   post-increment `(( attempt++ > HH_API_RETRIES ))` (`lib/api.sh` lines 62,
   116).

8. **Title PATCH base derivation** (seed §17). `lib/api.sh:hh_api_set_title`
   line 170: `URL = $(hh_api_base | sed 's#/v1$##')/api/sessions/<hermes_session_id>`.
   `hh_api_base` never contains `/v1` in normal use (each endpoint appends its
   own `/v1/…`), so the `sed` only bites when the user puts a trailing `/v1` in
   `HERMES_API_URL` **and** no profile is set. Effective Go rule:
   `titleBase = strings.TrimSuffix(apiBase(), "/v1")`. Method `PATCH`; headers
   `Authorization: Bearer`, `Content-Type: application/json`; body
   `{"title":<t>}`; `curl -m 8 --connect-timeout 4`; **all output and errors
   discarded**.

9. **`base` trims exactly one trailing slash.** `${HERMES_API_URL%/}`
   (`lib/api.sh:hh_api_base` line 23) removes a single `/`, not all. Profile
   suffix `/p/<profile>` is appended after that.

10. **Idempotency-Key** (seed §1): `hh-<unix_epoch_seconds>-<RANDOM><RANDOM>` —
    two bash `$RANDOM` values (each 0–32767) concatenated with **no
    separator** (`lib/api.sh:hh_api_ask` line 88). Minted **once** before the
    POST retry loop and reused across retries.

11. **Banner strings** (seed §14). `bin/hermes-hands` lines 141–142:
    line 1 is `hermes-hands <ver>  ·  <cwd>` — separator is `·` (U+00B7 middle
    dot, `\xc2\xb7`) with two spaces each side, **not** ` — `. Line 2 is
    `session <id>  ·  /help  /new  /sessions  /exit` (two-space gaps, middle
    dot); the id has any leading `hh_` stripped (`${SMODE#hh_}`). `/check` is a
    valid REPL command but is **not** in the banner — the shown set is
    `/help /new /sessions /exit`.

12. **REPL prompt is `you ❯ `** (`❯` = U+276F, `\xe2\x9d\xaf`), via bash
    `read -e` (readline). Empty input and a lone space are ignored
    (`bin/hermes-hands` line 156). Aliases: `/help` = `/h`, `/?`; `/exit` =
    `/quit`, `/q`. `/new`, `/sessions`, `/check` have **no** aliases. `/new`
    prints `— new session <id> —`.

13. **Env keys missing from seed §15** (all real, all with defaults in
    `lib/api.sh`): `HERMES_API_CONNECT_TIMEOUT`=5, `HERMES_API_MAX_TIME`=30
    (per HTTP request), `HERMES_API_POLL_INTERVAL`=2, `HERMES_API_RETRIES`=3,
    `HERMES_API_RUN_TIMEOUT`=600 (whole poll loop). Also
    `HERMES_HANDS_SECRETS` (secrets-file path override, `lib/util.sh` line 32)
    and `HERMES_HANDS_INSTRUCTIONS` (instructions-file path override,
    `lib/api.sh` line 75). `HERMES_HANDS_VERBOSE` is real (gates `hh_vlog`,
    `lib/util.sh` line 7) — seed listed it but only in passing.

14. **Instructions ladder** (seed §15 collapsed it). `lib/api.sh:hh_api_ask`
    lines 75–78: `$HERMES_HANDS_INSTRUCTIONS` (path) → `~/.config/hermes-hands/instructions.md`
    → `$HH_ROOT/share/instructions.md` → `$HH_INSTRUCTIONS_BUILTIN`
    (bundle-only base64 var). First **readable & non-empty** wins; an empty
    file falls through to the builtin. Sent only when non-empty.

15. **`hh_render()` (`bin/hermes-hands` lines 62–66) is dead code** — defined,
    never called (`grep` confirms zero callers). Real rendering is
    `lib/ui.sh:ui_answer` (REPL) and a bare `printf '%s\n'` (one-shot/stdin).
    The Go port omits it.

16. **The denylist also blocks `sftp`.** `lib/dispatch.sh:_hh_cmd_blocked`
    lines 69–75 match `ssh | scp | sftp | rsync | sudo | doas`; seed §11 and
    the README say only "ssh/scp/rsync". Full verbatim set in §3 #11.

17. **`--yolo` exports `HERMES_HANDS_APPROVE=auto` into the environment**
    (`bin/hermes-hands` line 104) before `hh_load_config`, so it also survives
    into any nested config parsing. REPL `a`/"all" latches `HH_APPROVE_ALL=1`
    for the **rest of the process**, not per turn (`lib/util.sh` line 94).

18. **`edit_file` uses regex replace, not literal.**
    `lib/dispatch.sh:hh_dispatch` lines 136–139:
    (a) the match test is `grep -F -c -- "$old"` = **count of lines containing
    `old`**, must be exactly `1` (multi-occurrence on one line still counts as
    1); (b) the substitution is `awk '{ if(!done && index($0,o)){ sub(o,nw);
    done=1 } print }'` — `sub()` treats `old` as a **regex** and `new`'s
    `&`/`\N` as replacement metacharacters. This is a latent
    injection/foot-gun. **Recommendation (flagged for user sign-off):** the Go
    port does a literal check (`strings.Count(content, old) == 1`) + literal
    `strings.Replace(content, old, new, 1)`, matching the documented contract
    ("`old` must appear exactly once") and dropping the regex behaviour as an
    intentional fix.

19. **`shell` output cap ≠ `read_file` cap.** `hh_shell_raw` caps at
    `2 × HERMES_HANDS_MAX_OUTPUT` (~40000 B, `lib/dispatch.sh` line 40);
    `read_file` caps at `HERMES_HANDS_MAX_OUTPUT` (~20000 B) via `_hh_cap`
    (line 112). Keep the asymmetry.

20. **`run_turn` (one-shot / stdin) prints the answer to STDOUT** with a
    single trailing newline and nothing else; every frame / `ui_*` line goes
    to STDERR. Exit code is the loop's: `0` = answer, `1` = `BLOCKED:` …
    (`bin/hermes-hands` lines 117–130). Seed did not spell out the split.

21. **`hh_setup` re-execs `"$_self" check`** (`bin/hermes-hands` line 81) —
    but `_self` is set only in the non-bundled tree (the `bundle:drop` block,
    lines 21–38, is removed by `build.sh`), so a **released bundle's `setup`
    would fail under `set -u`** at that line. The Go port calls its own check
    routine directly instead (no re-exec). Minor, noted for parity awareness.

22. **CI today also runs `./build.sh` + smokes `./dist/hermes-hands`**
    (`.github/workflows/ci.yml` line 20), and the trigger is a bare `push:`
    (all branches) + `pull_request:`. Seed §6 mentioned dropping
    shellcheck/bash-tests; also drop the `build.sh` smoke.

23. **`config` precedence "env wins" is conditional.**
    `lib/util.sh:hh_load_config` lines 33–36: the **config** file is sourced
    unconditionally (`set -a; . "$cfg"; set +a`) — it *would* override an env
    var if a key were uncommented; it only doesn't in practice because
    `config.example` ships fully commented. The **secrets** file is sourced
    **only if** `HERMES_API_URL` or `HERMES_API_KEY` is empty. The Go port must
    replicate this exact two-tier behaviour, **not** a naive "env always wins"
    (see §4 `internal/config`).

24. **`edit_file` diff / `write_file` diff are `head -c 4000`;** `git status`
    context is `head -c 1200`; the auto-context runs with a **15 s** timeout
    and only appends `make` targets when the failed command contained
    `" make "` (`lib/dispatch.sh` lines 102–105, 120, 140).

---

## 3. Verified contract (17 items, constants filled from source)

> Notation: `(src: file:function[:line])`. All constants are the bash default;
> the env override name is given where one exists.

**1 — Run submit + poll.**
`POST {base}/v1/runs`, body built by `jq` as
`{input:<msg>} + (instructions:<str> if non-empty) + (session_id:<str> if record has one AND not dropped)`.
Headers (every request carries `Authorization: Bearer <HERMES_API_KEY>` via
`_hh_curl`): `Content-Type: application/json`, `Idempotency-Key: hh-<epoch>-<RANDOM><RANDOM>`
(minted once, reused across retries), plus `X-Hermes-Session-Id: <record.hermes_session_id>`
and `X-Hermes-Session-Key: <record.hermes_session_key>` **iff the record has them**
(sent even on the post-reject retry — §2 #4).
Per-request timeouts: connect `HERMES_API_CONNECT_TIMEOUT`=5 s, total
`HERMES_API_MAX_TIME`=30 s. POST non-2xx/curl-error → `sleep 2`, retry, up to
`HERMES_API_RETRIES`=3 (→ 4 attempts) then fail. 2xx with no `.run_id` → fail.
Then **poll** `GET {base}/v1/runs/{run_id}` (+ `Accept: application/json`) every
`HERMES_API_POLL_INTERVAL`=2 s. Terminal: `status=="completed"` with non-empty
`.output` → success (read `.output`, `.session_id`); `failed|cancelled` → fail
(`.output // .error // "no detail"`, trunc 400); `started|running|queued|stopping|""`
→ keep polling; unknown status → `hh_log` + keep polling. Whole poll bounded by
`HERMES_API_RUN_TIMEOUT`=600 s. Poll HTTP errors are logged and retried, never
fatal on their own. `.usage` is **not** read (§2 #3).
`(src: lib/api.sh:hh_api_ask, _hh_curl)`

**2 — Base URL.** `base = strings.TrimSuffix(HERMES_API_URL, "/")` (one slash,
§2 #9) `+ "/p/" + HERMES_API_PROFILE` when `HERMES_API_PROFILE` is non-empty.
Endpoints append their own `/v1/...` or `/api/...`.
`(src: lib/api.sh:hh_api_base)`

**3 — Session-id drop / recap.** On `POST /v1/runs` → `400 | 404 | 422` **when
the request body carried `session_id`** and it has not yet been dropped this
call: warn `server rejected session_id (HTTP <c>) - retrying without it, local
recap on`, set `drop_sess=1`, `continue` (no attempt increment, no `sleep`).
The `X-Hermes-Session-*` **headers stay** (§2 #4). After a drop, `HH_ANS_THREADED=0`;
otherwise `HH_ANS_THREADED=1`. In the loop: `if round>1 && !threaded → recap=1`
(latched). `401|403` here is fatal immediately (§2 #5). Any other non-2xx →
generic retry then fail.
`(src: lib/api.sh:hh_api_ask:107-121, lib/loop.sh:45)`

**4 — Envelope + extraction ladder.**
Target object: `{"calls":[{"tool":"<name>","args":{…}}, …], "final": null | "<text>"}`.
`_hh_extract_obj(reply)`:
  1. copy `s=reply`; `s=TrimPrefix(s,"```json")`; `s=TrimPrefix(s,"```")`;
     `s=TrimSuffix(s,"```")` (literal, no whitespace trim between steps).
  2. if `json.Valid(TrimSpace(s))` **and** it is a JSON object → return `s`.
  3. else scan the **original** `reply`: first `{` … last `}` (byte indices);
     if none or `end<=start` → no object.
Back in `hh_loop_run`:
  * `obj` non-empty **and** `json.Valid(obj)` → parse: `calls_n = len(.calls // [])`,
    `final = .final if type=="string" else ""`.
  * else if `reply` matches `(?i)"(calls|tool)"[[:space:]]*:` (quoted key +
    optional ws + `:`) **and** `fixups < 2` → `fixups++`, resend
    `send = {"error":"your previous message was not a single valid JSON object of the form {\"calls\":[...],\"final\":null}. Resend ONLY that object, no prose, no code fences."}`,
    append `[round N] (invalid envelope, requested resend)` to turnlog,
    `round++`, continue.
  * else → **prose answer**: `ans = TrimPrefix(reply, "[INPUT_REQUIRED]")`,
    then strip leading whitespace of the **first line only**
    (`sed '1s/^[[:space:]]*//'`), return OK.
`(src: lib/loop.sh:_hh_extract_obj, hh_loop_run:47-69)`

**5 — `args` as a JSON string.** For each call: `args = .calls[i].args // .calls[i].arguments // {}`;
if `json` type of `args` is `"string"` → unwrap **once**
(`jq -r '.' | jq -c '.'`, i.e. parse the string's content as JSON; on failure
→ `{}`). `tool = .calls[i].tool // .calls[i].name // ""`.
`(src: lib/loop.sh:hh_loop_run:91-97)`

**6 — Tool results back.** After running the calls, `send = {"results":[…]}`
(compact) where each element is
`{tool:<str>, args:<obj>, exit_code:<int>, output:<Scrub(HH_TOOL_OUT)>}`
plus `context:<Scrub(HH_TOOL_CTX)>` **iff** non-empty (§2 #2). Next round's
message = `send`, unless `recap` — then it is prefixed:
```
[conversation so far this turn]
<turnlog>
[latest tool results]
<send>
```
`turnlog` accumulates one line per executed call:
`[round N]   <tool>(<compact args, head -c 120>) -> exit <code>`.
`(src: lib/loop.sh:hh_loop_run:33-40, 106-116)`

**7 — Rounds cap.** `HH_LOOP_MAX_ROUNDS = HERMES_HANDS_MAX_ROUNDS` (default
**8**). `round` starts at 1; the check `if round > MAX → BLOCKED: Hermes still
requesting data after <MAX> rounds.` sits **after** the empty-envelope handling
and **before** executing calls. Fixup resends also `round++`. Net: up to 8
rounds of executed tool calls; the 9th POST whose reply still has calls →
BLOCKED.
`(src: lib/loop.sh:9, 83-86)`

**8 — ID schemes.** In `hh_session_new`, with
`ts = strftime("%Y%m%dT%H%M%S", utc)` computed once and `RANDOM` a single bash
`$RANDOM` (0–32767):
  * local index id: `hh_<ts>_<sha1hex(cwd + str(RANDOM))[:6]>`
  * `hermes_session_id`: `hh-<sha1hex(cwd)[:8]>-<ts>`
  * `hermes_session_key`: `hermes-hands:<sha1hex(cwd)[:16]>`
  * by-cwd pointer file: `<sessDir>/by-cwd/<sha1hex(cwd)>.id` (full 40 hex)
`sha1` input is the raw string **without trailing newline**
(`printf '%s' … | sha1sum`). Adopt the server's `.session_id` from run status
when non-empty **and** different from the record's current
`hermes_session_id` (`_hh_session_write`).
`(src: lib/session.sh:hh_session_new, _hh_cwd_ptr; lib/api.sh:_hh_session_write)`

**9 — Config / secrets.**
`cfg = HERMES_HANDS_CONFIG | {XDG_CONFIG_HOME|~/.config}/hermes-hands/config`;
`sec = HERMES_HANDS_SECRETS | {XDG_CONFIG_HOME|~/.config}/hermes-hands/secrets`.
Load: if `cfg` readable → apply every `KEY=value` (overrides env — §2 #23).
Then **if `HERMES_API_URL=="" || HERMES_API_KEY==""`** and `sec` readable →
apply `sec` the same way. `setup` writes `config` (3 comment lines) and
`secrets` (`export HERMES_API_URL=%q` / `export HERMES_API_KEY=%q` via bash
`printf %q`), `chmod 600` on `secrets`, `chmod 700` on the dir, and offers to
append a source line to `~/.bashrc` if it does not already contain
`hermes-hands/secrets`.
`(src: lib/util.sh:hh_load_config; bin/hermes-hands:hh_setup)`

**10 — Tools + approval.**

| tool (aliases) | args (with fallbacks) | approval | notes |
|---|---|---|---|
| `shell` (`run`,`bash`,`exec`,`terminal`) | `cmd = .cmd//.command//.input`; `timeout = .timeout` if `^[0-9]+$` else `HERMES_HANDS_RUN_TIMEOUT`=120 | **per command** unless `APPROVE=auto` / `--yolo` / latched "all" | persistent login bash; denylist first (blocked → exit 126); on exit≠0 auto-attach `context` = `cwd:` + `git status -s | head -c 1200` (15 s), + `\n--- make targets ---\n` + parsed `make` targets **iff** cmd contains `" make "` |
| `read_file` (`read`,`cat`) | `path = .path//.file` | none | repo-jailed; `timeout 120 cat -- <abs> 2>&1`; output `head -c HERMES_HANDS_MAX_OUTPUT` (20000) |
| `write_file` (`write`) | `path`, `content = .content//.text` | diff + approval | diff = `diff -u` `head -c 4000`, or `(new file, <N> lines)` if absent; `mkdir -p` parent; success msg `wrote <p> (<bytes> bytes)` |
| `edit_file` (`edit`) | `path`, `old = .old//.find`, `new = .new//.replace` | diff + approval | file must exist; `old` must match exactly once (§2 #18); success msg `edited <p>` |

Missing required arg → `exit_code 2`. Jail refusal → `exit_code 1`. Approval
deny → `exit_code 125` `(declined by operator)`. Approval quit → dispatcher
returns "abort turn" → loop answer `(turn aborted by operator at a <tool>
approval)`, loop returns OK. Unknown tool → `exit_code 2`
`unknown tool '<t>' (have: shell, read_file, write_file, edit_file)`.
`(src: lib/dispatch.sh:hh_dispatch)`

**11 — Denylist (verbatim) + repo jail.**
`_hh_cmd_blocked` receives `c = " " + cmd + " "` (single space each side) and
matches these `case` globs (substring):

| pattern(s) | reason string |
|---|---|
| `*" ssh "*` `*" scp "*` `*" sftp "*` `*" rsync "*` | `ssh/scp/rsync to other hosts` |
| `*" sudo "*` `*" doas "*` | `privilege escalation` |
| `*"rm -rf /"*` `*"rm -fr /"*` `*":(){ :\|:& };:"*` | `destructive` |
| if `c` has `*" curl "*` or `*" wget "*`, then any of `*"\| sh"*` `*"\|sh"*` `*"\| bash"*` `*"\|bash"*` | `pipe-to-shell download` |
| each non-empty `\|`-split glob `g` of `HERMES_HANDS_DENY`, tested `[[ "$cmd" == $g ]]` against the **raw** cmd | `matches HERMES_HANDS_DENY (<g>)` |

Blocked → `HH_TOOL_EXIT=126`, `HH_TOOL_OUT="(blocked by worker policy: <reason>)"`.
**Repo jail** applies to `read_file`/`write_file`/`edit_file` only.
`_hh_resolve_in_repo(p)`: `abs = p` if absolute else `HH_REPO_ROOT/p`; resolve
the **parent** dir via `cd "$(dirname abs)" && pwd -P` (physical, symlinks
resolved) and re-append the literal basename; if `cd` fails → refuse. Accept
iff `abs == HH_REPO_ROOT` or `HasPrefix(abs+"/", HH_REPO_ROOT+"/")`.
`HH_REPO_ROOT = pwd -P` at launch. `shell` is **not** jailed (can `cd`
anywhere) — it relies on denylist + approval.
`(src: lib/dispatch.sh:_hh_cmd_blocked, _hh_resolve_in_repo)`

**12 — Secret scrub (verbatim).** `hh_scrub` = `sed -E` with these 7
expressions applied in order, each with GNU flags `Ig` (case-insensitive +
global); replacement guillemets are U+00AB / U+00BB:

```
1  s/(bearer[[:space:]]+)[A-Za-z0-9._~+\/-]{16,}=*/\1«redacted»/Ig
2  s/(authorization[[:space:]]*[:=][[:space:]]*)[^[:space:]"']+/\1«redacted»/Ig
3  s/((api[_-]?key|secret|token|passwd|password)[[:space:]]*[:=][[:space:]]*)[^[:space:]"']{6,}/\1«redacted»/Ig
4  s/(AKIA|ASIA)[A-Z0-9]{16}/«redacted-aws-key»/g
5  s/sk-[A-Za-z0-9]{20,}/«redacted»/g
6  s/gh[pousr]_[A-Za-z0-9]{20,}/«redacted»/g
7  s/-----BEGIN [A-Z ]*PRIVATE KEY-----/«redacted-private-key»/g
```

Go: 7 `regexp.MustCompile` values, `(?i)` prefix for 1–3, applied
sequentially with `ReplaceAllString`; `\1` → `${1}`. RE2 supports `{n,}`,
POSIX classes, and negated classes with POSIX classes. `.` in the RE2 default
does not cross `\n`, matching sed's line-oriented behaviour closely enough
(patterns 1–3 exclude whitespace anyway; 7 is single-line).
`(src: lib/util.sh:hh_scrub)`

**13 — TLS.** `hh_require_https(url, name)`:
`https://*` → ok. `http://127.0.0.1* | http://localhost* | http://0.0.0.0* |
http://[::1]*` → ok **iff** `HERMES_HANDS_ALLOW_HTTP=="1"` (with a warning),
else fatal. Any other `http://*` → fatal `<name> must be https:// - the bearer
token may not cross the network unencrypted.`. Non-http(s) → fatal `<name> is
not a http(s) URL: <url>`.
`(src: lib/util.sh:hh_require_https)`

**14 — CLI surface.** Arg parse (`bin/hermes-hands` lines 89–111), first match
wins per token:

| token | effect |
|---|---|
| `-h` `--help` | print `usage()` (heredoc, verbatim), exit 0 |
| `-v` `--version` `version` | `hermes-hands <ver>[ (<sha>)]`, exit 0 |
| `check` `--check` | `hh_load_config; hh_api_check; exit $?` |
| `sessions` `--list` | `hh_session_list`, exit 0 |
| `setup` | interactive first-run, exit 0 |
| `-c` `--continue` | `SMODE=continue` |
| `--new` | `SMODE=new` (default) |
| `--session <id>` | `SMODE=<id>` (errors if `<id>` missing) |
| `--yolo` | `export HERMES_HANDS_APPROVE=auto` |
| `-` | `STDIN=1` |
| `--` | rest of argv joined by spaces → `ONESHOT`, stop parsing |
| other `-*` | fatal `unknown option: <tok>` |
| bare word | appended to `ONESHOT` (space-joined) |

Dispatch after parse: `STDIN` → `run_turn(read all stdin)`, exit; else
`ONESHOT` non-empty → `run_turn(ONESHOT)`, exit; else interactive REPL
(preceded by a preflight: `hh_api_check` silent → on failure, if the secrets
file is readable die `API preflight failed - run: hermes-hands check`, else die
`not configured yet - run: hermes-hands setup`).
REPL line handling: ignore `""`/`" "`; `/exit`|`/quit`|`/q` → break;
`/help`|`/h`|`/?` → help; `/check` → re-test; `/new` → fresh session +
`— new session <id> —`; `/sessions` → list; else run a turn (`ui_rule`,
`ui_you`, `ui_working`, loop, `ui_answer`). EOF (`Ctrl-D`) → newline + break.
Banner + prompt strings: §2 #11, #12.
`(src: bin/hermes-hands)`

**15 — Env / config keys (complete).**

| key | default | consumer |
|---|---|---|
| `HERMES_API_URL` | — (required) | api |
| `HERMES_API_KEY` | — (required) | api (Bearer) |
| `HERMES_API_PROFILE` | — | api (`/p/<x>`) |
| `HERMES_API_CONNECT_TIMEOUT` | 5 | api |
| `HERMES_API_MAX_TIME` | 30 | api (per request) |
| `HERMES_API_POLL_INTERVAL` | 2 | api (poll) |
| `HERMES_API_RETRIES` | 3 | api (→ 4 attempts) |
| `HERMES_API_RUN_TIMEOUT` | 600 | api (poll ceiling) |
| `HERMES_HANDS_APPROVE` | `ask` | `ask`\|`auto`\|`never` |
| `HERMES_HANDS_DENY` | — | `\|`-sep shell globs |
| `HERMES_HANDS_MAX_ROUNDS` | 8 | loop |
| `HERMES_HANDS_RUN_TIMEOUT` | 120 | per shell command |
| `HERMES_HANDS_MAX_OUTPUT` | 20000 | bytes/tool result (shell = 2×) |
| `HERMES_HANDS_ALLOW_HTTP` | — | `1` allows http on loopback |
| `HERMES_HANDS_VERBOSE` | — | `hh_vlog` chatter |
| `HERMES_HANDS_STATE` | `{XDG_STATE_HOME\|~/.local/state}/hermes-hands` | session index dir |
| `HERMES_HANDS_CONFIG` | `{XDG_CONFIG_HOME\|~/.config}/hermes-hands/config` | config file path |
| `HERMES_HANDS_SECRETS` | `{XDG_CONFIG_HOME\|~/.config}/hermes-hands/secrets` | secrets file path |
| `HERMES_HANDS_INSTRUCTIONS` | `{XDG_CONFIG_HOME\|~/.config}/hermes-hands/instructions.md` | instructions override path |
| `NO_COLOR`, `TERM`, `COLUMNS` | — | ui gate / width |

Instructions ladder: §2 #14. `HERMES_HANDS_STATE`, when set, **is** the state
dir (no `/hermes-hands` suffix appended).
`(src: lib/*.sh, grep of HERMES_*/HH_*)`

**16 — `check`.** `GET {base}/v1/capabilities` (+ `Accept: application/json`).
Success = `curl ok && HTTP 2xx && body is JSON` → print
`API OK: <.model // .runtime.mode // "hermes"> @ <base>`, exit 0. `401|403` →
fatal `HERMES_API_KEY rejected`. `404` → fatal `wrong base URL / profile, or
API server disabled`. Else `sleep 2`, retry to `HERMES_API_RETRIES`, then fatal
`cannot reach <base>/v1 - curl <rc> <err>, last HTTP <code>`.
`(src: lib/api.sh:hh_api_check)`

**17 — Title mirroring.** After the first successful turn only (title still
empty), `hh_session_set_title` trims the user message
(`tr '\n' ' ' | head -c 72`), writes `.title` locally, then best-effort
`PATCH {TrimSuffix(base,"/v1")}/api/sessions/<hermes_session_id>` with
`{"title":<t>}` (§2 #8). Every failure swallowed.
`(src: lib/session.sh:hh_session_set_title; lib/api.sh:hh_api_set_title)`

---

## 4. Go package layout

```
go.mod  go.sum                  # module github.com/Zesales/hermes-hands ; go 1.X.Y + toolchain go1.X.Y
main.go                         # was bin/hermes-hands
embed.go                        # //go:embed share/instructions.md -> var builtinInstructions string
VERSION                         # unchanged; injected via -ldflags -X main.version
share/instructions.md           # unchanged; embedded
project.env  LICENSE            # unchanged
docs/go-port-plan.md            # this file
test/mock_hermes.py             # kept, README marks it manual/optional

internal/config/                # was lib/util.sh (config half)
internal/redact/                # was lib/util.sh:hh_scrub
internal/ttyio/                 # isatty + /dev/tty helper (new, tiny)
internal/prompt/                # was lib/util.sh:hh_confirm (approval gate)
internal/ui/                    # was lib/ui.sh + banner/help strings from bin/
internal/api/                   # was lib/api.sh
internal/session/               # was lib/session.sh
internal/shell/                 # was lib/dispatch.sh (persistent-shell half)
internal/dispatch/              # was lib/dispatch.sh (router + jail + denylist)
internal/loop/                  # was lib/loop.sh
internal/hermesmock/            # was test/mock_hermes.py, as an httptest.Server
```

### 4.1 Package → bash-function map + public API (design level)

#### `internal/config`  ← `lib/util.sh`

| replaces | |
|---|---|
| `hh_load_config` | `Load() (*Config, error)` |
| `hh_looks_unset` | `LooksUnset(v string) bool` |
| `hh_require_https` | `RequireHTTPS(rawURL, varName string, allowHTTP bool) error` |
| `HH_CONFIG_PATH` / `HH_SECRETS_PATH` | fields on `Config` |

```go
type Config struct {
    APIURL, APIKey, APIProfile           string
    ConnectTimeout, MaxTime, PollInterval time.Duration
    APIRetries                           int
    APIRunTimeout                        time.Duration
    Approve                              string // ask|auto|never
    Deny                                 []string
    MaxRounds, RunTimeout, MaxOutput     int
    AllowHTTP, Verbose                   bool
    StateDir                             string
    ConfigPath, SecretsPath, InstrPath   string
}
func Load() (*Config, error)   // read cfg file (override env), then secrets file
                               // only if APIURL|APIKey empty; then env defaults;
                               // resolves all *Path/*Dir; does NOT dial anything
func LooksUnset(v string) bool
func RequireHTTPS(rawURL, name string, allowHTTP bool) error
```
Shell-assignment file parser (internal): accepts `# comment`, blank lines,
`export KEY=value`, `KEY=value`, value optionally `"..."`, `'...'`, or bash
`$'...'` (ANSI-C, as emitted by `printf %q`); anything else in a config file →
ignored (bash would execute it — we deliberately narrow to assignments and
document that in the README config section).

#### `internal/redact`  ← `lib/util.sh:hh_scrub`

```go
func Scrub(s string) string   // 7 compiled regexps, applied in the source order
```

#### `internal/ttyio`  (new)

```go
func IsTerminal(f *os.File) bool          // f.Stat() & os.ModeCharDevice (pure stdlib)
func OpenControllingTTY() (*os.File, error) // os.OpenFile("/dev/tty", O_RDWR, 0)
```

#### `internal/prompt`  ← `lib/util.sh:hh_confirm`

```go
type Decision int
const (Approve Decision = iota; Deny; AbortTurn)

type Approver interface { Confirm(summary, detail string) Decision }

type TTYApprover struct {         // opens /dev/tty lazily; latches "all"
    Mode    string                // HERMES_HANDS_APPROVE
    allDone bool                  // was HH_APPROVE_ALL (process-lifetime)
    Warnf   func(string, ...any)
}
func (a *TTYApprover) Confirm(summary, detail string) Decision
// auto -> Approve; never -> warn+Deny; latched -> Approve;
// no /dev/tty -> warn+Deny; prompt "  ⚠  <summary>\n<detail indented 6>\n
//   [y]es  [n]o  [a]ll  [q]uit turn > " ; y->Approve a->latch+Approve
// q/EOF->AbortTurn *->Deny
type AutoApprover struct{}        // for --yolo / tests, always Approve
```

#### `internal/ui`  ← `lib/ui.sh` + banner/help from `bin/hermes-hands`

| replaces | |
|---|---|
| colour gate (`[[ -t 2 && NO_COLOR== && TERM!=dumb ]]`) | `New(stderr *os.File) *UI` |
| `ui_cols` | `(*UI).cols()` — `COLUMNS` else 96, clamp [40,120] |
| `ui_rule` / `ui_you` / `ui_working` / `ui_call` / `ui_answer` | methods, all write to stderr |
| banner (lines 141–142) | `(*UI).Banner(version, cwd, sessionID string)` |
| `_help` | `(*UI).Help()` |

```go
type UI struct { w *os.File; color bool }
func New(stderr *os.File) *UI
func (u *UI) Rule()
func (u *UI) You(msg string)
func (u *UI) Working()
func (u *UI) Call(tool, preview string, exit int)   // "   ⟩ %-7s <72-clip>  exit N", exit red if ≠0
func (u *UI) Answer(text string)                    // glow -> bat -> fmt -> plain, each line "   "-prefixed
func (u *UI) Banner(version, cwd, sessionID string)
func (u *UI) Help()
```
`Answer` shells to `glow -w <cols-3> -` / `bat -pp -l md --color=always` /
`fmt -s -w <cols-3>` via `os/exec` only when they are on `PATH` and stderr is a
tty (matches `ui_answer`); otherwise raw. No markdown library.

#### `internal/api`  ← `lib/api.sh`

| replaces | |
|---|---|
| `hh_api_base` | `(*Client).base()` |
| `sed 's#/v1$##'` in `hh_api_set_title` | `(*Client).titleBase()` |
| `hh_api_preflight` | `(*Client).preflight() error` (looksUnset + RequireHTTPS) |
| `_hh_curl` | `(*Client).do(ctx, method, url, body, hdr) (code int, respBody []byte, err error)` |
| `hh_api_check` | `(*Client).Check(ctx) (CheckResult, error)` |
| `hh_api_ask` | `(*Client).Ask(ctx, msg string, rec *session.Record) (AskResult, error)` |
| `_hh_session_write` (adopt) | done inside `Ask` via a callback / returned `SessionID` |
| `hh_api_set_title` | `(*Client).SetTitle(ctx, hermesSessionID, title string)` (no error return) |

```go
type Client struct {
    HTTP         *http.Client   // Timeout = MaxTime; DialContext connect-timeout = ConnectTimeout
    BaseURL, Key, Profile string
    Retries      int
    PollInterval, RunTimeout time.Duration
    Instructions string
    Warnf, Vlogf func(string, ...any)
    Log          func(string, ...any)
    now          func() time.Time  // injectable for tests
    rnd          func() int         // 0..32767, injectable
}
type CheckResult struct { Model, Base string }
type AskResult struct {
    State     string // "completed" | "blocked"
    Text      string // final output, or the failure reason
    RunID     string
    SessionID string // from run status .session_id ("" if none)
    Threaded  bool   // false after a session drop OR when none was sent-and-kept
}
func (c *Client) Check(ctx context.Context) (CheckResult, error)
func (c *Client) Ask(ctx context.Context, msg string, rec *session.Record) (AskResult, error)
func (c *Client) SetTitle(ctx context.Context, hermesSessionID, title string)
```
`Ask` builds the body (`input` + optional `instructions` + optional
`session_id`), mints the idempotency key once, runs the POST retry/drop
ladder, then the poll loop; on `completed` it returns `SessionID` for the
caller (`main`) to adopt into the record + persist. SSE reader (future #63966)
lands as `(*Client).Stream(...)` later — out of scope now.

#### `internal/session`  ← `lib/session.sh`

| replaces | |
|---|---|
| `HH_STATE_DIR` / `HH_SESS_DIR` resolution | `Open() (*Store, error)` |
| `hh_session_new` (+ id minting, by-cwd ptr) | `(*Store).New(cwd string) (*Record, error)` |
| `hh_session_latest_for` | `(*Store).LatestForCwd(cwd string) (string, bool)` |
| `hh_session_resolve` | `(*Store).Resolve(mode, cwd string) (*Record, error)` |
| `hh_session_path` | `(*Store).path(id string) string` |
| `_hh_session_write` (index bump + adopt) | `(*Store).BumpTurn(rec *Record, runID, serverSID string) error` |
| `hh_session_set_title` (local half) | `(*Store).SetTitleLocal(rec *Record, msg string) (bool, error)` |
| `hh_session_list` | `(*Store).List() ([]Record, error)` |
| `hh_sha1` | `sha1hex(s string) string` (always `crypto/sha1`, matching the primary path) |

```go
type Record struct {
    ID               string `json:"id"`
    HermesSessionID  string `json:"hermes_session_id"`
    HermesSessionKey string `json:"hermes_session_key"`
    Cwd              string `json:"cwd"`
    Title            string `json:"title"`
    Created          string `json:"created"`
    Updated          string `json:"updated"`
    Turns            int    `json:"turns"`
    LastRunID        string `json:"last_run_id,omitempty"`
    path             string // not serialised
}
type Store struct { dir string } // <state>/sessions ; ensures dir + dir/by-cwd
func Open() (*Store, error)
func (s *Store) New(cwd string) (*Record, error)
func (s *Store) Resolve(mode, cwd string) (*Record, error) // "new"|""|"continue"|<id>
func (s *Store) LatestForCwd(cwd string) (string, bool)
func (s *Store) BumpTurn(rec *Record, runID, serverSID string) error
func (s *Store) SetTitleLocal(rec *Record, msg string) (changed bool, err error)
func (s *Store) List() ([]Record, error)
```
JSON marshalling must produce the same key set and `time` format
(`hh_now` = `2006-01-02T15:04:05Z`, second precision, `Z` suffix). `List`
sorts by `Updated` descending; row: `%-24s  %5d  %-19s  %s` with `Title` or
`Cwd` fallback (`updated[:19]`).

#### `internal/shell`  ← `lib/dispatch.sh` (persistent-shell half)

| replaces | |
|---|---|
| `coproc HH_SH { exec bash --login 2>&1; }` | `(*Shell).start()` — `exec.Command("bash","--login")`, `Stderr = Stdout`, own process group |
| priming (`shopt`; `. ~/.bashrc`; `PROMPT_COMMAND=`; `PS1=`) + `cd <root>` | inside `start()`, output discarded to first mark, 15 s / 10 s |
| `hh_shell_stop` | `(*Shell).Stop()` — write `exit\n`, then `syscall.Kill(-pgid, SIGKILL)` |
| `hh_shell_raw` (mark protocol, timeout, recycle) | `(*Shell).Run(cmd string, timeout time.Duration) (out string, exit int)` |

```go
type Shell struct {
    repoRoot string
    maxOut   int // 2 * config.MaxOutput
    // cmd, stdin io.WriteCloser, out *bufio.Reader, pgid int, up bool
}
func New(repoRoot string, maxOut int) *Shell
func (s *Shell) Run(cmd string, timeout time.Duration) (out string, exit int)
func (s *Shell) Stop()
```
`Run`: mark `__HC_<pid>_<rand><rand>__`; write `cmd + "\nprintf '\\n%s %s\\n'
<mark> \"$?\"\n"`; read lines until one begins `"<mark> "` → parse trailing
int as exit, return `TrimRight(out,"\n")`; cap appends at `maxOut` bytes but
keep reading for the mark; on write error → `Stop()+start()`, exit 124,
`"(shell not available)"`; on read timeout / EOF → `Stop()+start()`, exit 124,
`out + "\n[timed out after <n>s - shell was reset]"` (state lost — fresh shell,
re-primed, re-`cd`).

#### `internal/dispatch`  ← `lib/dispatch.sh` (router half)

| replaces | |
|---|---|
| `hh_dispatch` | `(*Dispatcher).Dispatch(tool string, args json.RawMessage) (Result, error)` |
| `_hh_cmd_blocked` | `cmdBlocked(cmd string, deny []string) (reason string, blocked bool)` |
| `_hh_resolve_in_repo` | `resolveInRepo(repoRoot, p string) (string, error)` |
| `_hh_cap` | `capBytes(s string, n int) string` |
| auto-context-on-failure | `(*Dispatcher).failCtx(cmd string) string` |

```go
type Result struct { Exit int; Out, Ctx string }
var ErrAbortTurn = errors.New("hermes-hands: turn aborted by operator")

type Dispatcher struct {
    RepoRoot  string
    Shell     *shell.Shell
    Approver  prompt.Approver
    Deny      []string
    MaxOutput int  // 20000 (read_file); shell uses Shell.maxOut
    RunTimeout time.Duration // 120s default per shell cmd
}
func (d *Dispatcher) Dispatch(tool string, args json.RawMessage) (Result, error)
```
Arg extraction mirrors the `jq` fallbacks in §3 #10. `edit_file` uses literal
single-occurrence replace (§2 #18). Approval `AbortTurn` → return `ErrAbortTurn`.

#### `internal/loop`  ← `lib/loop.sh`

| replaces | |
|---|---|
| `hh_loop_run` | `(*Loop).Run(ctx, userMsg string, rec *session.Record, persist func()) Outcome` |
| `_hh_extract_obj` | `extractObj(reply string) (obj string, ok bool)` |
| envelope parse / fixups / recap / cap | inside `Run` |
| results assembly | `buildResults(...) string` |

```go
type Loop struct {
    API       *api.Client
    Dispatch  *dispatch.Dispatcher
    UI        *ui.UI
    MaxRounds int            // 8
    Scrub     func(string) string
    Vlogf, Warnf func(string, ...any)
}
type Outcome struct { Answer string; OK bool } // OK=false => "BLOCKED: ..."
func (l *Loop) Run(ctx context.Context, userMsg string, rec *session.Record, persist func()) Outcome

type envelope struct {
    Calls []struct {
        Tool, Name string
        Args, Arguments json.RawMessage
    } `json:"calls"`
    Final *string `json:"final"`
}
```
`persist` is `main`'s callback to `store.BumpTurn` after each `api.Ask` that
returns a server session id (mirrors `_hh_session_write` being called from
inside `hh_api_ask`).

#### `main.go`  ← `bin/hermes-hands`

`usage()` text (verbatim heredoc) · `versionString()` (`-ldflags -X
main.version`/`main.commit`, `runtime/debug.ReadBuildInfo()` fallback for
`go install`) · hand-rolled arg parse (the `--` rest-capture, `-` stdin, and
positional-accumulation semantics do not fit `flag`) · `runTurn()` (stdout =
answer + `\n`, stderr = frames; exit 0/1) · one-shot / stdin / REPL branch ·
REPL loop with `liner` · `setup()` (writes `config` + `secrets` 0600,
`~/.bashrc` offer) · wiring: `config.Load` → `api.Client` → `session.Store` →
`shell.Shell` → `dispatch.Dispatcher` → `loop.Loop`. `SIGINT` handler cancels
the turn context, stops the poll, kills the shell child, returns to the prompt
(does not exit); `liner.ErrPromptAborted` handles Ctrl-C at the prompt;
`io.EOF` (Ctrl-D) breaks.

---

## 5. Dependencies

**Toolchain.** `go.mod`: `go 1.X.Y` **and** `toolchain go1.X.Y`, both exact
(fill at M0 — see open questions). `CGO_ENABLED=0` everywhere.

**Third-party (pinned, none younger than 48 h):**

| module | version | why | age |
|---|---|---|---|
| `github.com/peterh/liner` | `v1.2.2` | interactive line editor for the REPL. Native `ErrPromptAborted` on Ctrl-C maps **directly** to "cancel the turn, stay in the REPL"; native `io.EOF` on Ctrl-D → `/exit`; `AppendHistory` mirrors bash `history -s`; `PasswordPrompt` covers `setup`'s `read -s`. Pure Go, no cgo, Windows support. MIT. | tag 2022-05-06 — very old, well-soaked |
| `github.com/mattn/go-runewidth` | `v0.0.3` | transitive (liner's own pin). Kept at liner's pinned version so the module graph stays at exactly these two. MIT. | 2018 |

**Chosen `liner` over `golang.org/x/term`** (seed §4 asked to pick one):
`term.Terminal.ReadLine` runs the tty in raw mode, delivers no SIGINT, and
needs manual prompt redraw around every `ui.Call` progress line — more code,
worse fit for the Ctrl-C-cancels-turn requirement (seed risk §9). `liner`
gives all of that for free. **Fallback** if the user rejects an unmaintained
dep: `golang.org/x/term` (`term.MakeRaw` + `term.Terminal`) — see open
questions.

**isatty**: pure stdlib — `f.Stat()` then `fi.Mode()&os.ModeCharDevice != 0`.
No `go-isatty`, no `x/term`.

**Explicitly NOT used:** cgo (`CGO_ENABLED=0`); any pty library
(`creack/pty` — bash used `coproc`/pipes, we match with `exec.Cmd` pipes);
any markdown renderer (shell out to `glow`/`bat`/`fmt` when present, else
raw — identical to bash); `bubbletea`/`tview` (full-screen TUI is a later,
separate feature).

**Stdlib:** `bufio bytes context crypto/sha1 embed encoding/hex encoding/json
errors fmt io math/rand net/http net/http/httptest os os/exec os/signal
path/filepath regexp runtime/debug sort strconv strings syscall time
unicode/utf8`.

**Optional dev tool (pinned, not a module require):**
`honnef.co/go/tools/cmd/staticcheck@v0.5.1` via `go run` in `make lint` — off
by default, opt-in.

---

## 6. Test plan

### 6.1 httptest mock  (`internal/hermesmock`)  ← `test/mock_hermes.py`

```go
func New(mode string) *httptest.Server   // mode ∈ plain|prose|badjson|delegate|shellstate
```
Handlers (path regexes identical to the Python, incl. the optional
`/(?:p/[^/]+/)?` profile prefix):

| route | behaviour |
|---|---|
| `GET …/v1/capabilities` | `200 {"model":"hermes-agent","runtime":{"mode":"server_agent"}}` |
| `POST …/v1/runs` | read body; `seenResults = contains(input,"\"results\"")`; `posted_sid = body.session_id or X-Hermes-Session-Id`; `n++`; `rid="run_<n>"`; pick `out` per mode; store `runs[rid]={out, posted_sid}`; reply `200 {"run_id":rid,"status":"started"}` |
| `GET …/v1/runs/{id}` | unknown → `404`; else `200 {"run_id":id,"status":"completed","session_id":<stored>,"output":<stored>}` |
| `PATCH …/api/sessions/{id}` | `200 {"ok":true}` |

Mode outputs (verbatim, incl. Python-style `True`/`False` in the `delegate` /
`shellstate` finals so integration assertions match):

* **plain** → `{"calls":[],"final":"plain answer from the mock brain."}`
* **prose** → `Just a plain-prose answer, no envelope at all.`
* **badjson** round 1 → `` ```json\n{"calls": [ {"tool": "read_file", }  ]  // oops\n``` ``; round 2 → `{"calls":[],"final":"recovered and answered."}`
* **delegate** round 1 → `{"calls":[{"tool":"read_file","args":{"path":"README.md"}},{"tool":"shell","args":{"cmd":"git rev-parse --abbrev-ref HEAD"}}],"final":null}`; round 2 → `final = "done. saw_results=<contains input \"exit_code\": 0 || \"exit_code\":0>."`
* **shellstate** round 1 → `{"calls":[{"tool":"shell","args":{"cmd":"mkdir -p sub && cd sub"}},{"tool":"shell","args":{"cmd":"pwd"}}],"final":null}`; round 2 → `final = "cwd_persisted=<contains input \"/sub\">"`

`test/mock_hermes.py` stays in the tree; README marks it "manual only — the Go
suite needs no `python3`".

### 6.2 Table-driven unit tests

| package | cases |
|---|---|
| `config` | env-set-vs-file precedence; secrets sourced only when URL\|KEY empty; `LooksUnset` (empty, `*REPLACE*`, `*placeholder*`, `EXAMPLE`, `<x>`, normal); `RequireHTTPS` (https, http+loopback+allow, http+loopback no-allow, http other host, `ftp://`); shell-assignment parser (`export K=v`, `K="v"`, `K='v'`, `K=$'a\tb'`, comment, blank, junk-ignored); `StateDir` (env set = literal; unset = `…/hermes-hands`) |
| `redact` | one row per regex (positive + a near-miss negative); a string hitting 3 patterns at once; plain prose unchanged; `«redacted»` byte-exact |
| `ui` | colour gate truth table (tty × `NO_COLOR` × `TERM=dumb`); `cols()` clamp (`COLUMNS=10`→40, `200`→120, unset→96); `Call` preview clipped at 72; `Answer` plain path prefixes every line with 3 spaces; banner + help byte-exact |
| `prompt` | `auto`→Approve; `never`→Deny(+warn); latched "all"→Approve; no tty→Deny(+warn); `y`/`a`/`q`/`n`/EOF mapping; prompt text byte-exact |
| `session` | `mintIDs` formats (`hh_<ts>_<6hex>`, `hh-<8hex>-<ts>`, `hermes-hands:<16hex>`, shared `ts`); `sha1hex` no-trailing-newline; `cwdPtr` = `by-cwd/<40hex>.id`; `New` writes file **and** ptr; `LatestForCwd` (ptr present / absent / dangling); `Resolve` (`new`; `continue` with none → warn + new; explicit missing → error); `BumpTurn` adopts a differing server id + `turns++`; `SetTitleLocal` only-if-empty + 72-char + `\n`→space; `List` order + fallback |
| `shell` | cwd persists call→call; `export FOO=bar` visible next call; alias/function from a fixture `HOME/.bashrc`; `exit 7` → exit 7; `sleep 5` @ 1 s timeout → exit 124 + reset text + cwd lost; >40 KB output capped, exit still parsed. Guard: `t.Skip` if `bash` absent |
| `dispatch` | `cmdBlocked` verbatim table (each pattern in §3 #11 + a benign line + a `HERMES_HANDS_DENY` glob); `resolveInRepo` (rel in repo, abs in repo, `../x` refused, symlinked parent resolves physical, missing parent refused, repo-root allowed); per-tool missing-arg → exit 2; read outside → exit 1; write new-file diff string + byte-count msg; edit 0/2 matches → exit 1, 1 → writes; approve Deny → exit 125; approve Quit → `ErrAbortTurn`; unknown tool → exit 2 |
| `loop` | `extractObj` (```json fence, bare ``` fence, prose+braces, no braces, object w/ leading newline, trailing prose after `}`); envelope parse (`.calls`, `.final` string, `.final` null, `.name`/`.arguments` aliases, `args` as JSON string → unwrapped once, bad string → `{}`); fixup (botched+structured & `fixups<2` → error resend & `round++`; `fixups==2` → prose accept); empty object → nudge then `BLOCKED: … empty envelope repeatedly.`; `round>Max` → `BLOCKED: … after 8 rounds.`; recap latches when `!threaded` past round 1; `[INPUT_REQUIRED]` strip + first-line ltrim; results object shape `{"results":[{tool,args,exit_code,output[,context]}]}` |
| `api` | (see 6.3) |
| `main` | arg-parse table → parsed struct (every row of §3 #14); `usage()` / `versionString()` byte-exact |

### 6.3 Integration tests (httptest fixture + real `bash`)

`internal/api` and a top-level `integration_test.go`; each `t.Skip` if `bash`
is unavailable.

| test | mode | asserts |
|---|---|---|
| `TestAsk_PlainCompleted` | plain | `AskResult.State=="completed"`, text has `plain answer from the mock brain` |
| `TestAsk_401Fatal` | (custom 401 handler) | error, no retry |
| `TestAsk_SessionDropRetry` | (handler 422 on first POST w/ `session_id`, 200 without) | second POST omits body `session_id`, keeps `X-Hermes-Session-*` headers, `Threaded==false` |
| `TestCheck_OK` | plain | `CheckResult.Model=="hermes-agent"` |
| `TestSetTitle_SwallowsError` | (handler 500 on PATCH) | no panic, no error surfaced |
| `TestIntegration_PlainFinal` | plain | answer ~ `plain answer from the mock brain` |
| `TestIntegration_ProseAccepted` | prose | answer ~ `plain-prose answer` |
| `TestIntegration_DelegateLoop` | delegate | answer ~ `saw_results=True` |
| `TestIntegration_BadJSONRecovery` | badjson | answer ~ `recovered and answered` |
| `TestIntegration_ShellCwdPersists` | shellstate | answer ~ `cwd_persisted=True` |
| `TestIntegration_SessionContinuity` | plain | `New` then `Resolve("continue")`: exactly 1 file for the dir; `Turns==2`; `HermesSessionID` has `hh-` prefix; `HermesSessionKey` has `hermes-hands:` prefix |

### 6.4 Mapping — today's 10 `run_tests.sh` checks → Go

| # | `run_tests.sh` check | Go equivalent |
|---|---|---|
| 1 | plain: final answer | `TestIntegration_PlainFinal` |
| 2 | prose: accepted as answer | `TestIntegration_ProseAccepted` |
| 3 | delegate: loop ran calls + fed results | `TestIntegration_DelegateLoop` |
| 4 | badjson: recovered after resend | `TestIntegration_BadJSONRecovery` |
| 5 | shell: cwd persists across calls in a turn | `TestIntegration_ShellCwdPersists` + `shell` unit test |
| 6 | check: API OK | `TestCheck_OK` / `TestIntegration_Check` |
| 7 | session: one file for the dir across `-c` | `TestIntegration_SessionContinuity` (file count) |
| 8 | session: turns accumulated | `TestIntegration_SessionContinuity` (`Turns==2`) |
| 9 | session: client session_id minted + kept (`hh-`) | `TestSession_MintIDs` + continuity test |
| 10 | session: per-repo session_key minted (`hermes-hands:`) | `TestSession_MintIDs` + continuity test |

Target: **meet or beat all 10.**

---

## 7. Build & distribution

### 7.1 `Makefile` (rewrite)

```
VERSION := $(shell tr -d '[:space:]' < VERSION)
GIT_SHA := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(GIT_SHA)
PREFIX  ?= $(HOME)/.local
BINDIR  ?= $(PREFIX)/bin
GOOSARCH := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64

build:        CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o dist/hermes-hands .
test:         CGO_ENABLED=0 go test ./...
lint:         gofmt -l . ; test -z "$$(gofmt -l .)" ; go vet ./...   # + optional staticcheck
dev-install:  build ; install -m0755 dist/hermes-hands $(BINDIR)/hermes-hands
uninstall:    rm -f $(BINDIR)/hermes-hands
release:      for t in $(GOOSARCH); do GOOS=$${t%/*} GOARCH=$${t#*/} CGO_ENABLED=0 \
                go build -trimpath -ldflags '$(LDFLAGS)' \
                -o dist/hermes-hands_$${t%/*}_$${t#*/}$$( [ $${t%/*} = windows ] && echo .exe ) . ; done
clean:        rm -rf dist
help:         @echo ...
```
`dev-install` copies a built binary (a symlink to source no longer works).
`build.sh` is **deleted** at M8.

### 7.2 `install.sh` (rewrite spec)

* `#!/bin/sh`, `set -eu`. Keep `REPO=${HERMES_HANDS_REPO:-Zesales/hermes-hands}`,
  `BIN=${HERMES_HANDS_BIN:-$HOME/.local/bin}`, `SRC=${HERMES_HANDS_DIR:-${XDG_DATA_HOME:-$HOME/.local/share}/hermes-hands-src}`,
  `TARGET=$BIN/hermes-hands`. `project.env` still the slug source of truth.
* Hard dep: **`curl` only** (drop the `bash` + `jq` checks).
* Detect: `os=$(uname -s)` → `Linux→linux`, `Darwin→darwin`, else error
  ("use WSL, or build from source with Go"). `arch=$(uname -m)` →
  `x86_64|amd64→amd64`, `aarch64|arm64→arm64`, else error.
* Asset: `hermes-hands_${os}_${arch}` (installer is unix-only; no `.exe`).
  `url=https://github.com/$REPO/releases/latest/download/hermes-hands_${os}_${arch}`.
* `curl -fsSL -o "$TARGET.new" "$url"` → on success `chmod +x`, run
  `"$TARGET.new" --version` in a subshell as a sanity gate, then `mv`. Drop
  the `head -1 | grep '#!/usr/bin/env bash'` shebang check (it is a binary now).
* Source fallback (any download/verify failure): require `git` **and** `go`;
  `git clone --depth 1` (or `git -C pull --ff-only`); then
  `( cd "$SRC" && CGO_ENABLED=0 go build -trimpath -ldflags "-X main.version=$(tr -d '[:space:]' <VERSION)" -o "$TARGET" . )`.
* Keep the trailing `"$TARGET" --version`, the PATH hint, `next: hermes-hands setup`.

### 7.3 CI  (`.github/workflows/ci.yml` diff)

Keep `name: ci`, `on: [push, pull_request]`, `runs-on: ubuntu-24.04`, and
`- uses: actions/checkout@11bd71901bbe5b1630ceea73d27597364c9af683`.

Replace the four bash steps with:

```yaml
      - uses: actions/setup-go@<PIN_SETUP_GO_SHA>   # resolve SHA for the chosen release, e.g. v5.x.y
        with:
          go-version: '1.X.Y'        # EXACT, must equal go.mod
          check-latest: false
      - run: test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }
      - run: go vet ./...
      - run: go test ./...
      - run: CGO_ENABLED=0 go build -trimpath ./...
      - run: GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build ./...
      - run: GOOS=darwin  GOARCH=arm64 CGO_ENABLED=0 go build ./...
```

Drop: the `apt-get install shellcheck jq` step, `shellcheck`, `./test/run_tests.sh`,
and the `./build.sh` smoke. Optional follow-up (separate PR): `release.yml` on
`push: tags: ['v*']` running `make release` + `softprops/action-gh-release@<SHA>`
uploading `dist/hermes-hands_*`.

### 7.4 Version injection

`-ldflags "-X main.version=<VERSION> -X main.commit=<short-sha>"`. `main`
declares `var version = "0.0.0-dev"; var commit = ""`. If `version` is still
`0.0.0-dev` at runtime, consult `runtime/debug.ReadBuildInfo()` —
`info.Main.Version` (set for `go install …@vX.Y.Z`) and the `vcs.revision` /
`vcs.modified` settings — as a fallback. Output format unchanged:
`hermes-hands <ver>` plus ` (<sha>)` only when a sha is known.

---

## 8. Milestones

One commit per milestone on `go-port`; message body ends with
`Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>`. Do not push. Do not
touch `main`. Bash sources are **kept until M8** (parity proven first).

### M0 — module skeleton + toolchain + CI swap  **(blocked on user installing Go)**

* **Scope:** `go mod init github.com/Zesales/hermes-hands`; pin `go`+`toolchain`
  (exact patch — open question); `main.go` skeleton implementing only
  `-h`/`--help` (verbatim `usage()`), `-v`/`--version`/`version`
  (`versionString()`); `embed.go` with `//go:embed share/instructions.md`;
  rewrite `Makefile` (§7.1) and `.github/workflows/ci.yml` (§7.3).
* **Files:** `go.mod`, `go.sum`, `main.go`, `embed.go`, `Makefile`,
  `.github/workflows/ci.yml`.
* **Accept:** `CGO_ENABLED=0 go build ./...` and `go vet ./...` green;
  `./dist/hermes-hands --version` → `hermes-hands 0.1.0 (<sha>)`;
  `./dist/hermes-hands --help` byte-matches the bash `usage()` heredoc.
* **Commit:** `M0: Go module skeleton, Makefile + CI swap to Go tooling`

### M1 — `config` + `redact` + `ui` + `ttyio` + `prompt`

* **Scope:** the `lib/util.sh` config half, `hh_scrub`, all of `lib/ui.sh`,
  the isatty/`/dev/tty` helpers, `hh_confirm`. No network, no shell.
* **Files:** `internal/config/*`, `internal/redact/*`, `internal/ui/*`,
  `internal/ttyio/*`, `internal/prompt/*` + `_test.go` for each.
* **Accept:** `go test ./internal/config/... ./internal/redact/... ./internal/ui/... ./internal/prompt/...`
  green; redact table exercises all 7 regexes; `RequireHTTPS` + `LooksUnset`
  tables pass.
* **Commit:** `M1: config, redaction, UI, approval-gate packages`

### M2 — `internal/api` + `internal/hermesmock`

* **Scope:** `hh_api_base`/`titleBase`, preflight, `do` (the `_hh_curl`
  equivalent), `Check`, `Ask` (POST retry/drop ladder + poll loop + server
  session-id return), `SetTitle`. httptest mock with all 5 modes.
* **Files:** `internal/api/*`, `internal/hermesmock/*` + `_test.go`.
* **Accept:** `go test ./internal/api/...` green — happy path, `401` fatal,
  `422` session-drop-retry (headers retained), capabilities `Check`,
  best-effort `SetTitle` swallows a `500`.
* **Commit:** `M2: Hermes Runs-API client + in-process mock`

### M3 — `internal/session`

* **Scope:** state-dir resolution, id minting, `New`/`Resolve`/`LatestForCwd`/
  `BumpTurn`/`SetTitleLocal`/`List`, by-cwd pointer, JSON shape + time format.
* **Files:** `internal/session/*` + `_test.go`.
* **Accept:** `go test ./internal/session/...` green — id formats, dangling
  pointer, `continue`-with-none warns + creates, explicit-missing errors,
  server-id adoption, `turns` accumulation, title-only-if-empty, list order.
* **Commit:** `M3: local session index`

### M4 — `internal/shell`

* **Scope:** persistent `bash --login` over pipes, priming + `cd`, mark
  protocol, per-command timeout, recycle-on-desync, output cap
  (`2×MaxOutput`), process-group kill.
* **Files:** `internal/shell/*` + `_test.go` (`t.Skip` without `bash`).
* **Accept:** `go test ./internal/shell/...` green — cwd/env/alias persist,
  exit-code propagation, timeout → 124 + reset text + state lost, large-output
  cap.
* **Commit:** `M4: persistent login-shell wrapper`

### M5 — `internal/dispatch`

* **Scope:** tool router with the alias sets, `cmdBlocked` (verbatim
  patterns), `resolveInRepo` jail, per-tool arg extraction + diffs + approval,
  auto-context-on-failure (+ `make` targets), `edit_file` literal replace
  (§2 #18).
* **Files:** `internal/dispatch/*` + `_test.go`.
* **Accept:** `go test ./internal/dispatch/...` green — denylist table,
  jail table, per-tool behaviour, exit codes 2/1/125/126, `ErrAbortTurn` on
  quit.
* **Commit:** `M5: tool dispatcher, repo jail, denylist`

### M6 — `internal/loop`

* **Scope:** the round loop, `extractObj`, the envelope→prose ladder, fixup
  counter, empty-object nudge, rounds cap, results assembly with `Scrub`,
  recap latch.
* **Files:** `internal/loop/*` + `_test.go` + `integration_test.go` (fixture).
* **Accept:** `go test ./...` green — all 5 mock flows plus the `extractObj`
  and fixup/recap/cap unit tables; `run_tests.sh` checks 1–5 covered.
* **Commit:** `M6: delegation loop + envelope recovery`

### M7 — `main.go` full wiring

* **Scope:** complete arg-parse table (§3 #14); REPL via `liner` (`/commands`
  + aliases, banner, prompt, `history -s`, Ctrl-C cancels turn, Ctrl-D exits);
  one-shot + stdin (`-`) + `--` rest; `setup` (writes `config` + `secrets`
  0600, dir 0700, `~/.bashrc` offer, then in-process `check`); instructions
  override ladder; SIGINT → cancel turn.
* **Files:** `main.go`, `embed.go`, `go.mod`/`go.sum` (add `liner` +
  `go-runewidth`), `internal/ui` (banner/help wired).
* **Accept:** `go test ./...` green; manual offline smoke vs the mock —
  `hermes-hands --new "hi"`, `hermes-hands -`, REPL `/new` `/sessions`
  `/check` `/exit`, `sessions`, `check`; `run_tests.sh` checks 6–10 covered
  by Go tests.
* **Commit:** `M7: main — CLI surface, REPL, setup, embedded instructions`

### M8 — drop bash; rewrite install/README/Makefile-release

* **Scope:** `git rm bin/hermes-hands lib/*.sh build.sh` (NOT
  `share/instructions.md` — it is embedded; `dist/` is untracked — §2 #1).
  Rewrite `install.sh` (§7.2), `README.md` (drop `bash`/`jq` runtime deps;
  "one static binary you can't `less` — ship checksums"; new Development /
  Releasing; keep Why / the ASCII contract / Sessions / Security model /
  Honest limitations), finish `Makefile` `release`. Mark `test/mock_hermes.py`
  manual in the README.
* **Files:** delete the bash tree; `install.sh`, `README.md`, `Makefile`.
* **Accept:** `go test ./...` green with **no bash sources present**;
  `rg -n 'lib/.*\.sh|build\.sh' .` clean; README has no `jq` runtime mention;
  `sh -n install.sh` ok.
* **Commit:** `M8: drop the bash implementation; Go is the only runtime`

### M9 — cross-compile + parity checklist

* **Scope:** verify `gofmt -l .` empty, `go vet ./...`, `go test ./...`, and
  `GOOS/GOARCH ∈ {linux,darwin,windows}×{amd64,arm64}` `CGO_ENABLED=0 go build ./...`
  all green. Walk §3's 17 items against the Go implementation; record the
  checklist in the PR body. `VERSION` stays `0.1.0` (never shipped).
* **Files:** none (or a tiny `docs/` parity note if useful).
* **Accept:** the full green matrix above; every §3 item ticked.
* **Commit:** `M9: cross-compile matrix + §3 parity checklist`

---

## 9. Constraints & risks

### 9.1 Constraints (from seed §8, plus discovered)

* **Version pinning.** `go.mod` `go 1.X.Y` + `toolchain go1.X.Y` exact; every
  `require` a full `major.minor.patch`; CI actions pinned by SHA or exact tag;
  `setup-go` `go-version:` exact and equal to `go.mod`. No dependency
  published < 48 h ago (`liner` 2022, `go-runewidth` 2018 — fine).
* **WSL network policy.** The implementer MUST NOT run the binary / `curl` /
  `ssh` against any `*.wvpk.net`, LAN IP, or real Hermes host — not even to
  "look". All testing is offline against the httptest mock. `HERMES_API_KEY`
  stays a placeholder in every fixture.
* **Branch discipline.** `go-port` only; never `main`; never `git push`.
  One commit per milestone; every commit body ends with the `Co-Authored-By`
  line.
* **Keep** `LICENSE`, `project.env`, `VERSION` (`0.1.0`),
  `share/instructions.md`, `test/mock_hermes.py`. Do not delete the bash tree
  until M8, in its own reviewable commit.
* **Out of scope:** the blog file in scratchpad
  (`remote-brain-local-hands.md`) — leave it.
* **`edit_file` semantics** (§2 #18): implement literal exactly-once replace;
  this is an **intentional** divergence from the bash regex behaviour and
  needs the user's OK (open question).
* **`config` precedence** (§2 #23): replicate the exact two-tier rule
  (config file overrides env; secrets file only when URL\|KEY empty) — not a
  naive "env always wins".
* **stdout/stderr split** (§2 #20): one-shot / stdin write only the answer +
  `\n` to stdout; everything else to stderr; exit `0`/`1`.

### 9.2 Risks

* **Real Hermes still untested** (network policy). The port preserves the
  exact wire contract so a later live test is apples-to-apples; a wrong
  assumption is the same small localized fix in Go as in bash.
* **`liner` is unmaintained** (last tag 2022). Mitigation: feature-complete
  for this use, pure Go, widely deployed, no transitive risk beyond
  `go-runewidth` (also frozen). Fallback: `golang.org/x/term`. → open question.
* **`liner` line editing is a subset of GNU readline** (no kill-ring, no
  incremental history search, no vi mode) — acceptable for a "minimal clean
  REPL"; matches the seed's stance.
* **`/dev/tty` approval under piped stdin / stdin mode** — open `/dev/tty`
  directly (`internal/ttyio.OpenControllingTTY`), exactly like `hh_confirm`;
  if it is absent, deny with a warning (bash parity).
* **Ctrl-C in the REPL** must cancel the current turn (cancel the context,
  stop the poll, kill the shell child) and return to the prompt — **not**
  exit. `HH_APPROVE_ALL`/"all" latching persists across the process, matching
  bash. Needs a `signal.Notify` handler scoped to the turn plus
  `liner.ErrPromptAborted` at the prompt.
* **`bash --login` sourcing `/etc/profile` + `~/.bashrc`** can print noise and
  be slow; discard priming output up to the first mark and keep the 15 s / 10 s
  priming timeouts. On priming failure the first real `Run` still works
  (fresh shell, best-effort).
* **RE2 vs `sed -E` with GNU `I`** — `(?i)`, `{n,}`, POSIX classes and negated
  POSIX classes are all supported; no pattern needs a backreference (only
  `\1` in a replacement → `${1}`). Behaviour matches for the 7 `hh_scrub`
  expressions; verify with the redact table.
* **Go `json` is stricter than `jq`** about trailing data — mitigate by
  running `json.Valid` on the exact first-`{`…last-`}` slice (not
  `Unmarshal` on the whole reply), matching `_hh_extract_obj`.
* **Windows** builds compile (`CGO_ENABLED=0`), and `--version` / `--help` /
  `check` / one-shot-without-`shell` work, but `shell` (persistent
  `bash --login`, process-group kill) and `/dev/tty` approval are POSIX-only
  for now — guard with `//go:build unix` files and a clear "unsupported on
  windows" error from `dispatch` for `shell`. Full Windows support is a later
  feature (matches the bash README's "use WSL").
* **`edit_file` regex→literal change** could surprise a caller that relied on
  the old `awk sub()` regex behaviour — but that behaviour is undocumented and
  a foot-gun; the documented contract is "exact substring, once". Flag in the
  M8 README changelog.

---

## 10. Open questions for the user (decide before / at M0)

1. **Which exact Go toolchain version** to install and pin (`go 1.X.Y` +
   `toolchain go1.X.Y`)? Recommendation: the latest Go **1.<current>.<patch>**
   that has been released ≥ 1 week, per the minor-version-soak policy. The
   agent cannot check current patch numbers offline.
2. **`peterh/liner` (unmaintained, 2022) vs `golang.org/x/term`** for the REPL
   editor. Plan picks `liner` for the Ctrl-C / Ctrl-D / history / password
   ergonomics. OK to take an unmaintained (but frozen, pure-Go, widely-used)
   dependency, or prefer `x/term` + hand-rolled SIGINT/prompt-redraw?
3. **`edit_file`: literal replace (recommended) vs bytewise-faithful `awk
   sub()` regex behaviour.** Plan assumes literal `strings.Replace(_, old,
   new, 1)` with `strings.Count(_, old) == 1`, dropping regex/`&`
   interpretation as a deliberate fix.
4. **`config`-file parsing scope.** Bash `source`s the file (arbitrary shell).
   The Go port narrows to `KEY=value` / `export KEY=value` with
   `"…"`/`'…'`/`$'…'` quoting and ignores anything else. Acceptable, or must an
   uncommon shell construct in someone's `config` keep working?
5. **`actions/setup-go` pin** — confirm the release (and its commit SHA) to
   pin in CI.
6. **Release workflow now or later?** Plan defers `release.yml` (tag `v*` →
   matrix upload) to a follow-up PR after M9.
