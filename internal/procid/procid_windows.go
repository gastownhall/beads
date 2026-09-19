//go:build windows

package procid

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"golang.org/x/sys/windows"
)

// Handle keeps a Windows process handle open to prevent PID reuse.
type Handle struct {
	process windows.Handle
	token   Token
}

// errProcessExited marks a process which is terminated but whose PID is still
// resolvable because some handle (ours or a third party's, such as Task
// Manager or an antivirus scanner) keeps the process object alive. Treating
// it as gone keeps the invariant "Verify == true implies running" on Windows.
var errProcessExited = errors.New("procid: process has exited")

// stillActive is the GetExitCodeProcess sentinel for a running process
// (STILL_ACTIVE, 259).
const stillActive = 259

func Capture(pid int) (Token, error) {
	process, err := openProcess(pid, windows.PROCESS_QUERY_LIMITED_INFORMATION)
	if err != nil {
		return "", fmt.Errorf("procid: open process %d: %w", pid, err)
	}
	defer func() { _ = windows.CloseHandle(process) }()
	return tokenForProcess(process)
}

func Verify(pid int, tok Token) (bool, error) {
	process, err := openProcess(pid, windows.PROCESS_QUERY_LIMITED_INFORMATION)
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return false, nil
		}
		return false, fmt.Errorf("procid: open process %d: %w", pid, err)
	}
	defer func() { _ = windows.CloseHandle(process) }()
	current, err := tokenForProcess(process)
	if err != nil {
		if errors.Is(err, errProcessExited) {
			return false, nil
		}
		return false, err
	}
	return current == tok, nil
}

func Open(pid int, tok Token) (*Handle, error) {
	process, err := openProcess(pid, windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE)
	if err != nil {
		return nil, fmt.Errorf("procid: open process %d: %w", pid, err)
	}
	current, err := tokenForProcess(process)
	if err != nil || current != tok {
		_ = windows.CloseHandle(process)
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("procid: process %d does not match token", pid)
	}
	return &Handle{process: process, token: tok}, nil
}

func (h *Handle) Signal(os.Signal) error {
	if err := h.verify(); err != nil {
		if errors.Is(err, errProcessExited) {
			// The target exited on its own after Open; termination's goal is
			// already met.
			return nil
		}
		return err
	}
	if err := windows.TerminateProcess(h.process, 1); err != nil {
		if _, exitedErr := tokenForProcess(h.process); errors.Is(exitedErr, errProcessExited) {
			return nil
		}
		return fmt.Errorf("procid: terminate process: %w", err)
	}
	return nil
}

func (h *Handle) Kill() error { return h.Signal(os.Kill) }

func (h *Handle) Close() error {
	if h.process == 0 {
		return nil
	}
	err := windows.CloseHandle(h.process)
	h.process = 0
	if err != nil {
		return fmt.Errorf("procid: close process handle: %w", err)
	}
	return nil
}

func (h *Handle) verify() error {
	current, err := tokenForProcess(h.process)
	if err != nil {
		return err
	}
	if current != h.token {
		return fmt.Errorf("procid: process no longer matches token")
	}
	return nil
}

func openProcess(pid int, access uint32) (windows.Handle, error) {
	return windows.OpenProcess(access, false, uint32(pid))
}

func tokenForProcess(process windows.Handle) (Token, error) {
	// An open handle keeps a terminated process's PID resolvable and its
	// creation time readable, so check liveness explicitly before minting a
	// token for it.
	var code uint32
	if err := windows.GetExitCodeProcess(process, &code); err != nil {
		return "", fmt.Errorf("procid: get process exit code: %w", err)
	}
	if code != stillActive {
		return "", errProcessExited
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(process, &created, &exited, &kernel, &user); err != nil {
		return "", fmt.Errorf("procid: get process times: %w", err)
	}
	value := uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)
	return Token("windows-v1:" + strconv.FormatUint(value, 10)), nil
}

// IsProcessGone reports whether err means the referenced process no longer
// exists.
func IsProcessGone(err error) bool {
	return errors.Is(err, windows.ERROR_INVALID_PARAMETER) ||
		errors.Is(err, errProcessExited)
}
