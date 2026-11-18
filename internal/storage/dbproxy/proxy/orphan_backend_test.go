//go:build !windows

package proxy

import (
	"bytes"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
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

// fakeOrphanPID is the pid the scripted sql-server.info files name.
const fakeOrphanPID = 4242

// fakeOrphan is a scripted orphanCandidate.
type fakeOrphan struct {
	exe        string
	cmdline    string
	cmdErr     error
	killErr    error
	neverExits bool

	killed bool
	closed bool
}

func (f *fakeOrphan) executableBasename() (string, bool, error) { return f.exe, false, nil }

func (f *fakeOrphan) commandLineContains(needle string) (bool, bool, error) {
	if f.cmdErr != nil {
		return false, false, f.cmdErr
	}
	return strings.Contains(f.cmdline, needle), false, nil
}

func (f *fakeOrphan) kill() (bool, error) {
	if f.killErr != nil {
		return false, f.killErr
	}
	f.killed = true
	return false, nil
}

func (f *fakeOrphan) exited() (bool, error) { return f.killed && !f.neverExits, nil }

func (f *fakeOrphan) close() { f.closed = true }

// useFakeOrphan makes fakeOrphanPID open as proc (any other pid is gone) and
// counts every open, so a test can prove sql-server.info was never acted on.
func useFakeOrphan(t *testing.T, proc *fakeOrphan) *atomic.Int32 {
	t.Helper()
	var opens atomic.Int32
	previous := openOrphanCandidate
	openOrphanCandidate = func(pid int) (orphanCandidate, bool, error) {
		opens.Add(1)
		if pid != fakeOrphanPID || proc == nil {
			return nil, true, nil
		}
		return proc, false, nil
	}
	t.Cleanup(func() { openOrphanCandidate = previous })
	return &opens
}

// writeServerInfo writes root/.dolt/sql-server.info and returns its path.
func writeServerInfo(t *testing.T, root, content string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".dolt"), 0o700))
	path := filepath.Join(root, ".dolt", doltserver.ServerInfoFileName)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func fakeOrphanInfo(t *testing.T, root string) string {
	t.Helper()
	return writeServerInfo(t, root, strconv.Itoa(fakeOrphanPID)+":3307:secret")
}

// captureLog redirects the standard logger for one test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(previous) })
	return &buf
}

func doltLaunchedWith(configPath string) string {
	return "/usr/local/bin/dolt sql-server --config " + configPath
}

// holdProxyLock takes proxy.lock as spawnAndHandoff's caller does.
func holdProxyLock(t *testing.T, root string) {
	t.Helper()
	lock, err := util.TryLock(filepath.Join(root, LockFileName))
	require.NoError(t, err)
	t.Cleanup(lock.Unlock)
}

// deadHelperPID returns the pid of a helper that has exited.
func deadHelperPID(t *testing.T) int {
	t.Helper()
	helper := startHelperProcess(t)
	handle, err := procid.Open(helper.cmd.Process.Pid, helper.token)
	require.NoError(t, err)
	require.NoError(t, handle.Kill())
	require.NoError(t, handle.Close())
	assertHelperExited(t, helper)
	return helper.cmd.Process.Pid
}

// AC-4 (#8489): with no live backend record and a free child lock, a process
// sql-server.info names that is a dolt launched with one of this root's
// bd-owned configs (today's launch or runtime config, or the config.yaml bd
// 1.3.x launched with) is killed, its exit confirmed and logged.
func TestCleanupOrphanBackendReapsVerifiedUnrecordedDolt(t *testing.T) {
	for _, name := range []string{server.LaunchConfigFileName, server.RuntimeConfigFileName, "config.yaml"} {
		t.Run("no record/"+name, func(t *testing.T) {
			root := t.TempDir()
			holdProxyLock(t, root)
			infoPath := fakeOrphanInfo(t, root)
			proc := &fakeOrphan{exe: "dolt", cmdline: doltLaunchedWith(filepath.Join(root, name))}
			useFakeOrphan(t, proc)
			logs := captureLog(t)

			require.NoError(t, cleanupOrphanBackend(root))
			assert.True(t, proc.killed, "verified unrecorded orphan dolt was not killed")
			assert.True(t, proc.closed)
			assert.Contains(t, logs.String(), "stopped unrecorded orphan dolt pid 4242 named by "+infoPath)
		})
	}

	t.Run("dead legacy record in front", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, pidfile.Write(root, server.PIDFileName, pidfile.PidFile{Pid: deadHelperPID(t), Port: 3306}))
		holdProxyLock(t, root)
		infoPath := fakeOrphanInfo(t, root)
		proc := &fakeOrphan{exe: "dolt", cmdline: doltLaunchedWith(filepath.Join(root, server.LaunchConfigFileName))}
		useFakeOrphan(t, proc)
		logs := captureLog(t)

		require.NoError(t, cleanupOrphanBackend(root))
		assert.Len(t, quarantines(t, root, server.PIDFileName), 1)
		assert.True(t, proc.killed)
		assert.Contains(t, logs.String(), "stopped unrecorded orphan dolt pid 4242 named by "+infoPath)
	})

	t.Run("dead v2 record in front", func(t *testing.T) {
		root := t.TempDir()
		helper := startHelperProcess(t)
		pf := v2Record(t, root, pidfile.KindDoltBackend, helper)
		pf.Birth += "-recycled"
		require.NoError(t, pidfile.Write(root, server.PIDFileName, pf))
		holdProxyLock(t, root)
		fakeOrphanInfo(t, root)
		proc := &fakeOrphan{exe: "dolt", cmdline: doltLaunchedWith(filepath.Join(root, "config.yaml"))}
		useFakeOrphan(t, proc)

		require.NoError(t, cleanupOrphanBackend(root))
		assert.Len(t, quarantines(t, root, server.PIDFileName), 1)
		assert.True(t, proc.killed)
		assertHelperAlive(t, helper)
	})
}

// AC-5: a missing or malformed info file, a dead pid, a live non-dolt, or a
// dolt for anything but this root's configs gets no signal and nil.
func TestCleanupOrphanBackendLeavesUnverifiedInfoPidAlone(t *testing.T) {
	for _, content := range []string{"", "garbage", "abc:3307:x", "0:3307:x", "-5:3307:x", "4242:3307", "4242:port:x"} {
		t.Run("malformed "+strconv.Quote(content), func(t *testing.T) {
			root := t.TempDir()
			holdProxyLock(t, root)
			writeServerInfo(t, root, content)
			opens := useFakeOrphan(t, &fakeOrphan{exe: "dolt"})
			require.NoError(t, cleanupOrphanBackend(root))
			assert.Zero(t, opens.Load(), "a malformed info file must name no process")
		})
	}

	t.Run("missing", func(t *testing.T) {
		root := t.TempDir()
		holdProxyLock(t, root)
		opens := useFakeOrphan(t, &fakeOrphan{exe: "dolt"})
		require.NoError(t, cleanupOrphanBackend(root))
		assert.Zero(t, opens.Load())
	})

	t.Run("dead pid", func(t *testing.T) {
		root := t.TempDir()
		holdProxyLock(t, root)
		writeServerInfo(t, root, "4243:3307:x")
		opens := useFakeOrphan(t, &fakeOrphan{exe: "dolt"})
		require.NoError(t, cleanupOrphanBackend(root))
		assert.Equal(t, int32(1), opens.Load())
	})

	other := t.TempDir()
	for name, mk := range map[string]func(root string) *fakeOrphan{
		"non-dolt with this root's config": func(root string) *fakeOrphan {
			return &fakeOrphan{exe: "sleep", cmdline: "sleep --config " + filepath.Join(root, "config.yaml")}
		},
		"dolt for another root's config.yaml": func(string) *fakeOrphan {
			return &fakeOrphan{exe: "dolt", cmdline: doltLaunchedWith(filepath.Join(other, "config.yaml"))}
		},
		"dolt for another root's launch config": func(string) *fakeOrphan {
			return &fakeOrphan{exe: "dolt", cmdline: doltLaunchedWith(filepath.Join(other, server.LaunchConfigFileName))}
		},
		"dolt for a sibling root sharing the prefix": func(root string) *fakeOrphan {
			return &fakeOrphan{exe: "dolt", cmdline: doltLaunchedWith(root + "-other/config.yaml")}
		},
		"dolt with no config": func(string) *fakeOrphan {
			return &fakeOrphan{exe: "dolt", cmdline: "/usr/local/bin/dolt sql-server"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			holdProxyLock(t, root)
			fakeOrphanInfo(t, root)
			proc := mk(root)
			useFakeOrphan(t, proc)
			require.NoError(t, cleanupOrphanBackend(root))
			assert.False(t, proc.killed, "an unverified process was signaled")
			assert.True(t, proc.closed)
		})
	}
}

// AC-6: a dolt whose command line cannot be read (always so on Windows) gets
// no signal; one log line names the pid, the info file and the remedy.
func TestCleanupOrphanBackendUnreadableCommandLineSendsNoSignal(t *testing.T) {
	root := t.TempDir()
	holdProxyLock(t, root)
	infoPath := fakeOrphanInfo(t, root)
	proc := &fakeOrphan{exe: "dolt.exe", cmdErr: errors.New("reading another process's command line is not supported")}
	useFakeOrphan(t, proc)
	logs := captureLog(t)

	require.NoError(t, cleanupOrphanBackend(root))
	assert.False(t, proc.killed)
	out := logs.String()
	assert.Equal(t, 1, strings.Count(out, "\n"), "want exactly one log line, got %q", out)
	assert.Contains(t, out, "dolt pid 4242 named by "+infoPath)
	assert.Contains(t, out, "if no other bd or dolt serves this workspace, stop pid 4242 and retry")
}

// AC-7: a matched orphan whose kill fails, or which outlives
// backendExitTimeout, fails the spawn with an error naming the pid and info
// file, and no proxy child is started.
func TestSpawnRefusesWhenUnrecordedOrphanSurvives(t *testing.T) {
	for name, proc := range map[string]*fakeOrphan{
		"kill fails":  {killErr: errors.New("operation not permitted")},
		"never exits": {neverExits: true},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			infoPath := fakeOrphanInfo(t, root)
			proc.exe = "dolt"
			proc.cmdline = doltLaunchedWith(filepath.Join(root, server.RuntimeConfigFileName))
			useFakeOrphan(t, proc)
			previous := ResolveExecutable
			ResolveExecutable = func() (string, error) { return "", errors.New("spawn reached") }
			t.Cleanup(func() { ResolveExecutable = previous })
			lock, err := util.TryLock(filepath.Join(root, LockFileName))
			require.NoError(t, err)

			_, err = spawnAndHandoff(root, externalOpenOpts(root), time.Now().Add(time.Minute), "", lock, adoptionResult{status: adoptionNoRecord})
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "spawn reached", "a proxy child was started past a surviving orphan")
			assert.Contains(t, err.Error(), "pid 4242")
			assert.Contains(t, err.Error(), infoPath)
		})
	}
}

// AC-8: the refusal paths keep today's errors and never act on
// sql-server.info, even when it names a matching dolt.
func TestCleanupOrphanBackendRefusalsNeverReadServerInfo(t *testing.T) {
	for name, tc := range map[string]struct {
		setup   func(t *testing.T, root string)
		wantErr string
	}{
		"held child lock": {
			setup: func(t *testing.T, root string) {
				childLock, err := util.TryLock(filepath.Join(root, server.LockFileName))
				require.NoError(t, err)
				t.Cleanup(childLock.Unlock)
			},
			wantErr: "refusing backend cleanup",
		},
		"malformed record": {
			setup: func(t *testing.T, root string) {
				require.NoError(t, os.WriteFile(pidfile.Path(root, server.PIDFileName), []byte(`{"pid":`), 0o600))
			},
			wantErr: "backend cleanup refused for unverifiable process",
		},
		"unverifiable live record": {
			setup: func(t *testing.T, root string) {
				helper := startHelperProcess(t)
				require.NoError(t, pidfile.Write(root, server.PIDFileName, pidfile.PidFile{Pid: helper.cmd.Process.Pid, Port: 3306}))
			},
			wantErr: "unverifiable live process",
		},
		"root identity mismatch": {
			setup: func(t *testing.T, root string) {
				pf := v2Record(t, root, pidfile.KindDoltBackend, startHelperProcess(t))
				pf.RootID = "different-workspace"
				require.NoError(t, pidfile.Write(root, server.PIDFileName, pf))
			},
			wantErr: "root identity mismatch",
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			holdProxyLock(t, root)
			fakeOrphanInfo(t, root)
			proc := &fakeOrphan{exe: "dolt", cmdline: doltLaunchedWith(filepath.Join(root, "config.yaml"))}
			opens := useFakeOrphan(t, proc)
			tc.setup(t, root)

			err := cleanupOrphanBackend(root)
			require.ErrorContains(t, err, tc.wantErr)
			assert.Zero(t, opens.Load(), "a refusal path acted on sql-server.info")
			assert.False(t, proc.killed)
		})
	}
}
