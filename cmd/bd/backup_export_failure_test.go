package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
)

// failingBackupStore is a minimal DoltStorage stand-in whose Dolt-native
// backup always fails. It embeds a nil DoltStorage for identity/type
// assertion and overrides only the methods runBackupExport touches.
type failingBackupStore struct {
	storage.DoltStorage
	commit      string
	backupErr   error
	backupCalls int
}

func (f *failingBackupStore) GetCurrentCommit(context.Context) (string, error) { return f.commit, nil }

func (f *failingBackupStore) BackupDatabase(context.Context, string) error {
	f.backupCalls++
	return f.backupErr
}
func (f *failingBackupStore) BackupAdd(context.Context, string, string) error     { return nil }
func (f *failingBackupStore) BackupSync(context.Context, string) error            { return nil }
func (f *failingBackupStore) BackupRemove(context.Context, string) error          { return nil }
func (f *failingBackupStore) RestoreDatabase(context.Context, string, bool) error { return nil }

// TestRunBackupExport_PersistsThrottleOnFailure is the wy-zrmqr repro: a
// failing Dolt backup sync must still advance the throttle timestamp so the
// next bd command is throttled instead of retrying immediately. Before the
// fix, saveBackupState ran only on success, so a slow/failing shared Dolt
// server was hammered on EVERY subsequent command — the amplifier behind the
// 2026-07 CPU-pin incident.
func TestRunBackupExport_PersistsThrottleOnFailure(t *testing.T) {
	// Point backupDir() at a temp git repo via backup.git-repo so we don't
	// need a live beads workspace.
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BD_BACKUP_GIT_REPO", repo)
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}

	dir, err := backupDir()
	if err != nil {
		t.Fatalf("backupDir: %v", err)
	}

	// Sanity: no prior state, so the throttle would NOT block a first attempt.
	if st, _ := loadBackupState(dir); !st.Timestamp.IsZero() {
		t.Fatalf("precondition: expected zero timestamp, got %v", st.Timestamp)
	}

	oldStore := store
	fake := &failingBackupStore{commit: "deadbeef", backupErr: errors.New("sync to backup: server too busy")}
	store = fake
	t.Cleanup(func() { store = oldStore })

	before := time.Now().UTC()
	if _, err := runBackupExport(context.Background(), true); err == nil {
		t.Fatal("expected runBackupExport to return the sync error, got nil")
	}
	if fake.backupCalls != 1 {
		t.Fatalf("expected exactly 1 BackupDatabase call, got %d", fake.backupCalls)
	}

	st, err := loadBackupState(dir)
	if err != nil {
		t.Fatalf("loadBackupState: %v", err)
	}
	if st.Timestamp.IsZero() {
		t.Fatal("throttle timestamp not persisted on failure — next command would retry unthrottled (storm)")
	}
	if st.Timestamp.Before(before) {
		t.Errorf("throttle timestamp %v predates the failed attempt %v", st.Timestamp, before)
	}
	// The recorded timestamp must be recent enough that the default 15m
	// throttle window blocks the next attempt.
	if time.Since(st.Timestamp) >= 15*time.Minute {
		t.Errorf("persisted timestamp %v is already outside the throttle window", st.Timestamp)
	}
	// LastDoltCommit must stay empty: the backup did NOT succeed, so change
	// detection must still see pending work once the failure clears.
	if st.LastDoltCommit != "" {
		t.Errorf("LastDoltCommit should remain empty after a failed backup, got %q", st.LastDoltCommit)
	}
}
