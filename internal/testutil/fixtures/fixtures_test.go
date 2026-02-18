//go:build cgo && integration
// +build cgo,integration

package fixtures

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
)

func TestLargeDolt(t *testing.T) {
	store, err := dolt.New(context.Background(), &dolt.Config{Path: t.TempDir()})
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Initialize database with prefix
	if err := store.SetConfig(ctx, "issue_prefix", "bd-"); err != nil {
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}

	if err := LargeDolt(ctx, store); err != nil {
		t.Fatalf("LargeDolt failed: %v", err)
	}

	// Verify issue count
	allIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("Failed to search issues: %v", err)
	}

	if len(allIssues) != 10000 {
		t.Errorf("Expected 10000 issues, got %d", len(allIssues))
	}

	// Verify we have epics, features, and tasks
	var epics, features, tasks int
	for _, issue := range allIssues {
		switch issue.IssueType {
		case types.TypeEpic:
			epics++
		case types.TypeFeature:
			features++
		case types.TypeTask:
			tasks++
		}
	}

	if epics == 0 || features == 0 || tasks == 0 {
		t.Errorf("Missing issue types: epics=%d, features=%d, tasks=%d", epics, features, tasks)
	}

	t.Logf("Created %d epics, %d features, %d tasks", epics, features, tasks)
}

func TestXLargeDolt(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping XLarge test in short mode")
	}

	store, err := dolt.New(context.Background(), &dolt.Config{Path: t.TempDir()})
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Initialize database with prefix
	if err := store.SetConfig(ctx, "issue_prefix", "bd-"); err != nil {
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}

	if err := XLargeDolt(ctx, store); err != nil {
		t.Fatalf("XLargeDolt failed: %v", err)
	}

	// Verify issue count
	allIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("Failed to search issues: %v", err)
	}

	if len(allIssues) != 20000 {
		t.Errorf("Expected 20000 issues, got %d", len(allIssues))
	}
}

func TestLargeFromJSONL(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping JSONL test in short mode")
	}

	store, err := dolt.New(context.Background(), &dolt.Config{Path: t.TempDir()})
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Initialize database with prefix
	if err := store.SetConfig(ctx, "issue_prefix", "bd-"); err != nil {
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}

	tempDir := t.TempDir()

	if err := LargeFromJSONL(ctx, store, tempDir); err != nil {
		t.Fatalf("LargeFromJSONL failed: %v", err)
	}

	// Verify issue count
	allIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("Failed to search issues: %v", err)
	}

	if len(allIssues) != 10000 {
		t.Errorf("Expected 10000 issues, got %d", len(allIssues))
	}
}
