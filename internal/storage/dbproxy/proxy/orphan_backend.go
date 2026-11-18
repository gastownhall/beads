package proxy

import (
	"fmt"
	"log"
	"path/filepath"
	"time"

	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage/dbproxy/server"
)

// legacyDefaultConfigName is the proxied-server config bd 1.3.x generated
// (<root>/config.yaml, cmd/bd's proxiedServerConfigName) and launched dolt
// with: before GH#7184, startAttempt passed the operator config itself as
// --config. An orphan from that version matches this needle, so an upgraded
// bd reaps storms older versions left behind (#8489). A custom 1.3.x config
// path is not bd-owned and deliberately matches no needle.
const legacyDefaultConfigName = "config.yaml"

// orphanCandidate is the process-inspection surface reapUnrecordedBackend
// needs: one held reference to a pid the pid records do not name, through
// which every check and the signal act on the same process. It is exactly
// unverifiedProcess's method set, so production wraps openUnverifiedProcess;
// tests substitute a fake through openOrphanCandidate.
type orphanCandidate interface {
	executableBasename() (basename string, gone bool, err error)
	commandLineContains(needle string) (matched bool, gone bool, err error)
	kill() (gone bool, err error)
	exited() (bool, error)
	close()
}

// openOrphanCandidate opens the candidate handle for a pid named by a dolt
// server info file. gone reports a process that no longer exists; the
// returned candidate is nil then, and on error.
var openOrphanCandidate = func(pid int) (cand orphanCandidate, gone bool, err error) {
	proc, gone, err := openUnverifiedProcess(pid)
	if err != nil || gone || proc == nil {
		return nil, gone, err
	}
	return proc, false, nil
}

// orphanConfigNeedles lists the "--config <path>" command-line fragments that
// tie a dolt sql-server to this root: the launch snapshot and runtime config
// startAttempt passes today, and the generated default config bd 1.3.x
// launched with (see legacyDefaultConfigName). Both the absolute root and its
// symlink-resolved form appear, since the command line may carry either.
func orphanConfigNeedles(rootDir string) ([]string, error) {
	abs, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, err
	}
	roots := []string{abs}
	if resolved, linkErr := filepath.EvalSymlinks(abs); linkErr == nil && resolved != abs {
		roots = append(roots, resolved)
	}
	names := []string{server.LaunchConfigFileName, server.RuntimeConfigFileName, legacyDefaultConfigName}
	needles := make([]string, 0, len(roots)*len(names))
	for _, root := range roots {
		for _, name := range names {
			needles = append(needles, "--config "+filepath.Join(root, name))
		}
	}
	return needles, nil
}

// reapUnrecordedBackend stops a dolt sql-server the pid records no longer
// name but dolt's own server info file still does (#8489). A kill landing in
// DoltServer.startAttempt's cmd.Start → pidfile.Write window leaves exactly
// that: a live dolt holding the database lock with no proxy-child.pid, which
// otherwise turns every later spawn into a lock failure.
//
// It runs only from cleanupOrphanBackend, whose caller holds proxy.lock,
// after proxy-child.lock was probed free: under those two locks any dolt
// launched with this root's config is an orphan by construction. The info
// file is a pointer only — the pid it names is signaled solely after one
// held handle proves the process is a dolt whose command line names one of
// this root's bd-owned configs. On any doubt no signal is sent, which is
// today's behavior; where the command line cannot be read (always on
// Windows) the log names the manual remedy. Read-only towards dolt state:
// beads never writes, removes or locks the info file.
func reapUnrecordedBackend(rootDir string) error {
	infoPath := filepath.Join(rootDir, ".dolt", doltserver.ServerInfoFileName)
	pid, _, err := doltserver.ReadServerInfo(rootDir)
	if err != nil {
		debug.Logf("dbproxy: no reappable backend named by %s: %v", infoPath, err)
		return nil
	}
	if pid <= 0 {
		return nil
	}
	cand, gone, err := openOrphanCandidate(pid)
	if err != nil {
		debug.Logf("dbproxy: could not open pid %d named by %s: %v", pid, infoPath, err)
		return nil
	}
	if gone || cand == nil {
		return nil
	}
	defer cand.close()

	basename, gone, err := cand.executableBasename()
	if err != nil || gone {
		debug.Logf("dbproxy: pid %d named by %s cannot be inspected (%v, gone=%t); leaving it alone", pid, infoPath, err, gone)
		return nil
	}
	if normalizeForceStopExecutable(basename) != "dolt" {
		debug.Logf("dbproxy: pid %d named by %s runs %q, not dolt; leaving it alone", pid, infoPath, basename)
		return nil
	}

	needles, err := orphanConfigNeedles(rootDir)
	if err != nil {
		debug.Logf("dbproxy: resolve candidate configs for %s: %v", infoPath, err)
		return nil
	}
	scoped := false
	for _, needle := range needles {
		matched, gone, err := cand.commandLineContains(needle)
		if err != nil {
			// A dolt whose command line cannot be read might be any root's
			// server; never signal on a guess.
			log.Printf("dbproxy: pid %d named by %s looks like dolt but its command line cannot be read (%v); no signal was sent: if no other bd or dolt serves this workspace, stop pid %d and retry", pid, infoPath, err, pid)
			return nil
		}
		if gone {
			debug.Logf("dbproxy: pid %d named by %s exited during inspection; no signal was sent", pid, infoPath)
			return nil
		}
		if matched {
			scoped = true
			break
		}
	}
	if !scoped {
		debug.Logf("dbproxy: dolt pid %d named by %s serves another root; leaving it alone", pid, infoPath)
		return nil
	}

	gone, err = cand.kill()
	if err != nil {
		return fmt.Errorf("stop unrecorded orphan dolt pid %d named by %s: %w", pid, infoPath, err)
	}
	if !gone {
		deadline := time.Now().Add(backendExitTimeout)
		for {
			exited, err := cand.exited()
			if err != nil {
				return fmt.Errorf("confirm exit of unrecorded orphan dolt pid %d named by %s: %w", pid, infoPath, err)
			}
			if exited {
				break
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("timeout (%s) waiting for unrecorded orphan dolt pid %d named by %s to exit", backendExitTimeout, pid, infoPath)
			}
			time.Sleep(shutdownConfirmPoll)
		}
	}
	log.Printf("dbproxy: stopped unrecorded orphan dolt pid %d named by %s", pid, infoPath)
	return nil
}
