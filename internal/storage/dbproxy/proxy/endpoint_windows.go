//go:build windows

package proxy

import (
	"syscall"

	"golang.org/x/sys/windows"
)

func procAttrDetached() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
	}
}
