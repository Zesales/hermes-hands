package redact

import (
	"strings"
	"testing"
)

func TestScrubPerPattern(t *testing.T) {
	cases := []struct {
		name       string
		in         string
		wantSub    string // must appear in output
		wantAbsent string // must NOT appear in output
	}{
		{"bearer", "Authorization: Bearer abcdefghijklmnop0123", "«redacted»", "abcdefghijklmnop0123"},
		{"bearer too short", "bearer short123", "bearer short123", "«redacted»"},
		{"authorization kv", "authorization=Basicdeadbeefxyz", "authorization=«redacted»", "deadbeefxyz"},
		{"api_key kv", "api_key: s3cr3tvalue", "api_key: «redacted»", "s3cr3tvalue"},
		{"password kv", "password = hunter2hunter", "password = «redacted»", "hunter2hunter"},
		{"secret too short", "secret: abc", "secret: abc", "«redacted»"},
		{"aws akia", "id AKIAABCDEFGHIJKLMNOP end", "«redacted-aws-key»", "AKIAABCDEFGHIJKLMNOP"},
		{"aws lowercase no match", "akiaabcdefghijklmnop", "akiaabcdefghijklmnop", "«redacted-aws-key»"},
		{"sk key", "token sk-ABCDEFGHIJKLMNOPQRSTUVWX", "«redacted»", "sk-ABCDEFGHIJKLMNOPQRSTUVWX"},
		{"gh token", "ghp_ABCDEFGHIJKLMNOPQRSTU1234567890", "«redacted»", "ghp_ABCDEFGHIJKLMNOPQRSTU"},
		{"pem header", "-----BEGIN OPENSSH PRIVATE KEY-----", "«redacted-private-key»", "BEGIN OPENSSH"},
		{"plain prose untouched", "the quick brown fox jumps over 12345", "the quick brown fox jumps over 12345", "«redacted"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Scrub(tc.in)
			if !strings.Contains(got, tc.wantSub) {
				t.Errorf("Scrub(%q) = %q, want substring %q", tc.in, got, tc.wantSub)
			}
			if tc.wantAbsent != "" && strings.Contains(got, tc.wantAbsent) {
				t.Errorf("Scrub(%q) = %q, still contains %q", tc.in, got, tc.wantAbsent)
			}
		})
	}
}

func TestScrubMultipleHitsOneString(t *testing.T) {
	in := "Bearer abcdefghijklmnop0123456 and api_key=supersecretvalue and ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ012345"
	got := Scrub(in)
	for _, leak := range []string{"abcdefghijklmnop0123456", "supersecretvalue", "ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ"} {
		if strings.Contains(got, leak) {
			t.Errorf("leak %q survived: %q", leak, got)
		}
	}
	if n := strings.Count(got, "«redacted»"); n < 3 {
		t.Errorf("want at least 3 «redacted» markers, got %d in %q", n, got)
	}
}

func TestScrubGuillemetsByteExact(t *testing.T) {
	got := Scrub("api_key: abcdef123456")
	want := "api_key: «redacted»"
	if got != want {
		t.Errorf("Scrub = %q, want %q (guillemets must be U+00AB / U+00BB)", got, want)
	}
}
