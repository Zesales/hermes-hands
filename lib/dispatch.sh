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
# hh_dispatch <tool> <args-json>  -> sets HH_TOOL_EXIT, HH_TOOL_OUT, HH_TOOL_CTX
#   return 0 normally, 3 to abort the whole turn ([q] at an approval).
# HH_REPO_ROOT must be set to the directory the turn is rooted at.

HH_TOOL_MAX_OUT="${HERMES_HANDS_MAX_OUTPUT:-20000}"     # bytes kept per call
HH_TOOL_RUN_TIMEOUT="${HERMES_HANDS_RUN_TIMEOUT:-120}"  # per shell command

# ---------------------------------------------------------------- persistent shell
HH_SH_UP=0
hh_shell_start() {
  (( HH_SH_UP )) && return 0
  # --login so /etc/profile + profile files run; then pull in ~/.bashrc so the
  # user's PATH shims, aliases and functions are live. stderr folded into stdout.
  coproc HH_SH { exec bash --login 2>&1; }
  HH_SH_UP=1
  hh_shell_raw 'shopt -s expand_aliases; [ -r ~/.bashrc ] && . ~/.bashrc; PROMPT_COMMAND=; PS1=' 15 >/dev/null 2>&1 || true
  hh_shell_raw "cd $(printf '%q' "$HH_REPO_ROOT")" 10 >/dev/null 2>&1 || true
}
hh_shell_stop() {
  (( HH_SH_UP )) || return 0
  { printf 'exit\n' >&"${HH_SH[1]}"; } 2>/dev/null || true
  [[ -n "${HH_SH_PID:-}" ]] && kill "$HH_SH_PID" 2>/dev/null || true
  HH_SH_UP=0
}
# hh_shell_raw <cmd> <timeout> -> echoes output; sets HH_SH_EXIT. Restarts the
# coproc on desync (a timed-out command leaves the stream unsynced).
hh_shell_raw() {
  local cmd="$1" to="${2:-$HH_TOOL_RUN_TIMEOUT}" mark line out="" n=0 cap=$(( HH_TOOL_MAX_OUT * 2 ))
  mark="__HC_$$_${RANDOM}${RANDOM}__"
  HH_SH_EXIT=0
  { printf '%s\nprintf "\\n%s %%s\\n" "$?"\n' "$cmd" "$mark" >&"${HH_SH[1]}"; } 2>/dev/null || {
    hh_shell_stop; hh_shell_start; HH_SH_EXIT=124; printf '(shell not available)'; return 0; }
  while IFS= read -r -t "$to" -u "${HH_SH[0]}" line; do
    if [[ "$line" == "$mark "* ]]; then HH_SH_EXIT="${line#"$mark" }"; printf '%s' "${out%$'\n'}"; return 0; fi
    (( n < cap )) && { out+="$line"$'\n'; n=$(( n + ${#line} + 1 )); }
  done
  # timed out or EOF -> the coproc is now out of step; recycle it
  hh_shell_stop; hh_shell_start
  HH_SH_EXIT=124
  printf '%s\n[timed out after %ss - shell was reset]' "${out%$'\n'}" "$to"
}

_hh_cap() { printf '%s' "$1" | head -c "$HH_TOOL_MAX_OUT"; }

# resolve a path arg and confine the structured file tools to the repo root
_hh_resolve_in_repo() {
  local p="$1" abs
  case "$p" in /*) abs="$p" ;; *) abs="$HH_REPO_ROOT/$p" ;; esac
  abs="$(cd "$(dirname -- "$abs")" 2>/dev/null && printf '%s/%s' "$(pwd -P)" "$(basename -- "$abs")")" || return 1
  case "$abs/" in "$HH_REPO_ROOT"/*|"$HH_REPO_ROOT/") printf '%s' "$abs"; return 0 ;; esac
  return 1
}

# denylist for shell commands; HERMES_HANDS_DENY adds '|'-separated case-globs
_hh_cmd_blocked() {
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

hh_dispatch() {
  local tool="$1" args="$2" p cmd content old new abs n rc reason t
  HH_TOOL_EXIT=0 HH_TOOL_OUT="" HH_TOOL_CTX=""

  case "$tool" in
    shell|run|bash|exec|terminal)
      cmd="$(printf '%s' "$args" | jq -r '.cmd // .command // .input // ""')"
      [[ -z "$cmd" ]] && { HH_TOOL_EXIT=2; HH_TOOL_OUT="shell: missing 'cmd'"; return 0; }
      if reason="$(_hh_cmd_blocked "$cmd")"; then
        HH_TOOL_EXIT=126; HH_TOOL_OUT="(blocked by worker policy: $reason)"; return 0
      fi
      hh_confirm "shell: $cmd"; rc=$?
      (( rc == 2 )) && return 3
      (( rc == 1 )) && { HH_TOOL_EXIT=125; HH_TOOL_OUT="(declined by operator)"; return 0; }
      hh_shell_start
      t="$(printf '%s' "$args" | jq -r '.timeout // ""')"; [[ "$t" =~ ^[0-9]+$ ]] || t="$HH_TOOL_RUN_TIMEOUT"
      HH_TOOL_OUT="$(hh_shell_raw "$cmd" "$t")"; HH_TOOL_EXIT="$HH_SH_EXIT"
      if (( HH_TOOL_EXIT != 0 )); then
        HH_TOOL_CTX="$(hh_shell_raw 'echo "cwd: $(pwd)"; git status -s 2>/dev/null | head -c 1200' 15 || true)"
        case " $cmd " in *" make "*)
          HH_TOOL_CTX+=$'\n--- make targets ---\n'"$(hh_shell_raw 'make -pRrq : 2>/dev/null | awk -F: "/^[a-zA-Z0-9][^\$#/\t=]*:/{print \$1}" | sort -u' 15 || true)" ;;
        esac
      fi ;;

    read_file|read|cat)
      p="$(printf '%s' "$args" | jq -r '.path // .file // ""')"
      [[ -z "$p" ]] && { HH_TOOL_EXIT=2; HH_TOOL_OUT="read_file: missing 'path'"; return 0; }
      abs="$(_hh_resolve_in_repo "$p")" || { HH_TOOL_EXIT=1; HH_TOOL_OUT="refused: '$p' resolves outside the repo root ($HH_REPO_ROOT)"; return 0; }
      HH_TOOL_OUT="$(_hh_cap "$(timeout "$HH_TOOL_RUN_TIMEOUT" cat -- "$abs" 2>&1)")"; HH_TOOL_EXIT=$? ;;

    write_file|write)
      p="$(printf '%s' "$args" | jq -r '.path // .file // ""')"
      content="$(printf '%s' "$args" | jq -r '.content // .text // ""')"
      [[ -z "$p" ]] && { HH_TOOL_EXIT=2; HH_TOOL_OUT="write_file: missing 'path'"; return 0; }
      abs="$(_hh_resolve_in_repo "$p")" || { HH_TOOL_EXIT=1; HH_TOOL_OUT="refused: '$p' outside repo root"; return 0; }
      local diff
      if [[ -f "$abs" ]]; then diff="$(printf '%s' "$content" | diff -u --label "a/$p" "$abs" --label "b/$p" - 2>/dev/null | head -c 4000)"
      else diff="(new file, $(printf '%s' "$content" | wc -l) lines)"; fi
      hh_confirm "write_file: $p" "$diff"; rc=$?
      (( rc == 2 )) && return 3
      (( rc == 1 )) && { HH_TOOL_EXIT=125; HH_TOOL_OUT="(declined by operator)"; return 0; }
      mkdir -p "$(dirname -- "$abs")"
      if printf '%s' "$content" > "$abs"; then HH_TOOL_EXIT=0; HH_TOOL_OUT="wrote $p ($(wc -c <"$abs") bytes)"
      else HH_TOOL_EXIT=1; HH_TOOL_OUT="write failed: $p"; fi ;;

    edit_file|edit)
      p="$(printf '%s' "$args" | jq -r '.path // .file // ""')"
      old="$(printf '%s' "$args" | jq -r '.old // .find // ""')"
      new="$(printf '%s' "$args" | jq -r '.new // .replace // ""')"
      [[ -z "$p" || -z "$old" ]] && { HH_TOOL_EXIT=2; HH_TOOL_OUT="edit_file: needs 'path' and 'old'"; return 0; }
      abs="$(_hh_resolve_in_repo "$p")" || { HH_TOOL_EXIT=1; HH_TOOL_OUT="refused: '$p' outside repo root"; return 0; }
      [[ -f "$abs" ]] || { HH_TOOL_EXIT=1; HH_TOOL_OUT="edit_file: no such file: $p"; return 0; }
      n="$(grep -F -c -- "$old" "$abs" 2>/dev/null || echo 0)"
      [[ "$n" == 1 ]] || { HH_TOOL_EXIT=1; HH_TOOL_OUT="edit_file: 'old' matched $n times in $p (need exactly 1)"; return 0; }
      local tmp; tmp="$(mktemp)"
      awk -v o="$old" -v nw="$new" '{ if (!done && index($0,o)) { sub(o,nw); done=1 } print }' "$abs" > "$tmp"
      local diff; diff="$(diff -u --label "a/$p" "$abs" --label "b/$p" "$tmp" 2>/dev/null | head -c 4000)"
      hh_confirm "edit_file: $p" "$diff"; rc=$?
      (( rc == 2 )) && { rm -f "$tmp"; return 3; }
      (( rc == 1 )) && { rm -f "$tmp"; HH_TOOL_EXIT=125; HH_TOOL_OUT="(declined by operator)"; return 0; }
      if mv "$tmp" "$abs"; then HH_TOOL_EXIT=0; HH_TOOL_OUT="edited $p"; else HH_TOOL_EXIT=1; HH_TOOL_OUT="edit failed: $p"; fi ;;

    *)
      HH_TOOL_EXIT=2; HH_TOOL_OUT="unknown tool '$tool' (have: shell, read_file, write_file, edit_file)" ;;
  esac
  return 0
}
