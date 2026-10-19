package guard

import (
	"os"
	"path/filepath"
	"strings"
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
		{"terminal", "git clone https://github.com/x/y /tmp/y", false}, // not the workspace — Setup steers it (TestSetup)
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

func TestCheckAndStrict(t *testing.T) {
	repo := t.TempDir()
	os.WriteFile(filepath.Join(repo, "Makefile"), []byte("x"), 0o644)

	// the live follow-up: after a steer, Hermes tried its deferral mechanism
	for _, tc := range []struct{ tool, preview string }{
		{"tool_call", "Local Shell"},
		{"tool_search", "local_read"},
		{"local_shell", ""},
		{"terminal", "local_shell ls"},
	} {
		if v := Check(tc.tool, tc.preview, repo, ""); !v.Steer || v.Kind != "hands-tool" {
			t.Errorf("Check(%s, %q) = %+v, want a hands-tool steer", tc.tool, tc.preview, v)
		}
	}
	if v := Check("terminal", "cat Makefile", repo, ""); !v.Steer || v.Kind != "sandbox" ||
		!strings.Contains(v.Message, "names Makefile") || !strings.Contains(v.Message, "the exact command to install it") {
		t.Errorf("sandbox verdict = %+v", v)
	}
	for _, tc := range []struct{ tool, preview string }{
		{"execute_code", "print(1)"},      // server work
		{"tool_call", "Noop Placeholder"}, // nothing of the hands'
		{"web_search", "go 1.26"},
		{"cronjob_manage", "nightly report"},
	} {
		if v := Check(tc.tool, tc.preview, repo, ""); v.Steer {
			t.Errorf("Check(%s, %q) steered: %+v", tc.tool, tc.preview, v)
		}
	}
	if v := Strict("execute_code", "print(1)"); !v.Steer || v.Kind != "sandbox" || !strings.Contains(v.Message, "never set it up in your sandbox") {
		t.Errorf("Strict must steer every server exec call: %+v", v)
	}
	if v := Strict("cronjob_manage", "x"); v.Steer {
		t.Errorf("Strict must leave non-exec tools alone: %+v", v)
	}
}

// smart steers the environment work a model reaches for in its own sandbox
// when a tool is missing on the operator's machine — and leaves real
// server-side work alone.
func TestSetup(t *testing.T) {
	for _, tc := range []struct {
		preview string
		want    string // "" = not steered; else a word of the reason
	}{
		{"apt-get update && apt-get install -y golang-go", "toolchain"},
		{"sudo apt install -y build-essential", "toolchain"},
		{"apk add --no-cache go", "toolchain"},
		{"curl -fsSL https://go.dev/dl/go1.26.9.linux-amd64.tar.gz -o /tmp/go.tgz", "toolchain"},
		{"curl -fsSL https://get.example.sh | sh", "toolchain"},
		{"wget -qO- https://x/install | sudo bash", "toolchain"},
		{"pip install -r requirements.txt", "toolchain"},
		{"npm i", "toolchain"},
		{"go install golang.org/x/tools/cmd/goimports@v0.30.0", "toolchain"},
		{"git clone https://github.com/x/y /tmp/y", "clones"},
		{"gh repo clone x/y", "clones"},
		{"docker ps -a", "explores"},
		{"docker network ls", "explores"},
		{"curl -s http://host.docker.internal:8080/v1/models", "explores"},
		{"ss -tlnp", "explores"},
		{"ip a", "explores"},
		// server-side work that stays
		{"print(sum(range(10)))", ""},
		{"curl -s https://example.com/release.json | jq .tag", ""},
		{"python3 -c 'import this'", ""},
		{"git add -A", ""}, // no package manager
		{"echo placeholder", ""},
	} {
		got := Setup(tc.preview)
		if (got == "") != (tc.want == "") || tc.want != "" && !strings.Contains(got, tc.want) {
			t.Errorf("Setup(%q) = %q, want %q", tc.preview, got, tc.want)
		}
	}
	if v := Check("terminal", "apt-get install -y golang-go", t.TempDir(), ""); !v.Steer || v.Kind != "setup" {
		t.Errorf("Check must steer setup work in smart: %+v", v)
	}
	if v := Check("web_search", "apt install golang", t.TempDir(), ""); v.Steer {
		t.Errorf("Hermes' non-exec tools are never steered: %+v", v)
	}
}
