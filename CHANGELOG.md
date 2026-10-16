# Changelog

## 0.11.0 — (unreleased, `bump:minor`) session event log, live tool calls, sandbox guard, known system

One minor release for the whole `better-instructions` line. Breaking for
scripts and plugins — see **Sessions** and **REPL**.

### Tool calls in the terminal

- **Every hands call is shown before it runs** — `#14 shell  make test` — and
  again when it finishes: `#14 ✓ exit 0 · 1m12s · 38 lines` plus a short,
  deterministic excerpt of the output (first + last lines on success; the last
  lines and earlier error-looking lines on failure; `read_file` shows only its
  size). The spinner names the running call (`#14 shell running · 42s`).
  Before, a call appeared only after it finished — a long `make test` looked
  like a hang.
- `#n` is **session-wide** and Hermes sees the same number as `ref` in its
  results, so operator and brain can point at the same call. `/out <n>` prints
  a call's full output.
- The streamed answer previews under the same `hermes` label and indent as
  the final rendering.

### Two machines: the hands and Hermes' server (sandbox guard)

- hermes-hands now reads Hermes' own tool events (`tool.started`,
  `approval.request`). Hermes keeps every tool — cron, memory, web, browser,
  delegation, and its server-side exec / file tools (`terminal`,
  `process_manage` — `process` before hermes-agent v0.22 —, `execute_code`,
  `read_file`, `write_file`, `patch`, `search_files`) for server work.
- `internal/guard` decides per call whether one of those is aimed at the
  operator's machine: it names the operator's workspace or home path or a
  file of their repo, runs `make` or a repo-local `git`, or echoes a hands
  envelope. Then the run is steered back to the hands
  (`POST /v1/runs/{id}/steer`, once per run, with the reason) and the
  terminal shows `⚠ hermes-sandbox`; anything else runs and shows as a dim
  `· hermes server-sandbox` line. `HERMES_HANDS_SANDBOX` = `smart` (default)
  | `steer` (every such call) | `show` (never).
- A hands tool called as if it were Hermes' own (`tool_call` "Local Shell",
  `tool_search local_read`, …) is steered too — seen live right after the
  rename to `local_*`. The guard steers a run once per kind of mistake
  (server call aimed at the workspace / hands tool misused), not once in all.
- **Approval requests of Hermes' server tools go to the operator** — their
  own prompt (`[o]nce` / `[s]ession` / `[n]o`), separate from the hands'
  approvals, whose `[a]ll` latch never answers them. With approvals off and
  in `--rpc` they are denied instead of hanging the turn until the watchdog.
- Every SSE frame now counts as a sign of life for the silence watchdog.

### Sessions

- **New session store**: `sessions/<id>/` holds `session.json`, the
  append-only, versioned **`events.jsonl`** (messages, calls before they run,
  results, Hermes' tool attempts, answers, turn footers — interrupted and
  timed-out turns included) and `out/<n>.txt` (full scrubbed output per call).
  Live turns and reopened sessions render from the same events:
  `--session <id>` / `/session <id>` replay the last 5 turns exactly as they
  ran, `/history` all of them (`/history <n>`, `/history <id>` read-only).
  The log is built for later views (only the last answer, a local compaction,
  an export) without a second store.
- Nothing is synced from the server into the local store. When hermes-agent
  lost a session, the first resumed turn carries a recap of the local log.
  Sessions written by earlier versions are not migrated.
- **Retention**: sessions idle longer than `HERMES_HANDS_RETENTION_DAYS`
  (default 14, `0` keeps all) are deleted on start — events, outputs, debug
  trace. On the first start of this version that removes every session older
  than two weeks.
- Permissions: `sessions/` and every `<id>/` 0700, every file 0600; older
  0755 / 0644 leftovers are tightened on start. Session ids are validated
  (no path traversal through `--session ../x`).
- **Sessions are addressed by id only.** The per-directory "last session"
  pointer (`sessions/by-cwd/`) is gone — `--session` needs an id, `--rpc`
  starts a fresh session, plugins keep the id from the `ready` event.
  `/session` shows each session's idle time and, past 1 h, that the prompt
  caches have likely expired (the first resumed turn re-reads the full
  conversation); `sessions` gained an **IDLE** column.
- `--rpc`: new `call` (before a call runs) and `hermes_tool` messages, `ref` /
  `lines` on `tool`, `history` returns the session's events, `debug` toggles
  the trace. RPC turns are recorded like REPL turns.
- `--rpc` now preflights like the REPL (`/v1/capabilities`). It never did, so
  it never knew the gateway streams and always polled: no live deltas, no tool
  events, no guard — and slow (a one-word live turn: ~23 s polled, ~4 s
  streamed). Streamed, it also got the REPL's **silence watchdog** (a quiet
  gateway can't hang the editor's turn forever) and stops the server run on
  SIGINT, not only the local wait.
- `--rpc` `delta` messages carry only the answer text — they used to stream
  the raw envelope (`{"`, `calls`, `":[`, …). Both front ends now reset the
  preview per round, so the final answer of a multi-round turn streams too
  (before, a tool round muted the preview for the rest of the turn).
- The answer preview muted itself whenever a chunk ended right after
  `"calls":[` — taken for a tool round before the `]` arrived. The live
  gateway splits exactly there, so the REPL's preview practically never
  showed. It now waits for the next chunk.
- Session titles: hermes-agent rejects a title another session already has
  (`400 invalid_title`), so a second session opened with the same first
  message stayed untitled. The title is retried as `… (2)`, `… (3)`.

### Debug instead of reasoning

- **`/reasoning` is removed.** The gateway's `reasoning.available` frames
  carried the reply envelope itself, not the model's reasoning — the view only
  duplicated the answer. `HERMES_HANDS_REASONING` is gone with it.
- **`/debug`** (or `HERMES_HANDS_DEBUG=1`; rpc `{"type":"debug"}`) writes a
  connection trace into the session's `debug.jsonl`: HTTP requests (method,
  path, status, duration), run input and outcome, every raw SSE frame, guard
  actions. Every string is redacted before it is written (the API key
  literally, plus bearer / authorization / password / token / key patterns);
  headers are never traced. Local only, deleted with its session.

### Known system

- `internal/knowsystem` probes once per session start and appends a brief to
  the instructions: host (OS/arch/kernel, **WSL** recognised), the tools that
  resolve **in the hands' own `bash --login`** (a PATH entry the login profile
  adds counts — before, `go` in `~/.local/go/bin` was reported missing), a
  docker hint (a throwaway container as the isolated way to a missing tool),
  git branch, repo signals, the **`make` targets with their `## help`**, and a
  one-line **layout map** of the tracked tree.
- **Operator rules**, inlined into the instructions as binding guidance:
  your **global rules**, which `hermes-hands setup` offers to import once from
  the home directory (`~/.claude/CLAUDE.md`, `~/.codex/AGENTS.md`,
  `~/AGENTS.md`, …) into `~/hermes-hands/guidance.md` (re-import:
  `hermes-hands guidance`), then the **repo's own** `AGENTS.md` / `CLAUDE.md`
  / `.claude/CLAUDE.md` / `CLAUDE.local.md` (repo root down to cwd; the
  personal `CLAUDE.local.md` is read from disk even when gitignored), read
  fresh at every session start. Identical copies once; capped.
- Claude Code's **`@path` imports** in those files are inlined too, after the
  rules files proper (the budget goes to those first): resolved relative to
  the importing file, outside code, 5 hops deep, from inside the repo only.
- **Rules of a subdirectory** (an `AGENTS.md` / `CLAUDE.md` deeper than cwd)
  reach Hermes **with the first call into that directory**, in the result's
  `context` — looked up on disk at call time, each file once. A `local_write` /
  `local_edit` there that was composed before Hermes saw them (in an earlier
  round, not the same batch) is **held back**: nothing changes, the operator
  is not asked, Hermes sends it again. A `local_shell` call only brings them
  along (its paths are a guess: the command's words and the shell's directory
  after it ran). The instructions name these files with their scope (`git
  ls-files`, shallow first, at most 20; `known-system.json`: `more_rules`).
- **macOS**: the brief names the release (`macOS 15.1`, Apple Silicon), the
  hands' bash version (bash 3.2's caveats spelled out), the **BSD userland**
  traps (`sed -i ''`, no `grep -P`, `date -v`, `stat -f`) and the installed GNU
  variants (`gsed`, `ggrep`, …); Linux gets its distribution
  (`/etc/os-release`). The package manager is named (`brew`, `apt-get`, …).
- The tool probe used to fall back to hermes-hands' own PATH whenever the last
  probed tool was missing (its script exited 1) — so the login-shell probe
  rarely ran, and on WSL every miss walked the ~50 Windows PATH entries under
  `/mnt`. Fixed; `/mnt` entries are skipped. Probe time ~1.2 s → ~40 ms.
- A `shell` exit 127 is learned as missing for the next session; a stale
  "missing" is dropped once the tool resolves — also for tools outside the
  fixed probe list (before, those stuck forever). Record:
  `~/hermes-hands/known-system.json` (0600).

### Turn protocol

- **The hands' tools are `local_shell`, `local_read`, `local_write`,
  `local_edit`.** Hermes has server-side tools named `read_file`,
  `write_file`, `terminal`; with the hands using the same names, the model
  mixed them up (live: it ran its own `terminal` before using the hands in
  4 of 5 runs). The old names stay as aliases; events, results and the
  terminal speak `local_*`.

- **Round budget is visible to Hermes**: the task frame states the ceiling,
  the results count down over the last 5 rounds, the round before the last one
  warns that its successor is the last whose calls run, and a capped turn
  leaves a note so "continue" resumes instead of redoing work.
  `HERMES_HANDS_MAX_ROUNDS` default 8 → 25.
- `instructions.md`: results carry `ref`; independent commands go in as
  separate calls of one envelope (own exit code / output / `#ref`), `&&` only
  for dependent steps, no `| tail` on long-running commands (the `shell`
  timeout counts silence); everything executes through the hands.

### REPL

- **`/quit` and `/q` are gone** — `/exit` (Ctrl-D too) is the exit. An
  unknown `/command` is answered locally and never sent to Hermes as a task
  (a typed `/quit` used to cost a whole run). A message that starts with a
  path (`/etc/hosts …`) still goes through.

### Platforms

- **Linux (WSL included) and macOS only.** A native Windows build now fails
  on purpose with a readable error (`internal/shell/platform.go`) — on
  Windows, run hermes-hands inside WSL. The windows compile check is gone.
- **macOS secrets binding**: the key is bound to the hardware
  `IOPlatformUUID` (macOS has no `/etc/machine-id`; it used to be bound to the
  uid only). Existing blobs keep decrypting.
- `make lint` vets for linux **and** darwin (test files included).

### Build / CI

- The toolchain is pinned in the repo: every go / gofmt / vet / test / build
  runs in a throwaway `golang:1.26.7-bookworm` container (`GO_MODE=local`
  uses a host go). Only the build runs there — `make dev` / `dev-setup` /
  `dev-install` run the binary on the host (the hands need the real bash, PATH
  and machine-id); `build` cross-compiles for the host's OS/arch. Debian, not
  alpine: the shell / dispatch / loop tests need bash and silently skipped 11
  tests without it.
- CI runs the same container: `test.yml` calls `make toolchain lint test` +
  `./build.sh release`, `release.yml` `./build.sh release`;
  `setup-go` is gone, so `GO_IMAGE` (= go.mod's `go` line) is the one place
  the build's Go version is pinned.

## 0.10.0 — continuous releases; pre-public cleanup

(first public release — 0.9.x was never tagged; `MIN_VERSION` 0.9.4 + a
`bump:minor` PR lands here.)

**A bare `hermes-hands` now starts a fresh session** — no implicit resume of
this repo's latest (that reverses 0.3.0's `5d657ea`). `--session` with no id
still continues the latest explicitly; `--session <id>` opens a specific one;
`--new` is now just the same as a bare run. `--rpc` still defaults to continue
(an editor holding the process wants session continuity across restarts).

**CI split into a gate and an auto-versioning shipper.**

- `ci.yml` → **`test.yml`**: gofmt / `go vet` / `go test` + `./build.sh release`
  (the full cross-compile, minus publish) on every branch push and PR — not on
  `main`. The `test` job is the required status check for merging.
- **`pr-label.yml`**: every PR must carry **exactly one** of `bump:patch` /
  `bump:minor` / `bump:major` (`bump:none` = no version change, no release).
  Make the `bump-label` job a required check — no bump decision, no merge.
- **`release.yml`** now runs on `pull_request` → merged. **There is no
  `VERSION` file** — git tags are the source of truth. It reads the label,
  takes the highest existing `v*.*.*` tag (or `MIN_VERSION` before the first
  release), bumps it, and puts an **annotated tag `vX.Y.Z` on the merge commit**
  (message names the version + PR). It **pushes only that tag** — nothing lands
  on the `main` branch — so branch protection needs no bypass, no deploy key,
  no PAT. Then `./build.sh release` (`VERSION` comes from the tag it just cut;
  `git describe` fills it in everywhere else) + `gh release create
  --generate-notes` (auto "What's Changed" + full-changelog link), marked
  *latest*. No re-test — the merge gate is `test.yml` + `bump-label` + "require
  branches up to date". `workflow_dispatch` fires it by hand with an explicit
  level. `install.sh --version X.Y.Z` pins a release. **One-time:** Settings →
  Actions → Workflow permissions → "Read and write"; make `test` **and**
  `bump-label` required checks; create the four `bump:*` labels. No `v*` tag
  ruleset.

- **`make dev [ARGS=…]`** — run straight from source (`go run`) as a
  self-contained dev instance out of `./.dev/` (gitignored): its own
  `HERMES_HANDS_HOME`, so sessions/config never touch your real
  `~/hermes-hands/`. First run copies the machine-bound key over from there
  (it's bound to machine-id + uid, not its path); `make dev-setup` instead
  prompts for a separate key. The prompt is read **live** from
  `share/instructions.md` — edit, `make dev` again, no rebuild. `make dev-setup`,
  grouped `make help`, `make clean` wipes `./.dev/`.
- README **Install** leads with the fast `curl … | sh` (always latest, SHA-256
  verified, no clone / Go / build) + a table for `--version` / `--source` /
  `--local`.
- `ui.Answer` no longer routes through `fmt -s` when neither `glow` nor `bat`
  is installed — `fmt` mangled code blocks and lists; the raw markdown reads
  fine in a terminal.
- Removed the stale `docs/go-port-{plan,parity}.md` (the bash→Go port is done)
  and the orphaned `project.env`. Package doc comments no longer reference the
  deleted bash sources.

## 0.9.3 — drop the stale repo `config/`; rename `build.sh` verb

- **Removed `config/`** (`config.example` + `secrets.example`) — a bash-era
  leftover: it pointed at the pre-0.7.0 `~/.config/hermes-hands/` path, was
  missing the current knobs (`STREAM`, `RESPONSE_TIMEOUT`, `WATCHDOG_INTERVAL`),
  and described the plaintext secrets file that `setup` no longer writes by
  default. Nothing referenced it — `setup` templates from an inline constant.
- `./build.sh all` → **`./build.sh release`**; a bare `./build.sh` now builds
  just this machine's binary (the common case). `all` said nothing.

## 0.9.2 — automated releases

A push to `main` that changes `VERSION` now publishes a GitHub Release.

- `.github/workflows/release.yml`: re-runs the checks, cross-compiles
  `{linux,darwin}×{amd64,arm64}` via `./build.sh release`, tags
  `vX.Y.Z`, and uploads the binaries + `SHA256SUMS`. Idempotent — a tag that
  already exists is left alone, so pushing `main` without a version bump never
  re-releases. Actions pinned by commit, Go `1.26.7`, `-mod=vendor` (offline).
- `build.sh` — the one build entrypoint CI and humans share. `./build.sh all`
  = every target + checksums into `dist/`; `./build.sh host` = just this
  machine's binary.
- `install.sh` reworked: **verifies the release SHA-256** before installing;
  `--version X.Y.Z` pins a release, `--local` builds from the current checkout,
  `--source` git-clones + builds. Latest-release download stays the default.

The module path stays `github.com/Zesales/hermes-hands` (GitHub is the home;
`go install …@latest` works once it's pushed).

## 0.9.1 — small cleanup

- `StartWorking()`'s non-tty branch now calls `UI.Working()` instead of
  duplicating the static-line format string (the two had already drifted a
  hair). `InterruptedNote` / `TimeoutNote` were already gone (folded into
  `TurnDone` in 0.8.0); this removes the last of that duplication.
- Stale `"version":"0.3.0"` in the `--rpc` doc comment → `"X.Y.Z"`.

## 0.9.0 — correlation `id` on every call

Each entry in `calls` may now carry a short `id` (`c1`, `c2`, …); the hands
echo it on the matching `results` entry. Same role as OpenAI's `tool_call_id`
and split-runtime's `tool_call.request` id — with more than one call in a turn,
Hermes can now map each output to the instruction it asked for instead of
relying on array position.

- `calls[].id` is **optional**. Hermes sends it (the instructions and the frame
  now show `{"id":"c1",…}`); if it's missing or a duplicate within the batch,
  the hands assign a positional `c<N>`. `call_id` is accepted as an alias.
- `results[].id` is **always present**, echoing the (possibly synthesized) id.
- Everything else in the wire shape is unchanged (`tool`, `args`, `exit_code`,
  `output`, `context?`), so a model that ignores `id` still works by position.
- The per-turn transcript line gained the id: `shell [c1](…) -> exit 0`.

## 0.8.0 — brain ↔ hands framing; turn timer footer

**The prompt, rebuilt around a task contract.** Earlier passes told the model
"every message is about the operator's directory" and it still answered "was
kannst du?" by listing its own tools. 0.8.0 reframes the whole relationship:

- `share/instructions.md` is now "**you are the brain; hermes-hands is your
  hands**. Every turn you are handed a **problem**. Your reply is always the
  same shape: reason it through, then **one instruction** — `{"calls":[…]}` or
  `{"calls":[],"final":"…"}`. That is your only output contract: no chat, no
  self-description." Verified against a live gateway trace: "lies die todo.md"
  → clean `{"calls":[{"tool":"read_file",…}]}` on the first model call, correct
  reasoning, zero `tool_search` / `skill_view` / sandbox detour.
- `frame()` is now a thin **task wrapper** around the operator's message
  (`PROBLEM — operator's working directory: <cwd>` / the message / the response
  contract restated) instead of a rules recap. All behavioural rules live in
  the cached `instructions` field.
- The tool-results turn is re-worded so the model can't misread it: "**your
  hands ran the instruction you just gave** … real output from the operator's
  machine — the operator did NOT paste it, not from your sandbox … now answer
  in `final`." (It had been prepending "I couldn't read it directly / you
  provided the content" disclaimers.)

**Turn timer footer.** Every turn now ends with a one-line footer:
`— worked for 12s —`, `— interrupted after 8s —`, or `— timeout after 603s —
no reply from hermes-agent (HERMES_HANDS_RESPONSE_TIMEOUT=600s) —`. Timed from
the operator's message to the answer / cancel. (`UI.TurnDone` replaces the bare
`— interrupted —` / timeout notes.)

## 0.7.1 — the prompt, again: vague asks are about the project too

From a live trace: explicit file commands ("lies die readme") delegate
correctly, but an open one ("was kannst du") sent the model on a 20-round
detour through its **own** `tool_search` / `tool_describe` / `skill_view` /
`cronjob` and its `/root` sandbox `terminal` / `read_file` (even with the
operator's absolute path) — it never emitted the `calls` envelope, then
answered from nothing.

The per-turn frame and `share/instructions.md` now say it straight: **every**
message this session is about the operator's directory, *including* "what can
you do" / "what's here" / "help" — those ask what you can do with **their
project**, so the first move is a `shell` / `read_file` look, not a
description of yourself. Two explanations added (not prohibitions, per the
"use your own tools, just define the setup" call): the operator's absolute
paths don't resolve in your container either; `tool_search` / `skill_view` /
`cronjob` describe *you*, not their project. New "was kannst du?" example
(`ls` + `cat README*` + `git log`).

## 0.7.0 — one self-contained directory (`~/hermes-hands/`)

The scattered XDG layout is gone. Everything hermes-hands reads and writes now
lives in **one directory**, `HERMES_HANDS_HOME` (default `~/hermes-hands/`):

```
~/hermes-hands/
  config
  secrets.enc   keyseed        (machine-bound store; setup default)
  secrets                       (only with setup --plaintext)
  instructions.md               (optional per-repo override)
  sessions/                     (local session index — was ~/.local/state/…)
```

- `HERMES_HANDS_HOME` roots all four paths; the existing per-path overrides
  (`HERMES_HANDS_CONFIG` / `_SECRETS` / `_STATE` / `_INSTRUCTIONS`) still win
  over the derived default. `XDG_CONFIG_HOME` / `XDG_STATE_HOME` are no longer
  consulted.
- **`/config` now reports the real secrets source** — `secrets.enc`
  (encrypted, machine-bound) / plaintext `secrets` / environment — instead of
  always printing the plaintext path. New `home` and `sessions` lines; a
  `Config.SecretsSource` field backs it.
- `setup` writes into `~/hermes-hands/` directly (flat, no `hermes-hands/`
  sub-path); `setup --plaintext`'s `~/.bashrc` line points at
  `${HERMES_HANDS_HOME:-$HOME/hermes-hands}/secrets`.

**Migrating an existing install:** move `config`, `secrets.enc`, `keyseed` from
`~/.config/hermes-hands/` and `sessions/` from `~/.local/state/hermes-hands/`
into `~/hermes-hands/`, then delete any stale plaintext
`~/.config/hermes-hands/secrets` and its `~/.bashrc` source line. The encrypted
store is bound to `keyseed` + machine-id + uid, **not** its path — moving the
files does not break decryption.

## 0.6.5 — turn timer, silence watchdog, read-only `/config`

- **The `⋯ working` line now shows elapsed seconds** (`working · 47s · Ctrl+C to
  cancel`), reset at the start of every turn — so the dead air between tool
  rounds while Hermes thinks is visibly counting, not frozen.
- **Silence watchdog.** A turn is cancelled if hermes-agent goes quiet for
  longer than `HERMES_HANDS_RESPONSE_TIMEOUT` (default **600s**, fine for local
  LLMs; `0` disables). It is *not* a hard wall: every sign of life — a streamed
  token, a finished tool round, a fresh sub-run — pushes the deadline back, and
  in the meantime a side check every `HERMES_HANDS_WATCHDOG_INTERVAL` (default
  **200s**) probes `GET /v1/runs/{id}`; while it still reports the run running,
  the deadline keeps resetting. Only a genuine stall (no tokens, and the probe
  can't confirm the run is alive) trips it, printing `— timeout: no reply from
  hermes-agent in 600s — turn cancelled —` and stopping the server run. This
  closes the hole where a stalled SSE stream hung the REPL forever with only
  Ctrl-C to break out.
- **`/config` (alias `/hh-settings`)** — prints the config-file / secrets /
  instructions / state paths and every hand-tunable knob's effective value with
  its env-var name. **Read-only on purpose**: hermes-hands never writes settings
  back — you edit the file. The API key is never shown.
- Both new knobs are read from the config file (like `HERMES_HANDS_APPROVE`),
  not env-only; `setup` writes them as commented examples.

## 0.6.4 — spinner vs. the approval prompt

The animated line from 0.6.3 was repainting over the `[y]es [n]o [a]ll [q]uit`
prompt, so a `write_file` / `shell` approval looked frozen. The approval gate
now freezes the spinner (`UI.Hold` via a new `TTYApprover.Pause` hook) while the
prompt is up and un-freezes it after you answer.

## 0.6.3 — animated "working" line

`⋯ working` is now an animated line (`.` → `..` → `...` pulsing, `· Ctrl+C to
cancel` alongside) that runs from your message until the answer starts — so
during the dead air between tool rounds (Hermes thinking for 30s+) it's obvious
the turn is still alive. Tool lines and the streamed answer clear it and print
above/over it cleanly; the first streamed chunk retires it. Non-tty keeps the
old static line.

## 0.6.2 — the prompt, rewritten for a fully-tooled Hermes

The live gateway's brain has its **own** `terminal` / `read_file` / `write_file`
/ `execute_code` tools (running in its `/root` sandbox). The old instructions
just said "don't use your tools", and the model ignored that — it ran
`read_file` / `terminal` in the sandbox, found nothing, and answered "the file
doesn't exist". Rewritten so the model *understands the setup* instead of being
forbidden things:

- `share/instructions.md` and the per-turn frame now: you are Hermes, reached
  **remotely**; the operator is at a Linux terminal, the worker holds a
  persistent **bash shell** in a directory (Linux-only for now); every request
  is about that directory; **for anything touching it, reply with the `calls`
  envelope** — your own `terminal`/`read_file`/… run in your sandbox (`/root`),
  not on the operator's machine, and will mislead you; keep using your memory /
  web / reasoning normally. Names in `calls` are the *worker's* operations, not
  your tools; don't route them through `tool_call`.
- Tool results going back now carry a one-line reminder: this is what actually
  ran in the operator's shell — empty / failing output means *try another
  command*, not "I can't access it" / "it's only on your system".
- Verified live: "what's in the readme?" and "list the service dirs" both
  now delegate correctly (`read_file` / `shell` executed locally, answered from
  the results) — no sandbox loop.
- Hidden `_run '<json>'` inspector (POST a raw /v1/runs body, stream events).

## 0.6.1 — drop `/skills`

Removed the `/skills` command (and `Client.Skills`). It was read-only so no
boundary issue, but a coding CLI listing the brain's ~100 skills is just noise —
the brain uses them on its own. `run_steer` (mid-turn steering) was considered
and left out: it needs async input during a turn, and in practice you either
Ctrl-C and re-ask or recall + edit the last message (liner history), so the
payoff is marginal.

## 0.6.0 — SSE streaming + more API (verified against a live gateway)

The remaining API sweep, checked against a real Hermes (the SSE event shape and
every response schema below are the live ones, not guesses).

- **SSE answer streaming.** `Ask` now reads `GET /v1/runs/{id}/events` when the
  gateway advertises `run_events_sse` (`HERMES_HANDS_STREAM` = `auto` default /
  `on` / `off`). The REPL previews the answer *text* live as it generates — a
  small extractor pulls the `final` string out of the streaming envelope, so
  you never see raw JSON, and tool-call rounds stream nothing. `run.completed`
  carries `output` + `usage`, so no follow-up poll. Any stream surprise falls
  back to polling — the poll is always authoritative. `--rpc` emits
  `{"type":"delta","text":…}` frames.
- **`/session` shows real server numbers** from `GET /api/sessions/{id}`
  (`{"session":{…}}`): message count, context tokens (`input_tokens +
  cache_read_tokens`), model, `parent_session_id`, ended.
- **`/fork`** — `POST /api/sessions/{id}/fork`: branch this session on the
  server and switch to the branch.
- **`/compact` is honest now** — this gateway exposes no REST compaction
  endpoint (`/compress` is an internal chat command); the command says so.
- Hidden dev inspectors: `hermes-hands _raw <path>` (GET a path, print the
  body) and `_events "<prompt>"` (start a run, dump its raw SSE stream).

## 0.5.0 — more of the Hermes API

Tier 1 of the API sweep — the safe, testable parts. SSE streaming of the answer
(`GET /v1/runs/{id}/events`) is the remaining Tier 1 item and lands separately.

- **`POST /v1/runs/{id}/stop` on Ctrl-C.** Cancelling a turn now also stops the
  run on Hermes, so the server isn't left burning tokens after you've moved on.
  (`api.Client.OnRunStart` records the in-flight run id.)
- **`GET /api/sessions/{id}` in `/session`.** The detail view now also shows
  server-side numbers when available: message count, context tokens, the
  `parent_session_id` (pre-compaction lineage), model, ended flag. Lenient
  parsing — the `/api/sessions` response schema isn't published, so missing
  fields are just skipped.
- **`GET /api/sessions` in `/sessions` and `--session-list`.** Alongside the
  local index, the real hermes-agent session list (id / message count / tokens /
  title, with `(from <parent>)` on compaction continuations).
- **`/v1/capabilities` is parsed** into `client.Caps` (feature flags) so later
  features can gate on what a given Hermes build actually supports.

## 0.4.2 — approval-prompt hang fixed

- **Fixed a hard hang**: at a `[y]es [n]o [a]ll [q]uit` approval prompt you
  could not type and Ctrl-C did nothing — the gate opened a second reader on
  `/dev/tty` while the REPL's line editor still owned the terminal, so
  keystrokes went nowhere and the blocking read never returned. The gate now
  reads its answer through the same line editor. Ctrl-C there = decline this
  call **and** stop the turn.
- On a cancelled turn, the next message to Hermes is prefixed with a note that
  the previous turn was cancelled and nothing ran — so its transcript stays
  coherent (Hermes is not told automatically otherwise).
- Per-turn frame + `instructions.md` tightened: delegation is **only** for the
  operator's repo. Questions about the operator, Hermes' own memory, or past
  conversation are answered from context — Hermes no longer tries to
  `read_file` its own `~/.hermes/memory.md` (which the repo jail rejects).

## 0.4.1 — `/compact`; drop `-c`

- **`/compact [focus]`** (in-REPL; alias `/compress`) and an rpc `compact`
  request — asks Hermes to compact the current session's context now via
  `POST {base}/api/session/compress` (the app-server's `/compress`; `/compact`
  is a legacy alias upstream). Optional `focus` = "compress around this topic".
  The next turn threads into the compacted continuation session. Best-effort:
  the exact request shape isn't fully documented, so a non-2xx is reported with
  its status.
- **`-c` / `--continue` removed** — redundant with a bare run / `--session`.

## 0.4.0 — no one-shot; explicit sessions; session detail

- **One-shot removed.** `hermes-hands "message"` and `… | hermes-hands -` are
  gone. A session is one task — continue it or start a new one. Scripts /
  editors use `--rpc` (send a `turn`, read the `answer`, close stdin).
- **`--session` takes an optional id.** `--session` alone (and a bare
  `hermes-hands`) continues this repo's latest; `--session <id>` opens a
  specific one; `--new` starts a fresh one. `-c` / `--continue` now alias
  `--session`. `--session-list` = `sessions` (list).
- **Session detail.** `--session-list` shows a `TOKENS` column; the in-REPL
  `/session` (no id) prints the current session's local id, the hermes-agent
  session id, turns, **splits** (times Hermes handed back a new session id ≈
  compactions), and the last run's token count. Real context-window usage and a
  compaction count are **not in the Hermes API yet**
  ([NousResearch/hermes-agent#15618](https://github.com/NousResearch/hermes-agent/issues/15618));
  "tokens" is the last run's cumulative billing `usage`, "splits" is the
  observable proxy.

## 0.3.0 — session-first, plugin surface

The theme: **always work on a session, never fragment the central Hermes** into
throwaway sessions, and make the CLI something an editor/plugin can drive.

- **`--rpc`** — a persistent JSON-lines session server. The caller holds one
  session for the process lifetime and only starts a new one on demand
  (`{"type":"new"}` / `{"type":"use","session":…}`); turns stream `tool`
  progress events and an `answer` event. See `README.md` → *Plugin / editor
  integration*. Requires `--yolo` / `HERMES_HANDS_APPROVE` non-interactive
  (the editor runs its own approval UI).
- **`hermes-hands sessions new`** — mint a session id and print it, for
  `--session <id>` / `--rpc` reuse across process restarts.
- **Every mode resumes by default.** Bare `hermes-hands`, `hermes-hands
  "message"` and `… | hermes-hands -` all attach to this repo's existing
  session instead of creating one per invocation. `--new` forces fresh;
  `--session <id>` pins one. `-c` / `--continue` are now no-ops (kept so old
  muscle memory doesn't error).
- **In-session `/commands`** — added `/setup` (configure without leaving the
  REPL — starts even when unconfigured), `/session <id>` (switch), `/yolo`
  (toggle approvals). `/help` reworded; `/sessions` gained a DIR column and
  drops the cwd-as-title fallback.
- Per-turn frame tells Hermes it is remote and names the cwd + branch, so it
  reaches for `read_file` / `shell` instead of "I can't see the repo".

## 0.2.0 — interactive polish

- REPL prompt `hermes-hands - session <id> > `; banner shows the hermes-agent
  session id (`(pending)` until the first turn) so a divergence is visible.
- Fixed: the REPL exited immediately on a colour terminal (ANSI in the liner
  prompt → `ErrInvalidPrompt`).
- Ctrl-C at the prompt clears the line and hints `/exit`; during a turn it
  cancels the turn. The working line shows `Ctrl+C to cancel`.
- Not-configured now prints what is missing and exits (no auto-wizard); a
  placeholder URL/key counts as unset so a stale `~/.bashrc` export doesn't
  wedge `setup`.
- `edit_file` does a literal replace (dropped the `awk sub()` regex foot-gun);
  machine-bound at-rest secrets store (`secrets.enc` + `keyseed`, AES-256-GCM
  + HKDF over `/etc/machine-id` + uid).

## 0.1.0 — bash → Go

Full rewrite of the CLI from the original bash implementation to Go: one static
binary (`CGO_ENABLED=0`), no `bash` / `curl` / `jq` runtime dependency, deps
vendored, real `encoding/json`. Same wire contract and behaviour as the bash
version (`docs/go-port-parity.md`). Still the outbound bridge over the merged
Hermes Runs API until split-runtime (#63966) lands.
