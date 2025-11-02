package importer

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

func TestImportWithExternalRef(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	// Create database
	store, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create database: %v", err)
	}
	defer store.Close()

	// Set prefix
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	// Create initial issue with external_ref
	externalRef := "JIRA-100"
	initial := &types.Issue{
		ID:          "bd-test-1",
		Title:       "Initial title",
		Description: "Initial description",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeBug,
		ExternalRef: &externalRef,
		CreatedAt:   time.Now().Add(-2 * time.Hour),
		UpdatedAt:   time.Now().Add(-2 * time.Hour),
	}

	err = store.CreateIssue(ctx, initial, "test")
	if err != nil {
		t.Fatalf("Failed to create initial issue: %v", err)
	}

	// Import updated issue with same external_ref but different content
	updated := &types.Issue{
		ID:          "bd-test-1", // Same ID
		Title:       "Updated title from Jira",
		Description: "Updated description from Jira",
		Status:      types.StatusInProgress,
		Priority:    2,
		IssueType:   types.TypeBug,
		ExternalRef: &externalRef, // Same external_ref
		CreatedAt:   initial.CreatedAt,
		UpdatedAt:   time.Now(), // Newer timestamp
	}

	opts := Options{
		DryRun:               false,
		SkipUpdate:           false,
		SkipPrefixValidation: true,
	}

	result, err := ImportIssues(ctx, dbPath, store, []*types.Issue{updated}, opts)
	if err != nil {
		t.Fatalf("ImportIssues failed: %v", err)
	}

	// Should have updated 1 issue
	if result.Updated != 1 {
		t.Errorf("Expected 1 updated issue, got %d", result.Updated)
	}

	if result.Created != 0 {
		t.Errorf("Expected 0 created issues, got %d", result.Created)
	}

	// Verify the update
	issue, err := store.GetIssue(ctx, "bd-test-1")
	if err != nil {
		t.Fatalf("Failed to get issue: %v", err)
	}

	if issue.Title != "Updated title from Jira" {
		t.Errorf("Expected title 'Updated title from Jira', got '%s'", issue.Title)
	}

	if issue.Status != types.StatusInProgress {
		t.Errorf("Expected status in_progress, got %s", issue.Status)
	}
}

func TestImportWithExternalRefDifferentID(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	// Create database
	store, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create database: %v", err)
	}
	defer store.Close()

	// Set prefix
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	// Create initial issue with external_ref
	externalRef := "GH-200"
	initial := &types.Issue{
		ID:          "bd-old-id",
		Title:       "Initial title",
		Description: "Initial description",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeFeature,
		ExternalRef: &externalRef,
		CreatedAt:   time.Now().Add(-2 * time.Hour),
		UpdatedAt:   time.Now().Add(-2 * time.Hour),
	}

	err = store.CreateIssue(ctx, initial, "test")
	if err != nil {
		t.Fatalf("Failed to create initial issue: %v", err)
	}

	// Import issue with same external_ref but DIFFERENT ID
	// This simulates re-syncing from GitHub where ID changed
	updated := &types.Issue{
		ID:          "bd-new-id", // Different ID
		Title:       "Updated title from GitHub",
		Description: "Updated description from GitHub",
		Status:      types.StatusInProgress,
		Priority:    2,
		IssueType:   types.TypeFeature,
		ExternalRef: &externalRef, // Same external_ref
		CreatedAt:   initial.CreatedAt,
		UpdatedAt:   time.Now(), // Newer timestamp
	}

	opts := Options{
		DryRun:               false,
		SkipUpdate:           false,
		SkipPrefixValidation: true,
	}

	result, err := ImportIssues(ctx, dbPath, store, []*types.Issue{updated}, opts)
	if err != nil {
		t.Fatalf("ImportIssues failed: %v", err)
	}

	// Should have updated the existing issue (matched by external_ref)
	if result.Updated != 1 {
		t.Errorf("Expected 1 updated issue, got %d", result.Updated)
	}

	// Verify the old ID was updated (not deleted/recreated)
	oldIssue, err := store.GetIssue(ctx, "bd-old-id")
	if err != nil {
		t.Fatalf("Failed to get issue by old ID: %v", err)
	}

	if oldIssue == nil {
		t.Fatal("Expected old ID to still exist and be updated")
	}

	if oldIssue.Title != "Updated title from GitHub" {
		t.Errorf("Expected title 'Updated title from GitHub', got '%s'", oldIssue.Title)
	}

	// The new ID should NOT exist (we updated the existing one)
	newIssue, err := store.GetIssue(ctx, "bd-new-id")
	if err != nil {
		t.Fatalf("Failed to check for new ID: %v", err)
	}

	if newIssue != nil {
		t.Error("Expected new ID to NOT be created, but it exists")
	}
}

func TestImportLocalIssueNotOverwrittenByExternalRef(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	// Create database
	store, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create database: %v", err)
	}
	defer store.Close()

	// Set prefix
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	// Create local issue WITHOUT external_ref
	local := &types.Issue{
		ID:          "bd-local-1",
		Title:       "Local task",
		Description: "Created locally",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
		// No ExternalRef
		CreatedAt: time.Now().Add(-2 * time.Hour),
		UpdatedAt: time.Now().Add(-2 * time.Hour),
	}

	err = store.CreateIssue(ctx, local, "test")
	if err != nil {
		t.Fatalf("Failed to create local issue: %v", err)
	}

	// Import external issue with external_ref but different ID
	externalRef := "JIRA-300"
	external := &types.Issue{
		ID:          "bd-external-1",
		Title:       "External issue",
		Description: "From Jira",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeBug,
		ExternalRef: &externalRef,
		CreatedAt:   time.Now().Add(-1 * time.Hour),
		UpdatedAt:   time.Now().Add(-1 * time.Hour),
	}

	opts := Options{
		DryRun:               false,
		SkipUpdate:           false,
		SkipPrefixValidation: true,
	}

	result, err := ImportIssues(ctx, dbPath, store, []*types.Issue{external}, opts)
	if err != nil {
		t.Fatalf("ImportIssues failed: %v", err)
	}

	// Should create new issue (not overwrite local one)
	if result.Created != 1 {
		t.Errorf("Expected 1 created issue, got %d", result.Created)
	}

	// Verify local issue still exists unchanged
	localIssue, err := store.GetIssue(ctx, "bd-local-1")
	if err != nil {
		t.Fatalf("Failed to get local issue: %v", err)
	}

	if localIssue == nil {
		t.Fatal("Local issue was deleted!")
	}

	if localIssue.Title != "Local task" {
		t.Errorf("Local issue was modified! Title: %s", localIssue.Title)
	}

	if localIssue.ExternalRef != nil {
		t.Error("Local issue should not have external_ref")
	}

	// Verify external issue was created
	externalIssue, err := store.GetIssue(ctx, "bd-external-1")
	if err != nil {
		t.Fatalf("Failed to get external issue: %v", err)
	}

	if externalIssue == nil {
		t.Fatal("External issue was not created")
	}

	if externalIssue.ExternalRef == nil || *externalIssue.ExternalRef != externalRef {
		t.Error("External issue missing external_ref")
	}
}

func TestImportExternalRefTimestampCheck(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	// Create database
	store, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create database: %v", err)
	}
	defer store.Close()

	// Set prefix
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	// Create issue with external_ref and recent timestamp
	externalRef := "LINEAR-400"
	recent := &types.Issue{
		ID:          "bd-test-1",
		Title:       "Recent version",
		Description: "Most recent",
		Status:      types.StatusInProgress,
		Priority:    1,
		IssueType:   types.TypeBug,
		ExternalRef: &externalRef,
		CreatedAt:   time.Now().Add(-1 * time.Hour),
		UpdatedAt:   time.Now(), // Recent
	}

	err = store.CreateIssue(ctx, recent, "test")
	if err != nil {
		t.Fatalf("Failed to create recent issue: %v", err)
	}

	// Try to import older version with same external_ref
	older := &types.Issue{
		ID:          "bd-test-1",
		Title:       "Older version",
		Description: "Older",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeBug,
		ExternalRef: &externalRef,
		CreatedAt:   time.Now().Add(-2 * time.Hour),
		UpdatedAt:   time.Now().Add(-2 * time.Hour), // Older
	}

	opts := Options{
		DryRun:               false,
		SkipUpdate:           false,
		SkipPrefixValidation: true,
	}

	result, err := ImportIssues(ctx, dbPath, store, []*types.Issue{older}, opts)
	if err != nil {
		t.Fatalf("ImportIssues failed: %v", err)
	}

	// Should NOT update (incoming is older)
	if result.Updated != 0 {
		t.Errorf("Expected 0 updated issues (timestamp check), got %d", result.Updated)
	}

	if result.Unchanged != 1 {
		t.Errorf("Expected 1 unchanged issue, got %d", result.Unchanged)
	}

	// Verify the issue was not changed
	issue, err := store.GetIssue(ctx, "bd-test-1")
	if err != nil {
		t.Fatalf("Failed to get issue: %v", err)
	}

	if issue.Title != "Recent version" {
		t.Errorf("Issue was updated when it shouldn't be! Title: %s", issue.Title)
	}

	if issue.Status != types.StatusInProgress {
		t.Errorf("Issue status changed! Got %s", issue.Status)
	}
}
