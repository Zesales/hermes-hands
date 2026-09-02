package config

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
)

// A machine-bound, no-passphrase encrypted secrets store. This is at-rest
// protection only: it does NOT defend against a same-uid read or an approved
// shell — the approval gate + denylist remain the security boundary. The point
// is that a copied `secrets.enc` alone is useless (it is bound to the machine
// id + uid + hostname, and the real secret is a separate 0600 `keyseed` file).

const secretsAAD = "hermes-hands/secrets.enc/v1"

// ErrSecretsUndecryptable is returned by Load when secrets.enc is present but
// cannot be decrypted here (wrong machine, tampered blob, or a missing
// keyseed). The caller prints it and exits 1; it never panics.
var ErrSecretsUndecryptable = errors.New("cannot decrypt secrets.enc on this machine — run 'hermes-hands setup'")

// machineIDForTest, when non-empty, replaces the /etc/machine-id lookup. Set
// only by tests (white-box).
var machineIDForTest string

// encBlob is the on-disk secrets.enc JSON.
type encBlob struct {
	V     int      `json:"v"`
	Binds []string `json:"binds"`
	Nonce string   `json:"nonce"`
	CT    string   `json:"ct"`
}

// resolveMachineID returns the host's machine id, or "" when none is available.
func resolveMachineID() string {
	if machineIDForTest != "" {
		return machineIDForTest
	}
	for _, p := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
		if b, err := os.ReadFile(p); err == nil {
			if s := strings.TrimSpace(string(b)); s != "" {
				return s
			}
		}
	}
	return ""
}

func currentUID() string { return strconv.Itoa(os.Getuid()) }

func currentHost() string {
	h, _ := os.Hostname()
	return h
}

// deriveKey = HKDF-SHA256(ikm = keyseed, salt = machine-id bytes (nil when not
// bound), info = "hermes-hands secrets v1 uid=<uid> host=<host>"), 32 bytes.
func deriveKey(keyseed, salt []byte, uid, host string) ([]byte, error) {
	info := "hermes-hands secrets v1 uid=" + uid + " host=" + host
	return hkdf.Key(sha256.New, keyseed, salt, info, 32)
}

// encryptBlob produces the secrets.enc bytes for the given inputs. binds records
// which inputs were used so decryption is reproducible from exactly that list.
func encryptBlob(keyseed []byte, machineID string, binds []string, payload map[string]string) ([]byte, error) {
	pt, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	var salt []byte
	if slices.Contains(binds, "machine-id") {
		salt = []byte(machineID)
	}
	dk, err := deriveKey(keyseed, salt, currentUID(), currentHost())
	if err != nil {
		return nil, err
	}
	gcm, err := newGCM(dk)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	ct := gcm.Seal(nil, nonce, pt, []byte(secretsAAD))
	return json.Marshal(encBlob{
		V:     1,
		Binds: binds,
		Nonce: base64.StdEncoding.EncodeToString(nonce),
		CT:    base64.StdEncoding.EncodeToString(ct),
	})
}

// decryptBlob reverses encryptBlob, deriving from exactly the inputs listed in
// the blob's binds. Every failure path returns ErrSecretsUndecryptable.
func decryptBlob(blob, keyseed []byte) (map[string]string, error) {
	var b encBlob
	if json.Unmarshal(blob, &b) != nil || b.V != 1 {
		return nil, ErrSecretsUndecryptable
	}
	var salt []byte
	if slices.Contains(b.Binds, "machine-id") {
		mid := resolveMachineID()
		if mid == "" {
			return nil, ErrSecretsUndecryptable
		}
		salt = []byte(mid)
	}
	dk, err := deriveKey(keyseed, salt, currentUID(), currentHost())
	if err != nil {
		return nil, ErrSecretsUndecryptable
	}
	nonce, nerr := base64.StdEncoding.DecodeString(b.Nonce)
	ct, cerr := base64.StdEncoding.DecodeString(b.CT)
	if nerr != nil || cerr != nil || len(nonce) != 12 {
		return nil, ErrSecretsUndecryptable
	}
	gcm, err := newGCM(dk)
	if err != nil {
		return nil, ErrSecretsUndecryptable
	}
	pt, err := gcm.Open(nil, nonce, ct, []byte(secretsAAD))
	if err != nil {
		return nil, ErrSecretsUndecryptable
	}
	var payload map[string]string
	if json.Unmarshal(pt, &payload) != nil {
		return nil, ErrSecretsUndecryptable
	}
	return payload, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key) // 32-byte key -> AES-256
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// WriteEncryptedSecrets mints a fresh 32-byte keyseed, binds to machine
// id/uid/host (machine id omitted when unavailable), and writes keyseed +
// secrets.enc into dir, both 0600.
func WriteEncryptedSecrets(dir, url, key string) error {
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		return err
	}
	binds := []string{"keyseed", "uid", "host"}
	mid := resolveMachineID()
	if mid != "" {
		binds = []string{"keyseed", "machine-id", "uid", "host"}
	}
	blob, err := encryptBlob(seed, mid, binds, map[string]string{
		"HERMES_API_URL": url,
		"HERMES_API_KEY": key,
	})
	if err != nil {
		return err
	}
	if err := os.WriteFile(dir+"/keyseed", seed, 0o600); err != nil {
		return err
	}
	return os.WriteFile(dir+"/secrets.enc", blob, 0o600)
}

// warnIfLoose warns (without failing) when a secret file is group/other
// readable. POSIX only — Windows perm bits are not meaningful here.
func warnIfLoose(path string) {
	if runtime.GOOS == "windows" {
		return
	}
	if fi, err := os.Stat(path); err == nil && fi.Mode().Perm()&0o077 != 0 {
		Warnf("%s is not 0600", path)
	}
}
