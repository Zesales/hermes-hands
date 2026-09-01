#!/usr/bin/env bash
# build.sh - bundle the repo into a single self-contained executable.
#   ./build.sh [out]        default: dist/hermes-hands
# The result needs only bash + curl + jq at runtime (rg/glow/bat used if present).
set -euo pipefail
cd -- "$(dirname -- "$0")"

out="${1:-dist/hermes-hands}"
mkdir -p -- "$(dirname -- "$out")"
ver="$(tr -d '[:space:]' < VERSION)"
sha="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
# shellcheck disable=SC1091
[ -r project.env ] && . ./project.env
slug="${REPO_SLUG:-$(git remote get-url origin 2>/dev/null | sed -E 's#(git@github.com:|https://github.com/)##; s#\.git$##')}"
slug="${slug:-your/hermes-hands}"
libs="lib/util.sh lib/ui.sh lib/session.sh lib/api.sh lib/dispatch.sh lib/loop.sh"

{
  printf '#!/usr/bin/env bash\n'
  printf '# hermes-hands %s (%s) - single-file build. https://github.com/%s\n' "$ver" "$sha" "$slug"
  printf 'set -euo pipefail\n'
  printf 'HH_VERSION=%q; HH_BUILD_SHA=%q; HH_BUNDLED=1; HH_ROOT=\n' "$ver" "$sha"
  # instructions.md is markdown (backticks, $, quotes) - carry it as base64 so no
  # escaping can break the bundle. The delimiter can't collide with base64 chars.
  printf "HH_INSTRUCTIONS_BUILTIN=\"\$(base64 -d <<'__HH_INSTR_B64__'\n"
  base64 < share/instructions.md
  printf '__HH_INSTR_B64__\n)"\n\n'

  for f in $libs; do
    printf '# ==== %s ====\n' "$f"
    sed -e '1{/^#!/d}' -- "$f"
    printf '\n'
  done

  printf '# ==== bin/hermes-hands (entry) ====\n'
  # everything after the (dropped) bundle block
  awk 'f; /^# <<< bundle:drop$/{f=1}' bin/hermes-hands
} > "$out"

chmod +x -- "$out"
bash -n -- "$out"
"$out" --version >/dev/null
lines="$(wc -l < "$out")"
printf 'built %s  (%s %s, %s lines, %s)\n' "$out" "$ver" "$sha" "$lines" "$(du -h "$out" | cut -f1)"
