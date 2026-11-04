//go:build !unix

package devstack

import (
	"os/exec"
	"syscall"
)

// Process groups are a POSIX concept. On other platforms a child is signalled
// directly, which is weaker — a grandchild can outlive its parent — but it is
// what the platform offers, and the local stack is developed on Unix.
func setProcessGroup(*exec.Cmd) {}

func signalGroup(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd.Process == nil {
		return nil
	}
	// Only Kill is implemented on Windows; a graceful signal would error and
	// leave the process running, so the kill is what gets delivered.
	if sig == syscall.SIGKILL {
		return cmd.Process.Kill()
	}
	return cmd.Process.Kill()
}
