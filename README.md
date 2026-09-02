# hermes-hands

**Terminal coding with a remote [Hermes](https://github.com/NousResearch/hermes-agent)
brain.** Your Hermes gateway holds the plan, the memory and the persona; a small
bash CLI on your machine lets it read files and run commands in the repo you're
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

Linux / WSL. Runtime needs only `bash`, `curl`, `jq` (`rg`, `glow`/`bat` used if
present). No model, no language runtime, no package manager. Windows: use WSL — a
PowerShell installer is a later feature.

```sh
curl -fsSL https://raw.githubusercontent.com/Zesales/hermes-hands/main/install.sh | sh
hermes-hands setup      # asks for your Hermes API URL + key, writes config
```

That drops **one self-contained file** at `~/.local/bin/hermes-hands` — plain
bash you can `less`, so you can see exactly what it will run. Re-run the same
`curl … | sh` any time to update.

`setup` writes `~/.config/hermes-hands/{config,secrets}` (secrets `chmod 600`)
and offers to source them from `~/.bashrc`. The key is your gateway's
`API_SERVER_KEY`; the URL must be `https://…`. `hermes-hands --version` prints the
version.

## Use

The primary mode is the **REPL** — coding is multi-turn (look, ask, look again,
change, verify) and each turn threads into the same Hermes session. The one-shot
forms are for quick questions and scripting.

```sh
hermes-hands                       # REPL, rooted at the current directory
hermes-hands "why is CI failing?"  # one-shot, plain output
hermes-hands -c "and now fix it"   # continue this directory's latest session
hermes-hands --new "…"             # force a fresh session
hermes-hands --session <id> "…"    # a specific session
hermes-hands --yolo "…"            # skip approval prompts
hermes-hands sessions              # list local sessions
hermes-hands check                 # preflight the connection
```

REPL commands: `/help` `/new` `/sessions` `/check` `/exit`. Tool calls are shown
as they run (`⟩ shell npm test… → exit 0`); the final answer renders through
`glow`/`bat` if installed.

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
model gets a clean diff and needn't fight shell quoting.

## Sessions

The conversation is threaded **server-side**: every `POST /v1/runs` carries a
stable `session_id` (Hermes loads that session's transcript as context), plus an
`X-Hermes-Session-Key` that's constant per repo (long-term memory handle) and an
`X-Hermes-Session-Id` header. If Hermes hands back a different `session_id`, the
CLI adopts it. If the server ever rejects the id, the CLI retries fresh and
falls back to a local transcript recap for that turn.

The `/v1` API has no endpoint to *list* sessions, so a thin local index lives in
`$XDG_STATE_HOME/hermes-hands/sessions/` purely to make `-c` (continue this
repo's latest), `--session <id>`, and `sessions` work offline. Titles are also
mirrored into Hermes via a best-effort `PATCH /api/sessions/{id}`.

## Config

`~/.config/hermes-hands/config` (`KEY=value`, sourced; env wins):

| key | default | meaning |
|---|---|---|
| `HERMES_API_URL` | — | gateway base, `https://…` |
| `HERMES_API_KEY` | — | `API_SERVER_KEY` (put this in `secrets`, `chmod 600`) |
| `HERMES_API_PROFILE` | — | route to `/p/<profile>/` (needs that profile's own key) |
| `HERMES_HANDS_APPROVE` | `ask` | `ask` \| `auto` \| `never` |
| `HERMES_HANDS_DENY` | — | extra denied `run` commands, `\|`-separated shell globs |
| `HERMES_HANDS_MAX_ROUNDS` | `8` | delegation rounds per turn |
| `HERMES_HANDS_RUN_TIMEOUT` | `120` | per-command seconds |
| `HERMES_HANDS_MAX_OUTPUT` | `20000` | bytes kept per tool result |

The per-run `instructions` block sent to Hermes is baked into the binary (source:
`share/instructions.md`). Drop a `~/.config/hermes-hands/instructions.md` to
override it with repo-specific rules. Either way it's scoped to `hermes-hands`
runs only — your phone and web UI never see it.

## Security model

- **Outbound only.** The CLI dials your gateway. Nothing listens on your machine.
- **Approval by default** for `run`, `write_file`, `edit_file` — you see the
  command or diff and confirm (`y`/`n`/`a`ll/`q`uit). `--yolo` or
  `HERMES_HANDS_APPROVE=auto` turns it off.
- **Denylist** for `shell`: ssh/scp/rsync, sudo/doas, `rm -rf /`, fork bombs,
  pipe-to-shell downloads — plus your `HERMES_HANDS_DENY` globs.
- **Repo jail** for the structured file tools: `read_file`/`write_file`/`edit_file`
  refuse paths that resolve outside the directory you launched from. `shell` is a
  real shell (it can `cd` anywhere) — it relies on the denylist + per-command
  approval, not a path jail.
- **Secret scrubbing.** Tool output is passed through a redactor (bearer tokens,
  `api_key=`/`password=`, AWS keys, `sk-…`, `ghp_…`, PEM headers) before it goes
  back to the gateway.
- **TLS enforced.** A non-`https://` `HERMES_API_URL` is refused (loopback needs
  an explicit `HERMES_HANDS_ALLOW_HTTP=1`, for tests).

## Honest limitations

- It's a **workaround** for an unmerged feature. Reliability depends on the model
  behind your Hermes API emitting the JSON envelope cleanly; the CLI recovers
  from fenced/prosey/botched JSON but a weak model will still be flaky. Tune
  `instructions.md`.
- The delegation transcript accumulates in the Hermes session. Long sessions lean
  on Hermes' compaction; start a `/new` session for a new task.
- No streaming of the final answer yet (the loop polls run status).

## Development

```sh
git clone https://github.com/Zesales/hermes-hands && cd hermes-hands
make dev-install     # symlink bin/hermes-hands onto your PATH (runs from the checkout)
make test            # offline suite against test/mock_hermes.py - no network, no model
make lint            # shellcheck
```

Source layout: `bin/hermes-hands` (entry) + `lib/*.sh` (util, ui, session, api,
dispatch, loop) + `share/instructions.md`. `build.sh` bundles all of it into the
single `dist/hermes-hands` (libs inlined, instructions base64'd) — that's what a
release ships and what `install.sh` fetches. CI runs shellcheck, the offline
suite, and the bundle build on every push.

**Releasing:** bump `VERSION`, tag `vX.Y.Z`, `make build`, upload `dist/hermes-hands`
as a release asset named `hermes-hands`. `install.sh` prefers that asset and only
clones + builds from source when no release exists.

## License

MIT.
