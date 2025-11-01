package importer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

// TestImportTimestampPrecedence verifies that imports respect updated_at timestamps (bd-e55c)
// When importing an issue with the same ID but different content, the newer version should win.
func TestImportTimestampPrecedence(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	
	// Initialize storage
	store, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer store.Close()
	
	ctx := context.Background()
	
	// Set up database with prefix
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}
	
	// Create an issue locally at time T1
	now := time.Now()
	closedAt := now
	localIssue := &types.Issue{
		ID:          "bd-test123",
		Title:       "Test Issue",
		Description: "Local version",
		Status:      types.StatusClosed,
		Priority:    1,
		IssueType:   types.TypeBug,
		CreatedAt:   now.Add(-2 * time.Hour),
		UpdatedAt:   now, // Newer timestamp
		ClosedAt:    &closedAt,
	}
	localIssue.ContentHash = localIssue.ComputeContentHash()
	
	if err := store.CreateIssue(ctx, localIssue, "test"); err != nil {
		t.Fatalf("Failed to create local issue: %v", err)
	}
	
	// Simulate importing an older version from remote (e.g., from git pull)
	// This represents the scenario in bd-e55c where remote has status=open from yesterday
	olderRemoteIssue := &types.Issue{
		ID:          "bd-test123", // Same ID
		Title:       "Test Issue",
		Description: "Remote version",
		Status:      types.StatusOpen, // Different status
		Priority:    1,
		IssueType:   types.TypeBug,
		CreatedAt:   now.Add(-2 * time.Hour),
		UpdatedAt:   now.Add(-1 * time.Hour), // Older timestamp
	}
	olderRemoteIssue.ContentHash = olderRemoteIssue.ComputeContentHash()
	
	// Import the older remote version
	result, err := ImportIssues(ctx, dbPath, store, []*types.Issue{olderRemoteIssue}, Options{
		SkipPrefixValidation: true,
	})
	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}
	
	// Verify that the import did NOT update the local version
	// The local version is newer, so it should be preserved
	if result.Updated > 0 {
		t.Errorf("Expected 0 updates, got %d - older remote should not overwrite newer local", result.Updated)
	}
	if result.Unchanged == 0 {
		t.Errorf("Expected unchanged count > 0, got %d", result.Unchanged)
	}
	
	// Verify the database still has the local (newer) version
	dbIssue, err := store.GetIssue(ctx, "bd-test123")
	if err != nil {
		t.Fatalf("Failed to get issue: %v", err)
	}
	
	if dbIssue.Status != types.StatusClosed {
		t.Errorf("Expected status=closed (local version), got status=%s", dbIssue.Status)
	}
	if dbIssue.Description != "Local version" {
		t.Errorf("Expected description='Local version', got '%s'", dbIssue.Description)
	}
	
	// Now test the reverse: importing a NEWER version should update
	newerRemoteIssue := &types.Issue{
		ID:          "bd-test123",
		Title:       "Test Issue",
		Description: "Even newer remote version",
		Status:      types.StatusOpen,
		Priority:    2, // Changed priority too
		IssueType:   types.TypeBug,
		CreatedAt:   now.Add(-2 * time.Hour),
		UpdatedAt:   now.Add(1 * time.Hour), // Newer than current DB
	}
	newerRemoteIssue.ContentHash = newerRemoteIssue.ComputeContentHash()
	
	result2, err := ImportIssues(ctx, dbPath, store, []*types.Issue{newerRemoteIssue}, Options{
		SkipPrefixValidation: true,
	})
	if err != nil {
		t.Fatalf("Import of newer version failed: %v", err)
	}
	
	if result2.Updated == 0 {
		t.Errorf("Expected 1 update, got 0 - newer remote should overwrite older local")
	}
	
	// Verify the database now has the newer remote version
	dbIssue2, err := store.GetIssue(ctx, "bd-test123")
	if err != nil {
		t.Fatalf("Failed to get issue after second import: %v", err)
	}
	
	if dbIssue2.Priority != 2 {
		t.Errorf("Expected priority=2 (newer remote), got %d", dbIssue2.Priority)
	}
	if dbIssue2.Description != "Even newer remote version" {
		t.Errorf("Expected description='Even newer remote version', got '%s'", dbIssue2.Description)
	}
}

// TestImportSameTimestamp tests behavior when timestamps are equal
func TestImportSameTimestamp(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	
	store, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer store.Close()
	
	ctx := context.Background()
	
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}
	
	now := time.Now()
	
	// Create local issue
	localIssue := &types.Issue{
		ID:          "bd-test456",
		Title:       "Test Issue",
		Description: "Local version",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	localIssue.ContentHash = localIssue.ComputeContentHash()
	
	if err := store.CreateIssue(ctx, localIssue, "test"); err != nil {
		t.Fatalf("Failed to create local issue: %v", err)
	}
	
	// Import with SAME timestamp but different content
	remoteIssue := &types.Issue{
		ID:          "bd-test456",
		Title:       "Test Issue",
		Description: "Remote version",
		Status:      types.StatusInProgress,
		Priority:    1,
		IssueType:   types.TypeTask,
		CreatedAt:   now,
		UpdatedAt:   now, // Same timestamp
	}
	remoteIssue.ContentHash = remoteIssue.ComputeContentHash()
	
	result, err := ImportIssues(ctx, dbPath, store, []*types.Issue{remoteIssue}, Options{
		SkipPrefixValidation: true,
	})
	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}
	
	// With equal timestamps, we should NOT update (local wins)
	if result.Updated > 0 {
		t.Errorf("Expected 0 updates with equal timestamps, got %d", result.Updated)
	}
	
	// Verify local version is preserved
	dbIssue, err := store.GetIssue(ctx, "bd-test456")
	if err != nil {
		t.Fatalf("Failed to get issue: %v", err)
	}
	
	if dbIssue.Description != "Local version" {
		t.Errorf("Expected local version to be preserved, got '%s'", dbIssue.Description)
	}
}

func TestMain(m *testing.M) {
	// Ensure test DB files are cleaned up
	code := m.Run()
	os.Exit(code)
}
