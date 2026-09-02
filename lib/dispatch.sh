#!/usr/bin/env bash
# dispatch.sh - the ONE local tool dispatcher. Used by the pre-split-runtime
# loop (parse envelope -> dispatch) and, later, by the split-runtime path
# (SSE tool_call.request -> dispatch -> POST tool_result). Same code both ways.
#
# The primary tool is `shell`: a PERSISTENT login bash. cd, environment, shell
# functions and aliases (your ~/.bashrc), and job state all survive between
# calls in a session - Hermes operates it like a real terminal, not a fixed
# toolbox. `read_file` / `write_file` / `edit_file` are structured helpers on
# top (path-jailed, diff+approval) so the model needn't fight shell quoting or
# heredocs for those.
#
# hc_dispatch <tool> <args-json>  -> sets HC_TOOL_EXIT, HC_TOOL_OUT, HC_TOOL_CTX
#   return 0 normally, 3 to abort the whole turn ([q] at an approval).
# HC_REPO_ROOT must be set to the directory the turn is rooted at.

HC_TOOL_MAX_OUT="${HERMES_HANDS_MAX_OUTPUT:-20000}"     # bytes kept per call
HC_TOOL_RUN_TIMEOUT="${HERMES_HANDS_RUN_TIMEOUT:-120}"  # per shell command

# ---------------------------------------------------------------- persistent shell
HC_SH_UP=0
hc_shell_start() {
  (( HC_SH_UP )) && return 0
  # --login so /etc/profile + profile files run; then pull in ~/.bashrc so the
  # user's PATH shims, aliases and functions are live. stderr folded into stdout.
  coproc HC_SH { exec bash --login 2>&1; }
  HC_SH_UP=1
  hc_shell_raw 'shopt -s expand_aliases; [ -r ~/.bashrc ] && . ~/.bashrc; PROMPT_COMMAND=; PS1=' 15 >/dev/null 2>&1 || true
  hc_shell_raw "cd $(printf '%q' "$HC_REPO_ROOT")" 10 >/dev/null 2>&1 || true
}
hc_shell_stop() {
  (( HC_SH_UP )) || return 0
  { printf 'exit\n' >&"${HC_SH[1]}"; } 2>/dev/null || true
  [[ -n "${HC_SH_PID:-}" ]] && kill "$HC_SH_PID" 2>/dev/null || true
  HC_SH_UP=0
}
# hc_shell_raw <cmd> <timeout> -> echoes output; sets HC_SH_EXIT. Restarts the
# coproc on desync (a timed-out command leaves the stream unsynced).
hc_shell_raw() {
  local cmd="$1" to="${2:-$HC_TOOL_RUN_TIMEOUT}" mark line out="" n=0 cap=$(( HC_TOOL_MAX_OUT * 2 ))
  mark="__HC_$$_${RANDOM}${RANDOM}__"
  HC_SH_EXIT=0
  { printf '%s\nprintf "\\n%s %%s\\n" "$?"\n' "$cmd" "$mark" >&"${HC_SH[1]}"; } 2>/dev/null || {
    hc_shell_stop; hc_shell_start; HC_SH_EXIT=124; printf '(shell not available)'; return 0; }
  while IFS= read -r -t "$to" -u "${HC_SH[0]}" line; do
    if [[ "$line" == "$mark "* ]]; then HC_SH_EXIT="${line#"$mark" }"; printf '%s' "${out%$'\n'}"; return 0; fi
    (( n < cap )) && { out+="$line"$'\n'; n=$(( n + ${#line} + 1 )); }
  done
  # timed out or EOF -> the coproc is now out of step; recycle it
  hc_shell_stop; hc_shell_start
  HC_SH_EXIT=124
  printf '%s\n[timed out after %ss - shell was reset]' "${out%$'\n'}" "$to"
}

_hc_cap() { printf '%s' "$1" | head -c "$HC_TOOL_MAX_OUT"; }

# resolve a path arg and confine the structured file tools to the repo root
_hc_resolve_in_repo() {
  local p="$1" abs
  case "$p" in /*) abs="$p" ;; *) abs="$HC_REPO_ROOT/$p" ;; esac
  abs="$(cd "$(dirname -- "$abs")" 2>/dev/null && printf '%s/%s' "$(pwd -P)" "$(basename -- "$abs")")" || return 1
  case "$abs/" in "$HC_REPO_ROOT"/*|"$HC_REPO_ROOT/") printf '%s' "$abs"; return 0 ;; esac
  return 1
}

# denylist for shell commands; HERMES_HANDS_DENY adds '|'-separated case-globs
_hc_cmd_blocked() {
  local c=" $1 " g
  case "$c" in
    *" ssh "*|*" scp "*|*" sftp "*|*" rsync "*) echo "ssh/scp/rsync to other hosts"; return 0 ;;
    *" sudo "*|*" doas "*)                       echo "privilege escalation"; return 0 ;;
    *"rm -rf /"*|*"rm -fr /"*|*":(){ :|:& };:"*) echo "destructive"; return 0 ;;
  esac
  case "$c" in *" curl "*|*" wget "*)
    case "$c" in *"| sh"*|*"|sh"*|*"| bash"*|*"|bash"*) echo "pipe-to-shell download"; return 0 ;; esac ;;
  esac
  if [[ -n "${HERMES_HANDS_DENY:-}" ]]; then
    IFS='|' read -r -a _dg <<< "$HERMES_HANDS_DENY"
    for g in "${_dg[@]}"; do [[ -n "$g" && "$1" == $g ]] && { echo "matches HERMES_HANDS_DENY ($g)"; return 0; }; done
  fi
  return 1
}

hc_dispatch() {
  local tool="$1" args="$2" p cmd content old new abs n rc reason t
  HC_TOOL_EXIT=0 HC_TOOL_OUT="" HC_TOOL_CTX=""

  case "$tool" in
    shell|run|bash|exec|terminal)
      cmd="$(printf '%s' "$args" | jq -r '.cmd // .command // .input // ""')"
      [[ -z "$cmd" ]] && { HC_TOOL_EXIT=2; HC_TOOL_OUT="shell: missing 'cmd'"; return 0; }
      if reason="$(_hc_cmd_blocked "$cmd")"; then
        HC_TOOL_EXIT=126; HC_TOOL_OUT="(blocked by worker policy: $reason)"; return 0
      fi
      hc_confirm "shell: $cmd"; rc=$?
      (( rc == 2 )) && return 3
      (( rc == 1 )) && { HC_TOOL_EXIT=125; HC_TOOL_OUT="(declined by operator)"; return 0; }
      hc_shell_start
      t="$(printf '%s' "$args" | jq -r '.timeout // ""')"; [[ "$t" =~ ^[0-9]+$ ]] || t="$HC_TOOL_RUN_TIMEOUT"
      HC_TOOL_OUT="$(hc_shell_raw "$cmd" "$t")"; HC_TOOL_EXIT="$HC_SH_EXIT"
      if (( HC_TOOL_EXIT != 0 )); then
        HC_TOOL_CTX="$(hc_shell_raw 'echo "cwd: $(pwd)"; git status -s 2>/dev/null | head -c 1200' 15 || true)"
        case " $cmd " in *" make "*)
          HC_TOOL_CTX+=$'\n--- make targets ---\n'"$(hc_shell_raw 'make -pRrq : 2>/dev/null | awk -F: "/^[a-zA-Z0-9][^\$#/\t=]*:/{print \$1}" | sort -u' 15 || true)" ;;
        esac
      fi ;;

    read_file|read|cat)
      p="$(printf '%s' "$args" | jq -r '.path // .file // ""')"
      [[ -z "$p" ]] && { HC_TOOL_EXIT=2; HC_TOOL_OUT="read_file: missing 'path'"; return 0; }
      abs="$(_hc_resolve_in_repo "$p")" || { HC_TOOL_EXIT=1; HC_TOOL_OUT="refused: '$p' resolves outside the repo root ($HC_REPO_ROOT)"; return 0; }
      HC_TOOL_OUT="$(_hc_cap "$(timeout "$HC_TOOL_RUN_TIMEOUT" cat -- "$abs" 2>&1)")"; HC_TOOL_EXIT=$? ;;

    write_file|write)
      p="$(printf '%s' "$args" | jq -r '.path // .file // ""')"
      content="$(printf '%s' "$args" | jq -r '.content // .text // ""')"
      [[ -z "$p" ]] && { HC_TOOL_EXIT=2; HC_TOOL_OUT="write_file: missing 'path'"; return 0; }
      abs="$(_hc_resolve_in_repo "$p")" || { HC_TOOL_EXIT=1; HC_TOOL_OUT="refused: '$p' outside repo root"; return 0; }
      local diff
      if [[ -f "$abs" ]]; then diff="$(printf '%s' "$content" | diff -u --label "a/$p" "$abs" --label "b/$p" - 2>/dev/null | head -c 4000)"
      else diff="(new file, $(printf '%s' "$content" | wc -l) lines)"; fi
      hc_confirm "write_file: $p" "$diff"; rc=$?
      (( rc == 2 )) && return 3
      (( rc == 1 )) && { HC_TOOL_EXIT=125; HC_TOOL_OUT="(declined by operator)"; return 0; }
      mkdir -p "$(dirname -- "$abs")"
      if printf '%s' "$content" > "$abs"; then HC_TOOL_EXIT=0; HC_TOOL_OUT="wrote $p ($(wc -c <"$abs") bytes)"
      else HC_TOOL_EXIT=1; HC_TOOL_OUT="write failed: $p"; fi ;;

    edit_file|edit)
      p="$(printf '%s' "$args" | jq -r '.path // .file // ""')"
      old="$(printf '%s' "$args" | jq -r '.old // .find // ""')"
      new="$(printf '%s' "$args" | jq -r '.new // .replace // ""')"
      [[ -z "$p" || -z "$old" ]] && { HC_TOOL_EXIT=2; HC_TOOL_OUT="edit_file: needs 'path' and 'old'"; return 0; }
      abs="$(_hc_resolve_in_repo "$p")" || { HC_TOOL_EXIT=1; HC_TOOL_OUT="refused: '$p' outside repo root"; return 0; }
      [[ -f "$abs" ]] || { HC_TOOL_EXIT=1; HC_TOOL_OUT="edit_file: no such file: $p"; return 0; }
      n="$(grep -F -c -- "$old" "$abs" 2>/dev/null || echo 0)"
      [[ "$n" == 1 ]] || { HC_TOOL_EXIT=1; HC_TOOL_OUT="edit_file: 'old' matched $n times in $p (need exactly 1)"; return 0; }
      local tmp; tmp="$(mktemp)"
      awk -v o="$old" -v nw="$new" '{ if (!done && index($0,o)) { sub(o,nw); done=1 } print }' "$abs" > "$tmp"
      local diff; diff="$(diff -u --label "a/$p" "$abs" --label "b/$p" "$tmp" 2>/dev/null | head -c 4000)"
      hc_confirm "edit_file: $p" "$diff"; rc=$?
      (( rc == 2 )) && { rm -f "$tmp"; return 3; }
      (( rc == 1 )) && { rm -f "$tmp"; HC_TOOL_EXIT=125; HC_TOOL_OUT="(declined by operator)"; return 0; }
      if mv "$tmp" "$abs"; then HC_TOOL_EXIT=0; HC_TOOL_OUT="edited $p"; else HC_TOOL_EXIT=1; HC_TOOL_OUT="edit failed: $p"; fi ;;

    *)
      HC_TOOL_EXIT=2; HC_TOOL_OUT="unknown tool '$tool' (have: shell, read_file, write_file, edit_file)" ;;
  esac
  return 0
}
