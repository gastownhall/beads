//go:build cgo

package embeddeddolt_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A backup URL must reach DOLT_BACKUP unchanged rather than be treated as a
// directory path. file:// is the only scheme this can exercise offline, and it
// is the one scheme ResolveBackupSource stats, so the URL names an existing
// directory that holds no backup: the stat passes, the URL goes to
// CALL DOLT_BACKUP('restore', ...) byte-for-byte, and Dolt refuses there, since
// the target database exists and force is off. The error carries
// versioncontrolops.BackupRestore's "restore from backup <url>" wrapper, which
// only that call produces.
func TestRestoreDatabasePassesFileURLThrough(t *testing.T) {
	env := newTestEnv(t, "test")
	source := "file://" + t.TempDir()

	err := env.store.RestoreDatabase(t.Context(), source, false)
	if err == nil {
		t.Fatalf("RestoreDatabase(%q) returned nil for a directory holding no backup", source)
	}
	if !strings.Contains(err.Error(), "restore from backup "+source) {
		t.Fatalf("RestoreDatabase error %q does not show the URL reaching DOLT_BACKUP unchanged", err)
	}
	if strings.Contains(err.Error(), "backup source does not exist") {
		t.Fatalf("RestoreDatabase stat'ed a backup URL as a directory path: %v", err)
	}
}

// A file:// URL naming a missing directory is refused before DOLT_BACKUP sees
// it. Handed one, DOLT_BACKUP creates the directory, opens it as an empty
// backup and, under --force, drops the live database before the restore fails.
// A typo in the source must cost neither the directory nor the data.
func TestRestoreDatabaseRefusesMissingFileURL(t *testing.T) {
	env := newTestEnv(t, "test")
	ctx := t.Context()
	if err := env.store.SetConfig(ctx, "restore.canary", "alive"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	missing := filepath.Join(t.TempDir(), "no-such-backup")

	// Errorf throughout: when the check regresses, all three consequences are
	// worth seeing, not just the first.
	err := env.store.RestoreDatabase(ctx, "file://"+missing, true)
	if err == nil || !strings.Contains(err.Error(), "backup source does not exist") {
		t.Errorf("RestoreDatabase(file://%s, force) = %v, want the missing-source refusal", missing, err)
	}
	if _, statErr := os.Stat(missing); !os.IsNotExist(statErr) {
		t.Errorf("RestoreDatabase created %s (stat: %v); a refused source must leave nothing behind", missing, statErr)
	}
	if got, err := env.store.GetConfig(ctx, "restore.canary"); err != nil || got != "alive" {
		t.Errorf("GetConfig(restore.canary) = %q, %v after the refused restore, want %q: the live database was touched",
			got, err, "alive")
	}
}

// A relative file:// path is refused, not stat'ed. Dolt resolves it against its
// data directory, never the working directory, so a stat here would pass on
// one directory while DOLT_BACKUP created another under the data directory
// and, under --force, dropped the live database before the restore failed.
func TestRestoreDatabaseRefusesRelativeFileURL(t *testing.T) {
	env := newTestEnv(t, "test")
	ctx := t.Context()
	if err := env.store.SetConfig(ctx, "restore.canary", "alive"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	// relbak exists in the working directory, so a stat of the URL's path
	// passes and only the refusal keeps the source away from DOLT_BACKUP.
	work := t.TempDir()
	if err := os.Mkdir(filepath.Join(work, "relbak"), 0o750); err != nil {
		t.Fatalf("mkdir relbak: %v", err)
	}
	t.Chdir(work)

	err := env.store.RestoreDatabase(ctx, "file://relbak", true)
	if err == nil || !strings.Contains(err.Error(), "not an absolute file:// path") {
		t.Errorf("RestoreDatabase(file://relbak, force) = %v, want the relative-path refusal", err)
	}
	created := filepath.Join(env.dataDir, "relbak")
	if _, statErr := os.Stat(created); !os.IsNotExist(statErr) {
		t.Errorf("RestoreDatabase created %s (stat: %v); a refused source must leave nothing behind", created, statErr)
	}
	if got, err := env.store.GetConfig(ctx, "restore.canary"); err != nil || got != "alive" {
		t.Errorf("GetConfig(restore.canary) = %q, %v after the refused restore, want %q: the live database was touched",
			got, err, "alive")
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
