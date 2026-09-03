# Changelog

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
