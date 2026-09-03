#!/bin/sh
# hermes-hands installer (Linux / macOS / WSL). Drops one static binary at
#   ~/.local/bin/hermes-hands
#
#   curl -fsSL https://raw.githubusercontent.com/Zesales/hermes-hands/main/install.sh | sh
#
# Default: download the latest GitHub release for your OS/CPU, verify its
# SHA-256, install it. Re-run any time to update. Options:
#
#   --version X.Y.Z   install that exact release (tag vX.Y.Z), not latest
#   --local           build from the current checkout (needs Go) and install that
#   --source          git-clone + build from source (needs git + Go)
#   --help
#
# Env: HERMES_HANDS_REPO (default Zesales/hermes-hands), HERMES_HANDS_BIN
#      (default ~/.local/bin), HERMES_HANDS_DIR (--source checkout dir).
set -eu

REPO="${HERMES_HANDS_REPO:-Zesales/hermes-hands}"
BIN="${HERMES_HANDS_BIN:-$HOME/.local/bin}"
SRC="${HERMES_HANDS_DIR:-${XDG_DATA_HOME:-$HOME/.local/share}/hermes-hands-src}"
TARGET="$BIN/hermes-hands"

usage() {
  cat >&2 <<'EOF'
hermes-hands installer

  install.sh                 latest release for this OS/CPU (SHA-256 verified)
  install.sh --version X.Y.Z pin to release vX.Y.Z
  install.sh --local         build from the current checkout (needs Go)
  install.sh --source        git-clone + build from source (needs git + Go)
  install.sh --help
EOF
}

die()  { echo "hermes-hands: $*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }
sha256() { if have sha256sum; then sha256sum "$@"; else shasum -a 256 "$@"; fi; }

mode=release
want_version=
while [ $# -gt 0 ]; do
  case "$1" in
    --version)   want_version="${2:-}"; [ -n "$want_version" ] || die "--version needs X.Y.Z"; shift 2 ;;
    --version=*) want_version="${1#*=}"; shift ;;
    --local)     mode=local;  shift ;;
    --source)    mode=source; shift ;;
    -h|--help)   usage; exit 0 ;;
    *) echo "hermes-hands: unknown option '$1'" >&2; usage; exit 2 ;;
  esac
done

mkdir -p "$BIN"

build_and_install() { # $1 = directory holding the go module
  have go || die "'go' is required for a source build"
  ( cd "$1"
    v=$({ git describe --tags --match 'v[0-9]*.[0-9]*.[0-9]*' --dirty 2>/dev/null || echo 0.0.0-dev; } | sed 's/^v//')
    sha=$(git rev-parse --short HEAD 2>/dev/null || echo unknown)
    CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X main.version=$v -X main.commit=$sha" -o "$TARGET" . )
}

if [ "$mode" = local ]; then
  [ -f go.mod ] || die "--local must run from a hermes-hands checkout (no go.mod here)"
  build_and_install .
  echo "hermes-hands: built from ./ -> $TARGET"
  echo; "$TARGET" --version; exit 0
fi

if [ "$mode" = source ]; then
  have git || die "'git' is required for --source"
  if [ -d "$SRC/.git" ]; then
    git -C "$SRC" pull --ff-only
  else
    mkdir -p "$(dirname "$SRC")"
    git clone --depth 1 "https://github.com/$REPO" "$SRC"
  fi
  build_and_install "$SRC"
  echo "hermes-hands: built from source -> $TARGET"
  echo; "$TARGET" --version; exit 0
fi

# --- release download --------------------------------------------------------
have curl || die "'curl' is required"
case "$(uname -s)" in
  Linux)  os=linux ;;
  Darwin) os=darwin ;;
  *) die "unsupported OS '$(uname -s)' — use WSL, or install.sh --source" ;;
esac
case "$(uname -m)" in
  x86_64|amd64)  arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) die "unsupported CPU '$(uname -m)'" ;;
esac
asset="hermes-hands_${os}_${arch}"

if [ -n "$want_version" ]; then
  base="https://github.com/$REPO/releases/download/v${want_version#v}"
else
  base="https://github.com/$REPO/releases/latest/download"
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

curl -fsSL -o "$tmp/$asset" "$base/$asset" \
  || die "no asset $asset at $base — check --version, or use install.sh --source"

if curl -fsSL -o "$tmp/SHA256SUMS" "$base/SHA256SUMS" 2>/dev/null; then
  want=$(awk -v f="$asset" '$2==f || $2=="*"f {print $1}' "$tmp/SHA256SUMS")
  [ -n "$want" ] || die "SHA256SUMS has no entry for $asset"
  got=$(sha256 "$tmp/$asset" | awk '{print $1}')
  [ "$want" = "$got" ] || die "SHA-256 mismatch for $asset (want $want, got $got)"
  echo "hermes-hands: SHA-256 verified"
else
  echo "hermes-hands: WARNING: no SHA256SUMS for this release — cannot verify" >&2
fi

chmod +x "$tmp/$asset"
"$tmp/$asset" --version >/dev/null 2>&1 || die "downloaded binary does not run on this host"
mv "$tmp/$asset" "$TARGET"
echo "hermes-hands: installed ${want_version:+v${want_version#v} }release -> $TARGET"

echo
"$TARGET" --version
case ":$PATH:" in
  *":$BIN:"*) ;;
  *) echo "hermes-hands: add $BIN to PATH  (export PATH=\"$BIN:\$PATH\")" ;;
esac
echo "next: hermes-hands setup"
