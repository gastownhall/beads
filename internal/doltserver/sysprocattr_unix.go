//go:build !windows

package doltserver

import "syscall"

// detachedProcessAttr configures detached child process groups on Unix-like OSes.
func detachedProcessAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}
