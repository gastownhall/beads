//go:build windows

package execwin

import (
	"syscall"

	"golang.org/x/sys/windows"
)

// NoWindowSysProcAttr returns the SysProcAttr that keeps a console-subsystem
// child from allocating (and showing) a console of its own.
//
// CREATE_NO_WINDOW is the right flag for a command whose output the parent
// captures through pipes: the child still gets a console object, so anything
// that queries console state keeps working, but it is never displayed. That
// matters for dolt, which is a console application — DETACHED_PROCESS would
// also hide the window but leaves the child with no console at all, which is
// the correct choice only for a server we intend to orphan.
func NoWindowSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
}
