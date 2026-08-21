package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

var errBackupRestoreReachedStorage = errors.New("backup restore reached storage")

type backupRestoreRecordingStore struct {
	storage.DoltStorage
	restoreCalls  int
	restoreSource string
}

func (s *backupRestoreRecordingStore) BackupAdd(context.Context, string, string) error { return nil }
func (s *backupRestoreRecordingStore) BackupSync(context.Context, string) error        { return nil }
func (s *backupRestoreRecordingStore) BackupRemove(context.Context, string) error      { return nil }
func (s *backupRestoreRecordingStore) BackupDatabase(context.Context, string) error    { return nil }
func (s *backupRestoreRecordingStore) RestoreDatabase(_ context.Context, source string, _ bool) error {
	s.restoreCalls++
	s.restoreSource = source
	return errBackupRestoreReachedStorage
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

	fake := &backupRestoreRecordingStore{}
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

	fake := &backupRestoreRecordingStore{}
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
