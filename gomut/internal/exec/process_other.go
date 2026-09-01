//go:build windows

package exec

import "os/exec"

// setProcessGroup is a no-op on Windows (process groups are handled by
// the job object API, which v1 does not use).
func setProcessGroup(cmd *exec.Cmd) {}

func killProcessGroup(cmd *exec.Cmd) error {
	return cmd.Process.Kill()
}
