//go:build windows

package doltserver

import "syscall"

// detachedProcessAttr configures detached child process groups on Windows.
func detachedProcessAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}
