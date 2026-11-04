//go:build unix

package devstack

import (
	"os/exec"
	"syscall"
)

// setProcessGroup puts the child in a process group of its own.
//
// Two things follow, and both are the point. A signal sent to the negative
// group id reaches the service and everything it spawned, so no grandchild
// survives to hold a port. And the terminal's Ctrl-C no longer reaches the
// children at all, because they have left the foreground group — which turns
// shutdown from a race between the terminal and the supervisor into a
// sequence the supervisor controls.
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// signalGroup delivers sig to the child's whole process group.
//
// It falls back to the process itself if the group is already gone, which
// happens when the child exited between the liveness check and the signal.
func signalGroup(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd.Process == nil {
		return nil
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		return cmd.Process.Signal(sig)
	}
	// Refuse to signal our own group: that would deliver the signal to the
	// supervisor too, and a SIGKILL there would strand every child.
	if pgid == syscall.Getpgrp() {
		return cmd.Process.Signal(sig)
	}
	return syscall.Kill(-pgid, sig)
}
