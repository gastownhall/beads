package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

func TestRepairDeps_NoOrphans(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, ".beads", "beads.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		t.Fatal(err)
	}

	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()

	// Initialize database
	store.SetConfig(ctx, "issue_prefix", "test-")

	// Create two issues with valid dependency
	i1 := &types.Issue{Title: "Issue 1", Priority: 1, Status: "open", IssueType: "task"}
	store.CreateIssue(ctx, i1, "test")
	i2 := &types.Issue{Title: "Issue 2", Priority: 1, Status: "open", IssueType: "task"}
	store.CreateIssue(ctx, i2, "test")
	store.AddDependency(ctx, &types.Dependency{
		IssueID:     i2.ID,
		DependsOnID: i1.ID,
		Type:        types.DepBlocks,
	}, "test")

	// Get all dependency records
	allDeps, err := store.GetAllDependencyRecords(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Get all issues
	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatal(err)
	}

	// Build valid ID set
	validIDs := make(map[string]bool)
	for _, issue := range issues {
		validIDs[issue.ID] = true
	}

	// Find orphans
	orphanCount := 0
	for issueID, deps := range allDeps {
		if !validIDs[issueID] {
			continue
		}
		for _, dep := range deps {
			if !validIDs[dep.DependsOnID] {
				orphanCount++
			}
		}
	}

	if orphanCount != 0 {
		t.Errorf("Expected 0 orphans, got %d", orphanCount)
	}
}

func TestRepairDeps_FindOrphans(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, ".beads", "beads.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		t.Fatal(err)
	}

	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()

	// Initialize database
	store.SetConfig(ctx, "issue_prefix", "test-")

	// Create two issues
	i1 := &types.Issue{Title: "Issue 1", Priority: 1, Status: "open", IssueType: "task"}
	if err := store.CreateIssue(ctx, i1, "test"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}
	t.Logf("Created i1: %s", i1.ID)

	i2 := &types.Issue{Title: "Issue 2", Priority: 1, Status: "open", IssueType: "task"}
	if err := store.CreateIssue(ctx, i2, "test"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}
	t.Logf("Created i2: %s", i2.ID)

	// Add dependency
	err = store.AddDependency(ctx, &types.Dependency{
		IssueID:     i2.ID,
		DependsOnID: i1.ID,
		Type:        types.DepBlocks,
	}, "test")
	if err != nil {
		t.Fatalf("AddDependency failed: %v", err)
	}

	// Manually create orphaned dependency by directly inserting invalid reference
	// This simulates corruption or import errors
	db := store.UnderlyingDB()
	_, err = db.ExecContext(ctx, "PRAGMA foreign_keys = OFF")
	if err != nil {
		t.Fatal(err)
	}
	// Insert a dependency pointing to a non-existent issue
	_, err = db.ExecContext(ctx, `INSERT INTO dependencies (issue_id, depends_on_id, type, created_at, created_by) 
		VALUES (?, 'nonexistent-123', 'blocks', datetime('now'), 'test')`, i2.ID)
	if err != nil {
		t.Fatalf("Failed to insert orphaned dependency: %v", err)
	}
	_, err = db.ExecContext(ctx, "PRAGMA foreign_keys = ON")
	if err != nil {
		t.Fatal(err)
	}

	// Verify the orphan was actually inserted
	var count int
	err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dependencies WHERE depends_on_id = 'nonexistent-123'").Scan(&count)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("Orphan dependency not inserted, count=%d", count)
	}

	// Get all dependency records
	allDeps, err := store.GetAllDependencyRecords(ctx)
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("Got %d issues with dependencies", len(allDeps))
	for issueID, deps := range allDeps {
		t.Logf("Issue %s has %d dependencies", issueID, len(deps))
		for _, dep := range deps {
			t.Logf("  -> %s (%s)", dep.DependsOnID, dep.Type)
		}
	}

	// Get all issues
	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatal(err)
	}

	// Build valid ID set
	validIDs := make(map[string]bool)
	for _, issue := range issues {
		validIDs[issue.ID] = true
	}
	t.Logf("Valid issue IDs: %v", validIDs)

	// Find orphans
	orphanCount := 0
	for issueID, deps := range allDeps {
		if !validIDs[issueID] {
			t.Logf("Skipping %s - issue itself doesn't exist", issueID)
			continue
		}
		for _, dep := range deps {
			if !validIDs[dep.DependsOnID] {
				t.Logf("Found orphan: %s -> %s", dep.IssueID, dep.DependsOnID)
				orphanCount++
			}
		}
	}

	if orphanCount != 1 {
		t.Errorf("Expected 1 orphan, got %d", orphanCount)
	}
}

func TestRepairDeps_FixOrphans(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, ".beads", "beads.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		t.Fatal(err)
	}

	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()

	// Initialize database
	store.SetConfig(ctx, "issue_prefix", "test-")

	// Create three issues
	i1 := &types.Issue{Title: "Issue 1", Priority: 1, Status: "open", IssueType: "task"}
	store.CreateIssue(ctx, i1, "test")
	i2 := &types.Issue{Title: "Issue 2", Priority: 1, Status: "open", IssueType: "task"}
	store.CreateIssue(ctx, i2, "test")
	i3 := &types.Issue{Title: "Issue 3", Priority: 1, Status: "open", IssueType: "task"}
	store.CreateIssue(ctx, i3, "test")

	// Add dependencies
	store.AddDependency(ctx, &types.Dependency{
		IssueID:     i2.ID,
		DependsOnID: i1.ID,
		Type:        types.DepBlocks,
	}, "test")
	store.AddDependency(ctx, &types.Dependency{
		IssueID:     i3.ID,
		DependsOnID: i1.ID,
		Type:        types.DepBlocks,
	}, "test")

	// Manually create orphaned dependencies by inserting invalid references
	db := store.UnderlyingDB()
	db.Exec("PRAGMA foreign_keys = OFF")
	_, err = db.ExecContext(ctx, `INSERT INTO dependencies (issue_id, depends_on_id, type, created_at, created_by) 
		VALUES (?, 'nonexistent-123', 'blocks', datetime('now'), 'test')`, i2.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO dependencies (issue_id, depends_on_id, type, created_at, created_by) 
		VALUES (?, 'nonexistent-456', 'blocks', datetime('now'), 'test')`, i3.ID)
	if err != nil {
		t.Fatal(err)
	}
	db.Exec("PRAGMA foreign_keys = ON")

	// Find and fix orphans
	allDeps, _ := store.GetAllDependencyRecords(ctx)
	issues, _ := store.SearchIssues(ctx, "", types.IssueFilter{})

	validIDs := make(map[string]bool)
	for _, issue := range issues {
		validIDs[issue.ID] = true
	}

	type orphan struct {
		issueID     string
		dependsOnID string
	}
	var orphans []orphan

	for issueID, deps := range allDeps {
		if !validIDs[issueID] {
			continue
		}
		for _, dep := range deps {
			if !validIDs[dep.DependsOnID] {
				orphans = append(orphans, orphan{
					issueID:     dep.IssueID,
					dependsOnID: dep.DependsOnID,
				})
			}
		}
	}

	if len(orphans) != 2 {
		t.Fatalf("Expected 2 orphans before fix, got %d", len(orphans))
	}

	// Fix orphans using direct SQL (like the command does)
	for _, o := range orphans {
		_, delErr := db.ExecContext(ctx, "DELETE FROM dependencies WHERE issue_id = ? AND depends_on_id = ?",
			o.issueID, o.dependsOnID)
		if delErr != nil {
			t.Errorf("Failed to remove orphan: %v", delErr)
		}
	}

	// Verify orphans removed
	allDeps, _ = store.GetAllDependencyRecords(ctx)
	orphanCount := 0
	for issueID, deps := range allDeps {
		if !validIDs[issueID] {
			continue
		}
		for _, dep := range deps {
			if !validIDs[dep.DependsOnID] {
				orphanCount++
			}
		}
	}

	if orphanCount != 0 {
		t.Errorf("Expected 0 orphans after fix, got %d", orphanCount)
	}
}

func TestRepairDeps_MultipleTypes(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, ".beads", "beads.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		t.Fatal(err)
	}

	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()

	// Initialize database
	store.SetConfig(ctx, "issue_prefix", "test-")

	// Create issues
	i1 := &types.Issue{Title: "Issue 1", Priority: 1, Status: "open", IssueType: "task"}
	store.CreateIssue(ctx, i1, "test")
	i2 := &types.Issue{Title: "Issue 2", Priority: 1, Status: "open", IssueType: "task"}
	store.CreateIssue(ctx, i2, "test")
	i3 := &types.Issue{Title: "Issue 3", Priority: 1, Status: "open", IssueType: "task"}
	store.CreateIssue(ctx, i3, "test")

	// Add different dependency types
	store.AddDependency(ctx, &types.Dependency{
		IssueID:     i2.ID,
		DependsOnID: i1.ID,
		Type:        types.DepBlocks,
	}, "test")
	store.AddDependency(ctx, &types.Dependency{
		IssueID:     i3.ID,
		DependsOnID: i1.ID,
		Type:        types.DepRelated,
	}, "test")

	// Manually create orphaned dependencies with different types
	db := store.UnderlyingDB()
	db.Exec("PRAGMA foreign_keys = OFF")
	_, err = db.ExecContext(ctx, `INSERT INTO dependencies (issue_id, depends_on_id, type, created_at, created_by) 
		VALUES (?, 'nonexistent-blocks', 'blocks', datetime('now'), 'test')`, i2.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO dependencies (issue_id, depends_on_id, type, created_at, created_by) 
		VALUES (?, 'nonexistent-related', 'related', datetime('now'), 'test')`, i3.ID)
	if err != nil {
		t.Fatal(err)
	}
	db.Exec("PRAGMA foreign_keys = ON")

	// Find orphans
	allDeps, _ := store.GetAllDependencyRecords(ctx)
	issues, _ := store.SearchIssues(ctx, "", types.IssueFilter{})

	validIDs := make(map[string]bool)
	for _, issue := range issues {
		validIDs[issue.ID] = true
	}

	orphanCount := 0
	depTypes := make(map[types.DependencyType]int)
	for issueID, deps := range allDeps {
		if !validIDs[issueID] {
			continue
		}
		for _, dep := range deps {
			if !validIDs[dep.DependsOnID] {
				orphanCount++
				depTypes[dep.Type]++
			}
		}
	}

	if orphanCount != 2 {
		t.Errorf("Expected 2 orphans, got %d", orphanCount)
	}
	if depTypes[types.DepBlocks] != 1 {
		t.Errorf("Expected 1 blocks orphan, got %d", depTypes[types.DepBlocks])
	}
	if depTypes[types.DepRelated] != 1 {
		t.Errorf("Expected 1 related orphan, got %d", depTypes[types.DepRelated])
	}
}
