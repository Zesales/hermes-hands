#!/usr/bin/env bash
# api.sh - one Hermes turn over the Runs API. Sourced by loop.sh / hermes-hands.
#
# hc_api_check                    -> preflight (GET /v1/capabilities); prints "API OK: ..."
# hc_api_ask "<message>" [sfile]  -> POST /v1/runs, poll, and set:
#     HC_ANS_STATE   completed | failed | blocked
#     HC_ANS_TEXT    the assistant's final output string
#     HC_ANS_RUN     run id
#     HC_ANS_SESSION hermes session id (may be "")
#     HC_ANS_THREADED 1 if server-side continuity was used, 0 if it had to reset
#   returns 0 on completed, 1 otherwise (HC_ANS_TEXT holds the reason).
#
# When <sfile> (a session json path) is given, continuity ids are read from and
# written back to it.

HC_API_CONNECT_TIMEOUT="${HERMES_API_CONNECT_TIMEOUT:-5}"
HC_API_MAX_TIME="${HERMES_API_MAX_TIME:-30}"
HC_API_POLL_INTERVAL="${HERMES_API_POLL_INTERVAL:-2}"
HC_API_RUN_TIMEOUT="${HERMES_API_RUN_TIMEOUT:-600}"
HC_API_RETRIES="${HERMES_API_RETRIES:-3}"

hc_api_base() {
  local b="${HERMES_API_URL%/}"
  [[ -n "${HERMES_API_PROFILE:-}" ]] && b="$b/p/$HERMES_API_PROFILE"
  printf '%s' "$b"
}

hc_api_preflight() {
  hc_need jq curl
  hc_looks_unset "${HERMES_API_URL:-}" && hc_die "HERMES_API_URL not set (or placeholder) - see ${HC_SECRETS_PATH/#$HOME/\~}"
  hc_looks_unset "${HERMES_API_KEY:-}" && hc_die "HERMES_API_KEY not set (or placeholder) - see ${HC_SECRETS_PATH/#$HOME/\~}"
  hc_require_https "$HERMES_API_URL" HERMES_API_URL
}

# _curl <method-args...> ; sets HC_RC HC_CODE HC_BODY HC_ERR
_hc_curl() {
  local errf out
  errf="$(mktemp "${TMPDIR:-/tmp}/hc.XXXXXX")"
  if out="$(curl -sS -m "$HC_API_MAX_TIME" --connect-timeout "$HC_API_CONNECT_TIMEOUT" \
             -w $'\n%{http_code}' -H "Authorization: Bearer $HERMES_API_KEY" "$@" 2>"$errf")"; then
    HC_RC=0
  else HC_RC=$?; fi
  HC_ERR="$(grep -v '^[[:space:]]*$' "$errf" 2>/dev/null | tail -n1 || true)"
  rm -f "$errf"
  if [[ "$out" == *$'\n'??? ]]; then HC_CODE="${out##*$'\n'}"; HC_BODY="${out%$'\n'*}"
  else HC_CODE=000; HC_BODY="$out"; fi
}

hc_api_check() {
  hc_api_preflight
  local base attempt=1; base="$(hc_api_base)"
  while :; do
    _hc_curl "$base/v1/capabilities" -H "Accept: application/json"
    if (( HC_RC == 0 )) && [[ "$HC_CODE" == 2?? ]] && hc_is_json "$HC_BODY"; then
      printf 'API OK: %s @ %s\n' "$(hc_json_get "$HC_BODY" '.model // .runtime.mode // "hermes"')" "$base"
      return 0
    fi
    case "$HC_CODE" in
      401|403) hc_die "HTTP $HC_CODE at /v1/capabilities - HERMES_API_KEY rejected." ;;
      404)     hc_die "HTTP 404 at $base/v1/capabilities - wrong base URL / profile, or API server disabled." ;;
    esac
    (( attempt++ > HC_API_RETRIES )) && hc_die "cannot reach $base/v1 - curl $HC_RC ${HC_ERR:-}, last HTTP $HC_CODE"
    sleep 2
  done
}

hc_api_ask() {   # $1 = message, $2 = session file (optional)
  local msg="$1" sfile="${2:-}"
  HC_ANS_STATE=blocked HC_ANS_TEXT="" HC_ANS_RUN="" HC_ANS_SESSION="" HC_ANS_THREADED=0
  hc_api_preflight
  local base; base="$(hc_api_base)"

  # instructions + continuity ids, resolved once, in this scope
  local instr="" prev="" sess=""
  local ifile="${HERMES_HANDS_INSTRUCTIONS:-${XDG_CONFIG_HOME:-$HOME/.config}/hermes-hands/instructions.md}"
  [[ -r "$ifile" ]] || ifile="$HC_ROOT/share/instructions.md"
  [[ -r "$ifile" ]] && instr="$(cat "$ifile")"
  if [[ -n "$sfile" && -r "$sfile" ]]; then
    prev="$(hc_json_get "$(cat "$sfile")" '.last_run_id // ""')"
    sess="$(hc_json_get "$(cat "$sfile")" '.hermes_session_id // ""')"
  fi

  local body run_id attempt=1 force_fresh=0 cont_mode
  while :; do
    if   [[ -n "$prev" && $force_fresh -eq 0 ]]; then cont_mode=prev
    elif [[ -n "$sess" && $force_fresh -eq 0 ]]; then cont_mode=session
    else cont_mode=fresh; fi
    body="$(jq -n --arg i "$msg" --arg ins "$instr" --arg p "$prev" --arg s "$sess" --arg m "$cont_mode" '
      {input:$i}
      + (if $ins != "" then {instructions:$ins} else {} end)
      + (if $m == "prev"    then {previous_response_id:$p} else {} end)
      + (if $m == "session" then {session_id:$s}           else {} end)')"
    _hc_curl "$base/v1/runs" -H "Content-Type: application/json" --data-binary "$body"
    if (( HC_RC == 0 )) && [[ "$HC_CODE" == 2?? ]]; then
      run_id="$(hc_json_get "$HC_BODY" '.run_id // ""')"
      [[ -n "$run_id" ]] && break
      HC_ANS_TEXT="POST /v1/runs 2xx but no run_id: $(printf '%s' "$HC_BODY" | tr -d '\n' | head -c 200)"; return 1
    fi
    case "$HC_CODE" in
      401|403) HC_ANS_TEXT="HTTP $HC_CODE at /v1/runs - HERMES_API_KEY rejected."; return 1 ;;
      400|404|422)
        if [[ "$cont_mode" != fresh && $force_fresh -eq 0 ]]; then
          hc_warn "server rejected continuity ($cont_mode, HTTP $HC_CODE) - retrying without it"
          force_fresh=1; continue
        fi
        HC_ANS_TEXT="POST /v1/runs -> HTTP $HC_CODE: $(printf '%s' "$HC_BODY" | tr -d '\n' | head -c 200)"; return 1 ;;
    esac
    if (( attempt++ > HC_API_RETRIES )); then
      HC_ANS_TEXT="POST /v1/runs unreachable - curl $HC_RC ${HC_ERR:-}, HTTP $HC_CODE"; return 1
    fi
    sleep 2
  done
  [[ "$cont_mode" == fresh ]] && HC_ANS_THREADED=0 || HC_ANS_THREADED=1
  HC_ANS_RUN="$run_id"
  hc_log "run $run_id ($cont_mode) - polling"

  local waited=0 status="" out=""
  while :; do
    _hc_curl "$base/v1/runs/$run_id" -H "Accept: application/json"
    if (( HC_RC == 0 )) && [[ "$HC_CODE" == 2?? ]] && hc_is_json "$HC_BODY"; then
      status="$(hc_json_get "$HC_BODY" '.status // "?"')"
      case "$status" in
        completed)
          out="$(hc_json_get "$HC_BODY" '.output // ""')"
          HC_ANS_SESSION="$(hc_json_get "$HC_BODY" '.session_id // ""')"
          [[ -z "${out// /}" ]] && { HC_ANS_TEXT="run $run_id completed but empty output"; return 1; }
          HC_ANS_STATE=completed; HC_ANS_TEXT="$out"
          _hc_session_write "$sfile" "$run_id" "$HC_ANS_SESSION"
          return 0 ;;
        failed|cancelled)
          HC_ANS_TEXT="run $run_id $status: $(hc_json_get "$HC_BODY" '.output // .error // "no detail"' | tr -d '\n' | head -c 400)"; return 1 ;;
        started|running|queued|stopping|"") : ;;
        *) hc_log "unknown run status '$status' - still polling" ;;
      esac
    else
      hc_log "poll: curl $HC_RC / HTTP $HC_CODE ${HC_ERR:-} - retrying"
    fi
    waited=$(( waited + HC_API_POLL_INTERVAL ))
    (( waited > HC_API_RUN_TIMEOUT )) && { HC_ANS_TEXT="run $run_id still '${status:-?}' after ${HC_API_RUN_TIMEOUT}s"; return 1; }
    sleep "$HC_API_POLL_INTERVAL"
  done
}

# write continuity ids back into the session file (no-op without one)
_hc_session_write() {   # $1 sfile, $2 run_id, $3 hermes_session_id
  local sfile="$1" rid="$2" sid="$3"
  [[ -n "$sfile" ]] || return 0
  local cur='{}'; [[ -r "$sfile" ]] && cur="$(cat "$sfile")"
  hc_is_json "$cur" || cur='{}'
  printf '%s' "$cur" | jq --arg r "$rid" --arg s "$sid" --arg t "$(hc_now)" '
    .last_run_id=$r | (if $s!="" then .hermes_session_id=$s else . end)
    | .updated=$t | .turns=((.turns // 0)+1)' > "$sfile.tmp" && mv "$sfile.tmp" "$sfile"
}
