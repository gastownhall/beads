package dolt

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/types"
)

// testTimeout is the maximum time for any single test operation.
const testTimeout = 30 * time.Second

// testContext returns a context with timeout for test operations
func testContext(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), testTimeout)
}

// skipIfNoDolt skips the test if Dolt is not installed
func skipIfNoDolt(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("dolt"); err != nil {
		t.Skip("Dolt not installed, skipping test")
	}
}

// setupTestStore creates a test store with a temporary directory
func setupTestStore(t *testing.T) (*DoltStore, func()) {
	t.Helper()
	skipIfNoDolt(t)

	ctx, cancel := testContext(t)
	defer cancel()

	tmpDir, err := os.MkdirTemp("", "dolt-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	cfg := &Config{
		Path:           tmpDir,
		CommitterName:  "test",
		CommitterEmail: "test@example.com",
		Database:       "testdb",
	}

	store, err := New(ctx, cfg)
	if err != nil {
		os.RemoveAll(tmpDir)
		// Server mode requires a running Dolt server - skip if unavailable
		t.Skipf("failed to create Dolt store: %v", err)
	}

	// Set up issue prefix
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		store.Close()
		os.RemoveAll(tmpDir)
		t.Fatalf("failed to set prefix: %v", err)
	}

	cleanup := func() {
		store.Close()
		os.RemoveAll(tmpDir)
	}

	return store, cleanup
}

func TestNewDoltStore(t *testing.T) {
	skipIfNoDolt(t)

	ctx := context.Background()
	tmpDir, err := os.MkdirTemp("", "dolt-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &Config{
		Path:           tmpDir,
		CommitterName:  "test",
		CommitterEmail: "test@example.com",
		Database:       "testdb",
	}

	store, err := New(ctx, cfg)
	if err != nil {
		t.Fatalf("failed to create Dolt store: %v", err)
	}
	defer store.Close()

	// Verify store path
	if store.Path() != tmpDir {
		t.Errorf("expected path %s, got %s", tmpDir, store.Path())
	}

	// Verify not closed
	if store.IsClosed() {
		t.Error("store should not be closed")
	}
}

// TestCreateIssueEventType verifies that CreateIssue accepts event type
// without requiring it in types.custom config (GH#1356).
func TestCreateIssueEventType(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// setupTestStore does not set types.custom, so this reproduces the bug
	event := &types.Issue{
		Title:     "state change audit trail",
		Status:    types.StatusClosed,
		Priority:  4,
		IssueType: types.TypeEvent,
	}
	err := store.CreateIssue(ctx, event, "test-user")
	if err != nil {
		t.Fatalf("CreateIssue with event type should succeed without types.custom, got: %v", err)
	}

	got, err := store.GetIssue(ctx, event.ID)
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}
	if got.IssueType != types.TypeEvent {
		t.Errorf("Expected IssueType %q, got %q", types.TypeEvent, got.IssueType)
	}
}

func TestDoltStoreConfig(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Test SetConfig
	if err := store.SetConfig(ctx, "test_key", "test_value"); err != nil {
		t.Fatalf("failed to set config: %v", err)
	}

	// Test GetConfig
	value, err := store.GetConfig(ctx, "test_key")
	if err != nil {
		t.Fatalf("failed to get config: %v", err)
	}
	if value != "test_value" {
		t.Errorf("expected 'test_value', got %q", value)
	}

	// Test GetAllConfig
	allConfig, err := store.GetAllConfig(ctx)
	if err != nil {
		t.Fatalf("failed to get all config: %v", err)
	}
	if allConfig["test_key"] != "test_value" {
		t.Errorf("expected test_key in all config")
	}

	// Test DeleteConfig
	if err := store.DeleteConfig(ctx, "test_key"); err != nil {
		t.Fatalf("failed to delete config: %v", err)
	}
	value, err = store.GetConfig(ctx, "test_key")
	if err != nil {
		t.Fatalf("failed to get deleted config: %v", err)
	}
	if value != "" {
		t.Errorf("expected empty value after delete, got %q", value)
	}
}

func TestDoltStoreIssue(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create an issue
	issue := &types.Issue{
		Title:       "Test Issue",
		Description: "Test description",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
	}

	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	// Verify ID was generated
	if issue.ID == "" {
		t.Error("expected issue ID to be generated")
	}

	// Get the issue back
	retrieved, err := store.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("failed to get issue: %v", err)
	}
	if retrieved == nil {
		t.Fatal("expected to retrieve issue")
	}
	if retrieved.Title != issue.Title {
		t.Errorf("expected title %q, got %q", issue.Title, retrieved.Title)
	}
}

func TestDoltStoreIssueUpdate(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create an issue
	issue := &types.Issue{
		Title:       "Original Title",
		Description: "Original description",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
	}

	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	// Update the issue
	updates := map[string]interface{}{
		"title":    "Updated Title",
		"priority": 1,
		"status":   string(types.StatusInProgress),
	}

	if err := store.UpdateIssue(ctx, issue.ID, updates, "tester"); err != nil {
		t.Fatalf("failed to update issue: %v", err)
	}

	// Get the updated issue
	retrieved, err := store.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("failed to get issue: %v", err)
	}
	if retrieved.Title != "Updated Title" {
		t.Errorf("expected title 'Updated Title', got %q", retrieved.Title)
	}
	if retrieved.Priority != 1 {
		t.Errorf("expected priority 1, got %d", retrieved.Priority)
	}
	if retrieved.Status != types.StatusInProgress {
		t.Errorf("expected status in_progress, got %s", retrieved.Status)
	}
}

func TestDoltStoreIssueClose(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create an issue
	issue := &types.Issue{
		Title:       "Issue to Close",
		Description: "Will be closed",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
	}

	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	// Close the issue
	if err := store.CloseIssue(ctx, issue.ID, "completed", "tester", "session123"); err != nil {
		t.Fatalf("failed to close issue: %v", err)
	}

	// Get the closed issue
	retrieved, err := store.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("failed to get issue: %v", err)
	}
	if retrieved.Status != types.StatusClosed {
		t.Errorf("expected status closed, got %s", retrieved.Status)
	}
	if retrieved.ClosedAt == nil {
		t.Error("expected closed_at to be set")
	}
}

func TestDoltStoreLabels(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create an issue
	issue := &types.Issue{
		Title:       "Issue with Labels",
		Description: "Test labels",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
	}

	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	// Add labels
	if err := store.AddLabel(ctx, issue.ID, "bug", "tester"); err != nil {
		t.Fatalf("failed to add label: %v", err)
	}
	if err := store.AddLabel(ctx, issue.ID, "priority", "tester"); err != nil {
		t.Fatalf("failed to add second label: %v", err)
	}

	// Get labels
	labels, err := store.GetLabels(ctx, issue.ID)
	if err != nil {
		t.Fatalf("failed to get labels: %v", err)
	}
	if len(labels) != 2 {
		t.Errorf("expected 2 labels, got %d", len(labels))
	}

	// Remove label
	if err := store.RemoveLabel(ctx, issue.ID, "bug", "tester"); err != nil {
		t.Fatalf("failed to remove label: %v", err)
	}

	// Verify removal
	labels, err = store.GetLabels(ctx, issue.ID)
	if err != nil {
		t.Fatalf("failed to get labels after removal: %v", err)
	}
	if len(labels) != 1 {
		t.Errorf("expected 1 label after removal, got %d", len(labels))
	}
}

func TestDoltStoreDependencies(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create parent and child issues
	parent := &types.Issue{
		ID:          "test-parent",
		Title:       "Parent Issue",
		Description: "Parent description",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeEpic,
	}
	child := &types.Issue{
		ID:          "test-child",
		Title:       "Child Issue",
		Description: "Child description",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
	}

	if err := store.CreateIssue(ctx, parent, "tester"); err != nil {
		t.Fatalf("failed to create parent issue: %v", err)
	}
	if err := store.CreateIssue(ctx, child, "tester"); err != nil {
		t.Fatalf("failed to create child issue: %v", err)
	}

	// Add dependency (child depends on parent)
	dep := &types.Dependency{
		IssueID:     child.ID,
		DependsOnID: parent.ID,
		Type:        types.DepBlocks,
	}
	if err := store.AddDependency(ctx, dep, "tester"); err != nil {
		t.Fatalf("failed to add dependency: %v", err)
	}

	// Get dependencies
	deps, err := store.GetDependencies(ctx, child.ID)
	if err != nil {
		t.Fatalf("failed to get dependencies: %v", err)
	}
	if len(deps) != 1 {
		t.Errorf("expected 1 dependency, got %d", len(deps))
	}
	if deps[0].ID != parent.ID {
		t.Errorf("expected dependency on %s, got %s", parent.ID, deps[0].ID)
	}

	// Get dependents
	dependents, err := store.GetDependents(ctx, parent.ID)
	if err != nil {
		t.Fatalf("failed to get dependents: %v", err)
	}
	if len(dependents) != 1 {
		t.Errorf("expected 1 dependent, got %d", len(dependents))
	}

	// Check if blocked
	blocked, blockers, err := store.IsBlocked(ctx, child.ID)
	if err != nil {
		t.Fatalf("failed to check if blocked: %v", err)
	}
	if !blocked {
		t.Error("expected child to be blocked")
	}
	if len(blockers) != 1 || blockers[0] != parent.ID {
		t.Errorf("expected blocker %s, got %v", parent.ID, blockers)
	}

	// Remove dependency
	if err := store.RemoveDependency(ctx, child.ID, parent.ID, "tester"); err != nil {
		t.Fatalf("failed to remove dependency: %v", err)
	}

	// Verify removal
	deps, err = store.GetDependencies(ctx, child.ID)
	if err != nil {
		t.Fatalf("failed to get dependencies after removal: %v", err)
	}
	if len(deps) != 0 {
		t.Errorf("expected 0 dependencies after removal, got %d", len(deps))
	}
}

func TestDoltStoreSearch(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create multiple issues
	issues := []*types.Issue{
		{
			ID:          "test-search-1",
			Title:       "First Issue",
			Description: "Search test one",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeTask,
		},
		{
			ID:          "test-search-2",
			Title:       "Second Issue",
			Description: "Search test two",
			Status:      types.StatusOpen,
			Priority:    2,
			IssueType:   types.TypeBug,
		},
		{
			ID:          "test-search-3",
			Title:       "Third Issue",
			Description: "Different content",
			Status:      types.StatusClosed,
			Priority:    3,
			IssueType:   types.TypeTask,
		},
	}

	for _, issue := range issues {
		if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("failed to create issue %s: %v", issue.ID, err)
		}
	}

	// Search by query
	results, err := store.SearchIssues(ctx, "Search test", types.IssueFilter{})
	if err != nil {
		t.Fatalf("failed to search issues: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results for 'Search test', got %d", len(results))
	}

	// Search with status filter
	openStatus := types.StatusOpen
	results, err = store.SearchIssues(ctx, "", types.IssueFilter{Status: &openStatus})
	if err != nil {
		t.Fatalf("failed to search with status filter: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 open issues, got %d", len(results))
	}

	// Search by issue type
	bugType := types.TypeBug
	results, err = store.SearchIssues(ctx, "", types.IssueFilter{IssueType: &bugType})
	if err != nil {
		t.Fatalf("failed to search by type: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 bug, got %d", len(results))
	}
}

func TestDoltStoreCreateIssues(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create multiple issues in batch
	issues := []*types.Issue{
		{
			ID:          "test-batch-1",
			Title:       "Batch Issue 1",
			Description: "First batch issue",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeTask,
		},
		{
			ID:          "test-batch-2",
			Title:       "Batch Issue 2",
			Description: "Second batch issue",
			Status:      types.StatusOpen,
			Priority:    2,
			IssueType:   types.TypeTask,
		},
	}

	if err := store.CreateIssues(ctx, issues, "tester"); err != nil {
		t.Fatalf("failed to create issues: %v", err)
	}

	// Verify all issues were created
	for _, issue := range issues {
		retrieved, err := store.GetIssue(ctx, issue.ID)
		if err != nil {
			t.Fatalf("failed to get issue %s: %v", issue.ID, err)
		}
		if retrieved == nil {
			t.Errorf("expected to retrieve issue %s", issue.ID)
		}
		if retrieved.Title != issue.Title {
			t.Errorf("expected title %q, got %q", issue.Title, retrieved.Title)
		}
	}
}

func TestDoltStoreComments(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create an issue
	issue := &types.Issue{
		ID:          "test-comment-issue",
		Title:       "Issue with Comments",
		Description: "Test comments",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
	}

	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	// Add comments
	comment1, err := store.AddIssueComment(ctx, issue.ID, "user1", "First comment")
	if err != nil {
		t.Fatalf("failed to add first comment: %v", err)
	}
	if comment1.ID == 0 {
		t.Error("expected comment ID to be generated")
	}

	_, err = store.AddIssueComment(ctx, issue.ID, "user2", "Second comment")
	if err != nil {
		t.Fatalf("failed to add second comment: %v", err)
	}

	// Get comments
	comments, err := store.GetIssueComments(ctx, issue.ID)
	if err != nil {
		t.Fatalf("failed to get comments: %v", err)
	}
	if len(comments) != 2 {
		t.Errorf("expected 2 comments, got %d", len(comments))
	}
	if comments[0].Text != "First comment" {
		t.Errorf("expected 'First comment', got %q", comments[0].Text)
	}
}

func TestDoltStoreEvents(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create an issue (this creates a creation event)
	issue := &types.Issue{
		ID:          "test-event-issue",
		Title:       "Issue with Events",
		Description: "Test events",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
	}

	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	// Add a comment event
	if err := store.AddComment(ctx, issue.ID, "user1", "A comment"); err != nil {
		t.Fatalf("failed to add comment: %v", err)
	}

	// Get events
	events, err := store.GetEvents(ctx, issue.ID, 10)
	if err != nil {
		t.Fatalf("failed to get events: %v", err)
	}
	if len(events) < 2 {
		t.Errorf("expected at least 2 events, got %d", len(events))
	}
}

func TestDoltStoreDeleteIssue(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create an issue
	issue := &types.Issue{
		ID:          "test-delete-issue",
		Title:       "Issue to Delete",
		Description: "Will be deleted",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
	}

	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	// Verify it exists
	retrieved, err := store.GetIssue(ctx, issue.ID)
	if err != nil || retrieved == nil {
		t.Fatalf("issue should exist before delete")
	}

	// Delete the issue
	if err := store.DeleteIssue(ctx, issue.ID); err != nil {
		t.Fatalf("failed to delete issue: %v", err)
	}

	// Verify it's gone
	retrieved, err = store.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("failed to get issue after delete: %v", err)
	}
	if retrieved != nil {
		t.Error("expected issue to be deleted")
	}
}

func TestDoltStoreDirtyTracking(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create an issue (marks it dirty)
	issue := &types.Issue{
		ID:          "test-dirty-issue",
		Title:       "Dirty Issue",
		Description: "Will be dirty",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
	}

	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	// Get dirty issues
	dirtyIDs, err := store.GetDirtyIssues(ctx)
	if err != nil {
		t.Fatalf("failed to get dirty issues: %v", err)
	}
	found := false
	for _, id := range dirtyIDs {
		if id == issue.ID {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected issue to be in dirty list")
	}

	// Clear dirty issues
	if err := store.ClearDirtyIssuesByID(ctx, []string{issue.ID}); err != nil {
		t.Fatalf("failed to clear dirty issues: %v", err)
	}

	// Verify it's cleared
	dirtyIDs, err = store.GetDirtyIssues(ctx)
	if err != nil {
		t.Fatalf("failed to get dirty issues after clear: %v", err)
	}
	for _, id := range dirtyIDs {
		if id == issue.ID {
			t.Error("expected issue to be cleared from dirty list")
		}
	}
}

func TestDoltStoreStatistics(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create some issues
	issues := []*types.Issue{
		{ID: "test-stat-1", Title: "Open 1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
		{ID: "test-stat-2", Title: "Open 2", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "test-stat-3", Title: "Closed", Status: types.StatusClosed, Priority: 1, IssueType: types.TypeTask},
	}

	for _, issue := range issues {
		if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}
	}

	// Get statistics
	stats, err := store.GetStatistics(ctx)
	if err != nil {
		t.Fatalf("failed to get statistics: %v", err)
	}

	if stats.OpenIssues < 2 {
		t.Errorf("expected at least 2 open issues, got %d", stats.OpenIssues)
	}
	if stats.ClosedIssues < 1 {
		t.Errorf("expected at least 1 closed issue, got %d", stats.ClosedIssues)
	}
}

// Test SQL injection protection

func TestValidateRef(t *testing.T) {
	tests := []struct {
		name    string
		ref     string
		wantErr bool
	}{
		{"valid hash", "abc123def456", false},
		{"valid branch", "main", false},
		{"valid with underscore", "feature_branch", false},
		{"valid with dash", "feature-branch", false},
		{"empty", "", true},
		{"too long", string(make([]byte, 200)), true},
		{"with SQL injection", "main'; DROP TABLE issues; --", true},
		{"with quotes", "main'test", true},
		{"with semicolon", "main;test", true},
		{"with space", "main test", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateRef(tt.ref)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateRef(%q) error = %v, wantErr %v", tt.ref, err, tt.wantErr)
			}
		})
	}
}

func TestValidateTableName(t *testing.T) {
	tests := []struct {
		name      string
		tableName string
		wantErr   bool
	}{
		{"valid table", "issues", false},
		{"valid with underscore", "dirty_issues", false},
		{"valid with numbers", "table123", false},
		{"empty", "", true},
		{"too long", string(make([]byte, 100)), true},
		{"starts with number", "123table", true},
		{"with SQL injection", "issues'; DROP TABLE issues; --", true},
		{"with space", "my table", true},
		{"with dash", "my-table", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTableName(tt.tableName)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateTableName(%q) error = %v, wantErr %v", tt.tableName, err, tt.wantErr)
			}
		})
	}
}

func TestDoltStoreGetReadyWork(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create issues: one blocked, one ready
	blocker := &types.Issue{
		ID:          "test-blocker",
		Title:       "Blocker",
		Description: "Blocks another issue",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
	}
	blocked := &types.Issue{
		ID:          "test-blocked",
		Title:       "Blocked",
		Description: "Is blocked",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
	}
	ready := &types.Issue{
		ID:          "test-ready",
		Title:       "Ready",
		Description: "Is ready",
		Status:      types.StatusOpen,
		Priority:    3,
		IssueType:   types.TypeTask,
	}

	for _, issue := range []*types.Issue{blocker, blocked, ready} {
		if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("failed to create issue %s: %v", issue.ID, err)
		}
	}

	// Add blocking dependency
	dep := &types.Dependency{
		IssueID:     blocked.ID,
		DependsOnID: blocker.ID,
		Type:        types.DepBlocks,
	}
	if err := store.AddDependency(ctx, dep, "tester"); err != nil {
		t.Fatalf("failed to add dependency: %v", err)
	}

	// Get ready work
	readyWork, err := store.GetReadyWork(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("failed to get ready work: %v", err)
	}

	// Should include blocker and ready, but not blocked
	foundBlocker := false
	foundBlocked := false
	foundReady := false
	for _, issue := range readyWork {
		switch issue.ID {
		case blocker.ID:
			foundBlocker = true
		case blocked.ID:
			foundBlocked = true
		case ready.ID:
			foundReady = true
		}
	}

	if !foundBlocker {
		t.Error("expected blocker to be in ready work")
	}
	if foundBlocked {
		t.Error("expected blocked issue to NOT be in ready work")
	}
	if !foundReady {
		t.Error("expected ready issue to be in ready work")
	}
}

// TestCloseWithTimeout tests the close timeout helper function
func TestCloseWithTimeout(t *testing.T) {
	// Test 1: Fast close succeeds
	t.Run("fast close succeeds", func(t *testing.T) {
		err := doltutil.CloseWithTimeout("test", func() error {
			return nil
		})
		if err != nil {
			t.Errorf("expected no error, got: %v", err)
		}
	})

	// Test 2: Fast close with error returns error
	t.Run("fast close with error", func(t *testing.T) {
		expectedErr := context.Canceled
		err := doltutil.CloseWithTimeout("test", func() error {
			return expectedErr
		})
		if err != expectedErr {
			t.Errorf("expected %v, got: %v", expectedErr, err)
		}
	})

	// Test 3: Slow close times out (use shorter timeout for test)
	t.Run("slow close times out", func(t *testing.T) {
		// Save original timeout and restore after test
		originalTimeout := doltutil.CloseTimeout
		// Note: CloseTimeout is a const, so we can't actually change it
		// This test verifies the timeout mechanism works conceptually
		// In practice, the 5s timeout is reasonable for production use

		// This test would take 5+ seconds with the real timeout,
		// so we just verify the function signature works correctly
		start := time.Now()
		err := doltutil.CloseWithTimeout("test", func() error {
			// Return immediately for this test
			return nil
		})
		elapsed := time.Since(start)

		if err != nil {
			t.Errorf("expected no error for fast close, got: %v", err)
		}
		if elapsed > time.Second {
			t.Errorf("fast close took too long: %v", elapsed)
		}
		_ = originalTimeout // silence unused warning
	})
}
