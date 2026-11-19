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

// backupExportTestDir points backupDir() at a throwaway git repo (the
// TestRunBackupExport_PersistsThrottleOnFailure trick) and returns the backup
// directory, so runBackupExport runs without a live beads workspace.
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

// rawBackupState is backup_state.json decoded through a test-local struct
// rather than backupState: the fields below are what GH#4070 (bd-5mwx39) adds,
// and a local struct keeps this test compiling — and failing on the zero
// values a base build writes — before they exist.
type rawBackupState struct {
	LastDoltCommit string    `json:"last_dolt_commit"`
	Timestamp      time.Time `json:"timestamp"`
	LastCapWarnAt  time.Time `json:"last_cap_warn_at"`
	LastSuccessAt  time.Time `json:"last_success_at"`
	LastError      string    `json:"last_error"`
	LastErrorAt    time.Time `json:"last_error_at"`
}

func readRawBackupState(t *testing.T, dir string) rawBackupState {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "backup_state.json"))
	if err != nil {
		t.Fatalf("read backup_state.json in %s: %v", dir, err)
	}
	var s rawBackupState
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("parse backup_state.json (%s): %v", data, err)
	}
	return s
}

// TestRunBackupExport_RecordsSyncFailure is the state half of GH#4070
// (bd-5mwx39): a failed sync must be recorded as a failure, separate from the
// throttle watermark the 2026-07 storm fix persists. Without it, every
// further attempt can die while `bd backup status` keeps advertising the
// timestamp of the last attempt as a backup — the silent 16-day outage in
// the report.
func TestRunBackupExport_RecordsSyncFailure(t *testing.T) {
	t.Run("failure_after_success", func(t *testing.T) {
		dir := backupExportTestDir(t)
		fake := &failingBackupStore{commit: "c1"}
		if _, err := runBackupExport(context.Background(), directLocalBackup{store: fake}, true); err != nil {
			t.Fatalf("first (successful) runBackupExport: %v", err)
		}
		success := readRawBackupState(t, dir)

		fake.backupErr = errors.New("sync to backup: boom")
		attempt := time.Now().UTC()
		if _, err := runBackupExport(context.Background(), directLocalBackup{store: fake}, true); err == nil {
			t.Fatal("expected runBackupExport to return the sync error, got nil")
		}
		after := readRawBackupState(t, dir)

		if after.LastError != "sync to backup: boom" {
			t.Errorf("last_error = %q, want the sync error's text", after.LastError)
		}
		if after.LastErrorAt.Before(attempt) {
			t.Errorf("last_error_at %v predates the failed attempt %v", after.LastErrorAt, attempt)
		}
		if !after.LastSuccessAt.Equal(success.Timestamp) {
			t.Errorf("last_success_at = %v, want the earlier success's time %v", after.LastSuccessAt, success.Timestamp)
		}
		if after.LastDoltCommit != success.LastDoltCommit {
			t.Errorf("last_dolt_commit = %q, want unchanged %q", after.LastDoltCommit, success.LastDoltCommit)
		}
		if after.Timestamp.Before(attempt) {
			t.Errorf("timestamp %v predates the failed attempt %v; the throttle watermark must still advance", after.Timestamp, attempt)
		}
	})

	t.Run("success_after_failure_clears_the_error", func(t *testing.T) {
		dir := backupExportTestDir(t)
		fake := &failingBackupStore{commit: "c1", backupErr: errors.New("sync to backup: boom")}
		if _, err := runBackupExport(context.Background(), directLocalBackup{store: fake}, true); err == nil {
			t.Fatal("expected the first runBackupExport to fail")
		}
		if failed := readRawBackupState(t, dir); failed.LastError == "" {
			t.Fatal("precondition: the failed attempt recorded no last_error")
		}

		fake.backupErr = nil
		if _, err := runBackupExport(context.Background(), directLocalBackup{store: fake}, true); err != nil {
			t.Fatalf("runBackupExport after the failure cleared: %v", err)
		}
		after := readRawBackupState(t, dir)
		if after.LastError != "" {
			t.Errorf("last_error = %q after a successful sync, want empty", after.LastError)
		}
		if !after.LastErrorAt.IsZero() {
			t.Errorf("last_error_at = %v after a successful sync, want zero", after.LastErrorAt)
		}
		if !after.LastSuccessAt.Equal(after.Timestamp) {
			t.Errorf("last_success_at = %v, want the new successful attempt's time %v", after.LastSuccessAt, after.Timestamp)
		}
	})

	t.Run("legacy_state_carries_timestamp_into_last_success_at", func(t *testing.T) {
		dir := backupExportTestDir(t)
		// A state file written by an older bd: no failure keys, but a
		// recorded backup whose watermark is the last success.
		legacy := `{"last_dolt_commit": "c0", "timestamp": "2026-11-01T00:00:00Z"}`
		if err := os.WriteFile(filepath.Join(dir, "backup_state.json"), []byte(legacy), 0o600); err != nil {
			t.Fatal(err)
		}
		fake := &failingBackupStore{commit: "c1", backupErr: errors.New("boom")}
		if _, err := runBackupExport(context.Background(), directLocalBackup{store: fake}, true); err == nil {
			t.Fatal("expected runBackupExport to fail")
		}
		after := readRawBackupState(t, dir)
		want, _ := time.Parse(time.RFC3339, "2026-11-01T00:00:00Z")
		if !after.LastSuccessAt.Equal(want) {
			t.Errorf("last_success_at = %v, want the legacy state carried from timestamp %v", after.LastSuccessAt, want)
		}
		if after.LastError != "boom" {
			t.Errorf("last_error = %q, want the sync error's text", after.LastError)
		}
		if after.LastDoltCommit != "c0" {
			t.Errorf("last_dolt_commit = %q, want unchanged %q", after.LastDoltCommit, "c0")
		}
	})

	t.Run("never_succeeded_keeps_last_success_at_zero", func(t *testing.T) {
		dir := backupExportTestDir(t)
		fake := &failingBackupStore{commit: "c1", backupErr: errors.New("boom")}
		if _, err := runBackupExport(context.Background(), directLocalBackup{store: fake}, true); err == nil {
			t.Fatal("expected runBackupExport to fail")
		}
		after := readRawBackupState(t, dir)
		if !after.LastSuccessAt.IsZero() {
			t.Errorf("last_success_at = %v after only failed attempts, want zero", after.LastSuccessAt)
		}
		if after.LastError != "boom" {
			t.Errorf("last_error = %q, want the sync error's text", after.LastError)
		}
		if after.LastDoltCommit != "" {
			t.Errorf("last_dolt_commit = %q after a failed sync, want empty", after.LastDoltCommit)
		}
	})
}
