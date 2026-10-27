//go:build windows

package proxy

import (
	"syscall"

	"golang.org/x/sys/windows"
)

// procAttrDetached starts bd db-proxy-child in its own process group with a
// console of its own that is never displayed (CREATE_NO_WINDOW), so it
// survives the parent exiting and never shows a window.
//
// It is deliberately not DETACHED_PROCESS. That leaves the child with no
// console, and then every console program anywhere below it — dolt, git,
// PowerShell, and the `uname` calls dolt makes on every start — gets a fresh,
// visible console, which Windows Terminal shows as a window when it is the
// registered default terminal. A hidden console is inherited by all of them.
func procAttrDetached() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP,
	}
}
