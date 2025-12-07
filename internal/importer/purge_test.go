package importer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/deletions"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

// TestPurgeDeletedIssues tests that issues in the deletions manifest are converted to tombstones during import
func TestPurgeDeletedIssues(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Create database
	dbPath := filepath.Join(tmpDir, "beads.db")
	store, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to create database: %v", err)
	}
	defer store.Close()

	// Initialize prefix
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("failed to set prefix: %v", err)
	}

	// Create some issues in the database
	issue1 := &types.Issue{
		ID:        "test-abc",
		Title:     "Issue 1",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	issue2 := &types.Issue{
		ID:        "test-def",
		Title:     "Issue 2",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	issue3 := &types.Issue{
		ID:        "test-ghi",
		Title:     "Issue 3 (local work)",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}

	for _, iss := range []*types.Issue{issue1, issue2, issue3} {
		if err := store.CreateIssue(ctx, iss, "test"); err != nil {
			t.Fatalf("failed to create issue %s: %v", iss.ID, err)
		}
	}

	// Create a deletions manifest with issue2 deleted
	deletionsPath := deletions.DefaultPath(tmpDir)
	delRecord := deletions.DeletionRecord{
		ID:        "test-def",
		Timestamp: time.Now().UTC(),
		Actor:     "test-user",
		Reason:    "test deletion",
	}
	if err := deletions.AppendDeletion(deletionsPath, delRecord); err != nil {
		t.Fatalf("failed to create deletions manifest: %v", err)
	}

	// Simulate import with only issue1 in the JSONL (issue2 was deleted, issue3 is local work)
	jsonlIssues := []*types.Issue{issue1}

	result := &Result{
		IDMapping:        make(map[string]string),
		MismatchPrefixes: make(map[string]int),
	}

	// Call purgeDeletedIssues
	if err := purgeDeletedIssues(ctx, store, dbPath, jsonlIssues, Options{}, result); err != nil {
		t.Fatalf("purgeDeletedIssues failed: %v", err)
	}

	// Verify issue2 was tombstoned (bd-dve: now converts to tombstone instead of hard-delete)
	if result.Purged != 1 {
		t.Errorf("expected 1 purged issue, got %d", result.Purged)
	}
	if len(result.PurgedIDs) != 1 || result.PurgedIDs[0] != "test-def" {
		t.Errorf("expected PurgedIDs to contain 'test-def', got %v", result.PurgedIDs)
	}

	// Verify issue2 is now a tombstone (not hard-deleted)
	// GetIssue returns nil for tombstones by default, so use IncludeTombstones filter
	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{IncludeTombstones: true})
	if err != nil {
		t.Fatalf("SearchIssues failed: %v", err)
	}
	var iss2 *types.Issue
	for _, iss := range issues {
		if iss.ID == "test-def" {
			iss2 = iss
			break
		}
	}
	if iss2 == nil {
		t.Errorf("expected issue2 to exist as tombstone, but it was hard-deleted")
	} else if iss2.Status != types.StatusTombstone {
		t.Errorf("expected issue2 to be a tombstone, got status %q", iss2.Status)
	}

	// Verify issue1 still exists (in JSONL)
	iss1, err := store.GetIssue(ctx, "test-abc")
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}
	if iss1 == nil {
		t.Errorf("expected issue1 to still exist")
	}

	// Verify issue3 still exists (local work, not in deletions manifest)
	iss3, err := store.GetIssue(ctx, "test-ghi")
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}
	if iss3 == nil {
		t.Errorf("expected issue3 (local work) to still exist")
	}
}

// TestPurgeDeletedIssues_NoDeletionsManifest tests that import works without a deletions manifest
func TestPurgeDeletedIssues_NoDeletionsManifest(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Create database
	dbPath := filepath.Join(tmpDir, "beads.db")
	store, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to create database: %v", err)
	}
	defer store.Close()

	// Initialize prefix
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("failed to set prefix: %v", err)
	}

	// Create an issue in the database
	issue := &types.Issue{
		ID:        "test-abc",
		Title:     "Issue 1",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	// No deletions manifest exists
	jsonlIssues := []*types.Issue{issue}

	result := &Result{
		IDMapping:        make(map[string]string),
		MismatchPrefixes: make(map[string]int),
	}

	// Call purgeDeletedIssues - should succeed with no errors
	if err := purgeDeletedIssues(ctx, store, dbPath, jsonlIssues, Options{}, result); err != nil {
		t.Fatalf("purgeDeletedIssues failed: %v", err)
	}

	// Verify nothing was purged
	if result.Purged != 0 {
		t.Errorf("expected 0 purged issues, got %d", result.Purged)
	}

	// Verify issue still exists
	iss, err := store.GetIssue(ctx, "test-abc")
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}
	if iss == nil {
		t.Errorf("expected issue to still exist")
	}
}

// TestPurgeDeletedIssues_EmptyDeletionsManifest tests that import works with empty deletions manifest
func TestPurgeDeletedIssues_EmptyDeletionsManifest(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Create database
	dbPath := filepath.Join(tmpDir, "beads.db")
	store, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to create database: %v", err)
	}
	defer store.Close()

	// Initialize prefix
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("failed to set prefix: %v", err)
	}

	// Create an issue in the database
	issue := &types.Issue{
		ID:        "test-abc",
		Title:     "Issue 1",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	// Create empty deletions manifest
	deletionsPath := deletions.DefaultPath(tmpDir)
	if err := os.WriteFile(deletionsPath, []byte{}, 0644); err != nil {
		t.Fatalf("failed to create empty deletions manifest: %v", err)
	}

	jsonlIssues := []*types.Issue{issue}

	result := &Result{
		IDMapping:        make(map[string]string),
		MismatchPrefixes: make(map[string]int),
	}

	// Call purgeDeletedIssues - should succeed with no errors
	if err := purgeDeletedIssues(ctx, store, dbPath, jsonlIssues, Options{}, result); err != nil {
		t.Fatalf("purgeDeletedIssues failed: %v", err)
	}

	// Verify nothing was purged
	if result.Purged != 0 {
		t.Errorf("expected 0 purged issues, got %d", result.Purged)
	}
}
