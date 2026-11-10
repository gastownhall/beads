//go:build windows

package execwin

import (
	"os/exec"
	"syscall"
)

func hide(cmd *exec.Cmd) {
	if cmd.SysProcAttr != nil {
		return
	}
	flags, apply := hiddenCreationFlags(0, false)
	if !apply {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags}
}
