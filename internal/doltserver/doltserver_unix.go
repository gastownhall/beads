//go:build !windows

package doltserver

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
)

// portConflictHint is the platform-specific command to diagnose port conflicts.
// Used in error messages when a port is busy but the occupying process can't be identified.
const portConflictHint = "lsof -i :%d"

// processListHint is the platform-specific command to list dolt processes.
// Used in error messages when too many dolt servers are running.
const processListHint = "pgrep -la 'dolt sql-server'"

// procAttrDetached is defined per-platform in procattr_linux.go (Linux) and
// procattr_other_unix.go (darwin/BSD): Pdeathsig, used to test-gate
// parent-death cleanup, exists only on Linux (see those files for details).

// findPIDOnPort returns the PID of the process listening on a TCP port.
// Uses lsof to look up the listener. Returns 0 if no process found or on error.
func findPIDOnPort(port int) int {
	out, err := exec.Command("lsof", "-ti", fmt.Sprintf(":%d", port), "-sTCP:LISTEN").Output() //nolint:gosec // G702: port is internal int, not user input
	if err != nil {
		return 0
	}
	// lsof may return multiple PIDs; take the first one
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if pid, err := strconv.Atoi(strings.TrimSpace(line)); err == nil && pid > 0 {
			return pid
		}
	}
	return 0
}

// listDoltProcessPIDs returns PIDs of all running dolt sql-server processes.
// Excludes zombies and defunct processes. Callers derive count (len) and
// membership (linear scan) from the returned slice.
func listDoltProcessPIDs() []int {
	out, err := exec.Command("ps", "-axo", "pid=,state=,command=").Output()
	if err != nil {
		return nil
	}
	return parseDoltProcessPIDs(out)
}

// parseDoltProcessPIDs returns matching, non-defunct Dolt server PIDs from a
// `ps -axo pid=,state=,command=` snapshot. It preserves the source row order.
func parseDoltProcessPIDs(snapshot []byte) []int {
	var pids []int
	for _, line := range strings.Split(string(snapshot), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		pidText, rest, ok := splitPSField(line)
		if !ok {
			continue
		}
		state, command, ok := splitPSField(rest)
		if !ok {
			continue
		}

		pid, err := strconv.Atoi(pidText)
		if err != nil || pid <= 0 {
			continue
		}
		if state[0] == 'Z' || state[0] == 'X' {
			continue
		}

		doltIndex := strings.Index(command, "dolt")
		if doltIndex >= 0 && strings.Contains(command[doltIndex+len("dolt"):], "sql-server") {
			pids = append(pids, pid)
		}
	}
	return pids
}

// splitPSField separates the next whitespace-delimited ps field from the
// remaining text, retaining whitespace within the final command column.
func splitPSField(line string) (field, rest string, ok bool) {
	fieldEnd := strings.IndexFunc(line, unicode.IsSpace)
	if fieldEnd == -1 {
		return "", "", false
	}
	field = line[:fieldEnd]
	rest = strings.TrimLeftFunc(line[fieldEnd:], unicode.IsSpace)
	return field, rest, rest != ""
}

// isProcessInDir checks if a process's working directory matches the given path.
// Uses lsof to look up the CWD, which is more reliable than checking command-line
// args since dolt sql-server is started with cmd.Dir (not a --data-dir flag).
func isProcessInDir(pid int, dir string) bool {
	// On macOS, lsof requires -a to AND selectors together; without it,
	// "-p <pid>" and "-d cwd" can yield cwd entries from unrelated processes.
	out, err := exec.Command("lsof", "-a", "-p", strconv.Itoa(pid), "-d", "cwd", "-Fn").Output()
	if err != nil {
		return false
	}
	absDir, _ := filepath.Abs(dir)
	// lsof -Fn output format: "p<pid>\nfcwd\nn<path>"
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "n") {
			cwd := strings.TrimSpace(line[1:])
			absCwd, _ := filepath.Abs(cwd)
			if absCwd == absDir {
				return true
			}
		}
	}
	return false
}

// isProcessAlive checks if a process with the given PID is running.
// Uses signal 0 which doesn't send a signal but checks process existence.
func isProcessAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}

// gracefulStop sends SIGTERM, waits for the process to exit, then SIGKILL if needed.
// Used by reclaimPort and StopWithForce where data has already been flushed.
func gracefulStop(pid int, timeout time.Duration) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("finding process %d: %w", pid, err)
	}

	if err := process.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("sending SIGTERM to PID %d: %w", pid, err)
	}

	// Poll for exit
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		if process.Signal(syscall.Signal(0)) != nil {
			return nil // exited
		}
	}

	// Still running — force kill
	_ = process.Signal(syscall.SIGKILL)
	time.Sleep(100 * time.Millisecond)
	return nil
}
