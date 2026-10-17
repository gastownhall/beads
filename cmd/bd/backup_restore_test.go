//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/testutil"
)

// TestValidateBackupRestoreDir pins GH#7098/GH#5972: an existing-but-empty
// directory (or any directory without a Dolt manifest) must be rejected
// before a restore ever reaches CALL DOLT_BACKUP('restore', ...), which
// drops the target database before discovering the source is invalid. Pure
// filesystem logic, no Dolt server needed.
func TestValidateBackupRestoreDir(t *testing.T) {
	t.Run("missing directory", func(t *testing.T) {
		err := validateBackupRestoreDir(filepath.Join(t.TempDir(), "does-not-exist"))
		if err == nil {
			t.Fatal("expected error for nonexistent backup dir")
		}
	})

	t.Run("existing but empty directory is rejected, not silently accepted", func(t *testing.T) {
		dir := t.TempDir()
		err := validateBackupRestoreDir(dir)
		if err == nil {
			t.Fatal("expected error for a directory with no Dolt manifest")
		}
		if !strings.Contains(err.Error(), "does not look like a Dolt backup") {
			t.Fatalf("validateBackupRestoreDir error = %q, want manifest-missing wording", err)
		}
	})

	t.Run("path is a file, not a directory", func(t *testing.T) {
		dir := t.TempDir()
		file := filepath.Join(dir, "not-a-dir")
		if err := os.WriteFile(file, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
		err := validateBackupRestoreDir(file)
		if err == nil {
			t.Fatal("expected error for a file path")
		}
	})

	t.Run("directory with a manifest is accepted", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "manifest"), []byte("fake"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := validateBackupRestoreDir(dir); err != nil {
			t.Fatalf("validateBackupRestoreDir on a real-looking backup dir: %v", err)
		}
	})
}

func TestBackupRestoreMissingDir(t *testing.T) {
	if testDoltServerPort == 0 {
		t.Skip("Dolt test server not available")
	}
	if testutil.DoltContainerCrashed() {
		t.Skipf("Dolt test server crashed: %v", testutil.DoltContainerCrashError())
	}

	ensureTestMode(t)

	dbName := uniqueTestDBName(t)
	testDBPath := filepath.Join(t.TempDir(), "dolt")
	writeTestMetadata(t, testDBPath, dbName)
	s := newTestStoreWithPrefix(t, testDBPath, "dn")
	t.Cleanup(func() { _ = s.Close() })

	ctx := context.Background()

	err := runBackupRestore(ctx, s, "/nonexistent/path", false)
	if err == nil {
		t.Error("expected error for nonexistent backup dir")
	}
}

func TestSyncProjectIDFromDB_NoWorkspaceUsesActiveWorkspaceError(t *testing.T) {
	if testDoltServerPort == 0 {
		t.Skip("Dolt test server not available")
	}
	if testutil.DoltContainerCrashed() {
		t.Skipf("Dolt test server crashed: %v", testutil.DoltContainerCrashError())
	}

	ensureTestMode(t)

	ctx := context.Background()
	repoDir := t.TempDir()
	testDBPath := filepath.Join(repoDir, ".beads", "beads.db")
	s := newTestStoreWithPrefix(t, testDBPath, "bp")
	t.Cleanup(func() { _ = s.Close() })

	if err := s.SetMetadata(ctx, "_project_id", "project-123"); err != nil {
		t.Fatalf("SetMetadata(_project_id): %v", err)
	}

	noWorkspaceDir := t.TempDir()
	t.Chdir(noWorkspaceDir)
	t.Setenv("BEADS_DIR", "")
	t.Setenv("BEADS_DB", "")

	beads.ResetCaches()
	git.ResetCaches()
	t.Cleanup(func() {
		beads.ResetCaches()
		git.ResetCaches()
	})

	err := syncProjectIDFromDB(ctx, s)
	if err == nil {
		t.Fatal("expected syncProjectIDFromDB to fail without an active workspace")
	}
	if !strings.Contains(err.Error(), activeWorkspaceNotFoundError()) {
		t.Fatalf("syncProjectIDFromDB error = %q, want active workspace wording", err)
	}
	if !strings.Contains(err.Error(), diagHint()) {
		t.Fatalf("syncProjectIDFromDB error = %q, want diag hint", err)
	}
	if _, statErr := os.Stat(filepath.Join(noWorkspaceDir, ".beads")); !os.IsNotExist(statErr) {
		t.Fatalf("syncProjectIDFromDB should not create local .beads, stat err = %v", statErr)
	}
}
