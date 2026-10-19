// Package excerpt picks the lines of a command's output worth showing —
// deterministic, no model involved. The terminal draws them under a call
// (internal/ui); an elided result in a chat checkpoint carries its gist
// (internal/session), so Hermes can tell whether reading it back is worth it.
package excerpt

import (
	"regexp"
	"strings"
)

// Row is one excerpt line, or (Gap > 0) a "… N lines" marker.
type Row struct {
	Text string
	Gap  int
}

var (
	ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
	errRe  = regexp.MustCompile(`(?i)\b(error|fail(ed|ure)?|fatal|panic|denied|not found|no such|cannot|undefined|exception)\b`)
)

// Lines splits output for display: ANSI and carriage returns stripped,
// trailing blank lines dropped.
func Lines(out string) []string {
	out = ansiRe.ReplaceAllString(out, "")
	out = strings.ReplaceAll(out, "\r", "")
	out = strings.TrimRight(out, "\n \t")
	if strings.TrimSpace(out) == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// Pick picks the lines worth showing:
//   - up to 6 lines: all of them
//   - success: the first 2 and the last 3 (what ran, how it ended)
//   - failure: the last 5 (errors land at the end) plus up to 3 earlier lines
//     that look like errors
//
// Hidden stretches become one "… N lines" marker each.
func Pick(lines []string, exit int) []Row {
	n := len(lines)
	if n == 0 {
		return nil
	}
	keep := make([]bool, n)
	switch {
	case n <= 6:
		for i := range keep {
			keep[i] = true
		}
	case exit == 0:
		keep[0], keep[1] = true, true
		for i := n - 3; i < n; i++ {
			keep[i] = true
		}
	default:
		for i := n - 5; i < n; i++ {
			keep[i] = true
		}
		found := 0
		for i := 0; i < n-5 && found < 3; i++ {
			if errRe.MatchString(lines[i]) {
				keep[i] = true
				found++
			}
		}
	}
	var rows []Row
	gap := 0
	for i, k := range keep {
		if !k {
			gap++
			continue
		}
		if gap > 0 {
			rows = append(rows, Row{Gap: gap})
			gap = 0
		}
		rows = append(rows, Row{Text: lines[i]})
	}
	if gap > 0 {
		rows = append(rows, Row{Gap: gap})
	}
	return rows
}

// Gist is one line about how a command ended, at most max bytes: on
// success its last line, on failure the first line that looks like an
// error and the last line. "" for no output.
func Gist(out string, exit, max int) string {
	lines := Lines(out)
	if len(lines) == 0 {
		return ""
	}
	last := strings.TrimSpace(lines[len(lines)-1])
	parts := []string{last}
	if exit != 0 {
		for _, l := range lines[:len(lines)-1] {
			if errRe.MatchString(l) {
				if e := strings.TrimSpace(l); e != last {
					parts = []string{e, last}
				}
				break
			}
		}
	}
	g := strings.Join(strings.Fields(strings.Join(parts, " ⏎ ")), " ")
	if len(g) > max {
		i := max
		for i > 0 && i < len(g) && (g[i]&0xC0) == 0x80 {
			i--
		}
		g = g[:i] + "…"
	}
	return g
}
