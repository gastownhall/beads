package proxy

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage/dbproxy/server"
)

// orphanCandidate is the process sql-server.info names. It is opened once,
// so every check and the signal go through the same handle; whether that
// handle also pins the process against PID reuse depends on the platform
// (pinned).
type orphanCandidate interface {
	executableBasename() (basename string, gone bool, err error)
	// commandLineArgs returns the process's argv exactly as it was passed,
	// one element per argument. It errors where another process's argv
	// cannot be read and split exactly (darwin, whose ps joins it with
	// spaces, and Windows).
	commandLineArgs() (args []string, gone bool, err error)
	// pinned reports whether the held handle pins the process, so a signal
	// through it cannot reach a process that reused its PID (a Linux pidfd,
	// a Windows process handle). Without one (darwin, a Linux kernel without
	// pidfd) the process can exit and its PID be reused between the checks
	// and the signal.
	pinned() bool
	kill() (gone bool, err error)
	exited() (bool, error)
	close()
}

// openOrphanCandidate opens pid for inspection. It returns a nil interface
// (never a typed nil) with gone set when the process no longer exists. A var
// so tests can substitute a scripted process.
var openOrphanCandidate = func(pid int) (orphanCandidate, bool, error) {
	proc, gone, err := openUnverifiedProcess(pid)
	if err != nil || gone || proc == nil {
		return nil, gone, err
	}
	return proc, false, nil
}

// orphanBackendConfigPaths lists the configs a bd-launched backend for
// rootDir runs with: the launch snapshot and runtime config startAttempt
// passes today, and <root>/config.yaml, the generated proxied-server config
// bd 1.3.x passed directly before the launch snapshot existed. Each path is
// given in its absolute form and, when different, its symlink-resolved form.
func orphanBackendConfigPaths(rootDir string) []string {
	roots := []string{rootDir}
	if abs, err := filepath.Abs(rootDir); err == nil {
		roots[0] = abs
		if resolved, err := filepath.EvalSymlinks(abs); err == nil && resolved != abs {
			roots = append(roots, resolved)
		}
	}
	var paths []string
	for _, root := range roots {
		for _, name := range []string{server.LaunchConfigFileName, server.RuntimeConfigFileName, "config.yaml"} {
			paths = append(paths, filepath.Join(root, name))
		}
	}
	return paths
}

// launchedWithConfig reports whether argv passes dolt's --config only with
// paths from allowed: every --config it carries, as the pair "--config",
// "<path>" or as one "--config=<path>" argument, names an allowed path
// exactly, and there is at least one. Values are compared whole, never as
// substrings, so <path>.other, a longer file name, or the needle inside
// another argument does not match; a --config with no value fails closed.
// argv[0], the program, is not an argument.
func launchedWithConfig(argv []string, allowed []string) bool {
	found := false
	for i := 1; i < len(argv); i++ {
		value, ok := strings.CutPrefix(argv[i], "--config=")
		if !ok {
			if argv[i] != "--config" {
				continue
			}
			if i+1 == len(argv) {
				return false
			}
			i++
			value = argv[i]
		}
		if !slices.Contains(allowed, value) {
			return false
		}
		found = true
	}
	return found
}

// reapUnrecordedBackend stops a dolt sql-server that a killed proxy child
// started but never recorded in proxy-child.pid (#8489): it keeps the
// database lock, so every later backend start fails. The caller holds
// proxy.lock and has just found proxy-child.lock free, so no proxy and no
// DoltServer is alive for this root, and any dolt launched with one of its
// bd-owned configs is an orphan.
//
// That holds only for BackendLocalServer, the one backend whose dolt bd
// launches. An external backend's dolt belongs to its operator and may serve
// this very directory (bd serve roots its proxy at the workspace's data
// directory), so for every other backend the file is never read.
//
// <root>/.dolt/sql-server.info is only a pointer: the named process is
// killed only when its executable is dolt, its exact argv names one of
// orphanBackendConfigPaths, and the handle that inspected it pins it
// (pinned). On any doubt no signal is sent and nil is returned, which leaves
// today's behavior; a dolt that may be the orphan is logged with the manual
// remedy.
func reapUnrecordedBackend(rootDir string, backend Backend) error {
	if backend != BackendLocalServer {
		return nil
	}
	infoPath := filepath.Join(rootDir, ".dolt", doltserver.ServerInfoFileName)
	pid, _, err := doltserver.ReadServerInfo(rootDir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			debug.Logf("dbproxy: not reaping an unrecorded backend: %v", err)
		}
		return nil
	}

	candidate, gone, err := openOrphanCandidate(pid)
	if err != nil {
		debug.Logf("dbproxy: not reaping pid %d named by %s: open: %v", pid, infoPath, err)
		return nil
	}
	if gone || candidate == nil {
		return nil
	}
	defer candidate.close()

	if !isUnrecordedBackend(candidate, pid, infoPath, rootDir) {
		return nil
	}
	if !candidate.pinned() {
		log.Printf(
			"dbproxy: dolt pid %d named by %s runs with this workspace's config but no backend record; this system cannot pin the process against PID reuse, so bd does not stop it: if no other bd or dolt serves this workspace, stop pid %d and retry",
			pid, infoPath, pid,
		)
		return nil
	}
	return stopUnrecordedBackend(candidate, pid, infoPath)
}

// isUnrecordedBackend reports whether candidate is a dolt launched with one
// of rootDir's bd-owned configs. Every "no" sends no signal: a mismatch or an
// exit mid-check is logged for debugging only, and a dolt whose argv cannot
// be read gets one log line with the manual remedy.
func isUnrecordedBackend(candidate orphanCandidate, pid int, infoPath, rootDir string) bool {
	executable, gone, err := candidate.executableBasename()
	if gone {
		return false
	}
	if err != nil {
		debug.Logf("dbproxy: not reaping pid %d named by %s: inspect executable: %v", pid, infoPath, err)
		return false
	}
	if normalizeForceStopExecutable(executable) != "dolt" {
		debug.Logf("dbproxy: not reaping pid %d named by %s: executable is %q, not dolt", pid, infoPath, executable)
		return false
	}

	argv, gone, err := candidate.commandLineArgs()
	if gone {
		return false
	}
	if err != nil {
		log.Printf(
			"dbproxy: dolt pid %d named by %s may be an unrecorded backend holding this workspace's database, but its command line cannot be read (%v); if no other bd or dolt serves this workspace, stop pid %d and retry",
			pid, infoPath, err, pid,
		)
		return false
	}
	if !launchedWithConfig(argv, orphanBackendConfigPaths(rootDir)) {
		debug.Logf("dbproxy: not reaping dolt pid %d named by %s: its --config names no config of %s", pid, infoPath, rootDir)
		return false
	}
	return true
}

// stopUnrecordedBackend kills a verified unrecorded orphan and confirms its
// exit within backendExitTimeout. A failed kill or an unconfirmed exit is an
// error naming the pid and the info file, so no child is spawned past it.
func stopUnrecordedBackend(candidate orphanCandidate, pid int, infoPath string) error {
	gone, err := candidate.kill()
	if err != nil {
		return fmt.Errorf("kill unrecorded orphan backend dolt pid %d named by %s: %w", pid, infoPath, err)
	}
	deadline := time.Now().Add(backendExitTimeout)
	for !gone {
		exited, err := candidate.exited()
		if err != nil {
			return fmt.Errorf("confirm exit of unrecorded orphan backend dolt pid %d named by %s: %w", pid, infoPath, err)
		}
		if exited {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout (%s) waiting for unrecorded orphan backend dolt pid %d named by %s to exit", backendExitTimeout, pid, infoPath)
		}
		time.Sleep(shutdownConfirmPoll)
	}
	log.Printf("dbproxy: stopped unrecorded orphan dolt pid %d named by %s", pid, infoPath)
	return nil
}
