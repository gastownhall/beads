package main

import (
	"context"
	"encoding/json"
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

	fake := &failingBackupStore{commit: "deadbeef", backupErr: errors.New("sync to backup: server too busy")}

	before := time.Now().UTC()
	if _, err := runBackupExport(context.Background(), directLocalBackup{store: fake}, true); err == nil {
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

// backupExportTestDir points backupDir() at a throwaway git repo through
// backup.git-repo, so runBackupExport needs no live workspace, and returns
// the directory it resolves to.
func backupExportTestDir(t *testing.T) string {
	t.Helper()
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
	return dir
}

// wireBackupState is backup_state.json decoded by key, independent of
// backupState's Go fields.
type wireBackupState struct {
	LastDoltCommit string    `json:"last_dolt_commit"`
	Timestamp      time.Time `json:"timestamp"`
	LastCapWarnAt  time.Time `json:"last_cap_warn_at"`
	LastSuccessAt  time.Time `json:"last_success_at"`
	LastError      string    `json:"last_error"`
	LastErrorAt    time.Time `json:"last_error_at"`
}

func readWireBackupState(t *testing.T, dir string) wireBackupState {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "backup_state.json"))
	if err != nil {
		t.Fatalf("read backup state: %v", err)
	}
	var st wireBackupState
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatalf("decode backup state: %v\n%s", err, data)
	}
	return st
}

func writeWireBackupState(t *testing.T, dir string, raw map[string]any) {
	t.Helper()
	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "backup_state.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestRunBackupExport_RecordsSyncFailure is the bd-5mwx39 (GH#4070) repro at
// the state layer: a failed sync used to persist only the throttle
// timestamp, which `bd backup status` then printed as "Last backup", so a
// destination that failed for days read as backed up minutes ago. The
// failure must be recorded next to, not instead of, the last success.
func TestRunBackupExport_RecordsSyncFailure(t *testing.T) {
	const boom = "sync to backup: boom"

	t.Run("failure_after_success", func(t *testing.T) {
		dir := backupExportTestDir(t)
		fake := &failingBackupStore{commit: "c1"}
		if _, err := runBackupExport(context.Background(), directLocalBackup{store: fake}, true); err != nil {
			t.Fatalf("first backup: %v", err)
		}
		good := readWireBackupState(t, dir)

		fake.commit = "c2"
		fake.backupErr = errors.New(boom)
		before := time.Now().UTC()
		if _, err := runBackupExport(context.Background(), directLocalBackup{store: fake}, true); err == nil {
			t.Fatal("second backup: want the sync error, got nil")
		}
		after := time.Now().UTC()
		st := readWireBackupState(t, dir)

		if st.LastError != boom {
			t.Errorf("last_error = %q, want %q", st.LastError, boom)
		}
		if st.LastErrorAt.Before(before) || st.LastErrorAt.After(after) {
			t.Errorf("last_error_at = %v, want the attempt time in [%v, %v]", st.LastErrorAt, before, after)
		}
		if !st.Timestamp.Equal(st.LastErrorAt) {
			t.Errorf("timestamp = %v, want it advanced to the attempt time %v", st.Timestamp, st.LastErrorAt)
		}
		if st.LastDoltCommit != "c1" {
			t.Errorf("last_dolt_commit = %q, want the last good backup's %q", st.LastDoltCommit, "c1")
		}
		if !st.LastSuccessAt.Equal(good.Timestamp) {
			t.Errorf("last_success_at = %v, want the last good backup's time %v", st.LastSuccessAt, good.Timestamp)
		}
	})

	t.Run("success_after_failure_clears_error", func(t *testing.T) {
		dir := backupExportTestDir(t)
		fake := &failingBackupStore{commit: "c1", backupErr: errors.New(boom)}
		if _, err := runBackupExport(context.Background(), directLocalBackup{store: fake}, true); err == nil {
			t.Fatal("first backup: want the sync error, got nil")
		}
		if st := readWireBackupState(t, dir); st.LastError != boom {
			t.Fatalf("precondition: last_error = %q, want %q", st.LastError, boom)
		}

		fake.commit = "c2"
		fake.backupErr = nil
		if _, err := runBackupExport(context.Background(), directLocalBackup{store: fake}, true); err != nil {
			t.Fatalf("second backup: %v", err)
		}
		st := readWireBackupState(t, dir)
		if st.LastError != "" {
			t.Errorf("last_error = %q after a successful backup, want empty", st.LastError)
		}
		if !st.LastErrorAt.IsZero() {
			t.Errorf("last_error_at = %v after a successful backup, want zero", st.LastErrorAt)
		}
		if st.Timestamp.IsZero() || !st.LastSuccessAt.Equal(st.Timestamp) {
			t.Errorf("last_success_at = %v, want the new timestamp %v", st.LastSuccessAt, st.Timestamp)
		}
		if st.LastDoltCommit != "c2" {
			t.Errorf("last_dolt_commit = %q, want %q", st.LastDoltCommit, "c2")
		}
	})

	t.Run("legacy_state_leaves_last_success_unknown", func(t *testing.T) {
		// The state shape every release since v1.2.0 writes, here after a
		// failed sync: no last_success_at, and a timestamp that failed sync
		// advanced. It is the failure's time, so the first failure under
		// this bd must not record it as the last success.
		dir := backupExportTestDir(t)
		failedAt := time.Now().UTC().Add(-20 * time.Minute).Truncate(time.Second)
		writeWireBackupState(t, dir, map[string]any{
			"last_dolt_commit": "c0",
			"timestamp":        failedAt,
			"last_cap_warn_at": time.Time{},
		})
		fake := &failingBackupStore{commit: "c1", backupErr: errors.New(boom)}
		if _, err := runBackupExport(context.Background(), directLocalBackup{store: fake}, true); err == nil {
			t.Fatal("want the sync error, got nil")
		}
		st := readWireBackupState(t, dir)
		if !st.LastSuccessAt.IsZero() {
			t.Errorf("last_success_at = %v, want zero: the older bd's timestamp %v was a failed attempt", st.LastSuccessAt, failedAt)
		}
		if st.LastError != boom {
			t.Errorf("last_error = %q, want %q", st.LastError, boom)
		}
		if st.LastDoltCommit != "c0" {
			t.Errorf("last_dolt_commit = %q, want %q", st.LastDoltCommit, "c0")
		}
		if !st.Timestamp.After(failedAt) || !st.Timestamp.Equal(st.LastErrorAt) {
			t.Errorf("timestamp = %v, want this attempt's time %v (after %v)", st.Timestamp, st.LastErrorAt, failedAt)
		}
	})

	t.Run("never_succeeded_keeps_last_success_zero", func(t *testing.T) {
		dir := backupExportTestDir(t)
		fake := &failingBackupStore{commit: "c1", backupErr: errors.New(boom)}
		for i := 0; i < 2; i++ {
			if _, err := runBackupExport(context.Background(), directLocalBackup{store: fake}, true); err == nil {
				t.Fatalf("attempt %d: want the sync error, got nil", i+1)
			}
		}
		st := readWireBackupState(t, dir)
		if !st.LastSuccessAt.IsZero() {
			t.Errorf("last_success_at = %v, want zero: no backup ever succeeded", st.LastSuccessAt)
		}
		if st.LastError != boom {
			t.Errorf("last_error = %q, want %q", st.LastError, boom)
		}
	})
}
