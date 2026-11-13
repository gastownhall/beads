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

func TestRepairPrefixesKeepsSuffixes(t *testing.T) {
	// GH#6591 AC-1: a suffix-preserving repair keeps each off-prefix row's
	// mnemonic suffix when the destination is free, leaves hand-named target
	// ids alone, carries payload and relationships, and sets issue_prefix.
	ctx := context.Background()
	testStore := newTestStore(t, filepath.Join(t.TempDir(), "test.db"))

	for _, issue := range []*types.Issue{
		{ID: "taxdb-u6ch", Title: "Renamed with payload", Description: "body", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Labels: []string{"area/storage"}},
		{ID: "cre-db-c2a", Title: "Other family", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "cre-db-a1b", Title: "Other family sibling", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "kb-hand", Title: "Hand-named target id", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	} {
		if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("create %s: %v", issue.ID, err)
		}
	}
	// A relationship inside one family: dep targets only follow a rename when
	// they are issue-routed, which they are for same-prefix pairs
	// (isCrossPrefixDep).
	if err := testStore.AddDependency(ctx, &types.Dependency{IssueID: "cre-db-a1b", DependsOnID: "cre-db-c2a", Type: types.DepBlocks}, "test"); err != nil {
		t.Fatalf("add dependency: %v", err)
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
	if len(allIssues) != 4 {
		t.Fatalf("expected 4 issues after repair, got %d", len(allIssues))
	}

	renamed, err := testStore.GetIssue(ctx, "kb-u6ch")
	if err != nil || renamed == nil {
		t.Fatalf("expected suffix-preserving id kb-u6ch after repair: %v", err)
	}
	if renamed.Title != "Renamed with payload" || renamed.Description != "body" || len(renamed.Labels) != 1 || renamed.Labels[0] != "area/storage" {
		t.Errorf("payload did not survive the rename: %+v", renamed)
	}

	for _, id := range []string{"kb-c2a", "kb-a1b"} {
		if other, err := testStore.GetIssue(ctx, id); err != nil || other == nil {
			t.Errorf("expected suffix-preserving id %s after repair: %v", id, err)
		}
	}
	if hand, err := testStore.GetIssue(ctx, "kb-hand"); err != nil || hand == nil {
		t.Errorf("hand-named target id kb-hand must be unchanged: %v", err)
	}

	// The dependency edge followed both renames.
	deps, err := testStore.GetDependencies(ctx, "kb-a1b")
	if err != nil {
		t.Fatalf("dependencies after repair: %v", err)
	}
	if len(deps) != 1 || deps[0].ID != "kb-c2a" {
		t.Errorf("dependency edge did not follow the rename: %+v", deps)
	}

	prefix, err := testStore.GetConfig(ctx, "issue_prefix")
	if err != nil || prefix != "kb" {
		t.Errorf("issue_prefix after repair = %q (%v), want kb", prefix, err)
	}
}

func TestRepairPrefixesRewritesDottedAndOrphanRefs(t *testing.T) {
	// GH#6591 AC-2: dotted children keep their path under the new root
	// (taxdb-5su1.13 -> kb-5su1.13), orphan children whose parent row is
	// missing still land under their wanted root (cre-db-nwb.3 -> kb-nwb.3),
	// the parent-child dependency still resolves, and text references in
	// renamed rows follow the mapping, dotted tails included.
	ctx := context.Background()
	testStore := newTestStore(t, filepath.Join(t.TempDir(), "test.db"))

	for _, issue := range []*types.Issue{
		{ID: "taxdb-5su1", Title: "Parent", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "taxdb-5su1.13", Title: "Child", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "cre-db-nwb.3", Title: "Orphan child", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "cre-db-nwb.4", Title: "Orphan sibling", Description: "see taxdb-5su1.13 and cre-db-nwb.3", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	} {
		if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("create %s: %v", issue.ID, err)
		}
	}
	// The child's parent-child edge follows the rename (same-prefix targets
	// are issue-routed).
	if err := testStore.AddDependency(ctx, &types.Dependency{IssueID: "taxdb-5su1.13", DependsOnID: "taxdb-5su1", Type: types.DepParentChild}, "test"); err != nil {
		t.Fatalf("add dependency: %v", err)
	}

	allIssues, err := testStore.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if err := repairPrefixes(ctx, testStore, "test", "kb", allIssues, detectPrefixes(allIssues), false); err != nil {
		t.Fatalf("repair failed: %v", err)
	}

	for _, id := range []string{"kb-5su1", "kb-5su1.13", "kb-nwb.3", "kb-nwb.4"} {
		if issue, err := testStore.GetIssue(ctx, id); err != nil || issue == nil {
			t.Errorf("expected %s after repair: %v", id, err)
		}
	}
	for _, id := range []string{"taxdb-5su1", "taxdb-5su1.13", "cre-db-nwb.3", "cre-db-nwb.4"} {
		if issue, err := testStore.GetIssue(ctx, id); err == nil && issue != nil {
			t.Errorf("expected old id %s to be gone", id)
		}
	}

	// The parent-child dependency resolves under the new ids.
	deps, err := testStore.GetDependencies(ctx, "kb-5su1.13")
	if err != nil {
		t.Fatalf("dependencies after repair: %v", err)
	}
	if len(deps) != 1 || deps[0].ID != "kb-5su1" {
		t.Errorf("parent-child dependency did not survive the repair: %+v", deps)
	}

	// Both text references were rewritten through the root map.
	sibling, err := testStore.GetIssue(ctx, "kb-nwb.4")
	if err != nil || sibling == nil {
		t.Fatalf("get kb-nwb.4: %v", err)
	}
	if want := "see kb-5su1.13 and kb-nwb.3"; sibling.Description != want {
		t.Errorf("description = %q, want %q", sibling.Description, want)
	}
}

func TestRenameFailureLeavesBeadGatesPending(t *testing.T) {
	// When renaming the awaited bead fails, whether or not that rename
	// landed, its gate is pointed back at the bead's old ID without a
	// sighting: bd gate check must find it pending, never resolve it as
	// though the bead had been deleted. The plain prefix rename renames the
	// gate before its bead (row order), so it moves the gate back under its
	// new ID; the suffix-preserving repair plans families in NaturalCompareIDs
	// order, which renames the bead old-1 before the gate old-gate, so there
	// the gate is still waiting under its old ID when the failure lands.
	for _, tt := range []struct {
		name        string
		gateRenamed bool
		run         func(ctx context.Context, st storage.DoltStorage, issues []*types.Issue) error
	}{
		{name: "rename", run: func(_ context.Context, st storage.DoltStorage, _ []*types.Issue) error {
			return withRenameStore(st, func() error { return runRename(renameCmd, []string{"old-1", "old-renamed"}) })
		}},
		{name: "repair", gateRenamed: false, run: func(ctx context.Context, st storage.DoltStorage, issues []*types.Issue) error {
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
