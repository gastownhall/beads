package dolt

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
)

// TestPrePushFSCK_EmptyCLIDir verifies that prePushFSCK is a no-op when
// CLIDir is empty (no local noms store configured).
func TestPrePushFSCK_EmptyCLIDir(t *testing.T) {
	t.Parallel()
	s := &DoltStore{dbPath: "", database: "test"}
	if err := s.prePushFSCK(context.Background()); err != nil {
		t.Fatalf("expected nil for empty CLIDir, got %v", err)
	}
}

// TestPrePushFSCK_NoNomsDir verifies that prePushFSCK is a no-op when
// CLIDir exists but .dolt/noms does not (uninitialized or non-dolt directory).
func TestPrePushFSCK_NoNomsDir(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	s := &DoltStore{dbPath: tmp, database: "mydb"}
	// CLIDir() = tmp/mydb, which doesn't exist and has no .dolt/noms
	if err := s.prePushFSCK(context.Background()); err != nil {
		t.Fatalf("expected nil when .dolt/noms absent, got %v", err)
	}
}

// TestPrePushFSCK_CleanDB verifies that prePushFSCK passes on a fresh
// dolt-initialized database with no corruption.
func TestPrePushFSCK_CleanDB(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("dolt"); err != nil {
		t.Skip("dolt not in PATH")
	}

	tmp := t.TempDir()
	dbDir := filepath.Join(tmp, "mydb")
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	initCmd := exec.Command("dolt", "init", "--name", "test", "--email", "test@example.com")
	initCmd.Dir = dbDir
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("dolt init: %v\n%s", err, out)
	}

	s := &DoltStore{dbPath: tmp, database: "mydb"}
	if err := s.prePushFSCK(context.Background()); err != nil {
		t.Fatalf("expected nil on clean DB, got %v", err)
	}
}

// TestPrePushFSCK_UnopenableDB verifies that prePushFSCK logs a warning and
// proceeds (returns nil) when dolt fsck cannot open the database. This avoids
// misleading users with a corruption warning for environmental / tooling
// failures. Example: dolthub/dolt#10915 (Windows url.Parse bug pre-v1.86.4)
// caused fsck to fail-to-open healthy databases, which the previous wrapper
// reported as "dangling chunk reference: aborting push to prevent propagating
// corrupt chunks".
//
// We simulate the unopenable state by creating a .dolt/noms directory without
// running dolt init — fsck prints "Could not open dolt database" and exits
// non-zero.
func TestPrePushFSCK_UnopenableDB(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("dolt"); err != nil {
		t.Skip("dolt not in PATH")
	}

	tmp := t.TempDir()
	dbDir := filepath.Join(tmp, "mydb")
	// Create .dolt/noms so the skip check passes, but don't init the repo.
	if err := os.MkdirAll(filepath.Join(dbDir, ".dolt", "noms"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	s := &DoltStore{dbPath: tmp, database: "mydb"}
	if err := s.prePushFSCK(context.Background()); err != nil {
		t.Fatalf("expected nil when fsck cannot open db (should warn and proceed), got %v", err)
	}
}

// TestFsckCouldNotOpen verifies the helper identifies both known dolt
// "couldn't open" phrasings and does not classify actual integrity failures
// (or unrelated output) as open-failures.
func TestFsckCouldNotOpen(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		output string
		want   bool
	}{
		{
			name:   "windows url.Parse bug pre-1.86.4 (dolthub/dolt#10915)",
			output: `Could not open dolt database: CreateFile \C:\Users\x\.beads\...\.dolt\noms: The filename, directory name, or volume label syntax is incorrect.`,
			want:   true,
		},
		{
			name:   "uninitialized .dolt directory",
			output: "The current directories repository state is invalid\nopen .dolt/repo_state.json: no such file or directory",
			want:   true,
		},
		{
			name:   "actual dangling chunk reference (must still abort)",
			output: "dangling chunk reference: hash abc123 referenced but not present",
			want:   false,
		},
		{
			name:   "empty output",
			output: "",
			want:   false,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := fsckCouldNotOpen(tc.output); got != tc.want {
				t.Errorf("fsckCouldNotOpen(%q) = %v, want %v", tc.output, got, tc.want)
			}
		})
	}
}

// TestClassifyFSCKFailure verifies that classifyFSCKFailure maps every failure
// mode to the correct outcome, and includes negative sentinel checks to guard
// against misclassification.
func TestClassifyFSCKFailure(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		parentErr    error
		fsckErr      error
		output       string
		wantNil      bool    // true → expect nil (could-not-open skip path)
		wantIs       error   // positive errors.Is assertion
		wantIsNot    []error // negative errors.Is assertions
		wantContains string  // substring expected in error message
		wantAbsent   string  // substring that must NOT appear
	}{
		// (a) Non-empty output wins over context state: real dangling reference
		//     arriving exactly at the deadline must not be masked as a timeout.
		{
			name:      "non-empty output + DeadlineExceeded → ErrDanglingReference, not ErrFSCKTimeout",
			parentErr: context.DeadlineExceeded,
			fsckErr:   context.DeadlineExceeded,
			output:    "dangling chunk reference: hash abc123 not found",
			wantIs:    ErrDanglingReference,
			wantIsNot: []error{ErrFSCKTimeout},
		},
		// (a) could-not-open output → nil (skip, caller logs).
		{
			name:      "non-empty could-not-open output → nil (skip, not an abort)",
			parentErr: nil,
			fsckErr:   nil,
			output:    "Could not open dolt database: some reason",
			wantNil:   true,
		},
		// (b) Parent context canceled → plain cancellation, not ErrDanglingReference
		//     or ErrFSCKTimeout.
		{
			name:         "parent Canceled → cancellation error, not dangling or timeout",
			parentErr:    context.Canceled,
			fsckErr:      context.Canceled,
			output:       "",
			wantIs:       context.Canceled,
			wantIsNot:    []error{ErrDanglingReference, ErrFSCKTimeout},
			wantContains: "interrupted",
		},
		// (c) Parent deadline exceeded → ErrFSCKTimeout, guidance names
		//     dolt.auto-push-timeout; the "raise via BEADS_FSCK_TIMEOUT env var"
		//     phrasing must NOT appear (it cannot extend the caller deadline).
		{
			name:         "parent DeadlineExceeded → ErrFSCKTimeout with caller-timeout guidance",
			parentErr:    context.DeadlineExceeded,
			fsckErr:      context.DeadlineExceeded,
			output:       "",
			wantIs:       ErrFSCKTimeout,
			wantIsNot:    []error{ErrDanglingReference},
			wantContains: "dolt.auto-push-timeout",
			wantAbsent:   "BEADS_FSCK_TIMEOUT environment variable",
		},
		// (d) Only fsck's own timeout fired (parent still running) → ErrFSCKTimeout
		//     with BEADS_FSCK_TIMEOUT guidance. Message must differ from (c).
		{
			name:         "own fsck DeadlineExceeded → ErrFSCKTimeout with BEADS_FSCK_TIMEOUT guidance",
			parentErr:    nil,
			fsckErr:      context.DeadlineExceeded,
			output:       "",
			wantIs:       ErrFSCKTimeout,
			wantIsNot:    []error{ErrDanglingReference},
			wantContains: "BEADS_FSCK_TIMEOUT",
		},
		// (e) Cancellation phrasing in output with no context error (bd-f2b15
		//     pin): a killed process group leaves "context canceled" in the
		//     output while both contexts look healthy. Must NOT be reported
		//     as corruption.
		{
			name:         "output 'context canceled' + nil ctx errors → interrupted, not ErrDanglingReference",
			parentErr:    nil,
			fsckErr:      nil,
			output:       "context canceled",
			wantIs:       context.Canceled,
			wantIsNot:    []error{ErrDanglingReference, ErrFSCKTimeout},
			wantContains: "interrupted",
		},
		{
			name:         "output 'signal: killed' + nil ctx errors → interrupted, not ErrDanglingReference",
			parentErr:    nil,
			fsckErr:      nil,
			output:       "signal: killed",
			wantIs:       context.Canceled,
			wantIsNot:    []error{ErrDanglingReference, ErrFSCKTimeout},
			wantContains: "interrupted",
		},
		// (a→b) Cancellation phrasing falls through to the parent-canceled branch.
		{
			name:      "output 'context canceled' + parent Canceled → cancellation, not dangling",
			parentErr: context.Canceled,
			fsckErr:   context.Canceled,
			output:    "context canceled",
			wantIs:    context.Canceled,
			wantIsNot: []error{ErrDanglingReference, ErrFSCKTimeout},
		},
		// (a→d) Cancellation phrasing + fsck's own deadline → timeout guidance,
		//       not a corruption report.
		{
			name:         "output 'context deadline exceeded' + own fsck timeout → ErrFSCKTimeout, not dangling",
			parentErr:    nil,
			fsckErr:      context.DeadlineExceeded,
			output:       "context deadline exceeded",
			wantIs:       ErrFSCKTimeout,
			wantIsNot:    []error{ErrDanglingReference},
			wantContains: "BEADS_FSCK_TIMEOUT",
		},
		// Negative sentinel: real corruption output without cancellation
		// phrasing must still abort.
		{
			name:      "real corruption output stays ErrDanglingReference despite nil ctx errors",
			parentErr: nil,
			fsckErr:   nil,
			output:    "dangling chunk reference: hash abc123 not found",
			wantIs:    ErrDanglingReference,
			wantIsNot: []error{ErrFSCKTimeout},
		},
		// (f) Generic non-zero exit, empty output, no context error → ErrDanglingReference.
		{
			name:      "generic failure (empty output, no ctx error) → ErrDanglingReference",
			parentErr: nil,
			fsckErr:   nil,
			output:    "",
			wantIs:    ErrDanglingReference,
			wantIsNot: []error{ErrFSCKTimeout},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := classifyFSCKFailure(tc.parentErr, tc.fsckErr, tc.output)

			if tc.wantNil {
				if err != nil {
					t.Errorf("want nil, got %v", err)
				}
				return
			}

			if err == nil {
				t.Fatalf("want non-nil error, got nil")
			}

			if tc.wantIs != nil && !errors.Is(err, tc.wantIs) {
				t.Errorf("errors.Is(err, %v) = false; err = %v", tc.wantIs, err)
			}
			for _, notErr := range tc.wantIsNot {
				if errors.Is(err, notErr) {
					t.Errorf("errors.Is(err, %v) = true, want false; err = %v", notErr, err)
				}
			}
			if tc.wantContains != "" && !strings.Contains(err.Error(), tc.wantContains) {
				t.Errorf("error message %q does not contain %q", err.Error(), tc.wantContains)
			}
			if tc.wantAbsent != "" && strings.Contains(err.Error(), tc.wantAbsent) {
				t.Errorf("error message %q must not contain %q", err.Error(), tc.wantAbsent)
			}
		})
	}
}

// TestFsckOutputInterrupted verifies the helper recognizes the cancellation
// phrasings an interrupted fsck prints, and does not classify integrity
// findings or unrelated output as interrupts.
func TestFsckOutputInterrupted(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		output string
		want   bool
	}{
		{
			name:   "context canceled (observed: killed background bd dolt push, bd-f2b15)",
			output: "context canceled",
			want:   true,
		},
		{
			name:   "context deadline exceeded",
			output: "error: context deadline exceeded",
			want:   true,
		},
		{
			name:   "signal killed",
			output: "signal: killed",
			want:   true,
		},
		{
			name:   "signal terminated",
			output: "signal: terminated",
			want:   true,
		},
		{
			name:   "real dangling reference is not an interrupt",
			output: "dangling chunk reference: hash abc123 referenced but not present",
			want:   false,
		},
		{
			name:   "empty output",
			output: "",
			want:   false,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := fsckOutputInterrupted(tc.output); got != tc.want {
				t.Errorf("fsckOutputInterrupted(%q) = %v, want %v", tc.output, got, tc.want)
			}
		})
	}
}

// TestClassifyFSCKFailure_CallerVsOwnTimeout verifies that caller-deadline (c)
// and own-timeout (d) produce distinct guidance text — the first must name
// dolt.auto-push-timeout and the second must name BEADS_FSCK_TIMEOUT.
func TestClassifyFSCKFailure_CallerVsOwnTimeout(t *testing.T) {
	t.Parallel()

	callerErr := classifyFSCKFailure(context.DeadlineExceeded, context.DeadlineExceeded, "")
	ownErr := classifyFSCKFailure(nil, context.DeadlineExceeded, "")

	if callerErr == nil || ownErr == nil {
		t.Fatal("both cases must return non-nil errors")
	}
	callerMsg := callerErr.Error()
	ownMsg := ownErr.Error()

	if !strings.Contains(callerMsg, "dolt.auto-push-timeout") {
		t.Errorf("caller-deadline message should name dolt.auto-push-timeout; got: %q", callerMsg)
	}
	if strings.Contains(callerMsg, "BEADS_FSCK_TIMEOUT environment variable") {
		t.Errorf("caller-deadline message must not say BEADS_FSCK_TIMEOUT environment variable; got: %q", callerMsg)
	}
	if !strings.Contains(ownMsg, "BEADS_FSCK_TIMEOUT") {
		t.Errorf("own-timeout message should name BEADS_FSCK_TIMEOUT; got: %q", ownMsg)
	}
	if strings.Contains(ownMsg, "dolt.auto-push-timeout") {
		t.Errorf("own-timeout message must not name dolt.auto-push-timeout; got: %q", ownMsg)
	}
}

// TestFsckTimeoutDuration verifies BEADS_FSCK_TIMEOUT parsing: valid durations
// are honored, unset and invalid values fall back to the compiled-in default.
func TestFsckTimeoutDuration(t *testing.T) {
	t.Run("valid duration honored", func(t *testing.T) {
		t.Setenv(fsckTimeoutEnv, "2m")
		if got := fsckTimeoutDuration(0); got != 2*time.Minute {
			t.Errorf("want 2m, got %v", got)
		}
	})
	t.Run("unset returns default", func(t *testing.T) {
		t.Setenv(fsckTimeoutEnv, "")
		if got := fsckTimeoutDuration(0); got != fsckTimeout {
			t.Errorf("want %v (default), got %v", fsckTimeout, got)
		}
	})
	t.Run("invalid returns default", func(t *testing.T) {
		t.Setenv(fsckTimeoutEnv, "not-a-duration")
		if got := fsckTimeoutDuration(0); got != fsckTimeout {
			t.Errorf("want %v (default), got %v", fsckTimeout, got)
		}
	})
}

func TestApplyFSCKTimeout(t *testing.T) {
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	t.Setenv("BD_DOLT_FSCK_TIMEOUT", "")
	for _, tc := range []struct {
		name    string
		raw     string
		want    time.Duration
		wantErr bool
	}{
		{name: "default", want: fsckTimeout},
		{name: "duration", raw: "5m", want: 5 * time.Minute},
		{name: "seconds", raw: "300", want: 5 * time.Minute},
		{name: "invalid", raw: "not-a-duration", wantErr: true},
		{name: "zero", raw: "0", wantErr: true},
		{name: "negative", raw: "-1s", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.raw != "" {
				if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("dolt:\n  fsck-timeout: "+tc.raw+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			cfg := &Config{BeadsDir: dir}
			err := applyFSCKTimeout(cfg)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr = %v", err, tc.wantErr)
			}
			if !tc.wantErr && cfg.FSCKTimeout != tc.want {
				t.Fatalf("timeout = %s, want %s", cfg.FSCKTimeout, tc.want)
			}
		})
	}
	t.Setenv("BEADS_DIR", t.TempDir())
	if err := config.Initialize(); err != nil {
		t.Fatal(err)
	}
	config.Set("dolt.fsck-timeout", "4m")
	cfg := &Config{}
	if err := applyFSCKTimeout(cfg); err != nil || cfg.FSCKTimeout != 4*time.Minute {
		t.Fatalf("initialized configuration: timeout=%s, error=%v", cfg.FSCKTimeout, err)
	}
	cfg.FSCKTimeout = time.Minute
	if err := applyFSCKTimeout(cfg); err != nil || cfg.FSCKTimeout != time.Minute {
		t.Fatalf("explicit caller budget was lost: timeout=%s, error=%v", cfg.FSCKTimeout, err)
	}
	cfg.FSCKTimeout = -time.Second
	if err := applyFSCKTimeout(cfg); err == nil {
		t.Fatal("negative caller budget was accepted")
	}
}

func TestFsckTimeoutDuration_RepositoryAndEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name, env string
		want      time.Duration
	}{
		{name: "persisted budget", want: 5 * time.Minute},
		{name: "legacy override", env: "2m", want: 2 * time.Minute},
		{name: "invalid override keeps repository budget", env: "bad", want: 5 * time.Minute},
		{name: "zero override keeps repository budget", env: "0", want: 5 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(fsckTimeoutEnv, tc.env)
			if got := fsckTimeoutDuration(5 * time.Minute); got != tc.want {
				t.Fatalf("timeout=%s, want %s", got, tc.want)
			}
		})
	}
}

func TestPrePushFSCK_RepositoryBudget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test command uses a POSIX shell")
	}
	t.Setenv(fsckTimeoutEnv, "")
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "mydb", ".dolt", "noms"), 0755); err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "dolt"), []byte("#!/bin/sh\nexec sleep 0.1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	s := &DoltStore{dbPath: dir, database: "mydb", fsckTimeout: 20 * time.Millisecond}
	if err := s.prePushFSCK(context.Background()); !errors.Is(err, ErrFSCKTimeout) {
		t.Fatalf("small repository budget should time out, got %v", err)
	}
	s.fsckTimeout = time.Second
	if err := s.prePushFSCK(context.Background()); err != nil {
		t.Fatalf("larger repository budget should pass, got %v", err)
	}
}
