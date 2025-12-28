package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

func TestValidatePrefix(t *testing.T) {
	tests := []struct {
		name    string
		prefix  string
		wantErr bool
	}{
		{"valid lowercase", "kw-", false},
		{"valid with numbers", "work1-", false},
		{"valid with hyphen", "my-work-", false},
		{"empty", "", true},
		{"long prefix ok", "verylongprefix-", false}, // No length limit (GH#770)
		{"starts with number", "1work-", true},
		{"uppercase", "KW-", true},
		{"no hyphen", "kw", false},
		{"just hyphen", "-", true},
		{"starts with hyphen", "-work", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePrefix(tt.prefix)
			if (err != nil) != tt.wantErr {
				t.Errorf("validatePrefix(%q) error = %v, wantErr %v", tt.prefix, err, tt.wantErr)
			}
		})
	}
}

func TestRenamePrefixCommand(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	testStore, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer testStore.Close()

	ctx := context.Background()

	store = testStore
	actor = "test"
	defer func() {
		store = nil
		actor = ""
	}()

	if err := testStore.SetConfig(ctx, "issue_prefix", "old"); err != nil {
		t.Fatalf("Failed to set config: %v", err)
	}

	issue1 := &types.Issue{
		ID:          "old-1",
		Title:       "Fix bug in old-2",
		Description: "See old-3 for details",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeBug,
	}
	issue2 := &types.Issue{
		ID:          "old-2",
		Title:       "Related to old-1",
		Description: "This depends on old-1",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
	}
	issue3 := &types.Issue{
		ID:          "old-3",
		Title:       "Another issue",
		Description: "Referenced by old-1",
		Design:      "Mentions old-2 in design",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeFeature,
	}

	if err := testStore.CreateIssue(ctx, issue1, "test"); err != nil {
		t.Fatalf("Failed to create issue1: %v", err)
	}
	if err := testStore.CreateIssue(ctx, issue2, "test"); err != nil {
		t.Fatalf("Failed to create issue2: %v", err)
	}
	if err := testStore.CreateIssue(ctx, issue3, "test"); err != nil {
		t.Fatalf("Failed to create issue3: %v", err)
	}

	dep := &types.Dependency{
		IssueID:     "old-1",
		DependsOnID: "old-2",
		Type:        types.DepBlocks,
	}
	if err := testStore.AddDependency(ctx, dep, "test"); err != nil {
		t.Fatalf("Failed to add dependency: %v", err)
	}

	issues := []*types.Issue{issue1, issue2, issue3}
	if err := renamePrefixInDB(ctx, "old", "new", issues); err != nil {
		t.Fatalf("renamePrefixInDB failed: %v", err)
	}

	newPrefix, err := testStore.GetConfig(ctx, "issue_prefix")
	if err != nil {
		t.Fatalf("Failed to get new prefix: %v", err)
	}
	if newPrefix != "new" {
		t.Errorf("Expected prefix 'new', got %q", newPrefix)
	}

	updatedIssue1, err := testStore.GetIssue(ctx, "new-1")
	if err != nil {
		t.Fatalf("Failed to get updated issue1: %v", err)
	}
	if updatedIssue1.Title != "Fix bug in new-2" {
		t.Errorf("Expected title 'Fix bug in new-2', got %q", updatedIssue1.Title)
	}
	if updatedIssue1.Description != "See new-3 for details" {
		t.Errorf("Expected description 'See new-3 for details', got %q", updatedIssue1.Description)
	}

	updatedIssue2, err := testStore.GetIssue(ctx, "new-2")
	if err != nil {
		t.Fatalf("Failed to get updated issue2: %v", err)
	}
	if updatedIssue2.Title != "Related to new-1" {
		t.Errorf("Expected title 'Related to new-1', got %q", updatedIssue2.Title)
	}
	if updatedIssue2.Description != "This depends on new-1" {
		t.Errorf("Expected description 'This depends on new-1', got %q", updatedIssue2.Description)
	}

	updatedIssue3, err := testStore.GetIssue(ctx, "new-3")
	if err != nil {
		t.Fatalf("Failed to get updated issue3: %v", err)
	}
	if updatedIssue3.Design != "Mentions new-2 in design" {
		t.Errorf("Expected design 'Mentions new-2 in design', got %q", updatedIssue3.Design)
	}

	deps, err := testStore.GetDependencies(ctx, "new-1")
	if err != nil {
		t.Fatalf("Failed to get dependencies: %v", err)
	}
	if len(deps) != 1 {
		t.Fatalf("Expected 1 dependency, got %d", len(deps))
	}
	if deps[0].ID != "new-2" {
		t.Errorf("Expected dependency ID 'new-2', got %q", deps[0].ID)
	}

	oldIssue, err := testStore.GetIssue(ctx, "old-1")
	if err == nil && oldIssue != nil {
		t.Errorf("Expected old-1 to not exist, but got: %+v", oldIssue)
	}
}

func TestRenamePrefixInDB(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	testStore, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	t.Cleanup(func() {
		testStore.Close()
		os.Remove(dbPath)
	})

	ctx := context.Background()
	store = testStore
	actor = "test-actor"

	if err := testStore.SetConfig(ctx, "issue_prefix", "old"); err != nil {
		t.Fatalf("Failed to set config: %v", err)
	}

	issue1 := &types.Issue{
		ID:          "old-1",
		Title:       "Test issue",
		Description: "Description",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
	}

	if err := testStore.CreateIssue(ctx, issue1, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	issues := []*types.Issue{issue1}
	err = renamePrefixInDB(ctx, "old", "new", issues)
	if err != nil {
		t.Fatalf("renamePrefixInDB failed: %v", err)
	}

	oldIssue, err := testStore.GetIssue(ctx, "old-1")
	if err == nil && oldIssue != nil {
		t.Errorf("Expected old-1 to not exist after rename, got: %+v", oldIssue)
	}

	newIssue, err := testStore.GetIssue(ctx, "new-1")
	if err != nil {
		t.Fatalf("Failed to get new-1: %v", err)
	}
	if newIssue.ID != "new-1" {
		t.Errorf("Expected ID 'new-1', got %q", newIssue.ID)
	}
}
