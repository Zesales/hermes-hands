package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func withMachineID(t *testing.T, id string) {
	t.Helper()
	old := machineIDForTest
	machineIDForTest = id
	t.Cleanup(func() { machineIDForTest = old })
}

func quietWarn(t *testing.T) {
	t.Helper()
	old := Warnf
	Warnf = func(string, ...any) {}
	t.Cleanup(func() { Warnf = old })
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	withMachineID(t, "test-machine-0001")
	seed := []byte("0123456789abcdef0123456789abcdef") // 32 bytes
	payload := map[string]string{"HERMES_API_URL": "https://h.example.net", "HERMES_API_KEY": "sk-abc123"}

	blob, err := encryptBlob(seed, "test-machine-0001", []string{"keyseed", "machine-id", "uid"}, payload)
	if err != nil {
		t.Fatalf("encryptBlob: %v", err)
	}
	var b encBlob
	if json.Unmarshal(blob, &b) != nil || b.V != 1 {
		t.Fatalf("blob shape wrong: %s", blob)
	}
	got, err := decryptBlob(blob, seed)
	if err != nil {
		t.Fatalf("decryptBlob: %v", err)
	}
	if got["HERMES_API_URL"] != payload["HERMES_API_URL"] || got["HERMES_API_KEY"] != payload["HERMES_API_KEY"] {
		t.Errorf("round-trip mismatch: %v", got)
	}
}

func TestRoundTripWithoutMachineID(t *testing.T) {
	withMachineID(t, "") // no machine id available
	seed := []byte("0123456789abcdef0123456789abcdef")
	blob, err := encryptBlob(seed, "", []string{"keyseed", "uid"}, map[string]string{"HERMES_API_KEY": "k"})
	if err != nil {
		t.Fatal(err)
	}
	var b encBlob
	_ = json.Unmarshal(blob, &b)
	for _, x := range b.Binds {
		if x == "machine-id" {
			t.Fatalf("binds must not contain machine-id: %v", b.Binds)
		}
	}
	if got, err := decryptBlob(blob, seed); err != nil || got["HERMES_API_KEY"] != "k" {
		t.Errorf("decrypt without machine-id = %v, %v", got, err)
	}
}

func TestTamperedCiphertextIsCleanError(t *testing.T) {
	withMachineID(t, "test-machine-0001")
	seed := []byte("0123456789abcdef0123456789abcdef")
	blob, _ := encryptBlob(seed, "test-machine-0001", []string{"keyseed", "machine-id", "uid"},
		map[string]string{"HERMES_API_KEY": "k"})

	var b encBlob
	_ = json.Unmarshal(blob, &b)
	ct := []byte(b.CT)
	ct[len(ct)/2] ^= 0x01 // flip a base64 char -> different / invalid ciphertext
	b.CT = string(ct)
	tampered, _ := json.Marshal(b)

	got, err := decryptBlob(tampered, seed)
	if err != ErrSecretsUndecryptable || got != nil {
		t.Errorf("tampered blob: got %v / %v, want nil / ErrSecretsUndecryptable", got, err)
	}
}

func TestWrongMachineIsCleanError(t *testing.T) {
	seed := []byte("0123456789abcdef0123456789abcdef")
	withMachineID(t, "machine-A")
	blob, _ := encryptBlob(seed, "machine-A", []string{"keyseed", "machine-id", "uid"},
		map[string]string{"HERMES_API_KEY": "k"})

	withMachineID(t, "machine-B") // decrypting elsewhere
	if got, err := decryptBlob(blob, seed); err != ErrSecretsUndecryptable || got != nil {
		t.Errorf("wrong machine: got %v / %v, want nil / ErrSecretsUndecryptable", got, err)
	}
}

func TestMissingKeyseedIsCleanError(t *testing.T) {
	quietWarn(t)
	clearEnv(t)
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	conf := filepath.Join(dir, "hermes-hands")
	mustWrite(t, filepath.Join(conf, "secrets.enc"), `{"v":1,"binds":["keyseed"],"nonce":"AAAAAAAAAAAAAAAA","ct":"AAAA"}`)
	// no keyseed file

	if _, err := Load(); err != ErrSecretsUndecryptable {
		t.Errorf("Load with .enc but no keyseed = %v, want ErrSecretsUndecryptable", err)
	}
}

func TestLoadFromEncryptedStore(t *testing.T) {
	quietWarn(t)
	withMachineID(t, "test-machine-0002")
	clearEnv(t)
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	conf := filepath.Join(dir, "hermes-hands")
	if err := os.MkdirAll(conf, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := WriteEncryptedSecrets(conf, "https://from-enc.example.net", "sk-fromenc"); err != nil {
		t.Fatalf("WriteEncryptedSecrets: %v", err)
	}

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.APIURL != "https://from-enc.example.net" || c.APIKey != "sk-fromenc" {
		t.Errorf("encrypted store not applied: %q / %q", c.APIURL, c.APIKey)
	}

	// files are 0600
	for _, f := range []string{"secrets.enc", "keyseed"} {
		fi, _ := os.Stat(filepath.Join(conf, f))
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s perm = %o, want 600", f, fi.Mode().Perm())
		}
	}

	// env still wins over the encrypted store
	t.Setenv("HERMES_API_URL", "https://from-env.example.net")
	t.Setenv("HERMES_API_KEY", "sk-fromenv")
	c, _ = Load()
	if c.APIURL != "https://from-env.example.net" || c.APIKey != "sk-fromenv" {
		t.Errorf("env should override the encrypted store: %q / %q", c.APIURL, c.APIKey)
	}
}

func TestEncAbsentFallsBackToPlaintext(t *testing.T) {
	quietWarn(t)
	clearEnv(t)
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	mustWrite(t, filepath.Join(dir, "hermes-hands", "secrets"),
		"export HERMES_API_URL=\"https://plain.example.net\"\nexport HERMES_API_KEY='sk-plain'\n")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.APIURL != "https://plain.example.net" || c.APIKey != "sk-plain" {
		t.Errorf("plaintext fallback not applied: %q / %q", c.APIURL, c.APIKey)
	}
}
