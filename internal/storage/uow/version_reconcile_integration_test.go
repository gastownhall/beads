package uow

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/steveyegge/beads/internal/procid"
	"github.com/steveyegge/beads/internal/storage/dbproxy/pidfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/storage/dbproxy/server"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/steveyegge/beads/internal/workapi"
	publicops "github.com/steveyegge/beads/issueops"
)

func newTestUOWProvider(t *testing.T) UnitOfWorkProvider {
	t.Helper()
	testutil.RequireDoltBinary(t)
	bin, err := exec.LookPath("dolt")
	require.NoError(t, err)

	bdBin := buildBDBinary(t)
	prev := proxy.ResolveExecutable
	proxy.ResolveExecutable = func() (string, error) { return bdBin, nil }
	t.Cleanup(func() { proxy.ResolveExecutable = prev })

	t.Setenv("HOME", t.TempDir())

	port, err := proxy.PickFreePort()
	require.NoError(t, err)
	storeRootDir := t.TempDir()
	shutdownOnInterrupt(t, storeRootDir)
	t.Cleanup(func() {
		// Read the backend's record BEFORE shutting down: a successful
		// proxy.Shutdown removes the pid file, so afterwards there is
		// nothing left to verify against.
		record := backendServerRecord(t, storeRootDir)
		err := proxy.Shutdown(storeRootDir)
		if err == nil {
			// Shutdown killed the backend AND confirmed its exit
			// (waitForRecordedProcessExit) before removing the record.
			// There is nothing left to check.
			return
		}
		t.Logf("proxy.Shutdown(%s): %v", storeRootDir, err)
		requireBackendExited(t, record)
	})
	cfgPath := writeServerConfig(t, port)
	logPath := filepath.Join(t.TempDir(), "server.log")

	provider, err := NewDoltServerUOWProvider(
		context.Background(),
		storeRootDir,
		"beads",
		logPath,
		cfgPath,
		proxy.BackendLocalServer,
		"root",
		"",
		bin,
		0,
		0,
		false,
		"",
	)
	require.NoError(t, err)
	require.NotNil(t, provider)
	t.Cleanup(func() { _ = provider.Close(context.Background()) })
	return provider
}

const (
	// backendExitTimeout is how long a test waits for the dolt sql-server to
	// leave the process table after proxy.Shutdown failed. It is generous on
	// purpose: Shutdown's own budget is already 5s of confirmation plus a 2s
	// post-kill minimum, and this only runs on the error path, where the
	// interesting outcome is "still there", not "took a while".
	backendExitTimeout = 30 * time.Second
	backendExitPoll    = 50 * time.Millisecond
)

// backendServerRecord returns the pid file the proxy's child manager wrote
// for the dolt sql-server under rootDir (server.PIDFileName). nil means there
// is nothing to verify: no server was started, or it shut down cleanly and
// removed its record.
//
// The whole record is returned, not just the PID: Birth is the process-birth
// token that makes the PID safe to act on at all.
func backendServerRecord(t *testing.T, rootDir string) *pidfile.PidFile {
	t.Helper()
	pf, err := pidfile.Read(rootDir, server.PIDFileName)
	if err != nil {
		t.Logf("read %s in %s: %v", server.PIDFileName, rootDir, err)
		return nil
	}
	if pf == nil || pf.Pid <= 0 {
		return nil
	}
	return pf
}

// requireBackendExited fails the test if the recorded dolt sql-server is
// still running after proxy.Shutdown returned an ERROR, then force-kills it
// so the leak does not outlive the run.
//
// It is deliberately a t.Errorf and not a t.Logf: a surviving server holds
// the temp tree the test is about to delete, which is precisely how this
// package produced sql-servers still serving directories that had been gone
// for hours (wy-j2zc8q). A leak must be visible where it is caused, not
// discovered later by a sweep.
//
// Every step is gated on procid birth identity rather than the bare PID. The
// server is not this process's child, so its PID is reusable the instant it
// exits, and proxy.Shutdown deliberately REFUSES to signal a record it cannot
// verify (ErrUnverifiableProcess) — killing that same PID here would defeat
// the exact protection the package went out of its way to provide. A record
// with no Birth token, or one whose token no longer matches, is therefore
// left alone: not the server we recorded, nothing to report.
func requireBackendExited(t *testing.T, pf *pidfile.PidFile) {
	t.Helper()
	if pf == nil || pf.Pid <= 0 {
		return
	}
	if pf.Birth == "" {
		// A record from before birth tokens were written. There is no safe
		// way to ask about that PID, so the check is skipped — say so, rather
		// than looking like a silent pass.
		t.Logf("backend record for pid %d carries no birth token; skipping the survived-Shutdown check", pf.Pid)
		return
	}
	token := procid.Token(pf.Birth)

	deadline := time.Now().Add(backendExitTimeout)
	for {
		same, err := procid.Verify(pf.Pid, token)
		if err != nil {
			t.Logf("procid.Verify(%d): %v", pf.Pid, err)
			return
		}
		if !same {
			// Either the process is gone, or the PID now belongs to
			// something else. Either way our server is not running.
			return
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(backendExitPoll)
	}

	handle, err := procid.Open(pf.Pid, token)
	if err != nil {
		if backendStillRunning(t, pf.Pid, token) {
			t.Errorf("dolt sql-server pid %d survived Shutdown by more than %s and could not be opened to kill: %v",
				pf.Pid, backendExitTimeout, err)
		}
		return
	}
	killErr := handle.Kill()
	_ = handle.Close()
	if killErr != nil {
		if backendStillRunning(t, pf.Pid, token) {
			t.Errorf("dolt sql-server pid %d survived Shutdown by more than %s and could not be killed: %v",
				pf.Pid, backendExitTimeout, killErr)
		}
		return
	}
	t.Errorf("dolt sql-server pid %d survived Shutdown by more than %s (force-killed)", pf.Pid, backendExitTimeout)
}

// backendStillRunning re-checks birth identity after procid.Open or
// Handle.Kill failed, and reports whether the recorded server is verifiably
// still there.
//
// Both calls verify the token themselves and return a plain "does not match
// token" error when it no longer does — which is exactly what a process that
// exited between the wait loop's last poll and the kill attempt produces, and
// that error is NOT procid.IsProcessGone. Without this second look, a server
// that shut down a few milliseconds late would be reported as one that
// survived Shutdown entirely. A verify that itself errors reports "not
// running": the run is over, and an inconclusive probe is not evidence of a
// leak.
func backendStillRunning(t *testing.T, pid int, token procid.Token) bool {
	t.Helper()
	same, err := procid.Verify(pid, token)
	if err != nil {
		t.Logf("procid.Verify(%d) after a failed kill attempt: %v", pid, err)
		return false
	}
	return same
}

// TestReconcileVersionPersistsAcrossUOW is the one version assertion that
// stays out of the conformance contract, because it is about this backend's
// TRANSACTION rather than about the role: a marker written inside a unit of
// work that is closed without a commit must not be there afterwards.
//
// The role cannot express that — every write through it commits — so the
// rolled-back leg drives the metadata seam directly, the same seam the role's
// body writes through. Everything else about version reconciliation is
// TestVersionReconcilerContract, which runs here and on the two store backends.
func TestReconcileVersionPersistsAcrossUOW(t *testing.T) {
	provider := newTestUOWProvider(t)
	ctx := context.Background()

	reconciler, err := NewVersionReconciler(provider)
	require.NoError(t, err)

	res, err := reconciler.ReconcileVersion(ctx, publicops.VersionReconcileRequest{CLIVersion: "0.5.0"})
	require.NoError(t, err)
	require.Equal(t, "", res.Previous)
	require.Equal(t, "0.5.0", res.Current)
	require.True(t, res.Migrated)

	res, err = reconciler.ReconcileVersion(ctx, publicops.VersionReconcileRequest{CLIVersion: "0.6.0"})
	require.NoError(t, err)
	require.Equal(t, "0.5.0", res.Previous, "a committed marker must persist into a new unit of work")
	require.True(t, res.Migrated)

	// Write the marker forward and abandon the unit of work.
	uw, err := provider.NewUOW(ctx)
	require.NoError(t, err)
	require.NoError(t, uw.ConfigUseCase().SetLocalMetadata(ctx, workapi.MetadataKeyVersion, "0.7.0"))
	uw.Close(ctx)

	recorded, err := reconciler.RecordedVersion(ctx, publicops.RecordedVersionRequest{})
	require.NoError(t, err)
	require.Equal(t, "0.6.0", recorded.Recorded, "a rolled-back marker write must not persist")
}
