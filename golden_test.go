package main

import (
	"bytes"
	"context"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"hermes-hands/internal/hermesmock"
)

// The golden tests pin what the operator sees: one full turn through the
// real main() — the REPL and --rpc — against the streaming mock, compared
// byte for byte (after masking paths, ids and times) with testdata/golden/.
// A refactor must leave them unchanged; an intended UI change rewrites them
// with `make golden` and the diff shows up in review.

var updateGolden = flag.Bool("update", false, "rewrite testdata/golden/ from the current output")

// TestMain re-runs this test binary as hermes-hands itself when
// HH_GOLDEN_MAIN=1, so a golden test can drive main() in a subprocess with
// its own stdin, stdout and environment.
func TestMain(m *testing.M) {
	if os.Getenv("HH_GOLDEN_MAIN") == "1" {
		os.Args = append([]string{"hermes-hands"}, strings.Fields(os.Getenv("HH_GOLDEN_ARGS"))...)
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// goldenRun is one hermes-hands process: a fresh home, a scratch git repo
// with a README, the streaming mock as the gateway.
type goldenRun struct {
	home, repo string
	env        []string
}

func newGoldenRun(t *testing.T, gatewayURL, approve string) *goldenRun {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("needs bash (the hands' shell)")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("needs git")
	}
	g := &goldenRun{home: t.TempDir(), repo: t.TempDir()}
	if err := os.WriteFile(filepath.Join(g.repo, "README.md"), []byte("demo readme\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", g.repo, "init", "-q", "-b", "main").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	g.env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + g.home,
		"HERMES_HANDS_HOME=" + filepath.Join(g.home, "hermes-hands"),
		"HERMES_API_URL=" + gatewayURL,
		"HERMES_API_KEY=testkey",
		"HERMES_HANDS_ALLOW_HTTP=1",
		"HERMES_HANDS_APPROVE=" + approve,
		"NO_COLOR=1",
		"TERM=dumb",
		"LANG=C.UTF-8",
	}
	return g
}

// run starts hermes-hands with args, feeds it stdin and returns everything
// it wrote — stdout and stderr through one pipe, so in the order written.
func (g *goldenRun) run(t *testing.T, args, stdin string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0])
	cmd.Env = append(g.env, "HH_GOLDEN_MAIN=1", "HH_GOLDEN_ARGS="+args, "GOCOVERDIR="+coverDir(t))
	cmd.Dir = g.repo
	cmd.Stdin = strings.NewReader(stdin)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("hermes-hands %s: %v\n%s", args, err, out.String())
	}
	return g.mask(out.String())
}

// coverDir is where a coverage-instrumented subprocess writes its counters:
// $HH_GOLDEN_COVERDIR for `make cover`, else a scratch dir. Without one, a
// binary built by `go test -cover` prints a warning into the transcript.
func coverDir(t *testing.T) string {
	if d := os.Getenv("HH_GOLDEN_COVERDIR"); d != "" {
		return d
	}
	return t.TempDir()
}

var goldenMasks = []struct {
	re *regexp.Regexp
	to string
}{
	{regexp.MustCompile(`http://127\.0\.0\.1:\d+`), "<GATEWAY>"},
	{regexp.MustCompile(`hh_\d{8}T\d{6}_[0-9a-f]{6}`), "<SESSION>"},
	{regexp.MustCompile(`session \d{8}T\d{6}_[0-9a-f]{6}`), "session <SESSION>"},
	{regexp.MustCompile(`hh-[0-9a-f]{8}-\d{8}T\d{6}`), "<HERMES-SESSION>"},
	{regexp.MustCompile(`"at":"[^"]*"`), `"at":"<AT>"`},
	{regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(Z|[+-]\d{2}:\d{2})?`), "<TIME>"},
	{regexp.MustCompile(`"(ms|local_ms|wait_ms)":\d+`), `"$1":<MS>`},
	{regexp.MustCompile(`<1s|\b\d+h\d{2}m\b|\b\d+m\d{2}s\b|\b\d+[smd]\b`), "<DUR>"},
}

// mask replaces what differs between runs: the scratch paths, session ids,
// timestamps and durations.
func (g *goldenRun) mask(s string) string {
	for _, p := range []string{g.repo, g.home} {
		if real, err := filepath.EvalSymlinks(p); err == nil && real != p {
			s = strings.ReplaceAll(s, real, "<TMP>")
		}
		s = strings.ReplaceAll(s, p, "<TMP>")
	}
	for _, m := range goldenMasks {
		s = m.re.ReplaceAllString(s, m.to)
	}
	return s
}

// golden compares got with testdata/golden/<name>, or rewrites it with -update.
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name)
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v — `make golden` creates it", err)
	}
	if string(want) == got {
		return
	}
	wl, gl := strings.Split(string(want), "\n"), strings.Split(got, "\n")
	for i := 0; i < len(wl) || i < len(gl); i++ {
		w, g := "<end>", "<end>"
		if i < len(wl) {
			w = wl[i]
		}
		if i < len(gl) {
			g = gl[i]
		}
		if w != g {
			t.Errorf("%s: line %d differs — an intended change? `make golden` rewrites it.\n want: %q\n  got: %q\n\nfull output:\n%s", path, i+1, w, g, got)
			return
		}
	}
}

// One REPL turn: Hermes' server call, a steered call and a server approval
// (answered "o"), two hands calls — the shell one approved with "y" — and a
// streamed final with escapes cut by the 5-byte deltas; then /history
// replays the same turn from the event log.
func TestGoldenREPL(t *testing.T) {
	srv := hermesmock.NewStream("golden")
	defer srv.Close()
	g := newGoldenRun(t, srv.URL, "ask")
	golden(t, "repl_turn.txt", g.run(t, "", "show me the readme\no\ny\n/history\n/exit\n"))
}

// Every slash command once, around one turn: the read-only ones (/help,
// /config, /check, /sessions, /session), the ones that change something
// (/yolo twice, /debug on + off, /new), the ones that need a turn first (/out,
// /history, /fork — which the mock refuses), and the mistakes (an unknown
// command, /out without a number).
func TestGoldenREPLCommands(t *testing.T) {
	srv := hermesmock.NewStream("golden")
	defer srv.Close()
	g := newGoldenRun(t, srv.URL, "ask")
	golden(t, "repl_commands.txt", g.run(t, "", strings.Join([]string{
		"/help", "/yolo", "/yolo", "/bogus",
		"show me the readme", "o", "y",
		"/out 2", "/out x", "/history 1", "/sessions", "/session",
		"/debug on", "/debug off", "/check", "/config", "/compact", "/fork",
		"/new", "/history", "/exit",
	}, "\n")+"\n"))
}

// The same turn through --rpc: the JSON lines an editor plugin reads, the
// server approval denied (no prompt over the pipe), then the session's
// recorded events — and every other request type once, including the
// mistakes (an empty turn, a use without an id, an unknown type).
func TestGoldenRPC(t *testing.T) {
	srv := hermesmock.NewStream("golden")
	defer srv.Close()
	g := newGoldenRun(t, srv.URL, "auto")
	golden(t, "rpc_turn.txt", g.run(t, "--rpc", strings.Join([]string{
		`{"id":1,"type":"turn","text":"show me the readme"}`,
		`{"id":2,"type":"history"}`,
		`{"id":3,"type":"check"}`,
		`{"id":4,"type":"debug","text":"on"}`,
		`{"id":5,"type":"debug","text":"off"}`,
		`{"id":6,"type":"compact"}`,
		`{"id":7,"type":"turn","text":""}`,
		`{"id":8,"type":"use"}`,
		`{"id":9,"type":"use","session":"hh_19990101T000000_000000"}`,
		`{"id":10,"type":"new"}`,
		`{"id":11,"type":"bogus"}`,
		`not json`,
	}, "\n")+"\n"))
}
