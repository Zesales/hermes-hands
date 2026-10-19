# Changelog

hermes-hands keeps its own version line. Each release names the hermes-agent
range it is built and tested for; `hermes-hands check` prints the version your
gateway runs.

## 0.14.3 — (unreleased, `bump:patch`) an exchange, not a tool: a misdirected call is steered, then its run stopped

Seen in real code sessions and in the measurements of 2026-10-14: Hermes
treats the hands as tools of its own. Steered once, a model kept calling
`tool_call` "Local Shell" / "Local Edit" — eight times in one run; after
each, hermes-agent's own error invites a retry under the exact name — until
hermes-agent's retry guard ended the turn with a prose answer: 19 minutes,
18 of them Hermes, one the hands. The terminal showed every attempt as
steered; only the first was. Right after a steer, or to "check the
connection", Hermes also calls a tool only to have called something —
`tool_call` "Noop Placeholder" / "Mcp Placeholder", `echo placeholder`, and
since `steer` took its `terminal` away, `delegate_task` "1 tasks: noop" —
up to ten in a row, each one a model call and, for `delegate_task`, a
sub-agent. Delegation is never steered, so nothing stopped it.

- **Each misdirected call is steered, up to five a run per kind of
  mistake** (was: once a run) — a model often catches itself after a few.
- **Past that hermes-hands stops the run** (`POST /v1/runs/{id}/stop`),
  waits it out and sends the same message again with a correction in front
  ("your last run was stopped: after its steers you still called …") — at
  most twice a turn; a third ends the turn `BLOCKED`. The terminal shows
  `↳ again after its steers — run stopped; hermes-hands asks once more`
  (action `stopped` in `events.jsonl` and `--rpc`). A reply that made it
  before the stop took effect is used as is. When the server refuses the
  stop, the run goes on and a repeat is only reported (`repeat`: `↳ again —
  this run was steered for it already; not again`) — never a steer claimed
  that did not go out, as the terminal did before.
- **The guard steers a placeholder call** of any tool in `steer` and `smart`
  (`guard.Placeholder`: a mark — `noop`, `placeholder`, `dummy` — among
  filler words only; a real task that mentions one is left alone), as a
  kind of its own (`placeholder`). The steer says why: the hands are
  connected whenever Hermes runs, and its first real call in the envelope
  is the check.
- **`delegate_task` counts as server-side work** (`guard.SandboxTools`): its
  sub-agents work in the server sandbox and never reach the hands. `steer`
  steers every delegation, `smart` one aimed at the project; a placeholder
  delegation is steered as such in both.
- **Sharper steers**: a hands tool called as Hermes' own is told its "not a
  known tool name" error is final under any spelling, and to end the run
  with the envelope; every sandbox steer ends with "No tool call answers
  this note: your next reply is the envelope."
- **Instructions: an exchange, not a tool** — a new section up front: work on
  the operator's machine as if it were one of Hermes' own, one round at a
  time: the reply is the envelope and ends the run, the hands run it as
  long as it takes, the results are the next message. Nothing to wait for,
  check or keep alive — no connection test, no health check, no
  placeholder; a dead shell or a timeout comes back in the results. Turn,
  round and run are named once. The per-turn frame says the same in one
  sentence, and so does **every results message** — rounds 2 and on had no
  such reminder, and most of a long turn's runs are those.
- **Instructions: two machines, and Hermes' own tools** — "Which machine"
  and the rules on Hermes' own tools are one section now: what belongs to
  which machine (web, memory, skills, cron stay Hermes' own; delegation no
  longer), the exec / file / delegation tools not to call for the project,
  and a table from each to the hands operation that does it instead, so the
  reflex has a target. A new example shows the envelope as the way to act on
  the operator's machine, also right after a steer, and what is wrong every
  time.
- **Instructions: one rule, said once** — the rule against Hermes' own tools
  stood in seven places in slightly different words; now in the section
  above, the example, the frame, the results and the steers. The word
  "terminal" names only Hermes' own tool (the instructions and the frame
  called the operator's machine a terminal, too); the instructions name no
  number for the stop (five steers read as four free tries); "think it
  through, not in the reply" in frame and instructions alike (the frame
  said "reason it through" before the JSON); "every reply", not "every
  turn". What the hands run are "hands calls" in every text Hermes reads —
  the budget in the frame, the countdown, the over-budget error, the note a
  capped turn leaves — not "tool calls", Hermes' word for its own. The rule
  against Hermes' own exec / file tools holds for the whole session, not
  only "for the project".
- **Instructions: web search is Hermes' own** — "Sources of truth" and the
  GitHub example said "no web access" and "I can't browse GitHub", while web
  research is one of the tools left to Hermes. Now: the remote's own facts
  (tags, branches) through git and the hands, anything else on the web
  through Hermes' web search — the hands have no browser.
- **An interrupted run ends the request** (hermes-agent's `run.interrupted`
  / status `interrupted`, a gateway restart mid-run): the poll knew only
  `failed` and `cancelled` and went on polling it until the watchdog.

## 0.14.2 — 2026-10-19 — Hermes works on the operator's machine: `steer` by default

hermes-agent: 0.21.0 and newer, tested through 0.22.x (live: 0.22.2).

The change below reached `main` as a direct commit, which cuts no release;
this entry's PR releases it as 0.14.2.

Seen in real code sessions: when a tool was missing on the operator's
machine, Hermes rebuilt the project in its own server sandbox — installed
the toolchain there, tried to get the code there, looked around the
server's containers. The instructions allowed it ("a download" was
server-side work), and smart steered only calls that named the operator's
paths or files.

- **`HERMES_HANDS_SANDBOX=steer` is the default** (was `smart`): every
  server-side exec / file call of Hermes' is steered back to the hands. Its
  web, memory, cron and delegation tools are untouched. An unknown value
  means `steer` too. A config written by `setup` before carries
  `HERMES_HANDS_SANDBOX=smart` as a value of its own — change it to `steer`
  (or remove the line) to get the new default.
- **A missing tool is the operator's call**: the instructions and every
  steer say it — the project's own way first (`make` targets, docker,
  scripts); else a final that names what is missing and the exact command
  for the operator to install it (`sudo apt install …`; the hands refuse
  `sudo`). Never set it up in the sandbox. The project's environment —
  toolchain, dependencies, services, containers — belongs to the hands'
  side; "a download" is no server-side work any more.
- **smart catches environment work** (guard.Setup): installing a toolchain
  or dependencies (`apt` / `apk` / `brew` … `install`, `pip` / `npm` /
  `go` / `cargo` … `install`, `curl … | sh`, toolchain downloads),
  `git clone`, exploring the server's containers or network (`docker ps`,
  `docker network`, `ss -`, `ip a`, `host.docker.internal`, the docker
  socket).

## 0.14.1 — 2026-10-19 — CI: actions/setup-go 7.0.0

- CI only: `actions/setup-go` 6.5.0 → 7.0.0 (Dependabot). Nothing changes
  for the operator; the binaries are built the same way.

## 0.14.0 — 2026-10-19 — `/compact-aggressive`; compaction keeps more of a code session

hermes-agent: 0.21.0 and newer, tested through 0.22.x (live: 0.22.2).

Tuned on a real code session of ~300 tool calls, compacted on 0.13.0.

- **`/compact-aggressive`** (rpc `compact` with `"text":"aggressive"`): every
  output as a line and every earlier turn as question + answer — for when
  `/compact` was not enough. The same depth a request refused for its size
  gets. `/compact` takes no argument any more: a mistyped
  `/compact agressive` does nothing instead of something else (rpc: an
  unknown `text` is an error).
- **`/compact` collapses turns only when it must**: every output still
  becomes a line, but turns are collapsed to question + answer only while
  the prompt would otherwise stay above three quarters of the trigger
  (`HERMES_HANDS_COMPACT_AT`) — not down to half of it. On a 128k window a
  ~300-call session keeps the line of every call instead of losing its
  first turns. Automatic compaction collapses by the same rule; with no
  window known nothing collapses on its own.
- **A collapsed turn names its files**: `· edited: a.go, b.go · read: …`
  (from its `local_write` / `local_edit` / `local_read` calls, up to 12 per
  list) — which files were read and changed is what a code session must not
  lose.
- **An honest "before"**: a session from before the meter had no reading,
  and the divider showed the whole log rendered in full (`~359k`) as if
  Hermes had held it. It now says `log ~359k → ~29k`; the checkpoint records
  `before_from` (`measured` | `estimated` | `log`).

## 0.13.0 — 2026-10-19 — the operator's turn budget; config layers; hermes-hands' own compaction

hermes-agent: 0.21.0 and newer, tested through 0.22.x (live: 0.22.2).

### Context: the meter and hermes-hands' own compaction

A hands session could outgrow the model's window for good: hermes-agent's
compaction keeps the latest messages whole (`compression.protect_last_n`),
and in a hands session those are the last rounds' results. Once they alone
were bigger than the window every request was refused — a "continue"
included, again and again.

- **The meter**: a run's `usage.input_tokens` is the prompt of its model
  calls — with no server tool of Hermes' own in the run, exactly the prompt
  the model saw. Between measurements hermes-hands estimates from bytes and
  calibrates the fixed part (system prompt, tool schemas, instructions) from
  measured runs. Shown on the working line (`· ctx 48k/131k`), in rpc
  `progress` (`context` / `window`) and in `/session`.
- **Compaction before the request**: past `HERMES_HANDS_COMPACT_AT` % (45)
  of `HERMES_HANDS_CONTEXT_WINDOW` (131072) hermes-hands compacts first, only
  as far as needed — the outputs of all but the latest two rounds, then of
  every round, then the oldest turns as question + answer. Nothing is
  summarised by a model and nothing is lost: an elided output goes as a stub
  naming its `#ref`, the new hands tool **`local_output`** reads it back
  (also the cut part of a round over its budget). The log is never rewritten
  — a compaction is a new event (`compact`), Hermes' replies are recorded
  (`reply`) so the transcript can be built from the log.
- **Same session, no switch**: from the first compaction on, hermes-hands
  sends its transcript with every run as `conversation_history` (hermes-agent
  uses it instead of its stored one and persists only the run's new
  messages). A resumed compacted session needs no recap.
- **`/compact`** and rpc **`compact`** compact now (every output so far);
  they say so when there is nothing to elide yet.
- **Chat checkpoints**: each compaction freezes what Hermes reads from then
  on in `<id>/chat-checkpoints/NNNN.json`; Hermes reads the latest one plus
  every event after it, so that prefix never changes (a later version's
  rules neither). `events.jsonl` stays the whole chat; the `compact` event
  names its checkpoint and is the divider — in the terminal a `──── compacted
  #1 · … ────` line, live and in `/history`. **`/context`** (`/context full`)
  and rpc **`context`** show what Hermes reads now; rpc `history` carries
  the dividers, so a plugin can show the old chat and the compacted one.
- **An elided round, short**: one line per call (`#14 local_shell `go test
  ./...` → exit 1 · 312 lines · 18 KB ·` and the output's error or last line
  — the terminal excerpt's rule, now `internal/excerpt`), its reasoning
  clipped to 200 characters; Hermes' replies stay the JSON envelope. Once
  outputs are stubs, the JSON around them was what filled the context.
- **`HERMES_HANDS_MAX_ROUND_OUTPUT`** (60000 bytes): one round's results
  together. A bigger round keeps its small outputs whole and the big ones'
  head and tail around a marker naming the cut bytes; the operator gets a
  note, the session keeps the full output.
- **A run refused for its size is handled locally**: the same request is
  made smaller and sent again within the round — the transcript compacted
  as far as it goes, then the request's own results fitted tighter. Only
  when that cannot make it fit does the turn end BLOCKED, saying why; the
  meter then stands at the refused size.
- Splitting the loop and the SSE stream for this took both off the size
  budget's exception list: `Loop.Run` is the round loop, `request.go` the
  request (meter, compaction, transcript), `envelope.go` the reply parsing;
  `streamRun` reads frames, `runStream.frame` handles one.
- The golden transcripts had not been regenerated after the budget and
  config-layer changes; the PR gate is green again.

### Budget per turn

- **Two budgets instead of one**: `HERMES_HANDS_MAX_ROUNDS` (default 25)
  counts requests to Hermes, the new `HERMES_HANDS_MAX_CALLS` (default 100)
  the hands calls a turn runs, the new `HERMES_HANDS_MAX_CALLS_PER_ROUND`
  (default 20) the calls in one envelope. `0` = no limit (a 0 round budget
  used to block every turn).
- **Hermes knows it is the operator's limit**: the task frame states all
  three; the results count down once the end gets close (rounds: a heads-up
  over the last 40 %, "finish now" over the last 20 %, then the last round;
  calls: from 75 % and 90 % used), and every stop asks for a final that says
  what is done, what is open and what a continuation must do — the note a
  "continue", and a compacted context, builds on. `share/instructions.md`
  explains the budget once.
- **All or nothing**: an envelope over the budget (too many calls for the
  envelope or for what is left) is not run at all and Hermes is asked to
  resend a smaller one — never a half-run batch. A turn out of calls ends
  like one out of rounds: BLOCKED, with a resume note for the next turn.
- **Visible in the terminal**: `round 3/25 · calls 12/100` on the working
  line, a dim note (kept in the session log) when a countdown starts or an
  envelope is refused, `· 12 rounds · 47 calls` in the turn footer, and a
  note when hermes-agent continues in a new session (a compaction). The `#N`
  next to a call is the session-wide call number, not a budget counter.
- **`--rpc`**: new `progress` and `note` messages; `answer` carries
  `rounds` / `calls`.
- The delegation loop is split by job (`loop.go` / `calls.go` /
  `budget.go`); `loop.go` left the size-budget exception list.

### Config layers

- **Environment and flags now win over the config file**: default < config
  < environment (a non-empty value) < flag (`--yolo`) < runtime switch
  (`/yolo`, `/debug`, rpc). The config file is the operator's defaults for
  every session. Before, the config file won — a config that set
  `HERMES_HANDS_APPROVE` silently disabled `--yolo`, and `--rpc --yolo`
  refused to start. Every setting, the API tuning knobs included, can now be
  set in the config file.
- **Runtime switches are never saved**, and every session opened — a resumed
  one, `/session`, rpc `use` / `new` — starts from config / env / flags
  again. **rpc `approvals`** switches the hands' approvals (`auto` |
  `never`) while the process runs; the `session` reply names the mode.
- **`/config` shows each value's layer** (default / config / env / flag /
  runtime / secrets store) and the three budgets.
- **`HERMES_HANDS_DEBUG=0` (`off`, `false`, `no`) is off**; before, any
  non-empty value was on. Same for `HERMES_HANDS_VERBOSE`.
- **`setup` keeps your config**: a fresh config sets every setting at its
  default; an existing one only gets the settings it lacks appended — a
  value you set is never changed; a config that sets nothing (only comments,
  an old template) is replaced, the old one kept as `config.bak`.
  **`hermes-hands setup --config`** does only that step, without prompts.

### Docs

- **README: "Recommended setup"** — a context window of at least 128k
  tokens (tested: clearly better than 64k), hermes-agent's `agent.max_turns`
  no lower than the call budget, compaction on with a round's worst-case
  size in mind, and prompt caching for the prefix every round re-sends.

## 0.12.2 — 2026-10-16 — a license badge that always renders

- The README's license badge is static (`license: MIT`). The dynamic one
  asked the GitHub API through shields.io's token pool on every view and
  showed "Unable to select next GitHub token from pool" / "invalid" when
  that pool ran dry; GitHub itself detects the LICENSE as MIT. The license
  never changes, so the badge needs no lookup.

## 0.12.1 — 2026-10-16 — every PR is a release

- **`bump:none` is gone**: a PR carries exactly one of `bump:patch`,
  `bump:minor`, `bump:major` — `pr-label.yml` requires it, `release.yml`
  cuts a release for every merge (its "no release" path is removed).
  Dependabot's PRs carry `bump:patch`. README, CONTRIBUTING and the PR
  template say so.

## 0.12.0 — 2026-10-16 — Go 1.26.9 security fixes; refactor: one job per unit

hermes-agent: 0.21.0 and newer, tested through 0.22.x (live: 0.22.2).

### Security

- **Built with Go 1.26.9** (was 1.26.7): govulncheck found nine
  vulnerabilities of the Go standard library on code paths hermes-hands
  calls — `net/http` (among them HTTP/1 client connection desynchronization,
  GO-2026-6605), `crypto/tls` (GO-2026-6607), `net/textproto` (GO-2026-6608)
  — all fixed in 1.26.9. `GO_IMAGE` is `golang:1.26.9-bookworm`, go.mod
  says `go 1.26.9`.
- **`make vuln`**: govulncheck, pinned (v1.8.0) — known vulnerabilities in
  the code paths actually called, stdlib and the vendored deps. The one
  check that needs the network. Clean now. It downloads with
  `-modcacherw`, and `make clean` makes an older read-only module cache
  deletable first: the Go module cache is read-only by default, which broke
  `make clean` — and with it `./build.sh release` — once something had been
  downloaded into it.

Apart from that, no change for the operator; the code is reorganised so that
every package and type does one job.

### The module is `hermes-hands`

- `go.mod` says `module hermes-hands` (was `github.com/Zesales/hermes-hands`),
  so the imports name the project, not where it is hosted:
  `hermes-hands/internal/api`. Nothing was ever fetched through that path —
  the packages are this repo's `internal/` and builds are vendored and
  offline — but the import no longer reads like a dependency on GitHub.
  `go install github.com/Zesales/hermes-hands@…` stops working with it;
  installs go through `install.sh` (a release, or a clone built here).

### Safety net

- **Golden transcripts** (`testdata/golden/`, `golden_test.go`): one full turn
  through the real `main()` — REPL and `--rpc` — against the streaming mock,
  compared byte for byte after masking paths, ids and times. `make golden`
  rewrites them after an intended change. Checked to fail on the old
  stream-preview escape bug.
- `internal/hermesmock`: `NewStream` serves the SSE run events like a live
  gateway — `run_events_sse` advertised, the envelope in 5-byte deltas, and
  in mode `golden` Hermes' own tool events (a server call, a steered call, a
  server-tool approval request); `/steer` and `/approval` answer.

### One turn, once

- **`internal/turn`**: the `Runner` runs a turn for every front end — event
  log, silence watchdog, the run id Ctrl-C stops, the answer preview and its
  dedupe, local / wait timing, title, footer. A `Frontend` only shows it
  (`Show` / `Preview` / `EndPreview` / `Quiet`): the REPL draws the terminal,
  `--rpc` writes JSON lines. Before, `runREPL` and `runRPC` each carried that
  whole turn — the copy is where the last fixes had to be made twice.
- The stream-preview decoder and the watchdog moved into `internal/turn`;
  `session.Log` records a session (`Store.OpenLog`, `BeginTurn`, `Add`),
  `ui.Show` / `ui.Replay` draw its events. `sessionlog.go` is gone.

### One place that builds the app

- **`internal/app`**: `App` and `New(cfg, instructions)` — the composition
  root (UI, API client, store, shell, dispatcher, loop; the known-system
  probe and the retention window as their own steps); `Bind` points a turn runner at it, `SetDebug` switches
  the wire trace (a copy each in the REPL and `--rpc` before), `OpenSession`
  / resume, the instructions ladder, the sandbox guard, the `check` line and
  compat warning, the session list. `sessionlist.go` moved in.
- `config.Describe` is the `/config` view, `Config.Configured` replaces
  `notConfigured`.

### The front ends

- **`internal/repl`**: `Run` is the read loop; a `repl` value holds what the
  slash commands change (app, session, runner, debug, configured). Every
  slash command is a small method in one table (`commands.go`, the session
  ones in `cmd_session.go`) instead of a 14-case `switch` inside a 440-line
  `runREPL`; `front.go` draws a turn and asks Hermes' server approvals.
- **`internal/rpc`**: `Run` serves the JSON lines; its `front` writes a turn.
  `rpc.go` / `frontends.go` left the root package. Like the REPL's slash
  commands, each request type is a small handler in one table (`start`
  preflights and says ready, `serve` reads the lines); the rpc golden
  transcript now sends every request type once, mistakes included — written
  with the old code, passed by the new.
- Both get what they need from outside as `Deps` (build the app, the setup
  wizard, the version).

### main is main

- **`internal/cli`**: `Main(args, Build)` — the arguments, `--help`,
  `--version`, and the subcommands (`check`, `sessions`, `sessions new`,
  `setup`, `guidance`, the hidden dev ones). **`internal/setup`**: the wizard,
  the global-rules import, the `~/.bashrc` offer.
- `main.go` is the build-time version and one call into `cli.Main` (1650
  lines before this refactor); the package doc names the hands' tools
  `local_*`.

### The API by what it talks to

- **`api.Client`** stays the one object everyone holds — the connection and
  the running turn's hooks — and its views say which part of the Hermes API
  a caller uses: `Runs()` (Ask, State, Stop), `Sessions()` (List, Info,
  SetTitle, Fork), `Server()` (Check, the version, capabilities). The loop
  asks through `client.Runs()`.
- `api.go` (769 lines) and `ask.go` became `client.go`, `transport.go` (the
  shared HTTP plumbing), `runs.go`, `sessions.go`, `server.go` and
  `decode.go`; `stream.go` stays the SSE stream.

### knowsystem by task

- `knowsystem.go` (1012 lines) is split, unchanged line for line: the
  types (`knowsystem.go`), the host and tool probe (`probe.go`), repo facts
  — make targets, layout (`repo.go`), the rules files — guidance, `@path`
  imports, nested rules, the budget, the global import (`rules.go`), the
  rules of a subdirectory at the first call into it (`scoped.go`), learned
  facts and the record on disk (`store.go`), what Hermes reads (`brief.go`).

### Guard rails

- **Size budget** (`budget_test.go`, part of `make test`): no function over
  80 lines, no file over 450 (tests, `vendor/`, `testdata/` excepted). The
  three functions and one file over it today — the delegation loop, the SSE
  stream, the brief — are listed at their current size and may only shrink;
  one that fits must leave the list.
- **`make race`**: the tests under the race detector (the golden runs drive
  the real REPL and `--rpc` through it); part of `make ci` and of the PR
  check (`test.yml`).
- **Every slash command in a golden transcript** (`repl_commands.txt`):
  /help, /yolo twice, an unknown command, a turn, /out, /history, /sessions,
  /session, /debug, /check, /config, /compact, /fork, /new. Written by the
  code from before this refactor and passed by today's — and that old code
  reproduces the two other transcripts byte for byte too.
- **`make cover`**: coverage per package — the unit tests', then what the
  golden runs reach in their subprocess (`go tool covdata`), where the REPL
  and `--rpc` actually get exercised. The golden runs set `GOCOVERDIR`, so
  `go test -cover` no longer breaks them with a warning in the transcript.
- **GitHub checks**: `test.yml` runs on a read-only token, a newer push
  cancels the run in flight, and the coverage table lands on the run's
  summary page; `pr-label.yml` needs no token rights at all; every job has a
  timeout.
- **More checks on every PR**: `make vuln` (govulncheck); a **macOS job**
  runs the tests on macOS itself — the system bash 3.2 and the BSD userland,
  which the linux job only cross-compiles and vets for (host Go via
  `actions/setup-go`, exactly go.mod's version); **CodeQL** (`codeql.yml`)
  scans the Go code on PRs, on main and weekly, results in the Security tab.
- **CodeQL's first findings fixed**: the stream preview parsed `\uXXXX`
  with a 32-bit size and converted it to a rune — four hex digits can never
  overflow it, but nothing said so; it parses 16 bits now (the size of a
  UTF-16 code unit), so the bound is in the code (four alerts, one cause).
- **`actions/checkout` v7.0.1** (was v4.2.2, which still ran on the
  deprecated Node.js 20 — the runner warned on every job): a patch release
  that carries the fixes GitHub released for all checkout lines on
  2026-07-20. Its v6 change (credentials in a separate file) keeps the tag
  push in `release.yml` working.
- **A failed test is an annotation**: both test jobs keep the output and,
  on a failure, put the failing tests with their `want` / `got` lines on the
  run as annotations — readable on the PR and through the API without the
  log.
- **Dependabot** watches the pinned GitHub Actions only, with a 14-day
  cooldown and the `bump:none` label; Go modules stay vendored and updated
  on purpose. Every action pin reads `@<sha> # vX.Y.Z`, the form Dependabot
  keeps current. New pins: `actions/setup-go` v6.5.0, `github/codeql-action`
  v4.38.2.

## 0.11.1 — 2026-10-16 — the answer once; approvals in the replay; local vs. server sessions

hermes-agent: **0.21.0 and newer**, tested through 0.22.x (live: 0.22.0).

### The answer is printed once

- **The live answer preview no longer garbles and then repeats the answer.**
  It decodes the `final` string out of the streamed envelope chunk by chunk;
  an escape cut by a chunk boundary (`\` ending one chunk, `n` starting the
  next) came out as two plain characters — the preview showed `\n` / `\"`
  literally, and a `\\\"` cut after its first `\` was taken for the end of
  the string, so the preview stopped mid-answer. Then it no longer matched the
  final answer, which was printed a second time below it. An escape now waits
  for its second half; `\uXXXX` (surrogate pairs included), `\r`, `\b`, `\f`
  decode too, and a `"final": null` written ahead of the calls previews
  nothing.
- A preview an earlier round left open (a prose reply sent back for a fix-up)
  is closed when the next round starts; the dedupe compares the last round's
  text only.
- `instructions.md`: `final` is a plain JSON string — a quote is escaped once
  (`\"`). The model sometimes wrote `\\\"`, which the operator reads as a
  stray backslash.

### The replay shows what the terminal showed

- **Answered approval prompts are in the event log** (`approval`: the
  summary, the diff it showed — first 200 lines —, the answer, the wait), and a
  reopened session draws them where they stood: `⚠ local_shell: …` /
  `[y]es [n]o [a]ll [q]uit turn > a`. Before, the replay skipped them. Hermes'
  own server-tool prompts (`[o]nce [s]ession [n]o`) too. A test pins replay ==
  live, byte for byte.
- The hands' approval prompts name the tools `local_shell:` / `local_write:` /
  `local_edit:` — they still said `shell:` / `write_file:` / `edit_file:`,
  right under a `#2 local_shell` line.

### Where the time went

- **A call's duration leaves the approval wait out.** `#2 ✓ exit 0 · 1m48s`
  was the operator's 1m40s at the prompt plus the command; now it is the
  command.
- **The turn footer splits the time**: `— worked for 1m06s · hermes 58s ·
  local 8s · waited on you 1m48s —` — Hermes' share (model, server tools,
  network), the hands' calls, and the approval wait ("worked for" already left
  it out). A turn without calls keeps `— worked for 8s —`. `turn_end` events
  carry `local_ms` / `wait_ms`; `--rpc` records the local share too.

### Sessions

- **`--session-list` lists this machine's sessions only** — offline, no server
  call. The server's whole list (cron jobs, other clients, sessions retention
  removed) no longer runs on below it.
- **`--session-list-verbose`** (also `sessions -v`, `/sessions -v`) is the
  local-vs-server view for debugging: under every local session its
  hermes-agent session (messages, context tokens, model, last activity,
  lineage) — or that the server doesn't have it, or that no turn was sent yet.
  A session missing from the server's latest-100 listing is looked up by id
  (at most 20). Then the `hh-*` sessions the server holds without a local one
  ("continued as …" for a split the local session moved on to). The rest is
  only counted.
- The IDLE column stays narrow: `24m`, `21h28m`, `3d` (was `24m0s`,
  `21h28m0s`).

### Versions

- **Supported hermes-agent: 0.21.0 and newer, tested through 0.22.x.**
  Releases before 0.21.0 are not supported and not recommended — hermes-hands
  was never tested against their Runs API and session handling. Newer ones
  should work until hermes-agent changes that API (its session rework, due
  with 0.23, touches `/v1/runs`); the release that adapts to it names the new
  range. `internal/compat` holds the range.
- `hermes-hands check` / `/check` name the gateway's hermes-agent version
  (`GET /health`, best effort): `API OK: hermes-agent @ https://… ·
  hermes-agent 0.22.0`, plus a `WARNING:` line before 0.21.0 and a `note:`
  past 0.22.x or when the gateway doesn't say. A session start warns for a
  known version outside the range. rpc: `check` carries `version` (+ `compat`),
  `ready` carries `hermes_agent` (+ `compat`). README: **Works with**.
- The usage text, the README (diagram, requirements, security model, config
  table, rpc examples) and the rpc doc comment name the hands' tools
  `local_*` — they still said `shell` / `read_file` / `write_file` /
  `edit_file`. The install example pins `0.11.0` (`0.9.3` was never tagged).

### On GitHub

- README: badges (release, tests, hermes-agent range, Go, license), a
  three-line quick start under the diagram, a link to the
  [Remote Brain, Local Hands](docs/remote-brain-local-hands.md) write-up, a
  Contributing section; the layout line names every package.
- The write-up describes the design only — no particular setup, hosts or
  hardware. Model: tested with `qwen3.6-35b-a3b`, currently runs best on
  `qwen3.8-flash-next`. Its tool names are `local_*`.
- `CONTRIBUTING.md` (build, the PR gate, bump labels, CHANGELOG, pinning),
  `SECURITY.md` (private reporting, what is in scope), issue templates (a bug
  report asks for the hermes-hands and hermes-agent versions; upstream issues
  go to hermes-agent) and a PR template.

## 0.11.0 — 2026-10-16 — session event log, live tool calls, sandbox guard, known system

hermes-agent: 0.21.x – 0.22.x (built on 0.21.5, live-tested on 0.22.0).

One minor release for the whole `better-instructions` line (PR #4). Breaking
for scripts and plugins — see **Sessions** and **REPL**.

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

### Docs

- README **Scheduled jobs on your machine**: a cron job set up in Hermes runs
  on the server, without the hands; for a job on the operator's machine a local
  crontab line feeds `use` + `turn` into `hermes-hands --rpc`, fenced with
  `HERMES_HANDS_DENY`.

## 0.10.1 — 2026-09-04 — the "Remote Brain, Local Hands" write-up

- `docs/remote-brain-local-hands.md`: why the hands are a separate outbound
  CLI, how the envelope loop works, where split-runtime fits. Docs only.

## 0.10.0 — 2026-09-03 — continuous releases; pre-public cleanup

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
