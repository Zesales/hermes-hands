#!/usr/bin/env bash
# session.sh - local session state. A "session" pairs a stable local id with the
# Hermes session_id / last run id so a conversation threads across turns and
# across invocations. State lives under $XDG_STATE_HOME/hermes-code/.

HC_STATE_DIR="${HERMES_CODE_STATE:-${XDG_STATE_HOME:-$HOME/.local/state}/hermes-code}"
HC_SESS_DIR="$HC_STATE_DIR/sessions"

hc_session_init_store() { mkdir -p "$HC_SESS_DIR/by-cwd"; }

_hc_cwd_ptr() { printf '%s/by-cwd/%s.id' "$HC_SESS_DIR" "$(hc_sha1 "$1")"; }

hc_session_path() { printf '%s/%s.json' "$HC_SESS_DIR" "$1"; }

# create a new session rooted at $1 (cwd); echoes its file path
hc_session_new() {
  hc_session_init_store
  local cwd="$1" id ts
  ts="$(date -u +%Y%m%dT%H%M%S)"
  id="hc_${ts}_$(hc_sha1 "$cwd$RANDOM" | cut -c1-6)"
  local f; f="$(hc_session_path "$id")"
  jq -n --arg id "$id" --arg cwd "$cwd" --arg t "$(hc_now)" '
    {id:$id, hermes_session_id:"", last_run_id:"", cwd:$cwd,
     title:"", created:$t, updated:$t, turns:0}' > "$f"
  printf '%s' "$id" > "$(_hc_cwd_ptr "$cwd")"
  printf '%s' "$f"
}

# latest session id for $1 (cwd), or empty
hc_session_latest_for() {
  local ptr; ptr="$(_hc_cwd_ptr "$1")"
  [[ -r "$ptr" ]] || return 1
  local id; id="$(cat "$ptr")"
  [[ -r "$(hc_session_path "$id")" ]] || return 1
  printf '%s' "$id"
}

# resolve: --session <id> | -c (latest for cwd) | (default) new
# echoes the session file path; sets HC_SESSION_ID
hc_session_resolve() {   # $1 = mode: new|continue|<explicit id>   $2 = cwd
  local mode="$1" cwd="$2" id f
  case "$mode" in
    continue)
      id="$(hc_session_latest_for "$cwd" || true)"
      if [[ -z "$id" ]]; then
        hc_warn "no session for this directory yet - starting a new one"
        f="$(hc_session_new "$cwd")"; id="$(basename "$f" .json)"
      else f="$(hc_session_path "$id")"; fi ;;
    new|"")
      f="$(hc_session_new "$cwd")"; id="$(basename "$f" .json)" ;;
    *)
      id="$mode"; f="$(hc_session_path "$id")"
      [[ -r "$f" ]] || hc_die "no such session: $id" ;;
  esac
  # keep the by-cwd pointer fresh so -c follows the one you're using
  printf '%s' "$id" > "$(_hc_cwd_ptr "$cwd")"
  printf '%s' "$f"
}

hc_session_set_title() {   # $1 sfile, $2 title (only if empty)
  local f="$1" t; t="$(printf '%s' "$2" | tr '\n' ' ' | head -c 72)"
  [[ -r "$f" ]] || return 0
  jq --arg t "$t" 'if (.title // "")=="" then .title=$t else . end' "$f" > "$f.tmp" && mv "$f.tmp" "$f"
}

hc_session_list() {
  hc_session_init_store
  local f
  printf '%-24s  %5s  %-19s  %s\n' ID TURNS UPDATED TITLE
  for f in "$HC_SESS_DIR"/*.json; do
    [[ -e "$f" ]] || continue
    jq -r '[.id, (.turns|tostring), (.updated|.[0:19]), (.title // ""), .cwd] | @tsv' "$f"
  done | sort -k3 -r | while IFS=$'\t' read -r id turns upd title cwd; do
    printf '%-24s  %5s  %-19s  %s\n' "$id" "$turns" "$upd" "${title:-$cwd}"
  done
}
