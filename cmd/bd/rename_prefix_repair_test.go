//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
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
			t.Errorf("dependencies of %s = %+v, want one edge to %s", edge.from, deps, edge.to)
		}
	}
	if prefix, err := testStore.GetConfig(ctx, "issue_prefix"); err != nil || prefix != "kb" {
		t.Errorf("issue_prefix = %q (err %v), want kb", prefix, err)
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
