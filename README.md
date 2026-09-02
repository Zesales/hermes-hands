# hermes-hands

**Terminal coding with a remote [Hermes](https://github.com/NousResearch/hermes-agent)
brain.** Your Hermes gateway holds the plan, the memory and the persona; a small
CLI on your machine lets it read files and run commands in the repo you're
standing in — over Hermes' own HTTP API, outbound only, nothing listening on your
box.

```
you type ──▶ POST /v1/runs ──────────────▶ Hermes  (plans, decides, remembers)
             GET  /v1/runs/{id} ◀── {"calls":[{"tool":"read_file",…}]} ── Hermes
             │
             └─ runs the calls in your repo (with your approval for run/write),
                feeds {"results":[…]} back, loops, until Hermes answers.
```

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

Linux / macOS / WSL. **One static binary, no runtime dependencies** — no `bash`,
`curl` or `jq` needed to run it, no language runtime, no package manager. (`git`
is used only for `git status` context on a failed command; `glow`/`bat`/`fmt` and
`diff` are used for prettier output/diffs when present.) Windows: use WSL.

```sh
curl -fsSL https://raw.githubusercontent.com/Zesales/hermes-hands/main/install.sh | sh
hermes-hands setup      # asks for your Hermes API URL + key, writes config
```

That drops **one self-contained binary** at `~/.local/bin/hermes-hands`. Unlike
the old bash script you can't `less` it, so releases ship SHA-256 checksums —
verify them, or build from source (below). Re-run the same `curl … | sh` any time
to update; it prefers a released platform binary and only builds from source (Go
required) when none is available.

`setup` writes `~/.config/hermes-hands/{config,secrets}` (secrets `chmod 600`)
and offers to source them from `~/.bashrc`. The key is your gateway's
`API_SERVER_KEY`; the URL must be `https://…`. `hermes-hands --version` prints the
version.

## Use

**A session is one task.** You continue it or start a new one — there is no
one-shot, so the central Hermes never fills up with throwaway sessions. When a
task is done, consciously `--new` (or `/new`) for the next one.

```sh
hermes-hands                       # continue this repo's latest session (main use)
hermes-hands --session             # same, explicitly
hermes-hands --session <id>        # open a specific session  (id from --session-list)
hermes-hands --new                 # start a fresh session (new task)
hermes-hands --rpc                 # JSON-lines session server for an editor/plugin

hermes-hands --session-list        # list sessions: id / turns / tokens / dir / title
hermes-hands sessions new          # mint a session id (prints it; for --session / --rpc)
hermes-hands check                 # preflight the connection
```

Scripts drive `--rpc` (send one `turn`, read the `answer`, close stdin) rather
than a per-call one-shot.

REPL commands: `/help` `/new` `/session` (this session's detail) `/session <id>`
(switch) `/sessions` `/compact [focus]` (ask Hermes to compact the context now)
`/setup` `/yolo` `/check` `/exit` (`Ctrl-D` also exits;
`Ctrl-C` at the prompt just hints, during a turn it cancels the turn). `/setup`
configures without leaving the session and the REPL starts even when
unconfigured. Tool calls are shown as they run
(`⟩ shell npm test… → exit 0`); the final answer renders through
`glow`/`bat`/`fmt` if installed.

Hermes drives these tools, all in the directory you launched from:

| tool | args | approval |
|---|---|---|
| `shell` | `{cmd, timeout?}` | **each command** (unless `--yolo`) |
| `read_file` | `{path}` | — (confined to the repo root) |
| `write_file` | `{path, content}` | **with a diff** |
| `edit_file` | `{path, old, new}` | **with a diff** (`old` must match once) |

`shell` is a **persistent login bash** rooted at your repo — `cd`, exported
vars, and shell functions/aliases from your `~/.bashrc` survive between calls in
a session, so Hermes operates it like a real terminal (`ls`, `rg`, `git`,
`make`, build/test) rather than a fixed toolbox. No tty: interactive programs
won't work. `read_file`/`write_file`/`edit_file` are structured helpers so the
model gets a clean diff and needn't fight shell quoting. `edit_file` replaces one
**literal** occurrence of `old` (no regex, no `&` metacharacters — a deliberate
change from the old bash version). `write_file`/`edit_file` need the target's
parent directory to already exist.

## Sessions

The conversation is threaded **server-side**: every `POST /v1/runs` carries a
stable `session_id` (Hermes loads that session's transcript as context), plus an
`X-Hermes-Session-Key` that's constant per repo (long-term memory handle) and an
`X-Hermes-Session-Id` header. If Hermes hands back a different `session_id`, the
CLI adopts it. If the server ever rejects the id, the CLI retries fresh (keeping
the headers) and falls back to a local transcript recap for that turn.

The `/v1` API has no endpoint to *list* sessions, so a thin local index lives in
`$XDG_STATE_HOME/hermes-hands/sessions/` purely to make a bare run (resume this
repo's latest), `--session <id>`, and `sessions` work offline. Titles are also
mirrored into Hermes via a best-effort `PATCH /api/sessions/{id}`.

`/session` (in-REPL) and `--session-list` show per-session **turns**, the last
run's **tokens** (cumulative billing `usage`), and **splits** — the number of
times Hermes handed back a *different* `session_id`, which a server-side
compaction / session-split causes, so it's a rough compaction counter. Real
context-window usage and a true compaction count are not exposed by the Hermes
API yet ([hermes-agent#15618](https://github.com/NousResearch/hermes-agent/issues/15618)).

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
{"id":2,"type":"new"}                                   // start a fresh session, becomes current
{"id":3,"type":"use","session":"hh_20260902T…_abc123"}  // switch (id from `sessions new`)
{"id":4,"type":"check"}
{"id":5,"type":"compact","text":"auth flow"}              // optional focus; asks Hermes to compact now

// responses
{"type":"ready","session":"hh_…","cwd":"/repo","version":"0.3.0","approvals":"off"}
{"type":"tool","id":1,"tool":"shell","preview":"npm test","exit":0}
{"type":"answer","id":1,"ok":true,"text":"…","session":"hh_…","hermes_session":"…"}
{"type":"session","id":2,"session":"hh_…","hermes_session":"…"}
{"type":"check","id":4,"ok":true,"model":"…","base":"…"}
{"type":"compact","id":5,"ok":true,"session":"hh_…"}
{"type":"error","id":1,"message":"…"}
```

Approvals can't be prompted over the pipe, so `--rpc` requires `--yolo`
(`HERMES_HANDS_APPROVE=auto`) or `HERMES_HANDS_APPROVE=never` — the editor is
expected to run its own approval UI before sending a `turn`. Close stdin to shut
it down; `SIGINT` cancels the in-flight turn.

Persist a session across editor restarts with `hermes-hands sessions new` (mint
+ print an id) and pass it back via `--session <id>` or an rpc `use` message.

## Config

`~/.config/hermes-hands/config` — `KEY=value` / `export KEY=value`, values
optionally `"…"`, `'…'` or `$'…'`-quoted. Anything else on a line is ignored (the
old bash version `source`d the file; the Go port deliberately parses only
assignments).

**Secrets.** The URL + key are resolved in this order: the `HERMES_API_URL` /
`HERMES_API_KEY` environment variables always win; then, only while one is still
unset, the secrets store. `setup` (default) writes a machine-bound
`secrets.enc` + a 0600 `keyseed` — AES-256-GCM, key derived per-machine
(HKDF over the keyseed, `/etc/machine-id` and your uid), so a copied
`secrets.enc` alone is useless. It needs nothing in your shell env. If it can't
be decrypted here (wrong machine, tampered, missing `keyseed`) the CLI says so
and exits — re-run `hermes-hands setup`. This is **at-rest protection only**: it
does not stop a program running as you (or an approved `shell`) from reading the
key — the approval gate + denylist are the real boundary. `setup --plaintext`
keeps the old 0600 `secrets` file (`export HERMES_API_URL=…` / `…KEY=…`) plus the
`~/.bashrc` offer, for people who inject via env or a secrets manager; that file
is the fallback when no `secrets.enc` exists. See [`docs/secrets.md`](docs/secrets.md).

| key | default | meaning |
|---|---|---|
| `HERMES_API_URL` | — | gateway base, `https://…` |
| `HERMES_API_KEY` | — | `API_SERVER_KEY` (put this in `secrets`, `chmod 600`) |
| `HERMES_API_PROFILE` | — | route to `/p/<profile>/` (needs that profile's own key) |
| `HERMES_HANDS_APPROVE` | `ask` | `ask` \| `auto` \| `never` |
| `HERMES_HANDS_DENY` | — | extra denied `shell` commands, `\|`-separated shell globs |
| `HERMES_HANDS_MAX_ROUNDS` | `8` | delegation rounds per turn |
| `HERMES_HANDS_RUN_TIMEOUT` | `120` | per-command seconds |
| `HERMES_HANDS_MAX_OUTPUT` | `20000` | bytes kept per tool result (`shell` keeps 2×) |
| `HERMES_API_*` | — | `CONNECT_TIMEOUT` 5, `MAX_TIME` 30, `POLL_INTERVAL` 2, `RETRIES` 3, `RUN_TIMEOUT` 600 |

The per-run `instructions` block sent to Hermes is baked into the binary (source:
`share/instructions.md`). Drop a `~/.config/hermes-hands/instructions.md` to
override it with repo-specific rules. Either way it's scoped to `hermes-hands`
runs only — your phone and web UI never see it.

## Security model

- **Outbound only.** The CLI dials your gateway. Nothing listens on your machine.
- **Approval by default** for `shell`, `write_file`, `edit_file` — you see the
  command or diff and confirm (`y`/`n`/`a`ll/`q`uit). `--yolo` or
  `HERMES_HANDS_APPROVE=auto` turns it off.
- **Denylist** for `shell`: ssh/scp/sftp/rsync, sudo/doas, `rm -rf /…`, fork
  bombs, `curl|wget … | sh` — plus your `HERMES_HANDS_DENY` globs.
- **Repo jail** for the structured file tools: `read_file`/`write_file`/`edit_file`
  refuse paths that resolve (symlinks included) outside the directory you
  launched from. `shell` is a real shell (it can `cd` anywhere) — it relies on
  the denylist + per-command approval, not a path jail.
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

## Honest limitations

- It's a **workaround** for an unmerged feature. Reliability depends on the model
  behind your Hermes API emitting the JSON envelope cleanly; the CLI recovers
  from fenced/prosey/botched JSON but a weak model will still be flaky. Tune
  `instructions.md`.
- The delegation transcript accumulates in the Hermes session. Long sessions lean
  on Hermes' compaction; start a `/new` session for a new task.
- No streaming of the final answer yet (the loop polls run status). The transport
  is isolated behind one client so an SSE reader drops in when split-runtime
  lands.
- `shell` is POSIX-only; on Windows use WSL.

## Development

```sh
git clone https://github.com/Zesales/hermes-hands && cd hermes-hands
make dev-install     # go build + copy dist/hermes-hands onto your PATH
make test            # go test ./... — offline, no network, no model
make lint            # gofmt check + go vet   (STATICCHECK=1 also runs staticcheck)
```

Go 1.26.7, `CGO_ENABLED=0`. Dependencies (`github.com/peterh/liner` +
`github.com/mattn/go-runewidth` + `golang.org/x/sys`) are **vendored** — builds
and CI never touch the network. Layout: `main.go` (CLI + REPL + setup) +
`internal/{config,redact,ttyio,prompt,ui,api,session,shell,dispatch,loop}` +
`share/instructions.md` (embedded via `//go:embed`). CI runs gofmt, `go vet`,
`go test`, and a windows/darwin cross-compile smoke on every push.

`test/mock_hermes.py` is a manual-only stand-in for the Runs API — the Go suite
has its own in-process mock (`internal/hermesmock`) and needs no `python3`.

## Releasing

Bump `VERSION`, tag `vX.Y.Z`, then `make release` — it cross-compiles
`dist/hermes-hands_<os>_<arch>` for `{linux,darwin}×{amd64,arm64}` and
`windows/amd64`. Upload those (plus SHA-256 sums) as release assets;
`install.sh` fetches `hermes-hands_<os>_<arch>` and only builds from source
when no matching asset exists.

## License

MIT.
