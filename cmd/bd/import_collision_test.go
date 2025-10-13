package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

// TestImportSimpleCollision tests the basic collision detection and resolution
func TestImportSimpleCollision(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bd-collision-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() {
		if err := os.RemoveAll(tmpDir); err != nil {
			t.Logf("Warning: cleanup failed: %v", err)
		}
	}()

	dbPath := filepath.Join(tmpDir, "test.db")
	testStore, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer func() {
		if err := testStore.Close(); err != nil {
			t.Logf("Warning: failed to close store: %v", err)
		}
	}()

	ctx := context.Background()

	// Create existing issue with a higher ID to avoid conflicts with auto-generated IDs
	existing := &types.Issue{
		ID:          "bd-10",
		Title:       "Existing issue",
		Description: "Original description",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	if err := testStore.CreateIssue(ctx, existing, "test"); err != nil {
		t.Fatalf("Failed to create existing issue: %v", err)
	}

	// Prepare import with collision
	incoming := &types.Issue{
		ID:          "bd-10",
		Title:       "MODIFIED issue",
		Description: "Different description",
		Status:      types.StatusInProgress,
		Priority:    2,
		IssueType:   types.TypeBug,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	incomingIssues := []*types.Issue{incoming}

	// Test collision detection
	result, err := sqlite.DetectCollisions(ctx, testStore, incomingIssues)
	if err != nil {
		t.Fatalf("DetectCollisions failed: %v", err)
	}

	if len(result.Collisions) != 1 {
		t.Fatalf("Expected 1 collision, got %d", len(result.Collisions))
	}

	if result.Collisions[0].ID != "bd-10" {
		t.Errorf("Expected collision ID bd-10, got %s", result.Collisions[0].ID)
	}

	// Test resolution
	allExisting, _ := testStore.SearchIssues(ctx, "", types.IssueFilter{})

	if err := sqlite.ScoreCollisions(ctx, testStore, result.Collisions, allExisting); err != nil {
		t.Fatalf("ScoreCollisions failed: %v", err)
	}

	idMapping, err := sqlite.RemapCollisions(ctx, testStore, result.Collisions, allExisting)
	if err != nil {
		t.Fatalf("RemapCollisions failed: %v", err)
	}

	if len(idMapping) != 1 {
		t.Fatalf("Expected 1 remapping, got %d", len(idMapping))
	}

	newID := idMapping["bd-10"]
	if newID == "" {
		t.Fatal("Expected bd-10 to be remapped")
	}

	// Verify remapped issue exists
	remapped, err := testStore.GetIssue(ctx, newID)
	if err != nil {
		t.Fatalf("Failed to get remapped issue: %v", err)
	}
	if remapped == nil {
		t.Fatal("Remapped issue not found")
	}
	if remapped.Title != "MODIFIED issue" {
		t.Errorf("Remapped issue title = %s, want 'MODIFIED issue'", remapped.Title)
	}

	// Verify original issue unchanged
	original, err := testStore.GetIssue(ctx, "bd-10")
	if err != nil {
		t.Fatalf("Failed to get original issue: %v", err)
	}
	if original.Title != "Existing issue" {
		t.Errorf("Original issue modified: %s", original.Title)
	}
}

// TestImportMultipleCollisions tests handling of multiple colliding issues
func TestImportMultipleCollisions(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bd-collision-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() {
		if err := os.RemoveAll(tmpDir); err != nil {
			t.Logf("Warning: cleanup failed: %v", err)
		}
	}()

	dbPath := filepath.Join(tmpDir, "test.db")
	testStore, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer func() {
		if err := testStore.Close(); err != nil {
			t.Logf("Warning: failed to close store: %v", err)
		}
	}()

	ctx := context.Background()

	// Create existing issues with high IDs to avoid conflicts with auto-generated sequence
	for i := 100; i <= 102; i++ {
		issue := &types.Issue{
			ID:          fmt.Sprintf("bd-%d", i),
			Title:       fmt.Sprintf("Existing issue %d", i),
			Description: "Original",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeTask,
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		}
		if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue %d: %v", i, err)
		}
	}

	// Prepare import with multiple collisions
	incomingIssues := []*types.Issue{
		{
			ID:          "bd-100",
			Title:       "Modified 1",
			Description: "Changed",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeTask,
		},
		{
			ID:          "bd-101",
			Title:       "Modified 2",
			Description: "Changed",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeTask,
		},
		{
			ID:          "bd-102",
			Title:       "Modified 3",
			Description: "Changed",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeTask,
		},
	}

	result, err := sqlite.DetectCollisions(ctx, testStore, incomingIssues)
	if err != nil {
		t.Fatalf("DetectCollisions failed: %v", err)
	}

	if len(result.Collisions) != 3 {
		t.Fatalf("Expected 3 collisions, got %d", len(result.Collisions))
	}

	// Resolve collisions
	allExisting, _ := testStore.SearchIssues(ctx, "", types.IssueFilter{})
	if err := sqlite.ScoreCollisions(ctx, testStore, result.Collisions, allExisting); err != nil {
		t.Fatalf("ScoreCollisions failed: %v", err)
	}

	idMapping, err := sqlite.RemapCollisions(ctx, testStore, result.Collisions, allExisting)
	if err != nil {
		t.Fatalf("RemapCollisions failed: %v", err)
	}

	if len(idMapping) != 3 {
		t.Fatalf("Expected 3 remappings, got %d", len(idMapping))
	}

	// Verify all remappings
	for oldID, newID := range idMapping {
		remapped, err := testStore.GetIssue(ctx, newID)
		if err != nil {
			t.Fatalf("Failed to get remapped issue %s: %v", newID, err)
		}
		if remapped == nil {
			t.Fatalf("Remapped issue %s not found", newID)
		}
		if !strings.Contains(remapped.Title, "Modified") {
			t.Errorf("Remapped issue %s has wrong title: %s", oldID, remapped.Title)
		}
	}
}

// TestImportDependencyUpdates tests that dependencies are updated during remapping
func TestImportDependencyUpdates(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bd-collision-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() {
		if err := os.RemoveAll(tmpDir); err != nil {
			t.Logf("Warning: cleanup failed: %v", err)
		}
	}()

	dbPath := filepath.Join(tmpDir, "test.db")
	testStore, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer func() {
		if err := testStore.Close(); err != nil {
			t.Logf("Warning: failed to close store: %v", err)
		}
	}()

	ctx := context.Background()

	// Create existing issues with dependencies
	issue1 := &types.Issue{
		ID:        "bd-10",
		Title:     "Issue 1",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	issue2 := &types.Issue{
		ID:        "bd-11",
		Title:     "Issue 2",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	issue3 := &types.Issue{
		ID:        "bd-12",
		Title:     "Issue 3",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	if err := testStore.CreateIssue(ctx, issue1, "test"); err != nil {
		t.Fatalf("Failed to create issue 1: %v", err)
	}
	if err := testStore.CreateIssue(ctx, issue2, "test"); err != nil {
		t.Fatalf("Failed to create issue 2: %v", err)
	}
	if err := testStore.CreateIssue(ctx, issue3, "test"); err != nil {
		t.Fatalf("Failed to create issue 3: %v", err)
	}

	// Add dependencies: bd-1 → bd-2, bd-3 → bd-2
	dep1 := &types.Dependency{
		IssueID:     "bd-10",
		DependsOnID: "bd-11",
		Type:        types.DepBlocks,
	}
	dep2 := &types.Dependency{
		IssueID:     "bd-12",
		DependsOnID: "bd-11",
		Type:        types.DepBlocks,
	}

	if err := testStore.AddDependency(ctx, dep1, "test"); err != nil {
		t.Fatalf("Failed to add dep1: %v", err)
	}
	if err := testStore.AddDependency(ctx, dep2, "test"); err != nil {
		t.Fatalf("Failed to add dep2: %v", err)
	}

	// Import colliding bd-2
	incomingIssues := []*types.Issue{
		{
			ID:          "bd-11",
			Title:       "Modified Issue 2",
			Description: "Changed",
			Status:      types.StatusOpen,
			Priority:    2,
			IssueType:   types.TypeBug,
		},
	}

	result, err := sqlite.DetectCollisions(ctx, testStore, incomingIssues)
	if err != nil {
		t.Fatalf("DetectCollisions failed: %v", err)
	}

	if len(result.Collisions) != 1 {
		t.Fatalf("Expected 1 collision, got %d", len(result.Collisions))
	}

	// Resolve collision
	allExisting, _ := testStore.SearchIssues(ctx, "", types.IssueFilter{})
	if err := sqlite.ScoreCollisions(ctx, testStore, result.Collisions, allExisting); err != nil {
		t.Fatalf("ScoreCollisions failed: %v", err)
	}

	idMapping, err := sqlite.RemapCollisions(ctx, testStore, result.Collisions, allExisting)
	if err != nil {
		t.Fatalf("RemapCollisions failed: %v", err)
	}

	newID := idMapping["bd-11"]
	if newID == "" {
		t.Fatal("bd-2 not remapped")
	}

	// Verify dependencies were updated
	// bd-1 should now depend on newID
	deps1, err := testStore.GetDependencyRecords(ctx, "bd-10")
	if err != nil {
		t.Fatalf("Failed to get deps for bd-1: %v", err)
	}
	if len(deps1) != 1 {
		t.Fatalf("Expected 1 dependency for bd-1, got %d", len(deps1))
	}
	if deps1[0].DependsOnID != newID {
		t.Errorf("bd-1 dependency not updated: %s, want %s", deps1[0].DependsOnID, newID)
	}

	// bd-3 should now depend on newID
	deps3, err := testStore.GetDependencyRecords(ctx, "bd-12")
	if err != nil {
		t.Fatalf("Failed to get deps for bd-3: %v", err)
	}
	if len(deps3) != 1 {
		t.Fatalf("Expected 1 dependency for bd-3, got %d", len(deps3))
	}
	if deps3[0].DependsOnID != newID {
		t.Errorf("bd-3 dependency not updated: %s, want %s", deps3[0].DependsOnID, newID)
	}
}

// TestImportTextReferenceUpdates tests that text references are updated during remapping
func TestImportTextReferenceUpdates(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bd-collision-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() {
		if err := os.RemoveAll(tmpDir); err != nil {
			t.Logf("Warning: cleanup failed: %v", err)
		}
	}()

	dbPath := filepath.Join(tmpDir, "test.db")
	testStore, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer func() {
		if err := testStore.Close(); err != nil {
			t.Logf("Warning: failed to close store: %v", err)
		}
	}()

	ctx := context.Background()

	// Create existing issues with text references
	issue1 := &types.Issue{
		ID:                 "bd-10",
		Title:              "Issue 1",
		Description:        "This depends on bd-11 and bd-12",
		Design:             "Implementation uses bd-11 approach",
		Notes:              "See bd-12 for details",
		AcceptanceCriteria: "Must work with bd-11",
		Status:             types.StatusOpen,
		Priority:           1,
		IssueType:          types.TypeTask,
		CreatedAt:          time.Now(),
		UpdatedAt:          time.Now(),
	}
	issue2 := &types.Issue{
		ID:        "bd-11",
		Title:     "Issue 2",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	issue3 := &types.Issue{
		ID:        "bd-12",
		Title:     "Issue 3",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	if err := testStore.CreateIssue(ctx, issue1, "test"); err != nil {
		t.Fatalf("Failed to create issue 1: %v", err)
	}
	if err := testStore.CreateIssue(ctx, issue2, "test"); err != nil {
		t.Fatalf("Failed to create issue 2: %v", err)
	}
	if err := testStore.CreateIssue(ctx, issue3, "test"); err != nil {
		t.Fatalf("Failed to create issue 3: %v", err)
	}

	// Import colliding issues
	incomingIssues := []*types.Issue{
		{
			ID:        "bd-11",
			Title:     "Modified Issue 2",
			Status:    types.StatusInProgress,
			Priority:  2,
			IssueType: types.TypeBug,
		},
		{
			ID:        "bd-12",
			Title:     "Modified Issue 3",
			Status:    types.StatusInProgress,
			Priority:  2,
			IssueType: types.TypeBug,
		},
	}

	result, err := sqlite.DetectCollisions(ctx, testStore, incomingIssues)
	if err != nil {
		t.Fatalf("DetectCollisions failed: %v", err)
	}

	if len(result.Collisions) != 2 {
		t.Fatalf("Expected 2 collisions, got %d", len(result.Collisions))
	}

	// Resolve collisions
	allExisting, _ := testStore.SearchIssues(ctx, "", types.IssueFilter{})
	if err := sqlite.ScoreCollisions(ctx, testStore, result.Collisions, allExisting); err != nil {
		t.Fatalf("ScoreCollisions failed: %v", err)
	}

	idMapping, err := sqlite.RemapCollisions(ctx, testStore, result.Collisions, allExisting)
	if err != nil {
		t.Fatalf("RemapCollisions failed: %v", err)
	}

	if len(idMapping) != 2 {
		t.Fatalf("Expected 2 remappings, got %d", len(idMapping))
	}

	newID2 := idMapping["bd-11"]
	newID3 := idMapping["bd-12"]

	// Verify text references were updated in issue 1
	updated, err := testStore.GetIssue(ctx, "bd-10")
	if err != nil {
		t.Fatalf("Failed to get updated issue 1: %v", err)
	}

	if !strings.Contains(updated.Description, newID2) {
		t.Errorf("Description not updated: %s (should contain %s)", updated.Description, newID2)
	}
	if !strings.Contains(updated.Description, newID3) {
		t.Errorf("Description not updated: %s (should contain %s)", updated.Description, newID3)
	}
	if !strings.Contains(updated.Design, newID2) {
		t.Errorf("Design not updated: %s (should contain %s)", updated.Design, newID2)
	}
	if !strings.Contains(updated.Notes, newID3) {
		t.Errorf("Notes not updated: %s (should contain %s)", updated.Notes, newID3)
	}
	if !strings.Contains(updated.AcceptanceCriteria, newID2) {
		t.Errorf("AcceptanceCriteria not updated: %s (should contain %s)", updated.AcceptanceCriteria, newID2)
	}

	// Verify old IDs are NOT present
	if strings.Contains(updated.Description, "bd-11") {
		t.Error("Old ID bd-11 still present in Description")
	}
	if strings.Contains(updated.Description, "bd-12") {
		t.Error("Old ID bd-12 still present in Description")
	}
}

// TestImportChainDependencies tests remapping with chained dependencies
func TestImportChainDependencies(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bd-collision-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() {
		if err := os.RemoveAll(tmpDir); err != nil {
			t.Logf("Warning: cleanup failed: %v", err)
		}
	}()

	dbPath := filepath.Join(tmpDir, "test.db")
	testStore, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer func() {
		if err := testStore.Close(); err != nil {
			t.Logf("Warning: failed to close store: %v", err)
		}
	}()

	ctx := context.Background()

	// Create chain: bd-100 → bd-101 → bd-102 → bd-103
	for i := 100; i <= 103; i++ {
		issue := &types.Issue{
			ID:        fmt.Sprintf("bd-%d", i),
			Title:     fmt.Sprintf("Issue %d", i),
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue %d: %v", i, err)
		}
	}

	// Add chain dependencies
	for i := 100; i <= 102; i++ {
		dep := &types.Dependency{
			IssueID:     fmt.Sprintf("bd-%d", i),
			DependsOnID: fmt.Sprintf("bd-%d", i+1),
			Type:        types.DepBlocks,
		}
		if err := testStore.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatalf("Failed to add dependency %d: %v", i, err)
		}
	}

	// Import colliding bd-101
	incomingIssues := []*types.Issue{
		{
			ID:        "bd-101",
			Title:     "Modified Issue 101",
			Status:    types.StatusInProgress,
			Priority:  2,
			IssueType: types.TypeBug,
		},
	}

	result, err := sqlite.DetectCollisions(ctx, testStore, incomingIssues)
	if err != nil {
		t.Fatalf("DetectCollisions failed: %v", err)
	}

	// Resolve collision
	allExisting, _ := testStore.SearchIssues(ctx, "", types.IssueFilter{})
	if err := sqlite.ScoreCollisions(ctx, testStore, result.Collisions, allExisting); err != nil {
		t.Fatalf("ScoreCollisions failed: %v", err)
	}

	idMapping, err := sqlite.RemapCollisions(ctx, testStore, result.Collisions, allExisting)
	if err != nil {
		t.Fatalf("RemapCollisions failed: %v", err)
	}

	newID := idMapping["bd-101"]

	// Verify chain is maintained
	// bd-100 → newID (was bd-101)
	deps1, _ := testStore.GetDependencyRecords(ctx, "bd-100")
	if len(deps1) != 1 || deps1[0].DependsOnID != newID {
		t.Errorf("bd-100 dependency broken: %v", deps1)
	}

	// newID → bd-102
	depsNew, _ := testStore.GetDependencyRecords(ctx, newID)
	if len(depsNew) != 1 || depsNew[0].DependsOnID != "bd-102" {
		t.Errorf("newID dependency broken: %v", depsNew)
	}

	// bd-102 → bd-103 (unchanged)
	deps3, _ := testStore.GetDependencyRecords(ctx, "bd-102")
	if len(deps3) != 1 || deps3[0].DependsOnID != "bd-103" {
		t.Errorf("bd-102 dependency broken: %v", deps3)
	}
}

// TestImportPartialIDMatch tests word boundary matching (bd-10 vs bd-100)
func TestImportPartialIDMatch(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bd-collision-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() {
		if err := os.RemoveAll(tmpDir); err != nil {
			t.Logf("Warning: cleanup failed: %v", err)
		}
	}()

	dbPath := filepath.Join(tmpDir, "test.db")
	testStore, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer func() {
		if err := testStore.Close(); err != nil {
			t.Logf("Warning: failed to close store: %v", err)
		}
	}()

	ctx := context.Background()

	// Create issues with similar IDs (use higher numbers to avoid conflicts)
	issues := []*types.Issue{
		{
			ID:          "bd-50",
			Title:       "Issue 50",
			Description: "References bd-100 and bd-1000 and bd-10000",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeTask,
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		},
		{
			ID:        "bd-100",
			Title:     "Issue 100",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		},
		{
			ID:        "bd-1000",
			Title:     "Issue 1000",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		},
		{
			ID:        "bd-10000",
			Title:     "Issue 10000",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		},
	}

	for _, issue := range issues {
		if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create %s: %v", issue.ID, err)
		}
	}

	// Import colliding bd-100
	incomingIssues := []*types.Issue{
		{
			ID:        "bd-100",
			Title:     "Modified Issue 100",
			Status:    types.StatusInProgress,
			Priority:  2,
			IssueType: types.TypeBug,
		},
	}

	result, err := sqlite.DetectCollisions(ctx, testStore, incomingIssues)
	if err != nil {
		t.Fatalf("DetectCollisions failed: %v", err)
	}

	// Resolve collision
	allExisting, _ := testStore.SearchIssues(ctx, "", types.IssueFilter{})
	if err := sqlite.ScoreCollisions(ctx, testStore, result.Collisions, allExisting); err != nil {
		t.Fatalf("ScoreCollisions failed: %v", err)
	}

	idMapping, err := sqlite.RemapCollisions(ctx, testStore, result.Collisions, allExisting)
	if err != nil {
		t.Fatalf("RemapCollisions failed: %v", err)
	}

	newID100 := idMapping["bd-100"]

	// Verify only bd-100 was replaced, not bd-1000 or bd-10000
	updated, err := testStore.GetIssue(ctx, "bd-50")
	if err != nil {
		t.Fatalf("Failed to get updated issue: %v", err)
	}

	if !strings.Contains(updated.Description, newID100) {
		t.Errorf("bd-100 not replaced: %s", updated.Description)
	}
	if !strings.Contains(updated.Description, "bd-1000") {
		t.Errorf("bd-1000 incorrectly replaced: %s", updated.Description)
	}
	if !strings.Contains(updated.Description, "bd-10000") {
		t.Errorf("bd-10000 incorrectly replaced: %s", updated.Description)
	}

	// Make sure old bd-100 reference is gone
	if strings.Contains(updated.Description, " bd-100 ") || strings.Contains(updated.Description, " bd-100,") {
		t.Errorf("Old bd-100 reference still present: %s", updated.Description)
	}
}

// TestImportExactMatch tests idempotent import (no collision)
func TestImportExactMatch(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bd-collision-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() {
		if err := os.RemoveAll(tmpDir); err != nil {
			t.Logf("Warning: cleanup failed: %v", err)
		}
	}()

	dbPath := filepath.Join(tmpDir, "test.db")
	testStore, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer func() {
		if err := testStore.Close(); err != nil {
			t.Logf("Warning: failed to close store: %v", err)
		}
	}()

	ctx := context.Background()

	// Create existing issue
	existing := &types.Issue{
		ID:          "bd-10",
		Title:       "Test issue",
		Description: "Description",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	if err := testStore.CreateIssue(ctx, existing, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	// Import identical issue
	incoming := &types.Issue{
		ID:          "bd-10",
		Title:       "Test issue",
		Description: "Description",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
	}

	result, err := sqlite.DetectCollisions(ctx, testStore, []*types.Issue{incoming})
	if err != nil {
		t.Fatalf("DetectCollisions failed: %v", err)
	}

	// Should be exact match, not collision
	if len(result.Collisions) != 0 {
		t.Errorf("Expected 0 collisions for exact match, got %d", len(result.Collisions))
	}
	if len(result.ExactMatches) != 1 {
		t.Errorf("Expected 1 exact match, got %d", len(result.ExactMatches))
	}
}

// TestImportMixedScenario tests import with exact matches, collisions, and new issues
func TestImportMixedScenario(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bd-collision-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() {
		if err := os.RemoveAll(tmpDir); err != nil {
			t.Logf("Warning: cleanup failed: %v", err)
		}
	}()

	dbPath := filepath.Join(tmpDir, "test.db")
	testStore, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer func() {
		if err := testStore.Close(); err != nil {
			t.Logf("Warning: failed to close store: %v", err)
		}
	}()

	ctx := context.Background()

	// Create existing issues with high IDs
	for i := 200; i <= 201; i++ {
		issue := &types.Issue{
			ID:          fmt.Sprintf("bd-%d", i),
			Title:       fmt.Sprintf("Issue %d", i),
			Description: "Original",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeTask,
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		}
		if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue %d: %v", i, err)
		}
	}

	// Import: exact match (bd-200), collision (bd-201), new (bd-202)
	incomingIssues := []*types.Issue{
		{
			ID:          "bd-200",
			Title:       "Issue 200",
			Description: "Original",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeTask,
		},
		{
			ID:          "bd-201",
			Title:       "Modified Issue 201",
			Description: "Changed",
			Status:      types.StatusInProgress,
			Priority:    2,
			IssueType:   types.TypeBug,
		},
		{
			ID:          "bd-202",
			Title:       "New Issue",
			Description: "Brand new",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeFeature,
		},
	}

	result, err := sqlite.DetectCollisions(ctx, testStore, incomingIssues)
	if err != nil {
		t.Fatalf("DetectCollisions failed: %v", err)
	}

	if len(result.ExactMatches) != 1 {
		t.Errorf("Expected 1 exact match, got %d", len(result.ExactMatches))
	}
	if len(result.Collisions) != 1 {
		t.Errorf("Expected 1 collision, got %d", len(result.Collisions))
	}
	if len(result.NewIssues) != 1 {
		t.Errorf("Expected 1 new issue, got %d", len(result.NewIssues))
	}
}

// TestImportWithDependenciesInJSONL tests importing issues with embedded dependencies
func TestImportWithDependenciesInJSONL(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bd-collision-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() {
		if err := os.RemoveAll(tmpDir); err != nil {
			t.Logf("Warning: cleanup failed: %v", err)
		}
	}()

	dbPath := filepath.Join(tmpDir, "test.db")
	testStore, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer func() {
		if err := testStore.Close(); err != nil {
			t.Logf("Warning: failed to close store: %v", err)
		}
	}()

	ctx := context.Background()

	// Create JSONL with dependencies
	jsonl := `{"id":"bd-10","title":"Issue 1","status":"open","priority":1,"issue_type":"task"}
{"id":"bd-11","title":"Issue 2","status":"open","priority":1,"issue_type":"task","dependencies":[{"issue_id":"bd-11","depends_on_id":"bd-10","type":"blocks"}]}`

	// Parse JSONL
	var issues []*types.Issue
	for _, line := range strings.Split(strings.TrimSpace(jsonl), "\n") {
		var issue types.Issue
		if err := json.Unmarshal([]byte(line), &issue); err != nil {
			t.Fatalf("Failed to parse JSONL: %v", err)
		}
		issues = append(issues, &issue)
	}

	// Create issues
	for _, issue := range issues {
		if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}
	}

	// Add dependencies from JSONL
	for _, issue := range issues {
		for _, dep := range issue.Dependencies {
			if err := testStore.AddDependency(ctx, dep, "test"); err != nil {
				t.Fatalf("Failed to add dependency: %v", err)
			}
		}
	}

	// Verify dependency
	deps, err := testStore.GetDependencyRecords(ctx, "bd-11")
	if err != nil {
		t.Fatalf("Failed to get dependencies: %v", err)
	}
	if len(deps) != 1 {
		t.Fatalf("Expected 1 dependency, got %d", len(deps))
	}
	if deps[0].DependsOnID != "bd-10" {
		t.Errorf("Dependency target = %s, want bd-1", deps[0].DependsOnID)
	}
}

func TestImportCounterSyncAfterHighID(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bd-collision-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() {
		if err := os.RemoveAll(tmpDir); err != nil {
			t.Logf("Warning: cleanup failed: %v", err)
		}
	}()

	dbPath := filepath.Join(tmpDir, "test.db")
	testStore, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer func() {
		if err := testStore.Close(); err != nil {
			t.Logf("Warning: failed to close store: %v", err)
		}
	}()

	ctx := context.Background()

	if err := testStore.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("Failed to set issue prefix: %v", err)
	}

	for i := 0; i < 3; i++ {
		issue := &types.Issue{
			Title:     fmt.Sprintf("Auto issue %d", i+1),
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
		}
		if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create auto issue %d: %v", i+1, err)
		}
	}

	highIDIssue := &types.Issue{
		ID:        "bd-100",
		Title:     "High ID issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	if err := testStore.CreateIssue(ctx, highIDIssue, "import"); err != nil {
		t.Fatalf("Failed to import high ID issue: %v", err)
	}

	// Step 4: Sync counters after import (mimics import command behavior)
	if err := testStore.SyncAllCounters(ctx); err != nil {
		t.Fatalf("Failed to sync counters: %v", err)
	}

	// Step 5: Create another auto-generated issue
	// This should get bd-101 (counter should have synced to 100), not bd-4
	newIssue := &types.Issue{
		Title:     "New issue after import",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := testStore.CreateIssue(ctx, newIssue, "test"); err != nil {
		t.Fatalf("Failed to create new issue: %v", err)
	}

	if newIssue.ID != "bd-101" {
		t.Errorf("Expected new issue to get ID bd-101, got %s", newIssue.ID)
	}
}
