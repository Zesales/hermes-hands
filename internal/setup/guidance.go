package setup

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Zesales/hermes-hands/internal/app"
)

// globalGuidanceSources are the home-directory rules files other agents read:
// the import candidates, in this order.
var globalGuidanceSources = []string{".claude/CLAUDE.md", ".codex/AGENTS.md", "AGENTS.md", "CLAUDE.md", ".config/AGENTS.md"}

// ImportGuidance offers to snapshot the global agent rules found in the
// home directory into <dir>/guidance.md (0600). A snapshot, not a link: edit
// it freely; `hermes-hands guidance` re-imports. Asks before replacing an
// existing import; identical sources are imported once.
func ImportGuidance(in *bufio.Reader, dir string) {
	home := os.Getenv("HOME")
	if home == "" {
		return
	}
	type src struct {
		path string
		body string
	}
	var found []src
	seen := map[string]bool{}
	for _, rel := range globalGuidanceSources {
		p := filepath.Join(home, rel)
		b, err := os.ReadFile(p)
		body := strings.TrimSpace(string(b))
		if err != nil || body == "" || seen[body] {
			continue
		}
		seen[body] = true
		found = append(found, src{p, body})
	}
	if len(found) == 0 {
		return
	}
	fmt.Fprintln(os.Stderr, "\nGlobal agent rules in your home directory:")
	for _, f := range found {
		fmt.Fprintf(os.Stderr, "  %s  (%d bytes)\n", f.path, len(f.body))
	}
	dst := filepath.Join(dir, app.GlobalGuidanceFile)
	q := "Import them as your global rules for Hermes (→ " + dst + ")? [y/N] "
	if _, err := os.Stat(dst); err == nil {
		q = "Replace " + dst + " with a fresh import of them? [y/N] "
	}
	if !strings.EqualFold(promptLine(in, q), "y") {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<!-- hermes-hands global rules, imported %s from:\n", time.Now().Format("2006-01-02"))
	for _, f := range found {
		fmt.Fprintf(&b, "     %s\n", f.path)
	}
	b.WriteString("     A snapshot — edit freely; `hermes-hands guidance` re-imports. -->\n")
	for _, f := range found {
		fmt.Fprintf(&b, "\n## from %s\n\n%s\n", f.path, f.body)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "hermes-hands: %v\n", err)
		return
	}
	if err := os.WriteFile(dst, []byte(b.String()), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "hermes-hands: %v\n", err)
		return
	}
	fmt.Printf("wrote %s — every session now starts with these rules\n", dst)
}
