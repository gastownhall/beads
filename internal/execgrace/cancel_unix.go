//go:build !windows

package execgrace

import (
	"os/exec"
	"syscall"
)

// setGracefulCancel sends SIGTERM to the immediate child on cancellation. An
// already-exited child makes Signal return os.ErrProcessDone, which os/exec
// treats like the default Kill's.
func setGracefulCancel(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		return cmd.Process.Signal(syscall.SIGTERM)
	}
}
