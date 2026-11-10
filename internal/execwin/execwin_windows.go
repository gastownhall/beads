//go:build windows

package execwin

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// consoleAttached reports whether this process has a console attached. It is
// a variable so tests can force either answer; production probes per call,
// because a process can gain or lose its console (AllocConsole, FreeConsole)
// while it runs.
var consoleAttached = probeConsoleAttached

// probeConsoleAttached asks for the console's input code page, which
// GetConsoleCP can only report for an attached console: zero, or an error,
// means there is none. Whether stdout is a terminal does not answer this, as
// an attached console can have every standard stream redirected.
func probeConsoleAttached() bool {
	cp, err := windows.GetConsoleCP()
	return err == nil && cp != 0
}

func hide(cmd *exec.Cmd) {
	if cmd.SysProcAttr != nil {
		return
	}
	flags, apply := hiddenCreationFlags(0, consoleAttached())
	if !apply {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags}
}
