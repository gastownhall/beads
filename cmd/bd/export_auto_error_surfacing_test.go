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

// preparePostRunE lets rootCmd.PersistentPostRunE run its maintenance block
// under a fresh root context, restoring both when the test ends.
func preparePostRunE(t *testing.T) context.Context {
	t.Helper()
	snapshotRootContext(t)
	testModeUseGlobals = true
	readonlyMode = false // let the maintenance block run
	ctx, cancel := context.WithCancel(context.Background())
	setRootContext(ctx, cancel)
	return ctx
}

// stubPostRunMaintenance stubs PersistentPostRunE's maintenance until the test
// ends: auto-commit and backup do nothing, auto-export runs export, and
// auto-push only counts its runs, which it returns.
func stubPostRunMaintenance(t *testing.T, export func(context.Context, bool) error) *int {
	t.Helper()
	oldCommit, oldBackup, oldExport, oldPush := runPostRunAutoCommit, runPostRunAutoBackup, runPostRunAutoExport, runPostRunAutoPush
	t.Cleanup(func() {
		runPostRunAutoCommit, runPostRunAutoBackup, runPostRunAutoExport, runPostRunAutoPush = oldCommit, oldBackup, oldExport, oldPush
	})
	runPostRunAutoCommit = func(context.Context, doltAutoCommitParams) error { return nil }
	runPostRunAutoBackup = func(context.Context) {}
	runPostRunAutoExport = export
	pushCalls := 0
	runPostRunAutoPush = func(context.Context) { pushCalls++ }
	return &pushCalls
}

// GH#6163, end to end through PersistentPostRunE: a mutating command whose
// real auto-export fails exits 1 and reports the failure exactly once, with a
// hint that the command's change is saved. Cobra is silenced, so a command
// that does not silence itself gets no second "Error:" line or usage text.
// Its deferred cleanup still runs (context cancelled and cleared, store
// closed).
func TestPersistentPostRunE_ReportsAutoExportFailureOnce(t *testing.T) {
	initConfigForTest(t)
	config.Set("export.auto", true)
	saveAndRestoreGlobals(t)
	t.Setenv("BD_GIT_HOOK", "")
	ctx := preparePostRunE(t)
	pushCalls := stubPostRunMaintenance(t, maybeAutoExport)

	sentinel := errors.New("injected state hash failure")
	fake := &closeCountingStateHashStore{failingStateHashStore: failingStateHashStore{err: sentinel}}
	store = fake
	beadsDir, jsonl, stateBytes := seedAutoExportFailureFixture(t, exportRelationSeed, &exportAutoState{LastDoltCommit: "hash-before-write"})

	cmd := &cobra.Command{Use: "create"}
	var err error
	stderr := captureStderr(t, func() { err = rootCmd.PersistentPostRunE(cmd, nil) })
	if code, ok := exitCodeFromError(err); !ok || code != 1 {
		t.Fatalf("PersistentPostRunE err = %v (exit code %d, reported %v), want exit 1", err, code, ok)
	}
	if strings.Count(stderr, "Error: ") != 1 || strings.Count(stderr, sentinel.Error()) != 1 || !strings.Contains(stderr, jsonl) {
		t.Fatalf("stderr = %q, want one Error line naming %q and %q", stderr, sentinel, jsonl)
	}
	if strings.Count(stderr, "Hint: ") != 1 || !strings.Contains(stderr, "any change this command made is saved") {
		t.Fatalf("stderr = %q, want one hint saying the command's change is saved", stderr)
	}
	if !cmd.SilenceErrors || !cmd.SilenceUsage {
		t.Errorf("SilenceErrors = %v, SilenceUsage = %v, want both set so cobra prints neither the error nor the usage again", cmd.SilenceErrors, cmd.SilenceUsage)
	}
	if strings.Contains(stderr, "Warning: auto-export") {
		t.Fatalf("stderr = %q, want no auto-export warning beside the error", stderr)
	}
	if *pushCalls != 0 {
		t.Errorf("auto-push ran %d time(s) after the failed export, want 0", *pushCalls)
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

// conflictedJSONL is an issues.jsonl left with git merge conflict markers,
// which the pre-export reconciliation cannot parse.
const conflictedJSONL = "<<<<<<< HEAD\n{\"id\":\"bd-main\"}\n=======\n{\"id\":\"bd-branch\"}\n>>>>>>> branch\n"

// setAutoExportBaselineForTest sets commandAutoExportBaseline, as the end of
// PersistentPreRunE would, until the test ends.
func setAutoExportBaselineForTest(t *testing.T, hash string) {
	t.Helper()
	old := commandAutoExportBaseline
	t.Cleanup(func() { commandAutoExportBaseline = old })
	commandAutoExportBaseline = hash
}

// newDryRunCommand returns a command invoked with --dry-run.
func newDryRunCommand(t *testing.T) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "create"}
	cmd.Flags().Bool("dry-run", false, "")
	if err := cmd.Flags().Set("dry-run", "true"); err != nil {
		t.Fatal(err)
	}
	return cmd
}

// GH#6163: when the state hash still matches the baseline read before
// the command ran, the command changed nothing the JSONL tracks. The export
// was failing already and none of the command's changes are missing from the
// JSONL, so each failure that follows the state hash read stays a warning
// with exit 0, and leaves the JSONL and export state for the next command to
// retry. A changed or unknown baseline still fails.
func TestMaybeAutoExportFailureOfUnchangedStoreWarns(t *testing.T) {
	issue := &types.Issue{ID: "bd-export1", Title: "export me", Status: types.StatusOpen, IssueType: types.TypeTask}
	cases := []struct {
		name     string
		store    func() storage.DoltStorage
		seed     string
		wantText string
	}{
		{
			name:     "pre-export check",
			store:    func() storage.DoltStorage { return &fakeStateHashStore{stateHash: "hash-after-write"} },
			seed:     conflictedJSONL,
			wantText: "compare existing JSONL against local store",
		},
		{
			name: "empty check query",
			store: func() storage.DoltStorage {
				return &queryFailingStore{
					fakeStateHashStore: fakeStateHashStore{stateHash: "hash-after-write", issues: []*types.Issue{issue}},
					failOn:             2,
					err:                errors.New("injected search failure"),
				}
			},
			seed:     exportRelationSeed,
			wantText: "check for an empty export over existing JSONL",
		},
		{
			// Its GetCurrentCommit, "hash-after-write", is its state hash.
			name:     "export itself",
			store:    func() storage.DoltStorage { return &failingExportRelationStore{fail: "labels"} },
			seed:     exportRelationSeed,
			wantText: "load labels",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			initConfigForTest(t)
			config.Set("export.auto", true)
			saveAndRestoreGlobals(t)
			t.Setenv("BD_GIT_HOOK", "")
			beadsDir, jsonl, stateBytes := seedAutoExportFailureFixture(t, tc.seed, &exportAutoState{LastDoltCommit: "hash-before-write"})

			store = tc.store()
			setAutoExportBaselineForTest(t, "hash-after-write")
			var err error
			stderr := captureStderr(t, func() { err = maybeAutoExport(context.Background(), false) })
			if err != nil {
				t.Fatalf("unchanged store: maybeAutoExport err = %v, want nil", err)
			}
			if want := "Warning: auto-export to " + jsonl + ": " + tc.wantText; strings.Count(stderr, want) != 1 || strings.Count(stderr, "Warning: ") != 1 {
				t.Fatalf("unchanged store: stderr = %q, want exactly one %q warning", stderr, want)
			}
			assertAutoExportUntouched(t, beadsDir, jsonl, tc.seed, stateBytes)

			for _, baseline := range []string{"hash-before-write", ""} {
				store = tc.store()
				commandAutoExportBaseline = baseline
				stderr = captureStderr(t, func() { err = maybeAutoExport(context.Background(), false) })
				if err == nil || !strings.Contains(err.Error(), tc.wantText) {
					t.Fatalf("baseline %q: maybeAutoExport err = %v, want the %q failure", baseline, err, tc.wantText)
				}
				if stderr != "" {
					t.Fatalf("baseline %q: stderr = %q, want nothing: the caller reports the failure once", baseline, stderr)
				}
				assertAutoExportUntouched(t, beadsDir, jsonl, tc.seed, stateBytes)
			}
		})
	}
}

// GH#6163: a state hash that cannot be read cannot show that the
// command changed nothing, so that failure fails even with a baseline.
func TestMaybeAutoExportStateHashFailureFailsDespiteBaseline(t *testing.T) {
	initConfigForTest(t)
	config.Set("export.auto", true)
	saveAndRestoreGlobals(t)
	t.Setenv("BD_GIT_HOOK", "")
	sentinel := errors.New("injected state hash failure")
	store = &failingStateHashStore{err: sentinel}
	setAutoExportBaselineForTest(t, "hash-before-write")
	beadsDir, jsonl, stateBytes := seedAutoExportFailureFixture(t, exportRelationSeed, &exportAutoState{LastDoltCommit: "hash-before-write"})

	var err error
	stderr := captureStderr(t, func() { err = maybeAutoExport(context.Background(), false) })
	if !errors.Is(err, sentinel) {
		t.Fatalf("maybeAutoExport err = %v, want errors.Is %v", err, sentinel)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want nothing: the caller reports the failure once", stderr)
	}
	assertAutoExportUntouched(t, beadsDir, jsonl, exportRelationSeed, stateBytes)
}

// captureAutoExportBaseline reads the state hash only for an invocation whose
// post-run auto-export may run. An unreadable hash leaves the baseline
// unknown, so a later export failure fails as it would after a change.
func TestCaptureAutoExportBaseline(t *testing.T) {
	create := &cobra.Command{Use: "create"}
	cases := []struct {
		name       string
		exportAuto bool
		noStore    bool
		cmd        *cobra.Command
		hashErr    error
		want       string
		wantReads  int
	}{
		{name: "write command", exportAuto: true, cmd: create, want: "hash-before-write", wantReads: 1},
		{name: "unreadable state hash", exportAuto: true, cmd: create, hashErr: errors.New("injected state hash failure"), wantReads: 1},
		{name: "export.auto off", cmd: create},
		{name: "no store", exportAuto: true, noStore: true, cmd: create},
		{name: "read-only command", exportAuto: true, cmd: &cobra.Command{Use: "list"}},
		{name: "preview", exportAuto: true, cmd: newDryRunCommand(t)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			initConfigForTest(t)
			config.Set("export.auto", tc.exportAuto)
			saveAndRestoreGlobals(t)
			setAutoExportBaselineForTest(t, "")
			hashes := &fakeStateHashStore{stateHash: "hash-before-write"}
			reads := &hashes.stateHashCalls
			store = hashes
			if tc.hashErr != nil {
				failing := &failingStateHashStore{err: tc.hashErr}
				store, reads = failing, &failing.stateHashCalls
			}
			if tc.noStore {
				store = nil
			}

			captureAutoExportBaseline(context.Background(), tc.cmd, nil)
			if commandAutoExportBaseline != tc.want || *reads != tc.wantReads {
				t.Fatalf("baseline = %q after %d state hash read(s), want %q after %d", commandAutoExportBaseline, *reads, tc.want, tc.wantReads)
			}
		})
	}
}

// closableStateHashStore is a fakeStateHashStore that PostRunE can close.
type closableStateHashStore struct {
	fakeStateHashStore
	closeCalls int
}

func (f *closableStateHashStore) Close() error {
	f.closeCalls++
	return nil
}

// GH#6163, end to end from the baseline PersistentPreRunE records to
// PersistentPostRunE: a failed export after a command that changed nothing
// (a read outside the read-only list) warns, exits 0 and lets the rest of
// post-run, auto-push included, run. The same failure after the command's
// own write exits 1.
func TestPersistentPostRunE_AutoExportFailureFollowsTheCommandsChange(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cmd   string
		wrote bool
	}{
		{name: "read changed nothing", cmd: "memories"},
		{name: "write", cmd: "create", wrote: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			initConfigForTest(t)
			config.Set("export.auto", true)
			saveAndRestoreGlobals(t)
			setAutoExportBaselineForTest(t, "")
			t.Setenv("BD_GIT_HOOK", "")
			ctx := preparePostRunE(t)
			pushCalls := stubPostRunMaintenance(t, maybeAutoExport)
			fake := &closableStateHashStore{fakeStateHashStore: fakeStateHashStore{stateHash: "hash-before-command"}}
			store = fake
			beadsDir, jsonl, stateBytes := seedAutoExportFailureFixture(t, conflictedJSONL, &exportAutoState{LastDoltCommit: "hash-of-last-export"})

			cmd := &cobra.Command{Use: tc.cmd}
			captureAutoExportBaseline(ctx, cmd, nil)
			if tc.wrote {
				fake.stateHash = "hash-after-command"
			}
			var err error
			stderr := captureStderr(t, func() { err = rootCmd.PersistentPostRunE(cmd, nil) })

			if tc.wrote {
				if code, ok := exitCodeFromError(err); !ok || code != 1 {
					t.Fatalf("PersistentPostRunE err = %v (exit code %d, reported %v), want exit 1", err, code, ok)
				}
				if strings.Count(stderr, "Error: auto-export to "+jsonl) != 1 || strings.Contains(stderr, "Warning: ") {
					t.Fatalf("stderr = %q, want one auto-export Error and no warning", stderr)
				}
				if *pushCalls != 0 {
					t.Errorf("auto-push ran %d time(s) after the failed export, want 0", *pushCalls)
				}
			} else {
				if err != nil {
					t.Fatalf("PersistentPostRunE err = %v, want nil: the command changed nothing", err)
				}
				if strings.Count(stderr, "Warning: auto-export to "+jsonl+": compare existing JSONL") != 1 || strings.Contains(stderr, "Error: ") {
					t.Fatalf("stderr = %q, want one auto-export warning and no error", stderr)
				}
				if *pushCalls != 1 {
					t.Errorf("auto-push ran %d time(s), want 1: the rest of post-run still runs", *pushCalls)
				}
			}
			if fake.closeCalls != 1 || store != nil {
				t.Errorf("store close calls = %d, store = %v, want closed once and cleared", fake.closeCalls, store)
			}
			assertAutoExportUntouched(t, beadsDir, jsonl, conflictedJSONL, stateBytes)
		})
	}
}

// GH#6163: the post-run auto-export gate also skips a preview, which
// promised not to mutate, and the export.auto off switch, whose config.yaml
// write this process does not read back before post-run.
func TestShouldRunPostCommandAutoExportSkipsPreviewsAndTheOffSwitch(t *testing.T) {
	inspect := &cobra.Command{Use: "update"}
	inspect.Flags().Bool("inspect", false, "")
	if err := inspect.Flags().Set("inspect", "true"); err != nil {
		t.Fatal(err)
	}
	dryRunOff := &cobra.Command{Use: "create"}
	dryRunOff.Flags().Bool("dry-run", false, "")
	configCmd := &cobra.Command{Use: "config"}
	setCmd := &cobra.Command{Use: "set"}
	setManyCmd := &cobra.Command{Use: "set-many"}
	configCmd.AddCommand(setCmd, setManyCmd)

	tests := []struct {
		name string
		cmd  *cobra.Command
		args []string
		want bool
	}{
		{name: "dry-run preview", cmd: newDryRunCommand(t), want: false},
		{name: "inspect preview", cmd: inspect, want: false},
		{name: "dry-run flag not given", cmd: dryRunOff, want: true},
		{name: "config set export.auto false", cmd: setCmd, args: []string{"export.auto", "false"}, want: false},
		{name: "config set-many export.auto=0", cmd: setManyCmd, args: []string{"export.interval=1m", "export.auto=0"}, want: false},
		{name: "config set export.auto true", cmd: setCmd, args: []string{"export.auto", "true"}, want: true},
		{name: "config set import.auto false", cmd: setCmd, args: []string{"import.auto", "false"}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldRunPostCommandAutoExport(tt.cmd, tt.args); got != tt.want {
				t.Fatalf("shouldRunPostCommandAutoExport(%q, %v) = %v, want %v", tt.cmd.CommandPath(), tt.args, got, tt.want)
			}
		})
	}
}

// GH#6163: PersistentPostRunE hands its args to that gate, so a
// preview and the off switch run no export at all, and so cannot fail one.
func TestPersistentPostRunE_SkipsAutoExportForPreviewsAndTheOffSwitch(t *testing.T) {
	configCmd := &cobra.Command{Use: "config"}
	setCmd := &cobra.Command{Use: "set"}
	configCmd.AddCommand(setCmd)

	for _, tc := range []struct {
		name string
		cmd  *cobra.Command
		args []string
		want int
	}{
		{name: "dry-run preview", cmd: newDryRunCommand(t)},
		{name: "config set export.auto false", cmd: setCmd, args: []string{"export.auto", "false"}},
		{name: "config set export.auto true", cmd: setCmd, args: []string{"export.auto", "true"}, want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			initConfigForTest(t)
			saveAndRestoreGlobals(t)
			store = nil
			preparePostRunE(t)
			exports := 0
			stubPostRunMaintenance(t, func(context.Context, bool) error {
				exports++
				return errors.New("injected auto-export failure")
			})

			var err error
			_ = captureStderr(t, func() { err = rootCmd.PersistentPostRunE(tc.cmd, tc.args) })
			if exports != tc.want || (err != nil) != (tc.want > 0) {
				t.Fatalf("auto-export ran %d time(s), PersistentPostRunE err = %v; want %d run(s), failing only if it ran", exports, err, tc.want)
			}
		})
	}
}
