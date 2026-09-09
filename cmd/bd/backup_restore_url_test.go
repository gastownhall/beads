package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

var errBackupRestoreReachedStorage = errors.New("backup restore reached storage")

type backupRestoreRecordingStore struct {
	storage.DoltStorage
	restoreErr    error
	restoreCalls  int
	restoreSource string
	backupAddURL  string
}

func (s *backupRestoreRecordingStore) BackupAdd(_ context.Context, _ string, url string) error {
	s.backupAddURL = url
	return nil
}
func (s *backupRestoreRecordingStore) BackupSync(context.Context, string) error     { return nil }
func (s *backupRestoreRecordingStore) BackupRemove(context.Context, string) error   { return nil }
func (s *backupRestoreRecordingStore) BackupDatabase(context.Context, string) error { return nil }
func (s *backupRestoreRecordingStore) RestoreDatabase(_ context.Context, source string, _ bool) error {
	s.restoreCalls++
	s.restoreSource = source
	return s.restoreErr
}
func (s *backupRestoreRecordingStore) Commit(context.Context, string) error { return nil }

var _ storage.BackupStore = (*backupRestoreRecordingStore)(nil)

func TestBackupRestoreCommandRoutesBackupURLToStorage(t *testing.T) {
	oldStore := store
	oldRootCtx := rootCtx
	oldProxiedServerMode := proxiedServerMode
	t.Cleanup(func() {
		store = oldStore
		rootCtx = oldRootCtx
		proxiedServerMode = oldProxiedServerMode
	})

	fake := &backupRestoreRecordingStore{restoreErr: errBackupRestoreReachedStorage}
	store = fake
	rootCtx = context.Background()
	proxiedServerMode = false

	const source = "s3://bucket/path?endpoint=https://minio.example&region=auto&path-style=true"
	err := backupRestoreCmd.RunE(backupRestoreCmd, []string{source})
	if !errors.Is(err, errBackupRestoreReachedStorage) {
		t.Fatalf("backup restore error = %v, want storage error", err)
	}
	if fake.restoreCalls != 1 {
		t.Fatalf("RestoreDatabase calls = %d, want 1", fake.restoreCalls)
	}
	if fake.restoreSource != source {
		t.Fatalf("RestoreDatabase source = %q, want %q", fake.restoreSource, source)
	}
}

func TestBackupRestoreCommandKeepsDirectoryValidation(t *testing.T) {
	oldStore := store
	oldRootCtx := rootCtx
	oldProxiedServerMode := proxiedServerMode
	t.Cleanup(func() {
		store = oldStore
		rootCtx = oldRootCtx
		proxiedServerMode = oldProxiedServerMode
	})

	fake := &backupRestoreRecordingStore{restoreErr: errBackupRestoreReachedStorage}
	store = fake
	rootCtx = context.Background()
	proxiedServerMode = false

	source := filepath.Join(t.TempDir(), "missing")
	err := backupRestoreCmd.RunE(backupRestoreCmd, []string{source})
	want := fmt.Sprintf("backup directory not found: %s\nRun 'bd backup' first to create a backup", source)
	if err == nil || err.Error() != want {
		t.Fatalf("backup restore error = %q, want %q", err, want)
	}
	if fake.restoreCalls != 0 {
		t.Fatalf("RestoreDatabase calls = %d, want 0", fake.restoreCalls)
	}
}

func TestBackupRestoreCommandRegistersBackupURLAfterRestore(t *testing.T) {
	oldStore := store
	oldRootCtx := rootCtx
	oldProxiedServerMode := proxiedServerMode
	t.Cleanup(func() {
		store = oldStore
		rootCtx = oldRootCtx
		proxiedServerMode = oldProxiedServerMode
	})

	// A successful restore re-registers the source as the backup remote and
	// saves .beads/dolt-backup.json via beads.FindBeadsDir, so point BEADS_DIR
	// at a throwaway workspace (embeddeddolt/ is the marker FindBeadsDir
	// accepts) rather than whatever .beads is ambient.
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "embeddeddolt"), 0o700); err != nil {
		t.Fatalf("create workspace marker: %v", err)
	}
	t.Setenv("BEADS_DIR", beadsDir)

	fake := &backupRestoreRecordingStore{}
	store = fake
	rootCtx = context.Background()
	proxiedServerMode = false

	const source = "s3://bucket/path?endpoint=https://minio.example&region=auto&path-style=true"
	if err := backupRestoreCmd.RunE(backupRestoreCmd, []string{source}); err != nil {
		t.Fatalf("backup restore error = %v, want nil", err)
	}
	if fake.backupAddURL != source {
		t.Fatalf("BackupAdd url = %q, want %q", fake.backupAddURL, source)
	}

	data, err := os.ReadFile(filepath.Join(beadsDir, "dolt-backup.json"))
	if err != nil {
		t.Fatalf("read saved backup config: %v", err)
	}
	var cfg doltBackupConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal saved backup config: %v", err)
	}
	if cfg.BackupURL != source {
		t.Fatalf("saved backup_url = %q, want %q", cfg.BackupURL, source)
	}
}
