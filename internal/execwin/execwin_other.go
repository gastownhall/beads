//go:build !windows

package execwin

import (
	"os/exec"
	"syscall"
)

// Hide is a strict no-op off Windows: the same command comes back, with
// its configuration and attributes untouched, because no other platform
// allocates a console window for a console-less child.
func Hide(cmd *exec.Cmd) *exec.Cmd {
	return cmd
}

// BackgroundSysProcAttr returns nil off Windows: the long-lived spawn
// sites assign it unconditionally, and nil leaves their existing
// platform-specific attributes unchanged.
func BackgroundSysProcAttr() *syscall.SysProcAttr {
	return nil
}
