package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/steveyegge/beads/internal/configfile"
)

// faultMigrationAtPrepared runs a fresh proxied→server migration that faults
// right after journaling, and returns the workspace with its recoverable
// journal. The caller rewrites the journal's log_assets — the state a
// tampered journal leaves behind — before resuming (bd-1tmym4 / #7888).
func faultMigrationAtPrepared(t *testing.T) (beadsDir, rootDir string, j *migrateJournal) {
	t.Helper()
	beadsDir = migrateModeWorkspace(t, configfile.DoltModeProxiedServer)
	rootDir = filepath.Join(beadsDir, "dolt")
	require.NoError(t, configfile.SaveProxiedServerClientInfo(beadsDir, &configfile.ProxiedServerClientInfo{RootPath: rootDir}))

	touchFile(t, filepath.Join(rootDir, "server.log"))
	touchFile(t, filepath.Join(rootDir, "server.log.1"))

	t.Setenv("BEADS_MIGRATION_FAIL_PHASE", string(migratePrepared))
	require.Error(t, runMigrateFromProxiedServer(false, false))
	t.Setenv("BEADS_MIGRATION_FAIL_PHASE", "")

	var err error
	j, err = loadMigrateJournal(beadsDir)
	require.NoError(t, err)
	require.NotNil(t, j)
	return beadsDir, rootDir, j
}

// TestMigrationLogAssetsStayConfined: a migration resumed from a committed
// journal removes only the two eligible proxied-server logs under the
// migration root; journal log_assets pointing anywhere else are skipped
// with a warning, survive untouched, and the migration still succeeds
// (bd-1tmym4 / #7888).
func TestMigrationLogAssetsStayConfined(t *testing.T) {
	beadsDir, rootDir, j := faultMigrationAtPrepared(t)

	// Unsafe journal assets: outside the root, and inside .beads but not an
	// expected log. Both must survive the resumed migration.
	outsideVictim := filepath.Join(t.TempDir(), "outside.log")
	touchFile(t, outsideVictim)
	innocentVictim := filepath.Join(beadsDir, "innocent.log")
	touchFile(t, innocentVictim)
	j.LogAssets = []string{
		filepath.Join(rootDir, "server.log"),
		filepath.Join(rootDir, "server.log.1"),
		innocentVictim,
		outsideVictim,
	}
	require.NoError(t, saveMigrateJournal(beadsDir, j))

	// The dry-run lists the same filtered assets: the eligible logs only.
	dryRunOut, dryRunErr := captureMigrationJSON(t, func() error {
		return runMigrateFromProxiedServer(true, false)
	})
	require.NoError(t, dryRunErr)
	assert.Contains(t, dryRunOut, filepath.Join(rootDir, "server.log"))
	assert.NotContains(t, dryRunOut, innocentVictim, "dry-run must not list a non-eligible log asset")
	assert.NotContains(t, dryRunOut, outsideVictim, "dry-run must not list a log asset outside the root")

	var migrateErr error
	stderr := captureStderr(t, func() {
		migrateErr = runMigrateFromProxiedServer(false, false)
	})
	require.NoError(t, migrateErr)

	for _, p := range []string{filepath.Join(rootDir, "server.log"), filepath.Join(rootDir, "server.log.1")} {
		_, err := os.Stat(p)
		assert.True(t, os.IsNotExist(err), "eligible log %s must be removed", p)
	}
	for _, p := range []string{innocentVictim, outsideVictim} {
		assert.Equal(t, "x", string(mustReadFile(t, p)), "unsafe log asset %s must survive", p)
	}
	assert.Contains(t, stderr, innocentVictim, "the warning must name the skipped asset")
	assert.Contains(t, stderr, outsideVictim, "the warning must name the skipped asset")

	cfg, err := configfile.Load(beadsDir)
	require.NoError(t, err)
	assert.True(t, cfg.IsDoltServerMode())
}

// TestMigrationLogAssetsRejectSymlinkDirectory: a leaf symlink standing
// where an eligible log should be is skipped, not removed, and its target
// is never touched (bd-1tmym4 / #7888).
func TestMigrationLogAssetsRejectSymlinkDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixtures need privileges on windows")
	}

	beadsDir, rootDir, j := faultMigrationAtPrepared(t)

	victim := filepath.Join(beadsDir, "victim.log")
	require.NoError(t, os.WriteFile(victim, []byte("original\n"), 0o600))
	require.NoError(t, os.Remove(filepath.Join(rootDir, "server.log")))
	require.NoError(t, os.Symlink(victim, filepath.Join(rootDir, "server.log")))
	j.LogAssets = []string{
		filepath.Join(rootDir, "server.log"),
		filepath.Join(rootDir, "server.log.1"),
	}
	require.NoError(t, saveMigrateJournal(beadsDir, j))

	var migrateErr error
	stderr := captureStderr(t, func() {
		migrateErr = runMigrateFromProxiedServer(false, false)
	})
	require.NoError(t, migrateErr)

	assert.Equal(t, "original\n", string(mustReadFile(t, victim)), "the symlink's target must survive")
	fi, lerr := os.Lstat(filepath.Join(rootDir, "server.log"))
	require.NoError(t, lerr)
	assert.NotZero(t, fi.Mode()&os.ModeSymlink, "the leaf symlink itself must be skipped, not removed")
	assert.Contains(t, stderr, "not a regular file", "the warning must name the skipped asset")

	// The other eligible log is still removed.
	_, err := os.Stat(filepath.Join(rootDir, "server.log.1"))
	assert.True(t, os.IsNotExist(err), "eligible rotated log must be removed")

	cfg, err := configfile.Load(beadsDir)
	require.NoError(t, err)
	assert.True(t, cfg.IsDoltServerMode())
	assert.False(t, strings.Contains(stderr, "failed to retire"))
}