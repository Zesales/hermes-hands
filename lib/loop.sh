#!/usr/bin/env bash
# loop.sh - one user turn = N Hermes turns. Send the message; if Hermes replies
# with a directive envelope ({calls:[...], final:null}) run the calls locally,
# feed a {results:[...]} object back, repeat; stop when Hermes gives {final:"..."}
# or plain prose. Sets HH_LOOP_ANSWER. Returns 0 on an answer, 1 on BLOCKED.
#
# Requires: api.sh, dispatch.sh, util.sh sourced. HH_REPO_ROOT set.

HH_LOOP_MAX_ROUNDS="${HERMES_HANDS_MAX_ROUNDS:-8}"

# pull the first '{' .. last '}' out of a possibly-fenced, possibly-prose string
_hh_extract_obj() {
  local s="$1"
  s="${s#'```json'}"; s="${s#'```'}"; s="${s%'```'}"
  if hh_is_json "$s" && printf '%s' "$s" | jq -e 'type=="object"' >/dev/null 2>&1; then
    printf '%s' "$s"; return 0
  fi
  printf '%s' "$1" | awk '
    { buf = buf $0 "\n" }
    END {
      s = index(buf, "{"); if (s == 0) exit 1
      for (i = length(buf); i >= 1; i--) if (substr(buf,i,1) == "}") { e = i; break }
      if (e <= s) exit 1
      print substr(buf, s, e - s + 1)
    }'
}

hh_loop_run() {   # $1 = user message, $2 = session file
  local user_msg="$1" sfile="$2"
  HH_LOOP_ANSWER=""
  local round=1 send="$user_msg" turnlog="" recap=0 fixups=0

  while :; do
    hh_vlog "round $round -> Hermes"
    local msg="$send"
    (( recap )) && msg="[conversation so far this turn]
$turnlog
[latest tool results]
$send"

    if ! hh_api_ask "$msg" "$sfile"; then
      HH_LOOP_ANSWER="BLOCKED: ${HH_ANS_TEXT:-Hermes API turn failed}"
      return 1
    fi
    (( round > 1 )) && (( HH_ANS_THREADED == 0 )) && recap=1

    local reply="$HH_ANS_TEXT" obj calls_n final
    obj="$(_hh_extract_obj "$reply" || true)"

    if [[ -n "$obj" ]] && hh_is_json "$obj"; then
      calls_n="$(printf '%s' "$obj" | jq -r '(.calls // []) | length' 2>/dev/null || echo 0)"
      final="$(printf '%s' "$obj" | jq -r 'if (.final|type)=="string" then .final else "" end' 2>/dev/null || echo "")"
    else
      calls_n=0; final=""
      # a botched envelope? (mentions structure but did not parse)
      if printf '%s' "$reply" | grep -qiE '"(calls|tool)"[[:space:]]*:'; then
        if (( fixups < 2 )); then
          fixups=$((fixups+1))
          hh_warn "reply was not a valid envelope - asking Hermes to resend just the JSON"
          send='{"error":"your previous message was not a single valid JSON object of the form {\"calls\":[...],\"final\":null}. Resend ONLY that object, no prose, no code fences."}'
          turnlog+="[round $round] (invalid envelope, requested resend)"$'\n'
          round=$((round+1)); continue
        fi
      fi
      # otherwise: treat the whole thing as the prose answer
      HH_LOOP_ANSWER="${reply#'[INPUT_REQUIRED]'}"
      HH_LOOP_ANSWER="$(printf '%s' "$HH_LOOP_ANSWER" | sed '1s/^[[:space:]]*//')"
      return 0
    fi

    if (( calls_n == 0 )); then
      if [[ -n "$final" ]]; then HH_LOOP_ANSWER="$final"; return 0; fi
      # empty object - nudge once, then give up
      if (( fixups < 2 )); then
        fixups=$((fixups+1))
        send='{"error":"empty envelope. Either put tool calls in .calls or your answer in .final."}'
        round=$((round+1)); continue
      fi
      HH_LOOP_ANSWER="BLOCKED: Hermes returned an empty envelope repeatedly."
      return 1
    fi

    if (( round > HH_LOOP_MAX_ROUNDS )); then
      HH_LOOP_ANSWER="BLOCKED: Hermes still requesting data after $HH_LOOP_MAX_ROUNDS rounds."
      return 1
    fi

    hh_vlog "round $round: $calls_n call(s)"
    # execute each call, build {results:[...]}
    local results='[]' i tool args ctx line
    for (( i=0; i<calls_n; i++ )); do
      tool="$(printf '%s' "$obj" | jq -r ".calls[$i].tool // .calls[$i].name // \"\"")"
      args="$(printf '%s' "$obj" | jq -c ".calls[$i].args // .calls[$i].arguments // {}")"
      # arguments may arrive as a JSON *string* (OpenAI style) - unwrap
      if printf '%s' "$args" | jq -e 'type=="string"' >/dev/null 2>&1; then
        args="$(printf '%s' "$args" | jq -r '.' | jq -c '.' 2>/dev/null || printf '{}')"
      fi
      local preview; preview="$(printf '%s' "$args" | jq -r '(.cmd // .path // .pattern // "")' 2>/dev/null | tr '\n' ' ' | head -c 72)"
      hh_dispatch "$tool" "$args"; local drc=$?
      if (( drc == 3 )); then
        HH_LOOP_ANSWER="(turn aborted by operator at a $tool approval)"
        return 0
      fi
      if declare -F ui_call >/dev/null 2>&1; then ui_call "$tool" "$preview" "$HH_TOOL_EXIT"
      else printf '  > %-8s %s  exit %s\n' "$tool" "$preview" "$HH_TOOL_EXIT" >&2; fi
      ctx="$(printf '%s' "$HH_TOOL_CTX" | hh_scrub)"
      results="$(jq -c --argjson r "$results" --arg t "$tool" --argjson a "$args" \
        --arg o "$(printf '%s' "$HH_TOOL_OUT" | hh_scrub)" --argjson x "$HH_TOOL_EXIT" \
        --arg c "$ctx" -n '$r + [ {tool:$t, args:$a, exit_code:$x, output:$o}
                                  + (if $c != "" then {context:$c} else {} end) ]')"
      line="  ${tool}($(printf '%s' "$args" | head -c 120)) -> exit $HH_TOOL_EXIT"
      turnlog+="[round $round] $line"$'\n'
    done

    send="$(jq -c -n --argjson r "$results" '{results:$r}')"
    round=$((round+1))
  done
}
