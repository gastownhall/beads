package main

// GH#6163 tests for the auto-export failure policy: maybeAutoExport must
// return operational failures instead of warning and exiting 0 while the
// JSONL silently drifts from the store. Deliberate safety refusals and
// unsupported state-hash capabilities stay warning-only skips.
//
// This file is kept separate from export_auto_test.go so the checkpoint
// pipeline can re-apply its triage repro (which appends to that file)
// without patch conflicts.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// stateHashReadErrStore fails the StateHasher read maybeAutoExport uses for
// change detection, while keeping the GetCurrentCommit surface intact so the
// failure is attributable to the state hash alone.
type stateHashReadErrStore struct {
	fakeHeadOnlyStore
}

func (f *stateHashReadErrStore) GetStateHash(_ context.Context) (string, error) {
	return "", errStateHashRead
}

// headCommitReadErrStore does not implement storage.StateHasher and fails
// GetCurrentCommit, so storeStateHash fails through its fallback arm.
type headCommitReadErrStore struct {
	fakeHeadOnlyStore
}

func (f *headCommitReadErrStore) GetCurrentCommit(_ context.Context) (string, error) {
	f.currentCommitCalls++
	return "", errHeadCommitRead
}

var (
	errStateHashRead  = errors.New("state hash unavailable")
	errHeadCommitRead = errors.New("current commit unavailable")
)

// TestMaybeAutoExportFailsWhenStateHashUnreadable pins AC-1 of GH#6163: an
// operational state-hash failure returns a contextual error wrapping the
// cause and naming the export destination, leaves the existing JSONL and
// export state byte-for-byte unchanged, and prints no warning itself (the
// caller's HandleError is the single print). The seeded recent Timestamp
// pins the ordering: the state hash is read before the throttle, so the
// failure must surface even while the export interval window is closed.
func TestMaybeAutoExportFailsWhenStateHashUnreadable(t *testing.T) {
	cases := []struct {
		name      string
		store     storage.DoltStorage
		wantCause error
	}{
		{"state hasher", &stateHashReadErrStore{}, errStateHashRead},
		{"GetCurrentCommit fallback", &headCommitReadErrStore{}, errHeadCommitRead},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			initConfigForTest(t)
			config.Set("export.auto", true)
			saveAndRestoreGlobals(t)
			t.Setenv("BD_GIT_HOOK", "")
			store = tc.store

			dir := autoExportTestDir(t)
			beadsDir := filepath.Join(dir, ".beads")
			seedJSONL := filepath.Join(beadsDir, "issues.jsonl")
			seed := `{"_type":"issue","id":"bd-seed","issue_type":"task","title":"seed"}` + "\n"
			if err := os.WriteFile(seedJSONL, []byte(seed), 0o600); err != nil {
				t.Fatal(err)
			}
			saveExportAutoState(beadsDir, &exportAutoState{
				LastDoltCommit: "hash-before-write",
				LastDiffAnchor: "anchor-before-write",
				Timestamp:      time.Now(),
				Issues:         1,
			})
			seedState := filepath.Join(beadsDir, exportAutoStateFile)
			seedStateBytes, err := os.ReadFile(seedState)
			if err != nil {
				t.Fatal(err)
			}

			var got error
			stderr := captureStderr(t, func() { got = maybeAutoExport(context.Background(), false) })
			if !errors.Is(got, tc.wantCause) {
				t.Fatalf("maybeAutoExport err = %v, want it to wrap %v", got, tc.wantCause)
			}
			if !strings.Contains(got.Error(), seedJSONL) {
				t.Fatalf("error %q must name the resolved export destination %q", got.Error(), seedJSONL)
			}
			if strings.Contains(stderr, "Warning") {
				t.Fatalf("maybeAutoExport printed %q; the error path must leave the single print to the caller", stderr)
			}
			assertFileUnchanged(t, seedJSONL, seed)
			assertFileUnchanged(t, seedState, string(seedStateBytes))
		})
	}
}

// --- AC-2: pre-export check failures are errors, not silent skips ---

var errPrecheckQuery = errors.New("precheck query failed")

// precheckErrStore fails every SearchIssues call, so the reconciliation
// sweep at the top of the pre-export checks errors out.
type precheckErrStore struct {
	fakeHeadOnlyStore
}

func (f *precheckErrStore) SearchIssues(context.Context, string, types.IssueFilter) ([]*types.Issue, error) {
	return nil, errPrecheckQuery
}

// emptyCheckErrStore serves the first SearchIssues call (reconciliation) and
// fails the second (shouldSkipEmptyAutoExport's filtered query), so the
// empty-overwrite check's error branch executes while reconciliation
// completes. Calls are distinguished by number: the reconciliation sweep is
// always first, and with no unproven ids the empty check is always next.
type emptyCheckErrStore struct {
	fakeHeadOnlyStore
	searchCalls int
}

func (f *emptyCheckErrStore) SearchIssues(context.Context, string, types.IssueFilter) ([]*types.Issue, error) {
	f.searchCalls++
	if f.searchCalls < 2 {
		// The seed record is present in the store, so reconciliation finds
		// no JSONL-only ids and the guards move on to the empty check.
		return []*types.Issue{{ID: "bd-seed", Title: "seed", Status: types.StatusOpen, IssueType: types.TypeTask}}, nil
	}
	return nil, errPrecheckQuery
}

// setupDueAutoExport prepares a beads dir whose auto-export is enabled and
// due, installs s as the store, seeds issues.jsonl with jsonlSeed and
// export-state.json with a prior export, and returns the JSONL path, the
// state-file path and the pre-run state bytes for unmodified-state
// assertions.
func setupDueAutoExport(t *testing.T, s storage.DoltStorage, jsonlSeed string) (jsonl, statePath, stateBytes string) {
	t.Helper()
	initConfigForTest(t)
	config.Set("export.auto", true)
	saveAndRestoreGlobals(t)
	t.Setenv("BD_GIT_HOOK", "")
	store = s

	dir := autoExportTestDir(t)
	beadsDir := filepath.Join(dir, ".beads")
	jsonl = filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(jsonl, []byte(jsonlSeed), 0o600); err != nil {
		t.Fatal(err)
	}
	saveExportAutoState(beadsDir, &exportAutoState{
		LastDoltCommit: "hash-before-write",
		LastDiffAnchor: "anchor-before-write",
		Timestamp:      time.Time{}, // zero: throttle window open
		Issues:         1,
	})
	statePath = filepath.Join(beadsDir, exportAutoStateFile)
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	return jsonl, statePath, string(data)
}

// assertPrecheckError checks the common AC-2 contract: maybeAutoExport
// returns an error (wrapping cause when one is injected) that names the
// resolved destination, prints no warning itself (no duplicate failure
// message), and leaves both the JSONL and export-state.json byte-for-byte
// untouched.
func assertPrecheckError(t *testing.T, cause error, jsonl, jsonlSeed, statePath, stateBytes string) {
	t.Helper()

	var got error
	stderr := captureStderr(t, func() { got = maybeAutoExport(context.Background(), false) })
	if cause != nil && !errors.Is(got, cause) {
		t.Fatalf("maybeAutoExport err = %v, want it to wrap %v", got, cause)
	}
	if got == nil {
		t.Fatal("maybeAutoExport err = nil, want a precheck failure")
	}
	if !strings.Contains(got.Error(), jsonl) {
		t.Fatalf("error %q must name the resolved export destination %q", got.Error(), jsonl)
	}
	if strings.Contains(stderr, "Warning") {
		t.Fatalf("maybeAutoExport printed %q; the error path must leave the single print to the caller", stderr)
	}
	assertFileUnchanged(t, jsonl, jsonlSeed)
	assertFileUnchanged(t, statePath, stateBytes)
}

// TestMaybeAutoExportPrecheckFailureIsAnError pins AC-2 of GH#6163: a
// pre-export check that cannot read the existing JSONL (open failure,
// malformed content) or query the store (reconciliation sweep, empty-check
// query) returns a contextual error instead of warning and skipping.
func TestMaybeAutoExportPrecheckFailureIsAnError(t *testing.T) {
	seed := `{"_type":"issue","id":"bd-seed","issue_type":"task","title":"seed"}` + "\n"
	conflictSeed := "<<<<<<< HEAD\n{\"id\":\"bd-main\"}\n=======\n{\"id\":\"bd-branch\"}\n>>>>>>> branch\n"

	t.Run("reconciliation store query fails", func(t *testing.T) {
		jsonl, statePath, stateBytes := setupDueAutoExport(t, &precheckErrStore{}, seed)
		assertPrecheckError(t, errPrecheckQuery, jsonl, seed, statePath, stateBytes)
	})

	t.Run("existing JSONL is malformed", func(t *testing.T) {
		jsonl, statePath, stateBytes := setupDueAutoExport(t, &fakeStateHashStore{stateHash: "hash-after-write"}, conflictSeed)
		assertPrecheckError(t, nil, jsonl, conflictSeed, statePath, stateBytes)
	})

	t.Run("existing JSONL cannot be opened for reading", func(t *testing.T) {
		jsonl, statePath, stateBytes := setupDueAutoExport(t, &fakeStateHashStore{stateHash: "hash-after-write"}, seed)
		// A directory where issues.jsonl must be makes every read of the
		// existing export fail (os.Open succeeds, the scan fails with EISDIR),
		// covering the precheck's opened-path failure without chmod tricks
		// that do not stop a root reader.
		if err := os.Remove(jsonl); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(jsonl, 0o755); err != nil {
			t.Fatal(err)
		}

		var got error
		stderr := captureStderr(t, func() { got = maybeAutoExport(context.Background(), false) })
		if got == nil {
			t.Fatal("maybeAutoExport err = nil when the existing JSONL cannot be read")
		}
		if !strings.Contains(got.Error(), jsonl) {
			t.Fatalf("error %q must name the resolved export destination %q", got.Error(), jsonl)
		}
		if strings.Contains(stderr, "Warning") {
			t.Fatalf("maybeAutoExport printed %q; the error path must leave the single print to the caller", stderr)
		}
		assertFileUnchanged(t, statePath, stateBytes)
	})

	t.Run("empty-overwrite check store query fails", func(t *testing.T) {
		jsonl, statePath, stateBytes := setupDueAutoExport(t, &emptyCheckErrStore{}, seed)
		assertPrecheckError(t, errPrecheckQuery, jsonl, seed, statePath, stateBytes)
	})
}

// TestMaybeAutoExportFailsWhenExistingJSONLIsInvalid carries contributor
// PR #6016's invalid-JSONL fixture onto the unified failure policy of
// GH#6163: a JSONL the pre-export checks cannot parse fails the invocation.
// Adapted from the PR with credit to the contributor; the assertions no
// longer depend on a single branch's wording.
func TestMaybeAutoExportFailsWhenExistingJSONLIsInvalid(t *testing.T) {
	jsonl, statePath, stateBytes := setupDueAutoExport(t, &fakeStateHashStore{stateHash: "changed-state"}, "<<<<<<< HEAD\n{\"id\":\"bd-main\"}\n=======\n{\"id\":\"bd-branch\"}\n>>>>>>> branch\n")

	var err error
	stderr := captureStderr(t, func() { err = maybeAutoExport(context.Background(), false) })
	if err == nil {
		t.Fatal("maybeAutoExport succeeded with an invalid existing JSONL file")
	}
	if !strings.Contains(err.Error(), jsonl) {
		t.Fatalf("maybeAutoExport error = %q, want it to name the existing JSONL path %q", err.Error(), jsonl)
	}
	if strings.Contains(stderr, "Warning") {
		t.Fatalf("maybeAutoExport printed %q; the error path must leave the single print to the caller", stderr)
	}
	assertFileUnchanged(t, statePath, stateBytes)
}

// TestMaybeAutoExport_WorktreeRedirectErrorUsesResolvedPath carries PR
// #6016's redirect fixture onto the unified failure policy: when a linked
// worktree's .beads/redirect points at the main repo's .beads and the
// precheck fails on the existing JSONL at the RESOLVED destination, the
// returned error must name that resolved path (not the worktree-relative
// one the old warning could print). Adapted by the checkpoint pipeline;
// the assertion moved from the warning text to the returned error so it no
// longer depends on a single branch's wording.
func TestMaybeAutoExport_WorktreeRedirectErrorUsesResolvedPath(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	initConfigForTest(t)
	config.Set("export.auto", true)
	saveAndRestoreGlobals(t)
	t.Setenv("BD_GIT_HOOK", "")
	store = &fakeStateHashStore{stateHash: "changed-state"}

	tmpRaw, err := os.MkdirTemp("", "bd-gh6163-redirect-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpRaw) })
	real, err := filepath.EvalSymlinks(tmpRaw)
	if err != nil {
		t.Fatal(err)
	}

	mainRepo := filepath.Join(real, "main")
	if err := os.MkdirAll(mainRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit := func(dir string, args ...string) {
		t.Helper()
		c := gitCommand(args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runGit(mainRepo, "init", "-q")
	runGit(mainRepo, "config", "user.email", "test@example.com")
	runGit(mainRepo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(mainRepo, "README.md"), []byte("# Test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(mainRepo, "add", "README.md")
	runGit(mainRepo, "commit", "-qm", "initial")

	mainBeadsDir := filepath.Join(mainRepo, ".beads")
	if err := os.MkdirAll(mainBeadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mainBeadsDir, "metadata.json"), []byte(`{"database":"beads","backend":"dolt"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	worktree := filepath.Join(real, "worktree")
	runGit(mainRepo, "worktree", "add", "-b", "redirect-test", worktree)
	t.Cleanup(func() {
		c := gitCommand("worktree", "remove", "--force", worktree)
		c.Dir = mainRepo
		_ = c.Run()
	})
	worktreeBeadsDir := filepath.Join(worktree, ".beads")
	if err := os.MkdirAll(worktreeBeadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktreeBeadsDir, "redirect"), []byte(mainBeadsDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The resolved destination holds a JSONL the precheck cannot parse.
	path := filepath.Join(mainBeadsDir, "issues.jsonl")
	badJSONL := "<<<<<<< HEAD\n{\"id\":\"bd-main\"}\n=======\n{\"id\":\"bd-branch\"}\n>>>>>>> branch\n"
	if err := os.WriteFile(path, []byte(badJSONL), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Chdir(worktree)
	t.Setenv("BEADS_DIR", "")
	git.ResetCaches()
	t.Cleanup(git.ResetCaches)

	var got error
	stderr := captureStderr(t, func() { got = maybeAutoExport(context.Background(), false) })
	if got == nil {
		t.Fatal("maybeAutoExport succeeded with an invalid redirected JSONL file")
	}
	if !strings.Contains(got.Error(), path) {
		t.Fatalf("error = %q, want it to name the resolved destination %q", got.Error(), path)
	}
	if strings.Contains(got.Error(), worktree) {
		t.Fatalf("error = %q names the worktree, not the redirected destination %q", got.Error(), path)
	}
	if strings.Contains(stderr, "Warning") {
		t.Fatalf("maybeAutoExport printed %q; the error path must leave the single print to the caller", stderr)
	}
	assertFileUnchanged(t, path, badJSONL)
}

// --- AC-4: deliberate refusals and capability skips stay warn-and-nil ---

// shrinkRefusalSeed mixes one in-scope issue (still in the store) with one
// memory record — exactly what guardAutoExportOverwrite refuses to drop.
const shrinkRefusalSeed = exportRelationSeed +
	`{"_type":"memory","key":"keep-me","value":"private context"}` + "\n"

// TestMaybeAutoExportShrinkGuardRefusalStaysWarning pins the shrink guard's
// half of AC-4 (GH#6163): the deliberate refusal to overwrite a JSONL with
// out-of-scope records is a safety skip, not an operational failure — the
// triggering command succeeds, exactly one warning is printed, and the
// previous JSONL and export state stay unchanged.
func TestMaybeAutoExportShrinkGuardRefusalStaysWarning(t *testing.T) {
	s := &fakeHeadOnlyStore{issues: []*types.Issue{
		{ID: "bd-export1", Title: "export me", Status: types.StatusOpen, IssueType: types.TypeTask},
	}}
	jsonl, statePath, stateBytes := setupDueAutoExport(t, s, shrinkRefusalSeed)
	// fakeHeadOnlyStore is no storage.DiffStore, so the export goes straight
	// to the full-export fallback that runs the shrink guard.

	var err error
	stderr := captureStderr(t, func() { err = maybeAutoExport(context.Background(), false) })
	if err != nil {
		t.Fatalf("maybeAutoExport err = %v, want nil (a shrink-guard refusal is a deliberate skip)", err)
	}
	if n := strings.Count(stderr, "Warning: auto-export skipped: auto-export shrink guard: refusing to overwrite"); n != 1 {
		t.Fatalf("stderr = %q, want exactly one shrink-guard refusal warning", stderr)
	}
	assertFileUnchanged(t, jsonl, shrinkRefusalSeed)
	assertFileUnchanged(t, statePath, stateBytes)
}

// stateHashUnsupportedStore refuses the state hash with the same wrapped
// typed sentinel the http backend returns, and counts SearchIssues calls so
// the test can prove the skip happens before any store read or file write.
type stateHashUnsupportedStore struct {
	fakeHeadOnlyStore
	searchCalls int
}

func (f *stateHashUnsupportedStore) GetStateHash(context.Context) (string, error) {
	return "", fmt.Errorf("get state hash: %w", &storage.ErrUnsupported{Op: "GetStateHash", Backend: "http"})
}

func (f *stateHashUnsupportedStore) SearchIssues(context.Context, string, types.IssueFilter) ([]*types.Issue, error) {
	f.searchCalls++
	return nil, nil
}

// TestMaybeAutoExportStateHashUnsupportedSkips pins the state-hash half of
// AC-4 (GH#6163): a typed *storage.ErrUnsupported — a backend that cannot
// report a state hash at all, wrapped like the http store's refusal — is a
// missing capability, not a broken read. The export warns and skips; no
// store query runs and no file is written.
func TestMaybeAutoExportStateHashUnsupportedSkips(t *testing.T) {
	fake := &stateHashUnsupportedStore{issues: []*types.Issue{
		{ID: "bd-export1", Title: "export me", Status: types.StatusOpen, IssueType: types.TypeTask},
	}}
	jsonl, statePath, stateBytes := setupDueAutoExport(t, fake, "")

	var err error
	stderr := captureStderr(t, func() { err = maybeAutoExport(context.Background(), false) })
	if err != nil {
		t.Fatalf("maybeAutoExport err = %v, want nil (an unsupported state hash is a warning-only skip)", err)
	}
	if n := strings.Count(stderr, "Warning: auto-export skipped"); n != 1 {
		t.Fatalf("stderr = %q, want exactly one skipped warning", stderr)
	}
	if fake.searchCalls != 0 {
		t.Fatalf("SearchIssues calls = %d, want 0 (the skip must precede every store read)", fake.searchCalls)
	}
	assertFileUnchanged(t, jsonl, "") // nothing was written
	assertFileUnchanged(t, statePath, stateBytes)
}

// TestMaybeAutoExportRetriesAfterFailure is the design's edge control over
// the failure policy (GH#6163): a failed export leaves the previous state
// in place, so the next eligible mutating command retries it without any
// new retry mechanism — and succeeds once the fault is gone.
func TestMaybeAutoExportRetriesAfterFailure(t *testing.T) {
	failStore := &stateHashReadErrStore{}
	jsonl, statePath, stateBytes := setupDueAutoExport(t, failStore, exportRelationSeed)

	var err error
	_ = captureStderr(t, func() { err = maybeAutoExport(context.Background(), false) })
	if err == nil {
		t.Fatal("first run: maybeAutoExport err = nil, want the state-hash failure")
	}
	assertFileUnchanged(t, statePath, stateBytes)

	// The fault is gone and the state timestamp never advanced, so the very
	// next command is eligible again and the export goes through.
	store = &fakeHeadOnlyStore{issues: []*types.Issue{
		{ID: "bd-export1", Title: "export me", Status: types.StatusOpen, IssueType: types.TypeTask},
	}}
	if err := maybeAutoExport(context.Background(), false); err != nil {
		t.Fatalf("retry after failure: maybeAutoExport: %v", err)
	}
	got, readErr := os.ReadFile(jsonl)
	if readErr != nil {
		t.Fatalf("retry did not export: %v", readErr)
	}
	if !strings.Contains(string(got), "bd-export1") {
		t.Fatalf("exported JSONL = %q, want it to contain bd-export1", got)
	}
	if state := loadExportAutoState(filepath.Dir(statePath)); state.LastDoltCommit != "head-commit-hash" {
		t.Fatalf("retry did not advance export state: LastDoltCommit = %q", state.LastDoltCommit)
	}
}
