//go:build cgo

package embeddeddolt_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A backup URL must reach DOLT_BACKUP unchanged instead of being stat'ed as a
// directory. A file:// URL whose path sits under a regular file is
// deterministic: Dolt cannot create that directory (root included), the
// restore fails, and the error carries versioncontrolops.BackupRestore's
// "restore from backup <url>" wrapper, which proves the URL was passed to
// CALL DOLT_BACKUP('restore', ...) byte-for-byte.
func TestRestoreDatabaseRoutesBackupURLWithoutStat(t *testing.T) {
	env := newTestEnv(t, "test")
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatalf("write %q: %v", blocker, err)
	}
	source := "file://" + filepath.Join(blocker, "backup")

	err := env.store.RestoreDatabase(t.Context(), source, false)
	if err == nil {
		t.Fatalf("RestoreDatabase(%q) returned nil for a backup that cannot exist", source)
	}
	if !strings.Contains(err.Error(), "restore from backup "+source) {
		t.Fatalf("RestoreDatabase error %q does not show the URL reaching DOLT_BACKUP unchanged", err)
	}
	if strings.Contains(err.Error(), "backup source does not exist") {
		t.Fatalf("RestoreDatabase stat'ed a backup URL: %v", err)
	}
}

func TestRestoreDatabaseStatsDirectorySource(t *testing.T) {
	env := newTestEnv(t, "test")
	source := filepath.Join(t.TempDir(), "missing")

	err := env.store.RestoreDatabase(t.Context(), source, false)
	if err == nil {
		t.Fatal("RestoreDatabase returned nil for a missing directory")
	}
	if !strings.Contains(err.Error(), "backup source does not exist") {
		t.Fatalf("RestoreDatabase error %q does not report a missing backup source", err)
	}
}
