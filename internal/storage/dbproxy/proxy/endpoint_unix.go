//go:build unix

package proxy

import (
	"os/exec"
	"syscall"
)

func procAttrDetached() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// requestSpawnedChildStop asks a timed-out proxy child to stop itself, so its
// SIGTERM handler cancels startup and stops the dolt backend it started
// (#8489). The child is bd's own un-reaped process, so its PID cannot have
// been recycled and a direct signal is safe (the same argument as
// killSpawnedChild's no-handle branch). This deliberately avoids
// procid.Handle.Signal: on darwin, and on linux without pidfd support, that
// call treats SIGTERM as fatal, blocks in confirmFatalSignal for up to 2s and
// errors while the target is alive.
func requestSpawnedChildStop(cmd *exec.Cmd) error {
	return cmd.Process.Signal(syscall.SIGTERM)
}
