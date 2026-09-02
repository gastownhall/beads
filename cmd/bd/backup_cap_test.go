package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
)

// TestBackupSizeCapExceeded pins the threshold check itself against a real
// directory (no stubbing needed — getDirSize/formatBytes are pure
// filesystem reads, already exercised by compact.go's own tests).
func TestBackupSizeCapExceeded(t *testing.T) {
	tests := []struct {
		name         string
		fileBytes    int
		capMB        string // config value; "" = use default (2048)
		wantExceeded bool
	}{
		{
			name:         "tiny dir, default cap → not exceeded",
			fileBytes:    1024,
			wantExceeded: false,
		},
		{
			name:         "dir over a small explicit cap → exceeded",
			fileBytes:    2 * 1024 * 1024, // 2MB
			capMB:        "1",
			wantExceeded: true,
		},
		{
			name:         "dir under a small explicit cap → not exceeded",
			fileBytes:    1024,
			capMB:        "1",
			wantExceeded: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			t.Setenv("BEADS_DIR", "")
			t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
			if tt.capMB != "" {
				t.Setenv("BD_BACKUP_SIZE_CAP_MB", tt.capMB)
			} else {
				os.Unsetenv("BD_BACKUP_SIZE_CAP_MB")
				t.Cleanup(func() { os.Unsetenv("BD_BACKUP_SIZE_CAP_MB") })
			}
			config.ResetForTesting()
			t.Cleanup(config.ResetForTesting)
			if err := config.Initialize(); err != nil {
				t.Fatalf("config.Initialize: %v", err)
			}

			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "data"), make([]byte, tt.fileBytes), 0o600); err != nil {
				t.Fatal(err)
			}

			exceeded, size, err := backupSizeCapExceeded(dir)
			if err != nil {
				t.Fatalf("backupSizeCapExceeded: %v", err)
			}
			if exceeded != tt.wantExceeded {
				t.Errorf("exceeded = %v (size=%d), want %v", exceeded, size, tt.wantExceeded)
			}
		})
	}
}

// TestMaybeWarnBackupSizeCap_Throttle pins the warning throttle: the
// stderr warning (and the state persistence) must not repeat on every
// single call once the cap is already known to be exceeded — only once
// per backup.size-warn-interval. Mirrors the throttle-persistence pattern
// already used for the backup interval itself (backup_export.go, wy-zrmqr).
func TestMaybeWarnBackupSizeCap_Throttle(t *testing.T) {
	tests := []struct {
		name         string
		lastWarnAt   time.Time
		warnInterval string // "" = use default (24h)
		wantUpdated  bool   // whether LastCapWarnAt should advance
	}{
		{
			name:        "never warned → warns now",
			lastWarnAt:  time.Time{},
			wantUpdated: true,
		},
		{
			name:        "warned 1h ago, default 24h interval → throttled",
			lastWarnAt:  time.Now().UTC().Add(-1 * time.Hour),
			wantUpdated: false,
		},
		{
			name:        "warned 25h ago, default 24h interval → warns again",
			lastWarnAt:  time.Now().UTC().Add(-25 * time.Hour),
			wantUpdated: true,
		},
		{
			name:         "warned 1h ago, custom 30m interval → warns again",
			lastWarnAt:   time.Now().UTC().Add(-1 * time.Hour),
			warnInterval: "30m",
			wantUpdated:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			t.Setenv("BEADS_DIR", "")
			t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
			if tt.warnInterval != "" {
				t.Setenv("BD_BACKUP_SIZE_WARN_INTERVAL", tt.warnInterval)
			} else {
				os.Unsetenv("BD_BACKUP_SIZE_WARN_INTERVAL")
				t.Cleanup(func() { os.Unsetenv("BD_BACKUP_SIZE_WARN_INTERVAL") })
			}
			config.ResetForTesting()
			t.Cleanup(config.ResetForTesting)
			if err := config.Initialize(); err != nil {
				t.Fatalf("config.Initialize: %v", err)
			}

			dir := t.TempDir()
			before := tt.lastWarnAt
			state := &backupState{LastCapWarnAt: tt.lastWarnAt}

			maybeWarnBackupSizeCap(dir, state, 3*1024*1024*1024)

			updated := !state.LastCapWarnAt.Equal(before)
			if updated != tt.wantUpdated {
				t.Errorf("LastCapWarnAt updated = %v (before=%v after=%v), want %v",
					updated, before, state.LastCapWarnAt, tt.wantUpdated)
			}
			if tt.wantUpdated {
				// Persisted state must reflect the new warning time too.
				st, err := loadBackupState(dir)
				if err != nil {
					t.Fatalf("loadBackupState: %v", err)
				}
				if st.LastCapWarnAt.IsZero() {
					t.Error("last_cap_warn_at not persisted to backup_state.json")
				}
			}
		})
	}
}

// TestMaybeAutoBackup_SkipsWhenCapExceeded is the wiring test: a backup
// destination already over the size cap must never attempt a sync at all
// (runDoltGCCommand-equivalent risk avoided entirely — there is nothing to
// stub here because the whole point is that BackupDatabase must NOT be
// called once capped).
func TestMaybeAutoBackup_SkipsWhenCapExceeded(t *testing.T) {
	// Isolate CWD/BEADS_DIR: unlike runBackupExport (used by the other
	// tests in this file), maybeAutoBackup also calls
	// clientServerShareFilesystem → beads.FindBeadsDir before ever
	// reaching backupDir(), so an unisolated CWD could walk up into this
	// repo's own real .beads/ directory (be-yjp4z; see backup_auto_test.go).
	t.Chdir(t.TempDir())
	t.Setenv("BEADS_DIR", "")
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")

	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BD_BACKUP_GIT_REPO", repo)
	t.Setenv("BD_BACKUP_ENABLED", "1")
	t.Setenv("BD_BACKUP_SIZE_CAP_MB", "1")
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}

	dir, err := backupDir()
	if err != nil {
		t.Fatalf("backupDir: %v", err)
	}
	// Push the destination over the 1MB cap before any backup attempt.
	if err := os.WriteFile(filepath.Join(dir, "filler"), make([]byte, 2*1024*1024), 0o600); err != nil {
		t.Fatal(err)
	}

	oldStore := store
	fake := &failingBackupStore{commit: "deadbeef", backupErr: nil}
	store = fake
	t.Cleanup(func() { store = oldStore })

	maybeAutoBackup(context.Background())

	if fake.backupCalls != 0 {
		t.Fatalf("BackupDatabase should not be called once the size cap is exceeded, got %d calls", fake.backupCalls)
	}
}
