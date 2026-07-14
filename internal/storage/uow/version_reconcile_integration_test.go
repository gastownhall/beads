package uow

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/testutil"
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
		if err := proxy.Shutdown(storeRootDir); err != nil {
			t.Logf("proxy.Shutdown(%s): %v", storeRootDir, err)
		}
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
	)
	require.NoError(t, err)
	require.NotNil(t, provider)
	t.Cleanup(func() { _ = provider.Close(context.Background()) })
	return provider
}

func TestReconcileVersionPersistsAcrossUOW(t *testing.T) {
	provider := newTestUOWProvider(t)
	ctx := context.Background()

	reconcileCommitted := func(cliVersion string) domain.VersionReconcileResult {
		uw, err := provider.NewUOW(ctx)
		require.NoError(t, err)
		defer uw.Close(ctx)
		res, err := uw.ConfigUseCase().ReconcileVersion(ctx, cliVersion)
		require.NoError(t, err)
		err = uw.Commit(ctx, "bd: reconcile version")
		if err != nil && !strings.Contains(strings.ToLower(err.Error()), "nothing to commit") {
			require.NoError(t, err)
		}
		return res
	}

	r := reconcileCommitted("0.5.0")
	require.Equal(t, "", r.Previous)
	require.Equal(t, "0.5.0", r.Current)
	require.True(t, r.Migrated)

	r = reconcileCommitted("0.5.0")
	require.Equal(t, "0.5.0", r.Previous, "committed bd_version must persist into a new UOW")
	require.False(t, r.Migrated)

	r = reconcileCommitted("0.6.0")
	require.Equal(t, "0.5.0", r.Previous)
	require.Equal(t, "0.6.0", r.Current)
	require.True(t, r.Migrated)

	uw, err := provider.NewUOW(ctx)
	require.NoError(t, err)
	res, err := uw.ConfigUseCase().ReconcileVersion(ctx, "0.7.0")
	require.NoError(t, err)
	require.True(t, res.Migrated)
	uw.Close(ctx)

	r = reconcileCommitted("0.7.0")
	require.Equal(t, "0.6.0", r.Previous, "rolled-back reconcile must not persist")
	require.True(t, r.Migrated)
}
