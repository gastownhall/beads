//go:build unix

package execgrace

import (
	"os"
	"os/exec"
	"syscall"
)

// applySignalCancel replaces CommandContext's hard kill with a SIGTERM to the
// immediate child only — not its process group — so the child can clean up
// the transport children it spawned before the WaitDelay forces the issue.
// A child that already exited reports os.ErrProcessDone, which exec treats
// as a completed cancel; any other signaling error still surfaces from Wait.
func applySignalCancel(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		return cmd.Process.Signal(syscall.SIGTERM)
	}
}