//go:build cgo && integration
// +build cgo,integration

package beads_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
)

// integrationTestHelper provides common test setup and assertion methods
type integrationTestHelper struct {
	t     *testing.T
	ctx   context.Context
	store beads.Storage
}

func newIntegrationHelper(t *testing.T, store beads.Storage) *integrationTestHelper {
	return &integrationTestHelper{t: t, ctx: context.Background(), store: store}
}

func (h *integrationTestHelper) createIssue(title string, issueType types.IssueType, priority int) *types.Issue {
	issue := &types.Issue{
		Title:     title,
		Status:    types.StatusOpen,
		Priority:  priority,
		IssueType: issueType,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := h.store.CreateIssue(h.ctx, issue, "test-actor"); err != nil {
		h.t.Fatalf("CreateIssue failed: %v", err)
	}
	return issue
}

func (h *integrationTestHelper) createFullIssue(desc, design, acceptance, notes, assignee string) *types.Issue {
	issue := &types.Issue{
		Title:              "Complete issue",
		Description:        desc,
		Design:             design,
		AcceptanceCriteria: acceptance,
		Notes:              notes,
		Status:             types.StatusOpen,
		Priority:           1,
		IssueType:          types.TypeFeature,
		Assignee:           assignee,
		CreatedAt:          time.Now(),
		UpdatedAt:          time.Now(),
	}
	if err := h.store.CreateIssue(h.ctx, issue, "test-actor"); err != nil {
		h.t.Fatalf("CreateIssue failed: %v", err)
	}
	return issue
}

func (h *integrationTestHelper) updateIssue(id string, updates map[string]interface{}) {
	if err := h.store.UpdateIssue(h.ctx, id, updates, "test-actor"); err != nil {
		h.t.Fatalf("UpdateIssue failed: %v", err)
	}
}

func (h *integrationTestHelper) closeIssue(id string, reason string) {
	if err := h.store.CloseIssue(h.ctx, id, reason, "test-actor", ""); err != nil {
		h.t.Fatalf("CloseIssue failed: %v", err)
	}
}

func (h *integrationTestHelper) addDependency(issue1ID, issue2ID string) {
	dep := &types.Dependency{
		IssueID:     issue1ID,
		DependsOnID: issue2ID,
		Type:        types.DepBlocks,
		CreatedAt:   time.Now(),
		CreatedBy:   "test-actor",
	}
	if err := h.store.AddDependency(h.ctx, dep, "test-actor"); err != nil {
		h.t.Fatalf("AddDependency failed: %v", err)
	}
}

func (h *integrationTestHelper) addLabel(id, label string) {
	if err := h.store.AddLabel(h.ctx, id, label, "test-actor"); err != nil {
		h.t.Fatalf("AddLabel failed: %v", err)
	}
}

func (h *integrationTestHelper) addComment(id, user, text string) *types.Comment {
	comment, err := h.store.AddIssueComment(h.ctx, id, user, text)
	if err != nil {
		h.t.Fatalf("AddIssueComment failed: %v", err)
	}
	return comment
}

func (h *integrationTestHelper) getIssue(id string) *types.Issue {
	issue, err := h.store.GetIssue(h.ctx, id)
	if err != nil {
		h.t.Fatalf("GetIssue failed: %v", err)
	}
	return issue
}

func (h *integrationTestHelper) getDependencies(id string) []*types.Issue {
	deps, err := h.store.GetDependencies(h.ctx, id)
	if err != nil {
		h.t.Fatalf("GetDependencies failed: %v", err)
	}
	return deps
}

func (h *integrationTestHelper) getLabels(id string) []string {
	labels, err := h.store.GetLabels(h.ctx, id)
	if err != nil {
		h.t.Fatalf("GetLabels failed: %v", err)
	}
	return labels
}

func (h *integrationTestHelper) getComments(id string) []*types.Comment {
	comments, err := h.store.GetIssueComments(h.ctx, id)
	if err != nil {
		h.t.Fatalf("GetIssueComments failed: %v", err)
	}
	return comments
}

func (h *integrationTestHelper) assertID(id string) {
	if id == "" {
		h.t.Error("Issue ID should be auto-generated")
	}
}

func (h *integrationTestHelper) assertEqual(expected, actual interface{}, field string) {
	if expected != actual {
		h.t.Errorf("Expected %s %v, got %v", field, expected, actual)
	}
}

func (h *integrationTestHelper) assertNotNil(value interface{}, field string) {
	if value == nil {
		h.t.Errorf("Expected %s to be set", field)
	}
}

func (h *integrationTestHelper) assertCount(count, expected int, item string) {
	if count != expected {
		h.t.Fatalf("Expected %d %s, got %d", expected, item, count)
	}
}

// TestLibraryIntegration tests the full public API that external users will use
func TestLibraryIntegration(t *testing.T) {
	if testDoltPort == 0 {
		t.Skip("skipping: Dolt test container not available")
	}
	// Setup: Create a temporary database
	tmpDir, err := os.MkdirTemp("", "beads-integration-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	ctx := context.Background()
	store, err := dolt.New(ctx, &dolt.Config{Path: filepath.Dir(dbPath)})
	if err != nil {
		t.Fatalf("dolt.New failed: %v", err)
	}
	defer store.Close()

	// CRITICAL (bd-166): Set issue_prefix to prevent "database not initialized" errors
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}

	h := newIntegrationHelper(t, store)

	// Test 1: Create issue
	t.Run("CreateIssue", func(t *testing.T) {
		issue := h.createIssue("Test task", types.TypeTask, 2)
		h.assertID(issue.ID)
		t.Logf("Created issue: %s", issue.ID)
	})

	// Test 2: Get issue
	t.Run("GetIssue", func(_ *testing.T) {
		issue := h.createIssue("Get test", types.TypeBug, 1)
		retrieved := h.getIssue(issue.ID)
		h.assertEqual(issue.Title, retrieved.Title, "title")
		h.assertEqual(types.TypeBug, retrieved.IssueType, "type")
	})

	// Test 3: Update issue
	t.Run("UpdateIssue", func(_ *testing.T) {
		issue := h.createIssue("Update test", types.TypeTask, 2)
		updates := map[string]interface{}{"status": types.StatusInProgress, "assignee": "test-user"}
		h.updateIssue(issue.ID, updates)
		updated := h.getIssue(issue.ID)
		h.assertEqual(types.StatusInProgress, updated.Status, "status")
		h.assertEqual("test-user", updated.Assignee, "assignee")
	})

	// Test 4: Add dependency
	t.Run("AddDependency", func(_ *testing.T) {
		issue1 := h.createIssue("Parent task", types.TypeTask, 1)
		issue2 := h.createIssue("Child task", types.TypeTask, 1)
		h.addDependency(issue1.ID, issue2.ID)
		deps := h.getDependencies(issue1.ID)
		h.assertCount(len(deps), 1, "dependencies")
		h.assertEqual(issue2.ID, deps[0].ID, "dependency ID")
	})

	// Test 5: Add label
	t.Run("AddLabel", func(t *testing.T) {
		issue := h.createIssue("Label test", types.TypeFeature, 2)
		h.addLabel(issue.ID, "urgent")
		labels := h.getLabels(issue.ID)
		h.assertCount(len(labels), 1, "labels")
		h.assertEqual("urgent", labels[0], "label")
	})

	// Test 6: Add comment
	t.Run("AddComment", func(t *testing.T) {
		issue := h.createIssue("Comment test", types.TypeTask, 2)
		comment := h.addComment(issue.ID, "test-user", "Test comment")
		h.assertEqual("Test comment", comment.Text, "comment text")
		comments := h.getComments(issue.ID)
		h.assertCount(len(comments), 1, "comments")
	})

	// Test 7: Get ready work
	t.Run("GetReadyWork", func(t *testing.T) {
		for i := 0; i < 3; i++ {
			h.createIssue("Ready work test", types.TypeTask, i)
		}
		ready, err := store.GetReadyWork(h.ctx, types.WorkFilter{Status: types.StatusOpen, Limit: 5})
		if err != nil {
			t.Fatalf("GetReadyWork failed: %v", err)
		}
		if len(ready) == 0 {
			t.Error("Expected some ready work, got none")
		}
		t.Logf("Found %d ready issues", len(ready))
	})

	// Test 8: Get statistics
	t.Run("GetStatistics", func(t *testing.T) {
		stats, err := store.GetStatistics(h.ctx)
		if err != nil {
			t.Fatalf("GetStatistics failed: %v", err)
		}
		if stats.TotalIssues == 0 {
			t.Error("Expected some total issues, got 0")
		}
		t.Logf("Stats: Total=%d, Open=%d, InProgress=%d, Closed=%d",
			stats.TotalIssues, stats.OpenIssues, stats.InProgressIssues, stats.ClosedIssues)
	})

	// Test 9: Close issue
	t.Run("CloseIssue", func(t *testing.T) {
		issue := h.createIssue("Close test", types.TypeTask, 2)
		h.closeIssue(issue.ID, "Completed")
		closed := h.getIssue(issue.ID)
		h.assertEqual(types.StatusClosed, closed.Status, "status")
		h.assertNotNil(closed.ClosedAt, "ClosedAt")
	})
}

// TestDependencyTypes ensures all dependency type constants are exported
func TestDependencyTypes(t *testing.T) {
	depTypes := []types.DependencyType{
		types.DepBlocks,
		types.DepRelated,
		types.DepParentChild,
		types.DepDiscoveredFrom,
	}

	for _, dt := range depTypes {
		if dt == "" {
			t.Errorf("Dependency type should not be empty")
		}
	}
}

// TestStatusConstants ensures all status constants are exported
func TestStatusConstants(t *testing.T) {
	statuses := []types.Status{
		types.StatusOpen,
		types.StatusInProgress,
		types.StatusClosed,
		types.StatusBlocked,
	}

	for _, s := range statuses {
		if s == "" {
			t.Errorf("Status should not be empty")
		}
	}
}

// TestIssueTypeConstants ensures all issue type constants are exported
func TestIssueTypeConstants(t *testing.T) {
	issueTypes := []types.IssueType{
		types.TypeBug,
		types.TypeFeature,
		types.TypeTask,
		types.TypeEpic,
		types.TypeChore,
	}

	for _, it := range issueTypes {
		if it == "" {
			t.Errorf("IssueType should not be empty")
		}
	}
}

// TestBatchCreateIssues tests creating multiple issues at once
func TestBatchCreateIssues(t *testing.T) {
	if testDoltPort == 0 {
		t.Skip("skipping: Dolt test container not available")
	}
	tmpDir, err := os.MkdirTemp("", "beads-batch-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	ctx := context.Background()
	store, err := dolt.New(ctx, &dolt.Config{Path: filepath.Dir(dbPath)})
	if err != nil {
		t.Fatalf("dolt.New failed: %v", err)
	}
	defer store.Close()

	// CRITICAL (bd-166): Set issue_prefix to prevent "database not initialized" errors
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}

	// Create multiple issues
	issues := make([]*types.Issue, 5)
	for i := 0; i < 5; i++ {
		issues[i] = &types.Issue{
			Title:     "Batch test",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
	}

	err = store.CreateIssues(ctx, issues, "test-actor")
	if err != nil {
		t.Fatalf("CreateIssues failed: %v", err)
	}

	// Verify all got IDs
	for i, issue := range issues {
		if issue.ID == "" {
			t.Errorf("Issue %d should have ID set", i)
		}
	}
}

// TestFindDatabasePathIntegration tests the database discovery
func TestFindDatabasePathIntegration(t *testing.T) {
	// Create temporary directory with .beads
	tmpDir, err := os.MkdirTemp("", "beads-find-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	beadsDir := filepath.Join(tmpDir, ".beads")
	os.MkdirAll(beadsDir, 0o755)

	dbPath := filepath.Join(beadsDir, "test.db")
	f, _ := os.Create(dbPath)
	f.Close()

	// Change to temp directory
	t.Chdir(tmpDir)

	// Should find the database
	found := beads.FindDatabasePath()
	if found == "" {
		t.Error("Expected to find database, got empty string")
	}

	t.Logf("Found database at: %s", found)
}

// TestRoundTripIssue tests creating, updating, and retrieving an issue
func TestRoundTripIssue(t *testing.T) {
	if testDoltPort == 0 {
		t.Skip("skipping: Dolt test container not available")
	}
	tmpDir, err := os.MkdirTemp("", "beads-roundtrip-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	ctx := context.Background()
	store, err := dolt.New(ctx, &dolt.Config{Path: filepath.Dir(dbPath)})
	if err != nil {
		t.Fatalf("dolt.New failed: %v", err)
	}
	defer store.Close()

	// CRITICAL (bd-166): Set issue_prefix to prevent "database not initialized" errors
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}

	h := newIntegrationHelper(t, store)
	original := h.createFullIssue("Full description", "Design notes", "Acceptance criteria", "Implementation notes", "developer")

	// Retrieve and verify all fields
	retrieved := h.getIssue(original.ID)
	h.assertEqual(original.Title, retrieved.Title, "Title")
	h.assertEqual(original.Description, retrieved.Description, "Description")
	h.assertEqual(original.Design, retrieved.Design, "Design")
	h.assertEqual(original.AcceptanceCriteria, retrieved.AcceptanceCriteria, "AcceptanceCriteria")
	h.assertEqual(original.Notes, retrieved.Notes, "Notes")
	h.assertEqual(original.Status, retrieved.Status, "Status")
	h.assertEqual(original.Priority, retrieved.Priority, "Priority")
	h.assertEqual(original.IssueType, retrieved.IssueType, "IssueType")
	h.assertEqual(original.Assignee, retrieved.Assignee, "Assignee")
}

// TestImportWithDeletedParent verifies parent resurrection during import
// This tests the fix for bd-d19a (import failure on missing parent issues)
func TestImportWithDeletedParent(t *testing.T) {
	if testDoltPort == 0 {
		t.Skip("skipping: Dolt test container not available")
	}
	tmpDir, err := os.MkdirTemp("", "beads-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	beadsDir := filepath.Join(tmpDir, ".beads")
	dbPath := filepath.Join(beadsDir, "beads.db")
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")

	// Create .beads directory
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	// Phase 1: Create parent and child in JSONL (simulating historical git state)
	ctx := context.Background()

	parent := types.Issue{
		ID:          "bd-parent",
		Title:       "Parent Epic",
		Description: "Original parent description",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeEpic,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	child := types.Issue{
		ID:        "bd-parent.1",
		Title:     "Child Task",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	// Write both to JSONL (parent exists in git history)
	file, err := os.Create(jsonlPath)
	if err != nil {
		t.Fatalf("Failed to create JSONL: %v", err)
	}
	encoder := json.NewEncoder(file)
	if err := encoder.Encode(parent); err != nil {
		file.Close()
		t.Fatalf("Failed to encode parent: %v", err)
	}
	if err := encoder.Encode(child); err != nil {
		file.Close()
		t.Fatalf("Failed to encode child: %v", err)
	}
	file.Close()

	// Phase 2: Create fresh database and import only the child
	// (simulating scenario where parent was deleted)
	store, err := dolt.New(ctx, &dolt.Config{Path: filepath.Dir(dbPath)})
	if err != nil {
		t.Fatalf("dolt.New failed: %v", err)
	}
	defer store.Close()

	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}

	// Manually create only the child (parent missing)
	childToImport := &types.Issue{
		ID:        "bd-parent.1",
		Title:     "Child Task",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	// This should trigger parent resurrection from JSONL
	if err := store.CreateIssue(ctx, childToImport, "test"); err != nil {
		t.Fatalf("Failed to create child (resurrection should have prevented error): %v", err)
	}

	// Phase 3: Verify results

	// Verify child was created successfully
	retrievedChild, err := store.GetIssue(ctx, "bd-parent.1")
	if err != nil {
		t.Fatalf("Failed to retrieve child: %v", err)
	}
	if retrievedChild == nil {
		t.Fatal("Child was not created")
	}
	if retrievedChild.Title != "Child Task" {
		t.Errorf("Expected child title 'Child Task', got %s", retrievedChild.Title)
	}

	// Verify parent was resurrected as a closed placeholder
	retrievedParent, err := store.GetIssue(ctx, "bd-parent")
	if err != nil {
		t.Fatalf("Failed to retrieve parent: %v", err)
	}
	if retrievedParent == nil {
		t.Fatal("Parent was not resurrected")
	}
	if retrievedParent.Status != types.StatusClosed {
		t.Errorf("Expected parent status=closed, got %s", retrievedParent.Status)
	}
	if retrievedParent.Priority != 4 {
		t.Errorf("Expected parent priority=4 (lowest), got %d", retrievedParent.Priority)
	}
	if retrievedParent.Title != "Parent Epic" {
		t.Errorf("Expected original title preserved, got %s", retrievedParent.Title)
	}
	if retrievedParent.Description == "" {
		t.Error("Expected resurrected parent description to be set")
	}
	if retrievedParent.ClosedAt == nil {
		t.Error("Expected resurrected parent to have ClosedAt set")
	}

	// Verify description contains resurrection marker
	if len(retrievedParent.Description) < 13 || retrievedParent.Description[:13] != "[RESURRECTED]" {
		t.Errorf("Expected [RESURRECTED] prefix in description, got: %s", retrievedParent.Description)
	}

	t.Logf("Parent %s successfully resurrected as closed placeholder", "bd-parent")
	t.Logf("Child %s created successfully with resurrected parent", "bd-parent.1")
}
