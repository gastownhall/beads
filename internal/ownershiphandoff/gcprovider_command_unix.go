//go:build unix

package ownershiphandoff

import (
	"errors"
	"os/exec"
	"syscall"
	"time"
)

const gcHandoffPipeDrainDelay = 100 * time.Millisecond

func configureGCHandoffCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			return err
		}
		return nil
	}
	cmd.WaitDelay = gcHandoffPipeDrainDelay
}
