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
)

// fsckUnknownConfigWarning is the warning dolt prints to stderr before running
// any command when a config file holds an option it does not know
// (cmd/dolt/dolt.go). It is not an integrity finding.
const fsckUnknownConfigWarning = "Warning: Unknown global config option 'sqlserver.foo'. " +
	"Use `dolt config --global --unset sqlserver.foo` to remove."

// fsckTestDBDir is the database directory the classifier tests pass, which an
// ErrFSCKFailed message names for a manual fsck.
const fsckTestDBDir = "/home/u/proj/.beads/dolt/proj"

// Finding fixtures in the shape dolt fsck prints them (cmd/dolt/commands/
// fsck.go: FsckReport.Print colors the report headers red; the corrupted
// journal warning is printed before any scan). Each one is an integrity
// finding that must refuse the push.
const (
	fsckCorruptChunksReport = "\x1b[31m============ Corrupt Chunks ============\x1b[0m\n" +
		"chunk 5c0bt2fvhq0lmqrs4v1ns0pgt1b3m0q8: checksum mismatch\n" +
		"============ Summary ============\n" +
		"Files with corruption:\n" +
		"  oldgen/2ah8d4kqmt3hbk3m7aep4t46r6ojv2j9: 1 chunk(s) with checksum errors"
	fsckCommitErrorsReport = "\x1b[31m============ Commit Errors ============\x1b[0m\n" +
		"::commit:9ujq4v7a6m1d2t0kh5u0jv0cb1d5s4mp: references missing parent " +
		"q1o0b6vn5hr7s2c4t8m3l9k0j1h2g3f4\n" +
		"============ Summary ============\n" +
		"Commit objects found: 12 (12 reachable from branches/tags/stashes, 0 unreachable)"
	fsckCorruptJournalWarning = "WARNING: Chunk journal is corrupted and some data may be lost.\n" +
		"Run `dolt fsck --revive-journal-with-data-loss` to attempt to recover the journal by\n" +
		"discarding invalid data blocks. Your original data will be preserved in a backup file."
)

// fsckFindingFixtures names every finding fixture for the table tests.
var fsckFindingFixtures = []struct {
	name   string
	output string
}{
	{"corrupt chunks", fsckCorruptChunksReport},
	{"commit errors", fsckCommitErrorsReport},
	{"corrupted journal", fsckCorruptJournalWarning},
}

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

// installFakeDolt puts an executable `dolt` running script first on PATH and
// returns a store whose CLI directory holds a .dolt/noms, so prePushFSCK runs
// the fake. Callers must not be parallel (t.Setenv).
func installFakeDolt(t *testing.T, script string) *DoltStore {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fake")
	}
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "dolt"), []byte("#!/bin/sh\n"+script), 0o755); err != nil { // #nosec G306 -- test fixture must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "mydb", ".dolt", "noms"), 0o755); err != nil {
		t.Fatal(err)
	}
	return &DoltStore{dbPath: tmp, database: "mydb"}
}

// TestPrePushFSCK_TimeoutAfterWarningIsTimeout verifies that an fsck which
// prints a startup warning and then runs past its own deadline is refused as
// ErrFSCKTimeout with BEADS_FSCK_TIMEOUT guidance, not reported as a dangling
// chunk reference: the warning is not an integrity finding.
func TestPrePushFSCK_TimeoutAfterWarningIsTimeout(t *testing.T) {
	// exec keeps sleep as the killed process, so no child holds the pipe.
	s := installFakeDolt(t, "echo \""+strings.ReplaceAll(fsckUnknownConfigWarning, "`", "\\`")+"\" >&2\nexec sleep 30\n")
	t.Setenv(fsckTimeoutEnv, "1s")

	err := s.prePushFSCK(context.Background())
	if errors.Is(err, ErrDanglingReference) {
		t.Fatalf("warning before the fsck deadline reported as corruption: %v", err)
	}
	if !errors.Is(err, ErrFSCKTimeout) {
		t.Fatalf("errors.Is(err, ErrFSCKTimeout) = false; err = %v", err)
	}
	if !strings.Contains(err.Error(), "BEADS_FSCK_TIMEOUT") {
		t.Errorf("timeout error %q does not name BEADS_FSCK_TIMEOUT", err)
	}
}

// TestPrePushFSCK_KilledWithoutOutputIsNotCorruption verifies that an fsck
// killed out from under bd with no output, while bd's own context is live, is
// not reported as a dangling chunk reference: nothing fsck said implicates the
// store.
func TestPrePushFSCK_KilledWithoutOutputIsNotCorruption(t *testing.T) {
	s := installFakeDolt(t, "kill -9 $$\n")

	err := s.prePushFSCK(context.Background())
	if err == nil {
		t.Fatal("a killed fsck must refuse the push, got nil")
	}
	if errors.Is(err, ErrDanglingReference) {
		t.Fatalf("killed fsck with no output reported as corruption: %v", err)
	}
	if !errors.Is(err, ErrFSCKFailed) {
		t.Fatalf("errors.Is(err, ErrFSCKFailed) = false; err = %v", err)
	}
	for _, want := range []string{"signal: killed", "(no output)", "not necessarily corrupt", "run `dolt fsck` in the database directory " + s.CLIDir()} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
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
			name:   "corrupt-chunks report (must still abort)",
			output: fsckCorruptChunksReport,
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
		runErr       error  // the fsck execution error
		dir          string // the database directory; fsckTestDBDir when empty
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
			name:      "finding output + DeadlineExceeded → ErrDanglingReference, not ErrFSCKTimeout",
			parentErr: context.DeadlineExceeded,
			fsckErr:   context.DeadlineExceeded,
			output:    fsckCorruptChunksReport,
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
		// (a→c) Interruption phrasing + parent deadline keeps caller-timeout
		//       guidance, not fsck's own BEADS_FSCK_TIMEOUT guidance.
		{
			name:         "output 'context deadline exceeded' + parent deadline → caller-timeout guidance",
			parentErr:    context.DeadlineExceeded,
			fsckErr:      context.DeadlineExceeded,
			output:       "context deadline exceeded",
			wantIs:       ErrFSCKTimeout,
			wantIsNot:    []error{ErrDanglingReference},
			wantContains: "dolt.auto-push-timeout",
			wantAbsent:   "BEADS_FSCK_TIMEOUT environment variable",
		},
		// (a→b) Could-not-open keeps its skip even when the parent context is
		//       canceled: the check never ran.
		{
			name:      "could-not-open + parent canceled → nil (skip preserved)",
			parentErr: context.Canceled,
			fsckErr:   context.Canceled,
			output:    "Could not open dolt database: some reason",
			wantNil:   true,
		},
		// (d) A non-finding warning printed before fsck's own deadline must
		//     not preempt the timeout classification.
		{
			name:         "warning output + own fsck timeout → ErrFSCKTimeout, not dangling",
			parentErr:    nil,
			fsckErr:      context.DeadlineExceeded,
			output:       fsckUnknownConfigWarning,
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
			output:    fsckCorruptChunksReport,
			wantIs:    ErrDanglingReference,
			wantIsNot: []error{ErrFSCKTimeout},
		},
		// (f) A failure with no finding, no recognized context error and no
		//     interruption or could-not-open output → ErrFSCKFailed: the push
		//     is still refused, but not reported as corruption.
		{
			name:         "generic failure (empty output, no ctx error) → ErrFSCKFailed",
			parentErr:    nil,
			fsckErr:      nil,
			runErr:       errors.New("exit status 1"),
			output:       "",
			wantIs:       ErrFSCKFailed,
			wantIsNot:    []error{ErrDanglingReference, ErrFSCKTimeout},
			wantContains: "exit status 1",
		},
		{
			name:         "generic failure reports explicit no-output",
			runErr:       errors.New("exit status 1"),
			output:       "",
			wantIs:       ErrFSCKFailed,
			wantIsNot:    []error{ErrDanglingReference},
			wantContains: "(no output)",
		},
		{
			name:         "global config load failure → ErrFSCKFailed with the output",
			runErr:       errors.New("exit status 1"),
			output:       "\x1b[31mFailed to load the global config. open /home/u/.dolt/config_global.json: permission denied\x1b[0m",
			wantIs:       ErrFSCKFailed,
			wantIsNot:    []error{ErrDanglingReference, ErrFSCKTimeout},
			wantContains: "Failed to load the global config",
		},
		{
			name:         "unknown output → ErrFSCKFailed, not corruption",
			runErr:       errors.New("exit status 1"),
			output:       "failed during full chunk scan: read oldgen/abc: input/output error",
			wantIs:       ErrFSCKFailed,
			wantIsNot:    []error{ErrDanglingReference, ErrFSCKTimeout},
			wantContains: "failed during full chunk scan",
		},
		{
			name:         "unknown output quoting dangling words → ErrFSCKFailed",
			runErr:       errors.New("exit status 1"),
			output:       "dangling chunk reference: hash abc123 not found",
			wantIs:       ErrFSCKFailed,
			wantIsNot:    []error{ErrDanglingReference},
			wantContains: "not necessarily corrupt",
		},
		{
			name:         "dolt not on PATH → ErrFSCKFailed naming the exec error",
			runErr:       &exec.Error{Name: "dolt", Err: exec.ErrNotFound},
			output:       "",
			wantIs:       ErrFSCKFailed,
			wantIsNot:    []error{ErrDanglingReference, ErrFSCKTimeout},
			wantContains: "executable file not found",
		},
		{
			name:         "warning output alone → ErrFSCKFailed, not corruption",
			runErr:       errors.New("exit status 1"),
			output:       fsckUnknownConfigWarning,
			wantIs:       ErrFSCKFailed,
			wantIsNot:    []error{ErrDanglingReference, ErrFSCKTimeout},
			wantContains: "Unknown global config option",
		},
		{
			name:         "generic failure names the database directory to run fsck in",
			runErr:       errors.New("exit status 1"),
			output:       fsckUnknownConfigWarning,
			wantIs:       ErrFSCKFailed,
			wantContains: "run `dolt fsck` in the database directory " + fsckTestDBDir + ";",
		},
		{
			name:         "database directory with a terminal escape is named escaped",
			runErr:       errors.New("exit status 1"),
			dir:          "/srv/x\x1b[2Ky/mydb",
			output:       "",
			wantIs:       ErrFSCKFailed,
			wantContains: `in the database directory "/srv/x\x1b[2Ky/mydb";`,
			wantAbsent:   "\x1b",
		},
		// fsck's unexpected-error path prints no report even for some real
		// damage (cmd/dolt/commands/fsck.go: commit DAG walking failed), so the
		// refusal warns against pushing around a manual fsck that fails so too.
		{
			name:         "unexpected-error output → ErrFSCKFailed warning against a direct dolt push",
			runErr:       errors.New("exit status 1"),
			output:       "commit DAG walking failed: store root chunk is empty",
			wantIs:       ErrFSCKFailed,
			wantIsNot:    []error{ErrDanglingReference, ErrFSCKTimeout},
			wantContains: "if it keeps failing there without a report, do not bypass the check with a direct `dolt push`",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := tc.dir
			if dir == "" {
				dir = fsckTestDBDir
			}
			err := classifyFSCKFailure(tc.parentErr, tc.fsckErr, tc.runErr, dir, tc.output)

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

// TestClassifyFSCKFailure_FindingWins verifies that a recognized fsck finding
// refuses the push as ErrDanglingReference with the finding output, whatever
// else happened: a deadline or cancellation, could-not-open text, or
// cancellation phrasing printed alongside the report must not hide it.
func TestClassifyFSCKFailure_FindingWins(t *testing.T) {
	t.Parallel()
	contexts := []struct {
		name      string
		parentErr error
		fsckErr   error
		extra     string
	}{
		{name: "no context error"},
		{name: "parent canceled", parentErr: context.Canceled, fsckErr: context.Canceled},
		{name: "parent deadline", parentErr: context.DeadlineExceeded, fsckErr: context.DeadlineExceeded},
		{name: "own deadline", fsckErr: context.DeadlineExceeded},
		{name: "could-not-open text", extra: "Could not open dolt database: oldgen: no such file or directory"},
		{name: "context canceled text", extra: "context canceled"},
		{name: "deadline text + own deadline", fsckErr: context.DeadlineExceeded, extra: "error: context deadline exceeded"},
		{name: "signal text", extra: "signal: killed"},
	}
	for _, f := range fsckFindingFixtures {
		for _, c := range contexts {
			f, c := f, c
			t.Run(f.name+"/"+c.name, func(t *testing.T) {
				t.Parallel()
				output := f.output
				if c.extra != "" {
					output = c.extra + "\n" + output
				}
				err := classifyFSCKFailure(c.parentErr, c.fsckErr, nil, fsckTestDBDir, output)
				if !errors.Is(err, ErrDanglingReference) {
					t.Fatalf("errors.Is(err, ErrDanglingReference) = false; err = %v", err)
				}
				if errors.Is(err, ErrFSCKTimeout) || errors.Is(err, context.Canceled) {
					t.Errorf("finding must not be reported as a timeout or interrupt; err = %v", err)
				}
				if !strings.Contains(err.Error(), f.output) {
					t.Errorf("error %q does not quote the finding output", err)
				}
			})
		}
	}
}

// TestClassifyFSCKFailure_NonFindingKeepsKind verifies that the supported
// skip, timeout and interruption outcomes keep their error kind and guidance
// when fsck output carries no finding, whether or not a non-finding warning
// precedes it: could-not-open still skips, a parent cancellation is still an
// interrupt, a caller deadline still points at the caller's timeout, fsck's
// own deadline still names BEADS_FSCK_TIMEOUT, and cancellation phrasing with
// healthy contexts is still an interrupt.
func TestClassifyFSCKFailure_NonFindingKeepsKind(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		parentErr    error
		fsckErr      error
		output       string
		wantNil      bool
		wantIs       error
		wantContains string
		wantAbsent   string
	}{
		{name: "could-not-open", output: "Could not open dolt database: some reason", wantNil: true},
		{name: "could-not-open + own deadline", fsckErr: context.DeadlineExceeded, output: "Could not open dolt database: some reason", wantNil: true},
		{name: "invalid repository state + parent canceled", parentErr: context.Canceled, fsckErr: context.Canceled, output: "The current directories repository state is invalid", wantNil: true},
		{name: "parent canceled", parentErr: context.Canceled, fsckErr: context.Canceled, wantIs: context.Canceled, wantContains: "interrupted"},
		{name: "parent deadline", parentErr: context.DeadlineExceeded, fsckErr: context.DeadlineExceeded, wantIs: ErrFSCKTimeout, wantContains: "dolt.auto-push-timeout", wantAbsent: "BEADS_FSCK_TIMEOUT environment variable"},
		{name: "own deadline", fsckErr: context.DeadlineExceeded, wantIs: ErrFSCKTimeout, wantContains: "BEADS_FSCK_TIMEOUT environment variable", wantAbsent: "dolt.auto-push-timeout"},
		{name: "context canceled text", output: "context canceled", wantIs: context.Canceled, wantContains: "interrupted"},
		{name: "signal terminated text", output: "signal: terminated", wantIs: context.Canceled, wantContains: "interrupted"},
	}
	for _, tc := range cases {
		for _, warn := range []bool{false, true} {
			tc, warn := tc, warn
			name := tc.name
			output := tc.output
			if warn {
				name += " after warning"
				output = strings.TrimSpace(fsckUnknownConfigWarning + "\n" + output)
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				err := classifyFSCKFailure(tc.parentErr, tc.fsckErr, nil, fsckTestDBDir, output)
				if tc.wantNil {
					if err != nil {
						t.Fatalf("want nil (skip), got %v", err)
					}
					return
				}
				if !errors.Is(err, tc.wantIs) {
					t.Fatalf("errors.Is(err, %v) = false; err = %v", tc.wantIs, err)
				}
				if errors.Is(err, ErrDanglingReference) {
					t.Errorf("non-finding output reported as corruption: %v", err)
				}
				if tc.wantContains != "" && !strings.Contains(err.Error(), tc.wantContains) {
					t.Errorf("error %q does not contain %q", err, tc.wantContains)
				}
				if tc.wantAbsent != "" && strings.Contains(err.Error(), tc.wantAbsent) {
					t.Errorf("error %q must not contain %q", err, tc.wantAbsent)
				}
			})
		}
	}
}

// TestFsckReportedFinding verifies the helper recognizes dolt fsck's report
// headers and corrupted-journal warning, including the ANSI color the headers
// carry, and does not treat a clean summary, warnings, empty output, or
// incidental "corrupt"/"dangling" wording as a finding.
func TestFsckReportedFinding(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		output string
		want   bool
	}{
		{"corrupt chunks report", fsckCorruptChunksReport, true},
		{"commit errors report alone", fsckCommitErrorsReport, true},
		{"corrupted journal warning", fsckCorruptJournalWarning, true},
		{"uncolored corrupt chunks header", "============ Corrupt Chunks ============\nchunk x: missing", true},
		{"clean summary", "============ Summary ============\nCommit objects found: 3 (3 reachable from branches/tags/stashes, 0 unreachable)\nNo problems found.", false},
		{"unknown config warning", "Warning: Unknown global config option 'sqlserver.foo'. Use `dolt config --global --unset sqlserver.foo` to remove.", false},
		{"global config load failure", "\x1b[31mFailed to load the global config. open /home/u/.dolt/config_global.json: permission denied\x1b[0m", false},
		{"empty output", "", false},
		{"incidental corrupt word in a path", "Could not open dolt database: open /srv/corrupt-backups/.dolt/noms: permission denied", false},
		{"incidental dangling words", "dangling chunk reference: hash abc123 referenced but not present", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := fsckReportedFinding(tc.output); got != tc.want {
				t.Errorf("fsckReportedFinding(%q) = %v, want %v", tc.output, got, tc.want)
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
			name:   "corrupt-chunks report is not an interrupt",
			output: fsckCorruptChunksReport,
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

	callerErr := classifyFSCKFailure(context.DeadlineExceeded, context.DeadlineExceeded, nil, fsckTestDBDir, "")
	ownErr := classifyFSCKFailure(nil, context.DeadlineExceeded, nil, fsckTestDBDir, "")

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
		if got := fsckTimeoutDuration(); got != 2*time.Minute {
			t.Errorf("want 2m, got %v", got)
		}
	})
	t.Run("unset returns default", func(t *testing.T) {
		t.Setenv(fsckTimeoutEnv, "")
		if got := fsckTimeoutDuration(); got != fsckTimeout {
			t.Errorf("want %v (default), got %v", fsckTimeout, got)
		}
	})
	t.Run("invalid returns default", func(t *testing.T) {
		t.Setenv(fsckTimeoutEnv, "not-a-duration")
		if got := fsckTimeoutDuration(); got != fsckTimeout {
			t.Errorf("want %v (default), got %v", fsckTimeout, got)
		}
	})
}
