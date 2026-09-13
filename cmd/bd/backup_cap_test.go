package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

// TestBackupSizeCapExceeded_DisabledWithZero pins the PR #6071 review fix:
// backup.size-cap-mb: 0 must mean "no cap", not "use the legacy 2048MB
// default" — an operator with a legitimately larger destination has no off
// switch otherwise. Uses a sparse file (Truncate, not a real write) to
// cross the legacy 2048MB threshold without allocating 2GB of real disk or
// memory.
func TestBackupSizeCapExceeded_DisabledWithZero(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("BEADS_DIR", "")
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	t.Setenv("BD_BACKUP_SIZE_CAP_MB", "0")
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}

	dir := t.TempDir()
	f, err := os.Create(filepath.Join(dir, "sparse-filler"))
	if err != nil {
		t.Fatal(err)
	}
	const overLegacyDefault = int64(2049) * 1024 * 1024 // just over the old 2048MB fallback
	if err := f.Truncate(overLegacyDefault); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	exceeded, _, err := backupSizeCapExceeded(dir)
	if err != nil {
		t.Fatalf("backupSizeCapExceeded: %v", err)
	}
	if exceeded {
		t.Error("exceeded = true with backup.size-cap-mb=0, want false (cap disabled)")
	}
}

// TestMaybeWarnBackupSizeCap_RemediationAdvice pins the PR #6071 review
// fix: the warning must not tell operators to delete the backup directory
// — nothing confirms the destination is cleanly recreated by the next
// sync, and Dolt's server-side backup remote stays registered against that
// path. It should point at the safe levers instead: raising the cap, or a
// fresh destination via `bd backup init`.
func TestMaybeWarnBackupSizeCap_RemediationAdvice(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("BEADS_DIR", "")
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}

	dir := t.TempDir()
	state := &backupState{}

	stderr := captureStderr(t, func() {
		maybeWarnBackupSizeCap(dir, state, 3*1024*1024*1024)
	})

	if strings.Contains(stderr, "delete") {
		t.Errorf("warning suggests deleting the backup directory (unsafe — see PR #6071 review): %q", stderr)
	}
	if !strings.Contains(stderr, "backup.size-cap-mb") {
		t.Errorf("warning missing backup.size-cap-mb pointer: %q", stderr)
	}
	if !strings.Contains(stderr, "bd backup init") {
		t.Errorf("warning missing `bd backup init <new-path>` pointer: %q", stderr)
	}
}

// TestMaybeWarnBackupSizeCap_InMemoryFallbackOnPersistFailure pins the PR
// #6071 review's minor point: maybeWarnBackupSizeCap persists the throttle
// by writing backup_state.json INTO the directory it just declared full —
// in the disk-full case this cap exists for, that write fails, so a caller
// that reloads state fresh every time (every request inside a long-lived
// bd server process, e.g. internal/storage/dbproxy) would see
// LastCapWarnAt stuck at zero forever and re-warn on every single call. An
// in-process fallback must keep the throttle honest even though disk
// persistence never succeeds.
func TestMaybeWarnBackupSizeCap_InMemoryFallbackOnPersistFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; chmod does not deny writes")
	}
	t.Chdir(t.TempDir())
	t.Setenv("BEADS_DIR", "")
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}

	dir := t.TempDir()
	// Make the destination read-only so saveBackupState can never persist
	// the throttle timestamp — the same failure mode as the disk-full case
	// this cap exists for.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) // let t.TempDir() clean up

	// First call: never warned before (fresh state, as loadBackupState
	// would return for this destination) — must warn, and attempt (and
	// fail) to persist.
	firstStderr := captureStderr(t, func() {
		maybeWarnBackupSizeCap(dir, &backupState{}, 3*1024*1024*1024)
	})
	if !strings.Contains(firstStderr, "PAUSED") {
		t.Fatalf("first call: expected a PAUSED warning, got %q", firstStderr)
	}

	// Second call simulates the NEXT invocation reloading state fresh —
	// since persistence failed above, a fresh load would again show
	// LastCapWarnAt zero. Without an in-process fallback this re-warns
	// immediately; with it, the in-memory record of "already warned" must
	// still throttle it.
	secondStderr := captureStderr(t, func() {
		maybeWarnBackupSizeCap(dir, &backupState{}, 3*1024*1024*1024)
	})
	if strings.Contains(secondStderr, "PAUSED") {
		t.Errorf("second call re-warned despite the in-process throttle fallback: %q", secondStderr)
	}
}

// TestMaybeAutoBackup_CapCheckSkippedWhenThrottled pins the ga-y6gjv PR
// review's performance fix: the size-cap directory walk must not run when
// the interval throttle would already skip the backup — the reviewer
// measured 65-100ms per bd invocation at 20k files in the backup dir if
// the cap check runs unconditionally before the throttle. Detected
// indirectly: if the cap check ran, it would find the destination over
// cap and persist LastCapWarnAt via maybeWarnBackupSizeCap; if the
// interval throttle short-circuits first (as it must), LastCapWarnAt
// stays exactly as pre-seeded (zero).
func TestMaybeAutoBackup_CapCheckSkippedWhenThrottled(t *testing.T) {
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
	// Push the destination over the 1MB cap...
	if err := os.WriteFile(filepath.Join(dir, "filler"), make([]byte, 2*1024*1024), 0o600); err != nil {
		t.Fatal(err)
	}
	// ...but seed a fresh backup timestamp so the interval throttle (15m
	// default) fires first, before the cap check ever gets a chance to run.
	seeded := &backupState{Timestamp: time.Now().UTC(), LastDoltCommit: "deadbeef"}
	if err := saveBackupState(dir, seeded); err != nil {
		t.Fatal(err)
	}

	oldStore := store
	fake := &failingBackupStore{commit: "deadbeef", backupErr: nil}
	store = fake
	t.Cleanup(func() { store = oldStore })

	maybeAutoBackup(context.Background())

	if fake.backupCalls != 0 {
		t.Fatalf("BackupDatabase should not be called while throttled, got %d calls", fake.backupCalls)
	}
	st, err := loadBackupState(dir)
	if err != nil {
		t.Fatalf("loadBackupState: %v", err)
	}
	if !st.LastCapWarnAt.IsZero() {
		t.Error("LastCapWarnAt was set — size-cap check ran despite the interval throttle, want it skipped entirely")
	}
}
