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

// BackgroundSysProcAttr returns fresh attributes for a long-lived child that
// must outlive the command that starts it: bd db-proxy-child and the dolt
// sql-servers. Unlike Hide it applies whether or not bd has a console, since
// such a child must never hold on to (or pop up) a console window.
//
// CREATE_NO_WINDOW gives the child a console of its own that is never
// displayed. It is the child's, not the parent's, so the child survives the
// parent exiting, and every console program it starts in turn inherits it
// rather than allocating a visible one. CREATE_NEW_PROCESS_GROUP keeps a
// Ctrl+C aimed at the parent's group from reaching it.
//
// Not DETACHED_PROCESS: that leaves the child with no console at all, and
// dolt runs `uname -s`, `uname -r` and `uname -m` on every start; with Git
// for Windows on PATH each is a console program that then gets a fresh,
// visible console, three windows per server start. CREATE_NO_WINDOW is
// ignored alongside it anyway.
func BackgroundSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: backgroundCreationFlags}
}
