//go:build cgo

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
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

func TestRepairPrefixesCollisionReRootsTree(t *testing.T) {
	// GH#6591 AC-3: a family whose natural destination is taken — here by a
	// live child-only namespace and by an earlier family's claim — moves as
	// a whole to one generated hash root, dotted paths intact, while
	// target-prefixed rows stay untouched.
	ctx := context.Background()
	testStore := newTestStore(t, filepath.Join(t.TempDir(), "test.db"))

	for _, issue := range []*types.Issue{
		{ID: "kb-5su1.13", Title: "Live child under the wanted root", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "kb-hand", Title: "Hand-named target id", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "taxdb-5su1", Title: "Blocked root", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "taxdb-5su1.13", Title: "Blocked child", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "taxdb-5su1.13.2", Title: "Blocked grandchild", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "cre-db-u6ch", Title: "First claimant", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "taxdb-u6ch", Title: "Second claimant", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	} {
		if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create issue %s: %v", issue.ID, err)
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
	idByTitle := make(map[string]string, len(allIssues))
	for _, issue := range allIssues {
		idByTitle[issue.Title] = issue.ID
	}
	generatedRoot := regexp.MustCompile(`^kb-[0-9a-f]{8}$`)

	// The blocked tree moved whole: one generated root, nested paths intact.
	blockedRoot := idByTitle["Blocked root"]
	if !generatedRoot.MatchString(blockedRoot) {
		t.Fatalf("blocked family re-rooted to %q, want a generated kb-<8hex> root", blockedRoot)
	}
	for _, tt := range []struct{ title, suffix string }{
		{"Blocked child", ".13"},
		{"Blocked grandchild", ".13.2"},
	} {
		if got := idByTitle[tt.title]; got != blockedRoot+tt.suffix {
			t.Errorf("%s -> %q, want %q (path intact under the generated root)", tt.title, got, blockedRoot+tt.suffix)
		}
	}

	// The first claimant took the natural destination; the second fell back
	// to its own generated root.
	if got := idByTitle["First claimant"]; got != "kb-u6ch" {
		t.Errorf("First claimant -> %q, want the natural kb-u6ch", got)
	}
	secondRoot := idByTitle["Second claimant"]
	if !generatedRoot.MatchString(secondRoot) || secondRoot == blockedRoot {
		t.Errorf("Second claimant -> %q, want a distinct generated root", secondRoot)
	}

	// Target rows stayed put; every off-target id is gone.
	for _, id := range []string{"kb-5su1.13", "kb-hand"} {
		if issue, err := testStore.GetIssue(ctx, id); err != nil || issue == nil {
			t.Errorf("expected target row %s unchanged: %v", id, err)
		}
	}
	for _, id := range []string{"taxdb-5su1", "taxdb-5su1.13", "taxdb-5su1.13.2", "cre-db-u6ch", "taxdb-u6ch"} {
		if issue, err := testStore.GetIssue(ctx, id); err == nil && issue != nil {
			t.Errorf("expected old id %s to be gone", id)
		}
	}

	prefix, err := testStore.GetConfig(ctx, "issue_prefix")
	if err != nil || prefix != "kb" {
		t.Errorf("issue_prefix after repair = %q (%v), want kb", prefix, err)
	}
}

func TestRepairPrefixesUsesKnownPrefixes(t *testing.T) {
	// GH#6591 AC-4: prefix extraction honors the workspace's known prefixes
	// (issue_prefix, allowed_prefixes, and the prefixes detected on the
	// rows) before falling back to the heuristic, so a hyphenated family
	// like cre-db keeps its whole suffix: cre-db-wxyz -> kb-wxyz, not the
	// heuristic's kb-db-wxyz.
	ctx := context.Background()
	testStore := newTestStore(t, filepath.Join(t.TempDir(), "test.db"))

	if err := testStore.SetConfig(ctx, "issue_prefix", "cre-db"); err != nil {
		t.Fatalf("set issue_prefix: %v", err)
	}
	if err := testStore.SetConfig(ctx, "allowed_prefixes", "cre-db,vc"); err != nil {
		t.Fatalf("set allowed_prefixes: %v", err)
	}

	for _, issue := range []*types.Issue{
		{ID: "cre-db-a1b", Title: "Hash-like suffix", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "cre-db-wxyz", Title: "Word-like suffix", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "kb-adr-0001", Title: "Hand-named target id", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	} {
		if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create issue %s: %v", issue.ID, err)
		}
	}

	allIssues, err := testStore.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if err := repairPrefixes(ctx, testStore, "test", "kb", allIssues, detectPrefixes(allIssues), false); err != nil {
		t.Fatalf("repair failed: %v", err)
	}

	for _, id := range []string{"kb-a1b", "kb-wxyz", "kb-adr-0001"} {
		if issue, err := testStore.GetIssue(ctx, id); err != nil || issue == nil {
			t.Errorf("expected %s after repair: %v", id, err)
		}
	}
	for _, id := range []string{"cre-db-a1b", "cre-db-wxyz"} {
		if issue, err := testStore.GetIssue(ctx, id); err == nil && issue != nil {
			t.Errorf("expected old id %s to be gone", id)
		}
	}
	prefix, err := testStore.GetConfig(ctx, "issue_prefix")
	if err != nil || prefix != "kb" {
		t.Errorf("issue_prefix after repair = %q (%v), want kb", prefix, err)
	}
}

// captureRepairStdout captures everything fn writes to stdout. The caller
// must not be parallel: the swap is process-global.
func captureRepairStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()
	fn()
	_ = w.Close()
	os.Stdout = old
	<-done
	_ = r.Close()
	return buf.String()
}

func TestRepairPrefixesDryRunMatchesExecution(t *testing.T) {
	// GH#6591 AC-5: --json repair output is exactly one object — the same
	// object for a dry run and the execution it previews — carrying the
	// legacy summary fields plus dry_run, the complete rename list and every
	// collision, with no text progress on the stream. The dry run writes
	// nothing.
	ctx := context.Background()
	testStore := newTestStore(t, filepath.Join(t.TempDir(), "test.db"))

	for _, issue := range []*types.Issue{
		{ID: "kb-5su1.13", Title: "Live child", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "kb-hand", Title: "Hand-named target id", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "taxdb-5su1", Title: "Blocked parent", Description: "see taxdb-5su1.13", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "taxdb-5su1.13", Title: "Blocked child", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "cre-db-c2a", Title: "Free family", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	} {
		if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create issue %s: %v", issue.ID, err)
		}
	}
	allIssues, err := testStore.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	prefixes := detectPrefixes(allIssues)

	oldJSON := jsonOutput
	jsonOutput = true
	defer func() { jsonOutput = oldJSON }()

	runJSON := func(dryRun bool) repairJSONResult {
		t.Helper()
		var result repairJSONResult
		out := captureRepairStdout(t, func() {
			if err := repairPrefixes(ctx, testStore, "test", "kb", allIssues, prefixes, dryRun); err != nil {
				t.Fatalf("repair (dry_run=%v): %v", dryRun, err)
			}
		})
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatalf("dry_run=%v stdout is not exactly one JSON object:\n%s\n%v", dryRun, out, err)
		}
		return result
	}

	preview := runJSON(true)
	if !preview.DryRun {
		t.Errorf("preview dry_run = false, want true")
	}
	if preview.TargetPrefix != "kb" || preview.IssuesUnchanged != 2 || preview.IssuesRepaired != 3 || preview.PrefixesFound != len(prefixes) {
		t.Errorf("preview summary = %+v, want target kb, 2 unchanged, 3 repaired", preview)
	}
	if len(preview.Renames) != 3 {
		t.Fatalf("preview renames = %+v, want the complete list of 3", preview.Renames)
	}
	previewByFrom := map[string]string{}
	for _, r := range preview.Renames {
		previewByFrom[r.From] = r.To
	}
	if previewByFrom["cre-db-c2a"] != "kb-c2a" {
		t.Errorf("preview renames = %v, want cre-db-c2a -> kb-c2a", previewByFrom)
	}
	if len(preview.Collisions) != 1 {
		t.Fatalf("preview collisions = %+v, want the taxdb family", preview.Collisions)
	}
	if c := preview.Collisions[0]; c.OldRoot != "taxdb-5su1" || c.Reason != repairReasonExists || c.Conflicting != "kb-5su1.13" || !regexp.MustCompile(`^kb-[0-9a-f]{8}$`).MatchString(c.AssignedRoot) {
		t.Errorf("preview collision = %+v, want taxdb-5su1 blocked by kb-5su1.13 on a generated root", c)
	}

	// The dry run wrote nothing.
	for _, id := range []string{"taxdb-5su1", "taxdb-5su1.13", "cre-db-c2a"} {
		if issue, err := testStore.GetIssue(ctx, id); err != nil || issue == nil {
			t.Errorf("dry run changed the database: %s is gone (%v)", id, err)
		}
	}
	if prefix, err := testStore.GetConfig(ctx, "issue_prefix"); err == nil && prefix == "kb" {
		t.Errorf("dry run set issue_prefix = kb")
	}

	applied := runJSON(false)
	if applied.DryRun {
		t.Errorf("applied dry_run = true, want false")
	}
	appliedByFrom := map[string]string{}
	for _, r := range applied.Renames {
		appliedByFrom[r.From] = r.To
	}
	if !reflect.DeepEqual(previewByFrom, appliedByFrom) {
		t.Errorf("execution renames %v disagree with the dry run's %v", appliedByFrom, previewByFrom)
	}

	// What the database holds matches the reported mappings.
	for from, to := range appliedByFrom {
		if issue, err := testStore.GetIssue(ctx, to); err != nil || issue == nil {
			t.Errorf("reported rename %s -> %s but %s is absent: %v", from, to, to, err)
		}
		if issue, err := testStore.GetIssue(ctx, from); err == nil && issue != nil {
			t.Errorf("reported rename %s -> %s but %s remains", from, to, from)
		}
	}
	// The re-rooted family's text reference followed the root map.
	parent, err := testStore.GetIssue(ctx, preview.Collisions[0].AssignedRoot)
	if err != nil || parent == nil {
		t.Fatalf("get re-rooted parent: %v", err)
	}
	if want := "see " + preview.Collisions[0].AssignedRoot + ".13"; parent.Description != want {
		t.Errorf("parent description = %q, want %q", parent.Description, want)
	}
	if prefix, err := testStore.GetConfig(ctx, "issue_prefix"); err != nil || prefix != "kb" {
		t.Errorf("issue_prefix after repair = %q (%v), want kb", prefix, err)
	}
}

// runRenamePrefixMintOnly points the globals at st and runs
// "rename-prefix <new> --mint-only" in-process.
func runRenamePrefixMintOnly(t *testing.T, st storage.DoltStorage, newPrefix string) error {
	t.Helper()
	oldCtx := rootCtx
	rootCtx = context.Background()
	defer func() { rootCtx = oldCtx }()
	return withRenameStore(st, func() error {
		if err := renamePrefixCmd.Flags().Set("mint-only", "true"); err != nil {
			return err
		}
		defer func() { _ = renamePrefixCmd.Flags().Set("mint-only", "false") }()
		return renamePrefixCmd.RunE(renamePrefixCmd, []string{newPrefix})
	})
}

func TestRenamePrefixMintOnly(t *testing.T) {
	// GH#6591 AC-6: --mint-only switches the prefix new ids mint under
	// without renaming any existing row: allowed_prefixes gains the old
	// prefix (written first), then issue_prefix moves; children under
	// old-prefix parents keep validating; ids and text stay untouched.
	ctx := context.Background()
	testStore := newTestStore(t, filepath.Join(t.TempDir(), "test.db"))
	if err := testStore.SetConfig(ctx, "issue_prefix", "taxdb"); err != nil {
		t.Fatalf("set issue_prefix: %v", err)
	}
	for _, issue := range []*types.Issue{
		{ID: "taxdb-5su1", Title: "Existing root", Description: "see taxdb-5su1.13", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "taxdb-5su1.13", Title: "Existing child", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	} {
		if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("create %s: %v", issue.ID, err)
		}
	}

	if err := runRenamePrefixMintOnly(t, testStore, "kb"); err != nil {
		t.Fatalf("rename-prefix kb --mint-only: %v", err)
	}

	// Every existing row is untouched, ids and text alike.
	root, err := testStore.GetIssue(ctx, "taxdb-5su1")
	if err != nil || root == nil {
		t.Fatalf("mint-only renamed existing rows: taxdb-5su1 is gone (%v)", err)
	}
	if root.Description != "see taxdb-5su1.13" {
		t.Errorf("mint-only rewrote text: description = %q", root.Description)
	}
	if issue, err := testStore.GetIssue(ctx, "kb-5su1"); err == nil && issue != nil {
		t.Errorf("mint-only minted new ids for existing rows: kb-5su1 exists")
	}

	// Config moved: the new prefix mints, the old prefix is allowed.
	prefix, err := testStore.GetConfig(ctx, "issue_prefix")
	if err != nil || prefix != "kb" {
		t.Errorf("issue_prefix = %q (%v), want kb", prefix, err)
	}
	allowed, err := testStore.GetConfig(ctx, "allowed_prefixes")
	if err != nil || !strings.Contains(","+allowed+",", ",taxdb,") {
		t.Errorf("allowed_prefixes = %q (%v), want it to contain taxdb", allowed, err)
	}

	// Ids the new prefix mints validate; children under old-prefix parents
	// validate against the allowed list, the way bd create checks them.
	if err := issueops.ValidateIssueIDPrefix("kb-a1b2c3", "kb", allowed); err != nil {
		t.Errorf("new-prefix id no longer validates: %v", err)
	}
	if err := issueops.ValidateIssueIDPrefix("taxdb-5su1.7", "kb", allowed); err != nil {
		t.Errorf("child under an old-prefix parent no longer validates: %v", err)
	}

	// Mint-only to the same prefix is refused, and it never becomes a repair.
	if err := runRenamePrefixMintOnly(t, testStore, "kb"); err == nil {
		t.Errorf("mint-only to the current prefix succeeded, want refusal")
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
