package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

func TestRenumberWithGaps(t *testing.T) {
	// Create a temporary directory for the test database
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	// Initialize store
	testStore, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer testStore.Close()

	ctx := context.Background()

	// Set up config
	if err := testStore.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("failed to set prefix: %v", err)
	}

	// Create issues with large gaps in numbering (simulating compacted database)
	// This reproduces the scenario from bd-345: 107 issues with IDs 1-344
	testIssues := []struct {
		id    string
		title string
	}{
		{"bd-1", "Issue 1"},
		{"bd-4", "Issue 4"},     // Gap here (2, 3 missing)
		{"bd-100", "Issue 100"}, // Large gap
		{"bd-200", "Issue 200"}, // Another large gap
		{"bd-344", "Issue 344"}, // Final issue
	}

	for _, tc := range testIssues {
		issue := &types.Issue{
			ID:          tc.id, // Set explicit ID to simulate gaps
			Title:       tc.title,
			Description: "Test issue for renumbering",
			Priority:    1,
			IssueType:   "task",
			Status:      "open",
		}
		if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}
	}

	// Add a dependency to test that it gets updated
	dep := &types.Dependency{
		IssueID:     "bd-4",
		DependsOnID: "bd-1",
		Type:        "blocks",
	}
	if err := testStore.AddDependency(ctx, dep, "test"); err != nil {
		t.Fatalf("failed to add dependency: %v", err)
	}

	// Get all issues before renumbering
	issuesBefore, err := testStore.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("failed to list issues before: %v", err)
	}
	if len(issuesBefore) != 5 {
		t.Fatalf("expected 5 issues, got %d", len(issuesBefore))
	}

	// Build the ID mapping (what renumber would create)
	idMapping := map[string]string{
		"bd-1":   "bd-1",
		"bd-4":   "bd-2",
		"bd-100": "bd-3",
		"bd-200": "bd-4",
		"bd-344": "bd-5",
	}

	// Temporarily set the global store for renumberIssuesInDB
	oldStore := store
	store = testStore
	defer func() { store = oldStore }()

	// Run the renumbering
	if err := renumberIssuesInDB(ctx, "bd", idMapping, issuesBefore); err != nil {
		t.Fatalf("renumberIssuesInDB failed: %v", err)
	}

	// Verify all issues were renumbered correctly
	issuesAfter, err := testStore.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("failed to list issues after: %v", err)
	}

	if len(issuesAfter) != 5 {
		t.Fatalf("expected 5 issues after renumbering, got %d", len(issuesAfter))
	}

	// Check that new IDs are sequential 1-5
	expectedIDs := map[string]bool{
		"bd-1": true,
		"bd-2": true,
		"bd-3": true,
		"bd-4": true,
		"bd-5": true,
	}

	for _, issue := range issuesAfter {
		if !expectedIDs[issue.ID] {
			t.Errorf("unexpected issue ID after renumbering: %s", issue.ID)
		}
		delete(expectedIDs, issue.ID)
	}

	if len(expectedIDs) > 0 {
		t.Errorf("missing expected IDs after renumbering: %v", expectedIDs)
	}

	// Verify dependency was updated using GetAllDependencyRecords
	finalDeps, err := testStore.GetAllDependencyRecords(ctx)
	if err != nil {
		t.Fatalf("failed to get dependencies: %v", err)
	}

	foundDep := false
	if deps, ok := finalDeps["bd-2"]; ok {
		for _, dep := range deps {
			if dep.DependsOnID == "bd-1" && dep.Type == "blocks" {
				foundDep = true
				break
			}
		}
	}

	if !foundDep {
		t.Errorf("dependency not updated correctly: expected bd-2 -> bd-1")
	}
}

func TestRenumberWithTextReferences(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	testStore, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer testStore.Close()

	ctx := context.Background()

	if err := testStore.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("failed to set prefix: %v", err)
	}

	// Create issues with text references to each other
	issue1 := &types.Issue{
		Title:       "First issue",
		Description: "See bd-2 for details",
		Priority:    1,
		IssueType:   "task",
		Status:      "open",
	}
	if err := testStore.CreateIssue(ctx, issue1, "test"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}
	// Manually set ID to bd-1
	oldID1 := issue1.ID
	if err := testStore.UpdateIssueID(ctx, oldID1, "bd-1", issue1, "test"); err != nil {
		t.Fatalf("failed to set issue ID: %v", err)
	}

	issue2 := &types.Issue{
		Title:       "Second issue",
		Description: "Blocks bd-1",
		Notes:       "Also see bd-1 and bd-100",
		Priority:    1,
		IssueType:   "task",
		Status:      "open",
	}
	if err := testStore.CreateIssue(ctx, issue2, "test"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}
	// Manually set ID to bd-100
	oldID2 := issue2.ID
	if err := testStore.UpdateIssueID(ctx, oldID2, "bd-100", issue2, "test"); err != nil {
		t.Fatalf("failed to set issue ID: %v", err)
	}

	// Renumber: bd-1 -> bd-1, bd-100 -> bd-2
	issues, _ := testStore.SearchIssues(ctx, "", types.IssueFilter{})
	idMapping := map[string]string{
		"bd-1":   "bd-1",
		"bd-100": "bd-2",
	}

	oldStore := store
	store = testStore
	defer func() { store = oldStore }()

	if err := renumberIssuesInDB(ctx, "bd", idMapping, issues); err != nil {
		t.Fatalf("renumberIssuesInDB failed: %v", err)
	}

	// Verify text references were updated
	updated1, err := testStore.GetIssue(ctx, "bd-1")
	if err != nil {
		t.Fatalf("failed to get bd-1: %v", err)
	}
	if updated1.Description != "See bd-2 for details" {
		t.Errorf("bd-1 description not updated: got %q, want %q",
			updated1.Description, "See bd-2 for details")
	}

	updated2, err := testStore.GetIssue(ctx, "bd-2")
	if err != nil {
		t.Fatalf("failed to get bd-2: %v", err)
	}
	if updated2.Description != "Blocks bd-1" {
		t.Errorf("bd-2 description not updated: got %q, want %q",
			updated2.Description, "Blocks bd-1")
	}
	if updated2.Notes != "Also see bd-1 and bd-2" {
		t.Errorf("bd-2 notes not updated: got %q, want %q",
			updated2.Notes, "Also see bd-1 and bd-2")
	}
}

func TestRenumberEmptyDatabase(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	testStore, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer testStore.Close()

	ctx := context.Background()

	if err := testStore.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("failed to set prefix: %v", err)
	}

	oldStore := store
	store = testStore
	defer func() { store = oldStore }()

	// Renumber should succeed with no issues
	issues, _ := testStore.SearchIssues(ctx, "", types.IssueFilter{})
	if err := renumberIssuesInDB(ctx, "bd", map[string]string{}, issues); err != nil {
		t.Fatalf("renumberIssuesInDB failed on empty database: %v", err)
	}
}

// Cleanup any test databases
func TestMain(m *testing.M) {
	code := m.Run()
	os.Exit(code)
}
