#!/bin/sh
# hermes-hands installer (Linux / macOS / WSL). Drops one static binary at
#   ~/.local/bin/hermes-hands
#
#   curl -fsSL https://raw.githubusercontent.com/Zesales/hermes-hands/main/install.sh | sh
#
# Prefers a released platform binary; falls back to building from source with Go.
# Re-run any time to update. Windows: use WSL.
set -eu

REPO="${HERMES_HANDS_REPO:-Zesales/hermes-hands}"
BIN="${HERMES_HANDS_BIN:-$HOME/.local/bin}"
SRC="${HERMES_HANDS_DIR:-${XDG_DATA_HOME:-$HOME/.local/share}/hermes-hands-src}"
TARGET="$BIN/hermes-hands"

command -v curl >/dev/null 2>&1 || { echo "hermes-hands: 'curl' is required" >&2; exit 1; }

case "$(uname -s)" in
  Linux)  os=linux ;;
  Darwin) os=darwin ;;
  *) echo "hermes-hands: unsupported OS '$(uname -s)' - use WSL, or build from source with Go" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64|amd64)  arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) echo "hermes-hands: unsupported CPU '$(uname -m)'" >&2; exit 1 ;;
esac

mkdir -p "$BIN"
asset="hermes-hands_${os}_${arch}"
url="https://github.com/$REPO/releases/latest/download/$asset"

if curl -fsSL -o "$TARGET.new" "$url" 2>/dev/null && chmod +x "$TARGET.new" && "$TARGET.new" --version >/dev/null 2>&1; then
  mv "$TARGET.new" "$TARGET"
  echo "hermes-hands: installed from release -> $TARGET"
else
  rm -f "$TARGET.new"
  command -v git >/dev/null 2>&1 || { echo "hermes-hands: no usable release and 'git' missing for the source build" >&2; exit 1; }
  command -v go  >/dev/null 2>&1 || { echo "hermes-hands: no usable release and 'go' missing for the source build" >&2; exit 1; }
  if [ -d "$SRC/.git" ]; then
    git -C "$SRC" pull --ff-only
  else
    mkdir -p "$(dirname "$SRC")"
    git clone --depth 1 "https://github.com/$REPO" "$SRC"
  fi
  ( cd "$SRC" && CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X main.version=$(tr -d '[:space:]' < VERSION)" -o "$TARGET" . )
  echo "hermes-hands: built from source -> $TARGET"
fi

echo
"$TARGET" --version
case ":$PATH:" in
  *":$BIN:"*) ;;
  *) echo "hermes-hands: add $BIN to PATH  (export PATH=\"$BIN:\$PATH\")" ;;
esac
echo "next: hermes-hands setup"
