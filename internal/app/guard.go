package app

import "hermes-hands/internal/guard"

// sandboxGuard is the policy for Hermes' own tool calls in a hands run
// (HERMES_HANDS_SANDBOX): steer (the default) steers every server-side exec /
// file call (guard.Strict), smart only what is meant for the operator's
// project or sets up an environment (guard.Check), show never steers.
func sandboxGuard(mode, workspace, home string) func(tool, preview string) guard.Verdict {
	switch mode {
	case guard.Smart:
		return func(tool, preview string) guard.Verdict { return guard.Check(tool, preview, workspace, home) }
	case guard.Show:
		return func(string, string) guard.Verdict { return guard.Verdict{} }
	}
	return guard.Strict
}
