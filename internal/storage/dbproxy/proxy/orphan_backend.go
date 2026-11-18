package proxy

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"path/filepath"
	"time"

	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage/dbproxy/server"
)

// orphanCandidate is the process sql-server.info names, held through one
// stable handle (unverifiedProcess) so every check and the signal act on the
// same process.
type orphanCandidate interface {
	executableBasename() (basename string, gone bool, err error)
	commandLineContains(needle string) (matched bool, gone bool, err error)
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

// reapUnrecordedBackend stops a dolt sql-server that a killed proxy child
// started but never recorded in proxy-child.pid (#8489): it keeps the
// database lock, so every later backend start fails. The caller holds
// proxy.lock and has just found proxy-child.lock free, so no proxy and no
// DoltServer is alive for this root, and any dolt launched with one of its
// bd-owned configs is an orphan.
//
// <root>/.dolt/sql-server.info is only a pointer: the named process is
// killed only when, through one held handle, its executable is dolt and its
// command line names one of orphanBackendConfigPaths. On any doubt no signal
// is sent and nil is returned, which leaves today's behavior.
func reapUnrecordedBackend(rootDir string) error {
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

	executable, gone, err := candidate.executableBasename()
	if gone {
		return nil
	}
	if err != nil {
		debug.Logf("dbproxy: not reaping pid %d named by %s: inspect executable: %v", pid, infoPath, err)
		return nil
	}
	if normalizeForceStopExecutable(executable) != "dolt" {
		debug.Logf("dbproxy: not reaping pid %d named by %s: executable is %q, not dolt", pid, infoPath, executable)
		return nil
	}

	matched := false
	for _, configPath := range orphanBackendConfigPaths(rootDir) {
		ok, gone, err := candidate.commandLineContains("--config " + configPath)
		if gone {
			return nil
		}
		if err != nil {
			log.Printf(
				"dbproxy: dolt pid %d named by %s may be an unrecorded backend holding this workspace's database, but its command line cannot be read (%v); if no other bd or dolt serves this workspace, stop pid %d and retry",
				pid, infoPath, err, pid,
			)
			return nil
		}
		if ok {
			matched = true
			break
		}
	}
	if !matched {
		debug.Logf("dbproxy: not reaping dolt pid %d named by %s: its command line names no config of %s", pid, infoPath, rootDir)
		return nil
	}

	gone, err = candidate.kill()
	if err != nil {
		return fmt.Errorf("kill unrecorded orphan backend dolt pid %d named by %s: %w", pid, infoPath, err)
	}
	if !gone {
		deadline := time.Now().Add(backendExitTimeout)
		for {
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
	}
	log.Printf("dbproxy: stopped unrecorded orphan dolt pid %d named by %s", pid, infoPath)
	return nil
}
