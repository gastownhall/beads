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
// matters for dolt, which is a console application. DETACHED_PROCESS also
// hides the child's own window but leaves it with no console at all, so every
// console program the child starts in turn (dolt runs `uname` on every start)
// gets a fresh, visible console. A hidden console is inherited by them instead.
func NoWindowSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
}
