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

// hide applies hiddenCreationFlags to cmd. A caller's SysProcAttr is never
// written to: the flags go on a copy that keeps every other field, so a
// structure the caller shares between commands is left as it was.
func hide(cmd *exec.Cmd) {
	var flags uint32
	if cmd.SysProcAttr != nil {
		flags = cmd.SysProcAttr.CreationFlags
	}
	flags, apply := hiddenCreationFlags(flags, consoleAttached())
	if !apply {
		return
	}
	attr := &syscall.SysProcAttr{}
	if cmd.SysProcAttr != nil {
		copied := *cmd.SysProcAttr
		attr = &copied
	}
	attr.CreationFlags = flags
	cmd.SysProcAttr = attr
}
