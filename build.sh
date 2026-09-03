#!/bin/sh
# Build hermes-hands into dist/.
#
#   ./build.sh            this machine's binary           -> dist/hermes-hands
#   ./build.sh release    every target + dist/SHA256SUMS  (what the release
#                         workflow publishes; identical output)
#
# VERSION and the short git sha are baked in via -ldflags (see the Makefile).
set -eu
cd "$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"

sha256() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$@"; else shasum -a 256 "$@"; fi; }

case "${1:-host}" in
  host|'')
    make build
    ;;
  release)
    make clean release
    ( cd dist && sha256 hermes-hands_* > SHA256SUMS )
    echo
    echo "dist/:"
    ls -1 dist
    ;;
  -h|--help)
    printf '%s\n' \
      "usage: ./build.sh [host|release]" \
      "  host  (default)  this machine's binary  -> dist/hermes-hands" \
      "  release          every target + dist/SHA256SUMS"
    ;;
  *)
    echo "usage: ./build.sh [host|release]" >&2
    exit 2
    ;;
esac
