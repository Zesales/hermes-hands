#!/usr/bin/env bash
# api.sh - one Hermes turn over the Runs API. Sourced by loop.sh / hermes-hands.
#
# hh_api_check                    -> preflight (GET /v1/capabilities); prints "API OK: ..."
# hh_api_ask "<message>" [sfile]  -> POST /v1/runs, poll, and set:
#     HH_ANS_STATE   completed | failed | blocked
#     HH_ANS_TEXT    the assistant's final output string
#     HH_ANS_RUN     run id
#     HH_ANS_SESSION hermes session id (may be "")
#     HH_ANS_THREADED 1 if server-side continuity was used, 0 if it had to reset
#   returns 0 on completed, 1 otherwise (HH_ANS_TEXT holds the reason).
#
# When <sfile> (a session json path) is given, continuity ids are read from and
# written back to it.

HH_API_CONNECT_TIMEOUT="${HERMES_API_CONNECT_TIMEOUT:-5}"
HH_API_MAX_TIME="${HERMES_API_MAX_TIME:-30}"
HH_API_POLL_INTERVAL="${HERMES_API_POLL_INTERVAL:-2}"
HH_API_RUN_TIMEOUT="${HERMES_API_RUN_TIMEOUT:-600}"
HH_API_RETRIES="${HERMES_API_RETRIES:-3}"

hh_api_base() {
  local b="${HERMES_API_URL%/}"
  [[ -n "${HERMES_API_PROFILE:-}" ]] && b="$b/p/$HERMES_API_PROFILE"
  printf '%s' "$b"
}

hh_api_preflight() {
  hh_need jq curl
  hh_looks_unset "${HERMES_API_URL:-}" && hh_die "HERMES_API_URL not set (or placeholder) - see ${HH_SECRETS_PATH/#$HOME/\~}"
  hh_looks_unset "${HERMES_API_KEY:-}" && hh_die "HERMES_API_KEY not set (or placeholder) - see ${HH_SECRETS_PATH/#$HOME/\~}"
  hh_require_https "$HERMES_API_URL" HERMES_API_URL
}

# _curl <method-args...> ; sets HH_RC HH_CODE HH_BODY HH_ERR
_hh_curl() {
  local errf out
  errf="$(mktemp "${TMPDIR:-/tmp}/hc.XXXXXX")"
  if out="$(curl -sS -m "$HH_API_MAX_TIME" --connect-timeout "$HH_API_CONNECT_TIMEOUT" \
             -w $'\n%{http_code}' -H "Authorization: Bearer $HERMES_API_KEY" "$@" 2>"$errf")"; then
    HH_RC=0
  else HH_RC=$?; fi
  HH_ERR="$(grep -v '^[[:space:]]*$' "$errf" 2>/dev/null | tail -n1 || true)"
  rm -f "$errf"
  if [[ "$out" == *$'\n'??? ]]; then HH_CODE="${out##*$'\n'}"; HH_BODY="${out%$'\n'*}"
  else HH_CODE=000; HH_BODY="$out"; fi
}

hh_api_check() {
  hh_api_preflight
  local base attempt=1; base="$(hh_api_base)"
  while :; do
    _hh_curl "$base/v1/capabilities" -H "Accept: application/json"
    if (( HH_RC == 0 )) && [[ "$HH_CODE" == 2?? ]] && hh_is_json "$HH_BODY"; then
      printf 'API OK: %s @ %s\n' "$(hh_json_get "$HH_BODY" '.model // .runtime.mode // "hermes"')" "$base"
      return 0
    fi
    case "$HH_CODE" in
      401|403) hh_die "HTTP $HH_CODE at /v1/capabilities - HERMES_API_KEY rejected." ;;
      404)     hh_die "HTTP 404 at $base/v1/capabilities - wrong base URL / profile, or API server disabled." ;;
    esac
    (( attempt++ > HH_API_RETRIES )) && hh_die "cannot reach $base/v1 - curl $HH_RC ${HH_ERR:-}, last HTTP $HH_CODE"
    sleep 2
  done
}

hh_api_ask() {   # $1 = message, $2 = session file (optional)
  local msg="$1" sfile="${2:-}"
  HH_ANS_STATE=blocked HH_ANS_TEXT="" HH_ANS_RUN="" HH_ANS_SESSION="" HH_ANS_THREADED=0
  hh_api_preflight
  local base; base="$(hh_api_base)"

  # instructions + the server-side session handles, resolved once
  local instr="" sess="" skey=""
  local ifile="${HERMES_HANDS_INSTRUCTIONS:-${XDG_CONFIG_HOME:-$HOME/.config}/hermes-hands/instructions.md}"
  [[ -r "$ifile" ]] || ifile="${HH_ROOT:-}/share/instructions.md"
  [[ -r "$ifile" ]] && instr="$(cat "$ifile")"
  [[ -z "$instr" && -n "${HH_INSTRUCTIONS_BUILTIN:-}" ]] && instr="$HH_INSTRUCTIONS_BUILTIN"
  if [[ -n "$sfile" && -r "$sfile" ]]; then
    sess="$(hh_json_get "$(cat "$sfile")" '.hermes_session_id // ""')"
    skey="$(hh_json_get "$(cat "$sfile")" '.hermes_session_key // ""')"
  fi
  # Continuity is server-side: pass the same session_id every turn and Hermes
  # loads that session's transcript as context (docs: /v1/runs "loads that
  # session's active transcript" when session_id is given and no explicit
  # conversation_history). X-Hermes-Session-Key is the stable per-repo handle for
  # long-term memory, independent of the transcript.
  local ikey; ikey="hh-$(date -u +%s)-$RANDOM$RANDOM"

  local body run_id attempt=1 drop_sess=0
  while :; do
    body="$(jq -n --arg i "$msg" --arg ins "$instr" --arg s "$sess" --argjson ds "$drop_sess" '
      {input:$i}
      + (if $ins != "" then {instructions:$ins} else {} end)
      + (if ($s != "" and $ds == 0) then {session_id:$s} else {} end)')"
    _hh_curl "$base/v1/runs" \
      -H "Content-Type: application/json" \
      -H "Idempotency-Key: $ikey" \
      ${sess:+-H "X-Hermes-Session-Id: $sess"} \
      ${skey:+-H "X-Hermes-Session-Key: $skey"} \
      --data-binary "$body"
    if (( HH_RC == 0 )) && [[ "$HH_CODE" == 2?? ]]; then
      run_id="$(hh_json_get "$HH_BODY" '.run_id // ""')"
      [[ -n "$run_id" ]] && break
      HH_ANS_TEXT="POST /v1/runs 2xx but no run_id: $(printf '%s' "$HH_BODY" | tr -d '\n' | head -c 200)"; return 1
    fi
    case "$HH_CODE" in
      401|403) HH_ANS_TEXT="HTTP $HH_CODE at /v1/runs - HERMES_API_KEY rejected."; return 1 ;;
      400|404|422)
        if [[ -n "$sess" && $drop_sess -eq 0 ]]; then
          hh_warn "server rejected session_id (HTTP $HH_CODE) - retrying without it, local recap on"
          drop_sess=1; continue
        fi
        HH_ANS_TEXT="POST /v1/runs -> HTTP $HH_CODE: $(printf '%s' "$HH_BODY" | tr -d '\n' | head -c 200)"; return 1 ;;
    esac
    if (( attempt++ > HH_API_RETRIES )); then
      HH_ANS_TEXT="POST /v1/runs unreachable - curl $HH_RC ${HH_ERR:-}, HTTP $HH_CODE"; return 1
    fi
    sleep 2
  done
  (( drop_sess )) && HH_ANS_THREADED=0 || HH_ANS_THREADED=1
  HH_ANS_RUN="$run_id"
  hh_vlog "run $run_id (session=${sess:-none}${drop_sess:+ DROPPED}) - polling"

  local waited=0 status="" out=""
  while :; do
    _hh_curl "$base/v1/runs/$run_id" -H "Accept: application/json"
    if (( HH_RC == 0 )) && [[ "$HH_CODE" == 2?? ]] && hh_is_json "$HH_BODY"; then
      status="$(hh_json_get "$HH_BODY" '.status // "?"')"
      case "$status" in
        completed)
          out="$(hh_json_get "$HH_BODY" '.output // ""')"
          HH_ANS_SESSION="$(hh_json_get "$HH_BODY" '.session_id // ""')"
          [[ -z "${out// /}" ]] && { HH_ANS_TEXT="run $run_id completed but empty output"; return 1; }
          HH_ANS_STATE=completed; HH_ANS_TEXT="$out"
          _hh_session_write "$sfile" "$run_id" "$HH_ANS_SESSION"
          return 0 ;;
        failed|cancelled)
          HH_ANS_TEXT="run $run_id $status: $(hh_json_get "$HH_BODY" '.output // .error // "no detail"' | tr -d '\n' | head -c 400)"; return 1 ;;
        started|running|queued|stopping|"") : ;;
        *) hh_log "unknown run status '$status' - still polling" ;;
      esac
    else
      hh_log "poll: curl $HH_RC / HTTP $HH_CODE ${HH_ERR:-} - retrying"
    fi
    waited=$(( waited + HH_API_POLL_INTERVAL ))
    (( waited > HH_API_RUN_TIMEOUT )) && { HH_ANS_TEXT="run $run_id still '${status:-?}' after ${HH_API_RUN_TIMEOUT}s"; return 1; }
    sleep "$HH_API_POLL_INTERVAL"
  done
}

# after a completed run: bump the index, and adopt Hermes' session_id if it
# handed back a different one than we sent.
_hh_session_write() {   # $1 sfile, $2 run_id, $3 session_id from run status
  local sfile="$1" rid="$2" sid="$3"
  [[ -n "$sfile" ]] || return 0
  local cur='{}'; [[ -r "$sfile" ]] && cur="$(cat "$sfile")"
  hh_is_json "$cur" || cur='{}'
  printf '%s' "$cur" | jq --arg r "$rid" --arg s "$sid" --arg t "$(hh_now)" '
    .last_run_id=$r | .updated=$t | .turns=((.turns // 0)+1)
    | (if ($s != "" and $s != (.hermes_session_id // "")) then .hermes_session_id=$s else . end)
  ' > "$sfile.tmp" && mv "$sfile.tmp" "$sfile"
}

# best-effort: set a session's title in Hermes via the /api/sessions REST layer
# (there is no /v1 endpoint for this). Silent on any failure.
hh_api_set_title() {   # $1 = hermes_session_id, $2 = title
  [[ -n "${HERMES_API_URL:-}" && -n "${HERMES_API_KEY:-}" && -n "$1" ]] || return 0
  curl -sS -m 8 --connect-timeout 4 -o /dev/null \
    -X PATCH "$(hh_api_base | sed 's#/v1$##')/api/sessions/$1" \
    -H "Authorization: Bearer $HERMES_API_KEY" -H "Content-Type: application/json" \
    --data-binary "$(jq -n --arg t "$2" '{title:$t}')" >/dev/null 2>&1 || true
}
