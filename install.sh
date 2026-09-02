#!/bin/sh
# hermes-hands installer (Linux / WSL).
#
#   curl -fsSL https://raw.githubusercontent.com/<you>/hermes-hands/main/install.sh | sh
#
# Clones (or updates) the repo and links bin/hermes-hands onto your PATH.
# Re-run any time to update. Windows: use WSL (a PowerShell installer is a
# later feature).
set -eu

REPO_URL="${HERMES_HANDS_REPO:-https://github.com/CHANGE-ME/hermes-hands}"
DEST="${HERMES_HANDS_DIR:-${XDG_DATA_HOME:-$HOME/.local/share}/hermes-hands}"
BIN="${HERMES_HANDS_BIN:-$HOME/.local/bin}"

missing=
for c in bash curl jq git; do
  command -v "$c" >/dev/null 2>&1 || missing="$missing $c"
done
if [ -n "$missing" ]; then
  echo "hermes-hands: missing dependencies:$missing" >&2
  echo "  Debian/Ubuntu:  sudo apt-get install -y$missing" >&2
  echo "  Fedora:         sudo dnf install -y$missing" >&2
  exit 1
fi

if [ -d "$DEST/.git" ]; then
  echo "hermes-hands: updating $DEST"
  git -C "$DEST" pull --ff-only
else
  echo "hermes-hands: cloning into $DEST"
  mkdir -p "$(dirname "$DEST")"
  git clone --depth 1 "$REPO_URL" "$DEST"
fi

mkdir -p "$BIN"
ln -sf "$DEST/bin/hermes-hands" "$BIN/hermes-hands"
chmod +x "$DEST/bin/hermes-hands" 2>/dev/null || true

echo
"$BIN/hermes-hands" --version
case ":$PATH:" in
  *":$BIN:"*) ;;
  *) echo "hermes-hands: add $BIN to your PATH  (export PATH=\"$BIN:\$PATH\")" ;;
esac
echo "next: hermes-hands setup"
