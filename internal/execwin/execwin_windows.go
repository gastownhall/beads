//go:build windows

package execwin

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// attachedToConsole reports whether this process has a console attached.
// GetConsoleCP returns 0 on failure, and "no console attached" is exactly
// that failure, so nonzero means a live console. A stdout TTY check would
// not do: an attached console can have redirected stdio, and the console
// allocation this package prevents depends on attachment, not on stdio.
// It is a var so the Windows tests can force both outcomes.
var attachedToConsole = func() bool {
	return windows.GetConsoleCP() != 0
}

// Hide applies the console policy to cmd and returns it. See the package
// comment: attached parents and explicit console choices are left alone;
// a console-less parent gets CREATE_NO_WINDOW so the child's console is
// hidden rather than a new visible window.
func Hide(cmd *exec.Cmd) *exec.Cmd {
	if cmd == nil || attachedToConsole() {
		return cmd
	}
	if cmd.SysProcAttr != nil {
		return cmd
	}
	flags, changed := hideCreationFlags(false, 0)
	if !changed {
		return cmd
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags}
	return cmd
}

// BackgroundSysProcAttr returns the process attributes for a long-lived
// child that must never show a window but must keep its own hidden
// console, in its own process group so it survives its parent exiting.
// Each call allocates fresh attributes; no two callers share a struct.
func BackgroundSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP}
}
