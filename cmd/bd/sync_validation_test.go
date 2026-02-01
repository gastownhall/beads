package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/types"
)

// setupSyncValidationTest creates a test store and properly initializes globals.
// Returns the store and a cleanup function that should be deferred.
func setupSyncValidationTest(t *testing.T) (*testing.T, func()) {
	t.Helper()

	tmpDir := t.TempDir()
	testDBPath := filepath.Join(tmpDir, ".beads", "issues.db")

	testStore := newTestStore(t, testDBPath)

	// Save original state
	origStore := store
	origStoreActive := storeActive
	origDBPath := dbPath

	// Set up test state
	store = testStore
	storeMutex.Lock()
	storeActive = true
	storeMutex.Unlock()
	dbPath = testDBPath

	cleanup := func() {
		storeMutex.Lock()
		store = origStore
		storeActive = origStoreActive
		storeMutex.Unlock()
		dbPath = origDBPath
	}

	return t, cleanup
}

// TestValidateOpenIssuesForSync_ModeNone verifies validation is skipped when
// validation.on-sync is set to "none" (default).
func TestValidateOpenIssuesForSync_ModeNone(t *testing.T) {
	_, cleanup := setupSyncValidationTest(t)
	defer cleanup()

	// Create a bug without required sections (should fail validation)
	ctx := context.Background()
	issue := &types.Issue{
		ID:          "test-001",
		Title:       "Bug without sections",
		IssueType:   types.TypeBug,
		Description: "No steps to reproduce or acceptance criteria",
		Status:      types.StatusOpen,
	}
	if err := store.CreateIssue(ctx, issue, "test-user"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	// Set validation mode to "none"
	initConfigForTest(t)
	config.Set("validation.on-sync", "none")

	// Should return nil (skip validation)
	err := validateOpenIssuesForSync(ctx)
	if err != nil {
		t.Errorf("validateOpenIssuesForSync with mode=none returned error: %v", err)
	}
}

// TestValidateOpenIssuesForSync_ModeEmpty verifies validation is skipped when
// validation.on-sync is empty (backwards compatibility).
func TestValidateOpenIssuesForSync_ModeEmpty(t *testing.T) {
	_, cleanup := setupSyncValidationTest(t)
	defer cleanup()

	// Create a bug without required sections
	ctx := context.Background()
	issue := &types.Issue{
		ID:          "test-001",
		Title:       "Bug without sections",
		IssueType:   types.TypeBug,
		Description: "No sections",
		Status:      types.StatusOpen,
	}
	if err := store.CreateIssue(ctx, issue, "test-user"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	// Set validation mode to empty string
	initConfigForTest(t)
	config.Set("validation.on-sync", "")

	// Should return nil (skip validation)
	err := validateOpenIssuesForSync(ctx)
	if err != nil {
		t.Errorf("validateOpenIssuesForSync with mode=empty returned error: %v", err)
	}
}

// TestValidateOpenIssuesForSync_ModeWarn verifies sync proceeds when
// validation.on-sync is "warn" even with invalid issues.
func TestValidateOpenIssuesForSync_ModeWarn(t *testing.T) {
	_, cleanup := setupSyncValidationTest(t)
	defer cleanup()

	// Create a bug without required sections
	ctx := context.Background()
	issue := &types.Issue{
		ID:          "test-001",
		Title:       "Bug without sections",
		IssueType:   types.TypeBug,
		Description: "No steps to reproduce or acceptance criteria",
		Status:      types.StatusOpen,
	}
	if err := store.CreateIssue(ctx, issue, "test-user"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	// Set validation mode to "warn"
	initConfigForTest(t)
	config.Set("validation.on-sync", "warn")

	// Should return nil (warnings printed but sync proceeds)
	// The function prints to stderr but returns nil to allow sync to continue
	err := validateOpenIssuesForSync(ctx)
	if err != nil {
		t.Errorf("validateOpenIssuesForSync with mode=warn should return nil, got: %v", err)
	}
}

// TestValidateOpenIssuesForSync_ModeError verifies sync is blocked when
// validation.on-sync is "error" and issues fail validation.
func TestValidateOpenIssuesForSync_ModeError(t *testing.T) {
	_, cleanup := setupSyncValidationTest(t)
	defer cleanup()

	// Create a bug without required sections
	ctx := context.Background()
	issue := &types.Issue{
		ID:          "test-001",
		Title:       "Bug without sections",
		IssueType:   types.TypeBug,
		Description: "No sections at all",
		Status:      types.StatusOpen,
	}
	if err := store.CreateIssue(ctx, issue, "test-user"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	// Set validation mode to "error"
	initConfigForTest(t)
	config.Set("validation.on-sync", "error")

	// Should return error (function also prints to stderr which we allow)
	err := validateOpenIssuesForSync(ctx)

	if err == nil {
		t.Error("validateOpenIssuesForSync with mode=error should return error for invalid issues")
	}
	if !strings.Contains(err.Error(), "template validation failed") {
		t.Errorf("expected 'template validation failed' in error, got: %v", err)
	}
}

// TestValidateOpenIssuesForSync_NoWarnings verifies no errors when all issues pass validation.
func TestValidateOpenIssuesForSync_NoWarnings(t *testing.T) {
	_, cleanup := setupSyncValidationTest(t)
	defer cleanup()

	// Create a bug WITH all required sections
	ctx := context.Background()
	issue := &types.Issue{
		ID:        "test-001",
		Title:     "Bug with sections",
		IssueType: types.TypeBug,
		Description: `## Steps to Reproduce
1. Do this
2. Do that

## Acceptance Criteria
- It works`,
		Status: types.StatusOpen,
	}
	if err := store.CreateIssue(ctx, issue, "test-user"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	// Set validation mode to "error" (strictest mode)
	initConfigForTest(t)
	config.Set("validation.on-sync", "error")

	// Should return nil (no validation errors)
	err := validateOpenIssuesForSync(ctx)
	if err != nil {
		t.Errorf("validateOpenIssuesForSync should not return error for valid issues: %v", err)
	}
}

// TestValidateOpenIssuesForSync_SkipsClosedIssues verifies closed issues are not validated.
func TestValidateOpenIssuesForSync_SkipsClosedIssues(t *testing.T) {
	_, cleanup := setupSyncValidationTest(t)
	defer cleanup()

	ctx := context.Background()

	// Create a closed bug without required sections (should be skipped)
	closedIssue := &types.Issue{
		ID:          "test-001",
		Title:       "Closed bug without sections",
		IssueType:   types.TypeBug,
		Description: "No sections",
		Status:      types.StatusClosed,
	}
	if err := store.CreateIssue(ctx, closedIssue, "test-user"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	// Set validation mode to "error"
	initConfigForTest(t)
	config.Set("validation.on-sync", "error")

	// Should return nil (closed issues are not validated)
	err := validateOpenIssuesForSync(ctx)
	if err != nil {
		t.Errorf("validateOpenIssuesForSync should skip closed issues: %v", err)
	}
}

// TestValidateOpenIssuesForSync_ChoreHasNoRequirements verifies chore type
// has no required sections and passes validation.
func TestValidateOpenIssuesForSync_ChoreHasNoRequirements(t *testing.T) {
	_, cleanup := setupSyncValidationTest(t)
	defer cleanup()

	// Create a chore without any sections (should pass - no requirements)
	ctx := context.Background()
	issue := &types.Issue{
		ID:          "test-001",
		Title:       "Chore issue",
		IssueType:   types.TypeChore,
		Description: "Just a description, no sections needed",
		Status:      types.StatusOpen,
	}
	if err := store.CreateIssue(ctx, issue, "test-user"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	// Set validation mode to "error"
	initConfigForTest(t)
	config.Set("validation.on-sync", "error")

	// Should return nil (chore has no requirements)
	err := validateOpenIssuesForSync(ctx)
	if err != nil {
		t.Errorf("validateOpenIssuesForSync should not error for chore issues: %v", err)
	}
}
