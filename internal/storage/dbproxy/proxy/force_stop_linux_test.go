//go:build linux

package proxy

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/procid"
	"github.com/steveyegge/beads/internal/storage/dbproxy/pidfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/server"
	"github.com/steveyegge/beads/internal/storage/dbproxy/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestForceStopUnverifiedFreeLock(t *testing.T) {
	root := t.TempDir()
	helper := startNamedForceStopHelper(t, root, "bd")
	writeLegacyProxyRecord(t, root, helper)

	report, err := ForceStopUnverified(root)
	require.NoError(t, err)
	assert.True(t, report.RecordFound)
	assert.False(t, report.LockWasHeld)
	assert.Equal(t, helper.cmd.Process.Pid, report.PID)
	assert.Equal(t, "bd", report.Executable)
	assert.True(t, report.SignalSent)
	assert.NotEmpty(t, report.QuarantinedPath)
	assertHelperExited(t, helper)
	assert.NoFileExists(t, pidfile.Path(root, PIDFileName))
	assert.FileExists(t, report.QuarantinedPath)
}

func TestForceStopUnverifiedHeldLock(t *testing.T) {
	root := t.TempDir()
	helper := startNamedForceStopHelper(t, root, "dolt")
	writeLegacyProxyRecord(t, root, helper)
	held, err := util.TryLock(filepath.Join(root, LockFileName))
	require.NoError(t, err)
	released := make(chan struct{})
	go func() {
		<-helper.done
		held.Unlock()
		close(released)
	}()

	report, err := ForceStopUnverified(root, ForceStopOptions{Timeout: 5 * time.Second})
	require.NoError(t, err)
	<-released
	assert.True(t, report.LockWasHeld)
	assert.Equal(t, "dolt", report.Executable)
	assert.True(t, report.SignalSent)
	assert.NotEmpty(t, report.QuarantinedPath)
	assert.NoFileExists(t, pidfile.Path(root, PIDFileName))
}

func TestForceStopUnverifiedGoneProcess(t *testing.T) {
	root := t.TempDir()
	helper := startNamedForceStopHelper(t, root, "bd")
	writeLegacyProxyRecord(t, root, helper)
	handle, err := procid.Open(helper.cmd.Process.Pid, helper.token)
	require.NoError(t, err)
	require.NoError(t, handle.Kill())
	require.NoError(t, handle.Close())
	assertHelperExited(t, helper)

	report, err := ForceStopUnverified(root)
	require.NoError(t, err)
	assert.True(t, report.ProcessWasGone)
	assert.False(t, report.SignalSent)
	assert.NotEmpty(t, report.QuarantinedPath)
}

// A pre-v2 managed-local deployment leaves BOTH proxy.pid and proxy-child.pid
// as legacy records, so Shutdown refuses on both sides and the force path must
// still be offered and recover both records.
func TestForceStopUnverifiedPairedLegacyRecords(t *testing.T) {
	root := t.TempDir()
	proxyHelper := startNamedForceStopHelper(t, root, "bd")
	backendHelper := startNamedForceStopHelper(t, root, "dolt")
	writeLegacyProxyRecord(t, root, proxyHelper)
	require.NoError(t, pidfile.Write(root, server.PIDFileName, pidfile.PidFile{
		Pid:  backendHelper.cmd.Process.Pid,
		Port: 3308,
	}))

	shutdownErr := Shutdown(root)
	require.Error(t, shutdownErr)
	assert.ErrorIs(t, shutdownErr, ErrUnverifiableProcess)
	assert.True(t, CanForceStopUnverified(shutdownErr),
		"paired legacy records must remain eligible for --force recovery")

	report, err := ForceStopUnverified(root)
	require.NoError(t, err)
	assert.True(t, report.SignalSent)
	assert.NotEmpty(t, report.QuarantinedPath)
	require.NotNil(t, report.Backend)
	assert.True(t, report.Backend.RecordFound)
	assert.Equal(t, backendHelper.cmd.Process.Pid, report.Backend.PID)
	assert.Equal(t, "dolt", report.Backend.Executable)
	assert.True(t, report.Backend.SignalSent)
	assert.NotEmpty(t, report.Backend.QuarantinedPath)
	assertHelperExited(t, proxyHelper)
	assertHelperExited(t, backendHelper)
	assert.NoFileExists(t, pidfile.Path(root, PIDFileName))
	assert.NoFileExists(t, pidfile.Path(root, server.PIDFileName))
}

func TestForceStopUnverifiedRejectsForeignExecutable(t *testing.T) {
	root := t.TempDir()
	helper := startHelperProcess(t)
	writeLegacyProxyRecord(t, root, helper)

	report, err := ForceStopUnverified(root)
	require.ErrorContains(t, err, "want bd or dolt")
	assert.False(t, report.SignalSent)
	assert.Empty(t, report.QuarantinedPath)
	assertHelperAlive(t, helper)
	assert.FileExists(t, pidfile.Path(root, PIDFileName))
}

// A recycled PID can point at an unrelated live bd process; a basename match
// alone must not be a license to SIGKILL it. The command line has to tie the
// process to this workspace.
func TestForceStopUnverifiedRejectsForeignWorkspaceProcess(t *testing.T) {
	root := t.TempDir()
	helper := startNamedForceStopHelper(t, t.TempDir(), "bd")
	writeLegacyProxyRecord(t, root, helper)

	report, err := ForceStopUnverified(root)
	require.ErrorContains(t, err, "does not reference workspace")
	assert.Equal(t, "bd", report.Executable)
	assert.False(t, report.SignalSent)
	assert.Empty(t, report.QuarantinedPath)
	assertHelperAlive(t, helper)
	assert.FileExists(t, pidfile.Path(root, PIDFileName))
}

func TestForceStopUnverifiedRejectsVerifiableV2Record(t *testing.T) {
	root := t.TempDir()
	helper := startNamedForceStopHelper(t, root, "bd")
	record := v2Record(t, root, pidfile.KindProxy, helper)
	require.NoError(t, pidfile.Write(root, PIDFileName, record))

	report, err := ForceStopUnverified(root)
	require.ErrorContains(t, err, "use proxy.Shutdown")
	assert.False(t, report.SignalSent)
	assert.Empty(t, report.QuarantinedPath)
	assertHelperAlive(t, helper)
	assert.FileExists(t, pidfile.Path(root, PIDFileName))
}

// forceStopHelperEnv gates TestForceStopHelperProcess so a normal test run
// skips the helper body.
const forceStopHelperEnv = "BEADS_FORCE_STOP_HELPER"

// TestForceStopHelperProcess is the body of the helper processes
// startNamedForceStopHelper spawns: a re-exec of this test binary that idles
// until the test (or its Cleanup) kills it.
func TestForceStopHelperProcess(t *testing.T) {
	if os.Getenv(forceStopHelperEnv) != "1" {
		t.Skip("helper-process body; only meaningful under startNamedForceStopHelper")
	}
	time.Sleep(30 * time.Second)
}

// startNamedForceStopHelper starts a long-sleeping process whose executable
// basename is name and whose command line references dir (the binary lives
// inside it), matching how a workspace's own bd/dolt processes reference
// their root path. The body is this test binary re-executed in helper mode,
// not a renamed copy of the system sleep: on busybox/uutils-coreutils
// systems (Ubuntu 25.10+) sleep is a multicall binary dispatching on
// argv[0], so a renamed copy exits instantly and the fixture silently loses
// its live process.
func startNamedForceStopHelper(t *testing.T, dir, name string) helperProcess {
	t.Helper()
	self, err := os.Executable()
	require.NoError(t, err)
	self, err = filepath.EvalSymlinks(self)
	require.NoError(t, err)
	executable := filepath.Join(dir, name)
	// Hard link when dir is on the same filesystem; stream a copy otherwise.
	if os.Link(self, executable) != nil {
		src, err := os.Open(self)
		require.NoError(t, err)
		dst, err := os.OpenFile(executable, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o700)
		require.NoError(t, err)
		_, cpErr := io.Copy(dst, src)
		require.NoError(t, src.Close())
		require.NoError(t, dst.Close())
		require.NoError(t, cpErr)
	}

	cmd := exec.Command(executable, "-test.run=^TestForceStopHelperProcess$")
	cmd.Env = append(os.Environ(), forceStopHelperEnv+"=1")
	require.NoError(t, cmd.Start())
	token, err := procid.Capture(cmd.Process.Pid)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
		close(done)
	}()
	helper := helperProcess{cmd: cmd, token: token, done: done}
	t.Cleanup(func() {
		matched, verifyErr := procid.Verify(cmd.Process.Pid, token)
		if verifyErr == nil && matched {
			handle, openErr := procid.Open(cmd.Process.Pid, token)
			if openErr == nil {
				_ = handle.Kill()
				_ = handle.Close()
			}
		}
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Errorf("named helper pid %d did not exit", cmd.Process.Pid)
		}
	})
	return helper
}

func writeLegacyProxyRecord(t *testing.T, root string, helper helperProcess) {
	t.Helper()
	require.NoError(t, pidfile.Write(root, PIDFileName, pidfile.PidFile{
		Pid:  helper.cmd.Process.Pid,
		Port: 3307,
	}))
}
