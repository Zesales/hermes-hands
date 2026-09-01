#!/usr/bin/env bash
# Offline tests: hermes-hands against test/mock_hermes.py. No network, no model.
set -uo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BIN="$ROOT/bin/hermes-hands"
MOCK="$ROOT/test/mock_hermes.py"
PASS=0; FAIL=0
WORK="$(mktemp -d)"; trap 'rm -rf "$WORK"' EXIT

export HERMES_HANDS_ALLOW_HTTP=1 HERMES_API_KEY=testkey HERMES_HANDS_APPROVE=auto
export HERMES_API_POLL_INTERVAL=1 HERMES_API_RETRIES=1
export HERMES_HANDS_STATE="$WORK/state" XDG_CONFIG_HOME="$WORK/cfg"
mkdir -p "$WORK/repo"; printf 'ai-stack readme\nHermes deploy via deploy.mk\n' > "$WORK/repo/README.md"
( cd "$WORK/repo" && git init -q && git add -A && git commit -qm init 2>/dev/null ) || true

start_mock() { MODE="$1" PORT="$2" python3 "$MOCK" & MP=$!; sleep 0.5; }
stop_mock()  { kill "$MP" 2>/dev/null; wait "$MP" 2>/dev/null; }

check() {   # <name> <expected-substring> <actual>
  if [[ "$3" == *"$2"* ]]; then PASS=$((PASS+1)); printf 'ok   %s\n' "$1"
  else FAIL=$((FAIL+1)); printf 'FAIL %s\n     want ~ %q\n     got    %q\n' "$1" "$2" "$3"; fi
}

P=8971
start_mock plain $P
out="$(cd "$WORK/repo" && HERMES_API_URL="http://127.0.0.1:$P" "$BIN" --new "hi" 2>/dev/null)"
check "plain: final answer" "plain answer from the mock brain" "$out"
stop_mock

P=8972
start_mock prose $P
out="$(cd "$WORK/repo" && HERMES_API_URL="http://127.0.0.1:$P" "$BIN" --new "hi" 2>/dev/null)"
check "prose: accepted as answer" "plain-prose answer" "$out"
stop_mock

P=8973
start_mock delegate $P
out="$(cd "$WORK/repo" && HERMES_API_URL="http://127.0.0.1:$P" "$BIN" --new "was steht in der README?" 2>/dev/null)"
check "delegate: loop ran calls + fed results" "saw_results=True" "$out"
stop_mock

P=8974
start_mock badjson $P
out="$(cd "$WORK/repo" && HERMES_API_URL="http://127.0.0.1:$P" "$BIN" --new "hi" 2>/dev/null)"
check "badjson: recovered after resend" "recovered and answered" "$out"
stop_mock

P=8977
start_mock shellstate $P
out="$(cd "$WORK/repo" && HERMES_API_URL="http://127.0.0.1:$P" "$BIN" --new "cd around" 2>/dev/null)"
check "shell: cwd persists across calls in a turn" "cwd_persisted=True" "$out"
stop_mock

P=8975
start_mock plain $P
out="$(cd "$WORK/repo" && HERMES_API_URL="http://127.0.0.1:$P" "$BIN" check 2>&1)"
check "check: API OK" "API OK" "$out"
stop_mock

# session continuity: -c reuses the id; two invocations, one file, turns=2
P=8976
start_mock plain $P
( export HERMES_HANDS_STATE="$WORK/sstate"
  cd "$WORK/repo" && HERMES_API_URL="http://127.0.0.1:$P" "$BIN" --new "one" >/dev/null 2>&1
  cd "$WORK/repo" && HERMES_API_URL="http://127.0.0.1:$P" "$BIN" -c   "two" >/dev/null 2>&1 )
files="$(ls "$WORK"/sstate/sessions/*.json 2>/dev/null | wc -l | tr -d ' ')"
turns="$(jq -s 'map(.turns) | max' "$WORK"/sstate/sessions/*.json 2>/dev/null)"
check "session: one file for the dir across -c" "1" "$files"
check "session: turns accumulated across invocations" "2" "$turns"
sf="$(ls "$WORK"/sstate/sessions/*.json 2>/dev/null | head -1)"
hsid="$(jq -r '.hermes_session_id' "$sf" 2>/dev/null)"
hkey="$(jq -r '.hermes_session_key' "$sf" 2>/dev/null)"
check "session: client session_id minted + kept" "hh-" "$hsid"
check "session: per-repo session_key minted"    "hermes-hands:" "$hkey"
stop_mock

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
(( FAIL == 0 ))
