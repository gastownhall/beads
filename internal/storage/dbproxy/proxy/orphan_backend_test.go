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
	argv       []string
	cmdErr     error
	unpinned   bool
	killErr    error
	neverExits bool

	killed bool
	closed bool
}

func (f *fakeOrphan) executableBasename() (string, bool, error) { return f.exe, false, nil }

func (f *fakeOrphan) commandLineArgs() ([]string, bool, error) {
	if f.cmdErr != nil {
		return nil, false, f.cmdErr
	}
	return f.argv, false, nil
}

func (f *fakeOrphan) pinned() bool { return !f.unpinned }

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

// doltLaunchedWith is the argv bd's DoltServer launches dolt with.
func doltLaunchedWith(configPath string) []string {
	return []string{"/usr/local/bin/dolt", "sql-server", "--config", configPath}
}

// localServerOpenOpts are spawn options for the managed-local backend, the
// one whose dolt bd launches and so the only one the reap runs for.
func localServerOpenOpts(root string) OpenOpts {
	return OpenOpts{
		Backend:        BackendLocalServer,
		ConfigFilePath: filepath.Join(root, "config.yaml"),
		LogFilePath:    filepath.Join(root, LogFileName),
		DoltBinPath:    "/usr/local/bin/dolt",
		Port:           3307,
	}
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
			proc := &fakeOrphan{exe: "dolt", argv: doltLaunchedWith(filepath.Join(root, name))}
			useFakeOrphan(t, proc)
			logs := captureLog(t)

			require.NoError(t, cleanupOrphanBackend(root, BackendLocalServer))
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
		proc := &fakeOrphan{exe: "dolt", argv: doltLaunchedWith(filepath.Join(root, server.LaunchConfigFileName))}
		useFakeOrphan(t, proc)
		logs := captureLog(t)

		require.NoError(t, cleanupOrphanBackend(root, BackendLocalServer))
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
		proc := &fakeOrphan{exe: "dolt", argv: doltLaunchedWith(filepath.Join(root, "config.yaml"))}
		useFakeOrphan(t, proc)

		require.NoError(t, cleanupOrphanBackend(root, BackendLocalServer))
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
			require.NoError(t, cleanupOrphanBackend(root, BackendLocalServer))
			assert.Zero(t, opens.Load(), "a malformed info file must name no process")
		})
	}

	t.Run("missing", func(t *testing.T) {
		root := t.TempDir()
		holdProxyLock(t, root)
		opens := useFakeOrphan(t, &fakeOrphan{exe: "dolt"})
		require.NoError(t, cleanupOrphanBackend(root, BackendLocalServer))
		assert.Zero(t, opens.Load())
	})

	t.Run("dead pid", func(t *testing.T) {
		root := t.TempDir()
		holdProxyLock(t, root)
		writeServerInfo(t, root, "4243:3307:x")
		opens := useFakeOrphan(t, &fakeOrphan{exe: "dolt"})
		require.NoError(t, cleanupOrphanBackend(root, BackendLocalServer))
		assert.Equal(t, int32(1), opens.Load())
	})

	other := t.TempDir()
	for name, mk := range map[string]func(root string) *fakeOrphan{
		"non-dolt with this root's config": func(root string) *fakeOrphan {
			return &fakeOrphan{exe: "sleep", argv: []string{"sleep", "--config", filepath.Join(root, "config.yaml")}}
		},
		"dolt for another root's config.yaml": func(string) *fakeOrphan {
			return &fakeOrphan{exe: "dolt", argv: doltLaunchedWith(filepath.Join(other, "config.yaml"))}
		},
		"dolt for another root's launch config": func(string) *fakeOrphan {
			return &fakeOrphan{exe: "dolt", argv: doltLaunchedWith(filepath.Join(other, server.LaunchConfigFileName))}
		},
		"dolt for a sibling root sharing the prefix": func(root string) *fakeOrphan {
			return &fakeOrphan{exe: "dolt", argv: doltLaunchedWith(root + "-other/config.yaml")}
		},
		"dolt with no config": func(string) *fakeOrphan {
			return &fakeOrphan{exe: "dolt", argv: []string{"/usr/local/bin/dolt", "sql-server"}}
		},
		"dolt for this root's config.yaml.other": func(root string) *fakeOrphan {
			return &fakeOrphan{exe: "dolt", argv: doltLaunchedWith(filepath.Join(root, "config.yaml.other"))}
		},
		"dolt for a longer launch config file name": func(root string) *fakeOrphan {
			return &fakeOrphan{exe: "dolt", argv: doltLaunchedWith(filepath.Join(root, server.LaunchConfigFileName+".bak"))}
		},
		"dolt with this root's config inside another argument": func(root string) *fakeOrphan {
			return &fakeOrphan{exe: "dolt", argv: []string{"/usr/local/bin/dolt", "sql-server", "--user-arg=--config " + filepath.Join(root, "config.yaml")}}
		},
		"dolt with a second --config of another root": func(root string) *fakeOrphan {
			return &fakeOrphan{exe: "dolt", argv: append(doltLaunchedWith(filepath.Join(root, "config.yaml")), "--config", filepath.Join(other, "config.yaml"))}
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			holdProxyLock(t, root)
			fakeOrphanInfo(t, root)
			proc := mk(root)
			useFakeOrphan(t, proc)
			require.NoError(t, cleanupOrphanBackend(root, BackendLocalServer))
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

	require.NoError(t, cleanupOrphanBackend(root, BackendLocalServer))
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
			proc.argv = doltLaunchedWith(filepath.Join(root, server.RuntimeConfigFileName))
			useFakeOrphan(t, proc)
			previous := ResolveExecutable
			ResolveExecutable = func() (string, error) { return "", errors.New("spawn reached") }
			t.Cleanup(func() { ResolveExecutable = previous })
			lock, err := util.TryLock(filepath.Join(root, LockFileName))
			require.NoError(t, err)

			_, err = spawnAndHandoff(root, localServerOpenOpts(root), time.Now().Add(time.Minute), "", lock, adoptionResult{status: adoptionNoRecord})
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
			proc := &fakeOrphan{exe: "dolt", argv: doltLaunchedWith(filepath.Join(root, "config.yaml"))}
			opens := useFakeOrphan(t, proc)
			tc.setup(t, root)

			err := cleanupOrphanBackend(root, BackendLocalServer)
			require.ErrorContains(t, err, tc.wantErr)
			assert.Zero(t, opens.Load(), "a refusal path acted on sql-server.info")
			assert.False(t, proc.killed)
		})
	}
}

// Only the managed-local backend's dolt is one bd launched. An external
// backend's dolt belongs to its operator and may serve this very directory,
// so its spawn never reads sql-server.info, even when the file names a dolt
// whose argv matches this root's config, and logs nothing about it.
func TestCleanupOrphanBackendNeverReapsForExternalBackend(t *testing.T) {
	t.Run("cleanup", func(t *testing.T) {
		root := t.TempDir()
		holdProxyLock(t, root)
		fakeOrphanInfo(t, root)
		proc := &fakeOrphan{exe: "dolt", argv: doltLaunchedWith(filepath.Join(root, "config.yaml"))}
		opens := useFakeOrphan(t, proc)
		logs := captureLog(t)

		require.NoError(t, cleanupOrphanBackend(root, BackendExternal))
		assert.Zero(t, opens.Load(), "an external backend's spawn acted on sql-server.info")
		assert.False(t, proc.killed)
		assert.Empty(t, logs.String())
	})

	t.Run("dead record in front", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, pidfile.Write(root, server.PIDFileName, pidfile.PidFile{Pid: deadHelperPID(t), Port: 3306}))
		holdProxyLock(t, root)
		fakeOrphanInfo(t, root)
		proc := &fakeOrphan{exe: "dolt", argv: doltLaunchedWith(filepath.Join(root, "config.yaml"))}
		opens := useFakeOrphan(t, proc)

		require.NoError(t, cleanupOrphanBackend(root, BackendExternal))
		assert.Len(t, quarantines(t, root, server.PIDFileName), 1, "the dead record is still quarantined")
		assert.Zero(t, opens.Load())
		assert.False(t, proc.killed)
	})

	t.Run("spawn", func(t *testing.T) {
		root := t.TempDir()
		fakeOrphanInfo(t, root)
		proc := &fakeOrphan{exe: "dolt", argv: doltLaunchedWith(filepath.Join(root, "config.yaml"))}
		opens := useFakeOrphan(t, proc)
		logs := captureLog(t)
		previous := ResolveExecutable
		ResolveExecutable = func() (string, error) { return "", errors.New("spawn reached") }
		t.Cleanup(func() { ResolveExecutable = previous })
		lock, err := util.TryLock(filepath.Join(root, LockFileName))
		require.NoError(t, err)

		_, err = spawnAndHandoff(root, externalOpenOpts(root), time.Now().Add(time.Minute), "", lock, adoptionResult{status: adoptionNoRecord})
		require.ErrorContains(t, err, "spawn reached", "the external spawn should go on to start its child")
		assert.Zero(t, opens.Load(), "an external backend's spawn acted on sql-server.info")
		assert.False(t, proc.killed)
		assert.NotContains(t, logs.String(), "4242")
	})
}

// A full match on a handle that does not pin the process (darwin, a Linux
// kernel without pidfd) is not signaled: the orphan could exit and its PID
// be reused before the kill. One log line names the pid, the info file and
// the manual remedy.
func TestCleanupOrphanBackendUnpinnedMatchSendsNoSignal(t *testing.T) {
	root := t.TempDir()
	holdProxyLock(t, root)
	infoPath := fakeOrphanInfo(t, root)
	proc := &fakeOrphan{exe: "dolt", argv: doltLaunchedWith(filepath.Join(root, "config.yaml")), unpinned: true}
	useFakeOrphan(t, proc)
	logs := captureLog(t)

	require.NoError(t, cleanupOrphanBackend(root, BackendLocalServer))
	assert.False(t, proc.killed, "an unpinned process was signaled")
	assert.True(t, proc.closed)
	out := logs.String()
	assert.Equal(t, 1, strings.Count(out, "\n"), "want exactly one log line, got %q", out)
	assert.Contains(t, out, "dolt pid 4242 named by "+infoPath)
	assert.Contains(t, out, "if no other bd or dolt serves this workspace, stop pid 4242 and retry")
}

// launchedWithConfig matches whole arguments: the "--config", "<path>" pair
// or one "--config=<path>" argument, with every --config naming an allowed
// path. A substring of the joined command line is never enough.
func TestLaunchedWithConfigMatchesWholeArguments(t *testing.T) {
	const root = "/ws/root"
	allowed := orphanBackendConfigPaths(root)
	config := root + "/config.yaml"
	launch := root + "/" + server.LaunchConfigFileName
	for name, tc := range map[string]struct {
		argv []string
		want bool
	}{
		"pair":                            {[]string{"dolt", "sql-server", "--config", config}, true},
		"equals form":                     {[]string{"dolt", "sql-server", "--config=" + launch}, true},
		"flag after the config":           {[]string{"dolt", "sql-server", "--config", launch, "--loglevel", "info"}, true},
		"same allowed config twice":       {[]string{"dolt", "sql-server", "--config", config, "--config=" + config}, true},
		"suffix after the file name":      {[]string{"dolt", "sql-server", "--config", config + ".other"}, false},
		"longer launch config file name":  {[]string{"dolt", "sql-server", "--config", launch + ".bak"}, false},
		"needle inside another argument":  {[]string{"dolt", "sql-server", "--user-arg=--config " + config}, false},
		"pair joined into one argument":   {[]string{"dolt", "sql-server", "--config " + config}, false},
		"config of another root":          {[]string{"dolt", "sql-server", "--config", "/ws/other/config.yaml"}, false},
		"second config of another root":   {[]string{"dolt", "sql-server", "--config", config, "--config", "/ws/other/config.yaml"}, false},
		"dangling --config":               {[]string{"dolt", "sql-server", "--config"}, false},
		"config path with no flag":        {[]string{"dolt", "sql-server", config}, false},
		"no arguments":                    {[]string{"dolt"}, false},
		"program name only names config":  {[]string{"--config=" + config}, false},
		"empty argv":                      {nil, false},
		"value consumed by --config pair": {[]string{"dolt", "--config", "--config", config}, false},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, launchedWithConfig(tc.argv, allowed), "argv %q", tc.argv)
		})
	}
}
