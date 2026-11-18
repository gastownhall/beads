//go:build unix

package proxy

import (
	"os/exec"
	"syscall"
)

func procAttrDetached() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// requestSpawnedChildStop asks a timed-out proxy child to shut down. It
// signals through cmd.Process rather than procid.Handle.Signal: the child is
// our own un-reaped process, so its PID cannot have been recycled, and on
// darwin and the Linux no-pidfd fallback Handle.Signal treats SIGTERM as fatal
// and blocks confirming an exit the grace period is meant to allow.
func requestSpawnedChildStop(cmd *exec.Cmd) error {
	return cmd.Process.Signal(syscall.SIGTERM)
}
