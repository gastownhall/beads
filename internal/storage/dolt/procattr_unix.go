//go:build cgo && !windows

package dolt

import (
	"os/exec"
	"syscall"
)

func setDoltServerSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
}
