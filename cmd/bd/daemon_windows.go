//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// configureDaemonProcess sets up platform-specific process attributes for daemon
func configureDaemonProcess(cmd *exec.Cmd) {
	// Windows doesn't support Setsid, use CREATE_NEW_PROCESS_GROUP instead
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
}
