# Secrets store (M10)

A built-in, machine-bound, **no-passphrase** encrypted store for the Hermes URL
+ key. Stdlib only — no new dependency.

## Threat model — read this first

This is **at-rest protection only.** It makes a copied / backed-up
`secrets.enc` useless on another machine (or another uid), and keeps the key
out of a plaintext dotfile. The blob is bound to `/etc/machine-id` + your uid —
**not** hostname (dropped as too fragile: renames, DHCP, WSL — for near-zero
gain over "the keyseed is already a 0600 file"). It does **not** defend against anything running as
your uid — including an approved `shell` command — reading the key. The
approval gate + denylist remain the security boundary. No passphrase is asked
for, on purpose.

## Files (`$HERMES_HANDS_HOME` — default `~/hermes-hands/`, both `0600`)

| file | contents |
|---|---|
| `keyseed` | 32 random bytes from `crypto/rand`. The actual secret. Created once by `setup`. |
| `secrets.enc` | `{"v":1,"binds":[…],"nonce":"<b64>","ct":"<b64>"}`. Plaintext payload before encryption is `{"HERMES_API_URL":"…","HERMES_API_KEY":"…"}`. |

`binds` lists the inputs the key was derived from, so decryption is reproducible
from exactly that list: `["keyseed","machine-id","uid"]`, or
`["keyseed","uid"]` when no machine id was available at `setup` time.

## Crypto

- **KDF:** `crypto/hkdf` (stdlib, Go 1.24+) HKDF-SHA256.
  `masterKey = HKDF(ikm = keyseed, salt = machine-id bytes (nil when unbound), info = "hermes-hands secrets v1 uid=<uid>")`, 32 bytes.
- **Cipher:** AES-256-GCM (`crypto/aes` + `crypto/cipher`), a fresh 12-byte
  nonce from `crypto/rand` per write, AAD = the literal `hermes-hands/secrets.enc/v1`.
- **machine-id:** `/etc/machine-id`, then `/var/lib/dbus/machine-id`, else omit
  from `binds` and derive from `keyseed` + uid only. Whitespace trimmed.

## Runtime load order (`internal/config.Load`)

1. env `HERMES_API_URL` / `HERMES_API_KEY` — win (unchanged).
2. `config` file (`$HERMES_HANDS_HOME/config`) — always applied (non-secret
   knobs; unchanged).
3. only if a URL or key is still empty:
   1. `secrets.enc` present → it is authoritative for the fallback. Read
      `keyseed`; a missing `keyseed` or any decrypt/auth failure →
      `hermes-hands: cannot decrypt secrets.enc on this machine — run 'hermes-hands setup'`
      and exit 1 (never a panic, never a silent fall-through). On success, its
      keys fill only the still-empty slots.
   2. else `secrets` (plaintext) present → parsed by the narrowed shell-
      assignment parser (the pre-M10 fallback, so old installs keep working
      until they re-run `setup`).
4. still empty → the existing "run `hermes-hands setup`" error.

Loose permissions on `keyseed` / `secrets.enc` (group/other readable) →
`WARNING: <path> is not 0600` and proceed — a wrong mode never locks you out.

The decrypted payload is never logged (checked against the `vlogf` / verbose
paths); `Load` and the decrypt path emit nothing but the loose-perm warning.

## `setup`

- default: prompt URL + key (key hidden), write `$HERMES_HANDS_HOME/{config,
  secrets.enc, keyseed}`. Nothing added to the shell env, no `~/.bashrc` offer.
- `hermes-hands setup --plaintext`: the pre-M10 behaviour — 0600 `secrets`
  file + the `~/.bashrc` source-line offer, no `.enc` / `keyseed`.
- either way, `setup` then runs `check` as before.
- `/config` (in-REPL) reports which store actually supplied the URL/key
  (`secrets.enc` / plaintext `secrets` / environment).

## Tests (`internal/config/secrets_test.go`, `main_test.go`)

Offline, no real secrets: encrypt→decrypt round-trip (with / without a bound
machine id, via the `machineIDForTest` white-box seam), a tampered `ct` byte →
clean `ErrSecretsUndecryptable` (not a panic), a different machine id → clean
error, `.enc` absent → plaintext fallback, `.enc` + missing `keyseed` → clean
error, env still overrides the store, and `setup` / `setup --plaintext` write
the right files at `0600`.
