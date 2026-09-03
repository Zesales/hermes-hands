#!/bin/sh
# Build hermes-hands into dist/ — the exact steps the release workflow runs, so
# a local `./build.sh` and CI produce identical binaries.
#
#   ./build.sh          cross-compile every target + write dist/SHA256SUMS
#   ./build.sh host     only this host's binary          -> dist/hermes-hands
#
# VERSION and the short git sha are baked in via -ldflags (see the Makefile).
set -eu
cd "$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"

sha256() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$@"; else shasum -a 256 "$@"; fi; }

case "${1:-all}" in
  host)
    make build
    ;;
  all)
    make release
    ( cd dist && sha256 hermes-hands_* > SHA256SUMS )
    echo
    echo "dist/:"
    ls -1 dist
    ;;
  -h|--help)
    printf '%s\n' \
      "usage: ./build.sh [host|all]" \
      "  all  (default)  cross-compile every target + dist/SHA256SUMS" \
      "  host           only this host's binary -> dist/hermes-hands"
    ;;
  *)
    echo "usage: ./build.sh [host|all]" >&2
    exit 2
    ;;
esac
