//go:build windows

package proxy

import (
	"errors"
	"fmt"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"
)

// windowsStillActive is the GetExitCodeProcess sentinel for a running
// process (STILL_ACTIVE, 259).
const windowsStillActive = 259

// unverifiedProcess holds one open process handle so force-stop inspection
// and signaling act on the same process; an open handle prevents Windows
// from recycling the PID underneath us.
type unverifiedProcess struct {
	pid    int
	handle windows.Handle
}

// openUnverifiedProcess opens a stable handle for pid. gone reports a PID
// that no longer exists or has already terminated.
func openUnverifiedProcess(pid int) (proc *unverifiedProcess, gone bool, err error) {
	handle, err := windows.OpenProcess(
		windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE,
		false,
		uint32(pid),
	)
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("open process %d: %w", pid, err)
	}
	exited, err := handleExited(handle)
	if err != nil {
		_ = windows.CloseHandle(handle)
		return nil, false, err
	}
	if exited {
		_ = windows.CloseHandle(handle)
		return nil, true, nil
	}
	return &unverifiedProcess{pid: pid, handle: handle}, false, nil
}

func (p *unverifiedProcess) executableBasename() (basename string, gone bool, err error) {
	buffer := make([]uint16, 32768)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(p.handle, 0, &buffer[0], &size); err != nil {
		return "", false, fmt.Errorf("query image name for pid %d: %w", p.pid, err)
	}
	return filepath.Base(syscall.UTF16ToString(buffer[:size])), false, nil
}

// commandLineContains cannot establish workspace scope on Windows: another
// process's command line is only reachable through undocumented PEB reads,
// so force-stop refuses rather than signaling on basename alone.
func (p *unverifiedProcess) commandLineContains(string) (matched bool, gone bool, err error) {
	exited, exitErr := handleExited(p.handle)
	if exitErr == nil && exited {
		return false, true, nil
	}
	return false, false, errors.New("reading another process's command line is not supported on windows")
}

// kill terminates the process through the held handle. gone reports a target
// that had already exited.
func (p *unverifiedProcess) kill() (gone bool, err error) {
	if err := windows.TerminateProcess(p.handle, 1); err != nil {
		if exited, exitErr := handleExited(p.handle); exitErr == nil && exited {
			return true, nil
		}
		return false, fmt.Errorf("terminate pid %d: %w", p.pid, err)
	}
	return false, nil
}

func (p *unverifiedProcess) exited() (bool, error) {
	return handleExited(p.handle)
}

func (p *unverifiedProcess) close() {
	if p.handle != 0 {
		_ = windows.CloseHandle(p.handle)
		p.handle = 0
	}
}

func handleExited(handle windows.Handle) (bool, error) {
	var code uint32
	if err := windows.GetExitCodeProcess(handle, &code); err != nil {
		return false, fmt.Errorf("get process exit code: %w", err)
	}
	return code != windowsStillActive, nil
}
