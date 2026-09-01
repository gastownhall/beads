//go:build unix

package hooks

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"syscall"
)

// runPreWriteCommand keeps the admission timeout from leaking a hook's
// descendants. A hook that starts a helper cannot keep holding a maintenance
// lease after its own admission attempt timed out.
func runPreWriteCommand(ctx context.Context, cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		if cmd.Process != nil {
			if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				return fmt.Errorf("kill pre-write hook process group: %w", err)
			}
		}
		<-done
		return ctx.Err()
	}
}
