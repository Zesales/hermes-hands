#!/usr/bin/env bash
# ui.sh - terminal presentation for the REPL. Colour only on a tty; degrades to
# clean plain text in a pipe. No alt-screen, no panes - a well-framed scrolling
# conversation.

if [[ -t 2 && "${NO_COLOR:-}" == "" && "${TERM:-dumb}" != dumb ]]; then
  UI_DIM=$'\033[2m'; UI_B=$'\033[1m'; UI_R=$'\033[0m'
  UI_YOU=$'\033[36m'; UI_HERMES=$'\033[32m'; UI_OK=$'\033[32m'; UI_BAD=$'\033[31m'; UI_ACC=$'\033[35m'
else
  UI_DIM= UI_B= UI_R= UI_YOU= UI_HERMES= UI_OK= UI_BAD= UI_ACC=
fi

ui_cols() { local c="${COLUMNS:-}"; [[ -z "$c" ]] && c="$(tput cols 2>/dev/null || echo 96)"; (( c > 120 )) && c=120; (( c < 40 )) && c=40; printf '%s' "$c"; }

ui_rule() {
  local w line; w="$(ui_cols)"; printf -v line '%*s' "$w" ''
  printf '%s%s%s\n' "$UI_DIM" "${line// /$'\xe2\x94\x80'}" "$UI_R" >&2
}

ui_you()    { printf '%s%syou%s\n' "$UI_B" "$UI_YOU" "$UI_R" >&2; printf '%s\n' "$1" | sed 's/^/   /' >&2; }
ui_working(){ printf '%s   %s\xe2\x8b\xaf working%s\n' "$UI_DIM" "$UI_ACC" "$UI_R" >&2; }

# tool-call progress line (called from the loop). $1 tool  $2 preview  $3 exit
ui_call() {
  local ec_col="$UI_OK"; [[ "$3" != 0 ]] && ec_col="$UI_BAD"
  printf '%s   \xe2\x9f\xa9 %-7s%s %s%.*s%s  %sexit %s%s\n' \
    "$UI_DIM" "$1" "$UI_R" "$UI_DIM" 72 "$2" "$UI_R" "$ec_col" "$3" "$UI_R" >&2
}

# render Hermes' answer: markdown if a renderer is around, else wrapped plain,
# always under a green "hermes" label, indented.
ui_answer() {
  local w; w=$(( $(ui_cols) - 3 ))
  printf '%s%shermes%s\n' "$UI_B" "$UI_HERMES" "$UI_R" >&2
  if [[ -t 2 ]] && command -v glow >/dev/null 2>&1; then
    printf '%s\n' "$1" | glow -w "$w" - | sed 's/^/   /' >&2
  elif [[ -t 2 ]] && command -v bat >/dev/null 2>&1; then
    printf '%s\n' "$1" | bat -pp -l md --color=always | sed 's/^/   /' >&2
  elif command -v fmt >/dev/null 2>&1; then
    printf '%s\n' "$1" | fmt -s -w "$w" | sed 's/^/   /' >&2
  else
    printf '%s\n' "$1" | sed 's/^/   /' >&2
  fi
}
