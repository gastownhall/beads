package main

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/steveyegge/beads/internal/configfile"
)

// plainDirMigrationJournal builds a journal that loadMigrateJournal accepts
// for beadsDir: a managed-local server-to-proxied-server transition whose
// sidecar root resolves to the journal's own root_path.
func plainDirMigrationJournal(t *testing.T, beadsDir string, phase migratePhase, attempt int) *migrateJournal {
	t.Helper()
	return &migrateJournal{
		Version:    1,
		SourceMode: configfile.DoltModeServer,
		TargetMode: configfile.DoltModeProxiedServer,
		RootPath:   filepath.Join(beadsDir, "dolt"),
		Sidecar:    &configfile.ProxiedServerClientInfo{RootPath: "dolt"},
		Ownership:  "managed-local",
		Attempt:    attempt,
		Phase:      phase,
	}
}

// TestMigrateJournal_SaveAndLoadOnPlainDirectory pins the Windows outcome of
// GH#6882: saving a valid journal must succeed even though Windows cannot
// flush a directory handle, and a saved journal must load back unchanged.
func TestMigrateJournal_SaveAndLoadOnPlainDirectory(t *testing.T) {
	beadsDir := t.TempDir()
	j := plainDirMigrationJournal(t, beadsDir, migratePrepared, 1)
	require.NoError(t, saveMigrateJournal(beadsDir, j))
	loaded, err := loadMigrateJournal(beadsDir)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Equal(t, j, loaded)

	j.Phase = migrateTargetConfigured
	j.Attempt = 2
	require.NoError(t, saveMigrateJournal(beadsDir, j))
	loaded, err = loadMigrateJournal(beadsDir)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Equal(t, j, loaded)

	residue, err := filepath.Glob(filepath.Join(beadsDir, ".dolt-mode-migration-*.tmp"))
	require.NoError(t, err)
	assert.Empty(t, residue, "temporary journal files must not survive a successful save")
}
