package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

func TestRemapDependencies(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	testStore, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer testStore.Close()

	ctx := context.Background()

	// Initialize database with prefix
	if err := testStore.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}

	// Create some issues
	issueA := &types.Issue{
		ID:        "test-aaa",
		Title:     "Issue A (to be moved)",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	issueB := &types.Issue{
		ID:        "test-bbb",
		Title:     "Issue B (depends on A)",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	issueC := &types.Issue{
		ID:        "test-ccc",
		Title:     "Issue C (A depends on it)",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}

	for _, issue := range []*types.Issue{issueA, issueB, issueC} {
		if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue %s: %v", issue.ID, err)
		}
	}

	// B depends on A (B is blocked by A)
	dep1 := &types.Dependency{
		IssueID:     issueB.ID,
		DependsOnID: issueA.ID,
		Type:        types.DepBlocks,
	}
	if err := testStore.AddDependency(ctx, dep1, "test"); err != nil {
		t.Fatalf("Failed to add dep B->A: %v", err)
	}

	// A depends on C (A is blocked by C)
	dep2 := &types.Dependency{
		IssueID:     issueA.ID,
		DependsOnID: issueC.ID,
		Type:        types.DepBlocks,
	}
	if err := testStore.AddDependency(ctx, dep2, "test"); err != nil {
		t.Fatalf("Failed to add dep A->C: %v", err)
	}

	// Now remap: A moves from test-aaa to other-xxx (CROSS-rig, different prefix)
	// This simulates moving from one rig to another
	newID := "other-xxx"
	count, err := remapDependencies(ctx, testStore, issueA.ID, newID, "other", "test-actor")
	if err != nil {
		t.Fatalf("remapDependencies failed: %v", err)
	}

	// For cross-rig moves:
	// - B->A becomes B->external:other:other-xxx (1 remapped)
	// - A->C is removed (cross-rig, can't recreate in source store)
	if count != 1 {
		t.Errorf("Expected 1 dependency remapped (B->external ref), got %d", count)
	}

	// Verify B now depends on external ref (not test-aaa)
	bDepRecords, err := testStore.GetDependencyRecords(ctx, issueB.ID)
	if err != nil {
		t.Fatalf("GetDependencyRecords(B) failed: %v", err)
	}
	expectedExtRef := "external:other:other-xxx"
	foundNewDep := false
	for _, dep := range bDepRecords {
		if dep.DependsOnID == expectedExtRef {
			foundNewDep = true
		}
		if dep.DependsOnID == issueA.ID {
			t.Errorf("B still depends on old ID %s", issueA.ID)
		}
	}
	if !foundNewDep {
		t.Errorf("B should depend on external ref %s, but doesn't. Has: %v", expectedExtRef, bDepRecords)
	}

	// Verify old A->C dependency was removed
	aDeps, err := testStore.GetDependencyRecords(ctx, issueA.ID)
	if err != nil {
		t.Fatalf("GetDependencyRecords(A) failed: %v", err)
	}
	if len(aDeps) != 0 {
		t.Errorf("Expected old issue A to have 0 dependencies after remap, got %d", len(aDeps))
	}
}

func TestRemapDependencies_NoDeps(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	testStore, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer testStore.Close()

	ctx := context.Background()

	// Initialize database with prefix
	if err := testStore.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}

	// Create an issue with no dependencies
	issue := &types.Issue{
		ID:        "test-lonely",
		Title:     "Lonely issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	count, err := remapDependencies(ctx, testStore, issue.ID, "other-id", "other", "test-actor")
	if err != nil {
		t.Fatalf("remapDependencies failed: %v", err)
	}
	if count != 0 {
		t.Errorf("Expected 0 dependencies remapped for issue with no deps, got %d", count)
	}
}

func TestRemapDependencies_PreservesMetadata(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	testStore, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer testStore.Close()

	ctx := context.Background()

	// Initialize database with prefix
	if err := testStore.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}

	// Create issues
	issueA := &types.Issue{
		ID:        "test-aaa",
		Title:     "Issue A",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	issueB := &types.Issue{
		ID:        "test-bbb",
		Title:     "Issue B",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}

	for _, issue := range []*types.Issue{issueA, issueB} {
		if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue %s: %v", issue.ID, err)
		}
	}

	// Create dependency with metadata (B depends on A)
	dep := &types.Dependency{
		IssueID:     issueB.ID,
		DependsOnID: issueA.ID,
		Type:        types.DepDiscoveredFrom,
		Metadata:    `{"reason": "found during work"}`,
	}
	if err := testStore.AddDependency(ctx, dep, "test"); err != nil {
		t.Fatalf("Failed to add dep: %v", err)
	}

	// Remap: A moves to other-xxx (cross-rig)
	newID := "other-xxx"
	_, err = remapDependencies(ctx, testStore, issueA.ID, newID, "other", "test-actor")
	if err != nil {
		t.Fatalf("remapDependencies failed: %v", err)
	}

	// Verify metadata was preserved on the new external ref dependency
	bDepRecords, err := testStore.GetDependencyRecords(ctx, issueB.ID)
	if err != nil {
		t.Fatalf("GetDependencyRecords(B) failed: %v", err)
	}
	if len(bDepRecords) != 1 {
		t.Fatalf("Expected 1 dependency, got %d", len(bDepRecords))
	}
	expectedExtRef := "external:other:other-xxx"
	if bDepRecords[0].DependsOnID != expectedExtRef {
		t.Errorf("Expected depends_on_id=%s, got %s", expectedExtRef, bDepRecords[0].DependsOnID)
	}
	if bDepRecords[0].Type != types.DepDiscoveredFrom {
		t.Errorf("Expected type=discovered-from, got %s", bDepRecords[0].Type)
	}
	if bDepRecords[0].Metadata != `{"reason": "found during work"}` {
		t.Errorf("Metadata not preserved: got %s", bDepRecords[0].Metadata)
	}
}
