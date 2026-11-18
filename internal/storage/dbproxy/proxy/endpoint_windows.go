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

// requestSpawnedChildStop has no graceful form on windows: the child runs
// detached with no console, so no console event can reach it, and the only
// signal procid supports is TerminateProcess. killSpawnedChild escalates to
// today's kill at once instead.
func requestSpawnedChildStop(*exec.Cmd) error {
	return errGracefulStopUnsupported
}
