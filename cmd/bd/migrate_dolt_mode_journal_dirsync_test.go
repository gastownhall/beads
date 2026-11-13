package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
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

// TestMigrateJournal_RemoveOnPlainDirectory writes the journal file directly,
// so a failing save cannot mask the removal regression (GH#6882): removing
// the journal must succeed even though Windows cannot flush a directory
// handle, and removing an absent journal stays a no-op.
func TestMigrateJournal_RemoveOnPlainDirectory(t *testing.T) {
	beadsDir := t.TempDir()
	j := plainDirMigrationJournal(t, beadsDir, migrateVerified, 1)
	b, err := json.MarshalIndent(j, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(migrateJournalPath(beadsDir), b, 0o600))

	require.NoError(t, removeMigrateJournal(beadsDir))
	_, err = os.Stat(migrateJournalPath(beadsDir))
	require.True(t, os.IsNotExist(err), "journal still present after removal: %v", err)

	require.NoError(t, removeMigrateJournal(beadsDir), "removing an absent journal must stay a no-op")
}

// TestMigrateJournalSaveAndRemoveSucceedOnPlainDirectory pins that persisting
// and removing the migration journal works on every platform. On Windows an
// os.Open'd directory cannot be Sync'd (FlushFileBuffers: Access is denied),
// so an unconditional directory Sync fails both operations (GH#6882).
func TestMigrateJournalSaveAndRemoveSucceedOnPlainDirectory(t *testing.T) {
	beadsDir := t.TempDir()
	j := &migrateJournal{SourceMode: "embedded", TargetMode: "server"}
	if err := saveMigrateJournal(beadsDir, j); err != nil {
		t.Fatalf("saveMigrateJournal: %v", err)
	}
	if _, err := os.Stat(migrateJournalPath(beadsDir)); err != nil {
		t.Fatalf("journal not written: %v", err)
	}
	if err := removeMigrateJournal(beadsDir); err != nil {
		t.Fatalf("removeMigrateJournal: %v", err)
	}
	if _, err := os.Stat(migrateJournalPath(beadsDir)); !os.IsNotExist(err) {
		t.Fatalf("journal still present after remove: %v", err)
	}
}

// TestSyncMigrateJournalDir_SkipsOnlyWindows pins the helper's branch
// selection directly: only Windows skips the directory flush; every other OS
// still calls Sync and propagates its error. A closed handle stands in for a
// real flush failure, so the propagated error is os.ErrClosed on every
// platform.
func TestSyncMigrateJournalDir_SkipsOnlyWindows(t *testing.T) {
	d, err := os.Open(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, d.Close())

	require.NoError(t, syncMigrateJournalDir("windows", d), "windows must skip the unsupported directory flush")
	for _, goos := range []string{"linux", "darwin", "freebsd"} {
		err := syncMigrateJournalDir(goos, d)
		require.Error(t, err, "%s must still flush the directory", goos)
		assert.ErrorIs(t, err, os.ErrClosed, "%s flush error must propagate", goos)
	}

	fresh, err := os.Open(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = fresh.Close() })
	require.NoError(t, syncMigrateJournalDir(runtime.GOOS, fresh))
}

// TestMigrateJournal_FilesystemErrorsStillReported pins that the Windows fix
// narrows only the directory flush: every other filesystem failure in journal
// persistence keeps its existing error and cleanup behavior, before and after
// the fix.
func TestMigrateJournal_FilesystemErrorsStillReported(t *testing.T) {
	t.Run("save into missing directory", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "missing")
		j := plainDirMigrationJournal(t, missing, migratePrepared, 1)
		err := saveMigrateJournal(missing, j)
		require.Error(t, err)
		assert.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("remove in missing directory", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "missing")
		err := removeMigrateJournal(missing)
		require.Error(t, err)
		assert.ErrorIs(t, err, os.ErrNotExist)
		assert.Contains(t, err.Error(), "opening migration directory: ")
	})

	t.Run("journal path occupied by a directory", func(t *testing.T) {
		beadsDir := t.TempDir()
		occupied := migrateJournalPath(beadsDir)
		require.NoError(t, os.Mkdir(occupied, 0o755))
		sentinel := []byte("journal-path-sentinel")
		require.NoError(t, os.WriteFile(filepath.Join(occupied, "keep"), sentinel, 0o600))

		j := plainDirMigrationJournal(t, beadsDir, migratePrepared, 1)
		err := saveMigrateJournal(beadsDir, j)
		require.Error(t, err, "renaming onto a directory must fail")
		got, readErr := os.ReadFile(filepath.Join(occupied, "keep"))
		require.NoError(t, readErr)
		assert.Equal(t, sentinel, got, "failed save must not touch the occupied journal path")

		residue, globErr := filepath.Glob(filepath.Join(beadsDir, ".dolt-mode-migration-*.tmp"))
		require.NoError(t, globErr)
		assert.Empty(t, residue, "failed save must clean its temporary file")

		err = removeMigrateJournal(beadsDir)
		require.Error(t, err, "removing a non-empty directory must fail")
		got, readErr = os.ReadFile(filepath.Join(occupied, "keep"))
		require.NoError(t, readErr)
		assert.Equal(t, sentinel, got, "failed removal must not touch the occupied journal path")
	})
}
