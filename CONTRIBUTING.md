# Contributing to hermes-hands

Thanks for helping. hermes-hands is small on purpose — one static Go binary,
one direct dependency, nothing listening on the operator's machine — so the
bar for a change is "does it keep that true?".

## Before you start

- **A bug?** Open an issue with the bug template: it asks for
  `hermes-hands --version` and the hermes-agent version (`hermes-hands
  check`), which is half of every diagnosis.
- **A bigger change** (a new tool, a protocol change, a new dependency)? Open
  an issue first, so nobody builds something that can't land.

## Build and test

Everything runs in the pinned Go container; the host needs Docker and `make`.
The README's [Development](README.md#development) section has the details.

```sh
make            # the targets, grouped
make dev        # build, run an isolated instance out of ./.dev/
make ci         # the whole PR gate: lint (linux + darwin) + test + cross-build
```

`make test` is offline — no network, no model (`internal/hermesmock` stands
in for hermes-agent). It includes the **golden transcripts** in
`testdata/golden/`: what the REPL and `--rpc` show for one full turn. A change
that is meant to alter that output runs `make golden` and commits the
rewritten files — the diff is part of the review. If your change touches how hermes-hands talks to
hermes-agent, also try it against a real gateway and name its version in the
PR.

## Pull requests

- **Exactly one bump label**: `bump:patch` | `bump:minor` | `bump:major` |
  `bump:none`. Merging cuts the release; see [Releasing](README.md#releasing).
- **CHANGELOG**: an entry under the top (unreleased) heading — what changed
  for the operator, and why.
- **Tests** for behaviour changes; `make ci` green.
- **Match the surrounding code**: names, comment density, idiom. One function,
  one job; comments say why, not what.
- **Dependencies**: none new without an issue first. Versions are pinned
  exactly — Go modules, the Go image, GitHub Actions (by commit).

## hermes-agent versions

hermes-hands supports the hermes-agent range in `internal/compat` (README:
**Works with**). A change that needs a newer hermes-agent moves that range —
say so in the PR and the CHANGELOG.
