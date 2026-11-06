package proxy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/steveyegge/beads/internal/storage/dbproxy/identity"
	"github.com/steveyegge/beads/internal/storage/dbproxy/pidfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/server"
	"github.com/steveyegge/beads/internal/storage/dbproxy/util"
)

// prepareControls creates every non-lock control file the purge owns.
func prepareControls(t *testing.T, root string) {
	t.Helper()
	proxyStale := pidfile.Path(root, PIDFileName) + ".stale-100"
	backendStale := pidfile.Path(root, server.PIDFileName) + ".stale-200"
	rotatedLog := filepath.Join(root, LogFileName+".1")
	require.NoError(t, os.WriteFile(proxyStale, []byte("proxy"), 0o600))
	require.NoError(t, os.WriteFile(backendStale, []byte("backend"), 0o600))
	require.NoError(t, os.WriteFile(rotatedLog, []byte("rotated"), 0o600))
	_, err := identity.WriteSecret(root)
	require.NoError(t, err)
}

// TestPurgeControlFilesRetiresHeldLeases is the purge half of the reverse
// migration's lock retirement (#6884): every control file, the two held
// lifecycle lock files included, is retired while both leases stay held,
// and unlocking afterwards is clean.
func TestPurgeControlFilesRetiresHeldLeases(t *testing.T) {
	root := t.TempDir()
	prepareControls(t, root)

	proxyLock, err := util.TryRetirableLock(filepath.Join(root, LockFileName))
	require.NoError(t, err)
	childLock, err := util.TryRetirableLock(filepath.Join(root, server.LockFileName))
	require.NoError(t, err)

	assert.Empty(t, PurgeControlFiles(root, proxyLock, childLock))

	proxyLock.Unlock()
	childLock.Unlock()
	for _, path := range ControlFilePaths(root) {
		_, statErr := os.Stat(path)
		assert.True(t, os.IsNotExist(statErr), "%s still present after purge: %v", path, statErr)
	}
}

// TestPurgeControlFiles_RetiresLocksHeldByReverseMigration mirrors
// runMigrateFromProxiedServer: the proxy and proxy-child lifecycle leases
// are held while the control files are retired, and released only
// afterwards (#6884).
func TestPurgeControlFiles_RetiresLocksHeldByReverseMigration(t *testing.T) {
	root := t.TempDir()

	proxyLock, err := util.TryRetirableLock(filepath.Join(root, LockFileName))
	require.NoError(t, err)
	childLock, err := util.TryRetirableLock(filepath.Join(root, server.LockFileName))
	require.NoError(t, err)

	errs := PurgeControlFiles(root, proxyLock, childLock)
	require.Empty(t, errs, "retiring controls while the migration holds its leases")

	proxyLock.Unlock()
	childLock.Unlock()
	for _, name := range []string{LockFileName, server.LockFileName} {
		_, statErr := os.Stat(filepath.Join(root, name))
		require.True(t, os.IsNotExist(statErr), "%s still present after purge: %v", name, statErr)
	}
}

// TestPurgeControlFilesRejectsMisusedLeases covers the lease validation:
// nil leases, leases outside the control set, and duplicate leases for one
// path are rejected before anything is deleted.
func TestPurgeControlFilesRejectsMisusedLeases(t *testing.T) {
	root := t.TempDir()
	prepareControls(t, root)
	secretPath := filepath.Join(root, identity.SecretFileName)

	t.Run("nil lease", func(t *testing.T) {
		errs := PurgeControlFiles(root, nil)
		require.Len(t, errs, 1)
		assert.FileExists(t, secretPath, "nothing may be deleted when a lease is rejected")
	})

	t.Run("lease outside the control set", func(t *testing.T) {
		outside, err := util.TryRetirableLock(filepath.Join(root, "unrelated.lock"))
		require.NoError(t, err)
		defer outside.Unlock()
		errs := PurgeControlFiles(root, outside)
		require.Len(t, errs, 1)
		assert.FileExists(t, secretPath, "nothing may be deleted when a lease is rejected")
	})

	t.Run("duplicate leases for one path", func(t *testing.T) {
		lease, err := util.TryRetirableLock(filepath.Join(root, LockFileName))
		require.NoError(t, err)
		defer lease.Unlock()
		errs := PurgeControlFiles(root, lease, lease)
		require.Len(t, errs, 1)
		assert.FileExists(t, secretPath, "nothing may be deleted when a lease is rejected")
	})
}

// TestPurgeControlFilesDoesNotFallbackWhenRetireRefuses pins the no-fallback
// rule: a matching control path held by an ordinary (non-retirable) lease
// reports the retirement refusal instead of removing the path behind the
// lease's back.
func TestPurgeControlFilesDoesNotFallbackWhenRetireRefuses(t *testing.T) {
	root := t.TempDir()
	ordinary, err := util.TryLock(filepath.Join(root, LockFileName))
	require.NoError(t, err)
	defer ordinary.Unlock()

	errs := PurgeControlFiles(root, ordinary)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0].Error(), "retirable")
	assert.FileExists(t, filepath.Join(root, LockFileName), "a refused retirement must not delete the path")
}
