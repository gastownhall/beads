//go:build windows

package proxy

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

func procAttrDetached() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
	}
}

// requestSpawnedChildStop cannot ask a detached, console-less child to stop on
// Windows, so a timed-out child is killed at once.
func requestSpawnedChildStop(*exec.Cmd) error {
	return errGracefulStopUnsupported
}
