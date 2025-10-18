//go:build unix || linux || darwin

package main

import (
	"os/exec"
	"syscall"
)

// configureDaemonProcess sets up platform-specific process attributes for daemon
func configureDaemonProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
