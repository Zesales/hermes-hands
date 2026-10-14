package guard

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTargets(t *testing.T) {
	repo := t.TempDir()
	os.WriteFile(filepath.Join(repo, "notes.txt"), []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(repo, "internal", "ui"), 0o755)
	home := "/home/op"

	for _, tc := range []struct {
		tool, preview string
		want          bool
	}{
		// the live misuse cases
		{"terminal", `echo '{"calls":[{"id":"c1","tool":"shell","args":{"cmd":"make count"}}],"final":null}'`, true},
		{"terminal", "make count FILE=notes.txt", true},
		{"terminal", "cd /tmp && make test", true},
		{"terminal", "git status --short", true},
		{"terminal", "wc -w notes.txt", true},
		{"read_file", "internal/ui", true},
		{"read_file", repo + "/notes.txt", true},
		{"terminal", "ls /home/op/projects", true},
		// legitimate server-side work
		{"execute_code", "print(sum(range(10)))", false},
		{"terminal", "curl -s https://example.com/release.json | jq .tag", false},
		{"terminal", "git clone https://github.com/x/y /tmp/y", false},
		{"terminal", "echo placeholder", false},
		{"read_file", "/tmp/scratch.py", false},
		{"terminal", "python3 -c 'import this'", false},
		{"terminal", "", false},
	} {
		got, why := Targets(tc.tool, tc.preview, repo, home)
		if got != tc.want {
			t.Errorf("Targets(%s, %q) = %v (%s), want %v", tc.tool, tc.preview, got, why, tc.want)
		}
		if got && why == "" {
			t.Errorf("Targets(%s, %q): a steer needs a reason", tc.tool, tc.preview)
		}
	}
}
