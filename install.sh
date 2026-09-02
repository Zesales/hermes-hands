#!/bin/sh
# hermes-hands installer (Linux / WSL). Produces one self-contained file at
#   ~/.local/bin/hermes-hands
#
#   curl -fsSL https://raw.githubusercontent.com/CHANGE-ME/hermes-hands/main/install.sh | sh
#
# Prefers a released single-file binary; falls back to cloning + building.
# Re-run any time to update. Windows: use WSL (PowerShell installer is later).
set -eu

REPO="${HERMES_HANDS_REPO:-CHANGE-ME/hermes-hands}"
BIN="${HERMES_HANDS_BIN:-$HOME/.local/bin}"
SRC="${HERMES_HANDS_DIR:-${XDG_DATA_HOME:-$HOME/.local/share}/hermes-hands-src}"
TARGET="$BIN/hermes-hands"

missing=
for c in bash curl jq; do command -v "$c" >/dev/null 2>&1 || missing="$missing $c"; done
if [ -n "$missing" ]; then
  echo "hermes-hands: missing:$missing" >&2
  echo "  Debian/Ubuntu:  sudo apt-get install -y$missing" >&2
  echo "  Fedora:         sudo dnf install -y$missing" >&2
  exit 1
fi

mkdir -p "$BIN"

# 1) try a released single-file
url="https://github.com/$REPO/releases/latest/download/hermes-hands"
if curl -fsSL -o "$TARGET.new" "$url" 2>/dev/null && head -1 "$TARGET.new" | grep -q '^#!/usr/bin/env bash'; then
  chmod +x "$TARGET.new"; mv "$TARGET.new" "$TARGET"
  echo "hermes-hands: installed from release -> $TARGET"
else
  rm -f "$TARGET.new"
  # 2) clone + build
  command -v git >/dev/null 2>&1 || { echo "hermes-hands: no release yet and 'git' missing for the source build" >&2; exit 1; }
  if [ -d "$SRC/.git" ]; then git -C "$SRC" pull --ff-only; else
    mkdir -p "$(dirname "$SRC")"; git clone --depth 1 "https://github.com/$REPO" "$SRC"
  fi
  ( cd "$SRC" && ./build.sh "$SRC/dist/hermes-hands" )
  install -m 0755 "$SRC/dist/hermes-hands" "$TARGET"
  echo "hermes-hands: built from source -> $TARGET"
fi

echo
"$TARGET" --version
case ":$PATH:" in *":$BIN:"*) ;; *) echo "hermes-hands: add $BIN to PATH  (export PATH=\"$BIN:\$PATH\")" ;; esac
echo "next: hermes-hands setup"
