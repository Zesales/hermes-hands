package debuglog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteRedactsEverything(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s", "debug.jsonl")
	l := New(path, "hk-live-SECRETKEY123", "abc") // "abc" too short to blank
	l.Write("run.start", map[string]any{
		"input":  "deploy with password: hunter2pass and key hk-live-SECRETKEY123",
		"data":   `{"event":"tool.started","preview":"curl -H 'Authorization: Bearer abcdefghijklmnop0123'"}`,
		"nested": map[string]any{"token": "ghp_" + strings.Repeat("A", 30)},
		"list":   []any{"sk-" + strings.Repeat("b", 24)},
		"status": 200,
	})
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, leak := range []string{"hunter2pass", "SECRETKEY123", "abcdefghijklmnop0123", "ghp_AAAA", "sk-bbbb"} {
		if strings.Contains(got, leak) {
			t.Errorf("trace leaks %q:\n%s", leak, got)
		}
	}
	if !strings.Contains(got, `"kind":"run.start"`) || !strings.Contains(got, `"status":200`) || !strings.Contains(got, "deploy with") {
		t.Errorf("trace lost content:\n%s", got)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("trace perms = %v", fi.Mode().Perm())
	}
	dir, _ := os.Stat(filepath.Dir(path))
	if dir.Mode().Perm() != 0o700 {
		t.Errorf("trace dir perms = %v", dir.Mode().Perm())
	}
}

func TestWriteCapsHugeFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "debug.jsonl")
	New(path).Write("sse", map[string]any{"data": strings.Repeat("x", maxField*3)})
	b, _ := os.ReadFile(path)
	if len(b) > maxField+200 || !strings.Contains(string(b), "[truncated]") {
		t.Errorf("field not capped: %d bytes", len(b))
	}
}
