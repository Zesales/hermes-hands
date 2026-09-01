# hermes-code

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

`hermes-code` is the **outbound bridge** until split-runtime lands. It uses only
the merged Runs API. Hermes replies with a small JSON envelope of tool calls;
the CLI runs them and feeds structured results back. The envelope shape is the
same one split-runtime expects, so when it merges the CLI switches to native tool
calls and this convention becomes the fallback — **the local tool dispatcher does
not change.**

## Install

Requires `bash`, `curl`, `jq` (and `git` / `rg` for the obvious tools). No model,
no runtime, no package manager.

```sh
git clone https://github.com/<you>/hermes-code-cli
ln -s "$PWD/hermes-code-cli/bin/hermes-code" ~/.local/bin/hermes-code
hermes-code setup            # asks for your Hermes API URL + key, writes config
```

`setup` writes `~/.config/hermes-code/{config,secrets}` (secrets `chmod 600`) and
offers to source them from `~/.bashrc`. The key is your gateway's
`API_SERVER_KEY`; the URL should be `https://…` (the token authenticates every
call).

## Use

```sh
hermes-code                       # REPL, rooted at the current directory
hermes-code "why is CI failing?"  # one-shot
hermes-code -c "and now fix it"   # continue this directory's latest session
hermes-code --new "…"             # force a fresh session
hermes-code --session <id> "…"    # a specific session
hermes-code --yolo "…"            # skip approval prompts
hermes-code sessions              # list local sessions
hermes-code check                 # preflight the connection
```

REPL commands: `/exit` `/check` `/new` `/sessions`.

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

Each turn threads via the Runs API (`previous_response_id`, then `session_id`,
then a local transcript recap if the server drops continuity). State lives in
`$XDG_STATE_HOME/hermes-code/sessions/`. `-c` continues the latest session for the
current directory; `--session <id>` picks one; `sessions` lists them.

## Config

`~/.config/hermes-code/config` (`KEY=value`, sourced; env wins):

| key | default | meaning |
|---|---|---|
| `HERMES_API_URL` | — | gateway base, `https://…` |
| `HERMES_API_KEY` | — | `API_SERVER_KEY` (put this in `secrets`, `chmod 600`) |
| `HERMES_API_PROFILE` | — | route to `/p/<profile>/` (needs that profile's own key) |
| `HERMES_CODE_APPROVE` | `ask` | `ask` \| `auto` \| `never` |
| `HERMES_CODE_DENY` | — | extra denied `run` commands, `\|`-separated shell globs |
| `HERMES_CODE_MAX_ROUNDS` | `8` | delegation rounds per turn |
| `HERMES_CODE_RUN_TIMEOUT` | `120` | per-command seconds |
| `HERMES_CODE_MAX_OUTPUT` | `20000` | bytes kept per tool result |

The per-run `instructions` block sent to Hermes is `share/instructions.md` (or
`~/.config/hermes-code/instructions.md` if you want repo-specific rules). It is
scoped to `hermes-code` runs only — your phone and web UI never see it.

## Security model

- **Outbound only.** The CLI dials your gateway. Nothing listens on your machine.
- **Approval by default** for `run`, `write_file`, `edit_file` — you see the
  command or diff and confirm (`y`/`n`/`a`ll/`q`uit). `--yolo` or
  `HERMES_CODE_APPROVE=auto` turns it off.
- **Denylist** for `shell`: ssh/scp/rsync, sudo/doas, `rm -rf /`, fork bombs,
  pipe-to-shell downloads — plus your `HERMES_CODE_DENY` globs.
- **Repo jail** for the structured file tools: `read_file`/`write_file`/`edit_file`
  refuse paths that resolve outside the directory you launched from. `shell` is a
  real shell (it can `cd` anywhere) — it relies on the denylist + per-command
  approval, not a path jail.
- **Secret scrubbing.** Tool output is passed through a redactor (bearer tokens,
  `api_key=`/`password=`, AWS keys, `sk-…`, `ghp_…`, PEM headers) before it goes
  back to the gateway.
- **TLS enforced.** A non-`https://` `HERMES_API_URL` is refused (loopback needs
  an explicit `HERMES_CODE_ALLOW_HTTP=1`, for tests).

## Honest limitations

- It's a **workaround** for an unmerged feature. Reliability depends on the model
  behind your Hermes API emitting the JSON envelope cleanly; the CLI recovers
  from fenced/prosey/botched JSON but a weak model will still be flaky. Tune
  `instructions.md`.
- The delegation transcript accumulates in the Hermes session. Long sessions lean
  on Hermes' compaction; start a `/new` session for a new task.
- No streaming of the final answer yet (the loop polls run status).

## Tests

`./test/run_tests.sh` — offline, against `test/mock_hermes.py`. No network, no
model. CI runs shellcheck + these on every push.

## License

MIT.
