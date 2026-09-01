#!/usr/bin/env bash
# util.sh - shared helpers. Sourced by every other lib; never run directly.

hh_log()  { printf 'hermes-hands: %s\n' "$*" >&2; }
hh_warn() { printf 'hermes-hands: WARNING: %s\n' "$*" >&2; }
# low-level progress chatter - shown only with HERMES_HANDS_VERBOSE=1
hh_vlog() { [[ -n "${HERMES_HANDS_VERBOSE:-}" ]] && printf '%shermes-hands: %s%s\n' "${UI_DIM:-}" "$*" "${UI_R:-}" >&2; return 0; }

# Print a BLOCKED line and exit non-zero. In --raw contexts callers wrap this.
hh_die() { printf 'BLOCKED: %s\n' "$*"; exit 1; }

hh_need() {
  local c
  for c in "$@"; do
    command -v "$c" >/dev/null 2>&1 || hh_die "required command not found: $c"
  done
}

# sha1 of $1 (for per-cwd session pointers). Falls back across common tools.
hh_sha1() {
  if command -v sha1sum >/dev/null 2>&1; then printf '%s' "$1" | sha1sum | cut -c1-40
  elif command -v shasum >/dev/null 2>&1; then printf '%s' "$1" | shasum | cut -c1-40
  else printf '%s' "$1" | cksum | tr -d ' ' ; fi
}

hh_now() { date -u +%Y-%m-%dT%H:%M:%SZ; }

# --- config / secrets ------------------------------------------------------
# KEY=value files, sourced. Env vars already set win. secrets should be 0600.
hh_load_config() {
  local cfg="${HERMES_HANDS_CONFIG:-${XDG_CONFIG_HOME:-$HOME/.config}/hermes-hands/config}"
  local sec="${HERMES_HANDS_SECRETS:-${XDG_CONFIG_HOME:-$HOME/.config}/hermes-hands/secrets}"
  [[ -r "$cfg" ]] && { set -a; . "$cfg"; set +a; }
  if { [[ -z "${HERMES_API_URL:-}" ]] || [[ -z "${HERMES_API_KEY:-}" ]]; } && [[ -r "$sec" ]]; then
    set -a; . "$sec"; set +a
  fi
  HH_CONFIG_PATH="$cfg"; HH_SECRETS_PATH="$sec"
}

hh_looks_unset() {
  local v="${1:-}"; [[ -z "$v" ]] && return 0
  case "$v" in *REPLACE*|*CHANGE*|*PLACEHOLDER*|*placeholder*|*EXAMPLE*|"<"*">") return 0 ;; esac
  return 1
}

hh_require_https() {   # $1 = url, $2 = var name for messages
  case "$1" in
    https://*) return 0 ;;
    http://127.0.0.1*|http://localhost*|http://0.0.0.0*|http://[::1]*)
      [[ "${HERMES_HANDS_ALLOW_HTTP:-}" == "1" ]] && { hh_warn "$2 is plain http on loopback (tests only)"; return 0; }
      hh_die "$2 is plain http on loopback; set HERMES_HANDS_ALLOW_HTTP=1 only for local tests." ;;
    http://*) hh_die "$2 must be https:// - the bearer token may not cross the network unencrypted." ;;
    *) hh_die "$2 is not a http(s) URL: $1" ;;
  esac
}

# --- json helpers (thin wrappers so intent reads clearly) -----------------
hh_json_get() { printf '%s' "$1" | jq -r "$2" 2>/dev/null || true; }
hh_is_json()  { printf '%s' "$1" | jq -e . >/dev/null 2>&1; }

# --- secret scrubbing: redact credential-shaped strings before they leave --
# the machine (mirrors what Hermes' own adapters do on outbound text).
hh_scrub() {
  sed -E \
    -e 's/(bearer[[:space:]]+)[A-Za-z0-9._~+\/-]{16,}=*/\1«redacted»/Ig' \
    -e 's/(authorization[[:space:]]*[:=][[:space:]]*)[^[:space:]"'\'']+/\1«redacted»/Ig' \
    -e 's/((api[_-]?key|secret|token|passwd|password)[[:space:]]*[:=][[:space:]]*)[^[:space:]"'\'']{6,}/\1«redacted»/Ig' \
    -e 's/(AKIA|ASIA)[A-Z0-9]{16}/«redacted-aws-key»/g' \
    -e 's/sk-[A-Za-z0-9]{20,}/«redacted»/g' \
    -e 's/gh[pousr]_[A-Za-z0-9]{20,}/«redacted»/g' \
    -e 's/-----BEGIN [A-Z ]*PRIVATE KEY-----/«redacted-private-key»/g'
}

# --- approval gate -------------------------------------------------------
# HERMES_HANDS_APPROVE: ask (default) | auto | never
# hh_confirm <one-line summary> [<multiline detail>]  -> 0 approve, 1 deny, 2 abort-turn
HH_APPROVE_ALL=0
hh_confirm() {
  local summary="$1" detail="${2:-}"
  case "${HERMES_HANDS_APPROVE:-ask}" in
    auto) return 0 ;;
    never) hh_warn "denied by policy (HERMES_HANDS_APPROVE=never): $summary"; return 1 ;;
  esac
  (( HH_APPROVE_ALL )) && return 0
  [[ -e /dev/tty ]] || { hh_warn "no tty for approval, denying: $summary"; return 1; }
  {
    printf '\n  \342\232\240  %s\n' "$summary"
    [[ -n "$detail" ]] && printf '%s\n' "$detail" | sed 's/^/      /'
    printf '  [y]es  [n]o  [a]ll  [q]uit turn > '
  } >/dev/tty
  local a; read -r a </dev/tty || a=q
  case "$a" in
    y|Y) return 0 ;;
    a|A) HH_APPROVE_ALL=1; return 0 ;;
    q|Q) return 2 ;;
    *)   return 1 ;;
  esac
}
