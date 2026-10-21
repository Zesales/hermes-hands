# hermes-hands

[![release](https://img.shields.io/github/v/release/Zesales/hermes-hands)](https://github.com/Zesales/hermes-hands/releases/latest)
[![tests](https://github.com/Zesales/hermes-hands/actions/workflows/test.yml/badge.svg?event=pull_request)](https://github.com/Zesales/hermes-hands/actions/workflows/test.yml)
[![hermes-agent](https://img.shields.io/badge/hermes--agent-0.21%2B%20%C2%B7%20tested%200.22-blue)](#works-with)
[![go](https://img.shields.io/github/go-mod/go-version/Zesales/hermes-hands)](go.mod)
[![license](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

**Terminal coding with a remote [Hermes Agent](https://github.com/NousResearch/hermes-agent)
brain.** Your Hermes gateway (NousResearch's hermes-agent) holds the plan, the
memory and the persona; a small Go CLI on your machine gives it hands — a
persistent shell plus file read / write / edit in the repo you're standing in —
over Hermes' own HTTP API, outbound only, nothing listening on your box.

```
you type ──▶ POST /v1/runs ──────────────▶ Hermes  (plans, decides, remembers)
             GET  /v1/runs/{id} ◀── {"calls":[{"tool":"local_read",…}]} ── Hermes
             │
             └─ runs the calls in your repo (with your approval for run/write),
                feeds {"results":[…]} back, loops, until Hermes answers.
```

```sh
curl -fsSL https://raw.githubusercontent.com/Zesales/hermes-hands/main/install.sh | sh
hermes-hands setup            # your gateway URL + API key, stored machine-bound
cd ~/projects/myrepo && hermes-hands
```

The design behind it — one brain on a server, hands on your machine that carry
no model: [Remote Brain, Local Hands](docs/remote-brain-local-hands.md).

> **Status.** A native "remote brain, local hands" path for Hermes is still
> being decided upstream —
> [issue #18715](https://github.com/NousResearch/hermes-agent/issues/18715) /
> [PR #63966](https://github.com/NousResearch/hermes-agent/pull/63966), open.
> `hermes-hands` **works well today** on the merged Runs API. Once Hermes picks
> a mechanism (and here's hoping it's a clean one), hermes-hands moves to it for
> a tighter local↔remote loop and this bridge becomes the fallback — the local
> dispatcher doesn't change either way.

<a id="works-with"></a>
> **Works with hermes-agent 0.21.0 and newer** — tested through **0.22.x**.
> Releases before 0.21.0 are not supported and not recommended: hermes-hands
> was never tested against their Runs API and session handling. Newer releases
> should work until hermes-agent changes that API — its session rework, due
> with 0.23, touches `/v1/runs`; the release that adapts to it will name its
> range. hermes-hands keeps its own version line; every
> [CHANGELOG](CHANGELOG.md) release names the hermes-agent range it was tested
> for, and `hermes-hands check` prints the version your gateway runs (with a
> warning outside the range; a session start warns too).

## Why

Running Hermes as your one always-on assistant is great until you want it to
touch code on your dev machine. The clean answer — Hermes making an ordinary tool
call that happens to execute locally — is
[split-runtime](https://github.com/NousResearch/hermes-agent/pull/63966), which
isn't merged. The alternatives are worse: an inbound MCP listener on your laptop,
or the desktop app (which doesn't split execution at all).

`hermes-hands` is the **outbound bridge** until split-runtime lands. It uses only
the merged Runs API. Hermes replies with a small JSON envelope of tool calls;
the CLI runs them and feeds structured results back. The envelope shape is the
same one split-runtime expects, so when it merges the CLI switches to native tool
calls and this convention becomes the fallback — **the local tool dispatcher does
not change.**

## Install

**Requirements**

- **OS:** Linux (WSL included) or macOS, `x86-64` or `arm64`. There is no
  native Windows build — on Windows, run it **inside WSL** (the Linux binary):
  native Windows has no `bash`, no `/dev/tty`, no process groups, and WSL is
  the better place to develop anyway. On macOS the hands run whatever `bash`
  is first on `PATH` (the system's 3.2, or Homebrew's 5.x) and Hermes is told
  about the BSD userland (see below).
- **`bash`** on `PATH` — `local_shell` drives a persistent `bash --login`.
  Without it only `local_read` / `local_write` / `local_edit` work.
- Nothing else to *run* the binary: one static executable, no `curl` / `jq`, no
  language runtime, no package manager. (`git` is used only for `git status`
  context on a failed command; `glow` / `bat` / `diff` prettify output when
  present.)

**Fast install — recommended.** No clone, no Go, no build:

```sh
curl -fsSL https://raw.githubusercontent.com/Zesales/hermes-hands/main/install.sh | sh
hermes-hands setup      # asks for your Hermes API URL + key
```

With no arguments this always grabs the **latest** GitHub release for your
OS/CPU, **verifies its SHA-256** against the release's `SHA256SUMS`, and drops the
single binary at `~/.local/bin/hermes-hands`. Re-run the same line any time to
update — that's the whole update mechanism.

Other modes (same script):

| command | does |
|---|---|
| `… \| sh` | latest release, SHA-256 verified *(the default)* |
| `… \| sh -s -- --version 0.11.0` | pin to release `v0.11.0` |
| `… \| sh -s -- --source` | git-clone + build from source (needs Go + git) |
| run in a checkout: `sh install.sh --local` | build the current checkout, install that |

(`--source` is also the automatic fallback when no release exists yet for your
platform.)

`setup` writes into one self-contained directory, `~/hermes-hands/`
(`$HERMES_HANDS_HOME` overrides): `config` + the machine-bound secrets store
(`secrets.enc` + `keyseed`, `0600`). A fresh `config` sets every setting at
its default; re-running `setup` (a new key, a new machine) never changes a
value you set — it only appends settings a newer version brings.
`hermes-hands setup --config` does just that config step, without prompts.
The key is your gateway's `API_SERVER_KEY`;
the URL must be `https://…`. `hermes-hands --version` prints the version.

## Use

**A session is one task.** There is no one-shot and no implicit resume: a bare
run starts a fresh session. Resume an old one only by its id (`--session <id>`,
`/session <id>`, or an rpc `use`) — exactly how editor plugins work: they keep
the id from the `ready` event. A session idle for over an hour is flagged in
`/session`: the gateway/model prompt caches (a model gateway, hermes-agent,
Claude-style ~1h TTL) have likely expired, so its first resumed turn re-reads
the full transcript and can cost full-price **input tokens** — an information
hint, not a hard expiry. Opening a session replays its last turns in the
terminal exactly as they ran (see [Sessions](#sessions)).

```sh
hermes-hands                       # start a fresh session here (main use)
hermes-hands --session <id>        # open a specific session  (id from --session-list)
hermes-hands --new                 # start a fresh session  (same as a bare run)
hermes-hands --rpc                 # JSON-lines session server for an editor/plugin

hermes-hands --session-list        # list this machine's sessions: id / turns / tokens / idle / dir / title
hermes-hands --session-list-verbose  # … each with its hermes-agent session under it (local vs. server)
hermes-hands sessions new          # mint a session id (prints it; for --session / --rpc)
hermes-hands check                 # preflight the connection (names hermes-agent's version)
```

Scripts drive `--rpc` (send one `turn`, read the `answer`, close stdin) rather
than a per-call one-shot.

**Scheduled jobs on your machine.** A cron job set up *in Hermes* runs on the
server, without the hands — it can't touch your repo. For a job on your
machine, turn it around: your own cron / systemd timer starts hermes-hands,
and Hermes works through the hands as usual.

```sh
# crontab -e — weekdays 07:00: what is new upstream, in the same session every day
0 7 * * 1-5  cd "$HOME/projects/myrepo" && printf '%s\n' \
  '{"type":"use","session":"hh_20261014T070000_abc123"}' \
  '{"type":"turn","text":"git fetch, then summarize what is new on origin/main"}' \
  | HERMES_HANDS_APPROVE=auto HERMES_HANDS_DENY='*git push*|*rm -r*' \
    "$HOME/.local/bin/hermes-hands" --rpc >> "$HOME/hermes-hands/cron.jsonl" 2>&1
```

Unattended means `HERMES_HANDS_APPROVE=auto` — fence it with
`HERMES_HANDS_DENY` (each glob is matched against the whole command line,
hence the leading `*`). Approval requests of Hermes' own server tools are always
denied in `--rpc`. The answer is the `{"type":"answer",…}` line; the turn is
in the session's event log like any other (`hermes-hands --session <id>`
replays it). Without the `use` line every run starts a fresh session.

REPL commands: `/help` `/new` `/session` (this session's detail — local + server
numbers) `/session <id>` (switch; replays its last turns) `/sessions`
(`/sessions -v`: with their hermes-agent sessions)
`/history` (replay every turn of this session; `/history <n>` the last n,
`/history <id>` another session's — read-only) `/out <n>` (the full output of
call `#n`) `/compact` (compact this session's chat now — see
[Context and compaction](#context-and-compaction)) `/compact-aggressive`
(when `/compact` was not enough) `/context` (what Hermes
reads now; `/context full` every message) `/fork` (branch on the
server) `/debug` (toggle the redacted
connection trace, see below) `/setup` `/approve` (the approvals for this
session: no argument steps `ask` → `plan` → `auto`, `/approve <mode>` sets
one; in `plan` Hermes looks and answers with a plan — reads run, shell
commands ask, nothing is written) `/yolo` `/check` `/exit` (`Ctrl-D` also
exits; `Ctrl-C` at the prompt just hints, during a turn it cancels the turn
locally *and* stops the run on Hermes). An unknown `/command` is answered
locally, never sent to Hermes. `/setup` configures without leaving the session
and the REPL starts even when unconfigured. When the gateway supports SSE the
answer text previews live as it generates (`HERMES_HANDS_STREAM` =
`auto`|`on`|`off`); the final answer renders through `glow`/`bat` if installed.

**Type while Hermes works**, as in Claude Code: what you type stands on the
working line (`you› …`), Enter queues it — `queued for the next round: …` —
and it goes out with the turn's next round, after the results, as `you
(added)`. What the turn did not take — it ended first — becomes the next
turn; after Ctrl-C or a timeout it waits in the prompt instead, with the line
you were still typing, to send or change. A `/command` typed meanwhile runs
when the turn is done. Backspace, Ctrl-W, Ctrl-U edit; an approval prompt
takes the keys while it is up; Ctrl-C still cancels the turn.

**Attach a file with `@path`**, as in Claude Code: `@shot.png`,
`@"path with spaces"`, `@~/notes.txt` in a message — or an image dropped into
the terminal, which pastes its path. Read-only, and also from outside the
directory: you named it. An image (png, jpeg, gif, webp, up to 5 MB) goes
along as an image in the turn's first run — the model behind Hermes needs
vision; hermes-agent keeps only `[screenshot]` of it in the session. A text
file goes along as its content (up to 100 KB, secrets scrubbed). A name that
is no file stays text; each attachment shows as a note.

**Questions get answers.** Hermes answers a question, an opinion or a
discussion straight away, as in a chat, and looks on your machine only when
the answer depends on it; a task — change, fix, build, run — it works
through the hands.

Every hands call is shown **before it runs** — its session-wide number, the
tool and what it runs — and again when it finishes, with a short excerpt of
its output (the first and last lines on success; the last lines plus earlier
error-looking lines on failure; `local_read` shows only the size). The full
output stays available as `/out <n>`, and Hermes sees the same `#n` (as `ref`)
in its results, so both of you can point at a call:

```
 #14 shell  make test
 #14 ✓ exit 0 · 1m12s · 38 lines
     │ ok   …/internal/loop   0.036s
     │ … 35 lines — /out 14
     │ ok   …/internal/ui     1.914s
```

**Your machine does the work.** Hermes keeps its own thinking-side tools —
cron, memory, web, skills, browser. Its server-side `terminal` /
`read_file` / `execute_code` — and `delegate_task`, whose sub-agents work
there too — are another matter: a model that meets a
missing tool on your machine tends to rebuild your project in its own
sandbox — install the toolchain there, clone your code there, look around
the server's containers. hermes-hands watches the run's tool events and, by
default, steers **every** server-side exec / file / delegation call back to the hands
(`POST /v1/runs/{id}/steer`); a `⚠ hermes-sandbox` line says so. The steer
tells Hermes where the work belongs and what to do about a missing tool:
the project's own way (`make`, docker, scripts) — or a final that names
what is missing and the exact command for **you** to install it (the hands
refuse `sudo`). A steer redirects the run; the call it answers has started
already. `HERMES_HANDS_SANDBOX` = `steer` (the default) | `smart` (steer
only calls meant for your project — your paths or files, `make`, a
repo-local `git`, a hands envelope — or environment work: installing a
toolchain or dependencies, `curl … | sh`, `git clone`, exploring the
server's containers or network; other server work shows as a dim
`· hermes server-sandbox …` line and runs) | `show` (never steer).
`steer` and `smart` also steer a **placeholder** call of any of Hermes'
tools — `tool_call` "Noop Placeholder", a `delegate_task` of "noop check",
`echo placeholder` — a call made only to have called something, typically
right after a steer or to "check the connection". The hands are connected
whenever Hermes runs; its first real call in the envelope is the check.
Web, memory, skills and cron for real work are never steered. A run is
steered up to five times per kind of mistake. Past that — or at once when
the server refuses a steer, which then never reached the model —
hermes-hands **stops the run** (`↳ run stopped — hermes-hands asks once
more, with a correction`) and sends the same message again with the
correction in front: at most twice a turn, then the turn ends `BLOCKED` and
`continue` starts over. Without that a model can loop on its mistake for many minutes
until hermes-agent's own retry guard gives up.

When one of Hermes' server-side tools asks for **approval**, you decide —
in its own prompt, separate from the hands' approvals (their `[a]ll` never
answers it): `[o]nce`, `[s]ession` (for this Hermes session) or `[n]o`.
With approvals off (`--yolo`, `never`) or in `--rpc`, server approvals are
denied — a server command nobody looked at never runs.

Hermes drives these tools, all in the directory you launched from:

| tool | args | approval |
|---|---|---|
| `local_shell` | `{cmd, timeout?}` | **each command** (unless `--yolo`) |
| `local_read` | `{path}` | — (confined to the repo root) |
| `local_write` | `{path, content}` | **with a diff** |
| `local_edit` | `{path, old, new}` | **with a diff** (`old` must match once) |
| `local_output` | `{ref, offset?, limit?}` | — (reads the hands' own copy of call `#ref`'s output; nothing runs) |

The `local_*` names keep them apart from Hermes' own server-side `read_file` /
`write_file` / `terminal`; the old names (`shell`, `read_file`, `write_file`,
`edit_file`) still work as aliases.

`local_shell` is a **persistent login bash** rooted at your repo — `cd`, exported
vars, and shell functions/aliases from your `~/.bashrc` survive between calls in
a session, so Hermes operates it like a real terminal (`ls`, `rg`, `git`,
`make`, build/test) rather than a fixed toolbox. No tty: interactive programs
won't work. `local_read`/`local_write`/`local_edit` are structured helpers so the
model gets a clean diff and needn't fight shell quoting. `local_edit` replaces one
**literal** occurrence of `old` (no regex, no `&` metacharacters — a deliberate
change from the old bash version). `local_write`/`local_edit` need the target's
parent directory to already exist.

The hands also tell Hermes **what your machine has**: each session start probes
once and appends a compact brief to the instructions Hermes receives — the
brain plans against known facts instead of burning tool rounds rediscovering
them.

- **The host:** the OS release (`Ubuntu 24.04 LTS`, `macOS 15.1`), arch
  (Apple Silicon named), kernel, WSL recognised; the `bash` version the hands
  run (bash 3.2 on macOS gets its caveats spelled out: no `mapfile`, no
  associative arrays, …); on macOS the **BSD userland** traps (`sed -i ''`, no
  `grep -P`, `date -v`, `stat -f`) and which GNU variants (`gsed`, `ggrep`, …)
  are installed; the package manager (`brew`, `apt-get`, `dnf`, …); and which
  tools resolve — probed in the same `bash --login` `local_shell` runs, so a
  PATH entry your login profile adds counts. With docker present, Hermes is
  told a throwaway `docker run --rm` container is the isolated way to a tool
  the host lacks.
- **The repo** you stand in: git branch, manifests (Makefile, compose, go.mod,
  …), the `make` targets **with their `## help` text**, a one-line layout map
  of the tracked tree, and the repo's own rules — read fresh at every session
  start, since every repo has its own:
  - **inlined:** `AGENTS.md`, `CLAUDE.md` (or `.claude/CLAUDE.md`) and your
    personal, usually gitignored `CLAUDE.local.md`, from the repo root down to
    where you stand — plus the files they import with Claude Code's `@path`
    syntax (from inside the repo, 5 hops deep, like Claude Code);
  - **handed out on first contact:** the rules files deeper in the tree. The
    instructions only name them with their scope ("`services/api/AGENTS.md` —
    governs `services/api/`"); the hands attach one to the result of the first
    call into its directory, the way Claude Code loads a nested `CLAUDE.md`.
    A `local_write` / `local_edit` there that Hermes wrote before it saw those
    rules is **held back** (nothing changes, you are not asked) — Hermes gets
    the rules and sends the call again. A `local_shell` command only brings the
    rules along (which paths it touches is a guess: its words, and where a
    `cd` left the shell). `vendor/` / `node_modules/` are skipped.
- **Your global rules:** `hermes-hands setup` offers to import the agent rules
  in your home directory (`~/.claude/CLAUDE.md`, `~/.codex/AGENTS.md`,
  `~/AGENTS.md`, …) once into `~/hermes-hands/guidance.md` — stored knowledge
  every session puts in front of the repo's rules. It is a snapshot: edit it
  freely, re-import with `hermes-hands guidance`.

All rules are inlined as binding guidance (identical copies once, capped). The
record (without the rules' text) is `~/hermes-hands/known-system.json` (0600,
written by hermes-hands only). A `local_shell` result with exit 127 records the tool
as missing for the next session's brief; a stale "missing" is dropped the
moment the tool resolves.

## Sessions

The conversation is threaded **server-side**: every `POST /v1/runs` carries a
stable `session_id` (Hermes loads that session's transcript as context), plus an
`X-Hermes-Session-Key` that's constant per repo (long-term memory handle) and an
`X-Hermes-Session-Id` header. If Hermes hands back a different `session_id`, the
CLI adopts it. If the server ever rejects the id, the CLI retries fresh (keeping
the headers) and, when Hermes lost the session, opens the first turn with a
recap of the local history.

The local session store lives in `~/hermes-hands/sessions/`
(`$HERMES_HANDS_STATE` overrides), one directory per session, 0700 / files
0600:

| file | what |
|---|---|
| `<id>/session.json` | the index record — `--session <id>` and `sessions` work offline |
| `<id>/events.jsonl` | everything the terminal showed, as an append-only, versioned event log: your messages, every hands call (before it runs) and result, the approval prompts you answered, Hermes' own tool attempts, answers, turn footers — plus Hermes' replies and the compactions, which the transcript of a compacted session is built from (without Hermes' own tool attempts: they would be examples of the mistake in its history; an output it has verbatim already goes once, a repeat as `[same output as #n …]`) |
| `<id>/out/<n>.txt` | the full (scrubbed) output of call `#n` |
| `<id>/debug.jsonl` | the `/debug` trace, only while it is on |
| `<id>/chat-checkpoints/NNNN.json` | what Hermes reads from compaction N on — see [Context and compaction](#context-and-compaction) |

Live turns and reopened sessions are rendered from the **same** events, so
`--session <id>` / `/session <id>` show the last 5 turns exactly as they ran
(`/history` all of them). Nothing is synced from the server into this store —
it is your local record; the conversation the model sees stays on Hermes.
Text is scrubbed (`internal/redact`) before it is stored. Sessions idle longer
than `HERMES_HANDS_RETENTION_DAYS` (default **14**) are deleted on start —
events, outputs and debug trace with them — so anything typed by mistake
doesn't linger. Sessions are addressed by id only. Titles are also mirrored
into Hermes via a best-effort `PATCH /api/sessions/{id}`.

`/debug` (or `HERMES_HANDS_DEBUG=1`) traces the connection into the session's
`debug.jsonl`: every HTTP request (method, path, status, duration), every run
input and outcome, every raw SSE frame, every guard action. Every string is
redacted before it is written — the API key literally, plus the
bearer / authorization / password / token / key patterns; headers are never
traced. A bare password typed into a message matches no pattern: that is what
the retention window bounds. The trace never leaves the machine.

`/session` (in-REPL) and `--session-list` show per-session **turns**, the last
run's **tokens** (cumulative billing `usage`), and — `/session` — **splits**:
the number of times Hermes handed back a *different* `session_id`, which a
server-side compaction / session-split causes, so it's a rough compaction
counter. `--session-list` lists this machine's sessions only and works
offline. **`--session-list-verbose`** (`/sessions -v`) is the local-vs-server
view for debugging: under every local session its hermes-agent session —
message count, context tokens, model, last activity, the `parent_session_id`
of a compaction continuation — or that the server doesn't have it (the next
turn sends the local history along); then the hermes-hands sessions the server
still holds without a local one (retention removed them, another machine ran
them). The server's other sessions — cron jobs, other clients — are only
counted. Real context-window usage and a true compaction count are not exposed
by the Hermes API yet
([hermes-agent#15618](https://github.com/NousResearch/hermes-agent/issues/15618)),
so hermes-hands meters the context itself: `/session` shows it after the last
run (measured or estimated) and how often hermes-hands compacted the session.

## Context and compaction

A hands session fills the model's context fast: every round's results — file
contents, diffs, build and test output — stay in the conversation.
hermes-agent compacts by summarising the middle of a conversation and keeps
its latest messages whole (`compression.protect_last_n`); once those alone
outgrow the window, no summary helps and every request is refused — a
"continue" included. hermes-hands holds a full local record of the session,
every call's output among it, so it compacts on its own, without losing
anything:

- **The meter.** A run's `usage.input_tokens` is the prompt of its model
  calls; when Hermes ran none of its own server tools that is exactly the
  prompt the model saw. Between measurements hermes-hands estimates from
  bytes. The working line shows it: `round 3/25 · calls 12/100 · ctx 48k/131k`
  (rpc: `context` / `window` on `progress`).
- **Compaction before the request, never after it failed.** Before every
  request hermes-hands predicts its prompt; past `HERMES_HANDS_COMPACT_AT` %
  of `HERMES_HANDS_CONTEXT_WINDOW` it compacts first. Outputs are elided as
  far as needed — those of all but the latest two rounds, then of every
  round — which loses nothing. Turns are collapsed to question + answer when
  the outputs alone do not bring the prompt down to half the trigger (never
  the latest turn) — deep rather than often: every compaction rewrites the
  prompt's prefix, which the server's prompt cache then misses. A collapsed
  turn still names the files it edited and read. `/compact` (rpc `compact`) elides
  every output so far now; **`/compact-aggressive`** (rpc `compact` with
  `"text":"aggressive"`) also collapses every earlier turn — for when
  `/compact` was not enough. `/compact` takes no argument, so a mistyped
  `/compact agressive` does nothing.
- **Nothing is thrown away.** An elided round's results go as one line per
  call — `#14 local_shell `go test ./...` → exit 1 · 312 lines · 18 KB ·`
  and the output's error or last line, picked by the same rule as the
  terminal's excerpt — its reasoning clipped, Hermes' own reply still the
  JSON envelope. Hermes reads any output back with `local_output`; the log
  and `out/<n>.txt` stay untouched. No model writes a summary.
- **A chat checkpoint per compaction.** What Hermes reads from then on is
  frozen in `<id>/chat-checkpoints/NNNN.json` (`n`, `seq` — the last event it
  covers —, `depth`, `before` / `after` and where `before` comes from —
  `measured`, `estimated`, or `log`: a session from before the meter, the
  whole log in full, more than hermes-agent ever held —, which outputs and
  turns it carries short, and the `messages`). Hermes reads the latest checkpoint plus every
  event after it, so that prefix never changes. `events.jsonl` stays the
  whole chat: the `compact` event names its checkpoint and is the divider
  between the old chat and the compacted one — in the terminal a
  `──── compacted #1 · ~98k → ~31k tokens · … · chat-checkpoints/0001.json ────`
  line, live and in `/history`.
- **Same session.** From the first compaction on, hermes-hands sends its
  transcript with every run as `conversation_history`; hermes-agent uses it
  instead of its stored one and keeps only each run's new messages under the
  same session id. No session switch.
- **One round can't flood it.** `HERMES_HANDS_MAX_ROUND_OUTPUT` caps the
  bytes of one round's results together: the small outputs stay whole, the
  big ones keep head and tail around a marker that names the cut bytes (and
  how to read them back). The full output stays local.
- **A refused request is handled locally.** If the model server refuses a
  request for its size anyway, hermes-hands makes the same request smaller
  and sends it again, within the same round: first the transcript compacted
  as far as it goes (every output elided, every earlier turn as question +
  answer), then the results the request carries fitted tighter. Only when
  that cannot make it fit — the fixed part of the prompt and this request
  alone are too large for the window — does the turn end BLOCKED.

An editor plugin puts the two side by side from the same files: rpc
`history` returns every event (the `compact` events with their
`checkpoint`), rpc `context` what Hermes reads now — the checkpoint's
messages and those after it.

`HERMES_HANDS_COMPACT_AT` defaults to `auto`: a few points below where
hermes-agent compacts on its own, so hermes-hands' lossless compaction comes
first. hermes-agent refuses windows under 64K; under 512K it raises any
`compression.threshold` to at least 0.75 (never below 64K tokens, at most
85 % of the window); from 512K it uses the threshold (default 0.5). So
`auto` is 79 % at 64K, **69 %** from ~86K to 512K, **47 %** from 512K. To use
more of the window, raise hermes-agent's threshold above 0.75 (it allows
that) and set `HERMES_HANDS_COMPACT_AT` a few points below it. It takes an output reservation
(`max_tokens`) on the model off the window first, and with
`compression.threshold_tokens` set it compacts at the lower of the two — in
either case set `HERMES_HANDS_COMPACT_AT` below that by hand. Whatever the
share, the trigger leaves 24K above the request: 20K for the reply, 4K
buffer.

The turn footer splits the turn's time: `— worked for 1m06s · hermes 58s ·
local 8s · waited on you 1m48s —` — Hermes' share (model, server tools,
network), the hands' calls, and the approval prompts (left out of the worked
time and of each call's own duration).

Ctrl-C during a turn cancels it locally **and** `POST /v1/runs/{id}/stop`s the
run on Hermes.

## Plugin / editor integration

`hermes-hands --rpc` is a **persistent JSON-lines session server**: one process,
one held session, driven over stdin/stdout. An editor extension spawns it once
and streams turns — it does **not** script the interactive REPL, and it does not
spawn a process per turn (that would fragment the central Hermes into throwaway
sessions). It wraps the CLI rather than calling the Hermes API directly to get
the persistent shell, approval denylist, repo jail and secret-scrubbing for
free.

One request object per line on **stdin**; one response object per line on
**stdout**; diagnostics on **stderr**.

```jsonc
// requests
{"id":1,"type":"turn","text":"why is CI red?"}
{"id":9,"type":"append","text":"also the lint job"}     // while a turn runs: with its next round ("queued");
                                                        // what it did not take: the next turn, under this id;
                                                        // no turn running: a turn of its own
{"id":2,"type":"new"}                                   // start a fresh session, becomes current
{"id":3,"type":"use","session":"hh_20260902T…_abc123"}  // switch (id from `sessions new`)
{"id":4,"type":"check"}
{"id":5,"type":"compact"}                                 // compact this session's chat now ("text":"aggressive": every earlier turn too)
{"id":7,"type":"context"}                                 // what Hermes reads now
{"id":6,"type":"approvals","text":"auto"}                 // auto | never ("" = report) — this session only

// responses
{"type":"ready","session":"hh_…","cwd":"/repo","version":"X.Y.Z","approvals":"off","hermes_agent":"0.22.2"}
{"type":"call","id":1,"ref":14,"tool":"local_shell","preview":"npm test"}
{"type":"tool","id":1,"ref":14,"tool":"local_shell","preview":"npm test","exit":0,"lines":12}
{"type":"progress","id":1,"round":3,"max_rounds":25,"calls":12,"max_calls":100}  // the turn's budget (max 0 = no limit); + "context","window" once known
{"type":"note","id":1,"text":"budget: 10 of 25 rounds · 29 of 100 calls left — …"}
{"type":"queued","id":9,"text":"also the lint job"}                         // append taken; sent with the next round as:
{"type":"added","id":1,"text":"also the lint job"}
{"type":"answer","id":1,"ok":true,"text":"…","session":"hh_…","hermes_session":"…","rounds":12,"calls":47}
{"type":"session","id":2,"session":"hh_…","hermes_session":"…","approvals":"off"}
{"type":"check","id":4,"ok":true,"model":"…","base":"…","version":"0.22.2"}
{"type":"compact","id":5,"ok":true,"session":"hh_…","before":98000,"after":31000,"text":"compacted #1 · …","checkpoint":"chat-checkpoints/0001.json"}
{"type":"context","id":7,"session":"hh_…","owned":true,"checkpoint":"chat-checkpoints/0001.json","n":1,"messages":[…],"since":[…],"context":31000,"measured":false,"window":131072}
{"type":"approvals","id":6,"approvals":"off"}             // off (= auto) | never
{"type":"error","id":1,"message":"…"}
```

Approvals can't be prompted over the pipe, so `--rpc` requires `--yolo`
(`HERMES_HANDS_APPROVE=auto`) or `HERMES_HANDS_APPROVE=never` — the editor is
expected to run its own approval UI before sending a `turn`. An `approvals`
request switches between `auto` and `never` while the process runs — a runtime
switch: the next `new` / `use` starts from config / env / flags again, and its
`session` reply says which mode is in effect. Close stdin to shut it down;
`SIGINT` cancels the in-flight turn.

Persist a session across editor restarts with `hermes-hands sessions new` (mint
+ print an id) and pass it back via `--session <id>` or an rpc `use` message.

## Config

Everything lives in one directory, `~/hermes-hands/` — set `HERMES_HANDS_HOME`
to move it. Inside: `config`, `secrets.enc` + `keyseed`, `sessions/`, and an
optional `instructions.md`. Each path also has its own override
(`HERMES_HANDS_CONFIG` / `_SECRETS` / `_STATE` / `_INSTRUCTIONS`).

`~/hermes-hands/config` — `KEY=value` / `export KEY=value`, values optionally
`"…"`, `'…'` or `$'…'`-quoted. Anything else on a line is ignored (the old bash
version `source`d the file; the Go port deliberately parses only assignments).

**Layers.** Every setting resolves weakest first: the built-in default <
`config` (your defaults for every session) < the environment (a non-empty
value) < a flag (`--yolo`) < a runtime switch (`/approve`, `/yolo`, `/debug`, an rpc
`approvals` / `debug`). Runtime switches are never saved: every session you
open or resume — also with `/session` or rpc `use` in a running process —
starts from config / env / flags again. `/config` shows each value with the
layer it came from.

**Budget.** Each turn has the operator's budget: `HERMES_HANDS_MAX_ROUNDS`
requests to Hermes, `HERMES_HANDS_MAX_CALLS` hands calls, at most
`HERMES_HANDS_MAX_CALLS_PER_ROUND` calls per envelope (`0` = no limit).
Hermes is told the budget in the task frame and gets a countdown in its
results once the end gets close (rounds: the last 40 %, then "finish" over the
last 20 %; calls: from 75 % and 90 % used). An envelope over the budget is not
run at all — no half-run batch — and Hermes is asked to resend a smaller one.
The terminal shows `round 3/25 · calls 12/100` on the working line, a dim note
when a countdown starts, and `· 12 rounds · 47 calls` in the turn footer. A
turn stopped at its budget is unfinished work: the next turn is told where it
stopped, so a "continue" resumes with a fresh budget.

**Secrets.** The URL + key are resolved in this order: the `HERMES_API_URL` /
`HERMES_API_KEY` environment variables always win; then, only while one is still
unset, the secrets store. `setup` (default) writes a machine-bound
`secrets.enc` + a 0600 `keyseed` — AES-256-GCM, key derived per-machine
(HKDF over the keyseed, `/etc/machine-id` and your uid), so a copied
`secrets.enc` alone is useless. It needs nothing in your shell env. If it can't
be decrypted here (wrong machine, tampered, missing `keyseed`) the CLI says so
and exits — re-run `hermes-hands setup`. This is **at-rest protection only**: it
does not stop a program running as you (or an approved `local_shell`) from reading the
key — the approval gate + denylist are the real boundary. `setup --plaintext`
keeps a 0600 `~/hermes-hands/secrets` file (`export HERMES_API_URL=…` /
`…KEY=…`) plus the `~/.bashrc` offer, for people who inject via env or a secrets
manager; that file is the fallback when no `secrets.enc` exists. `/config` shows
which store is actually in effect. See [`docs/secrets.md`](docs/secrets.md).

| key | default | meaning |
|---|---|---|
| `HERMES_HANDS_HOME` | `~/hermes-hands` | the one directory holding config, secrets, sessions |
| `HERMES_API_URL` | — | gateway base, `https://…` |
| `HERMES_API_KEY` | — | `API_SERVER_KEY` (goes in the machine-bound `secrets.enc`) |
| `HERMES_API_PROFILE` | — | route to `/p/<profile>/` (needs that profile's own key) |
| `HERMES_HANDS_APPROVE` | `ask` | `ask` \| `plan` \| `auto` \| `never` |
| `HERMES_HANDS_STREAM` | `auto` | SSE answer streaming: `auto` (when advertised) \| `on` \| `off` |
| `HERMES_HANDS_DEBUG` | `0` | `1` = start with `/debug` on (redacted connection trace); `0` / `off` = off |
| `HERMES_HANDS_RETENTION_DAYS` | `14` | delete local sessions idle longer than this on start (`0` keeps all) |
| `HERMES_HANDS_SANDBOX` | `steer` | Hermes' own server-side terminal / file tools in a hands run: `steer` (all back to the hands) \| `smart` (only calls meant for your project, or environment work) \| `show` (never) |
| `HERMES_HANDS_DENY` | — | extra denied `local_shell` commands, `\|`-separated shell globs |
| `HERMES_HANDS_MAX_ROUNDS` | `25` | requests to Hermes per turn (`0` = no limit) |
| `HERMES_HANDS_MAX_CALLS` | `100` | hands calls per turn (`0` = no limit) |
| `HERMES_HANDS_MAX_CALLS_PER_ROUND` | `20` | calls per envelope; a bigger envelope is not run (`0` = no limit) |
| `HERMES_HANDS_RUN_TIMEOUT` | `120` | per-command seconds |
| `HERMES_HANDS_MAX_OUTPUT` | `20000` | bytes kept per tool result (`local_shell` keeps 2×) |
| `HERMES_HANDS_MAX_ROUND_OUTPUT` | `60000` | bytes of one round's results together; a bigger round is cut, the full output stays local (`0` = no limit) |
| `HERMES_HANDS_CONTEXT_WINDOW` | `131072` | the model's context window in tokens (`0` = unknown: the meter counts, nothing compacts on its own) |
| `HERMES_HANDS_COMPACT_AT` | `auto` | compact before a request past this % of the window — must stay below where hermes-agent compacts. `auto` does that for hermes-agent's defaults (69 under a 512K window, 47 from 512K); with another `compression.threshold` or a `compression.threshold_tokens`, set a number a few points below it (`0` = only `/compact`) |
| `HERMES_HANDS_RESPONSE_TIMEOUT` | `600` | seconds hermes-agent may go silent in a turn before it is cancelled; pushed back by any token / tool round / a live server-run probe (`0` = no limit) |
| `HERMES_HANDS_WATCHDOG_INTERVAL` | `200` | seconds between those out-of-band "still working?" probes (floored at 15) |
| `HERMES_API_*` | — | `CONNECT_TIMEOUT` 5, `MAX_TIME` 30, `POLL_INTERVAL` 2, `RETRIES` 3, `RUN_TIMEOUT` 600 |

`/config` (alias `/hh-settings`) in-session prints these paths and effective
values with their layer; it never writes anything — edit
`~/hermes-hands/config` by hand.

The per-run `instructions` block sent to Hermes is baked into the binary (source:
`share/instructions.md`). Drop a `~/hermes-hands/instructions.md` to override it
with repo-specific rules. Either way it's scoped to `hermes-hands` runs only —
your phone and web UI never see it.

### Recommended setup

The defaults above are the recommended starting point; the budget is sized
for them. Around them:

- **A context window of at least 128k tokens (131,072).** hermes-agent
  takes a large share of the window itself — its system prompt, tool schemas
  and memory — before the hermes-hands instructions and the first results
  arrive. Tested: clearly better results at 128k than at 64k, where
  compaction sets in after a few rounds and the model loses the thread.
- **hermes-agent's own limit no lower than the budget.** `agent.max_turns`
  caps hermes-agent's iterations within one run, and one hands round is one
  run — what iterates there are Hermes' own server-side tools, not the hands
  calls. Set it in the order of `HERMES_HANDS_MAX_CALLS` (e.g. `100`), so
  the budget hermes-hands announces is the one that binds, not a silent cut
  inside a round.
- **`HERMES_HANDS_CONTEXT_WINDOW` = the model's window**, and
  `HERMES_HANDS_COMPACT_AT` at `auto`, below where hermes-agent compacts
  ([Context and compaction](#context-and-compaction)). hermes-agent's own
  compaction (`compression.enabled`) stays on for every other client of the
  gateway. The budget is built around compaction: every stop asks for a
  final that says what is done, what is open and what the next turn must do.
  A single round stays within `HERMES_HANDS_MAX_ROUND_OUTPUT` whatever
  `HERMES_HANDS_MAX_CALLS_PER_ROUND` × `HERMES_HANDS_MAX_OUTPUT` allows; with
  a smaller window, lower it.
- **Prompt caching for the fixed prefix.** Every round re-sends the same
  prefix: hermes-agent's system prompt plus the hermes-hands instructions
  (unchanged for a session). Caching does not shrink it or free context, but
  makes re-reading it cheap and fast — use prefix caching in the model
  server, or prompt caching through a caching model gateway
  or hermes-agent's `prompt_caching` with a provider that supports
  it. Keep a
  `~/hermes-hands/instructions.md` override short: it is part of that prefix
  on every round.

## Security model

- **Outbound only.** The CLI dials your gateway. Nothing listens on your machine.
- **Approval by default** for `local_shell`, `local_write`, `local_edit` — you
  see the command or diff and confirm (`y`/`n`/`a`ll/`q`uit); every answer is
  kept in the session's event log. `--yolo` or `HERMES_HANDS_APPROVE=auto`
  turns it off.
- **Denylist** for `local_shell`: ssh/scp/sftp/rsync, sudo/doas, `rm -rf /…`,
  fork bombs, `curl|wget … | sh` — plus your `HERMES_HANDS_DENY` globs.
- **Repo jail** for the structured file tools: `local_read` / `local_write` /
  `local_edit` refuse paths that resolve (symlinks included) outside the
  directory you launched from. `local_shell` is a real shell (it can `cd`
  anywhere) — it relies on the denylist + per-command approval, not a path
  jail.
- **Secret scrubbing.** Tool output is passed through a redactor (bearer tokens,
  `api_key=`/`password=`, AWS keys, `sk-…`, `ghp_…`, PEM headers) before it goes
  back to the gateway.
- **Secrets at rest.** The default `secrets.enc` is AES-256-GCM, machine-bound
  (see Config). No passphrase — deliberately: it protects a stolen/backed-up
  copy of the file, not a live read by something already running as you. The
  approval gate + denylist are the boundary that matters.
- **TLS enforced.** A non-`https://` `HERMES_API_URL` is refused (loopback needs
  an explicit `HERMES_HANDS_ALLOW_HTTP=1`, for tests).
- **WSL:** `/etc/machine-id` is stable across WSL restarts but is regenerated if
  you re-register the distro — re-run `hermes-hands setup` after that.
- **macOS** has no machine-id file: the key is bound to the hardware
  `IOPlatformUUID` (from `ioreg`) instead.

## Honest limitations

- It's a **workaround** for an unmerged feature. Reliability depends on the model
  behind your Hermes API emitting the JSON envelope cleanly; the CLI recovers
  from fenced/prosey/botched JSON but a weak model will still be flaky. Tune
  `instructions.md`.
- The delegation transcript accumulates in the session. Long sessions lean on
  hermes-hands' compaction (and the meter's estimate between measurements);
  start a `/new` session for a new task.
- Linux and macOS only; on Windows use WSL.

## Development

```sh
git clone https://github.com/Zesales/hermes-hands && cd hermes-hands
make                 # list targets, grouped (Develop / Verify / Artifacts)
make toolchain       # once: docker pull the pinned Go image (golang:1.26.10-bookworm)
make dev             # build in the container, run it on this host out of ./.dev/
make dev ARGS=--new  # pass CLI args through ARGS=
make test            # go test ./... — offline, no network, no model
make golden          # rewrite testdata/golden/ after an intended UI change
make race            # the tests under the race detector
make cover           # coverage per package: unit tests, then what the golden runs reach
make vuln            # govulncheck (pinned): known vulnerabilities on the code paths we call — needs network
make lint            # gofmt check + go vet   (STATICCHECK=1 also runs staticcheck)
make ci              # the PR gate locally: lint + test + race + release cross-build
make dev-install     # build + copy dist/hermes-hands onto your PATH
```

The toolchain is pinned in the repo: every `go` / `gofmt` / `vet` / `test` /
`build` runs in a throwaway `docker run --rm` container, so the host needs only
Docker (`GO_MODE=local` uses a host-installed go instead). Only the build runs
there — the binary always runs on your host, with your real bash, PATH and
tools, because that is what the hands are.

`make dev` runs a **self-contained dev instance out of `./.dev/`** (gitignored):
its own `HERMES_HANDS_HOME`, so nothing touches your real `~/hermes-hands/`. On
first run it copies the machine-bound key over from `~/hermes-hands/` (bound to
machine-id + uid, not its path, so the copy decrypts) — or `make dev-setup`
prompts for a separate one. The prompt is read straight from
`share/instructions.md`: edit it, `make dev` again (code changes are rebuilt
incrementally on every `make dev`). `make clean` wipes `./.dev/`, including the
toolchain cache.

Go 1.26.10, `CGO_ENABLED=0`. Dependencies (`github.com/peterh/liner` +
`github.com/mattn/go-runewidth` + `golang.org/x/sys`) are **vendored** — builds
never fetch modules (the one download is the pinned Go image). Layout: `main.go`
(the build-time version, then `internal/cli`) +
`internal/{api,app,cli,compat,config,debuglog,dispatch,guard,knowsystem,loop,prompt,redact,repl,rpc,session,setup,shell,ttyio,turn,ui}` +
`share/instructions.md` (embedded via `//go:embed`). `.github/workflows/test.yml`
runs gofmt, `go vet` (for linux **and** darwin), `go test` and
`./build.sh release` (the linux+darwin × amd64+arm64 cross-compile) on every
PR, all in the pinned Go container (the `test`
job is a required check for merging), plus `make race`, `make vuln` and the
coverage table; a second job runs the tests on macOS itself (bash 3.2, BSD
userland). `codeql.yml` scans the code with CodeQL (PRs, main, weekly; results
in the Security tab); Dependabot proposes updates of the pinned Actions, never
before a release is 14 days old, labelled `bump:patch`. `release.yml` tags + publishes on merge (see
Releasing).

**Golden transcripts** (`testdata/golden/`) pin what the operator sees: one
full turn through the real `main()` — the REPL and `--rpc` — against the
streaming mock (Hermes' own tool events, a steered call, both kinds of
approval prompt, a final whose escapes the 5-byte deltas cut), every slash
command and every rpc request type once, masked for paths, ids and times. `make test` fails on any difference; an intended change
is `make golden` plus a reviewed diff of those files.

A **size budget** (`budget_test.go`) keeps one job per unit: no function over
80 lines, no file over 450 (tests and `vendor/` excepted). What is over it
today is listed at its current size and may only shrink.

`test/mock_hermes.py` is a manual-only stand-in for the Runs API — the Go suite
has its own in-process mock (`internal/hermesmock`) and needs no `python3`.

## Releasing

**No `VERSION` file — git tags are the source of truth.** `git describe` bakes
the version into every build (`0.11.0` on a release, `0.11.0-3-gabc123` in
between; `install.sh --version 0.11.0` pins one).

**Every merged PR ships a release.** `.github/workflows/release.yml` (on
`pull_request` → closed & merged):

1. **next version** = the highest `v*.*.*` tag, bumped by the PR's label —
   exactly one of `bump:patch` / `bump:minor` / `bump:major` — there is no
   "no release" label. `pr-label.yml`'s `bump-label` job enforces the label as
   a required check.
2. puts an **annotated tag `vX.Y.Z`** on the merge commit and pushes **only the
   tag** — nothing is committed to `main`, so branch protection needs no
   bypass,
3. runs `./build.sh release` (`{linux,darwin}×{amd64,arm64}` + `SHA256SUMS`)
   with that version, publishes with auto-generated notes ("What's Changed"
   from the PRs + a full-changelog link), marked *latest*.

It does **not** re-test — the gate is `test.yml` + `bump-label` on the PR
(required checks) plus "require branches up to date", so the merged code is
exactly what was verified. `workflow_dispatch` fires it by hand with an
explicit bump level.

> One-time repo setup: Settings → Actions → Workflow permissions → **Read and
> write**; make `test` and `bump-label` required status checks; create the
> labels `bump:patch` / `bump:minor` / `bump:major`. The first
> release's baseline is `MIN_VERSION` in `release.yml` (used only until the
> first tag exists).

`./build.sh` builds this machine's binary into `dist/`; `./build.sh release`
produces the exact artifact set the workflow publishes. `install.sh`:

- default — latest release, SHA-256 verified;
- `--version X.Y.Z` — pin to release `vX.Y.Z`;
- `--local` — build from the current checkout;
- `--source` — git-clone + build.

## Contributing

Issues and PRs are welcome — see [CONTRIBUTING.md](CONTRIBUTING.md) (one
`make ci` is the whole PR gate). Security issues: [SECURITY.md](SECURITY.md).

## License

MIT.
