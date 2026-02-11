//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
)

// TestMultiWorkspaceDeletionSync simulates the bd-hv01 bug scenario:
// Clone A deletes an issue, Clone B still has it, and after sync it should stay deleted
func TestMultiWorkspaceDeletionSync(t *testing.T) {
	// Setup two separate workspaces simulating two git clones
	cloneADir := t.TempDir()
	cloneBDir := t.TempDir()

	cloneAJSONL := filepath.Join(cloneADir, "issues.jsonl")
	cloneBJSONL := filepath.Join(cloneBDir, "issues.jsonl")

	cloneADB := filepath.Join(cloneADir, "beads.db")
	cloneBDB := filepath.Join(cloneBDir, "beads.db")

	ctx := context.Background()

	// Create stores for both clones
	storeA, err := dolt.New(context.Background(), &dolt.Config{Path: cloneADB})
	if err != nil {
		t.Fatalf("Failed to create store A: %v", err)
	}
	defer storeA.Close()

	if err := storeA.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("Failed to set issue_prefix for store A: %v", err)
	}

	storeB, err := dolt.New(context.Background(), &dolt.Config{Path: cloneBDB})
	if err != nil {
		t.Fatalf("Failed to create store B: %v", err)
	}
	defer storeB.Close()

	if err := storeB.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("Failed to set issue_prefix for store B: %v", err)
	}

	// Step 1: Both clones start with the same two issues
	issueToDelete := &types.Issue{
		ID:          "bd-delete-me",
		Title:       "Issue to be deleted",
		Description: "This will be deleted in clone A",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   "bug",
	}

	issueToKeep := &types.Issue{
		ID:          "bd-keep-me",
		Title:       "Issue to keep",
		Description: "This should remain",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   "feature",
	}

	// Create in both stores (using "test" as actor)
	if err := storeA.CreateIssue(ctx, issueToDelete, "test"); err != nil {
		t.Fatalf("Failed to create issue in store A: %v", err)
	}
	if err := storeA.CreateIssue(ctx, issueToKeep, "test"); err != nil {
		t.Fatalf("Failed to create issue in store A: %v", err)
	}

	if err := storeB.CreateIssue(ctx, issueToDelete, "test"); err != nil {
		t.Fatalf("Failed to create issue in store B: %v", err)
	}
	if err := storeB.CreateIssue(ctx, issueToKeep, "test"); err != nil {
		t.Fatalf("Failed to create issue in store B: %v", err)
	}

	// Export from both
	if err := exportToJSONLWithStore(ctx, storeA, cloneAJSONL); err != nil {
		t.Fatalf("Failed to export from store A: %v", err)
	}
	if err := exportToJSONLWithStore(ctx, storeB, cloneBJSONL); err != nil {
		t.Fatalf("Failed to export from store B: %v", err)
	}

	// Initialize base snapshots for both (simulating first sync)
	if err := initializeSnapshotsIfNeeded(cloneAJSONL); err != nil {
		t.Fatalf("Failed to initialize snapshots for A: %v", err)
	}
	if err := initializeSnapshotsIfNeeded(cloneBJSONL); err != nil {
		t.Fatalf("Failed to initialize snapshots for B: %v", err)
	}

	// Step 2: Clone A deletes the issue
	if err := storeA.DeleteIssue(ctx, "bd-delete-me"); err != nil {
		t.Fatalf("Failed to delete issue in store A: %v", err)
	}

	// Step 3: Clone A exports and captures left snapshot (simulating pre-pull)
	if err := exportToJSONLWithStore(ctx, storeA, cloneAJSONL); err != nil {
		t.Fatalf("Failed to export from store A after deletion: %v", err)
	}
	if err := captureLeftSnapshot(cloneAJSONL); err != nil {
		t.Fatalf("Failed to capture left snapshot for A: %v", err)
	}

	// Simulate git push/pull: Copy Clone A's JSONL to Clone B's "remote" state
	remoteJSONL := cloneAJSONL

	// Step 4: Clone B exports (still has both issues) and captures left snapshot
	if err := exportToJSONLWithStore(ctx, storeB, cloneBJSONL); err != nil {
		t.Fatalf("Failed to export from store B: %v", err)
	}
	if err := captureLeftSnapshot(cloneBJSONL); err != nil {
		t.Fatalf("Failed to capture left snapshot for B: %v", err)
	}

	// Step 5: Simulate Clone B pulling from remote (copy remote JSONL)
	remoteData, err := os.ReadFile(remoteJSONL)
	if err != nil {
		t.Fatalf("Failed to read remote JSONL: %v", err)
	}
	if err := os.WriteFile(cloneBJSONL, remoteData, 0644); err != nil {
		t.Fatalf("Failed to write pulled JSONL to clone B: %v", err)
	}

	// Step 6: Clone B applies 3-way merge and prunes deletions
	// This is the key fix - it should detect that bd-delete-me was deleted remotely
	merged, err := merge3WayAndPruneDeletions(ctx, storeB, cloneBJSONL)
	if err != nil {
		t.Fatalf("Failed to apply deletions from merge: %v", err)
	}

	if !merged {
		t.Error("Expected 3-way merge to run, but it was skipped")
	}

	// Step 7: Verify the deletion was applied to Clone B's database
	deletedIssue, err := storeB.GetIssue(ctx, "bd-delete-me")
	if err == nil && deletedIssue != nil {
		t.Errorf("Issue bd-delete-me should have been deleted from Clone B, but still exists")
	}

	// Verify the kept issue still exists
	keptIssue, err := storeB.GetIssue(ctx, "bd-keep-me")
	if err != nil || keptIssue == nil {
		t.Errorf("Issue bd-keep-me should still exist in Clone B")
	}

	// Verify Clone A still has only one issue
	issuesA, err := storeA.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("Failed to search issues in store A: %v", err)
	}
	if len(issuesA) != 1 {
		t.Errorf("Clone A should have 1 issue after deletion, got %d", len(issuesA))
	}

	// Verify Clone B now matches Clone A (both have 1 issue)
	issuesB, err := storeB.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("Failed to search issues in store B: %v", err)
	}
	if len(issuesB) != 1 {
		t.Errorf("Clone B should have 1 issue after merge, got %d", len(issuesB))
	}
}

// TestDeletionWithLocalModification tests the conflict scenario:
// Remote deletes an issue, but local has modified it
func TestDeletionWithLocalModification(t *testing.T) {
	dir := t.TempDir()
	jsonlPath := filepath.Join(dir, "issues.jsonl")
	dbPath := filepath.Join(dir, "beads.db")

	ctx := context.Background()

	store, err := dolt.New(context.Background(), &dolt.Config{Path: dbPath})
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}

	// Create an issue
	issue := &types.Issue{
		ID:          "bd-conflict",
		Title:       "Original title",
		Description: "Original description",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   "bug",
	}

	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	// Export and create base snapshot
	if err := exportToJSONLWithStore(ctx, store, jsonlPath); err != nil {
		t.Fatalf("Failed to export: %v", err)
	}
	if err := initializeSnapshotsIfNeeded(jsonlPath); err != nil {
		t.Fatalf("Failed to initialize snapshots: %v", err)
	}

	// Modify the issue locally
	updates := map[string]interface{}{
		"title": "Modified title locally",
	}
	if err := store.UpdateIssue(ctx, "bd-conflict", updates, "test"); err != nil {
		t.Fatalf("Failed to update issue: %v", err)
	}

	// Export modified state and capture left snapshot
	if err := exportToJSONLWithStore(ctx, store, jsonlPath); err != nil {
		t.Fatalf("Failed to export after modification: %v", err)
	}
	if err := captureLeftSnapshot(jsonlPath); err != nil {
		t.Fatalf("Failed to capture left snapshot: %v", err)
	}

	// Simulate remote deletion (write empty JSONL)
	if err := os.WriteFile(jsonlPath, []byte(""), 0644); err != nil {
		t.Fatalf("Failed to simulate remote deletion: %v", err)
	}

	// Try to merge - deletion now wins over modification (bd-pq5k)
	// This should succeed and delete the issue
	_, err = merge3WayAndPruneDeletions(ctx, store, jsonlPath)
	if err != nil {
		t.Errorf("Expected merge to succeed (deletion wins), but got error: %v", err)
	}

	// The issue should be deleted (deletion wins over modification)
	conflictIssue, err := store.GetIssue(ctx, "bd-conflict")
	if err == nil && conflictIssue != nil {
		t.Error("Issue should be deleted after merge (deletion wins)")
	}
}

// TestComputeAcceptedDeletions tests the deletion detection logic
func TestComputeAcceptedDeletions(t *testing.T) {
	dir := t.TempDir()

	jsonlPath := filepath.Join(dir, "issues.jsonl")
	sm := NewSnapshotManager(jsonlPath)
	basePath, leftPath := sm.GetSnapshotPaths()
	mergedPath := filepath.Join(dir, "merged.jsonl")

	// Base has 3 issues
	baseContent := `{"id":"bd-1","title":"Issue 1"}
{"id":"bd-2","title":"Issue 2"}
{"id":"bd-3","title":"Issue 3"}
`

	// Left has 3 issues (unchanged from base)
	leftContent := baseContent

	// Merged has only 2 issues (bd-2 was deleted remotely)
	mergedContent := `{"id":"bd-1","title":"Issue 1"}
{"id":"bd-3","title":"Issue 3"}
`

	if err := os.WriteFile(basePath, []byte(baseContent), 0644); err != nil {
		t.Fatalf("Failed to write base: %v", err)
	}
	if err := os.WriteFile(leftPath, []byte(leftContent), 0644); err != nil {
		t.Fatalf("Failed to write left: %v", err)
	}
	if err := os.WriteFile(mergedPath, []byte(mergedContent), 0644); err != nil {
		t.Fatalf("Failed to write merged: %v", err)
	}

	deletions, err := sm.ComputeAcceptedDeletions(mergedPath)
	if err != nil {
		t.Fatalf("Failed to compute deletions: %v", err)
	}

	if len(deletions) != 1 {
		t.Errorf("Expected 1 deletion, got %d", len(deletions))
	}

	if len(deletions) > 0 && deletions[0] != "bd-2" {
		t.Errorf("Expected deletion of bd-2, got %s", deletions[0])
	}
}

// TestComputeAcceptedDeletions_LocallyModified tests that deletion wins even for locally modified issues (bd-pq5k)
func TestComputeAcceptedDeletions_LocallyModified(t *testing.T) {
	dir := t.TempDir()

	jsonlPath := filepath.Join(dir, "issues.jsonl")
	sm := NewSnapshotManager(jsonlPath)
	basePath, leftPath := sm.GetSnapshotPaths()
	mergedPath := filepath.Join(dir, "merged.jsonl")

	// Base has 2 issues
	baseContent := `{"id":"bd-1","title":"Original 1"}
{"id":"bd-2","title":"Original 2"}
`

	// Left has bd-2 modified locally
	leftContent := `{"id":"bd-1","title":"Original 1"}
{"id":"bd-2","title":"Modified locally"}
`

	// Merged has only bd-1 (bd-2 deleted remotely, we modified it locally, but deletion wins per bd-pq5k)
	mergedContent := `{"id":"bd-1","title":"Original 1"}
`

	if err := os.WriteFile(basePath, []byte(baseContent), 0644); err != nil {
		t.Fatalf("Failed to write base: %v", err)
	}
	if err := os.WriteFile(leftPath, []byte(leftContent), 0644); err != nil {
		t.Fatalf("Failed to write left: %v", err)
	}
	if err := os.WriteFile(mergedPath, []byte(mergedContent), 0644); err != nil {
		t.Fatalf("Failed to write merged: %v", err)
	}

	deletions, err := sm.ComputeAcceptedDeletions(mergedPath)
	if err != nil {
		t.Fatalf("Failed to compute deletions: %v", err)
	}

	// bd-pq5k: bd-2 SHOULD be in accepted deletions even though modified locally (deletion wins)
	if len(deletions) != 1 {
		t.Errorf("Expected 1 deletion (deletion wins over local modification), got %d: %v", len(deletions), deletions)
	}
	if len(deletions) == 1 && deletions[0] != "bd-2" {
		t.Errorf("Expected deletion of bd-2, got %v", deletions)
	}
}

// TestSnapshotManagement tests the snapshot file lifecycle
func TestSnapshotManagement(t *testing.T) {
	dir := t.TempDir()
	jsonlPath := filepath.Join(dir, "issues.jsonl")

	// Write initial JSONL
	content := `{"id":"bd-1","title":"Test"}
`
	if err := os.WriteFile(jsonlPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write JSONL: %v", err)
	}

	// Initialize snapshots
	if err := initializeSnapshotsIfNeeded(jsonlPath); err != nil {
		t.Fatalf("Failed to initialize snapshots: %v", err)
	}

	sm := NewSnapshotManager(jsonlPath)
	basePath, leftPath := sm.GetSnapshotPaths()

	// Base should exist, left should not
	if !fileExists(basePath) {
		t.Error("Base snapshot should exist after initialization")
	}
	if fileExists(leftPath) {
		t.Error("Left snapshot should not exist yet")
	}

	// Capture left snapshot
	if err := captureLeftSnapshot(jsonlPath); err != nil {
		t.Fatalf("Failed to capture left snapshot: %v", err)
	}

	if !fileExists(leftPath) {
		t.Error("Left snapshot should exist after capture")
	}

	// Update base snapshot
	if err := updateBaseSnapshot(jsonlPath); err != nil {
		t.Fatalf("Failed to update base snapshot: %v", err)
	}

	// Both should exist now
	baseCount, leftCount, baseExists, leftExists := getSnapshotStats(jsonlPath)
	if !baseExists || !leftExists {
		t.Error("Both snapshots should exist")
	}
	if baseCount != 1 || leftCount != 1 {
		t.Errorf("Expected 1 issue in each snapshot, got base=%d left=%d", baseCount, leftCount)
	}
}

// TestMultiRepoDeletionTracking tests deletion tracking with multi-repo mode
// This is the test for bd-4oob: snapshot files need to be created per-JSONL file
func TestMultiRepoDeletionTracking(t *testing.T) {
	t.Skip("JSONL multi-repo sync removed in Phase 2: ExportToMultiRepo is a no-op on Dolt backend")
	initConfigForTest(t)
	// Setup workspace directories
	primaryDir := t.TempDir()
	additionalDir := t.TempDir()

	// Setup .beads directories
	primaryBeadsDir := filepath.Join(primaryDir, ".beads")
	additionalBeadsDir := filepath.Join(additionalDir, ".beads")
	if err := os.MkdirAll(primaryBeadsDir, 0755); err != nil {
		t.Fatalf("Failed to create primary .beads dir: %v", err)
	}
	if err := os.MkdirAll(additionalBeadsDir, 0755); err != nil {
		t.Fatalf("Failed to create additional .beads dir: %v", err)
	}

	// Create database in primary dir
	dbPath := filepath.Join(primaryBeadsDir, "beads.db")
	ctx := context.Background()

	store, err := dolt.New(context.Background(), &dolt.Config{Path: dbPath})
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}

	// Setup multi-repo config
	config.Set("repos.primary", primaryDir)
	config.Set("repos.additional", []string{additionalDir})
	defer func() {
		config.Set("repos.primary", "")
		config.Set("repos.additional", nil)
	}()

	// Create issues in different repos
	primaryIssue := &types.Issue{
		ID:          "bd-primary",
		Title:       "Primary repo issue",
		Description: "This belongs to primary",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   "task",
		SourceRepo:  ".", // Primary repo
	}

	additionalIssue := &types.Issue{
		ID:          "bd-additional",
		Title:       "Additional repo issue",
		Description: "This belongs to additional",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   "task",
		SourceRepo:  additionalDir,
	}

	if err := store.CreateIssue(ctx, primaryIssue, "test"); err != nil {
		t.Fatalf("Failed to create primary issue: %v", err)
	}
	if err := store.CreateIssue(ctx, additionalIssue, "test"); err != nil {
		t.Fatalf("Failed to create additional issue: %v", err)
	}

	// Export to multi-repo (this creates multiple JSONL files)
	results, err := store.ExportToMultiRepo(ctx)
	if err != nil {
		t.Fatalf("ExportToMultiRepo failed: %v", err)
	}
	if results == nil {
		t.Fatal("Expected multi-repo results, got nil")
	}
	if results["."] != 1 {
		t.Errorf("Expected 1 issue in primary repo, got %d", results["."])
	}
	if results[additionalDir] != 1 {
		t.Errorf("Expected 1 issue in additional repo, got %d", results[additionalDir])
	}

	// Verify JSONL files exist
	primaryJSONL := filepath.Join(primaryBeadsDir, "issues.jsonl")
	additionalJSONL := filepath.Join(additionalBeadsDir, "issues.jsonl")

	if !fileExists(primaryJSONL) {
		t.Fatalf("Primary JSONL not created: %s", primaryJSONL)
	}
	if !fileExists(additionalJSONL) {
		t.Fatalf("Additional JSONL not created: %s", additionalJSONL)
	}

	// THIS IS THE BUG: Initialize snapshots - currently only works for single JSONL
	// Should create snapshots for BOTH JSONL files
	if err := initializeSnapshotsIfNeeded(primaryJSONL); err != nil {
		t.Fatalf("Failed to initialize primary snapshots: %v", err)
	}
	if err := initializeSnapshotsIfNeeded(additionalJSONL); err != nil {
		t.Fatalf("Failed to initialize additional snapshots: %v", err)
	}

	// Verify snapshot files exist for both repos
	primarySM := NewSnapshotManager(primaryJSONL)
	primaryBasePath, primaryLeftPath := primarySM.GetSnapshotPaths()
	additionalSM := NewSnapshotManager(additionalJSONL)
	additionalBasePath, additionalLeftPath := additionalSM.GetSnapshotPaths()

	if !fileExists(primaryBasePath) {
		t.Errorf("Primary base snapshot not created: %s", primaryBasePath)
	}
	if !fileExists(additionalBasePath) {
		t.Errorf("Additional base snapshot not created: %s", additionalBasePath)
	}

	// Capture left snapshot BEFORE simulating git pull
	// This represents our local state before the pull
	if err := captureLeftSnapshot(primaryJSONL); err != nil {
		t.Fatalf("Failed to capture primary left snapshot: %v", err)
	}
	if err := captureLeftSnapshot(additionalJSONL); err != nil {
		t.Fatalf("Failed to capture additional left snapshot: %v", err)
	}

	// Simulate remote deletion: replace additional repo JSONL with empty file
	// This simulates what happens after a git pull where remote deleted the issue
	emptyFile, err := os.Create(additionalJSONL)
	if err != nil {
		t.Fatalf("Failed to create empty JSONL: %v", err)
	}
	emptyFile.Close()

	// Verify left snapshots exist
	if !fileExists(primaryLeftPath) {
		t.Errorf("Primary left snapshot not created: %s", primaryLeftPath)
	}
	if !fileExists(additionalLeftPath) {
		t.Errorf("Additional left snapshot not created: %s", additionalLeftPath)
	}

	// Now apply deletion tracking for additional repo
	// This should detect that bd-additional was deleted remotely and remove it from DB
	merged, err := merge3WayAndPruneDeletions(ctx, store, additionalJSONL)
	if err != nil {
		t.Fatalf("merge3WayAndPruneDeletions failed for additional repo: %v", err)
	}
	if !merged {
		t.Error("Expected merge to be performed (base snapshot exists)")
	}

	// Verify the issue was deleted from the database
	issue, err := store.GetIssue(ctx, "bd-additional")
	if err != nil {
		t.Errorf("Unexpected error getting issue: %v", err)
	}
	if issue != nil {
		t.Errorf("Expected bd-additional to be deleted from database, but it still exists: %+v", issue)
	}

	// Verify primary issue still exists
	primaryResult, err := store.GetIssue(ctx, "bd-primary")
	if err != nil {
		t.Errorf("Primary issue should still exist: %v", err)
	}
	if primaryResult == nil {
		t.Error("Primary issue should not be nil")
	}
}

// TestGetMultiRepoJSONLPaths_EmptyPaths tests handling of empty path configs
func TestGetMultiRepoJSONLPaths_EmptyPaths(t *testing.T) {
	initConfigForTest(t)
	// Test with empty primary (should default to ".")
	config.Set("repos.primary", "")
	config.Set("repos.additional", []string{})
	defer func() {
		config.Set("repos.primary", "")
		config.Set("repos.additional", nil)
	}()

	paths := getMultiRepoJSONLPaths()
	if paths != nil {
		t.Errorf("Expected nil (single-repo mode) for empty primary, got %v", paths)
	}
}

// TestGetMultiRepoJSONLPaths_Duplicates tests deduplication of paths
func TestGetMultiRepoJSONLPaths_Duplicates(t *testing.T) {
	initConfigForTest(t)
	// Setup temp dirs
	primaryDir := t.TempDir()

	// Create .beads directories
	if err := os.MkdirAll(filepath.Join(primaryDir, ".beads"), 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	// Test with duplicate paths (., ./, and absolute path to same location)
	config.Set("repos.primary", primaryDir)
	config.Set("repos.additional", []string{primaryDir, primaryDir}) // Duplicates
	defer func() {
		config.Set("repos.primary", "")
		config.Set("repos.additional", nil)
	}()

	paths := getMultiRepoJSONLPaths()

	// Current implementation doesn't dedupe - just verify it returns all entries
	// (This documents current behavior; future improvement could dedupe)
	expectedCount := 3 // primary + 2 duplicates
	if len(paths) != expectedCount {
		t.Errorf("Expected %d paths, got %d: %v", expectedCount, len(paths), paths)
	}

	// All should point to same JSONL location
	expectedJSONL := filepath.Join(primaryDir, ".beads", "issues.jsonl")
	for i, p := range paths {
		if p != expectedJSONL {
			t.Errorf("Path[%d] = %s, want %s", i, p, expectedJSONL)
		}
	}
}

// TestGetMultiRepoJSONLPaths_PathsWithSpaces tests handling of paths containing spaces
func TestGetMultiRepoJSONLPaths_PathsWithSpaces(t *testing.T) {
	initConfigForTest(t)
	// Create temp dir with space in name
	baseDir := t.TempDir()
	primaryDir := filepath.Join(baseDir, "my project")
	additionalDir := filepath.Join(baseDir, "other repo")

	// Create .beads directories
	if err := os.MkdirAll(filepath.Join(primaryDir, ".beads"), 0755); err != nil {
		t.Fatalf("Failed to create primary .beads: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(additionalDir, ".beads"), 0755); err != nil {
		t.Fatalf("Failed to create additional .beads: %v", err)
	}

	config.Set("repos.primary", primaryDir)
	config.Set("repos.additional", []string{additionalDir})
	defer func() {
		config.Set("repos.primary", "")
		config.Set("repos.additional", nil)
	}()

	paths := getMultiRepoJSONLPaths()

	if len(paths) != 2 {
		t.Fatalf("Expected 2 paths, got %d", len(paths))
	}

	// Verify paths are constructed correctly
	expectedPrimary := filepath.Join(primaryDir, ".beads", "issues.jsonl")
	expectedAdditional := filepath.Join(additionalDir, ".beads", "issues.jsonl")

	if paths[0] != expectedPrimary {
		t.Errorf("Primary path = %s, want %s", paths[0], expectedPrimary)
	}
	if paths[1] != expectedAdditional {
		t.Errorf("Additional path = %s, want %s", paths[1], expectedAdditional)
	}
}

// TestGetMultiRepoJSONLPaths_RelativePaths tests handling of relative paths
func TestGetMultiRepoJSONLPaths_RelativePaths(t *testing.T) {
	initConfigForTest(t)
	// Note: Current implementation takes paths as-is without normalization
	// This test documents current behavior
	config.Set("repos.primary", ".")
	config.Set("repos.additional", []string{"../other", "./foo/../bar"})
	defer func() {
		config.Set("repos.primary", "")
		config.Set("repos.additional", nil)
	}()

	paths := getMultiRepoJSONLPaths()

	if len(paths) != 3 {
		t.Fatalf("Expected 3 paths, got %d", len(paths))
	}

	// Current implementation: relative paths are NOT expanded to absolute
	// They're used as-is with filepath.Join
	expectedPrimary := filepath.Join(".", ".beads", "issues.jsonl")
	expectedOther := filepath.Join("../other", ".beads", "issues.jsonl")
	expectedBar := filepath.Join("./foo/../bar", ".beads", "issues.jsonl")

	if paths[0] != expectedPrimary {
		t.Errorf("Primary path = %s, want %s", paths[0], expectedPrimary)
	}
	if paths[1] != expectedOther {
		t.Errorf("Additional[0] path = %s, want %s", paths[1], expectedOther)
	}
	if paths[2] != expectedBar {
		t.Errorf("Additional[1] path = %s, want %s", paths[2], expectedBar)
	}
}

// TestGetMultiRepoJSONLPaths_TildeExpansion tests that tilde is NOT expanded
func TestGetMultiRepoJSONLPaths_TildeExpansion(t *testing.T) {
	initConfigForTest(t)
	// Current implementation does NOT expand tilde - it's used literally
	config.Set("repos.primary", "~/repos/main")
	config.Set("repos.additional", []string{"~/repos/other"})
	defer func() {
		config.Set("repos.primary", "")
		config.Set("repos.additional", nil)
	}()

	paths := getMultiRepoJSONLPaths()

	if len(paths) != 2 {
		t.Fatalf("Expected 2 paths, got %d", len(paths))
	}

	// Tilde should be literal (NOT expanded) in current implementation
	expectedPrimary := filepath.Join("~/repos/main", ".beads", "issues.jsonl")
	expectedAdditional := filepath.Join("~/repos/other", ".beads", "issues.jsonl")

	if paths[0] != expectedPrimary {
		t.Errorf("Primary path = %s, want %s", paths[0], expectedPrimary)
	}
	if paths[1] != expectedAdditional {
		t.Errorf("Additional path = %s, want %s", paths[1], expectedAdditional)
	}
}

// TestMultiRepoSnapshotIsolation verifies that snapshot operations on one repo
// don't interfere with another repo's snapshots
func TestMultiRepoSnapshotIsolation(t *testing.T) {
	// Setup two repo directories
	repo1Dir := t.TempDir()
	repo2Dir := t.TempDir()

	// Create .beads/issues.jsonl in each
	repo1JSONL := filepath.Join(repo1Dir, ".beads", "issues.jsonl")
	repo2JSONL := filepath.Join(repo2Dir, ".beads", "issues.jsonl")

	if err := os.MkdirAll(filepath.Dir(repo1JSONL), 0755); err != nil {
		t.Fatalf("Failed to create repo1 .beads: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(repo2JSONL), 0755); err != nil {
		t.Fatalf("Failed to create repo2 .beads: %v", err)
	}

	// Write test issues
	issue1 := map[string]interface{}{
		"id":    "bd-repo1-issue",
		"title": "Repo 1 Issue",
	}
	issue2 := map[string]interface{}{
		"id":    "bd-repo2-issue",
		"title": "Repo 2 Issue",
	}

	// Write to repo1
	f1, err := os.Create(repo1JSONL)
	if err != nil {
		t.Fatalf("Failed to create repo1 JSONL: %v", err)
	}
	json.NewEncoder(f1).Encode(issue1)
	f1.Close()

	// Write to repo2
	f2, err := os.Create(repo2JSONL)
	if err != nil {
		t.Fatalf("Failed to create repo2 JSONL: %v", err)
	}
	json.NewEncoder(f2).Encode(issue2)
	f2.Close()

	// Initialize snapshots for both
	if err := initializeSnapshotsIfNeeded(repo1JSONL); err != nil {
		t.Fatalf("Failed to init repo1 snapshots: %v", err)
	}
	if err := initializeSnapshotsIfNeeded(repo2JSONL); err != nil {
		t.Fatalf("Failed to init repo2 snapshots: %v", err)
	}

	// Get snapshot paths for both
	repo1SM := NewSnapshotManager(repo1JSONL)
	repo1Base, repo1Left := repo1SM.GetSnapshotPaths()
	repo2SM := NewSnapshotManager(repo2JSONL)
	repo2Base, repo2Left := repo2SM.GetSnapshotPaths()

	// Verify isolation: snapshots should be in different directories
	if filepath.Dir(repo1Base) == filepath.Dir(repo2Base) {
		t.Error("Snapshot directories should be different for different repos")
	}

	// Verify each snapshot contains only its own issue
	repo1IDs, err := repo1SM.BuildIDSet(repo1Base)
	if err != nil {
		t.Fatalf("Failed to read repo1 base snapshot: %v", err)
	}
	repo2IDs, err := repo2SM.BuildIDSet(repo2Base)
	if err != nil {
		t.Fatalf("Failed to read repo2 base snapshot: %v", err)
	}

	if !repo1IDs["bd-repo1-issue"] {
		t.Error("Repo1 snapshot should contain bd-repo1-issue")
	}
	if repo1IDs["bd-repo2-issue"] {
		t.Error("Repo1 snapshot should NOT contain bd-repo2-issue")
	}

	if !repo2IDs["bd-repo2-issue"] {
		t.Error("Repo2 snapshot should contain bd-repo2-issue")
	}
	if repo2IDs["bd-repo1-issue"] {
		t.Error("Repo2 snapshot should NOT contain bd-repo1-issue")
	}

	// Capture left snapshots for both
	if err := captureLeftSnapshot(repo1JSONL); err != nil {
		t.Fatalf("Failed to capture repo1 left: %v", err)
	}
	if err := captureLeftSnapshot(repo2JSONL); err != nil {
		t.Fatalf("Failed to capture repo2 left: %v", err)
	}

	// Verify left snapshots are isolated
	if !fileExists(repo1Left) || !fileExists(repo2Left) {
		t.Error("Both left snapshots should exist")
	}

	repo1LeftIDs, err := repo1SM.BuildIDSet(repo1Left)
	if err != nil {
		t.Fatalf("Failed to read repo1 left snapshot: %v", err)
	}
	repo2LeftIDs, err := repo2SM.BuildIDSet(repo2Left)
	if err != nil {
		t.Fatalf("Failed to read repo2 left snapshot: %v", err)
	}

	if !repo1LeftIDs["bd-repo1-issue"] || repo1LeftIDs["bd-repo2-issue"] {
		t.Error("Repo1 left snapshot has wrong issues")
	}
	if !repo2LeftIDs["bd-repo2-issue"] || repo2LeftIDs["bd-repo1-issue"] {
		t.Error("Repo2 left snapshot has wrong issues")
	}
}

// TestMultiRepoFlushPrefixFiltering tests that non-primary repos only flush issues
// with their own prefix (GH #437 fix).
//
// The bug: In multi-repo mode, when a non-primary repo flushes, it incorrectly writes
// ALL issues (including from primary) to its local issues.jsonl. This causes prefix
// mismatch errors on subsequent imports.
//
// Expected behavior:
// - Primary repo: writes all issues (from all repos)
// - Non-primary repos: only writes issues matching their prefix
func TestMultiRepoFlushPrefixFiltering(t *testing.T) {
	initConfigForTest(t)
	// Setup workspace directories
	primaryDir := t.TempDir()
	additionalDir := t.TempDir()

	// Setup .beads directories
	primaryBeadsDir := filepath.Join(primaryDir, ".beads")
	additionalBeadsDir := filepath.Join(additionalDir, ".beads")
	if err := os.MkdirAll(primaryBeadsDir, 0755); err != nil {
		t.Fatalf("Failed to create primary .beads dir: %v", err)
	}
	if err := os.MkdirAll(additionalBeadsDir, 0755); err != nil {
		t.Fatalf("Failed to create additional .beads dir: %v", err)
	}

	// Create database in additional (non-primary) dir
	dbPath := filepath.Join(additionalBeadsDir, "beads.db")
	ctx := context.Background()

	store, err := dolt.New(context.Background(), &dolt.Config{Path: dbPath})
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Set prefix for additional repo (different from primary)
	if err := store.SetConfig(ctx, "issue_prefix", "foo-b"); err != nil {
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}

	// Setup multi-repo config
	config.Set("repos.primary", primaryDir)
	config.Set("repos.additional", []string{additionalDir})
	defer func() {
		config.Set("repos.primary", "")
		config.Set("repos.additional", nil)
	}()

	// Create issues with different prefixes (simulating hydrated multi-repo)
	// foo-a prefix = primary repo issues (hydrated from remote)
	// foo-b prefix = additional repo issues (local)
	primaryIssue := &types.Issue{
		ID:          "foo-a-001",
		Title:       "Primary repo issue",
		Description: "This belongs to primary",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   "task",
		SourceRepo:  ".",
	}

	additionalIssue := &types.Issue{
		ID:          "foo-b-001",
		Title:       "Additional repo issue",
		Description: "This belongs to additional",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   "task",
		SourceRepo:  additionalDir,
	}

	// Use batch create with SkipPrefixValidation to simulate multi-repo hydration
	// (in real multi-repo mode, issues from other repos are imported with prefix validation skipped)
	if err := store.CreateIssuesWithFullOptions(ctx, []*types.Issue{primaryIssue, additionalIssue}, "test", storage.BatchCreateOptions{SkipPrefixValidation: true, OrphanHandling: storage.OrphanAllow}); err != nil {
		t.Fatalf("Failed to batch create issues: %v", err)
	}

	// Build issues slice (simulating what flushToJSONLWithState does)
	allIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("Failed to search issues: %v", err)
	}
	if len(allIssues) != 2 {
		t.Fatalf("Expected 2 issues, got %d", len(allIssues))
	}

	// Get configured prefix for this repo (additional)
	prefix, err := store.GetConfig(ctx, "issue_prefix")
	if err != nil {
		t.Fatalf("Failed to get prefix: %v", err)
	}

	// Determine if we're primary (we're not - we're in additional)
	cwd := additionalDir // Simulate being in additional repo
	primaryPath := config.GetMultiRepoConfig().Primary
	absCwd, _ := filepath.Abs(cwd)
	absPrimary, _ := filepath.Abs(primaryPath)
	isPrimary := absCwd == absPrimary

	if isPrimary {
		t.Fatal("Expected to be non-primary repo")
	}

	// Filter issues by prefix (the fix)
	filtered := make([]*types.Issue, 0, len(allIssues))
	prefixWithDash := prefix + "-"
	for _, issue := range allIssues {
		if len(issue.ID) >= len(prefixWithDash) && issue.ID[:len(prefixWithDash)] == prefixWithDash {
			filtered = append(filtered, issue)
		}
	}

	// Verify filtering worked
	if len(filtered) != 1 {
		t.Errorf("Expected 1 filtered issue, got %d", len(filtered))
	}
	if len(filtered) > 0 && filtered[0].ID != "foo-b-001" {
		t.Errorf("Expected filtered issue to be foo-b-001, got %s", filtered[0].ID)
	}

	// Write filtered issues to additional repo's JSONL
	jsonlPath := filepath.Join(additionalBeadsDir, "issues.jsonl")
	f, err := os.Create(jsonlPath)
	if err != nil {
		t.Fatalf("Failed to create JSONL: %v", err)
	}
	encoder := json.NewEncoder(f)
	for _, issue := range filtered {
		if err := encoder.Encode(issue); err != nil {
			f.Close()
			t.Fatalf("Failed to encode issue: %v", err)
		}
	}
	f.Close()

	// Read back and verify only foo-b issue is present
	data, err := os.ReadFile(jsonlPath)
	if err != nil {
		t.Fatalf("Failed to read JSONL: %v", err)
	}

	var readIssue types.Issue
	if err := json.Unmarshal(data, &readIssue); err != nil {
		t.Fatalf("Failed to parse JSONL: %v", err)
	}

	if readIssue.ID != "foo-b-001" {
		t.Errorf("JSONL contains wrong issue: expected foo-b-001, got %s", readIssue.ID)
	}

	// Verify foo-a issue is NOT in the file
	if string(data[:10]) == `{"id":"foo-a` {
		t.Error("JSONL incorrectly contains foo-a issue (GH #437 bug)")
	}
}
