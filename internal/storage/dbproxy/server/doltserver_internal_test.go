package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/dolthub/dolt/go/libraries/doltcore/servercfg"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/procid"
	"github.com/steveyegge/beads/internal/storage/dbproxy/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newFakeDoltServer builds a DoltServer whose dolt is this test binary in
// fake mode (see fakeDolt in testmain_test.go) and whose config names port at
// log_level info.
func newFakeDoltServer(t *testing.T, mode string, port int) (*DoltServer, string) {
	t.Helper()
	return newFakeDoltServerAtLevel(t, mode, port, "info")
}

// newFakeDoltServerAtLevel is newFakeDoltServer with the config's log_level.
func newFakeDoltServerAtLevel(t *testing.T, mode string, port int, logLevel string) (*DoltServer, string) {
	t.Helper()
	t.Setenv("BEADS_TEST_FAKE_DOLT", mode)
	rootDir := t.TempDir()
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(cfg, []byte(fmt.Sprintf("log_level: %s\nlistener:\n  host: 127.0.0.1\n  port: %d\n", logLevel, port)), 0o600))
	s, err := NewDoltServer(os.Args[0], rootDir, cfg, filepath.Join(t.TempDir(), "server.log"), 0, "")
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = s.Stop(ctx)
	})
	return s, rootDir
}

func freeTestPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// TestDoltServer_Start_GivesUpAfterMaxPorts pins the bound on recovery: a
// dolt that reports its port taken on every launch ends Start after
// maxStartPortAttempts ports with ErrPortInUse naming the last one, and no
// runtime config is left behind.
func TestDoltServer_Start_GivesUpAfterMaxPorts(t *testing.T) {
	s, rootDir := newFakeDoltServer(t, "inuse", freeTestPort(t))
	asked := 0
	operatorPort := s.config.Port()
	s.SetPortConflictPolicy(func(_ string, inUsePort int) error {
		asked++
		assert.Equal(t, operatorPort, inUsePort, "the policy is asked about the operator config's port")
		return nil
	})

	err := s.Start(context.Background())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrPortInUse), "got %v", err)
	assert.Contains(t, err.Error(), fmt.Sprintf("gave up after %d ports", maxStartPortAttempts))
	assert.Equal(t, 1, asked, "the policy judges the operator config's port, so it is asked once")
	_, serr := os.Stat(filepath.Join(rootDir, RuntimeConfigFileName))
	assert.True(t, os.IsNotExist(serr), "a failed Start must not leave a runtime config")
}

// TestDoltServer_Start_MissingReadyLineIsDiagnosed pins SF4: a dolt that
// answers on its port but never logs the ready line is not accepted, and the
// timeout error names the missing line and the log_level escape hatch.
func TestDoltServer_Start_MissingReadyLineIsDiagnosed(t *testing.T) {
	old := startReadyTimeout
	// The fake is this test binary, which takes a few seconds to start.
	startReadyTimeout = 30 * time.Second
	t.Cleanup(func() { startReadyTimeout = old })

	s, _ := newFakeDoltServer(t, "silent", freeTestPort(t))
	err := s.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "never logged \"Server ready. Accepting connections.\"")
	assert.Contains(t, err.Error(), "although something answered")
	assert.Contains(t, err.Error(), "log_level to warning")
}

// TestDoltServer_Start_QuietLogLevelProvesListenerOwnership pins readiness
// for a config whose log level hides dolt's ready line (warning, which
// Beads-generated configs use so dolt does not log every connection): a
// greeting counts once the listener is shown to be the child's, a listener
// shown to be another process's ends Start with ErrPortInUse (so a
// PortConflictPolicy can move the port), and where ownership cannot be told
// the greeting decides. A listener the host lists no holder for is another
// process's where the host lists this process's own listeners, and unknown
// where it lists none; only that case runs the host probe.
func TestDoltServer_Start_QuietLogLevelProvesListenerOwnership(t *testing.T) {
	for _, tc := range []struct {
		name      string
		ownership doltserver.Ownership
		provable  bool
		wantProbe bool
		wantInUse bool
	}{
		{name: "owned", ownership: doltserver.OwnershipOwned, provable: true},
		{name: "unknown", ownership: doltserver.OwnershipUnknown, provable: true},
		{name: "another process", ownership: doltserver.OwnershipForeign, provable: true, wantInUse: true},
		// Another user's socket (darwin), or a listener reached through a
		// forward (WSL2 mirrored networking).
		{name: "unlisted where own listeners are listed", ownership: doltserver.OwnershipUnlisted, provable: true, wantProbe: true, wantInUse: true},
		// WSL1 and some sandboxes list no sockets at all.
		{name: "unlisted where no listener is listed", ownership: doltserver.OwnershipUnlisted, provable: false, wantProbe: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newFakeDoltServerAtLevel(t, "silent", freeTestPort(t), "warning")
			asked, probed := 0, 0
			s.listenerOwner = func(pid, port int) doltserver.Ownership {
				asked++
				assert.Equal(t, s.pid, pid, "ownership is asked about the launched child")
				assert.Equal(t, s.config.Port(), port)
				return tc.ownership
			}
			s.ownerProvable = func() bool {
				probed++
				return tc.provable
			}
			err := s.Start(context.Background())
			assert.Positive(t, asked, "a greeting at a quiet log level must be checked for ownership")
			assert.Equal(t, tc.wantProbe, probed > 0, "the host probe runs only for an unlisted listener")
			if tc.wantInUse {
				require.Error(t, err)
				assert.ErrorIs(t, err, ErrPortInUse)
				assert.Contains(t, err.Error(), "another process")
				assert.False(t, s.Running(context.Background()))
				return
			}
			require.NoError(t, err)
			assert.True(t, s.Running(context.Background()))
		})
	}
}

// TestDoltServer_Start_QuietLogLevelRealOwnership runs the real ownership
// check against the fake child: on platforms that can tell (linux, darwin)
// the child's own listener is recognized, so Start succeeds on proven
// ownership, not on the unknown-ownership fallback.
func TestDoltServer_Start_QuietLogLevelRealOwnership(t *testing.T) {
	s, _ := newFakeDoltServerAtLevel(t, "silent", freeTestPort(t), "warning")
	var last doltserver.Ownership
	s.listenerOwner = func(pid, port int) doltserver.Ownership {
		last = doltserver.ClassifyListener(pid, port)
		return last
	}
	require.NoError(t, s.Start(context.Background()))
	assert.True(t, s.Running(context.Background()))
	if doltserver.ListenerOwnershipProvable() {
		assert.Equal(t, doltserver.OwnershipOwned, last, "the child's own listener must be proven its own")
	}
}

// holdTestPort listens on a free loopback port and accepts every
// connection, standing in for another process that took the port first.
func holdTestPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

// TestDoltServer_Start_UnknownOwnershipKeepsReadyLineProof pins readiness
// for the config cmd/bd generates on a host that cannot prove listener
// ownership (Windows, WSL1, some sandboxes): there it stays at log_level
// info, because only dolt's ready line then tells the dolt Start launched
// from another process holding its port. Such a listener answers at once,
// while dolt reaches its port check only after its own startup; its
// greeting must not count, dolt's port-in-use exit must reach the policy,
// and Start must come up on a moved port.
func TestDoltServer_Start_UnknownOwnershipKeepsReadyLineProof(t *testing.T) {
	foreign := holdTestPort(t)
	s, _ := newFakeDoltServerAtLevel(t, "slowbind", foreign, "info")
	s.listenerOwner = func(int, int) doltserver.Ownership { return doltserver.OwnershipUnknown }
	var asked []int
	s.SetPortConflictPolicy(func(_ string, inUsePort int) error {
		asked = append(asked, inUsePort)
		return nil
	})

	require.NoError(t, s.Start(context.Background()))
	assert.Equal(t, []int{foreign}, asked, "dolt's port-in-use exit must reach the policy")
	assert.NotEqual(t, foreign, s.config.Port(), "Start must not serve from the foreign listener's port")
	assert.True(t, s.Running(context.Background()))
}

// TestDoltServer_Start_QuietLogLevelMovesOffUnlistedListener pins readiness
// for the config cmd/bd generates on a host that lists this process's own
// listeners (log_level warning) when the listener on the port is listed as
// no process's: another user's socket, which lsof cannot see without
// privilege, or a Windows listener under WSL2's mirrored networking. Such a
// listener answers at once, while dolt reaches its port check only after
// its own startup; its greeting must not count, the port must reach the
// policy, and Start must come up on a moved port.
func TestDoltServer_Start_QuietLogLevelMovesOffUnlistedListener(t *testing.T) {
	foreign := holdTestPort(t)
	s, _ := newFakeDoltServerAtLevel(t, "slowbind", foreign, "warning")
	s.listenerOwner = func(_, port int) doltserver.Ownership {
		if port == foreign {
			return doltserver.OwnershipUnlisted
		}
		return doltserver.OwnershipOwned
	}
	s.ownerProvable = func() bool { return true }
	var asked []int
	s.SetPortConflictPolicy(func(_ string, inUsePort int) error {
		asked = append(asked, inUsePort)
		return nil
	})

	require.NoError(t, s.Start(context.Background()))
	assert.Equal(t, []int{foreign}, asked, "the unlisted listener's port must reach the policy")
	assert.NotEqual(t, foreign, s.config.Port(), "Start must not serve from the foreign listener's port")
	assert.True(t, s.Running(context.Background()))
}

// TestDoltServer_Start_FakeReadyLine is the positive control for the fake.
func TestDoltServer_Start_FakeReadyLine(t *testing.T) {
	s, _ := newFakeDoltServer(t, "ready", freeTestPort(t))
	require.NoError(t, s.Start(context.Background()))
	assert.True(t, s.Running(context.Background()))
}

func TestDoltServerStartUsesValidatedLaunchSnapshot(t *testing.T) {
	s, _ := newFakeDoltServer(t, "ready", freeTestPort(t))
	original, err := os.ReadFile(s.configPath)
	require.NoError(t, err)
	wantPort := s.config.Port()
	s.SetConfigValidator(func(servercfg.ServerConfig) error {
		return os.WriteFile(s.configPath, []byte("invalid: [operator mutation\n"), 0o600)
	})

	require.NoError(t, s.Start(context.Background()))
	assert.Equal(t, s.launchSnapshotPath(), s.launchConfigPath)
	body, err := os.ReadFile(s.launchSnapshotPath())
	require.NoError(t, err)
	assert.Equal(t, original, body)
	assert.Equal(t, wantPort, s.config.Port())
	info, err := os.Stat(s.launchSnapshotPath())
	require.NoError(t, err)
	if runtime.GOOS != "windows" {
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
	conn, err := s.Dial(context.Background())
	require.NoError(t, err)
	require.NoError(t, conn.Close())
}

func TestDoltServerConfigValidatorRejectsBeforeSpawn(t *testing.T) {
	s, rootDir := newFakeDoltServer(t, "ready", freeTestPort(t))
	s.SetConfigValidator(func(servercfg.ServerConfig) error {
		return errors.New("managed config rejected")
	})

	err := s.Start(context.Background())
	require.ErrorContains(t, err, "managed config rejected")
	assert.False(t, s.Running(context.Background()))
	_, err = os.Stat(s.launchSnapshotPath())
	assert.True(t, os.IsNotExist(err), "rejected validation must remove the launch snapshot")
	_, err = os.Stat(filepath.Join(rootDir, PIDFileName))
	assert.True(t, os.IsNotExist(err), "validation must reject before spawning")
}

// TestUseRuntimePort_CopiesRawText pins that the runtime config carries the
// operator config's environment placeholders and "$$" escapes unexpanded:
// dolt interpolates the runtime file when it reads it, so expanding them here
// would expand them twice and put their values on disk. The server's own
// view (Dial, DSN) still uses the interpolated values.
func TestUseRuntimePort_CopiesRawText(t *testing.T) {
	t.Setenv("BD_TEST_HOST", "127.0.0.1")
	rootDir := t.TempDir()
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(cfg, []byte("log_level: info\ndata_dir: \"a$$b\"\nlistener:\n  host: ${BD_TEST_HOST}\n  port: 40001\n"), 0o600))
	s, err := NewDoltServer(os.Args[0], rootDir, cfg, "", 0, "")
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1", s.config.Host())
	require.NoError(t, os.WriteFile(cfg, []byte("log_level: info\nlistener:\n  host: 127.0.0.1\n  port: 40002\n"), 0o600))

	require.NoError(t, s.useRuntimePort(40001))

	body, err := os.ReadFile(filepath.Join(rootDir, RuntimeConfigFileName))
	require.NoError(t, err)
	assert.Contains(t, string(body), "${BD_TEST_HOST}", "placeholder must be copied, not expanded")
	assert.Contains(t, string(body), "a$$b", "a $$ escape must be copied, not unescaped")
	assert.Equal(t, "127.0.0.1", s.config.Host(), "the server dials the interpolated host")
	assert.NotEqual(t, 40001, s.config.Port())
	assert.Equal(t, filepath.Join(rootDir, RuntimeConfigFileName), s.launchConfigPath)
	info, err := os.Stat(filepath.Join(rootDir, RuntimeConfigFileName))
	require.NoError(t, err)
	if runtime.GOOS != "windows" {
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
}

// newScriptDoltServer builds a DoltServer whose dolt is a shell script that
// answers `config`/`init` and runs sqlServer (shell) for `sql-server`, with
// $port set to the --config file's listener.port. Shell scripts start in
// milliseconds, unlike the fake-mode test binary, which is what the
// fast-exit cases need.
func newScriptDoltServer(t *testing.T, sqlServer string) *DoltServer {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("stand-in dolt is a shell script")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "dolt")
	script := "#!/bin/sh\ncase \"$1\" in\nconfig) echo fake; exit 0 ;;\ninit) exit 0 ;;\nsql-server)\n" +
		"  port=$(sed -n 's/^ *port: *\\([0-9][0-9]*\\).*/\\1/p' \"$3\" | head -n 1)\n" +
		sqlServer + "\n ;;\nesac\nexit 2\n"
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))
	cfg := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(cfg, []byte(fmt.Sprintf("log_level: info\nlistener:\n  host: 127.0.0.1\n  port: %d\n", freeTestPort(t))), 0o600))
	root := filepath.Join(dir, "root")
	require.NoError(t, os.MkdirAll(root, 0o755))
	s, err := NewDoltServer(bin, root, cfg, filepath.Join(dir, "server.log"), 0, "")
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = s.Stop(ctx)
	})
	return s
}

// captureAfterChildGone makes the birth-identity capture lose the race the
// way a real fast-exiting dolt can: it waits long enough for the stand-in
// to have exited, then fails as procid does for a dead pid.
func captureAfterChildGone(t *testing.T) {
	t.Helper()
	old := captureBirth
	captureBirth = func(pid int) (procid.Token, error) {
		time.Sleep(300 * time.Millisecond)
		return "", fmt.Errorf("procid: process is no longer running: no such process")
	}
	t.Cleanup(func() { captureBirth = old })
}

// TestDoltServer_Start_FastPortInUseExitBeatsIdentityCapture pins that a
// child which reports its port taken and exits before Start captured its
// identity is still classified as ErrPortInUse (and so still recoverable),
// not reported as a fatal identity-capture failure.
func TestDoltServer_Start_FastPortInUseExitBeatsIdentityCapture(t *testing.T) {
	captureAfterChildGone(t)
	s := newScriptDoltServer(t, `  echo "Port $port already in use." >&2; exit 1`)
	err := s.Start(context.Background())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrPortInUse), "got %v", err)
	assert.NotContains(t, err.Error(), "capture child birth identity")

	// With a policy, every move hits the same fast exit and Start keeps
	// recovering until its port budget runs out.
	s2 := newScriptDoltServer(t, `  echo "Port $port already in use." >&2; exit 1`)
	s2.SetPortConflictPolicy(func(string, int) error { return nil })
	err = s2.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), fmt.Sprintf("gave up after %d ports", maxStartPortAttempts))
}

// TestDoltServer_Start_FastOtherExitBeatsIdentityCapture: a child that exits
// on its own for another reason is reported as having exited, with its
// status, not as an identity-capture failure.
func TestDoltServer_Start_FastOtherExitBeatsIdentityCapture(t *testing.T) {
	captureAfterChildGone(t)
	s := newScriptDoltServer(t, `  echo "some other startup failure" >&2; exit 3`)
	err := s.Start(context.Background())
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrPortInUse), "got %v", err)
	assert.Contains(t, err.Error(), "exited (status 3) before startup completed")
}

// TestDoltServer_Start_IdentityCaptureFailsWhileChildAlive: when the child is
// alive and the capture fails anyway, that failure is fatal, and the child is
// killed rather than left running.
func TestDoltServer_Start_IdentityCaptureFailsWhileChildAlive(t *testing.T) {
	old := captureBirth
	captureBirth = func(int) (procid.Token, error) { return "", errors.New("capture broke") }
	t.Cleanup(func() { captureBirth = old })

	s := newScriptDoltServer(t, `  exec sleep 30`)
	done := make(chan error, 1)
	go func() { done <- s.Start(context.Background()) }()
	select {
	case err := <-done:
		require.Error(t, err)
		assert.False(t, errors.Is(err, ErrPortInUse), "got %v", err)
		assert.Contains(t, err.Error(), "capture child birth identity: capture broke")
	case <-time.After(20 * time.Second):
		t.Fatal("Start did not return: the live child was not killed")
	}
	assert.False(t, s.Running(context.Background()))
}

// TestDoltServer_Start_CanceledDuringPortConflictReleasesLock pins the root
// lock's single owner while a start is settling. The child has said its port
// is taken, which with a port policy is a conflict Start would retry under
// the same lock, but the start ends some other way first (its context is
// canceled, as a stop that begins mid-boot does): Start does not retry, so
// it must release the lock, and its cleanup, which takes the lock, must run.
// A lock kept on the strength of the child's port-in-use line alone was
// never released here.
func TestDoltServer_Start_CanceledDuringPortConflictReleasesLock(t *testing.T) {
	s := newScriptDoltServer(t, `  echo "Port $port already in use." >&2; exec sleep 30`)
	s.SetPortConflictPolicy(func(string, int) error { return nil })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err := s.Start(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.False(t, errors.Is(err, ErrPortInUse), "got %v", err)

	lock, err := util.TryLock(filepath.Join(s.rootDir, LockFileName))
	require.NoError(t, err, "Start returned without releasing %s", LockFileName)
	lock.Unlock()
	assert.NoFileExists(t, s.launchSnapshotPath(), "Start's failure cleanup did not run")
	assert.NoFileExists(t, s.runtimeConfigPath())
}

// TestDoltServer_Start_ImmediateExitRealCapture is the same fast port-in-use
// exit with the real identity capture: whichever of the capture and the
// child's exit wins, the result is ErrPortInUse.
func TestDoltServer_Start_ImmediateExitRealCapture(t *testing.T) {
	for i := 0; i < 10; i++ {
		s := newScriptDoltServer(t, `  echo "Port $port already in use." >&2; exit 1`)
		err := s.Start(context.Background())
		require.Error(t, err)
		require.True(t, errors.Is(err, ErrPortInUse), "attempt %d: got %v", i, err)
	}
}
