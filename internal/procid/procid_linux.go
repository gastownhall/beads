//go:build linux

package procid

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const fallbackSignalConfirmTimeout = 2 * time.Second

var (
	bootIDPath = "/proc/sys/kernel/random/boot_id"
	pidfdOpen  = unix.PidfdOpen
)

// Handle identifies a process through a pidfd when the running kernel supports
// it. On older kernels without pidfds, it falls back to verify, signal, verify;
// that fallback retains a small PID-reuse race.
type Handle struct {
	pid   int
	token Token
	pidfd int
}

// Capture resolves pid's current process-birth token.
func Capture(pid int) (Token, error) {
	bootID, err := os.ReadFile(bootIDPath)
	if err != nil {
		// Do not unwrap this environmental failure: a missing or inaccessible
		// boot_id is not evidence that the requested process is gone.
		return "", &bootIDReadError{path: bootIDPath, err: err}
	}
	startTime, err := processStartTime(pid)
	if err != nil {
		return "", err
	}
	return Token("linux-v1:" + strings.TrimSpace(string(bootID)) + ":" + startTime), nil
}

// Verify reports whether pid still refers to tok. A process which has exited
// returns false, nil.
func Verify(pid int, tok Token) (bool, error) {
	current, err := Capture(pid)
	if err != nil {
		if isGone(err) {
			return false, nil
		}
		return false, err
	}
	return current == tok, nil
}

// Open verifies pid's token and opens a handle suitable for safe signaling.
//
// The pidfd is opened before the token check: a pidfd pins the PID number
// against reuse, so verifying afterwards proves the fd refers to the process
// the token describes. Verifying first would leave a window where the
// verified process exits, the PID is recycled, and the pidfd targets the
// unrelated replacement.
func Open(pid int, tok Token) (*Handle, error) {
	fd, err := pidfdOpen(pid, 0)
	if err != nil {
		if !errors.Is(err, unix.ENOSYS) {
			return nil, fmt.Errorf("procid: pidfd open %d: %w", pid, err)
		}
		// Kernel without pidfds: fall back to verify-then-signal, which
		// retains the documented small PID-reuse race.
		match, verifyErr := Verify(pid, tok)
		if verifyErr != nil {
			return nil, verifyErr
		}
		if !match {
			return nil, fmt.Errorf("procid: process %d does not match token", pid)
		}
		return &Handle{pid: pid, token: tok, pidfd: -1}, nil
	}
	match, verifyErr := Verify(pid, tok)
	if verifyErr != nil {
		_ = unix.Close(fd)
		return nil, verifyErr
	}
	if !match {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("procid: process %d does not match token", pid)
	}
	return &Handle{pid: pid, token: tok, pidfd: fd}, nil
}

// Signal sends sig to the verified process.
func (h *Handle) Signal(sig os.Signal) error {
	if h.pidfd >= 0 {
		unixSig, ok := sig.(syscall.Signal)
		if !ok {
			return fmt.Errorf("procid: unsupported signal %v", sig)
		}
		if err := unix.PidfdSendSignal(h.pidfd, unixSig, nil, 0); err != nil {
			if isFatalSignal(unixSig) && errors.Is(err, unix.ESRCH) {
				// The target exited on its own after Open; a fatal signal's
				// goal is already met, matching the fallback path.
				return nil
			}
			return fmt.Errorf("procid: pidfd signal %d: %w", h.pid, err)
		}
		return nil
	}
	if err := h.verifyThenSignal(sig); err != nil {
		return err
	}
	return nil
}

// Kill sends SIGKILL to the verified process.
func (h *Handle) Kill() error { return h.Signal(syscall.SIGKILL) }

// Close releases the underlying pidfd, if one was opened.
func (h *Handle) Close() error {
	if h.pidfd < 0 {
		return nil
	}
	err := unix.Close(h.pidfd)
	h.pidfd = -1
	if err != nil {
		return fmt.Errorf("procid: close pidfd: %w", err)
	}
	return nil
}

func (h *Handle) verifyThenSignal(sig os.Signal) error {
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

func processStartTime(pid int) (string, error) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "", &processStatReadError{pid: pid, err: err}
	}
	return parseStartTime(string(data))
}

func parseStartTime(stat string) (string, error) {
	endComm := strings.LastIndex(stat, ")")
	if endComm == -1 {
		return "", errors.New("procid: malformed proc stat: missing comm terminator")
	}
	fields := strings.Fields(stat[endComm+1:])
	// The remainder starts with state (field 3), so starttime (field 22) is
	// its twentieth field.
	if len(fields) < 20 {
		return "", errors.New("procid: malformed proc stat: missing starttime")
	}
	if fields[0] == "Z" || fields[0] == "X" || fields[0] == "x" {
		return "", fmt.Errorf("procid: process is no longer running: %w", unix.ESRCH)
	}
	if _, err := strconv.ParseUint(fields[19], 10, 64); err != nil {
		return "", fmt.Errorf("procid: malformed proc stat starttime: %w", err)
	}
	return fields[19], nil
}

type bootIDReadError struct {
	path string
	err  error
}

func (e *bootIDReadError) Error() string {
	return fmt.Sprintf("procid: read boot ID %s: %v", e.path, e.err)
}

type processStatReadError struct {
	pid int
	err error
}

func (e *processStatReadError) Error() string {
	return fmt.Sprintf("procid: read stat for %d: %v", e.pid, e.err)
}

func (e *processStatReadError) Unwrap() error {
	return e.err
}

func isGone(err error) bool {
	var statErr *processStatReadError
	return (errors.As(err, &statErr) && errors.Is(statErr.err, os.ErrNotExist)) ||
		errors.Is(err, unix.ESRCH)
}

// IsProcessGone reports whether err means the referenced process no longer
// exists. Callers use it to distinguish a dead recorded process from a live
// process whose birth token does not match.
func IsProcessGone(err error) bool {
	return isGone(err)
}
