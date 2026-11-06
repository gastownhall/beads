package proxy_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage/dbproxy/identity"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/storage/dbproxy/server"
	"github.com/steveyegge/beads/internal/storage/dbproxy/util"
	"github.com/stretchr/testify/require"
)

// prepareControlFiles creates every control file of root, including the
// rotated log and both kinds of PID quarantine.
func prepareControlFiles(t *testing.T, root string) {
	t.Helper()
	for _, name := range []string{
		proxy.PIDFileName + ".stale-100",
		server.PIDFileName + ".stale-200",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte("stale"), 0o600))
	}
	_, err := identity.WriteSecret(root)
	require.NoError(t, err)
	for _, path := range proxy.ControlFilePaths(root) {
		if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
			require.NoError(t, os.WriteFile(path, []byte("control"), 0o600))
		}
	}
}

// TestPurgeControlFilesRetiresHeldLeases mirrors runMigrateFromProxiedServer:
// it holds the proxy and proxy-child lifecycle leases and retires every
// control file before releasing them (#6884). Windows refuses a pathname
// removal of a file the caller itself holds open.
func TestPurgeControlFilesRetiresHeldLeases(t *testing.T) {
	root := t.TempDir()
	prepareControlFiles(t, root)

	proxyLease, err := util.TryRetirableLock(filepath.Join(root, proxy.LockFileName))
	require.NoError(t, err)
	childLease, err := util.TryRetirableLock(filepath.Join(root, server.LockFileName))
	require.NoError(t, err)
	paths := proxy.ControlFilePaths(root)
	require.Len(t, paths, 11, "every control, rotation and quarantine prepared")

	errs := proxy.PurgeControlFiles(root, proxyLease, childLease)
	require.Empty(t, errs, "retiring controls while the migration holds its leases")

	childLease.Unlock()
	proxyLease.Unlock()
	for _, path := range paths {
		_, statErr := os.Lstat(path)
		require.True(t, os.IsNotExist(statErr), "%s still present after purge: %v", path, statErr)
	}
}
