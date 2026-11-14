//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
)

func TestRepairMultiplePrefixes(t *testing.T) {
	tmpDir := t.TempDir()
	testDBPath := filepath.Join(tmpDir, "test.db")

	ctx := context.Background()

	testStore := newTestStore(t, testDBPath)

	// Set the globals this test's code path actually reads. Deliberately NOT
	// dbPath: on the shared-branch fast path newTestStore writes a
	// metadata.json naming the shared database with no branch qualifier
	// (test_helpers_test.go newTestStoreSharedBranch) while the live store sits
	// on this test's own branch, so a global dbPath would point anything that
	// reopens by path at shared `main` instead. repairPrefixes takes the store
	// explicitly and never reopens, so the global is left alone rather than
	// made to lie.
	oldStore := store
	oldActor := actor
	store = testStore
	actor = "test"
	defer func() {
		store = oldStore
		actor = oldActor
	}()

	// Create issues with multiple prefixes (simulating corruption).
	// CreateIssue accepts explicit IDs without prefix validation,
	// so we can create issues with different prefixes to simulate
	// a corrupted database state.
	testIssues := []types.Issue{
		{ID: "test-1", Title: "Test issue 1", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "test-2", Title: "Test issue 2", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "old-1", Title: "Old issue 1", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "old-2", Title: "Old issue 2", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "another-1", Title: "Another issue 1", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	}

	for i := range testIssues {
		if err := testStore.CreateIssue(ctx, &testIssues[i], "test"); err != nil {
			t.Fatalf("failed to create issue %s: %v", testIssues[i].ID, err)
		}
	}

	// Verify we have multiple prefixes
	allIssues, err := testStore.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("failed to search issues: %v", err)
	}

	prefixes := detectPrefixes(allIssues)
	if len(prefixes) != 3 {
		t.Fatalf("expected 3 prefixes, got %d: %v", len(prefixes), prefixes)
	}

	// Test repair — now uses UpdateIssueID (Dolt rename semantics)
	// instead of the old CreateIssue+DeleteIssue approach that caused deadlocks
	if err := repairPrefixes(ctx, testStore, "test", "test", allIssues, prefixes, false); err != nil {
		t.Fatalf("repair failed: %v", err)
	}

	// Verify all issues now have correct prefix
	allIssues, err = testStore.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("failed to search issues after repair: %v", err)
	}

	prefixes = detectPrefixes(allIssues)
	if len(prefixes) != 1 {
		t.Fatalf("expected 1 prefix after repair, got %d: %v", len(prefixes), prefixes)
	}

	if _, ok := prefixes["test"]; !ok {
		t.Fatalf("expected prefix 'test', got %v", prefixes)
	}

	// Verify the original test-1 and test-2 are unchanged
	for _, id := range []string{"test-1", "test-2"} {
		issue, err := testStore.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("expected issue %s to exist unchanged: %v", id, err)
		}
		if issue == nil {
			t.Fatalf("expected issue %s to exist", id)
		}
	}

	// Verify total count: 2 original (test-1, test-2) + 3 renamed = 5
	if len(allIssues) != 5 {
		t.Fatalf("expected 5 issues total, got %d", len(allIssues))
	}

	// Count issues with correct prefix
	testPrefixCount := 0
	for _, issue := range allIssues {
		if len(issue.ID) > 5 && issue.ID[:5] == "test-" {
			testPrefixCount++
		}
	}
	if testPrefixCount != 5 {
		t.Fatalf("expected all 5 issues to have 'test-' prefix, got %d", testPrefixCount)
	}

	// Verify old IDs no longer exist
	for _, oldID := range []string{"old-1", "old-2", "another-1"} {
		issue, err := testStore.GetIssue(ctx, oldID)
		if err == nil && issue != nil {
			t.Fatalf("expected old ID %s to no longer exist", oldID)
		}
	}
}

func TestRepairPrefixesRetargetsBeadGates(t *testing.T) {
	// The repair gives every off-prefix bead a new ID. A bead gate waiting on
	// one must follow it, sighting included, or its next check would read the
	// rename as a deletion and resolve. That holds whether the gate itself is
	// renamed before its bead, after it, or not at all.
	ctx := context.Background()
	testStore := newTestStore(t, filepath.Join(t.TempDir(), "test.db"))

	for _, issue := range []*types.Issue{
		{ID: "test-1", Title: "Kept bead", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "old-1", Title: "Renamed bead", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "test-gate", Title: "Gate on the renamed bead", Status: types.StatusOpen, Priority: 2, IssueType: "gate",
			AwaitType: "bead", AwaitID: "old-1", Metadata: json.RawMessage(`{"await_seen":"old-1"}`)},
		{ID: "old-gate", Title: "Renamed gate on the kept bead", Status: types.StatusOpen, Priority: 2, IssueType: "gate",
			AwaitType: "bead", AwaitID: "test-1", Metadata: json.RawMessage(`{"await_seen":"test-1"}`)},
		// The repair renames in ID-number order: old-0, old-1, old-2.
		{ID: "old-0", Title: "Gate renamed before its bead", Status: types.StatusOpen, Priority: 2, IssueType: "gate",
			AwaitType: "bead", AwaitID: "old-1", Metadata: json.RawMessage(`{"await_seen":"old-1"}`)},
		{ID: "old-2", Title: "Gate renamed after its bead", Status: types.StatusOpen, Priority: 2, IssueType: "gate",
			AwaitType: "bead", AwaitID: "old-1", Metadata: json.RawMessage(`{"await_seen":"old-1"}`)},
	} {
		if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create issue %s: %v", issue.ID, err)
		}
	}

	allIssues, err := testStore.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("failed to search issues: %v", err)
	}
	if err := repairPrefixes(ctx, testStore, "test", "test", allIssues, detectPrefixes(allIssues), false); err != nil {
		t.Fatalf("repair failed: %v", err)
	}

	allIssues, err = testStore.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("failed to search issues after repair: %v", err)
	}
	idByTitle := make(map[string]string, len(allIssues))
	for _, issue := range allIssues {
		idByTitle[issue.Title] = issue.ID
	}
	renamedBead := idByTitle["Renamed bead"]
	if renamedBead == "" || renamedBead == "old-1" {
		t.Fatalf("old-1 was not renamed: %v", idByTitle)
	}

	for _, tt := range []struct {
		title       string
		wantAwaitID string
	}{
		{title: "Gate on the renamed bead", wantAwaitID: renamedBead},
		{title: "Renamed gate on the kept bead", wantAwaitID: "test-1"},
		{title: "Gate renamed before its bead", wantAwaitID: renamedBead},
		{title: "Gate renamed after its bead", wantAwaitID: renamedBead},
	} {
		gate, err := testStore.GetIssue(ctx, idByTitle[tt.title])
		if err != nil {
			t.Fatalf("%s: %v", tt.title, err)
		}
		if gate.AwaitID != tt.wantAwaitID || !beadGateTargetSeen(gate) {
			t.Errorf("%s: await_id=%q metadata=%s, want %q recorded as seen", tt.title, gate.AwaitID, gate.Metadata, tt.wantAwaitID)
		}
	}
}

// TestRepairMultiplePrefixesPreservesSuffix pins beads#6591: a repair with
// no actual suffix collisions must keep each issue's mnemonic suffix
// (including dotted children) under the target prefix instead of minting a
// hash id for every renamed issue.
//
// It and TestRepairMultiplePrefixesFallsBackOnCollision come from #6621 by
// Rongjun GENG, the first fix for GH#6591, unchanged except that they leave
// the global dbPath alone, as TestRepairMultiplePrefixes explains, and let
// newTestStoreWithPrefix's cleanup close the store.
func TestRepairMultiplePrefixesPreservesSuffix(t *testing.T) {
	tmpDir := t.TempDir()
	testDBPath := filepath.Join(tmpDir, "test.db")

	ctx := context.Background()

	testStore := newTestStoreWithPrefix(t, testDBPath, "kb")

	oldStore := store
	oldActor := actor
	store = testStore
	actor = "test"
	defer func() {
		store = oldStore
		actor = oldActor
	}()

	// No two incorrect-prefix issues share a suffix, and none collide with
	// an already-correct kb-* id — the common case the issue reports (only
	// one collision across 1,266 issues in the real database).
	testIssues := []types.Issue{
		{ID: "kb-u6ch", Title: "Already correct", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "cre-db-nwb", Title: "Parent", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "cre-db-nwb.3", Title: "Dotted child of nwb", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "investment-c2a", Title: "Different family, distinct suffix", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	}
	for i := range testIssues {
		if err := testStore.CreateIssue(ctx, &testIssues[i], "test"); err != nil {
			t.Fatalf("failed to create issue %s: %v", testIssues[i].ID, err)
		}
	}

	allIssues, err := testStore.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("failed to search issues: %v", err)
	}
	prefixes := detectPrefixes(allIssues)

	if err := repairPrefixes(ctx, testStore, "test", "kb", allIssues, prefixes, false); err != nil {
		t.Fatalf("repair failed: %v", err)
	}

	wantSuffixPreserved := map[string]string{
		"cre-db-nwb":     "kb-nwb",
		"cre-db-nwb.3":   "kb-nwb.3",
		"investment-c2a": "kb-c2a",
	}
	for oldID, wantID := range wantSuffixPreserved {
		if issue, _ := testStore.GetIssue(ctx, oldID); issue != nil {
			t.Fatalf("expected old id %s to no longer exist", oldID)
		}
		issue, err := testStore.GetIssue(ctx, wantID)
		if err != nil || issue == nil {
			t.Fatalf("expected suffix-preserved id %s (from %s), got lookup error: %v", wantID, oldID, err)
		}
	}
}

// TestRepairMultiplePrefixesFallsBackOnCollision pins the other half of
// beads#6591: when a suffix-preserving id would collide with an existing or
// already-assigned id, repair must fall back to a minted hash id instead of
// silently dropping one issue's rename.
func TestRepairMultiplePrefixesFallsBackOnCollision(t *testing.T) {
	tmpDir := t.TempDir()
	testDBPath := filepath.Join(tmpDir, "test.db")

	ctx := context.Background()

	testStore := newTestStoreWithPrefix(t, testDBPath, "kb")

	oldStore := store
	oldActor := actor
	store = testStore
	actor = "test"
	defer func() {
		store = oldStore
		actor = oldActor
	}()

	// kb-1 already exists under the target prefix; both incorrect-prefix
	// issues below would naively rewrite to kb-1 too (a same-batch and a
	// same-batch-vs-existing collision).
	testIssues := []types.Issue{
		{ID: "kb-1", Title: "Already correct", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "old-1", Title: "Collides with kb-1", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "another-1", Title: "Also collides", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	}
	for i := range testIssues {
		if err := testStore.CreateIssue(ctx, &testIssues[i], "test"); err != nil {
			t.Fatalf("failed to create issue %s: %v", testIssues[i].ID, err)
		}
	}

	allIssues, err := testStore.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("failed to search issues: %v", err)
	}
	prefixes := detectPrefixes(allIssues)

	if err := repairPrefixes(ctx, testStore, "test", "kb", allIssues, prefixes, false); err != nil {
		t.Fatalf("repair failed: %v", err)
	}

	allIssues, err = testStore.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("failed to search issues after repair: %v", err)
	}
	if len(allIssues) != 3 {
		t.Fatalf("expected 3 issues total (no data loss on collision), got %d", len(allIssues))
	}
	for _, issue := range allIssues {
		if !strings.HasPrefix(issue.ID, "kb-") {
			t.Fatalf("expected kb- prefix on every issue, got %s", issue.ID)
		}
	}

	// old-1 and another-1 must not both have silently become kb-1 or been
	// dropped — each must resolve to a distinct, real issue.
	seen := make(map[string]bool)
	for _, issue := range allIssues {
		if seen[issue.ID] {
			t.Fatalf("duplicate id %s after repair (collision not resolved)", issue.ID)
		}
		seen[issue.ID] = true
	}
}

// failingRenameStore fails UpdateIssueID for failID. With landed, the rename
// is applied before the error is returned, like a commit whose response was
// lost.
type failingRenameStore struct {
	storage.DoltStorage
	failID   string
	landed   bool
	injected bool
}

func (s *failingRenameStore) UpdateIssueID(ctx context.Context, oldID, newID string, issue *types.Issue, actor string) error {
	if oldID != s.failID {
		return s.DoltStorage.UpdateIssueID(ctx, oldID, newID, issue, actor)
	}
	if s.landed {
		if err := s.DoltStorage.UpdateIssueID(ctx, oldID, newID, issue, actor); err != nil {
			return err
		}
	}
	s.injected = true
	return errors.New("injected UpdateIssueID failure")
}

// withRenameStore points the store and actor globals at st while run runs.
func withRenameStore(st storage.DoltStorage, run func() error) error {
	oldStore, oldActor := store, actor
	store, actor = st, "test"
	storeMutex.Lock()
	oldActive := storeActive
	storeActive = true
	storeMutex.Unlock()
	defer func() {
		store, actor = oldStore, oldActor
		storeMutex.Lock()
		storeActive = oldActive
		storeMutex.Unlock()
	}()
	return run()
}

func TestRenameFailureLeavesBeadGatesPending(t *testing.T) {
	// When renaming the awaited bead fails, whether or not that rename
	// landed, its gate is pointed back at the bead's old ID without a
	// sighting: bd gate check must find it pending, never resolve it as
	// though the bead had been deleted. The prefix renames rename the gate
	// first, so they move it back under its new ID.
	for _, tt := range []struct {
		name        string
		gateRenamed bool
		run         func(ctx context.Context, st storage.DoltStorage, issues []*types.Issue) error
	}{
		{name: "rename", run: func(_ context.Context, st storage.DoltStorage, _ []*types.Issue) error {
			return withRenameStore(st, func() error { return runRename(renameCmd, []string{"old-1", "old-renamed"}) })
		}},
		{name: "repair", gateRenamed: true, run: func(ctx context.Context, st storage.DoltStorage, issues []*types.Issue) error {
			return repairPrefixes(ctx, st, "test", "test", issues, detectPrefixes(issues), false)
		}},
		{name: "rename-prefix", gateRenamed: true, run: func(ctx context.Context, st storage.DoltStorage, issues []*types.Issue) error {
			return withRenameStore(st, func() error { return renamePrefixInDB(ctx, "old", "test", issues) })
		}},
	} {
		for _, landed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/landed=%v", tt.name, landed), func(t *testing.T) {
				ctx := context.Background()
				testStore := newTestStore(t, filepath.Join(t.TempDir(), "test.db"))
				issues := []*types.Issue{
					{ID: "old-gate", Title: "Gate", Status: types.StatusOpen, Priority: 2, IssueType: "gate",
						AwaitType: "bead", AwaitID: "old-1", Metadata: json.RawMessage(`{"await_seen":"old-1"}`)},
					{ID: "old-1", Title: "Awaited bead", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
				}
				for _, issue := range issues {
					if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
						t.Fatalf("failed to create issue %s: %v", issue.ID, err)
					}
				}

				st := &failingRenameStore{DoltStorage: testStore, failID: "old-1", landed: landed}
				if err := tt.run(ctx, st, issues); err == nil || !st.injected {
					t.Fatalf("rename error = %v (failure injected: %v), want the injected failure", err, st.injected)
				}

				all, err := testStore.SearchIssues(ctx, "", types.IssueFilter{})
				if err != nil {
					t.Fatalf("failed to search issues: %v", err)
				}
				var gate *types.Issue
				for _, issue := range all {
					if issue.Title == "Gate" {
						gate = issue
					}
				}
				if gate == nil || (gate.ID != "old-gate") != tt.gateRenamed {
					t.Fatalf("gate after the failed rename: %+v, want it renamed: %v", gate, tt.gateRenamed)
				}
				if gate.AwaitID != "old-1" || beadGateTargetSeen(gate) {
					t.Errorf("gate after the failed rename: await_id=%q metadata=%s, want %q without a sighting", gate.AwaitID, gate.Metadata, "old-1")
				}
				resolved, reason, err := evaluateBeadGate(ctx, gate, testStore, nil)
				if err != nil || resolved {
					t.Errorf("bd gate check on the gate: resolved=%v reason=%q err=%v, want it pending", resolved, reason, err)
				}
			})
		}
	}
}

// seedRepairIssues creates issues with explicit IDs, as a store absorbed from
// several trackers holds them.
func seedRepairIssues(t *testing.T, ctx context.Context, st storage.DoltStorage, issues ...*types.Issue) {
	t.Helper()
	for _, issue := range issues {
		if issue.Status == "" {
			issue.Status = types.StatusOpen
		}
		if issue.IssueType == "" {
			issue.IssueType = types.TypeTask
		}
		if issue.Priority == 0 {
			issue.Priority = 2
		}
		if err := st.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("create %s: %v", issue.ID, err)
		}
	}
}

// repairAll runs the repair over every stored issue, as bd rename-prefix
// --repair does.
func repairAll(t *testing.T, ctx context.Context, st storage.DoltStorage, target string, dryRun bool) error {
	t.Helper()
	all, err := st.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	return repairPrefixes(ctx, st, "test", target, all, detectPrefixes(all), dryRun)
}

// storedIDs returns the set of IDs now stored.
func storedIDs(t *testing.T, ctx context.Context, st storage.DoltStorage) map[string]*types.Issue {
	t.Helper()
	all, err := st.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	got := make(map[string]*types.Issue, len(all))
	for _, issue := range all {
		got[issue.ID] = issue
	}
	return got
}

func TestRepairPrefixesKeepsSuffixes(t *testing.T) {
	// GH#6591 AC-1: --repair keeps each off-prefix issue's suffix when its
	// destination is free, keeps target IDs, and carries data and edges.
	ctx := context.Background()
	testStore := newTestStore(t, filepath.Join(t.TempDir(), "test.db"))
	seedRepairIssues(t, ctx, testStore,
		&types.Issue{ID: "taxdb-u6ch", Title: "Tax work", Description: "payload body", Priority: 1},
		&types.Issue{ID: "cre-db-c2a", Title: "CRE work", Notes: "depends on taxdb-u6ch"},
		&types.Issue{ID: "kb-hand", Title: "Hand named"},
	)
	if err := testStore.AddLabel(ctx, "taxdb-u6ch", "finance", "test"); err != nil {
		t.Fatalf("add label: %v", err)
	}
	if err := testStore.AddDependency(ctx, &types.Dependency{IssueID: "cre-db-c2a", DependsOnID: "taxdb-u6ch", Type: types.DepBlocks}, "test"); err != nil {
		t.Fatalf("add dependency: %v", err)
	}
	if err := testStore.AddDependency(ctx, &types.Dependency{IssueID: "kb-hand", DependsOnID: "cre-db-c2a", Type: types.DepBlocks}, "test"); err != nil {
		t.Fatalf("add dependency: %v", err)
	}

	if err := repairAll(t, ctx, testStore, "kb", false); err != nil {
		t.Fatalf("repair failed: %v", err)
	}

	got := storedIDs(t, ctx, testStore)
	if len(got) != 3 {
		t.Fatalf("stored ids after repair = %v, want exactly kb-u6ch, kb-c2a, kb-hand", got)
	}
	for _, want := range []string{"kb-u6ch", "kb-c2a", "kb-hand"} {
		if got[want] == nil {
			t.Fatalf("expected %s after repair; ids now: %v", want, got)
		}
	}
	if tax := got["kb-u6ch"]; tax.Title != "Tax work" || tax.Description != "payload body" || tax.Priority != 1 {
		t.Errorf("kb-u6ch lost its data: %+v", tax)
	}
	if notes := got["kb-c2a"].Notes; notes != "depends on kb-u6ch" {
		t.Errorf("kb-c2a notes = %q, want the reference rewritten to kb-u6ch", notes)
	}
	labels, err := testStore.GetLabels(ctx, "kb-u6ch")
	if err != nil || len(labels) != 1 || labels[0] != "finance" {
		t.Errorf("kb-u6ch labels = %v (err %v), want [finance]", labels, err)
	}
	for _, edge := range []struct{ from, to string }{{"kb-c2a", "kb-u6ch"}, {"kb-hand", "kb-c2a"}} {
		deps, err := testStore.GetDependencyRecords(ctx, edge.from)
		if err != nil {
			t.Fatalf("dependencies of %s: %v", edge.from, err)
		}
		if len(deps) != 1 || deps[0].DependsOnID != edge.to {
			t.Errorf("dependencies of %s = %s, want one edge to %s", edge.from, fmtDeps(deps), edge.to)
		}
	}
	if prefix, err := testStore.GetConfig(ctx, "issue_prefix"); err != nil || prefix != "kb" {
		t.Errorf("issue_prefix = %q (err %v), want kb", prefix, err)
	}
}

func TestRepairPrefixesRepointsEdgesOntoUnrenamedTargetRows(t *testing.T) {
	// GH#6591 AC-1: an edge stored across prefixes (taxdb-x -> kb-hand) sits
	// in the external column. Renaming its SOURCE onto the target prefix must
	// leave kb-x blocked by kb-hand, not holding a dangling external edge
	// blocked state never reads.
	ctx := context.Background()
	testStore := newTestStore(t, filepath.Join(t.TempDir(), "test.db"))
	seedRepairIssues(t, ctx, testStore,
		&types.Issue{ID: "taxdb-x", Title: "Blocked tax work"},
		&types.Issue{ID: "kb-hand", Title: "Open blocker"},
	)
	if err := testStore.AddDependency(ctx, &types.Dependency{IssueID: "taxdb-x", DependsOnID: "kb-hand", Type: types.DepBlocks}, "test"); err != nil {
		t.Fatalf("add dependency: %v", err)
	}

	if err := repairAll(t, ctx, testStore, "kb", false); err != nil {
		t.Fatalf("repair failed: %v", err)
	}

	deps, err := testStore.GetDependencyRecords(ctx, "kb-x")
	if err != nil {
		t.Fatalf("dependencies of kb-x: %v", err)
	}
	if len(deps) != 1 || deps[0].DependsOnID != "kb-hand" || deps[0].Type != types.DepBlocks {
		t.Fatalf("dependencies of kb-x = %s, want one blocks edge to kb-hand", fmtDeps(deps))
	}
	blocked, blockers, err := testStore.IsBlocked(ctx, "kb-x")
	if err != nil {
		t.Fatalf("IsBlocked(kb-x): %v", err)
	}
	if !blocked || !slices.Contains(blockers, "kb-hand") {
		t.Errorf("IsBlocked(kb-x) = %v %v, want blocked by kb-hand", blocked, blockers)
	}
}

// depTo returns the edge in deps that names target, or nil.
func depTo(deps []*types.Dependency, target string) *types.Dependency {
	for _, d := range deps {
		if d.DependsOnID == target {
			return d
		}
	}
	return nil
}

func TestRepairPrefixesLeavesEdgesStorageAlreadyMoved(t *testing.T) {
	// An edge whose two ends shared a prefix before the repair (a family's
	// parent-child edge, a blocks edge between two taxdb rows, a close gate
	// older than the check that refuses it today, #6506) is re-pointed by
	// storage in place when its ends are renamed. The repair must leave it
	// alone: re-adding it stamped the repair's actor and time on it, and the
	// re-add's hierarchy check refused the close gate, which deleted it and
	// stopped the run before issue_prefix was set.
	ctx := context.Background()
	testStore := newTestStore(t, filepath.Join(t.TempDir(), "test.db"))
	seedRepairIssues(t, ctx, testStore,
		&types.Issue{ID: "taxdb-5su1", Title: "Epic", IssueType: types.TypeEpic},
		&types.Issue{ID: "taxdb-5su1.13", Title: "Child"},
		&types.Issue{ID: "taxdb-u6ch", Title: "Waits on the child"},
		&types.Issue{ID: "cre-db-c2a", Title: "Blocker in another family"},
		&types.Issue{ID: "kb-hand", Title: "Hand named"},
	)
	for _, dep := range []*types.Dependency{
		{IssueID: "taxdb-5su1.13", DependsOnID: "taxdb-5su1", Type: types.DepParentChild},
		{IssueID: "taxdb-u6ch", DependsOnID: "taxdb-5su1.13", Type: types.DepBlocks},
		{IssueID: "kb-hand", DependsOnID: "cre-db-c2a", Type: types.DepBlocks},
	} {
		if err := testStore.AddDependency(ctx, dep, "seeder"); err != nil {
			t.Fatalf("add %s -> %s: %v", dep.IssueID, dep.DependsOnID, err)
		}
	}
	// bd dep add refuses an epic blocking on its own child, so seed the close
	// gate the way an older store holds it.
	if _, err := testStore.DB().ExecContext(ctx,
		"INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by, metadata) VALUES (UUID(), ?, ?, 'blocks', UTC_TIMESTAMP(), 'seeder', '{}')",
		"taxdb-5su1", "taxdb-5su1.13"); err != nil {
		t.Fatalf("seed close gate: %v", err)
	}

	result := repairAllJSON(t, ctx, testStore, "kb", false)
	if len(result.UnrepointedEdges) != 0 {
		t.Errorf("unrepointed edges = %+v, want none", result.UnrepointedEdges)
	}

	for _, want := range []struct {
		from, to string
		typ      types.DependencyType
	}{
		{"kb-5su1.13", "kb-5su1", types.DepParentChild},
		{"kb-u6ch", "kb-5su1.13", types.DepBlocks},
		{"kb-5su1", "kb-5su1.13", types.DepBlocks},
	} {
		deps, err := testStore.GetDependencyRecords(ctx, want.from)
		if err != nil {
			t.Fatalf("dependencies of %s: %v", want.from, err)
		}
		if d := depTo(deps, want.to); d == nil || d.Type != want.typ || d.CreatedBy != "seeder" {
			t.Errorf("dependencies of %s = %s (created_by of the edge to %s: %+v), want the seeded %s edge left in place", want.from, fmtDeps(deps), want.to, d, want.typ)
		}
	}
	// The edge across prefixes still follows its target's rename.
	deps, err := testStore.GetDependencyRecords(ctx, "kb-hand")
	if err != nil {
		t.Fatalf("dependencies of kb-hand: %v", err)
	}
	if len(deps) != 1 || deps[0].DependsOnID != "kb-c2a" {
		t.Errorf("dependencies of kb-hand = %s, want one edge to kb-c2a", fmtDeps(deps))
	}
	if blocked, blockers, err := testStore.IsBlocked(ctx, "kb-hand"); err != nil || !blocked || !slices.Contains(blockers, "kb-c2a") {
		t.Errorf("IsBlocked(kb-hand) = %v %v (err %v), want blocked by kb-c2a", blocked, blockers, err)
	}
	if prefix, err := testStore.GetConfig(ctx, "issue_prefix"); err != nil || prefix != "kb" {
		t.Errorf("issue_prefix = %q (err %v), want kb", prefix, err)
	}
}

func TestRepairPrefixesKeepsAnEdgeStorageRefusesToRepoint(t *testing.T) {
	// Re-pointing an edge re-adds it, and storage checks the re-add as it
	// checks a new edge. taxdb-gc blocks on kb-p was accepted across
	// prefixes, where no hierarchy check runs; once both ends are kb-*, kb-p
	// is kb-gc's grandparent and the re-add is refused. The refused edge must
	// stay as it was and be reported, and the run must still re-point the
	// edges after it and set issue_prefix, instead of deleting it and stopping.
	ctx := context.Background()
	testStore := newTestStore(t, filepath.Join(t.TempDir(), "test.db"))
	seedRepairIssues(t, ctx, testStore,
		&types.Issue{ID: "kb-p", Title: "Grandparent", IssueType: types.TypeEpic},
		&types.Issue{ID: "taxdb-c", Title: "Parent", IssueType: types.TypeEpic},
		&types.Issue{ID: "taxdb-gc", Title: "Grandchild"},
		&types.Issue{ID: "taxdb-z", Title: "Blocked across prefixes"},
		&types.Issue{ID: "cre-y", Title: "Blocker"},
	)
	for _, dep := range []*types.Dependency{
		{IssueID: "taxdb-c", DependsOnID: "kb-p", Type: types.DepParentChild},
		{IssueID: "taxdb-gc", DependsOnID: "taxdb-c", Type: types.DepParentChild},
		{IssueID: "taxdb-gc", DependsOnID: "kb-p", Type: types.DepBlocks},
		{IssueID: "taxdb-z", DependsOnID: "cre-y", Type: types.DepBlocks},
	} {
		if err := testStore.AddDependency(ctx, dep, "seeder"); err != nil {
			t.Fatalf("add %s -> %s: %v", dep.IssueID, dep.DependsOnID, err)
		}
	}

	result := repairAllJSON(t, ctx, testStore, "kb", false)

	if len(result.UnrepointedEdges) != 1 {
		t.Fatalf("unrepointed edges = %+v, want exactly kb-gc -> kb-p", result.UnrepointedEdges)
	}
	if e := result.UnrepointedEdges[0]; e.IssueID != "kb-gc" || e.DependsOnID != "kb-p" || e.WantedDependsOnID != "kb-p" ||
		e.Type != string(types.DepBlocks) || !strings.Contains(e.Error, "ancestor") {
		t.Errorf("unrepointed edge = %+v, want kb-gc -> kb-p (blocks) refused because kb-p is its ancestor", e)
	}
	deps, err := testStore.GetDependencyRecords(ctx, "kb-gc")
	if err != nil {
		t.Fatalf("dependencies of kb-gc: %v", err)
	}
	if d := depTo(deps, "kb-p"); d == nil || d.Type != types.DepBlocks || d.CreatedBy != "seeder" {
		t.Errorf("dependencies of kb-gc = %s, want the refused blocks edge to kb-p still stored", fmtDeps(deps))
	}
	if d := depTo(deps, "kb-c"); d == nil || d.Type != types.DepParentChild {
		t.Errorf("dependencies of kb-gc = %s, want its parent-child edge to kb-c", fmtDeps(deps))
	}
	if deps, err := testStore.GetDependencyRecords(ctx, "kb-c"); err != nil || len(deps) != 1 || deps[0].DependsOnID != "kb-p" {
		t.Errorf("dependencies of kb-c = %s (err %v), want its parent-child edge to kb-p", fmtDeps(deps), err)
	}
	if blocked, blockers, err := testStore.IsBlocked(ctx, "kb-z"); err != nil || !blocked || !slices.Contains(blockers, "kb-y") {
		t.Errorf("IsBlocked(kb-z) = %v %v (err %v), want blocked by kb-y: the edge after the refusal is re-pointed too", blocked, blockers, err)
	}
	if prefix, err := testStore.GetConfig(ctx, "issue_prefix"); err != nil || prefix != "kb" {
		t.Errorf("issue_prefix = %q (err %v), want kb", prefix, err)
	}
}

func TestRepairPrefixesRepointsTheHierarchyFirst(t *testing.T) {
	// Storage checks a re-pointed edge as it checks a new one, against the
	// graph as it stands. Moved in source-ID order alone, a blocking edge
	// whose source sorts first was checked while the hierarchy edge it
	// conflicts with still named a renamed issue's old ID, where the ancestry
	// and cycle walks stop: the blocking edge landed and went unreported, or
	// the hierarchy edge was refused instead. Parent-child edges move first,
	// so in the first two shapes below the blocking edge is the one refused.
	// In the last two the close gate names two live issues before any edge
	// moves, so the cycle walk, which reads every column, refuses the
	// hierarchy edge through it. The gate must then stay where blocked state
	// never reads it, instead of being re-added where it blocks.
	type leftEdge struct {
		// The edge's source, the target it keeps naming, the target it
		// wanted, its type, and words of why it was left.
		from, to, wanted string
		typ              types.DependencyType
		reason           string
	}
	seedEdges := func(t *testing.T, ctx context.Context, st *dolt.DoltStore, deps ...*types.Dependency) {
		t.Helper()
		for _, dep := range deps {
			if err := st.AddDependency(ctx, dep, "seeder"); err != nil {
				t.Fatalf("add %s -> %s: %v", dep.IssueID, dep.DependsOnID, err)
			}
		}
	}
	// bd dep add refuses an epic blocking on its own descendant, so the
	// close gate is seeded across prefixes, as an older store holds it.
	seedCloseGate := func(t *testing.T, ctx context.Context, st *dolt.DoltStore, epic, descendant string) {
		t.Helper()
		if _, err := st.DB().ExecContext(ctx,
			"INSERT INTO dependencies (id, issue_id, depends_on_external, type, created_at, created_by, metadata) VALUES (UUID(), ?, ?, 'blocks', UTC_TIMESTAMP(), 'seeder', '{}')",
			epic, descendant); err != nil {
			t.Fatalf("seed close gate %s -> %s: %v", epic, descendant, err)
		}
	}
	for _, tt := range []struct {
		name string
		seed func(t *testing.T, ctx context.Context, st *dolt.DoltStore)
		// The edges the repair must report and leave as they were, in
		// report order.
		left []leftEdge
		// The parent-child edge that must land, child under parent, if any.
		child, parent string
	}{
		{
			// kb-a would be blocked by its own grandparent kb-g.
			name: "grandchild_blocked_by_its_grandparent",
			seed: func(t *testing.T, ctx context.Context, st *dolt.DoltStore) {
				seedRepairIssues(t, ctx, st,
					&types.Issue{ID: "cre-g", Title: "Grandparent", IssueType: types.TypeEpic},
					&types.Issue{ID: "taxdb-m", Title: "Parent", IssueType: types.TypeEpic},
					&types.Issue{ID: "taxdb-a", Title: "Grandchild"},
				)
				seedEdges(t, ctx, st,
					&types.Dependency{IssueID: "taxdb-m", DependsOnID: "cre-g", Type: types.DepParentChild},
					&types.Dependency{IssueID: "taxdb-a", DependsOnID: "taxdb-m", Type: types.DepParentChild},
					&types.Dependency{IssueID: "taxdb-a", DependsOnID: "cre-g", Type: types.DepBlocks},
				)
			},
			left:  []leftEdge{{"kb-a", "cre-g", "kb-g", types.DepBlocks, "ancestor"}},
			child: "kb-m", parent: "kb-g",
		},
		{
			// An epic's close gate on its own child, both renamed.
			name: "close_gate_across_prefixes",
			seed: func(t *testing.T, ctx context.Context, st *dolt.DoltStore) {
				seedRepairIssues(t, ctx, st,
					&types.Issue{ID: "cre-a", Title: "Epic", IssueType: types.TypeEpic},
					&types.Issue{ID: "taxdb-z", Title: "Child"},
				)
				seedEdges(t, ctx, st, &types.Dependency{IssueID: "taxdb-z", DependsOnID: "cre-a", Type: types.DepParentChild})
				seedCloseGate(t, ctx, st, "cre-a", "taxdb-z")
			},
			left:  []leftEdge{{"kb-a", "taxdb-z", "kb-z", types.DepBlocks, "descendant"}},
			child: "kb-z", parent: "kb-a",
		},
		{
			// The close gate on a child already on the target prefix: once
			// the epic is kb-p, the gate names two live issues.
			name: "close_gate_on_a_child_already_on_the_target",
			seed: func(t *testing.T, ctx context.Context, st *dolt.DoltStore) {
				seedRepairIssues(t, ctx, st,
					&types.Issue{ID: "taxdb-p", Title: "Epic", IssueType: types.TypeEpic},
					&types.Issue{ID: "kb-x", Title: "Child"},
				)
				seedEdges(t, ctx, st, &types.Dependency{IssueID: "kb-x", DependsOnID: "taxdb-p", Type: types.DepParentChild})
				seedCloseGate(t, ctx, st, "taxdb-p", "kb-x")
			},
			left: []leftEdge{
				{"kb-x", "taxdb-p", "kb-p", types.DepParentChild, "cycle"},
				{"kb-p", "kb-x", "kb-x", types.DepBlocks, "closes the cycle that kept kb-x -> kb-p (parent-child)"},
			},
		},
		{
			// The close gate on a grandchild whose parent is already on the
			// target prefix.
			name: "close_gate_on_a_grandchild_already_on_the_target",
			seed: func(t *testing.T, ctx context.Context, st *dolt.DoltStore) {
				seedRepairIssues(t, ctx, st,
					&types.Issue{ID: "taxdb-g", Title: "Grand epic", IssueType: types.TypeEpic},
					&types.Issue{ID: "kb-m", Title: "Parent", IssueType: types.TypeEpic},
					&types.Issue{ID: "kb-gc", Title: "Grandchild"},
				)
				seedEdges(t, ctx, st,
					&types.Dependency{IssueID: "kb-m", DependsOnID: "taxdb-g", Type: types.DepParentChild},
					&types.Dependency{IssueID: "kb-gc", DependsOnID: "kb-m", Type: types.DepParentChild},
				)
				seedCloseGate(t, ctx, st, "taxdb-g", "kb-gc")
			},
			left: []leftEdge{
				{"kb-m", "taxdb-g", "kb-g", types.DepParentChild, "cycle"},
				{"kb-g", "kb-gc", "kb-gc", types.DepBlocks, "closes the cycle that kept kb-m -> kb-g (parent-child)"},
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			testStore := newTestStore(t, filepath.Join(t.TempDir(), "test.db"))
			tt.seed(t, ctx, testStore)

			result := repairAllJSON(t, ctx, testStore, "kb", false)

			if len(result.UnrepointedEdges) != len(tt.left) {
				t.Fatalf("unrepointed edges = %+v, want %+v", result.UnrepointedEdges, tt.left)
			}
			for i, want := range tt.left {
				if e := result.UnrepointedEdges[i]; e.IssueID != want.from || e.DependsOnID != want.to || e.WantedDependsOnID != want.wanted ||
					e.Type != string(want.typ) || !strings.Contains(e.Error, want.reason) {
					t.Errorf("unrepointed edge %d = %+v, want %s -> %s (%s, wanted %s) left with %q", i, e, want.from, want.to, want.typ, want.wanted, want.reason)
				}
				deps, err := testStore.GetDependencyRecords(ctx, want.from)
				if err != nil {
					t.Fatalf("dependencies of %s: %v", want.from, err)
				}
				if d := depTo(deps, want.to); d == nil || d.Type != want.typ || d.CreatedBy != "seeder" {
					t.Errorf("dependencies of %s = %s, want the seeded %s edge to %s left as it was", want.from, fmtDeps(deps), want.typ, want.to)
				}
				if want.typ != types.DepBlocks {
					continue
				}
				if blocked, blockers, err := testStore.IsBlocked(ctx, want.from); err != nil || blocked {
					t.Errorf("IsBlocked(%s) = %v %v (err %v), want it unblocked: the edge left as it was blocks nothing", want.from, blocked, blockers, err)
				}
			}
			if tt.child != "" {
				deps, err := testStore.GetDependencyRecords(ctx, tt.child)
				if err != nil {
					t.Fatalf("dependencies of %s: %v", tt.child, err)
				}
				if d := depTo(deps, tt.parent); d == nil || d.Type != types.DepParentChild {
					t.Errorf("dependencies of %s = %s, want its parent-child edge to %s", tt.child, fmtDeps(deps), tt.parent)
				}
			}
			if prefix, err := testStore.GetConfig(ctx, "issue_prefix"); err != nil || prefix != "kb" {
				t.Errorf("issue_prefix = %q (err %v), want kb", prefix, err)
			}
		})
	}
}

func TestRepairPrefixesKeepsAnEdgeThatWouldCloseACycle(t *testing.T) {
	// An older store can hold a cycle across prefixes: taxdb-a blocks on
	// cre-b, and cre-b on taxdb-a. Once both are kb-*, the first edge
	// re-pointed lands and storage refuses the other for the cycle. That
	// refusal must be reported and stepped over like the others, and the run
	// must still set issue_prefix instead of stopping after the renames.
	ctx := context.Background()
	testStore := newTestStore(t, filepath.Join(t.TempDir(), "test.db"))
	seedRepairIssues(t, ctx, testStore,
		&types.Issue{ID: "taxdb-a", Title: "Blocked"},
		&types.Issue{ID: "cre-b", Title: "Blocker"},
	)
	if err := testStore.AddDependency(ctx, &types.Dependency{IssueID: "taxdb-a", DependsOnID: "cre-b", Type: types.DepBlocks}, "seeder"); err != nil {
		t.Fatalf("add taxdb-a -> cre-b: %v", err)
	}
	// bd dep add refuses the edge that closes the cycle, so seed it as an
	// older store holds it.
	if _, err := testStore.DB().ExecContext(ctx,
		"INSERT INTO dependencies (id, issue_id, depends_on_external, type, created_at, created_by, metadata) VALUES (UUID(), ?, ?, 'blocks', UTC_TIMESTAMP(), 'seeder', '{}')",
		"cre-b", "taxdb-a"); err != nil {
		t.Fatalf("seed cre-b -> taxdb-a: %v", err)
	}

	result := repairAllJSON(t, ctx, testStore, "kb", false)

	if len(result.UnrepointedEdges) != 1 {
		t.Fatalf("unrepointed edges = %+v, want exactly kb-b -> taxdb-a", result.UnrepointedEdges)
	}
	if e := result.UnrepointedEdges[0]; e.IssueID != "kb-b" || e.DependsOnID != "taxdb-a" || e.WantedDependsOnID != "kb-a" ||
		e.Type != string(types.DepBlocks) || !strings.Contains(e.Error, "cycle") {
		t.Errorf("unrepointed edge = %+v, want kb-b -> taxdb-a (blocks, wanted kb-a) refused for the cycle", e)
	}
	deps, err := testStore.GetDependencyRecords(ctx, "kb-b")
	if err != nil {
		t.Fatalf("dependencies of kb-b: %v", err)
	}
	if d := depTo(deps, "taxdb-a"); d == nil || d.CreatedBy != "seeder" {
		t.Errorf("dependencies of kb-b = %s, want the refused edge to taxdb-a left as it was", fmtDeps(deps))
	}
	if blocked, blockers, err := testStore.IsBlocked(ctx, "kb-a"); err != nil || !blocked || !slices.Contains(blockers, "kb-b") {
		t.Errorf("IsBlocked(kb-a) = %v %v (err %v), want blocked by kb-b: the edge re-pointed first lands", blocked, blockers, err)
	}
	if prefix, err := testStore.GetConfig(ctx, "issue_prefix"); err != nil || prefix != "kb" {
		t.Errorf("issue_prefix = %q (err %v), want kb", prefix, err)
	}
}

func TestRepairPrefixesReportsARefusedIssueColumnEdgeAsLeftAsItWas(t *testing.T) {
	// A database older than the dependency-target split, or a bd rename,
	// keeps edges between two prefixes in the issue column, where they gate
	// readiness. The repair cannot see the column, so it re-adds such an
	// edge too; when storage refuses the re-add, the edge stays where it was
	// and still blocks. The report must say it was left as it was, and must
	// not call it inert.
	ctx := context.Background()
	testStore := newTestStore(t, filepath.Join(t.TempDir(), "test.db"))
	seedRepairIssues(t, ctx, testStore,
		&types.Issue{ID: "kb-p", Title: "Grandparent", IssueType: types.TypeEpic},
		&types.Issue{ID: "taxdb-c", Title: "Parent", IssueType: types.TypeEpic},
		&types.Issue{ID: "taxdb-gc", Title: "Grandchild"},
	)
	for _, edge := range []struct{ from, typ string }{{"taxdb-c", "parent-child"}, {"taxdb-gc", "blocks"}} {
		if _, err := testStore.DB().ExecContext(ctx,
			"INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by, metadata) VALUES (UUID(), ?, 'kb-p', ?, UTC_TIMESTAMP(), 'seeder', '{}')",
			edge.from, edge.typ); err != nil {
			t.Fatalf("seed %s -%s-> kb-p in the issue column: %v", edge.from, edge.typ, err)
		}
	}
	if err := testStore.AddDependency(ctx, &types.Dependency{IssueID: "taxdb-gc", DependsOnID: "taxdb-c", Type: types.DepParentChild}, "seeder"); err != nil {
		t.Fatalf("add parent-child: %v", err)
	}
	if blocked, blockers, err := testStore.IsBlocked(ctx, "taxdb-gc"); err != nil || !blocked || !slices.Contains(blockers, "kb-p") {
		t.Fatalf("fixture: IsBlocked(taxdb-gc) = %v %v (err %v), want blocked by kb-p", blocked, blockers, err)
	}

	out := captureStdout(t, func() error { return repairAll(t, ctx, testStore, "kb", false) })

	if !strings.Contains(out, "left exactly as they were") || !strings.Contains(out, "-> kb-p (blocks, wanted ") {
		t.Errorf("repair report does not list kb-gc -> kb-p as left as it was:\n%s", out)
	}
	if strings.Contains(out, "readiness") {
		t.Errorf("repair report makes a readiness claim about an edge that still blocks:\n%s", out)
	}
	deps, err := testStore.GetDependencyRecords(ctx, "kb-gc")
	if err != nil {
		t.Fatalf("dependencies of kb-gc: %v", err)
	}
	if d := depTo(deps, "kb-p"); d == nil || d.Type != types.DepBlocks || d.CreatedBy != "seeder" {
		t.Errorf("dependencies of kb-gc = %s, want the refused blocks edge to kb-p left in place", fmtDeps(deps))
	}
	if blocked, blockers, err := testStore.IsBlocked(ctx, "kb-gc"); err != nil || !blocked || !slices.Contains(blockers, "kb-p") {
		t.Errorf("IsBlocked(kb-gc) = %v %v (err %v), want it still blocked by kb-p", blocked, blockers, err)
	}
}

// indeterminateRepointStore runs every transaction, then reports the one that
// added an edge on failSource as of unknown outcome, as storage does when the
// Dolt commit after a landed SQL commit fails.
type indeterminateRepointStore struct {
	storage.DoltStorage
	failSource string
	injected   bool
}

func (s *indeterminateRepointStore) RunInTransaction(ctx context.Context, commitMsg string, fn func(tx storage.Transaction) error) error {
	added := false
	err := s.DoltStorage.RunInTransaction(ctx, commitMsg, func(tx storage.Transaction) error {
		return fn(&addSpyTx{Transaction: tx, added: func(dep *types.Dependency) { added = added || dep.IssueID == s.failSource }})
	})
	if err != nil || !added {
		return err
	}
	s.injected = true
	return fmt.Errorf("injected Dolt commit failure: %w", storage.ErrCommitIndeterminate)
}

// addSpyTx reports each edge added through it.
type addSpyTx struct {
	storage.Transaction
	added func(dep *types.Dependency)
}

func (t *addSpyTx) AddDependency(ctx context.Context, dep *types.Dependency, actor string) error {
	t.added(dep)
	return t.Transaction.AddDependency(ctx, dep, actor)
}

func TestRepairPrefixesStopsOnAFailureThatIsNotARefusal(t *testing.T) {
	// Only storage refusing an edge (a cycle, a hierarchy conflict, ...) is
	// reported and stepped over. A re-point whose commit outcome is unknown
	// may have moved the edge, so reporting it as left as it was would be
	// false: the run stops with the error before setting issue_prefix, and
	// the error names the edge refused before it, whose report the run never
	// reaches.
	ctx := context.Background()
	testStore := newTestStore(t, filepath.Join(t.TempDir(), "test.db"))
	seedRepairIssues(t, ctx, testStore,
		&types.Issue{ID: "kb-p", Title: "Grandparent", IssueType: types.TypeEpic},
		&types.Issue{ID: "taxdb-c", Title: "Parent", IssueType: types.TypeEpic},
		&types.Issue{ID: "taxdb-gc", Title: "Grandchild"},
		&types.Issue{ID: "taxdb-z", Title: "Blocked across prefixes"},
		&types.Issue{ID: "cre-y", Title: "Blocker"},
	)
	for _, dep := range []*types.Dependency{
		{IssueID: "taxdb-c", DependsOnID: "kb-p", Type: types.DepParentChild},
		{IssueID: "taxdb-gc", DependsOnID: "taxdb-c", Type: types.DepParentChild},
		{IssueID: "taxdb-gc", DependsOnID: "kb-p", Type: types.DepBlocks},
		{IssueID: "taxdb-z", DependsOnID: "cre-y", Type: types.DepBlocks},
	} {
		if err := testStore.AddDependency(ctx, dep, "seeder"); err != nil {
			t.Fatalf("add %s -> %s: %v", dep.IssueID, dep.DependsOnID, err)
		}
	}
	st := &indeterminateRepointStore{DoltStorage: testStore, failSource: "kb-z"}

	_, err := captureStdoutWithError(t, func() error { return repairAll(t, ctx, st, "kb", false) })

	if !st.injected {
		t.Fatalf("kb-z's edge was never re-pointed (err %v)", err)
	}
	if !errors.Is(err, storage.ErrCommitIndeterminate) {
		t.Fatalf("repair error = %v, want the indeterminate commit, not a reported refusal", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "kb-z -> cre-y at kb-y") || !strings.Contains(msg, "kb-gc -> kb-p (blocks, wanted kb-p)") {
		t.Errorf("repair error = %q, want it to name the failed edge and the refused kb-gc -> kb-p", msg)
	}
	if prefix, err := testStore.GetConfig(ctx, "issue_prefix"); err != nil || prefix != "test" {
		t.Errorf("issue_prefix = %q (err %v), want it unchanged after the failure", prefix, err)
	}
}

func TestRepairPrefixesKeepsSuffixesWithoutCollision(t *testing.T) {
	// GH#6591: consolidating prefixes with --repair should keep each issue's
	// suffix (cre-db-c2a -> kb-c2a) when the target id is free, instead of
	// minting a fresh hash id for every off-prefix issue.
	ctx := context.Background()
	testStore := newTestStore(t, filepath.Join(t.TempDir(), "test.db"))

	for _, issue := range []*types.Issue{
		{ID: "kb-u6ch", Title: "Kept", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "cre-db-c2a", Title: "Other family", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "taxdb-5su1", Title: "Third family", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	} {
		if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("create %s: %v", issue.ID, err)
		}
	}

	allIssues, err := testStore.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if err := repairPrefixes(ctx, testStore, "test", "kb", allIssues, detectPrefixes(allIssues), false); err != nil {
		t.Fatalf("repair failed: %v", err)
	}

	allIssues, err = testStore.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("search after repair: %v", err)
	}
	got := map[string]bool{}
	for _, issue := range allIssues {
		got[issue.ID] = true
	}
	for _, want := range []string{"kb-u6ch", "kb-c2a", "kb-5su1"} {
		if !got[want] {
			t.Errorf("expected suffix-preserving id %s after repair; ids now: %v", want, got)
		}
	}
}

func TestRepairPrefixesKeepsDottedChildren(t *testing.T) {
	// GH#6591 AC-2: a dotted child keeps its path under the renamed root, an
	// orphan child (no root row) is mapped through its virtual root, the
	// parent-child edge still resolves, and text references to both children
	// point at the new IDs.
	ctx := context.Background()
	testStore := newTestStore(t, filepath.Join(t.TempDir(), "test.db"))
	seedRepairIssues(t, ctx, testStore,
		&types.Issue{ID: "taxdb-5su1", Title: "Parent", IssueType: types.TypeEpic},
		&types.Issue{ID: "taxdb-5su1.13", Title: "Child"},
		&types.Issue{ID: "cre-db-nwb.3", Title: "Orphan child"},
		&types.Issue{ID: "cre-db-c2a", Title: "Referrer", Description: "see taxdb-5su1.13 and cre-db-nwb.3."},
	)
	if err := testStore.AddDependency(ctx, &types.Dependency{IssueID: "taxdb-5su1.13", DependsOnID: "taxdb-5su1", Type: types.DepParentChild}, "test"); err != nil {
		t.Fatalf("add parent-child: %v", err)
	}

	if err := repairAll(t, ctx, testStore, "kb", false); err != nil {
		t.Fatalf("repair failed: %v", err)
	}

	got := storedIDs(t, ctx, testStore)
	for _, want := range []string{"kb-5su1", "kb-5su1.13", "kb-nwb.3", "kb-c2a"} {
		if got[want] == nil {
			t.Fatalf("expected %s after repair; ids now: %v", want, got)
		}
	}
	if desc := got["kb-c2a"].Description; desc != "see kb-5su1.13 and kb-nwb.3." {
		t.Errorf("referrer description = %q, want both child references rewritten", desc)
	}
	deps, err := testStore.GetDependencyRecords(ctx, "kb-5su1.13")
	if err != nil {
		t.Fatalf("dependencies of kb-5su1.13: %v", err)
	}
	if len(deps) != 1 || deps[0].DependsOnID != "kb-5su1" || deps[0].Type != types.DepParentChild {
		t.Fatalf("dependencies of kb-5su1.13 = %+v, want parent-child on kb-5su1", deps)
	}
	if parent, err := testStore.GetIssue(ctx, deps[0].DependsOnID); err != nil || parent == nil || parent.Title != "Parent" {
		t.Errorf("parent of kb-5su1.13 does not resolve: %+v (err %v)", parent, err)
	}
}

// repairJSONResult is the single object bd rename-prefix --repair --json
// writes.
type repairJSONResult struct {
	TargetPrefix    string `json:"target_prefix"`
	PrefixesFound   int    `json:"prefixes_found"`
	IssuesRepaired  int    `json:"issues_repaired"`
	IssuesUnchanged int    `json:"issues_unchanged"`
	DryRun          bool   `json:"dry_run"`
	Renames         []struct {
		OldID string `json:"old_id"`
		NewID string `json:"new_id"`
	} `json:"renames"`
	Collisions []struct {
		OldRoot      string   `json:"old_root"`
		WantedRoot   string   `json:"wanted_root"`
		AssignedRoot string   `json:"assigned_root"`
		Reason       string   `json:"reason"`
		ConflictID   string   `json:"conflict_id"`
		IDs          []string `json:"ids"`
	} `json:"collisions"`
	UnrepointedEdges []struct {
		IssueID           string `json:"issue_id"`
		DependsOnID       string `json:"depends_on_id"`
		WantedDependsOnID string `json:"wanted_depends_on_id"`
		Type              string `json:"type"`
		Error             string `json:"error"`
	} `json:"unrepointed_edges"`
}

// repairAllJSON runs the repair under --json and decodes stdout, which must
// hold exactly one JSON object.
func repairAllJSON(t *testing.T, ctx context.Context, st storage.DoltStorage, target string, dryRun bool) repairJSONResult {
	t.Helper()
	oldJSON := jsonOutput
	jsonOutput = true
	defer func() { jsonOutput = oldJSON }()
	out := captureStdout(t, func() error { return repairAll(t, ctx, st, target, dryRun) })
	var result repairJSONResult
	dec := json.NewDecoder(strings.NewReader(out))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&result); err != nil {
		t.Fatalf("repair --json stdout is not one result object: %v\n%s", err, out)
	}
	if dec.More() {
		t.Fatalf("repair --json stdout holds more than one JSON value:\n%s", out)
	}
	return result
}

// retiringRepairStore reports retiredIDs as retired, and on its second probe
// (the first hash root tried for the first conflicting family) reports every
// probed ID retired, recording them in rejectedProbe.
type retiringRepairStore struct {
	storage.DoltStorage
	retiredIDs    map[string]bool
	probes        int
	rejectedProbe []string
}

func (s *retiringRepairStore) RetiredIssueIDs(_ context.Context, ids []string) (map[string]time.Time, error) {
	s.probes++
	out := make(map[string]time.Time)
	for _, id := range ids {
		if s.retiredIDs[id] || s.probes == 2 {
			out[id] = time.Unix(1, 0)
		}
	}
	if s.probes == 2 {
		s.rejectedProbe = append(s.rejectedProbe, ids...)
	}
	return out, nil
}

func TestRepairPrefixesCollisionReRootsTree(t *testing.T) {
	// GH#6591 AC-3: a family whose wanted root namespace is live (here only a
	// target child holds it), claimed by an earlier source family, or retired
	// moves whole to one unique, non-retired hash root with its dotted paths
	// intact, and every fallback is reported.
	ctx := context.Background()
	testStore := newTestStore(t, filepath.Join(t.TempDir(), "test.db"))
	seedRepairIssues(t, ctx, testStore,
		&types.Issue{ID: "kb-5su1.2", Title: "Target child holding kb-5su1"},
		&types.Issue{ID: "taxdb-5su1", Title: "Tax parent", IssueType: types.TypeEpic},
		&types.Issue{ID: "taxdb-5su1.13", Title: "Tax child"},
		&types.Issue{ID: "taxdb-5su1.13.2", Title: "Tax grandchild"},
		&types.Issue{ID: "cre-db-c2a", Title: "CRE c2a"},
		&types.Issue{ID: "taxdb-c2a", Title: "Tax c2a", Description: "twin of cre-db-c2a, under taxdb-5su1.13"},
		&types.Issue{ID: "cre-db-nwb.3", Title: "Orphan whose destination was retired"},
	)
	if err := testStore.AddDependency(ctx, &types.Dependency{IssueID: "taxdb-5su1.13.2", DependsOnID: "taxdb-5su1.13", Type: types.DepParentChild}, "test"); err != nil {
		t.Fatalf("add parent-child: %v", err)
	}
	st := &retiringRepairStore{DoltStorage: testStore, retiredIDs: map[string]bool{"kb-nwb.3": true}}

	result := repairAllJSON(t, ctx, st, "kb", false)

	hashRoot := regexp.MustCompile(`^kb-[0-9a-f]{8}$`)
	type want struct{ wanted, reason, conflict string }
	wants := map[string]want{
		"cre-db-nwb": {"kb-nwb", "retired", "kb-nwb.3"},
		"taxdb-5su1": {"kb-5su1", "exists", "kb-5su1.2"},
		"taxdb-c2a":  {"kb-c2a", "claimed", "cre-db-c2a"},
	}
	if len(result.Collisions) != len(wants) {
		t.Fatalf("collisions = %+v, want one per conflicting family %v", result.Collisions, wants)
	}
	assigned := map[string]string{}
	seen := map[string]bool{}
	for _, c := range result.Collisions {
		w, ok := wants[c.OldRoot]
		if !ok || c.WantedRoot != w.wanted || c.Reason != w.reason || c.ConflictID != w.conflict {
			t.Errorf("collision %+v, want %+v for %s", c, w, c.OldRoot)
		}
		if !hashRoot.MatchString(c.AssignedRoot) || seen[c.AssignedRoot] {
			t.Errorf("collision %s assigned %q, want a unique kb-<8hex> root", c.OldRoot, c.AssignedRoot)
		}
		seen[c.AssignedRoot] = true
		assigned[c.OldRoot] = c.AssignedRoot
	}
	if len(st.rejectedProbe) == 0 || st.rejectedProbe[0] == assigned["cre-db-nwb"] {
		t.Errorf("the retired hash root %v was not rejected for cre-db-nwb (assigned %s)", st.rejectedProbe, assigned["cre-db-nwb"])
	}
	for _, c := range result.Collisions {
		if c.OldRoot == "taxdb-5su1" && fmt.Sprint(c.IDs) != "[taxdb-5su1 taxdb-5su1.13 taxdb-5su1.13.2]" {
			t.Errorf("taxdb-5su1 collision ids = %v, want the whole family", c.IDs)
		}
	}

	tax := assigned["taxdb-5su1"]
	wantIDs := []string{"kb-5su1.2", "kb-c2a", assigned["cre-db-nwb"] + ".3", tax, tax + ".13", tax + ".13.2", assigned["taxdb-c2a"]}
	got := storedIDs(t, ctx, testStore)
	if len(got) != len(wantIDs) {
		t.Fatalf("stored ids = %v, want %v", got, wantIDs)
	}
	for _, id := range wantIDs {
		if got[id] == nil {
			t.Fatalf("expected %s after repair; ids now: %v", id, got)
		}
	}
	if got[tax+".13.2"].Title != "Tax grandchild" || got["kb-c2a"].Title != "CRE c2a" {
		t.Errorf("families landed on the wrong roots: %v", got)
	}
	if desc, wantDesc := got[assigned["taxdb-c2a"]].Description, "twin of kb-c2a, under "+tax+".13"; desc != wantDesc {
		t.Errorf("re-rooted text = %q, want %q", desc, wantDesc)
	}
	deps, err := testStore.GetDependencyRecords(ctx, tax+".13.2")
	if err != nil || len(deps) != 1 || deps[0].DependsOnID != tax+".13" {
		t.Errorf("dependencies of %s.13.2 = %+v (err %v), want parent %s.13", tax, deps, err, tax)
	}
	for _, r := range result.Renames {
		if got[r.NewID] == nil {
			t.Errorf("reported rename %s -> %s is not stored", r.OldID, r.NewID)
		}
	}
}

func fmtDeps(deps []*types.Dependency) string {
	var parts []string
	for _, d := range deps {
		parts = append(parts, fmt.Sprintf("%s -%s-> %s", d.IssueID, d.Type, d.DependsOnID))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func TestRepairPrefixesSplitsByKnownPrefix(t *testing.T) {
	// GH#6591 AC-4: a source prefix with a hyphen is split off whole, even for
	// an all-letter suffix the heuristic reads as part of the prefix, whether
	// the family is observed on another row or declared in allowed_prefixes;
	// a custom ID already under the target prefix is left alone.
	for _, tt := range []struct {
		name     string
		ids      []string
		declared string
		want     []string
	}{
		{name: "observed", ids: []string{"cre-db-a1b", "cre-db-wxyz", "kb-adr-0001"}, want: []string{"kb-a1b", "kb-wxyz", "kb-adr-0001"}},
		{name: "declared", ids: []string{"cre-db-a1b", "cre-db-wxyz", "kb-adr-0001"}, declared: "other, cre-db-", want: []string{"kb-a1b", "kb-wxyz", "kb-adr-0001"}},
		{name: "declared only", ids: []string{"cre-db-wxyz", "taxdb-u6ch", "kb-adr-0001"}, declared: "cre-db", want: []string{"kb-wxyz", "kb-u6ch", "kb-adr-0001"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			testStore := newTestStore(t, filepath.Join(t.TempDir(), "test.db"))
			if tt.declared != "" {
				if err := testStore.SetConfig(ctx, "allowed_prefixes", tt.declared); err != nil {
					t.Fatalf("set allowed_prefixes: %v", err)
				}
			}
			for i, id := range tt.ids {
				seedRepairIssues(t, ctx, testStore, &types.Issue{ID: id, Title: fmt.Sprintf("row %d", i)})
			}

			if err := repairAll(t, ctx, testStore, "kb", false); err != nil {
				t.Fatalf("repair failed: %v", err)
			}

			got := storedIDs(t, ctx, testStore)
			if len(got) != len(tt.want) {
				t.Fatalf("stored ids = %v, want %v", got, tt.want)
			}
			for i, id := range tt.want {
				if got[id] == nil || got[id].Title != fmt.Sprintf("row %d", i) {
					t.Errorf("expected %s to hold %s; ids now: %v", id, tt.ids[i], got)
				}
			}
		})
	}
}

// repairSnapshot is everything a repair may change: each issue's text and
// edges, and the stored issue_prefix.
func repairSnapshot(t *testing.T, ctx context.Context, st storage.DoltStorage) string {
	t.Helper()
	got := storedIDs(t, ctx, st)
	ids := make([]string, 0, len(got))
	for id := range got {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var b strings.Builder
	for _, id := range ids {
		deps, err := st.GetDependencyRecords(ctx, id)
		if err != nil {
			t.Fatalf("deps of %s: %v", id, err)
		}
		fmt.Fprintf(&b, "%s %q %q %s\n", id, got[id].Title, got[id].Description, fmtDeps(deps))
	}
	prefix, err := st.GetConfig(ctx, "issue_prefix")
	if err != nil {
		t.Fatalf("issue_prefix: %v", err)
	}
	fmt.Fprintf(&b, "issue_prefix=%s\n", prefix)
	return b.String()
}

func TestRepairPrefixesDryRunMatchesRealRun(t *testing.T) {
	// GH#6591 AC-5: against unchanged inputs, --dry-run writes nothing and
	// reports the same renames and collisions the real run then performs, and
	// the real run's JSON mapping is what the store holds.
	ctx := context.Background()
	testStore := newTestStore(t, filepath.Join(t.TempDir(), "test.db"))
	seedRepairIssues(t, ctx, testStore,
		&types.Issue{ID: "kb-c2a", Title: "Live target"},
		&types.Issue{ID: "kb-5su1.2", Title: "Target child holding kb-5su1"},
		&types.Issue{ID: "taxdb-c2a", Title: "Tax c2a", Description: "see taxdb-u6ch"},
		&types.Issue{ID: "cre-db-c2a", Title: "CRE c2a"},
		&types.Issue{ID: "taxdb-u6ch", Title: "Tax u6ch"},
		&types.Issue{ID: "taxdb-5su1", Title: "Tax parent"},
		&types.Issue{ID: "taxdb-5su1.13", Title: "Tax child"},
		&types.Issue{ID: "cre-db-nwb.3", Title: "Orphan on a retired root"},
	)
	if err := testStore.AddDependency(ctx, &types.Dependency{IssueID: "taxdb-5su1.13", DependsOnID: "taxdb-5su1", Type: types.DepParentChild}, "test"); err != nil {
		t.Fatalf("add parent-child: %v", err)
	}
	// Each run gets a fresh probe store, so both see the same retirements.
	probeStore := func() storage.DoltStorage {
		return &retiringRepairStore{DoltStorage: testStore, retiredIDs: map[string]bool{"kb-nwb.3": true}}
	}

	before := repairSnapshot(t, ctx, testStore)
	dry := repairAllJSON(t, ctx, probeStore(), "kb", true)
	if after := repairSnapshot(t, ctx, testStore); after != before {
		t.Fatalf("dry run wrote:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	live := repairAllJSON(t, ctx, probeStore(), "kb", false)

	if !dry.DryRun || live.DryRun {
		t.Errorf("dry_run flags = %v, %v; want true, false", dry.DryRun, live.DryRun)
	}
	if len(live.Collisions) != 4 {
		t.Errorf("collisions = %+v, want taxdb-c2a, cre-db-c2a, taxdb-5su1 and cre-db-nwb", live.Collisions)
	}
	if !reflect.DeepEqual(dry.Renames, live.Renames) || !reflect.DeepEqual(dry.Collisions, live.Collisions) {
		t.Fatalf("dry run and real run disagree:\ndry:  %+v\nreal: %+v", dry, live)
	}
	if dry.IssuesRepaired != live.IssuesRepaired || dry.IssuesUnchanged != live.IssuesUnchanged || live.IssuesRepaired != len(live.Renames) {
		t.Errorf("counts differ: dry %+v, real %+v", dry, live)
	}

	got := storedIDs(t, ctx, testStore)
	if len(got) != live.IssuesRepaired+live.IssuesUnchanged {
		t.Errorf("stored %d issues, want %d repaired + %d unchanged", len(got), live.IssuesRepaired, live.IssuesUnchanged)
	}
	for _, r := range live.Renames {
		if got[r.NewID] == nil || got[r.OldID] != nil {
			t.Errorf("rename %s -> %s is not what the store holds: %v", r.OldID, r.NewID, got)
		}
	}
	if got["kb-u6ch"] == nil || got["kb-c2a"].Title != "Live target" || got["kb-5su1.2"] == nil {
		t.Errorf("free roots or live targets moved: %v", got)
	}
}
