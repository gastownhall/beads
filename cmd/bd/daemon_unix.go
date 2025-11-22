//go:build unix || linux || darwin

package main

import (
	"os"
	"os/exec"
	"syscall"
)

var daemonSignals = []os.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP}

// configureDaemonProcess sets up platform-specific process attributes for daemon
func configureDaemonProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

func sendStopSignal(process *os.Process) error {
	return process.Signal(syscall.SIGTERM)
}

func isReloadSignal(sig os.Signal) bool {
	return sig == syscall.SIGHUP
}

// isProcessRunning checks if a process with the given PID is running.
// Permission-aware: handles EPERM (operation not permitted) correctly.
//
// In sandboxed environments, syscall.Kill may return EPERM even when the process
// exists. We treat EPERM as "process exists but we can't signal it", which means
// it's still running from our perspective.
//
// Implements bd-e0o: Phase 3 permission-aware process checks for GH #353
func isProcessRunning(pid int) bool {
	err := syscall.Kill(pid, 0)
	if err == nil {
		// No error = process exists and we can signal it
		return true
	}
	if err == syscall.EPERM {
		// EPERM = operation not permitted
		// Process exists but we don't have permission to signal it
		// This happens in sandboxed environments (Codex, containers)
		// Treat this as "process is running"
		return true
	}
	// ESRCH = no such process
	// Any other error = process not running
	return false
}
