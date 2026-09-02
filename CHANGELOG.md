# Changelog

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
