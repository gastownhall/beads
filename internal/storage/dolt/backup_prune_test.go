package dolt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDoltStoreBackupSyncWithPrune exercises the SQL-server storage route
// against a real Dolt server. Dolt performs the prune, but its summary is
// printed in the server process and is not returned through SQL.
func TestDoltStoreBackupSyncWithPrune(t *testing.T) {
	store, cleanup := setupAnyServerStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	backupDir := filepath.Join(t.TempDir(), "backup")
	backupURL := "file://" + backupDir
	if err := store.BackupAdd(ctx, "default", backupURL); err != nil {
		t.Fatalf("BackupAdd: %v", err)
	}
	if err := store.BackupSync(ctx, "default"); err != nil {
		t.Fatalf("initial BackupSync: %v", err)
	}
	registeredURL, err := store.BackupRemoteURL(ctx, "default")
	if err != nil {
		t.Fatalf("BackupRemoteURL: %v", err)
	}
	if registeredURL != backupURL {
		t.Fatalf("registered backup URL = %q, want %q", registeredURL, backupURL)
	}

	orphan := filepath.Join(backupDir, "00000000000000000000000000000001")
	if err := os.WriteFile(orphan, []byte("orphaned table file"), 0o600); err != nil {
		t.Fatalf("seed aged orphan: %v", err)
	}
	ageBackupPruneDestination(t, backupDir, 20*time.Minute)

	outcome, err := store.BackupSyncWithPrune(ctx, "default", 10*time.Minute)
	if err != nil {
		t.Fatalf("BackupSyncWithPrune on aged orphan: %v", err)
	}
	if outcome.Status != "unavailable" || !strings.Contains(outcome.Message, "did not return a prune summary") {
		t.Fatalf("SQL-server prune outcome = %+v, want explicit unavailable status", outcome)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("SQL-server prune left the aged orphan in place; stat error: %v", err)
	}

	recent := filepath.Join(backupDir, "00000000000000000000000000000002")
	if err := os.WriteFile(recent, []byte("recent orphaned table file"), 0o600); err != nil {
		t.Fatalf("seed recent orphan: %v", err)
	}
	outcome, err = store.BackupSyncWithPrune(ctx, "default", 10*time.Minute)
	if err != nil {
		t.Fatalf("BackupSyncWithPrune on recent destination: %v", err)
	}
	if outcome.Status != "unavailable" || !strings.Contains(outcome.Message, "did not return a prune summary") {
		t.Fatalf("SQL-server recent-file prune outcome = %+v, want explicit unavailable status", outcome)
	}
	if _, err := os.Stat(recent); err != nil {
		t.Fatalf("SQL-server prune removed a recent destination file: %v", err)
	}
}

func ageBackupPruneDestination(t *testing.T, dir string, age time.Duration) {
	t.Helper()
	modified := time.Now().Add(-age)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read backup destination: %v", err)
	}
	for _, entry := range entries {
		if err := os.Chtimes(filepath.Join(dir, entry.Name()), modified, modified); err != nil {
			t.Fatalf("age backup entry %s: %v", entry.Name(), err)
		}
	}
}
