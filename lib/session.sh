#!/usr/bin/env bash
# session.sh - local session INDEX. The conversation itself is server-side
# (Hermes threads it from the session_id we pass on every /v1/runs); this is a
# thin local record so `-c`, `--session <id>` and `sessions` work offline - the
# /v1 surface has no list/read endpoints for sessions. State lives under
# $XDG_STATE_HOME/hermes-hands/.
#
# Each record: {id (local), hermes_session_id, hermes_session_key (stable per
# repo), cwd, title, created, updated, turns}.

HH_STATE_DIR="${HERMES_HANDS_STATE:-${XDG_STATE_HOME:-$HOME/.local/state}/hermes-hands}"
HH_SESS_DIR="$HH_STATE_DIR/sessions"

hh_session_init_store() { mkdir -p "$HH_SESS_DIR/by-cwd"; }

_hh_cwd_ptr() { printf '%s/by-cwd/%s.id' "$HH_SESS_DIR" "$(hh_sha1 "$1")"; }

hh_session_path() { printf '%s/%s.json' "$HH_SESS_DIR" "$1"; }

# create a new session rooted at $1 (cwd); echoes its file path.
# We mint a client session_id up front and pass it to Hermes; if Hermes hands
# back a different one we adopt that (see _hh_session_write).
hh_session_new() {
  hh_session_init_store
  local cwd="$1" id ts hsid hkey
  ts="$(date -u +%Y%m%dT%H%M%S)"
  id="hh_${ts}_$(hh_sha1 "$cwd$RANDOM" | cut -c1-6)"
  hsid="hh-$(hh_sha1 "$cwd" | cut -c1-8)-$ts"
  hkey="hermes-hands:$(hh_sha1 "$cwd" | cut -c1-16)"
  local f; f="$(hh_session_path "$id")"
  jq -n --arg id "$id" --arg hsid "$hsid" --arg hkey "$hkey" --arg cwd "$cwd" --arg t "$(hh_now)" '
    {id:$id, hermes_session_id:$hsid, hermes_session_key:$hkey, cwd:$cwd,
     title:"", created:$t, updated:$t, turns:0}' > "$f"
  printf '%s' "$id" > "$(_hh_cwd_ptr "$cwd")"
  printf '%s' "$f"
}

# latest session id for $1 (cwd), or empty
hh_session_latest_for() {
  local ptr; ptr="$(_hh_cwd_ptr "$1")"
  [[ -r "$ptr" ]] || return 1
  local id; id="$(cat "$ptr")"
  [[ -r "$(hh_session_path "$id")" ]] || return 1
  printf '%s' "$id"
}

# resolve: --session <id> | -c (latest for cwd) | (default) new
# echoes the session file path; sets HH_SESSION_ID
hh_session_resolve() {   # $1 = mode: new|continue|<explicit id>   $2 = cwd
  local mode="$1" cwd="$2" id f
  case "$mode" in
    continue)
      id="$(hh_session_latest_for "$cwd" || true)"
      if [[ -z "$id" ]]; then
        hh_warn "no session for this directory yet - starting a new one"
        f="$(hh_session_new "$cwd")"; id="$(basename "$f" .json)"
      else f="$(hh_session_path "$id")"; fi ;;
    new|"")
      f="$(hh_session_new "$cwd")"; id="$(basename "$f" .json)" ;;
    *)
      id="$mode"; f="$(hh_session_path "$id")"
      [[ -r "$f" ]] || hh_die "no such session: $id" ;;
  esac
  # keep the by-cwd pointer fresh so -c follows the one you're using
  printf '%s' "$id" > "$(_hh_cwd_ptr "$cwd")"
  printf '%s' "$f"
}

hh_session_set_title() {   # $1 sfile, $2 title (only sets it if still empty)
  local f="$1" t; t="$(printf '%s' "$2" | tr '\n' ' ' | head -c 72)"
  [[ -r "$f" ]] || return 0
  local had; had="$(hh_json_get "$(cat "$f")" '.title // ""')"
  [[ -n "$had" ]] && return 0
  jq --arg t "$t" '.title=$t' "$f" > "$f.tmp" && mv "$f.tmp" "$f"
  # best-effort: mirror the title into Hermes' own session record
  local hsid; hsid="$(hh_json_get "$(cat "$f")" '.hermes_session_id // ""')"
  declare -F hh_api_set_title >/dev/null 2>&1 && [[ -n "$hsid" ]] && hh_api_set_title "$hsid" "$t" || true
}

hh_session_list() {
  hh_session_init_store
  local f
  printf '%-24s  %5s  %-19s  %s\n' ID TURNS UPDATED TITLE
  for f in "$HH_SESS_DIR"/*.json; do
    [[ -e "$f" ]] || continue
    jq -r '[.id, (.turns|tostring), (.updated|.[0:19]), (.title // ""), .cwd] | @tsv' "$f"
  done | sort -k3 -r | while IFS=$'\t' read -r id turns upd title cwd; do
    printf '%-24s  %5s  %-19s  %s\n' "$id" "$turns" "$upd" "${title:-$cwd}"
  done
}
