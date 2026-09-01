//go:build !windows

package exec

import (
	"os/exec"
	"syscall"
)

// setProcessGroup puts the child in its own process group so the whole
// tree (go command, test binary, anything they spawn) can be killed at
// once.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
