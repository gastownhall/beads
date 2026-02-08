package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

func TestMigrateHashIDs(t *testing.T) {
	// Create temporary directory for test database
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	// Create test database with sequential IDs
	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("Failed to create database: %v", err)
	}

	ctx := context.Background()

	// Set ID prefix config
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	// Create test issues with sequential IDs
	issue1 := &types.Issue{
		ID:          "bd-1",
		Title:       "First issue",
		Description: "This is issue bd-1",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue1, "test"); err != nil {
		t.Fatalf("Failed to create issue 1: %v", err)
	}

	issue2 := &types.Issue{
		ID:          "bd-2",
		Title:       "Second issue",
		Description: "This is issue bd-2 which references bd-1",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue2, "test"); err != nil {
		t.Fatalf("Failed to create issue 2: %v", err)
	}

	// Create a dependency
	dep := &types.Dependency{
		IssueID:     "bd-2",
		DependsOnID: "bd-1",
		Type:        types.DepBlocks,
	}
	if err := store.AddDependency(ctx, dep, "test"); err != nil {
		t.Fatalf("Failed to add dependency: %v", err)
	}

	// Close store before migration
	store.Close()

	// Test dry run
	store, err = sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("Failed to reopen database: %v", err)
	}

	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("Failed to get issues: %v", err)
	}

	mapping, err := migrateToHashIDs(ctx, store, issues, true)
	if err != nil {
		t.Fatalf("Dry run failed: %v", err)
	}

	if len(mapping) != 2 {
		t.Errorf("Expected 2 issues in mapping, got %d", len(mapping))
	}

	// Check mapping contains both IDs
	if _, ok := mapping["bd-1"]; !ok {
		t.Error("Mapping missing bd-1")
	}
	if _, ok := mapping["bd-2"]; !ok {
		t.Error("Mapping missing bd-2")
	}

	// Verify new IDs are hash-based
	for old, new := range mapping {
		if !isHashID(new) {
			t.Errorf("New ID %s for %s is not a hash ID", new, old)
		}
	}

	store.Close()

	// Test actual migration
	store, err = sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("Failed to reopen database: %v", err)
	}
	defer store.Close()

	issues, err = store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("Failed to get issues: %v", err)
	}

	mapping, err = migrateToHashIDs(ctx, store, issues, false)
	if err != nil {
		t.Fatalf("Migration failed: %v", err)
	}

	// Verify migration
	newID1 := mapping["bd-1"]
	newID2 := mapping["bd-2"]

	// Get migrated issues
	migratedIssue1, err := store.GetIssue(ctx, newID1)
	if err != nil {
		t.Fatalf("Failed to get migrated issue 1: %v", err)
	}

	migratedIssue2, err := store.GetIssue(ctx, newID2)
	if err != nil {
		t.Fatalf("Failed to get migrated issue 2: %v", err)
	}

	// Verify content is preserved
	if migratedIssue1.Title != "First issue" {
		t.Errorf("Issue 1 title changed: %s", migratedIssue1.Title)
	}
	if migratedIssue2.Title != "Second issue" {
		t.Errorf("Issue 2 title changed: %s", migratedIssue2.Title)
	}

	// Verify text reference was updated
	if migratedIssue2.Description != "This is issue "+newID2+" which references "+newID1 {
		t.Errorf("Text references not updated: %s", migratedIssue2.Description)
	}

	// Verify dependency was updated
	deps, err := store.GetDependencyRecords(ctx, newID2)
	if err != nil {
		t.Fatalf("Failed to get dependencies: %v", err)
	}

	if len(deps) != 1 {
		t.Fatalf("Expected 1 dependency, got %d", len(deps))
	}

	if deps[0].IssueID != newID2 {
		t.Errorf("Dependency issue_id not updated: %s", deps[0].IssueID)
	}
	if deps[0].DependsOnID != newID1 {
		t.Errorf("Dependency depends_on_id not updated: %s", deps[0].DependsOnID)
	}
}

func TestMigrateHashIDsWithParentChild(t *testing.T) {
	// Create temporary directory for test database
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	// Create test database
	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("Failed to create database: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Set ID prefix config
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	// Create epic (parent)
	epic := &types.Issue{
		ID:          "bd-1",
		Title:       "Epic issue",
		Description: "This is an epic",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeEpic,
	}
	if err := store.CreateIssue(ctx, epic, "test"); err != nil {
		t.Fatalf("Failed to create epic: %v", err)
	}

	// Create child issue
	child := &types.Issue{
		ID:          "bd-2",
		Title:       "Child issue",
		Description: "This is a child of bd-1",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
	}
	if err := store.CreateIssue(ctx, child, "test"); err != nil {
		t.Fatalf("Failed to create child: %v", err)
	}

	// Create parent-child dependency
	dep := &types.Dependency{
		IssueID:     "bd-2",
		DependsOnID: "bd-1",
		Type:        types.DepParentChild,
	}
	if err := store.AddDependency(ctx, dep, "test"); err != nil {
		t.Fatalf("Failed to add dependency: %v", err)
	}

	// Migrate
	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("Failed to get issues: %v", err)
	}

	mapping, err := migrateToHashIDs(ctx, store, issues, false)
	if err != nil {
		t.Fatalf("Migration failed: %v", err)
	}

	// Verify parent got hash ID
	newEpicID := mapping["bd-1"]
	if !isHashID(newEpicID) {
		t.Errorf("Epic ID is not a hash ID: %s", newEpicID)
	}

	// Verify child got hierarchical ID (parent.1)
	newChildID := mapping["bd-2"]
	expectedChildID := newEpicID + ".1"
	if newChildID != expectedChildID {
		t.Errorf("Child ID should be %s, got %s", expectedChildID, newChildID)
	}
}

func TestIsHashID(t *testing.T) {
	tests := []struct {
		id       string
		expected bool
	}{
		// Sequential IDs (numeric only, short)
		{"bd-1", false},
		{"bd-123", false},
		{"bd-9999", false},

		// Hash IDs with letters
		{"bd-a3f8e9a2", true},
		{"bd-abc123", true},
		{"bd-123abc", true},
		{"bd-a3f8e9a2.1", true},
		{"bd-a3f8e9a2.1.2", true},
		// Hash IDs that are numeric but 5+ characters (likely hash)
		{"bd-12345", true},
		{"bd-0088", false}, // 4 chars, all numeric - ambiguous, defaults to false
		{"bd-00880", true}, // 5+ chars, likely hash

		// Base36 hash IDs with letters
		{"bd-5n3", true},
		{"bd-65w", true},
		{"bd-jmx", true},
		{"bd-4rt", true},

		// Edge cases
		{"bd-", false},     // Empty suffix
		{"invalid", false}, // No dash
		{"bd-0", false},    // Single digit

		// Hyphenated prefixes
		{"bd-beads-1", false},
		{"bd-beads-123", false},
		{"bd-beads-a3f8e9a2", true},
		{"bd-beads-abc123", true},
		{"bd-beads-123abc", true},
		{"bd-beads-a3f8e9a2.1", true},
		{"bd-beads-a3f8e9a2.1.2", true},
	}

	for _, tt := range tests {
		result := isHashID(tt.id)
		if result != tt.expected {
			t.Errorf("isHashID(%s) = %v, want %v", tt.id, result, tt.expected)
		}
	}
}

func TestCopyFile(t *testing.T) {
	tmpDir := t.TempDir()
	src := filepath.Join(tmpDir, "source.txt")
	dst := filepath.Join(tmpDir, "dest.txt")

	// Write test file
	content := []byte("test content")
	if err := os.WriteFile(src, content, 0644); err != nil {
		t.Fatalf("Failed to write source file: %v", err)
	}

	// Copy file
	if err := copyFile(src, dst); err != nil {
		t.Fatalf("copyFile failed: %v", err)
	}

	// Verify copy
	copied, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("Failed to read destination file: %v", err)
	}

	if string(copied) != string(content) {
		t.Errorf("Content mismatch: got %s, want %s", copied, content)
	}
}
