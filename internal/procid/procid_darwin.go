//go:build darwin

package procid

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const fallbackSignalConfirmTimeout = 2 * time.Second

// Handle verifies before and after signaling. macOS has no pidfd equivalent
// here, so a residual PID-reuse race remains between those operations.
type Handle struct {
	pid   int
	token Token
}

func Capture(pid int) (Token, error) {
	proc, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return "", fmt.Errorf("procid: sysctl process %d: %w", pid, err)
	}
	started := proc.Proc.P_starttime
	return Token(fmt.Sprintf("darwin-v1:%d.%d", started.Sec, started.Usec)), nil
}

func Verify(pid int, tok Token) (bool, error) {
	current, err := Capture(pid)
	if err != nil {
		if IsProcessGone(err) {
			return false, nil
		}
		return false, err
	}
	return current == tok, nil
}

func Open(pid int, tok Token) (*Handle, error) {
	match, err := Verify(pid, tok)
	if err != nil {
		return nil, err
	}
	if !match {
		return nil, fmt.Errorf("procid: process %d does not match token", pid)
	}
	return &Handle{pid: pid, token: tok}, nil
}

func (h *Handle) Signal(sig os.Signal) error {
	match, err := Verify(h.pid, h.token)
	if err != nil {
		return err
	}
	if !match {
		return fmt.Errorf("procid: process %d no longer matches token", h.pid)
	}
	unixSig, ok := sig.(syscall.Signal)
	if !ok {
		return fmt.Errorf("procid: unsupported signal %v", sig)
	}
	if err := syscall.Kill(h.pid, unixSig); err != nil {
		if isFatalSignal(unixSig) && errors.Is(err, unix.ESRCH) {
			return nil
		}
		return fmt.Errorf("procid: signal %d: %w", h.pid, err)
	}
	if isFatalSignal(unixSig) {
		return h.confirmFatalSignal()
	}
	match, err = Verify(h.pid, h.token)
	if err != nil {
		return err
	}
	if !match {
		return fmt.Errorf("procid: process %d no longer matches token", h.pid)
	}
	return nil
}

func (h *Handle) confirmFatalSignal() error {
	deadline := time.Now().Add(fallbackSignalConfirmTimeout)
	for {
		match, err := Verify(h.pid, h.token)
		if err != nil {
			return err
		}
		if !match {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf(
				"procid: process %d still matches token after fatal signal and %s re-check",
				h.pid,
				fallbackSignalConfirmTimeout,
			)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func isFatalSignal(sig syscall.Signal) bool {
	return sig == syscall.SIGKILL || sig == syscall.SIGTERM
}

func (h *Handle) Kill() error { return h.Signal(syscall.SIGKILL) }

func (h *Handle) Close() error { return nil }

// IsProcessGone reports whether err means the referenced process no longer
// exists.
func IsProcessGone(err error) bool {
	// SysctlKinfoProc returns EIO when kern.proc.pid produces a zero-length
	// result, which is Darwin's normal missing-pid result.
	return errors.Is(err, unix.ESRCH) || errors.Is(err, unix.EIO)
}
