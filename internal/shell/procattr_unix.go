//go:build unix

package shell

import (
	"os/exec"
	"syscall"
)

// setPgid puts the shell in its own process group so killGroup can take down
// anything it spawned.
func setPgid(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killGroup(pgid int) {
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}
