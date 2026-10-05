package uow

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/steveyegge/beads/internal/workapi"
	publicops "github.com/steveyegge/beads/issueops"
)

func openTestUOWProvider(t *testing.T, bin string) (UnitOfWorkProvider, error) {
	t.Helper()
	port, err := proxy.PickFreePort()
	if err != nil {
		return nil, err
	}
	storeRootDir := t.TempDir()
	shutdownOnInterrupt(t, storeRootDir)
	verifiedShutdownCleanup(t, storeRootDir)
	cfgPath := writeServerConfig(t, port)
	logPath := filepath.Join(t.TempDir(), "server.log")
	return NewDoltServerUOWProvider(
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
}

func localDoltNeverListened(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "exited before listener became ready")
}

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

	provider, err := openTestUOWProvider(t, bin)
	// This helper starts a private dolt. When that process exits before it
	// listens, the open fails with connection refused and the test is the
	// only red one (Main, 2026-09-26,
	// TestIssueOperationsCreateRoutesInfraTypesToWisps). A second server on
	// a new port is the same start the next test would have done anyway.
	if err != nil && localDoltNeverListened(err) {
		t.Logf("local dolt did not accept connections (%v); starting another", err)
		provider, err = openTestUOWProvider(t, bin)
	}
	require.NoError(t, err)
	require.NotNil(t, provider)
	t.Cleanup(func() { _ = provider.Close(context.Background()) })
	return provider
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
