//go:build cgo

package main

// GH#6163: maybeAutoExport's failure policy. Operational failures return an
// error the caller reports once (exit 1); deliberate safety refusals and an
// unsupported state hash stay warning-only skips. Kept apart from
// export_auto_test.go so patches appended to that file still apply.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// seedAutoExportFailureFixture makes an auto-export workspace whose JSONL
// holds seed (none when seed is "") and whose export-state.json holds state,
// and returns the JSONL path and the state file's exact bytes.
func seedAutoExportFailureFixture(t *testing.T, seed string, state *exportAutoState) (beadsDir, jsonl string, stateBytes []byte) {
	t.Helper()
	beadsDir = filepath.Join(autoExportTestDir(t), ".beads")
	jsonl = filepath.Join(beadsDir, "issues.jsonl")
	if seed != "" {
		if err := os.WriteFile(jsonl, []byte(seed), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	saveExportAutoState(beadsDir, state)
	stateBytes, err := os.ReadFile(filepath.Join(beadsDir, exportAutoStateFile))
	if err != nil {
		t.Fatal(err)
	}
	return beadsDir, jsonl, stateBytes
}

// assertAutoExportUntouched checks that a failed or skipped auto-export left
// the JSONL exactly as seeded (absent when seed is "") and export-state.json
// byte for byte, timestamp included, so the next eligible command retries.
func assertAutoExportUntouched(t *testing.T, beadsDir, jsonl, seed string, stateBytes []byte) {
	t.Helper()
	if seed == "" {
		if _, err := os.Stat(jsonl); !os.IsNotExist(err) {
			t.Fatalf("%s was created (stat err %v), want no JSONL", jsonl, err)
		}
	} else {
		assertFileUnchanged(t, jsonl, seed)
	}
	got, err := os.ReadFile(filepath.Join(beadsDir, exportAutoStateFile))
	if err != nil || !bytes.Equal(got, stateBytes) {
		t.Fatalf("export state changed: got %s (err %v), want %s", got, err, stateBytes)
	}
}

// failingStateHashStore is a StateHasher whose state hash read fails.
type failingStateHashStore struct {
	fakeStateHashStore
	err         error
	searchCalls int
}

func (f *failingStateHashStore) GetStateHash(context.Context) (string, error) {
	f.stateHashCalls++
	return "", f.err
}

func (f *failingStateHashStore) SearchIssues(ctx context.Context, q string, filter types.IssueFilter) ([]*types.Issue, error) {
	f.searchCalls++
	return f.fakeStateHashStore.SearchIssues(ctx, q, filter)
}

// failingHeadCommitStore is no StateHasher, so its failing GetCurrentCommit
// is the state hash read.
type failingHeadCommitStore struct {
	fakeHeadOnlyStore
	err error
}

func (f *failingHeadCommitStore) GetCurrentCommit(context.Context) (string, error) {
	f.currentCommitCalls++
	return "", f.err
}

// GH#6163: an ordinary state-hash failure fails the auto-export instead of
// warning and returning nil while the JSONL drifts from the store. It is
// checked before the throttle, so a recent export does not hide it.
func TestMaybeAutoExportFailsWhenStateHashUnreadable(t *testing.T) {
	cases := []struct {
		name  string
		store func(error) storage.DoltStorage
		seed  string
	}{
		{
			name:  "state hasher",
			store: func(err error) storage.DoltStorage { return &failingStateHashStore{err: err} },
			seed:  exportRelationSeed,
		},
		{
			name:  "head commit fallback",
			store: func(err error) storage.DoltStorage { return &failingHeadCommitStore{err: err} },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			initConfigForTest(t)
			config.Set("export.auto", true)
			saveAndRestoreGlobals(t)
			t.Setenv("BD_GIT_HOOK", "")
			sentinel := errors.New("injected state hash failure")
			store = tc.store(sentinel)
			beadsDir, jsonl, stateBytes := seedAutoExportFailureFixture(t, tc.seed, &exportAutoState{
				LastDoltCommit: "hash-before-write",
				Timestamp:      time.Now(),
			})

			var err error
			stderr := captureStderr(t, func() { err = maybeAutoExport(context.Background(), false) })
			if !errors.Is(err, sentinel) {
				t.Fatalf("maybeAutoExport err = %v, want errors.Is %v", err, sentinel)
			}
			if !strings.Contains(err.Error(), jsonl) {
				t.Fatalf("maybeAutoExport err = %q, want the export destination %q", err, jsonl)
			}
			if stderr != "" {
				t.Fatalf("stderr = %q, want nothing: the caller reports the failure once", stderr)
			}
			assertAutoExportUntouched(t, beadsDir, jsonl, tc.seed, stateBytes)
		})
	}
}

// queryFailingStore fails the failOn-th SearchIssues call. With one in-scope
// issue in both the store and the JSONL, call 1 is the reconciliation query
// and call 2 is the empty-overwrite check.
type queryFailingStore struct {
	fakeStateHashStore
	failOn      int
	err         error
	searchCalls int
}

func (f *queryFailingStore) SearchIssues(ctx context.Context, q string, filter types.IssueFilter) ([]*types.Issue, error) {
	f.searchCalls++
	if f.searchCalls == f.failOn {
		return nil, f.err
	}
	return f.fakeStateHashStore.SearchIssues(ctx, q, filter)
}

// GH#6163: every pre-export check failure — an unreadable or invalid JSONL,
// or a failing store query in either guard — fails the auto-export with the
// destination named, prints nothing itself (the caller reports it once), and
// leaves the JSONL and export state alone.
func TestMaybeAutoExportPrecheckFailureIsAnError(t *testing.T) {
	sentinel := errors.New("injected search failure")
	issue := &types.Issue{ID: "bd-export1", Title: "export me", Status: types.StatusOpen, IssueType: types.TypeTask}
	cases := []struct {
		name      string
		failOn    int
		seed      string
		dirAtPath bool
		wantErr   error
		wantCalls int
		wantText  string
	}{
		{name: "reconciliation query", failOn: 1, seed: exportRelationSeed, wantErr: sentinel, wantCalls: 1, wantText: "compare existing JSONL against local store"},
		{name: "empty check query", failOn: 2, seed: exportRelationSeed, wantErr: sentinel, wantCalls: 2, wantText: "check for an empty export over existing JSONL"},
		{name: "malformed JSONL", seed: "{not json\n", wantText: "compare existing JSONL against local store"},
		{name: "unreadable JSONL", dirAtPath: true, wantText: "compare existing JSONL against local store"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			initConfigForTest(t)
			config.Set("export.auto", true)
			saveAndRestoreGlobals(t)
			t.Setenv("BD_GIT_HOOK", "")
			fake := &queryFailingStore{
				fakeStateHashStore: fakeStateHashStore{stateHash: "hash-after-write", issues: []*types.Issue{issue}},
				failOn:             tc.failOn,
				err:                sentinel,
			}
			store = fake
			beadsDir, jsonl, stateBytes := seedAutoExportFailureFixture(t, tc.seed, &exportAutoState{LastDoltCommit: "hash-before-write"})
			if tc.dirAtPath {
				// Opens fine, then fails on read.
				if err := os.Mkdir(jsonl, 0o755); err != nil {
					t.Fatal(err)
				}
			}

			var err error
			stderr := captureStderr(t, func() { err = maybeAutoExport(context.Background(), false) })
			if err == nil {
				t.Fatal("maybeAutoExport err = nil, want the pre-export check failure")
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("maybeAutoExport err = %v, want errors.Is %v", err, tc.wantErr)
			}
			if !strings.Contains(err.Error(), jsonl) || !strings.Contains(err.Error(), tc.wantText) {
				t.Fatalf("maybeAutoExport err = %q, want %q naming %q", err, tc.wantText, jsonl)
			}
			if tc.wantCalls != 0 && fake.searchCalls != tc.wantCalls {
				t.Fatalf("SearchIssues calls = %d, want %d", fake.searchCalls, tc.wantCalls)
			}
			if stderr != "" {
				t.Fatalf("stderr = %q, want nothing: the caller reports the failure once", stderr)
			}
			if tc.dirAtPath {
				if fi, statErr := os.Stat(jsonl); statErr != nil || !fi.IsDir() {
					t.Fatalf("%s replaced (stat %v, err %v), want the directory left alone", jsonl, fi, statErr)
				}
				got, readErr := os.ReadFile(filepath.Join(beadsDir, exportAutoStateFile))
				if readErr != nil || !bytes.Equal(got, stateBytes) {
					t.Fatalf("export state changed: got %s (err %v), want %s", got, readErr, stateBytes)
				}
				return
			}
			assertAutoExportUntouched(t, beadsDir, jsonl, tc.seed, stateBytes)
		})
	}
}

// GH#6163: the deliberate pre-export refusals stay warning-only skips. Only
// an operational failure fails the command; a guard that refuses to
// overwrite records it cannot account for is doing its job.
func TestMaybeAutoExportGuardRefusalsStayWarnings(t *testing.T) {
	other := &types.Issue{ID: "bd-other", Title: "store only", Status: types.StatusOpen, IssueType: types.TypeTask}
	cases := []struct {
		name     string
		issues   []*types.Issue
		wantWarn string
	}{
		{name: "empty overwrite", wantWarn: "current database would export 0 issues"},
		{name: "unproven JSONL record", issues: []*types.Issue{other}, wantWarn: "JSONL-only issue record(s) absent from the local Dolt store (bd-export1)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			initConfigForTest(t)
			config.Set("export.auto", true)
			saveAndRestoreGlobals(t)
			t.Setenv("BD_GIT_HOOK", "")
			store = &fakeStateHashStore{stateHash: "hash-after-write", issues: tc.issues}
			beadsDir, jsonl, stateBytes := seedAutoExportFailureFixture(t, exportRelationSeed, &exportAutoState{LastDoltCommit: "hash-before-write"})

			var err error
			stderr := captureStderr(t, func() { err = maybeAutoExport(context.Background(), false) })
			if err != nil {
				t.Fatalf("maybeAutoExport err = %v, want nil: a guard refusal is a skip", err)
			}
			if !strings.Contains(stderr, "Warning: auto-export skipped: ") || !strings.Contains(stderr, tc.wantWarn) {
				t.Fatalf("stderr = %q, want the %q refusal warning", stderr, tc.wantWarn)
			}
			assertAutoExportUntouched(t, beadsDir, jsonl, exportRelationSeed, stateBytes)
		})
	}
}

// GH#6163: the shrink guard's refusal to overwrite a JSONL holding records
// outside auto-export scope (here a memory) is reached through the full
// export but stays a warning-only skip, unlike the export's real failures.
func TestMaybeAutoExportShrinkGuardRefusalStaysWarning(t *testing.T) {
	initConfigForTest(t)
	config.Set("export.auto", true)
	saveAndRestoreGlobals(t)
	t.Setenv("BD_GIT_HOOK", "")
	// fakeStateHashStore is no DiffStore, so the export is a full one.
	store = &fakeStateHashStore{
		stateHash: "hash-after-write",
		issues:    []*types.Issue{{ID: "bd-export1", Title: "export me", Status: types.StatusOpen, IssueType: types.TypeTask}},
	}
	seed := exportRelationSeed + `{"_type":"memory","key":"private-note","value":"agent context"}` + "\n"
	beadsDir, jsonl, stateBytes := seedAutoExportFailureFixture(t, seed, &exportAutoState{LastDoltCommit: "hash-before-write"})

	var err error
	stderr := captureStderr(t, func() { err = maybeAutoExport(context.Background(), false) })
	if err != nil {
		t.Fatalf("maybeAutoExport err = %v, want nil: the shrink guard refusal is a skip", err)
	}
	if want := "Warning: auto-export failed: auto-export shrink guard: refusing to overwrite " + jsonl; strings.Count(stderr, want) != 1 {
		t.Fatalf("stderr = %q, want exactly one %q warning", stderr, want)
	}
	assertAutoExportUntouched(t, beadsDir, jsonl, seed, stateBytes)
}

// GH#6163: a backend that reports the state hash as a typed unsupported
// capability (the HTTP client's refusal, wrapped) has nothing to detect
// changes with. That stays a warning-only skip that reads nothing else and
// writes nothing.
func TestMaybeAutoExportStateHashUnsupportedSkips(t *testing.T) {
	initConfigForTest(t)
	config.Set("export.auto", true)
	saveAndRestoreGlobals(t)
	t.Setenv("BD_GIT_HOOK", "")
	fake := &failingStateHashStore{err: fmt.Errorf("http backend: %w", &storage.ErrUnsupported{Op: "GetStateHash", Backend: "http"})}
	store = fake
	beadsDir, jsonl, stateBytes := seedAutoExportFailureFixture(t, exportRelationSeed, &exportAutoState{LastDoltCommit: "hash-before-write"})

	var err error
	stderr := captureStderr(t, func() { err = maybeAutoExport(context.Background(), false) })
	if err != nil {
		t.Fatalf("maybeAutoExport err = %v, want nil: an unsupported state hash is a skip", err)
	}
	if !strings.Contains(stderr, "Warning: auto-export skipped: ") || !strings.Contains(stderr, "GetStateHash") {
		t.Fatalf("stderr = %q, want the skip warning naming the unsupported operation", stderr)
	}
	if fake.searchCalls != 0 {
		t.Fatalf("SearchIssues calls = %d, want 0: the skip happens before any export work", fake.searchCalls)
	}
	assertAutoExportUntouched(t, beadsDir, jsonl, exportRelationSeed, stateBytes)
}

// GH#6163: a failed export leaves the JSONL and export state alone, so the
// next eligible command retries it without any new retry mechanism and
// succeeds once the fault is gone.
func TestMaybeAutoExportRetriesAfterFailure(t *testing.T) {
	initConfigForTest(t)
	config.Set("export.auto", true)
	saveAndRestoreGlobals(t)
	t.Setenv("BD_GIT_HOOK", "")
	store = &failingStateHashStore{err: errors.New("injected state hash failure")}
	// A zero timestamp leaves the throttle window open.
	beadsDir, jsonl, stateBytes := seedAutoExportFailureFixture(t, exportRelationSeed, &exportAutoState{LastDoltCommit: "hash-before-write"})

	var err error
	_ = captureStderr(t, func() { err = maybeAutoExport(context.Background(), false) })
	if err == nil {
		t.Fatal("first run: maybeAutoExport err = nil, want the state-hash failure")
	}
	assertAutoExportUntouched(t, beadsDir, jsonl, exportRelationSeed, stateBytes)

	store = &fakeHeadOnlyStore{issues: []*types.Issue{
		{ID: "bd-export1", Title: "export me", Status: types.StatusOpen, IssueType: types.TypeTask},
	}}
	if err := maybeAutoExport(context.Background(), false); err != nil {
		t.Fatalf("retry after failure: maybeAutoExport: %v", err)
	}
	if got := jsonlRecordsByID(t, jsonl); got["bd-export1"] == nil {
		t.Fatalf("retry did not export bd-export1: %v", got)
	}
	if state := loadExportAutoState(beadsDir); state.LastDoltCommit != "head-commit-hash" || state.Timestamp.IsZero() {
		t.Fatalf("retry did not advance export state: %+v", state)
	}
}

// closeCountingStateHashStore is a failingStateHashStore that PostRunE's
// deferred cleanup can close.
type closeCountingStateHashStore struct {
	failingStateHashStore
	closeCalls int
}

func (f *closeCountingStateHashStore) Close() error {
	f.closeCalls++
	return nil
}

// GH#6163, end to end through PersistentPostRunE: a mutating command whose
// real auto-export fails exits 1, reports the failure exactly once, and
// still runs its deferred cleanup (context cancelled and cleared, store
// closed).
func TestPersistentPostRunE_ReportsAutoExportFailureOnce(t *testing.T) {
	initConfigForTest(t)
	config.Set("export.auto", true)
	saveAndRestoreGlobals(t)
	snapshotRootContext(t)
	t.Setenv("BD_GIT_HOOK", "")
	testModeUseGlobals = true
	readonlyMode = false // let the maintenance block run

	oldCommit, oldBackup, oldExport, oldPush := runPostRunAutoCommit, runPostRunAutoBackup, runPostRunAutoExport, runPostRunAutoPush
	t.Cleanup(func() {
		runPostRunAutoCommit, runPostRunAutoBackup, runPostRunAutoExport, runPostRunAutoPush = oldCommit, oldBackup, oldExport, oldPush
	})
	runPostRunAutoCommit = func(context.Context, doltAutoCommitParams) error { return nil }
	runPostRunAutoBackup = func(context.Context) {}
	runPostRunAutoExport = maybeAutoExport
	pushCalls := 0
	runPostRunAutoPush = func(context.Context) { pushCalls++ }

	sentinel := errors.New("injected state hash failure")
	fake := &closeCountingStateHashStore{failingStateHashStore: failingStateHashStore{err: sentinel}}
	store = fake
	beadsDir, jsonl, stateBytes := seedAutoExportFailureFixture(t, exportRelationSeed, &exportAutoState{LastDoltCommit: "hash-before-write"})

	ctx, cancel := context.WithCancel(context.Background())
	setRootContext(ctx, cancel)

	var err error
	stderr := captureStderr(t, func() { err = rootCmd.PersistentPostRunE(&cobra.Command{Use: "create"}, nil) })
	if code, ok := exitCodeFromError(err); !ok || code != 1 {
		t.Fatalf("PersistentPostRunE err = %v (exit code %d, reported %v), want exit 1", err, code, ok)
	}
	if strings.Count(stderr, "Error: ") != 1 || strings.Count(stderr, sentinel.Error()) != 1 || !strings.Contains(stderr, jsonl) {
		t.Fatalf("stderr = %q, want one Error line naming %q and %q", stderr, sentinel, jsonl)
	}
	if strings.Contains(stderr, "Warning: auto-export") {
		t.Fatalf("stderr = %q, want no auto-export warning beside the error", stderr)
	}
	if pushCalls != 0 {
		t.Errorf("auto-push ran %d time(s) after the failed export, want 0", pushCalls)
	}
	if err := ctx.Err(); !errors.Is(err, context.Canceled) {
		t.Errorf("post-run must cancel the context on the error path, got %v", err)
	}
	if rootCtx != nil || rootCancel != nil {
		t.Errorf("post-run must clear the root context on the error path, got ctx=%v cancel!=nil=%v", rootCtx, rootCancel != nil)
	}
	if fake.closeCalls != 1 || store != nil {
		t.Errorf("store close calls = %d, store = %v, want closed once and cleared", fake.closeCalls, store)
	}
	assertAutoExportUntouched(t, beadsDir, jsonl, exportRelationSeed, stateBytes)
}
