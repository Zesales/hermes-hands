package app

import "hermes-hands/internal/guard"

// sandboxGuard is the policy for Hermes' own tool calls in a hands run
// (HERMES_HANDS_SANDBOX): smart steers only what is aimed at the operator's
// machine or calls a hands tool as Hermes' own (guard.Check), steer every
// server-side exec / file call (guard.Strict), show never steers.
func sandboxGuard(mode, workspace, home string) func(tool, preview string) guard.Verdict {
	switch mode {
	case guard.Steer:
		return guard.Strict
	case guard.Show:
		return func(string, string) guard.Verdict { return guard.Verdict{} }
	}
	return func(tool, preview string) guard.Verdict { return guard.Check(tool, preview, workspace, home) }
}
