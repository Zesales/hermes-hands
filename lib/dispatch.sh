#!/usr/bin/env bash
# dispatch.sh - the ONE local tool dispatcher. Used by the pre-split-runtime
# loop (parse envelope -> dispatch) and, later, by the split-runtime path
# (SSE tool_call.request -> dispatch -> POST tool_result). Same code both ways.
#
# hc_dispatch <tool> <args-json>  -> sets:
#     HC_TOOL_EXIT  integer exit / status code
#     HC_TOOL_OUT   captured output (stdout+stderr, capped)
#     HC_TOOL_CTX   extra auto-gathered context on a failed run (may be "")
#   return 0 normally, 3 to abort the whole turn (operator hit [q]).
#
# HC_REPO_ROOT must be set by the caller to the directory the turn is rooted at.

HC_TOOL_MAX_OUT="${HERMES_CODE_MAX_OUTPUT:-20000}"     # bytes kept per tool call
HC_TOOL_RUN_TIMEOUT="${HERMES_CODE_RUN_TIMEOUT:-120}"

# base denylist for `run`; HERMES_CODE_DENY adds '|'-separated case-globs.
_hc_run_blocked() {   # $1 = command -> echoes reason, returns 0 if blocked
  local c=" $1 " g
  case "$c" in
    *" ssh "*|*" scp "*|*" sftp "*|*" rsync "*) echo "ssh/scp/rsync to other hosts"; return 0 ;;
    *" sudo "*|*" doas "*)                       echo "privilege escalation"; return 0 ;;
    *"rm -rf /"*|*"rm -fr /"*|*":(){ :|:& };:"*) echo "destructive"; return 0 ;;
    *" curl "*|*" wget "*)
      case "$c" in *"| sh"*|*"| bash"*|*"|sh"*|*"|bash"*) echo "pipe-to-shell download"; return 0 ;; esac ;;
  esac
  if [[ -n "${HERMES_CODE_DENY:-}" ]]; then
    IFS='|' read -r -a _dg <<< "$HERMES_CODE_DENY"
    for g in "${_dg[@]}"; do [[ -n "$g" && "$1" == $g ]] && { echo "matches HERMES_CODE_DENY ($g)"; return 0; }; done
  fi
  return 1
}

# resolve a path argument and confine read-only tools to the repo root
_hc_resolve_in_repo() {   # $1 = path -> echoes abspath, returns 1 if it escapes
  local p="$1" abs
  case "$p" in /*) abs="$p" ;; *) abs="$HC_REPO_ROOT/$p" ;; esac
  abs="$(cd "$(dirname -- "$abs")" 2>/dev/null && printf '%s/%s' "$(pwd -P)" "$(basename -- "$abs")")" || return 1
  case "$abs/" in "$HC_REPO_ROOT"/*|"$HC_REPO_ROOT/") printf '%s' "$abs"; return 0 ;; esac
  return 1
}

_hc_cap() { printf '%s' "$1" | head -c "$HC_TOOL_MAX_OUT"; }

hc_dispatch() {
  local tool="$1" args="$2" p cmd content old new rc=0 out="" ctx="" reason abs n
  HC_TOOL_EXIT=0 HC_TOOL_OUT="" HC_TOOL_CTX=""

  case "$tool" in
    read_file|read|cat)
      p="$(hc_json_get "$args" '.path // .file // ""')"
      [[ -z "$p" ]] && { HC_TOOL_EXIT=2; HC_TOOL_OUT="read_file: missing 'path'"; return 0; }
      if ! abs="$(_hc_resolve_in_repo "$p")"; then
        HC_TOOL_EXIT=1; HC_TOOL_OUT="refused: '$p' resolves outside the repo root ($HC_REPO_ROOT)"; return 0
      fi
      out="$(timeout "$HC_TOOL_RUN_TIMEOUT" cat -- "$abs" 2>&1)" || rc=$?
      HC_TOOL_EXIT=$rc; HC_TOOL_OUT="$(_hc_cap "$out")" ;;

    list_dir|ls)
      p="$(hc_json_get "$args" '.path // "."')"
      abs="$(_hc_resolve_in_repo "$p")" || { HC_TOOL_EXIT=1; HC_TOOL_OUT="refused: '$p' outside repo root"; return 0; }
      out="$(timeout "$HC_TOOL_RUN_TIMEOUT" ls -la --time-style=long-iso -- "$abs" 2>&1)" || rc=$?
      HC_TOOL_EXIT=$rc; HC_TOOL_OUT="$(_hc_cap "$out")" ;;

    grep|search)
      local pat path flags; pat="$(hc_json_get "$args" '.pattern // .query // ""')"
      path="$(hc_json_get "$args" '.path // "."')"; flags="$(hc_json_get "$args" '.flags // ""')"
      [[ -z "$pat" ]] && { HC_TOOL_EXIT=2; HC_TOOL_OUT="grep: missing 'pattern'"; return 0; }
      abs="$(_hc_resolve_in_repo "$path")" || { HC_TOOL_EXIT=1; HC_TOOL_OUT="refused: '$path' outside repo root"; return 0; }
      if command -v rg >/dev/null 2>&1; then
        out="$(cd "$HC_REPO_ROOT" && timeout "$HC_TOOL_RUN_TIMEOUT" rg --line-number --no-heading $flags -- "$pat" "$abs" 2>&1)" || rc=$?
      else
        out="$(cd "$HC_REPO_ROOT" && timeout "$HC_TOOL_RUN_TIMEOUT" grep -rnI $flags -e "$pat" -- "$abs" 2>&1)" || rc=$?
      fi
      HC_TOOL_EXIT=$rc; HC_TOOL_OUT="$(_hc_cap "$out")" ;;

    run|exec|bash)
      cmd="$(hc_json_get "$args" '.cmd // .command // ""')"
      [[ -z "$cmd" ]] && { HC_TOOL_EXIT=2; HC_TOOL_OUT="run: missing 'cmd'"; return 0; }
      if reason="$(_hc_run_blocked "$cmd")"; then
        HC_TOOL_EXIT=126; HC_TOOL_OUT="(blocked by worker policy: $reason)"; return 0
      fi
      hc_confirm "run: $cmd" ; local a=$?
      (( a == 2 )) && return 3
      (( a == 1 )) && { HC_TOOL_EXIT=125; HC_TOOL_OUT="(declined by operator)"; return 0; }
      local t; t="$(hc_json_get "$args" '.timeout // ""')"; [[ "$t" =~ ^[0-9]+$ ]] || t="$HC_TOOL_RUN_TIMEOUT"
      out="$(cd "$HC_REPO_ROOT" && timeout "$t" bash -lc "$cmd" 2>&1)" || rc=$?
      HC_TOOL_EXIT=$rc; HC_TOOL_OUT="$(_hc_cap "$out")"
      if (( rc != 0 )); then
        ctx="$(cd "$HC_REPO_ROOT" 2>/dev/null && {
          printf -- '--- git status -s ---\n'; git status -s 2>&1 | head -c 1200
          case " $cmd " in *" make "*)
            printf -- '\n--- make targets ---\n'
            { make -pRrq : 2>/dev/null | awk -F: '/^[a-zA-Z0-9][^$#\/\t=]*:/{print $1}' | sort -u; } | head -c 1200 ;;
          esac; })"
        HC_TOOL_CTX="$ctx"
      fi ;;

    write_file|write)
      p="$(hc_json_get "$args" '.path // .file // ""')"
      content="$(printf '%s' "$args" | jq -r '.content // .text // ""')"
      [[ -z "$p" ]] && { HC_TOOL_EXIT=2; HC_TOOL_OUT="write_file: missing 'path'"; return 0; }
      abs="$(_hc_resolve_in_repo "$p")" || { HC_TOOL_EXIT=1; HC_TOOL_OUT="refused: '$p' outside repo root"; return 0; }
      local diff; if [[ -f "$abs" ]]; then
        diff="$(printf '%s' "$content" | diff -u --label "a/$p" "$abs" --label "b/$p" - 2>/dev/null | head -c 4000)"
      else diff="(new file, $(printf '%s' "$content" | wc -l) lines)"; fi
      hc_confirm "write_file: $p" "$diff"; local a=$?
      (( a == 2 )) && return 3
      (( a == 1 )) && { HC_TOOL_EXIT=125; HC_TOOL_OUT="(declined by operator)"; return 0; }
      mkdir -p "$(dirname -- "$abs")"
      printf '%s' "$content" > "$abs" && { HC_TOOL_EXIT=0; HC_TOOL_OUT="wrote $p ($(wc -c <"$abs") bytes)"; } \
        || { HC_TOOL_EXIT=1; HC_TOOL_OUT="write failed: $p"; } ;;

    edit_file|edit)
      p="$(hc_json_get "$args" '.path // .file // ""')"
      old="$(printf '%s' "$args" | jq -r '.old // .find // ""')"
      new="$(printf '%s' "$args" | jq -r '.new // .replace // ""')"
      [[ -z "$p" || -z "$old" ]] && { HC_TOOL_EXIT=2; HC_TOOL_OUT="edit_file: needs 'path' and 'old'"; return 0; }
      abs="$(_hc_resolve_in_repo "$p")" || { HC_TOOL_EXIT=1; HC_TOOL_OUT="refused: '$p' outside repo root"; return 0; }
      [[ -f "$abs" ]] || { HC_TOOL_EXIT=1; HC_TOOL_OUT="edit_file: no such file: $p"; return 0; }
      n="$(grep -F -c -- "$old" "$abs" 2>/dev/null || echo 0)"
      if [[ "$n" != 1 ]]; then HC_TOOL_EXIT=1; HC_TOOL_OUT="edit_file: 'old' matched $n times in $p (need exactly 1)"; return 0; fi
      local tmp; tmp="$(mktemp)"; awk -v o="$old" -v nw="$new" '
        { if (!done && index($0,o)) { sub(o,nw); done=1 } print }' "$abs" > "$tmp"
      local diff; diff="$(diff -u --label "a/$p" "$abs" --label "b/$p" "$tmp" 2>/dev/null | head -c 4000)"
      hc_confirm "edit_file: $p" "$diff"; local a=$?
      (( a == 2 )) && { rm -f "$tmp"; return 3; }
      (( a == 1 )) && { rm -f "$tmp"; HC_TOOL_EXIT=125; HC_TOOL_OUT="(declined by operator)"; return 0; }
      mv "$tmp" "$abs" && { HC_TOOL_EXIT=0; HC_TOOL_OUT="edited $p"; } || { HC_TOOL_EXIT=1; HC_TOOL_OUT="edit failed: $p"; } ;;

    *)
      HC_TOOL_EXIT=2; HC_TOOL_OUT="unknown tool '$tool'" ;;
  esac
  return 0
}
