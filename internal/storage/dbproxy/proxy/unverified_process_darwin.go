//go:build darwin

package proxy

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// unverifiedProcess identifies a PID whose birth token cannot be verified.
// Darwin has no pidfd-style primitive, so inspection and signaling keep the
// historical small PID-reuse race; the workspace-scope check below still
// narrows what force-stop is willing to signal.
type unverifiedProcess struct {
	pid int
}

// openUnverifiedProcess probes pid. gone reports a PID that no longer exists.
func openUnverifiedProcess(pid int) (proc *unverifiedProcess, gone bool, err error) {
	if killErr := syscall.Kill(pid, 0); errors.Is(killErr, unix.ESRCH) {
		return nil, true, nil
	}
	return &unverifiedProcess{pid: pid}, false, nil
}

func (p *unverifiedProcess) executableBasename() (basename string, gone bool, err error) {
	return processExecutableBasename(p.pid)
}

// commandLineContains reports whether the process command line contains
// needle, read best-effort through ps.
func (p *unverifiedProcess) commandLineContains(needle string) (matched bool, gone bool, err error) {
	output, commandErr := exec.Command("ps", "-p", strconv.Itoa(p.pid), "-o", "args=").Output()
	if commandErr != nil {
		if killErr := syscall.Kill(p.pid, 0); errors.Is(killErr, unix.ESRCH) {
			return false, true, nil
		}
		return false, false, fmt.Errorf("read command line for pid %d: %w", p.pid, commandErr)
	}
	return strings.Contains(strings.TrimSpace(string(output)), needle), false, nil
}

// kill sends SIGKILL. gone reports a target that had already exited.
func (p *unverifiedProcess) kill() (gone bool, err error) {
	if killErr := syscall.Kill(p.pid, syscall.SIGKILL); killErr != nil {
		if errors.Is(killErr, unix.ESRCH) {
			return true, nil
		}
		return false, fmt.Errorf("signal pid %d: %w", p.pid, killErr)
	}
	return false, nil
}

func (p *unverifiedProcess) exited() (bool, error) {
	_, gone, err := processExecutableBasename(p.pid)
	if err != nil {
		return false, err
	}
	return gone, nil
}

func (p *unverifiedProcess) close() {}
