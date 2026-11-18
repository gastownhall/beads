//go:build !windows

package proxy

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/procid"
	"github.com/steveyegge/beads/internal/storage/dbproxy/pidfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/server"
	"github.com/steveyegge/beads/internal/storage/dbproxy/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeOrphanCandidate replaces unverifiedProcess behind openOrphanCandidate:
// kill marks it exited unless the test asks for an error or an
// already-gone target, and every probe is recorded so tests can pin what
// reapUnrecordedBackend asked before signaling.
type fakeOrphanCandidate struct {
	basename   string
	cmdline    string
	cmdlineErr error
	killErr    error
	killGone   bool
	exitedErr  error
	neverExits bool

	killed        bool
	exitConfirmed bool
	closed        bool
	needles       []string
}

func (f *fakeOrphanCandidate) executableBasename() (string, bool, error) {
	return f.basename, false, nil
}

func (f *fakeOrphanCandidate) commandLineContains(needle string) (bool, bool, error) {
	f.needles = append(f.needles, needle)
	if f.cmdlineErr != nil {
		return false, false, f.cmdlineErr
	}
	return strings.Contains(f.cmdline, needle), false, nil
}

func (f *fakeOrphanCandidate) kill() (bool, error) {
	f.killed = true
	if f.killErr != nil {
		return false, f.killErr
	}
	if f.killGone {
		return true, nil
	}
	f.exitConfirmed = true
	return false, nil
}

func (f *fakeOrphanCandidate) exited() (bool, error) {
	if f.exitedErr != nil {
		return false, f.exitedErr
	}
	if f.neverExits {
		return false, nil
	}
	return f.exitConfirmed, nil
}

func (f *fakeOrphanCandidate) close() { f.closed = true }

// installOrphanCandidate swaps the candidate-opening seam for one test.
func installOrphanCandidate(t *testing.T, open func(pid int) (orphanCandidate, bool, error)) {
	t.Helper()
	previous := openOrphanCandidate
	openOrphanCandidate = open
	t.Cleanup(func() { openOrphanCandidate = previous })
}

// writeServerInfo writes a dolt-style server info file naming pid under
// root/.dolt, as a dolt sql-server serving root would, and returns its path.
func writeServerInfo(t *testing.T, root string, pid int) string {
	t.Helper()
	infoDir := filepath.Join(root, ".dolt")
	require.NoError(t, os.MkdirAll(infoDir, 0o755))
	path := filepath.Join(infoDir, doltserver.ServerInfoFileName)
	require.NoError(t, os.WriteFile(path, []byte(fmt.Sprintf("%d:3306:secret", pid)), 0o600))
	return path
}

// captureLogs redirects the standard logger for one test.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &buf
}

// TestReapUnrecordedBackendStopsVerifiedOrphan pins #8489 AC-4: with no live
// backend record and a free proxy-child.lock, a live process named by
// <root>/.dolt/sql-server.info whose basename is dolt and whose command line
// names this root's launch snapshot, runtime config, or the config.yaml bd
// 1.3.x launched with, is killed, its exit confirmed, and the stop logged
// with the pid and the info file.
func TestReapUnrecordedBackendStopsVerifiedOrphan(t *testing.T) {
	for _, tc := range []struct {
		name     string
		basename string
		config   string
	}{
		{"launch snapshot", "dolt", server.LaunchConfigFileName},
		{"runtime config", "dolt", server.RuntimeConfigFileName},
		{"1.3.x default config", "dolt", "config.yaml"},
		{"exe suffix", "DOLT.EXE", server.LaunchConfigFileName},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			infoPath := writeServerInfo(t, root, 4242)
			fake := &fakeOrphanCandidate{
				basename: tc.basename,
				cmdline:  "dolt sql-server --config " + filepath.Join(root, tc.config),
			}
			var openedPID int
			installOrphanCandidate(t, func(pid int) (orphanCandidate, bool, error) {
				openedPID = pid
				return fake, false, nil
			})
			logs := captureLogs(t)

			require.NoError(t, reapUnrecordedBackend(root))
			assert.Equal(t, 4242, openedPID, "the pid named by the info file must be the one inspected")
			assert.True(t, fake.killed, "a fully verified orphan must be killed")
			assert.True(t, fake.closed)
			assert.Contains(t, logs.String(),
				fmt.Sprintf("dbproxy: stopped unrecorded orphan dolt pid 4242 named by %s", infoPath))
			assert.Contains(t, fake.needles,
				"--config "+filepath.Join(root, server.LaunchConfigFileName),
				"the current launch snapshot must be one of the accepted needles")
		})
	}
}

// TestCleanupOrphanBackendReapsUnrecordedOrphan pins the pf == nil wiring of
// #8489 AC-4: with no proxy-child.pid at all, cleanupOrphanBackend still
// reaps the backend the info file names.
func TestCleanupOrphanBackendReapsUnrecordedOrphan(t *testing.T) {
	root := t.TempDir()
	writeServerInfo(t, root, 4242)
	fake := &fakeOrphanCandidate{
		basename: "dolt",
		cmdline:  "dolt sql-server --config " + filepath.Join(root, server.LaunchConfigFileName),
	}
	installOrphanCandidate(t, func(int) (orphanCandidate, bool, error) { return fake, false, nil })

	proxyLock, err := util.TryLock(filepath.Join(root, LockFileName))
	require.NoError(t, err)
	defer proxyLock.Unlock()

	require.NoError(t, cleanupOrphanBackend(root))
	assert.True(t, fake.killed, "cleanupOrphanBackend must reap the unrecorded orphan behind a missing record")
}

// TestCleanupOrphanBackendReapsAfterDeadRecordQuarantine pins the second
// wiring site of #8489 AC-4: a storm iteration killed after its own dolt
// recorded leaves a dead record in front of the original unrecorded orphan,
// so the dead-record quarantine must fall through to the reap, not return.
func TestCleanupOrphanBackendReapsAfterDeadRecordQuarantine(t *testing.T) {
	root := t.TempDir()
	helper := startHelperProcess(t)
	require.NoError(t, pidfile.Write(root, server.PIDFileName, v2Record(t, root, pidfile.KindDoltBackend, helper)))
	handle, err := procid.Open(helper.cmd.Process.Pid, helper.token)
	require.NoError(t, err)
	require.NoError(t, handle.Kill())
	require.NoError(t, handle.Close())
	assertHelperExited(t, helper)

	writeServerInfo(t, root, 4242)
	fake := &fakeOrphanCandidate{
		basename: "dolt",
		cmdline:  "dolt sql-server --config " + filepath.Join(root, "config.yaml"),
	}
	installOrphanCandidate(t, func(int) (orphanCandidate, bool, error) { return fake, false, nil })

	proxyLock, err := util.TryLock(filepath.Join(root, LockFileName))
	require.NoError(t, err)
	defer proxyLock.Unlock()

	require.NoError(t, cleanupOrphanBackend(root))
	assert.True(t, fake.killed, "quarantining the dead record must not skip the unrecorded orphan behind it")
	assert.NoFileExists(t, pidfile.Path(root, server.PIDFileName))
	assert.Len(t, quarantines(t, root, server.PIDFileName), 1)
}

// TestReapUnrecordedBackendSendsNoSignalWithoutFullMatch pins #8489 AC-5:
// a missing, malformed or dead-pid info file, a live non-dolt process, or a
// dolt whose command line names another root's config (including another
// root's config.yaml) is never signaled, and the reap returns nil.
func TestReapUnrecordedBackendSendsNoSignalWithoutFullMatch(t *testing.T) {
	t.Run("missing info file", func(t *testing.T) {
		root := t.TempDir()
		opened := false
		installOrphanCandidate(t, func(int) (orphanCandidate, bool, error) {
			opened = true
			return nil, false, nil
		})
		require.NoError(t, reapUnrecordedBackend(root))
		assert.False(t, opened, "a missing info file must not open any candidate")
	})

	t.Run("malformed info file", func(t *testing.T) {
		root := t.TempDir()
		infoDir := filepath.Join(root, ".dolt")
		require.NoError(t, os.MkdirAll(infoDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(infoDir, doltserver.ServerInfoFileName), []byte("not-a-server"), 0o600))
		opened := false
		installOrphanCandidate(t, func(int) (orphanCandidate, bool, error) {
			opened = true
			return nil, false, nil
		})
		require.NoError(t, reapUnrecordedBackend(root))
		assert.False(t, opened, "a malformed info file must not open any candidate")
	})

	t.Run("dead pid", func(t *testing.T) {
		root := t.TempDir()
		writeServerInfo(t, root, 4242)
		fake := &fakeOrphanCandidate{basename: "dolt"}
		installOrphanCandidate(t, func(int) (orphanCandidate, bool, error) {
			return fake, true, nil
		})
		require.NoError(t, reapUnrecordedBackend(root))
		assert.False(t, fake.killed, "a pid that no longer exists must not be signaled")
	})

	t.Run("live non-dolt process through the real proc path", func(t *testing.T) {
		root := t.TempDir()
		helper := startHelperProcess(t)
		writeServerInfo(t, root, helper.cmd.Process.Pid)
		require.NoError(t, reapUnrecordedBackend(root))
		assertHelperAlive(t, helper)
	})

	t.Run("dolt serving another root's config", func(t *testing.T) {
		root := t.TempDir()
		other := t.TempDir()
		writeServerInfo(t, root, 4242)
		for _, otherConfig := range []string{
			filepath.Join(other, server.LaunchConfigFileName),
			filepath.Join(other, server.RuntimeConfigFileName),
			filepath.Join(other, "config.yaml"),
		} {
			fake := &fakeOrphanCandidate{
				basename: "dolt",
				cmdline:  "dolt sql-server --config " + otherConfig,
			}
			installOrphanCandidate(t, func(int) (orphanCandidate, bool, error) { return fake, false, nil })
			require.NoError(t, reapUnrecordedBackend(root))
			assert.False(t, fake.killed, "a dolt serving another root (%s) must never be signaled", otherConfig)
		}
	})
}

// TestReapUnrecordedBackendLogsRemedyWhenCommandLineUnreadable pins #8489
// AC-6: a live dolt whose command line cannot be read (every Windows case,
// any read failure elsewhere) gets no signal, and exactly one log line names
// the pid, the info file and the manual remedy.
func TestReapUnrecordedBackendLogsRemedyWhenCommandLineUnreadable(t *testing.T) {
	root := t.TempDir()
	infoPath := writeServerInfo(t, root, 4242)
	fake := &fakeOrphanCandidate{
		basename:   "dolt",
		cmdlineErr: errors.New("reading another process's command line is not supported on windows"),
	}
	installOrphanCandidate(t, func(int) (orphanCandidate, bool, error) { return fake, false, nil })
	logs := captureLogs(t)

	require.NoError(t, reapUnrecordedBackend(root))
	assert.False(t, fake.killed, "an unreadable command line must never earn a signal")
	assert.Contains(t, logs.String(), "pid 4242")
	assert.Contains(t, logs.String(), infoPath)
	assert.Contains(t, logs.String(), "stop pid 4242 and retry")
	assert.Equal(t, 1, strings.Count(logs.String(), "command line cannot be read"),
		"exactly one remedy line, not a per-needle spill")
}

// TestReapUnrecordedBackendReportsKillAndExitFailures pins #8489 AC-7: a
// fully matched orphan whose kill fails, or which has not exited
// backendExitTimeout after the kill, produces an error naming the pid and
// the info file.
func TestReapUnrecordedBackendReportsKillAndExitFailures(t *testing.T) {
	t.Run("kill fails", func(t *testing.T) {
		root := t.TempDir()
		infoPath := writeServerInfo(t, root, 4242)
		fake := &fakeOrphanCandidate{
			basename: "dolt",
			cmdline:  "dolt sql-server --config " + filepath.Join(root, server.LaunchConfigFileName),
			killErr:  errors.New("permission denied"),
		}
		installOrphanCandidate(t, func(int) (orphanCandidate, bool, error) { return fake, false, nil })

		err := reapUnrecordedBackend(root)
		require.Error(t, err)
		assert.ErrorContains(t, err, "pid 4242")
		assert.ErrorContains(t, err, infoPath)
		assert.ErrorContains(t, err, "permission denied")
	})

	t.Run("exit not confirmed within backendExitTimeout", func(t *testing.T) {
		root := t.TempDir()
		infoPath := writeServerInfo(t, root, 4242)
		fake := &fakeOrphanCandidate{
			basename: "dolt",
			cmdline:  "dolt sql-server --config " + filepath.Join(root, server.LaunchConfigFileName),
			neverExits: true,
		}
		installOrphanCandidate(t, func(int) (orphanCandidate, bool, error) { return fake, false, nil })
		start := time.Now()

		err := reapUnrecordedBackend(root)
		require.Error(t, err)
		assert.ErrorContains(t, err, "pid 4242")
		assert.ErrorContains(t, err, infoPath)
		assert.ErrorContains(t, err, fmt.Sprintf("timeout (%s)", backendExitTimeout))
		assert.Less(t, time.Since(start), 2*backendExitTimeout, "the exit wait must be bounded by backendExitTimeout")
	})
}

// TestSpawnAndHandoffStartsNoChildWhenReapFails pins #8489 AC-7: when the
// reap of a fully matched orphan fails, spawnAndHandoff propagates the error
// and never forks a proxy child on top of a live unrecorded backend.
func TestSpawnAndHandoffStartsNoChildWhenReapFails(t *testing.T) {
	root := t.TempDir()
	infoPath := writeServerInfo(t, root, 4242)
	fake := &fakeOrphanCandidate{
		basename: "dolt",
		cmdline:  "dolt sql-server --config " + filepath.Join(root, server.LaunchConfigFileName),
		killErr:  errors.New("permission denied"),
	}
	installOrphanCandidate(t, func(int) (orphanCandidate, bool, error) { return fake, false, nil })
	previousResolve := ResolveExecutable
	ResolveExecutable = func() (string, error) { return "", errors.New("spawn reached") }
	t.Cleanup(func() { ResolveExecutable = previousResolve })

	proxyLock, err := util.TryLock(filepath.Join(root, LockFileName))
	require.NoError(t, err)
	// spawnAndHandoff takes ownership of the lock and releases it when the
	// handoff does not happen.

	_, err = spawnAndHandoff(
		root,
		externalOpenOpts(root),
		time.Now().Add(time.Second),
		"",
		proxyLock,
		adoptionResult{status: adoptionNoRecord},
	)
	require.Error(t, err)
	assert.ErrorContains(t, err, "pid 4242")
	assert.ErrorContains(t, err, infoPath)
	assert.NotContains(t, err.Error(), "spawn reached", "no proxy child may be forked while the orphan cannot be stopped")
}

// TestCleanupOrphanBackendRefusalsNeverReadServerInfo pins #8489 AC-8: a
// held proxy-child.lock, a malformed record, an unverifiable live record and
// a root-ID mismatch keep today's refusal error, and sql-server.info is
// never consulted — a fully matching dolt named there stays alive.
func TestCleanupOrphanBackendRefusalsNeverReadServerInfo(t *testing.T) {
	// matchingCandidate would be killed if the refusal paths ever reached the
	// reap: a dolt running this root's launch snapshot.
	matchingCandidate := func(root string) *fakeOrphanCandidate {
		return &fakeOrphanCandidate{
			basename: "dolt",
			cmdline:  "dolt sql-server --config " + filepath.Join(root, server.LaunchConfigFileName),
		}
	}
	installSeam := func(t *testing.T, root string) *fakeOrphanCandidate {
		t.Helper()
		fake := matchingCandidate(root)
		opened := false
		installOrphanCandidate(t, func(int) (orphanCandidate, bool, error) {
			opened = true
			return fake, false, nil
		})
		t.Cleanup(func() {
			assert.False(t, opened, "a refusal path must never open the candidate sql-server.info names")
		})
		return fake
	}

	t.Run("held child lock", func(t *testing.T) {
		root := t.TempDir()
		helper := startHelperProcess(t)
		require.NoError(t, pidfile.Write(root, server.PIDFileName, v2Record(t, root, pidfile.KindDoltBackend, helper)))
		writeServerInfo(t, root, 4242)
		fake := installSeam(t, root)

		proxyLock, err := util.TryLock(filepath.Join(root, LockFileName))
		require.NoError(t, err)
		defer proxyLock.Unlock()
		childLock, err := util.TryLock(filepath.Join(root, server.LockFileName))
		require.NoError(t, err)
		defer childLock.Unlock()

		err = cleanupOrphanBackend(root)
		require.ErrorContains(t, err, "refusing backend cleanup")
		assert.False(t, fake.killed)
		assertHelperAlive(t, helper)
	})

	t.Run("malformed record", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.WriteFile(pidfile.Path(root, server.PIDFileName), []byte(`{"pid":`), 0o600))
		writeServerInfo(t, root, 4242)
		fake := installSeam(t, root)

		proxyLock, err := util.TryLock(filepath.Join(root, LockFileName))
		require.NoError(t, err)
		defer proxyLock.Unlock()

		err = cleanupOrphanBackend(root)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrUnverifiableProcess)
		assert.False(t, fake.killed)
	})

	t.Run("unverifiable live record", func(t *testing.T) {
		root := t.TempDir()
		helper := startHelperProcess(t)
		require.NoError(t, pidfile.Write(root, server.PIDFileName, pidfile.PidFile{
			Pid:  helper.cmd.Process.Pid,
			Port: 3306,
		}))
		writeServerInfo(t, root, 4242)
		fake := installSeam(t, root)

		proxyLock, err := util.TryLock(filepath.Join(root, LockFileName))
		require.NoError(t, err)
		defer proxyLock.Unlock()

		err = cleanupOrphanBackend(root)
		require.ErrorContains(t, err, "unverifiable live process")
		assert.False(t, fake.killed)
		assertHelperAlive(t, helper)
	})

	t.Run("root identity mismatch", func(t *testing.T) {
		root := t.TempDir()
		helper := startHelperProcess(t)
		pf := v2Record(t, root, pidfile.KindDoltBackend, helper)
		pf.RootID = "different-workspace"
		require.NoError(t, pidfile.Write(root, server.PIDFileName, pf))
		writeServerInfo(t, root, 4242)
		fake := installSeam(t, root)

		proxyLock, err := util.TryLock(filepath.Join(root, LockFileName))
		require.NoError(t, err)
		defer proxyLock.Unlock()

		err = cleanupOrphanBackend(root)
		require.ErrorContains(t, err, "root identity mismatch")
		assert.False(t, fake.killed)
		assertHelperAlive(t, helper)
	})
}
