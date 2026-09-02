//go:build !unix

package shell

import "os/exec"

// Non-unix: no process groups. The `shell` tool is refused by internal/dispatch
// on windows anyway (bash README: "use WSL").
func setPgid(*exec.Cmd) {}

func killGroup(int) {}
