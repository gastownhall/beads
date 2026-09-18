//go:build cgo

package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
)

func TestRepairMultiplePrefixes(t *testing.T) {
	tmpDir := t.TempDir()
	testDBPath := filepath.Join(tmpDir, "test.db")

	ctx := context.Background()

	testStore, err := dolt.New(ctx, &dolt.Config{Path: testDBPath})
	if err != nil {
		t.Skipf("skipping: Dolt server not available: %v", err)
	}
	defer testStore.Close()

	// Set globals following TestRenamePrefixCommand pattern
	oldStore := store
	oldActor := actor
	oldDBPath := dbPath
	store = testStore
	actor = "test"
	dbPath = testDBPath
	defer func() {
		store = oldStore
		actor = oldActor
		dbPath = oldDBPath
	}()

	// Set initial prefix
	if err := testStore.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("failed to set prefix: %v", err)
	}

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

// TestRepairMultiplePrefixesPreservesSuffix pins beads#6591: a repair with
// no actual suffix collisions must keep each issue's mnemonic suffix
// (including dotted children) under the target prefix instead of minting a
// hash id for every renamed issue.
func TestRepairMultiplePrefixesPreservesSuffix(t *testing.T) {
	tmpDir := t.TempDir()
	testDBPath := filepath.Join(tmpDir, "test.db")

	ctx := context.Background()

	testStore, err := dolt.New(ctx, &dolt.Config{Path: testDBPath})
	if err != nil {
		t.Skipf("skipping: Dolt server not available: %v", err)
	}
	defer testStore.Close()

	oldStore := store
	oldActor := actor
	oldDBPath := dbPath
	store = testStore
	actor = "test"
	dbPath = testDBPath
	defer func() {
		store = oldStore
		actor = oldActor
		dbPath = oldDBPath
	}()

	if err := testStore.SetConfig(ctx, "issue_prefix", "kb"); err != nil {
		t.Fatalf("failed to set prefix: %v", err)
	}

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

	testStore, err := dolt.New(ctx, &dolt.Config{Path: testDBPath})
	if err != nil {
		t.Skipf("skipping: Dolt server not available: %v", err)
	}
	defer testStore.Close()

	oldStore := store
	oldActor := actor
	oldDBPath := dbPath
	store = testStore
	actor = "test"
	dbPath = testDBPath
	defer func() {
		store = oldStore
		actor = oldActor
		dbPath = oldDBPath
	}()

	if err := testStore.SetConfig(ctx, "issue_prefix", "kb"); err != nil {
		t.Fatalf("failed to set prefix: %v", err)
	}

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
