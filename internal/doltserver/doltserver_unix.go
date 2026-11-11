//go:build !windows

package doltserver

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// portConflictHint is the platform-specific command to diagnose port conflicts.
// Used in error messages when a port is busy but the occupying process can't be identified.
const portConflictHint = "lsof -i :%d"

// processListHint is the platform-specific command to list dolt processes.
// Used in error messages when too many dolt servers are running.
const processListHint = "pgrep -fl 'dolt sql-server'"

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

// readDoltProcessPIDs returns PIDs of all running dolt sql-server processes.
// Excludes zombies and defunct processes. An error means the process list
// could not be read (ps can be refused, e.g. inside a macOS sandbox), which
// is not the same as there being no dolt processes.
func readDoltProcessPIDs() ([]int, error) {
	out, err := exec.Command("ps", "-axo", "pid=,state=,command=").Output()
	if err != nil {
		return nil, fmt.Errorf("listing processes: %w", err)
	}
	return parseDoltProcessPIDs(out), nil
}

// parseDoltProcessPIDs returns matching, non-zombie Dolt server PIDs from a
// `ps -axo pid=,state=,command=` snapshot. It preserves the source row order.
// The state column is optional: macOS 26 leaves it empty for many rows
// (#7121). Every word after the PID goes to isDoltSQLServerCommand, which
// skips leading words, so a state, present or not, is just a leading word.
func parseDoltProcessPIDs(snapshot []byte) []int {
	var pids []int
	for _, line := range strings.Split(string(snapshot), "\n") {
		words := strings.Fields(line)
		if len(words) < 2 {
			continue
		}

		pid, err := strconv.Atoi(words[0])
		if err != nil || pid <= 0 {
			continue
		}
		// Only a zombie is skipped: ps prints Z first for one on every
		// platform, whatever modifiers follow. X is not skipped: where macOS
		// 26 leaves the state code blank, a leading X is the traced modifier
		// of a live process, and Linux's dead state X leaves no command line
		// to match.
		if words[1][0] == 'Z' {
			continue
		}

		if isDoltSQLServerCommand(words[1:]) {
			pids = append(pids, pid)
		}
	}
	return pids
}

// isDoltSQLServerCommand reports whether the command words name the dolt
// executable — the word "dolt" or a path ending in "/dolt" — followed by a
// whole "sql-server" argument, so global flags between the two are fine. It
// is deliberately not an exact argv match: leading words are allowed, which
// keeps the shell stand-ins tests use (`sh …/dolt sql-server`) and
// executable paths containing spaces recognizable, at the cost of still
// matching a command that merely mentions both words in order (`some-tool
// says dolt then sql-server`). A renamed executable (dolt-2.6) no longer
// qualifies, and neither does a server started through a `dolt` wrapper on
// PATH that execs a binary with another name (dolt.real): ps shows the
// command the wrapper exec'd, so the wrapper must exec a binary named dolt or
// keep argv[0] (bash's `exec -a "$0"`). Windows likewise requires the dolt.exe
// image name. Every later dolt word's suffix is contained in the first one's,
// so the first dolt word decides.
func isDoltSQLServerCommand(words []string) bool {
	for i, word := range words {
		if word == "dolt" || strings.HasSuffix(word, "/dolt") {
			return slices.Contains(words[i+1:], "sql-server")
		}
	}
	return false
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

// signalZero sends signal 0 to pid, which checks the process without
// signaling it. It is a variable so tests can simulate a process this user may
// not signal.
var signalZero = func(pid int) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Signal(syscall.Signal(0))
}

// isProcessAlive checks if a process with the given PID is running.
// Uses signal 0 which doesn't send a signal but checks process existence.
// A process this user may not signal still exists, so it counts as alive.
func isProcessAlive(pid int) bool {
	return aliveAfterSignalZero(signalZero(pid))
}

// isSignalableProcess reports whether pid is a live process this user may
// signal. Unlike isProcessAlive it is false for a process that belongs to
// another account (EPERM).
func isSignalableProcess(pid int) bool {
	return signalZero(pid) == nil
}

// aliveAfterSignalZero classifies the error from signaling a process with
// signal 0. EPERM (bare, or wrapped by kill and pidfd_send_signal) means the
// process exists but belongs to someone else; only "no such process"
// (ESRCH, os.ErrProcessDone) means dead. Same rule as processExists.
func aliveAfterSignalZero(err error) bool {
	return err == nil || errors.Is(err, syscall.EPERM)
}

// serverInfoProcessExists reports whether pid, from a sql-server.info file,
// names a process, by the dolt CLI's own test (sqlserver.ProcessExists): a
// signal 0 that succeeds or fails with EPERM (a process of another user, such
// as a dolt sql-server a service account runs). Unlike isProcessAlive it
// takes EPERM as alive, as the dolt CLI does when it decides whether to hand
// a command to the server a file names; ServesDatabaseDirOnPort also
// requires the database's lock to be held, as the dolt CLI does.
func serverInfoProcessExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = process.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
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

// killProcessGroup SIGKILLs the process group pgid leads. Start launches
// each dolt sql-server as a group leader (Setpgid), so this reaches a dolt
// that a non-exec wrapper started, too.
func killProcessGroup(pgid int) {
	if pgid > 0 {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	}
}
