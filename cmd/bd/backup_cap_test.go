package main

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
)

// TestBackupSizeCapExceeded pins the threshold check itself against a real
// directory (no stubbing needed — getDirSize is a pure filesystem read and
// formatBytes a pure formatter).
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

// TestPauseAutoBackupForSizeCap_WarnThrottle pins the warning throttle: the
// stderr warning must not repeat on every single call once the cap is
// already known to be exceeded — only once per backup.size-warn-interval.
// Mirrors the throttle-persistence pattern already used for the backup
// interval itself (backup_export.go, wy-zrmqr).
//
// It also pins the half of the PR #6071 review fix that is independent of
// the warning: every call, throttled or not, re-arms the backup interval
// throttle (state.Timestamp) and persists it, so a paused destination pays
// the getDirSize walk at most once per backup.interval instead of once per
// bd command.
func TestPauseAutoBackupForSizeCap_WarnThrottle(t *testing.T) {
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
			state := &backupState{LastCapWarnAt: tt.lastWarnAt, LastDoltCommit: "deadbeef"}

			pauseAutoBackupForSizeCap(dir, state, 3*1024*1024*1024)

			updated := !state.LastCapWarnAt.Equal(before)
			if updated != tt.wantUpdated {
				t.Errorf("LastCapWarnAt updated = %v (before=%v after=%v), want %v",
					updated, before, state.LastCapWarnAt, tt.wantUpdated)
			}

			// The interval throttle is re-armed and persisted on EVERY
			// skip, throttled warning or not — that is what stops a
			// paused destination from walking on every bd command.
			st, err := loadBackupState(dir)
			if err != nil {
				t.Fatalf("loadBackupState: %v", err)
			}
			if st.Timestamp.IsZero() {
				t.Error("timestamp not persisted to backup_state.json: the interval throttle never re-arms, so the cap walk reruns on every command")
			}
			if st.LastDoltCommit != "deadbeef" {
				t.Errorf("last_dolt_commit = %q, want it left untouched so change detection still sees pending work", st.LastDoltCommit)
			}
			if tt.wantUpdated {
				// Persisted state must reflect the new warning time too.
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

// TestPauseAutoBackupForSizeCap_RemediationAdvice pins the PR #6071 review
// fix: the warning must not tell operators to delete the backup directory
// — nothing confirms the destination is cleanly recreated by the next
// sync, and Dolt's server-side backup remote stays registered against that
// path. It should point at the levers that end the pause instead: raising
// the cap, or pointing backup.git-repo at another repository. It must not
// point at `bd backup init`, which the original fix suggested (PR #6071
// post-merge review): that configures the destination for manual `bd backup
// sync` and leaves auto-backup paused — see
// TestMaybeAutoBackup_RemediationLevers for all three, run for real.
func TestPauseAutoBackupForSizeCap_RemediationAdvice(t *testing.T) {
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
		pauseAutoBackupForSizeCap(dir, state, 3*1024*1024*1024)
	})

	if strings.Contains(stderr, "delete") {
		t.Errorf("warning suggests deleting the backup directory (unsafe — see PR #6071 review): %q", stderr)
	}
	if !strings.Contains(stderr, "backup.size-cap-mb") {
		t.Errorf("warning missing backup.size-cap-mb pointer: %q", stderr)
	}
	if !strings.Contains(stderr, "backup.git-repo") {
		t.Errorf("warning missing backup.git-repo pointer: %q", stderr)
	}
	if strings.Contains(stderr, "bd backup init") {
		t.Errorf("warning points at `bd backup init`, which does not move the auto-backup destination: %q", stderr)
	}
}

// TestPauseAutoBackupForSizeCap_PersistFailureIsNonFatal pins the contract
// the whole pause path rests on: it persists the throttle by writing
// backup_state.json INTO the directory it just declared over-cap, and in
// the disk-full case this cap exists for that write fails. The skip and
// its operator warning must still happen — a failure to record the
// throttle must never block the already-decided skip, nor escape.
func TestPauseAutoBackupForSizeCap_PersistFailureIsNonFatal(t *testing.T) {
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

	state := &backupState{}
	stderr := captureStderr(t, func() {
		pauseAutoBackupForSizeCap(dir, state, 3*1024*1024*1024)
	})
	if !strings.Contains(stderr, "PAUSED") {
		t.Fatalf("expected a PAUSED warning even when the throttle cannot be persisted, got %q", stderr)
	}
	// The in-memory state still carries the re-armed throttle, so the rest
	// of this process behaves as though it had been recorded.
	if state.Timestamp.IsZero() {
		t.Error("interval throttle not re-armed in memory when persistence failed")
	}
	if state.LastCapWarnAt.IsZero() {
		t.Error("warn timestamp not recorded in memory when persistence failed")
	}
}

// TestMaybeAutoBackup_CapCheckSkippedWhenThrottled pins the ga-y6gjv PR
// review's performance fix: the size-cap directory walk must not run when
// the interval throttle would already skip the backup — the reviewer
// measured 65-100ms per bd invocation at 20k files in the backup dir if
// the cap check runs unconditionally before the throttle. Detected
// indirectly: if the cap check ran, it would find the destination over
// cap and persist LastCapWarnAt via pauseAutoBackupForSizeCap; if the
// interval throttle short-circuits first (as it must), LastCapWarnAt
// stays exactly as pre-seeded (zero). The cap check now sits after change
// detection too, so the store reports a commit that differs from the
// seeded watermark: otherwise change detection alone would stop the walk
// and this test would pass without the throttle.
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
	seeded := &backupState{Timestamp: time.Now().UTC(), LastDoltCommit: "oldcommit"}
	if err := saveBackupState(dir, seeded); err != nil {
		t.Fatal(err)
	}

	oldStore := store
	// Different commit from the watermark ⇒ change detection would pass,
	// so only the interval throttle can keep the walk from running.
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

// TestMaybeAutoBackup_CapCheckSkippedWhenUnchanged pins the first half of
// the PR #6071 review's gating finding: the size-cap walk must sit AFTER
// change detection, so an IDLE workspace — nothing new committed since the
// last backup — never pays it.
//
// The interval throttle alone cannot cover this case, because only a
// backup attempt advances state.Timestamp: an idle workspace never reaches
// runBackupExport, so the throttle can never re-arm and, with the walk
// ahead of change detection, every single bd command paid the full
// filepath.Walk indefinitely.
//
// Detected the same way as the throttle test above: if the cap check ran,
// it would find the destination over cap and record LastCapWarnAt.
func TestMaybeAutoBackup_CapCheckSkippedWhenUnchanged(t *testing.T) {
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
	// Push the destination over the 1MB cap, so any walk that runs finds
	// it exceeded and leaves a mark.
	if err := os.WriteFile(filepath.Join(dir, "filler"), make([]byte, 2*1024*1024), 0o600); err != nil {
		t.Fatal(err)
	}
	// Last backup an hour ago: the 15m interval throttle has passed, so
	// change detection — not the throttle — is what must stop this.
	seeded := &backupState{Timestamp: time.Now().UTC().Add(-time.Hour), LastDoltCommit: "deadbeef"}
	if err := saveBackupState(dir, seeded); err != nil {
		t.Fatal(err)
	}
	want, err := loadBackupState(dir)
	if err != nil {
		t.Fatalf("loadBackupState: %v", err)
	}

	oldStore := store
	// Same commit as the recorded watermark ⇒ nothing changed ⇒ idle.
	fake := &failingBackupStore{commit: "deadbeef", backupErr: nil}
	store = fake
	t.Cleanup(func() { store = oldStore })

	maybeAutoBackup(context.Background())

	if fake.backupCalls != 0 {
		t.Fatalf("BackupDatabase should not be called when nothing changed, got %d calls", fake.backupCalls)
	}
	st, err := loadBackupState(dir)
	if err != nil {
		t.Fatalf("loadBackupState: %v", err)
	}
	if !st.LastCapWarnAt.IsZero() {
		t.Error("LastCapWarnAt was set — the size-cap walk ran on an idle workspace, want it skipped entirely (it would then run on every bd command, forever)")
	}
	if !st.Timestamp.Equal(want.Timestamp) {
		t.Errorf("timestamp = %v, want it untouched at %v: the idle path must not write state at all, or an idle period would cost up to one backup.interval of extra latency",
			st.Timestamp, want.Timestamp)
	}
}

// TestMaybeAutoBackup_PausedSkipArmsIntervalThrottle pins the second half
// of the PR #6071 review's gating finding, and is the positive control for
// the idle test above: with data genuinely changed, the cap walk DOES run
// and finds the destination over cap — and that skip must re-arm the
// interval throttle it would otherwise never reach.
//
// PAUSED is the pathological state: an over-cap destination is by
// definition the largest one, so without this the feature's terminal state
// is its most expensive, paying the full walk on every bd command until an
// operator intervenes. LastDoltCommit stays untouched so change detection
// still sees the pending work once the cap is raised.
func TestMaybeAutoBackup_PausedSkipArmsIntervalThrottle(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(dir, "filler"), make([]byte, 2*1024*1024), 0o600); err != nil {
		t.Fatal(err)
	}
	seededAt := time.Now().UTC().Add(-time.Hour)
	seeded := &backupState{Timestamp: seededAt, LastDoltCommit: "oldcommit"}
	if err := saveBackupState(dir, seeded); err != nil {
		t.Fatal(err)
	}

	oldStore := store
	// Different commit from the watermark ⇒ data changed ⇒ the cap check
	// is reached.
	fake := &failingBackupStore{commit: "deadbeef", backupErr: nil}
	store = fake
	t.Cleanup(func() { store = oldStore })

	maybeAutoBackup(context.Background())

	if fake.backupCalls != 0 {
		t.Fatalf("BackupDatabase should not be called once the size cap is exceeded, got %d calls", fake.backupCalls)
	}
	st, err := loadBackupState(dir)
	if err != nil {
		t.Fatalf("loadBackupState: %v", err)
	}
	if st.LastCapWarnAt.IsZero() {
		t.Fatal("LastCapWarnAt not set — the cap path never ran, so this test proves nothing about the skip")
	}
	if !st.Timestamp.After(seededAt) {
		t.Errorf("timestamp = %v, want it advanced past %v: the cap skip must re-arm the interval throttle or the walk reruns on every bd command",
			st.Timestamp, seededAt)
	}
	if st.LastDoltCommit != "oldcommit" {
		t.Errorf("last_dolt_commit = %q, want %q left untouched so change detection still sees pending work once the cap is raised",
			st.LastDoltCommit, "oldcommit")
	}
}

// dirRecordingBackupStore records the destination of every backup, so a
// test can tell a sync into the paused destination from one into a new one.
type dirRecordingBackupStore struct {
	failingBackupStore
	dirs []string
}

func (f *dirRecordingBackupStore) BackupDatabase(ctx context.Context, dir string) error {
	f.dirs = append(f.dirs, dir)
	return f.failingBackupStore.BackupDatabase(ctx, dir)
}

// TestMaybeAutoBackup_RemediationLevers runs the PAUSED advice for real
// (PR #6071 post-merge review): each lever it names must resume
// auto-backup, and `bd backup init <new-path>`, which it used to name, must
// not. backupDir() reads only backup.git-repo, so the destination init
// configures for manual `bd backup sync` never reaches auto-backup.
func TestMaybeAutoBackup_RemediationLevers(t *testing.T) {
	// paused returns an over-cap destination whose pause has already been
	// recorded, and the fake store a resumed sync would reach.
	paused := func(t *testing.T) (string, *dirRecordingBackupStore) {
		t.Helper()
		t.Chdir(t.TempDir())
		// A resumed sync takes the workspace-scoped backup lock, so the
		// command needs a real workspace.
		prepareBackupStatusTest(t)

		repo := t.TempDir()
		if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("BD_GIT_HOOK", "")
		t.Setenv("BD_BACKUP_GIT_REPO", repo)
		t.Setenv("BD_BACKUP_ENABLED", "1")
		t.Setenv("BD_BACKUP_SIZE_CAP_MB", "1")
		initConfigForTest(t)

		dir, err := backupDir()
		if err != nil {
			t.Fatalf("backupDir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "filler"), make([]byte, 2*1024*1024), 0o600); err != nil {
			t.Fatal(err)
		}
		seeded := &backupState{Timestamp: time.Now().UTC().Add(-time.Hour), LastDoltCommit: "oldcommit"}
		if err := saveBackupState(dir, seeded); err != nil {
			t.Fatal(err)
		}

		oldStore := store
		fake := &dirRecordingBackupStore{failingBackupStore: failingBackupStore{commit: "deadbeef"}}
		store = fake
		t.Cleanup(func() { store = oldStore })

		stderr := captureStderr(t, func() { maybeAutoBackup(context.Background()) })
		if !strings.Contains(stderr, "auto-backup PAUSED") || len(fake.dirs) != 0 {
			t.Fatalf("auto-backup did not pause (synced to %q), so this test proves nothing: %q", fake.dirs, stderr)
		}
		return dir, fake
	}
	// rewindThrottle stands in for waiting out backup.interval, which the
	// pause re-armed: a lever applied to the same destination takes effect
	// at its next eligible sync, not on the very next command.
	rewindThrottle := func(t *testing.T, dir string) time.Time {
		t.Helper()
		st, err := loadBackupState(dir)
		if err != nil {
			t.Fatalf("loadBackupState: %v", err)
		}
		st.Timestamp = time.Now().UTC().Add(-time.Hour)
		if err := saveBackupState(dir, st); err != nil {
			t.Fatal(err)
		}
		return st.Timestamp
	}

	t.Run("raise backup.size-cap-mb", func(t *testing.T) {
		dir, fake := paused(t)
		t.Setenv("BD_BACKUP_SIZE_CAP_MB", "10")
		initConfigForTest(t)
		rewindThrottle(t, dir)

		maybeAutoBackup(context.Background())
		if len(fake.dirs) != 1 || fake.dirs[0] != dir {
			t.Errorf("synced to %q after raising the cap, want one sync to %q", fake.dirs, dir)
		}
	})

	t.Run("point backup.git-repo at a different git repository", func(t *testing.T) {
		_, fake := paused(t)
		other := t.TempDir()
		if err := os.MkdirAll(filepath.Join(other, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("BD_BACKUP_GIT_REPO", other)
		initConfigForTest(t)

		// No rewind: the new destination has no throttle state of its own.
		maybeAutoBackup(context.Background())
		want := filepath.Join(other, "backup")
		if len(fake.dirs) != 1 || fake.dirs[0] != want {
			t.Errorf("synced to %q after switching backup.git-repo, want one sync to %q", fake.dirs, want)
		}
	})

	t.Run("bd backup init leaves auto-backup paused", func(t *testing.T) {
		dir, fake := paused(t)
		oldCtx, oldWrote := rootCtx, commandDidWrite.Load()
		rootCtx = context.Background()
		t.Cleanup(func() {
			rootCtx = oldCtx
			commandDidWrite.Store(oldWrote)
		})
		stdout := captureStdout(t, func() error {
			return backupInitCmd.RunE(backupInitCmd, []string{t.TempDir()})
		})
		if !strings.Contains(stdout, "Backup destination configured") {
			t.Fatalf("`bd backup init` did not configure a destination, so this test proves nothing: %q", stdout)
		}
		rewound := rewindThrottle(t, dir)

		maybeAutoBackup(context.Background())
		if len(fake.dirs) != 0 {
			t.Fatalf("synced to %q after `bd backup init`, want auto-backup still paused", fake.dirs)
		}
		st, err := loadBackupState(dir)
		if err != nil {
			t.Fatalf("loadBackupState: %v", err)
		}
		if !st.Timestamp.After(rewound) {
			t.Errorf("timestamp = %v, want it re-armed past %v by the size-cap skip: the cap check never ran, so this proves nothing",
				st.Timestamp, rewound)
		}
	})
}

// TestWarnBackupSizeCapUnavailable_IsOperatorVisible pins the PR #6071
// review's error-handling minor: getDirSize aborts its whole walk on the
// first unreadable entry, and maybeAutoBackup then proceeds UNCAPPED. That
// is the right direction (fail open), but it used to happen behind a
// debug-only line, so the cap could silently disable itself and restore
// the unbounded growth the feature exists to prevent. The operator must be
// told, on stderr, both that the measurement failed and what it costs.
func TestWarnBackupSizeCapUnavailable_IsOperatorVisible(t *testing.T) {
	stderr := captureStderr(t, func() {
		warnBackupSizeCapUnavailable(errors.New("permission denied"))
	})

	if !strings.Contains(stderr, "permission denied") {
		t.Errorf("warning does not name the underlying error: %q", stderr)
	}
	if !strings.Contains(stderr, "cap") {
		t.Errorf("warning does not say the size cap is what failed: %q", stderr)
	}
	if !strings.Contains(stderr, "unbounded") {
		t.Errorf("warning does not state the consequence — the backup proceeds uncapped: %q", stderr)
	}
}

// TestSizeCapStatus_UnavailableOnWalkError pins the PR #6071 review's
// render-asymmetry nit: on a non-ENOENT getDirSize error the human status
// path used to drop the "Size cap:" line entirely — indistinguishable from
// "no cap configured" for an operator debugging a permission problem —
// while --json reported the error all along. The JSON error object must
// also carry an explicit null `exceeded`, because a consumer reading a
// missing key as false sees "not exceeded" during exactly the failure that
// makes it unmeasurable.
func TestSizeCapStatus_UnavailableOnWalkError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; chmod does not deny reads")
	}
	t.Chdir(t.TempDir())
	t.Setenv("BEADS_DIR", "")
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	t.Setenv("BD_BACKUP_SIZE_CAP_MB", "1")
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}

	dir := t.TempDir()
	// Unreadable, but present: getDirSize fails with a permission error,
	// which is NOT os.IsNotExist, so it takes the error branch rather than
	// the "destination not created yet" branch.
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) // let t.TempDir() clean up

	stdout := captureStdout(t, func() error {
		showSizeCapStatus(dir)
		return nil
	})
	if !strings.Contains(stdout, "Size cap: unavailable") {
		t.Errorf("human status dropped the size-cap line on a walk error, want an explicit `unavailable`: %q", stdout)
	}

	got := showSizeCapStatusJSON(dir)
	if got["error"] == nil || got["error"] == "" {
		t.Errorf("JSON status omits the walk error: %#v", got)
	}
	exceeded, ok := got["exceeded"]
	if !ok {
		t.Errorf("JSON error object omits `exceeded`; a consumer reading the missing key as false sees `not exceeded`: %#v", got)
	}
	if exceeded != nil {
		t.Errorf("exceeded = %#v, want an explicit null while the size is unmeasurable", exceeded)
	}
}

// destinationGoesReadOnlyStore syncs successfully, but the destination
// loses write access the instant the sync finishes (e.g. a quota hit or a
// read-only remount mid-backup): BackupDatabase itself succeeds, so the
// sync genuinely happens, but it chmods dir read-only as a side effect, so
// runBackupExport's post-sync saveBackupState — the call that would record
// the new watermark — fails and never reaches disk. That is one of the
// runBackupExport exits that returns without persisting state; the single
// pre-sync GetCurrentCommit read (#7044) always succeeds here, so this
// isolates the write-failure exit rather than a commit-read failure.
type destinationGoesReadOnlyStore struct {
	failingBackupStore
	t *testing.T
}

func (f *destinationGoesReadOnlyStore) BackupDatabase(ctx context.Context, dir string) error {
	if err := f.failingBackupStore.BackupDatabase(ctx, dir); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		f.t.Fatalf("chmod destination read-only: %v", err)
	}
	return nil
}

// TestMaybeAutoBackup_WalkErrorArmsIntervalThrottle pins the PR #6071
// iteration-2 review's behavioral minor: when the size-cap walk fails,
// maybeAutoBackup warns and proceeds uncapped, and it must re-arm the
// interval throttle itself before doing so. runBackupExport does not persist
// state.Timestamp on every exit — here the sync itself succeeds (proving
// fail-open) but the destination goes read-only immediately afterward, so
// the post-sync saveBackupState that would record the new watermark fails —
// so without the re-arm the walk, its warning and the sync itself all
// repeat on every bd command instead of once per backup.interval.
func TestMaybeAutoBackup_WalkErrorArmsIntervalThrottle(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; chmod does not deny reads/writes")
	}
	t.Chdir(t.TempDir())
	// The sync has to actually run for this test to reach runBackupExport's
	// non-persisting exit, so give the command a real workspace: a
	// workspace-scoped step on the way (a backup lock, for one) must not
	// skip the sync before it starts.
	prepareBackupStatusTest(t)

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
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) // undo the simulated read-only destination so t.TempDir() can clean up
	// One unreadable entry inside an otherwise writable destination: the
	// walk fails, but backup_state.json can still be read and written.
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) }) // let t.TempDir() clean up
	seededAt := time.Now().UTC().Add(-time.Hour)
	seeded := &backupState{Timestamp: seededAt, LastDoltCommit: "oldcommit"}
	if err := saveBackupState(dir, seeded); err != nil {
		t.Fatal(err)
	}

	oldStore := store
	// Different commit from the watermark ⇒ data changed ⇒ the walk runs.
	fake := &destinationGoesReadOnlyStore{failingBackupStore: failingBackupStore{commit: "deadbeef"}, t: t}
	store = fake
	t.Cleanup(func() { store = oldStore })

	first := captureStderr(t, func() { maybeAutoBackup(context.Background()) })
	if !strings.Contains(first, "could not be measured") {
		t.Fatalf("the walk error never reached its warning, so this test proves nothing: %q", first)
	}
	if fake.backupCalls != 1 {
		t.Fatalf("BackupDatabase calls = %d, want 1: an unmeasurable destination must fail open and still sync", fake.backupCalls)
	}
	st, err := loadBackupState(dir)
	if err != nil {
		t.Fatalf("loadBackupState: %v", err)
	}
	if !st.Timestamp.After(seededAt) {
		t.Errorf("timestamp = %v, want it advanced past %v: runBackupExport returned without persisting it, so the walk-error path must re-arm the interval throttle",
			st.Timestamp, seededAt)
	}
	if st.LastDoltCommit != "oldcommit" {
		t.Errorf("last_dolt_commit = %q, want %q left untouched: no watermark was recorded, so change detection must still see the pending work",
			st.LastDoltCommit, "oldcommit")
	}

	second := captureStderr(t, func() { maybeAutoBackup(context.Background()) })
	if strings.Contains(second, "could not be measured") {
		t.Errorf("the walk-error warning repeated on the very next command, want it bounded by the interval throttle: %q", second)
	}
	if fake.backupCalls != 1 {
		t.Errorf("BackupDatabase calls = %d after a second command, want still 1: the interval throttle should have stopped it", fake.backupCalls)
	}
}

// TestShowSizeCapStatus_DisabledEchoesConfiguredValue pins the PR #6071
// iteration-2 review's debuggability minor: viper's GetInt resolves an
// unparseable backup.size-cap-mb ("2048MB") to 0, which disables the cap,
// so the status line must echo what is actually configured rather than
// asserting "backup.size-cap-mb=0", a value the operator never wrote.
func TestShowSizeCapStatus_DisabledEchoesConfiguredValue(t *testing.T) {
	for _, raw := range []string{"0", "2048MB"} {
		t.Run(raw, func(t *testing.T) {
			t.Chdir(t.TempDir())
			t.Setenv("BEADS_DIR", "")
			t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
			t.Setenv("BD_BACKUP_SIZE_CAP_MB", raw)
			config.ResetForTesting()
			t.Cleanup(config.ResetForTesting)
			if err := config.Initialize(); err != nil {
				t.Fatalf("config.Initialize: %v", err)
			}
			if got := effectiveSizeCapMB(); got != 0 {
				t.Fatalf("effectiveSizeCapMB() = %d for %q, want 0 (cap disabled), or this test proves nothing", got, raw)
			}

			stdout := captureStdout(t, func() error {
				showSizeCapStatus(t.TempDir())
				return nil
			})
			want := "Size cap: disabled (backup.size-cap-mb=" + raw + ")"
			if !strings.Contains(stdout, want) {
				t.Errorf("status = %q, want it to contain %q", stdout, want)
			}
		})
	}
}

// TestSizeCapStatus_MidWalkNotExistIsUnavailable pins the PR #6071
// iteration-2 review's error-handling minor: only a destination that does
// not exist yet may read as empty. An ENOENT from inside the walk — a chunk
// file removed between listing and stat-ing it — used to be swallowed as
// size 0, reporting `"exceeded": false` for a destination that may be well
// over its cap, which is exactly what the explicit-null `exceeded` on the
// error branch exists to prevent.
func TestSizeCapStatus_MidWalkNotExistIsUnavailable(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("BEADS_DIR", "")
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	t.Setenv("BD_BACKUP_SIZE_CAP_MB", "1")
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}

	t.Run("missing destination reads as empty", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "not-created-yet")

		stdout := captureStdout(t, func() error {
			showSizeCapStatus(missing)
			return nil
		})
		if !strings.Contains(stdout, "Size cap: 0 B / ") {
			t.Errorf("status for a not-yet-created destination = %q, want an empty `Size cap: 0 B / ...`", stdout)
		}
		got := showSizeCapStatusJSON(missing)
		if got["exceeded"] != false || got["current_bytes"] != int64(0) {
			t.Errorf("JSON status for a not-yet-created destination = %#v, want current_bytes 0 and exceeded false", got)
		}
	})

	t.Run("ENOENT inside an existing destination is unavailable", func(t *testing.T) {
		dir := t.TempDir()
		vanished := &fs.PathError{Op: "lstat", Path: filepath.Join(dir, "chunk"), Err: fs.ErrNotExist}
		if !os.IsNotExist(vanished) {
			t.Fatalf("injected error is not an os.IsNotExist error, so this test proves nothing: %v", vanished)
		}
		oldWalk := walkBackupDestination
		walkBackupDestination = func(string) (int64, error) { return 0, vanished }
		t.Cleanup(func() { walkBackupDestination = oldWalk })

		stdout := captureStdout(t, func() error {
			showSizeCapStatus(dir)
			return nil
		})
		if !strings.Contains(stdout, "Size cap: unavailable") {
			t.Errorf("status after a mid-walk ENOENT = %q, want `Size cap: unavailable`, not a size", stdout)
		}
		got := showSizeCapStatusJSON(dir)
		exceeded, ok := got["exceeded"]
		if !ok || exceeded != nil {
			t.Errorf("JSON status after a mid-walk ENOENT = %#v, want an explicit null `exceeded`", got)
		}
		if got["error"] == nil || got["error"] == "" {
			t.Errorf("JSON status omits the walk error: %#v", got)
		}
	})
}
