package app

import (
	"os"
	"strings"

	"github.com/Zesales/hermes-hands/internal/knowsystem"
)

// GlobalGuidanceFile (in the hermes-hands home) holds the operator's global
// agent rules, imported once at setup — stored knowledge every session puts in
// front of the repo's own AGENTS.md / CLAUDE.md, which are read fresh at each
// session start.
const GlobalGuidanceFile = "guidance.md"

// buildInstructions is everything Hermes is told up front, in one stable
// block (stable = cacheable): the protocol and behaviour (instructions.md),
// the known-system brief, then the operator's AGENTS.md / CLAUDE.md rules.
func buildInstructions(base string, known *knowsystem.System) string {
	s := base + "\n\n" + known.Brief()
	if g := known.GuidanceBlock(); g != "" {
		s += "\n\n" + g
	}
	return s
}

// resolveInstructions is the instructions ladder:
// $HERMES_HANDS_INSTRUCTIONS | ~/hermes-hands/instructions.md (first readable
// & non-empty) else the embedded share/instructions.md.
func resolveInstructions(path, builtin string) string {
	if b, err := os.ReadFile(path); err == nil {
		if s := strings.TrimRight(string(b), "\n"); s != "" {
			return s
		}
	}
	return strings.TrimRight(builtin, "\n")
}
