package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage/memory"
	"github.com/steveyegge/beads/internal/types"
)

func TestRepairDeps_NoOrphans(t *testing.T) {
	dir := t.TempDir()
	jsonlPath := filepath.Join(dir, "issues.jsonl")

	store := memory.New(jsonlPath)
	defer store.Close()

	ctx := context.Background()

	// Initialize store
	store.SetConfig(ctx, "issue_prefix", "test")

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
