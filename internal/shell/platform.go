//go:build !(linux || darwin)

package shell

// hermes-hands supports Linux (WSL included) and macOS only. Native Windows
// has no bash for the `shell` tool, no /dev/tty, no process groups — on
// Windows, run hermes-hands inside WSL. This file only exists to make that a
// readable compile error instead of a half-working binary.
var _ = hermesHandsSupportsLinuxAndMacOSOnly_OnWindowsUseWSL
