//go:build unix

package sandbox

import (
	"os/exec"
	"syscall"
)

// configureProcessGroup starts the command in its own process group and, on
// cancellation, kills the whole group, so children such as test binaries
// spawned by a test runner do not outlive the timeout.
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
