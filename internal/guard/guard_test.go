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
		{"tool_call", "Local Output"},
		{"tool_search", "local_read"},
		{"local_shell", ""},
		{"terminal", "local_shell ls"},
		// the envelope passed to tool_call: its calls have no name
		{"tool_call", "Unnamed call"},
		{"tool_call", "Unnamed call +2"},
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
		{"execute_code", "print(1)"}, // server work
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

// a do-nothing call — after a steer, or to "check the connection" — is
// steered in both modes; a real task that mentions such a word is not
func TestPlaceholder(t *testing.T) {
	repo := t.TempDir()
	for _, tc := range []struct{ tool, preview string }{
		// seen live
		{"tool_call", "Noop Placeholder"},
		{"tool_call", "Mcp Placeholder"},
		{"delegate_task", "1 tasks: noop"},
		{"delegate_task", "1 tasks: noop check"},
		{"terminal", "echo placeholder"},
		// spellings
		{"tool_call", "mcp__noop__placeholder"},
		{"delegate_task", "2 tasks: no-op, no_op"},
		{"execute_code", "dummy"},
	} {
		for mode, v := range map[string]Verdict{"smart": Check(tc.tool, tc.preview, repo, ""), "steer": Strict(tc.tool, tc.preview)} {
			if !v.Steer || v.Kind != "placeholder" || !strings.Contains(v.Message, "first real call") ||
				!strings.Contains(v.Message, "`"+tc.tool+"`") {
				t.Errorf("%s(%s, %q) = %+v, want a placeholder steer", mode, tc.tool, tc.preview, v)
			}
		}
	}
	for _, tc := range []struct{ tool, preview string }{
		{"delegate_task", "1 tasks: research no-op patterns in Go"}, // steer steers it — as a delegation (TestDelegation)
		{"web_search", "placeholder text css"},
		{"tool_call", "Check"},
		{"cronjob_manage", "nightly report"},
	} {
		for mode, v := range map[string]Verdict{"smart": Check(tc.tool, tc.preview, repo, ""), "steer": Strict(tc.tool, tc.preview)} {
			if v.Kind == "placeholder" || v.Steer && !SandboxTools[tc.tool] {
				t.Errorf("%s(%s, %q) = %+v, want no placeholder steer", mode, tc.tool, tc.preview, v)
			}
		}
	}
	if v := Check("tool_call", "Local Shell placeholder", repo, ""); v.Kind != "hands-tool" {
		t.Errorf("a hands tool name wins over a placeholder: %+v", v)
	}
}

// delegate_task's sub-agents work in the server sandbox and never reach the
// hands: steer steers every delegation, smart one aimed at the project
func TestDelegation(t *testing.T) {
	repo := t.TempDir()
	os.MkdirAll(filepath.Join(repo, "internal", "ui"), 0o755)
	if v := Strict("delegate_task", "1 tasks: summarize the release notes"); !v.Steer || v.Kind != "sandbox" ||
		!strings.Contains(v.Message, "exec, file and delegation tools") {
		t.Errorf("Strict must steer a delegation: %+v", v)
	}
	if v := Check("delegate_task", "1 tasks: summarize the release notes", repo, ""); v.Steer {
		t.Errorf("smart leaves a delegation of server work alone: %+v", v)
	}
	if v := Check("delegate_task", "1 tasks: fix the tests in internal/ui", repo, ""); !v.Steer || v.Kind != "sandbox" {
		t.Errorf("smart must steer a delegation aimed at the project: %+v", v)
	}
}
