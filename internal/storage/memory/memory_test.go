package memory

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/types"
)

func setupTestMemory(t *testing.T) *MemoryStorage {
	t.Helper()

	store := New("")
	ctx := context.Background()

	// Set issue_prefix config
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	// Configure Gas Town custom types for test compatibility (bd-find4)
	// These types are no longer built-in but many tests use them
	if err := store.SetConfig(ctx, "types.custom", "message,merge-request,molecule,gate,agent,role,rig,convoy,event,slot"); err != nil {
		t.Fatalf("failed to set types.custom: %v", err)
	}

	return store
}

func TestCreateIssue(t *testing.T) {
	store := setupTestMemory(t)
	defer store.Close()

	ctx := context.Background()
	issue := &types.Issue{
		Title:       "Test issue",
		Description: "Test description",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
	}

	err := store.CreateIssue(ctx, issue, "test-user")
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	if issue.ID == "" {
		t.Error("Issue ID should be set")
	}

	if !issue.CreatedAt.After(time.Time{}) {
		t.Error("CreatedAt should be set")
	}

	if !issue.UpdatedAt.After(time.Time{}) {
		t.Error("UpdatedAt should be set")
	}
}

func TestCreateIssueValidation(t *testing.T) {
	store := setupTestMemory(t)
	defer store.Close()

	ctx := context.Background()

	tests := []struct {
		name    string
		issue   *types.Issue
		wantErr bool
	}{
		{
			name: "valid issue",
			issue: &types.Issue{
				Title:     "Valid",
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: types.TypeTask,
			},
			wantErr: false,
		},
		{
			name: "missing title",
			issue: &types.Issue{
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: types.TypeTask,
			},
			wantErr: true,
		},
		{
			name: "invalid priority",
			issue: &types.Issue{
				Title:     "Test",
				Status:    types.StatusOpen,
				Priority:  10,
				IssueType: types.TypeTask,
			},
			wantErr: true,
		},
		{
			name: "invalid status",
			issue: &types.Issue{
				Title:     "Test",
				Status:    "invalid",
				Priority:  2,
				IssueType: types.TypeTask,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := store.CreateIssue(ctx, tt.issue, "test-user")
			if (err != nil) != tt.wantErr {
				t.Errorf("CreateIssue() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestGetIssue(t *testing.T) {
	store := setupTestMemory(t)
	defer store.Close()

	ctx := context.Background()
	original := &types.Issue{
		Title:              "Test issue",
		Description:        "Description",
		Design:             "Design notes",
		AcceptanceCriteria: "Acceptance",
		Notes:              "Notes",
		Status:             types.StatusOpen,
		Priority:           1,
		IssueType:          types.TypeFeature,
		Assignee:           "alice",
	}

	err := store.CreateIssue(ctx, original, "test-user")
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Retrieve the issue
	retrieved, err := store.GetIssue(ctx, original.ID)
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}

	if retrieved == nil {
		t.Fatal("GetIssue returned nil")
	}

	if retrieved.ID != original.ID {
		t.Errorf("ID mismatch: got %v, want %v", retrieved.ID, original.ID)
	}

	if retrieved.Title != original.Title {
		t.Errorf("Title mismatch: got %v, want %v", retrieved.Title, original.Title)
	}

	if retrieved.Description != original.Description {
		t.Errorf("Description mismatch: got %v, want %v", retrieved.Description, original.Description)
	}

	if retrieved.Assignee != original.Assignee {
		t.Errorf("Assignee mismatch: got %v, want %v", retrieved.Assignee, original.Assignee)
	}
}

func TestGetIssueNotFound(t *testing.T) {
	store := setupTestMemory(t)
	defer store.Close()

	ctx := context.Background()
	issue, err := store.GetIssue(ctx, "bd-999")
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}

	if issue != nil {
		t.Errorf("Expected nil for non-existent issue, got %v", issue)
	}
}

func TestCreateIssues(t *testing.T) {
	store := setupTestMemory(t)
	defer store.Close()

	ctx := context.Background()

	tests := []struct {
		name    string
		issues  []*types.Issue
		wantErr bool
	}{
		{
			name:    "empty batch",
			issues:  []*types.Issue{},
			wantErr: false,
		},
		{
			name: "single issue",
			issues: []*types.Issue{
				{Title: "Single issue", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
			},
			wantErr: false,
		},
		{
			name: "multiple issues",
			issues: []*types.Issue{
				{Title: "Issue 1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
				{Title: "Issue 2", Status: types.StatusInProgress, Priority: 2, IssueType: types.TypeBug},
				{Title: "Issue 3", Status: types.StatusOpen, Priority: 3, IssueType: types.TypeFeature},
			},
			wantErr: false,
		},
		{
			name: "validation error - missing title",
			issues: []*types.Issue{
				{Title: "Valid issue", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
				{Title: "", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
			},
			wantErr: true,
		},
		{
			name: "duplicate ID within batch error",
			issues: []*types.Issue{
				{ID: "dup-1", Title: "First", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
				{ID: "dup-1", Title: "Second", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create fresh storage for each test
			testStore := setupTestMemory(t)
			defer testStore.Close()

			err := testStore.CreateIssues(ctx, tt.issues, "test-user")
			if (err != nil) != tt.wantErr {
				t.Errorf("CreateIssues() error = %v, wantErr %v", err, tt.wantErr)
			}

			if !tt.wantErr && len(tt.issues) > 0 {
				// Verify all issues got IDs
				for i, issue := range tt.issues {
					if issue.ID == "" {
						t.Errorf("issue %d: ID should be set", i)
					}
					if !issue.CreatedAt.After(time.Time{}) {
						t.Errorf("issue %d: CreatedAt should be set", i)
					}
				}
			}
		})
	}
}

func TestUpdateIssue(t *testing.T) {
	store := setupTestMemory(t)
	defer store.Close()

	ctx := context.Background()

	// Create an issue
	issue := &types.Issue{
		Title:     "Original",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "test-user"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Update it
	updates := map[string]interface{}{
		"title":    "Updated",
		"priority": 1,
		"status":   string(types.StatusInProgress),
	}
	if err := store.UpdateIssue(ctx, issue.ID, updates, "test-user"); err != nil {
		t.Fatalf("UpdateIssue failed: %v", err)
	}

	// Retrieve and verify
	updated, err := store.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}

	if updated.Title != "Updated" {
		t.Errorf("Title not updated: got %v", updated.Title)
	}

	if updated.Priority != 1 {
		t.Errorf("Priority not updated: got %v", updated.Priority)
	}

	if updated.Status != types.StatusInProgress {
		t.Errorf("Status not updated: got %v", updated.Status)
	}
}

func TestCloseIssue(t *testing.T) {
	store := setupTestMemory(t)
	defer store.Close()

	ctx := context.Background()

	// Create an issue
	issue := &types.Issue{
		Title:     "Test",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "test-user"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Close it
	if err := store.CloseIssue(ctx, issue.ID, "Completed", "test-user", ""); err != nil {
		t.Fatalf("CloseIssue failed: %v", err)
	}

	// Verify
	closed, err := store.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}

	if closed.Status != types.StatusClosed {
		t.Errorf("Status should be closed, got %v", closed.Status)
	}

	if closed.ClosedAt == nil {
		t.Error("ClosedAt should be set")
	}
}

func TestSearchIssues(t *testing.T) {
	store := setupTestMemory(t)
	defer store.Close()

	ctx := context.Background()

	// Create test issues
	issues := []*types.Issue{
		{Title: "Bug fix", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeBug},
		{Title: "New feature", Status: types.StatusInProgress, Priority: 2, IssueType: types.TypeFeature},
		{Title: "Task", Status: types.StatusOpen, Priority: 3, IssueType: types.TypeTask},
	}

	for _, issue := range issues {
		if err := store.CreateIssue(ctx, issue, "test-user"); err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}
	}

	tests := []struct {
		name     string
		query    string
		filter   types.IssueFilter
		wantSize int
	}{
		{
			name:     "all issues",
			query:    "",
			filter:   types.IssueFilter{},
			wantSize: 3,
		},
		{
			name:     "search by title",
			query:    "feature",
			filter:   types.IssueFilter{},
			wantSize: 1,
		},
		{
			name:     "filter by status",
			query:    "",
			filter:   types.IssueFilter{Status: func() *types.Status { s := types.StatusOpen; return &s }()},
			wantSize: 2,
		},
		{
			name:     "filter by priority",
			query:    "",
			filter:   types.IssueFilter{Priority: func() *int { p := 1; return &p }()},
			wantSize: 1,
		},
		{
			name:     "filter by type",
			query:    "",
			filter:   types.IssueFilter{IssueType: func() *types.IssueType { t := types.TypeBug; return &t }()},
			wantSize: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results, err := store.SearchIssues(ctx, tt.query, tt.filter)
			if err != nil {
				t.Fatalf("SearchIssues failed: %v", err)
			}

			if len(results) != tt.wantSize {
				t.Errorf("Expected %d results, got %d", tt.wantSize, len(results))
			}
		})
	}
}

func TestDependencies(t *testing.T) {
	store := setupTestMemory(t)
	defer store.Close()

	ctx := context.Background()

	// Create two issues
	issue1 := &types.Issue{
		Title:     "Issue 1",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	issue2 := &types.Issue{
		Title:     "Issue 2",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}

	if err := store.CreateIssue(ctx, issue1, "test-user"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}
	if err := store.CreateIssue(ctx, issue2, "test-user"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Add dependency
	dep := &types.Dependency{
		IssueID:     issue1.ID,
		DependsOnID: issue2.ID,
		Type:        types.DepBlocks,
	}
	if err := store.AddDependency(ctx, dep, "test-user"); err != nil {
		t.Fatalf("AddDependency failed: %v", err)
	}

	// Get dependencies
	deps, err := store.GetDependencies(ctx, issue1.ID)
	if err != nil {
		t.Fatalf("GetDependencies failed: %v", err)
	}

	if len(deps) != 1 {
		t.Errorf("Expected 1 dependency, got %d", len(deps))
	}

	if deps[0].ID != issue2.ID {
		t.Errorf("Dependency mismatch: got %v", deps[0].ID)
	}

	// Get dependents
	dependents, err := store.GetDependents(ctx, issue2.ID)
	if err != nil {
		t.Fatalf("GetDependents failed: %v", err)
	}

	if len(dependents) != 1 {
		t.Errorf("Expected 1 dependent, got %d", len(dependents))
	}

	// Remove dependency
	if err := store.RemoveDependency(ctx, issue1.ID, issue2.ID, "test-user"); err != nil {
		t.Fatalf("RemoveDependency failed: %v", err)
	}

	// Verify removed
	deps, err = store.GetDependencies(ctx, issue1.ID)
	if err != nil {
		t.Fatalf("GetDependencies failed: %v", err)
	}

	if len(deps) != 0 {
		t.Errorf("Expected 0 dependencies after removal, got %d", len(deps))
	}
}

func TestLabels(t *testing.T) {
	store := setupTestMemory(t)
	defer store.Close()

	ctx := context.Background()

	// Create an issue
	issue := &types.Issue{
		Title:     "Test",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "test-user"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Add labels
	if err := store.AddLabel(ctx, issue.ID, "bug", "test-user"); err != nil {
		t.Fatalf("AddLabel failed: %v", err)
	}
	if err := store.AddLabel(ctx, issue.ID, "critical", "test-user"); err != nil {
		t.Fatalf("AddLabel failed: %v", err)
	}

	// Get labels
	labels, err := store.GetLabels(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetLabels failed: %v", err)
	}

	if len(labels) != 2 {
		t.Errorf("Expected 2 labels, got %d", len(labels))
	}

	// Remove label
	if err := store.RemoveLabel(ctx, issue.ID, "bug", "test-user"); err != nil {
		t.Fatalf("RemoveLabel failed: %v", err)
	}

	// Verify
	labels, err = store.GetLabels(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetLabels failed: %v", err)
	}

	if len(labels) != 1 {
		t.Errorf("Expected 1 label after removal, got %d", len(labels))
	}
}

func TestComments(t *testing.T) {
	store := setupTestMemory(t)
	defer store.Close()

	ctx := context.Background()

	// Create an issue
	issue := &types.Issue{
		Title:     "Test",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "test-user"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Add comment
	comment, err := store.AddIssueComment(ctx, issue.ID, "alice", "First comment")
	if err != nil {
		t.Fatalf("AddIssueComment failed: %v", err)
	}

	if comment == nil {
		t.Fatal("Comment should not be nil")
	}

	// Get comments
	comments, err := store.GetIssueComments(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetIssueComments failed: %v", err)
	}

	if len(comments) != 1 {
		t.Errorf("Expected 1 comment, got %d", len(comments))
	}

	if comments[0].Text != "First comment" {
		t.Errorf("Comment text mismatch: got %v", comments[0].Text)
	}
}

func TestLoadFromIssues(t *testing.T) {
	store := New("")
	defer store.Close()

	issues := []*types.Issue{
		{
			ID:           "bd-1",
			Title:        "Issue 1",
			Status:       types.StatusOpen,
			Priority:     1,
			IssueType:    types.TypeTask,
			Labels:       []string{"bug", "critical"},
			Dependencies: []*types.Dependency{{IssueID: "bd-1", DependsOnID: "bd-2", Type: types.DepBlocks}},
		},
		{
			ID:        "bd-2",
			Title:     "Issue 2",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
		},
	}

	if err := store.LoadFromIssues(issues); err != nil {
		t.Fatalf("LoadFromIssues failed: %v", err)
	}

	// Verify issues loaded
	ctx := context.Background()
	loaded, err := store.GetIssue(ctx, "bd-1")
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}

	if loaded == nil {
		t.Fatal("Issue should be loaded")
	}

	if loaded.Title != "Issue 1" {
		t.Errorf("Title mismatch: got %v", loaded.Title)
	}

	// Verify labels loaded
	if len(loaded.Labels) != 2 {
		t.Errorf("Expected 2 labels, got %d", len(loaded.Labels))
	}

	// Verify dependencies loaded
	if len(loaded.Dependencies) != 1 {
		t.Errorf("Expected 1 dependency, got %d", len(loaded.Dependencies))
	}

	// Verify counter updated
	if store.counters["bd"] != 2 {
		t.Errorf("Expected counter bd=2, got %d", store.counters["bd"])
	}
}

func TestGetAllIssues(t *testing.T) {
	store := setupTestMemory(t)
	defer store.Close()

	ctx := context.Background()

	// Create issues
	for i := 1; i <= 3; i++ {
		issue := &types.Issue{
			Title:     "Issue",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issue, "test-user"); err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}
	}

	// Get all
	all := store.GetAllIssues()
	if len(all) != 3 {
		t.Errorf("Expected 3 issues, got %d", len(all))
	}

	// Verify sorted by ID
	for i := 1; i < len(all); i++ {
		if all[i-1].ID >= all[i].ID {
			t.Error("Issues should be sorted by ID")
		}
	}
}

func TestStatistics(t *testing.T) {
	store := setupTestMemory(t)
	defer store.Close()

	ctx := context.Background()

	// Create issues with different statuses
	issues := []*types.Issue{
		{Title: "Open 1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
		{Title: "Open 2", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
		{Title: "In Progress", Status: types.StatusInProgress, Priority: 1, IssueType: types.TypeTask},
		{Title: "Closed", Status: types.StatusClosed, Priority: 1, IssueType: types.TypeTask, ClosedAt: func() *time.Time { t := time.Now(); return &t }()},
	}

	for _, issue := range issues {
		if err := store.CreateIssue(ctx, issue, "test-user"); err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}
		// Close the one marked as closed
		if issue.Status == types.StatusClosed {
			if err := store.CloseIssue(ctx, issue.ID, "Done", "test-user", ""); err != nil {
				t.Fatalf("CloseIssue failed: %v", err)
			}
		}
	}

	stats, err := store.GetStatistics(ctx)
	if err != nil {
		t.Fatalf("GetStatistics failed: %v", err)
	}

	if stats.TotalIssues != 4 {
		t.Errorf("Expected 4 total issues, got %d", stats.TotalIssues)
	}
	if stats.OpenIssues != 2 {
		t.Errorf("Expected 2 open issues, got %d", stats.OpenIssues)
	}
	if stats.InProgressIssues != 1 {
		t.Errorf("Expected 1 in-progress issue, got %d", stats.InProgressIssues)
	}
	if stats.ClosedIssues != 1 {
		t.Errorf("Expected 1 closed issue, got %d", stats.ClosedIssues)
	}
}

func TestStatistics_BlockedAndReadyCounts(t *testing.T) {
	store := setupTestMemory(t)
	defer store.Close()

	ctx := context.Background()
	closedAt := time.Now()

	// Create issues:
	// - blocker: open issue that blocks others
	// - blocked1: open issue blocked by blocker
	// - blocked2: in_progress issue blocked by blocker
	// - ready1: open issue with no blockers
	// - ready2: open issue "blocked" by a closed issue (should be ready)
	// - closedBlocker: closed issue that doesn't block
	blocker := &types.Issue{Title: "Blocker", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	blocked1 := &types.Issue{Title: "Blocked Open", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	blocked2 := &types.Issue{Title: "Blocked InProgress", Status: types.StatusInProgress, Priority: 1, IssueType: types.TypeTask}
	ready1 := &types.Issue{Title: "Ready 1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	ready2 := &types.Issue{Title: "Ready 2 (closed blocker)", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	closedBlocker := &types.Issue{Title: "Closed Blocker", Status: types.StatusClosed, Priority: 1, IssueType: types.TypeTask, ClosedAt: &closedAt}

	for _, issue := range []*types.Issue{blocker, blocked1, blocked2, ready1, ready2, closedBlocker} {
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}
	}

	// Close the closedBlocker properly
	if err := store.CloseIssue(ctx, closedBlocker.ID, "Done", "test", ""); err != nil {
		t.Fatalf("CloseIssue failed: %v", err)
	}

	// Add blocking dependencies
	// blocked1 is blocked by blocker (open)
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     blocked1.ID,
		DependsOnID: blocker.ID,
		Type:        types.DepBlocks,
		CreatedAt:   time.Now(),
		CreatedBy:   "test",
	}, "test"); err != nil {
		t.Fatalf("AddDependency failed: %v", err)
	}

	// blocked2 is blocked by blocker (open)
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     blocked2.ID,
		DependsOnID: blocker.ID,
		Type:        types.DepBlocks,
		CreatedAt:   time.Now(),
		CreatedBy:   "test",
	}, "test"); err != nil {
		t.Fatalf("AddDependency failed: %v", err)
	}

	// ready2 is "blocked" by closedBlocker (closed, so doesn't actually block)
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     ready2.ID,
		DependsOnID: closedBlocker.ID,
		Type:        types.DepBlocks,
		CreatedAt:   time.Now(),
		CreatedBy:   "test",
	}, "test"); err != nil {
		t.Fatalf("AddDependency failed: %v", err)
	}

	stats, err := store.GetStatistics(ctx)
	if err != nil {
		t.Fatalf("GetStatistics failed: %v", err)
	}

	// Expected:
	// - BlockedIssues: 2 (blocked1 and blocked2)
	// - ReadyIssues: 3 (blocker, ready1, ready2 - all open with no open blockers)
	if stats.BlockedIssues != 2 {
		t.Errorf("Expected 2 blocked issues, got %d", stats.BlockedIssues)
	}
	if stats.ReadyIssues != 3 {
		t.Errorf("Expected 3 ready issues, got %d", stats.ReadyIssues)
	}

	// Verify other counts are correct
	if stats.TotalIssues != 6 {
		t.Errorf("Expected 6 total issues, got %d", stats.TotalIssues)
	}
	if stats.OpenIssues != 4 {
		t.Errorf("Expected 4 open issues, got %d", stats.OpenIssues)
	}
	if stats.InProgressIssues != 1 {
		t.Errorf("Expected 1 in-progress issue, got %d", stats.InProgressIssues)
	}
	if stats.ClosedIssues != 1 {
		t.Errorf("Expected 1 closed issue, got %d", stats.ClosedIssues)
	}
}

func TestStatistics_EpicsEligibleForClosure(t *testing.T) {
	store := setupTestMemory(t)
	defer store.Close()

	ctx := context.Background()
	closedAt := time.Now()

	// Create an epic with two children, both closed
	epic1 := &types.Issue{Title: "Epic 1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeEpic}
	child1 := &types.Issue{Title: "Child 1", Status: types.StatusClosed, Priority: 1, IssueType: types.TypeTask, ClosedAt: &closedAt}
	child2 := &types.Issue{Title: "Child 2", Status: types.StatusClosed, Priority: 1, IssueType: types.TypeTask, ClosedAt: &closedAt}

	// Create an epic with one open child (not eligible)
	epic2 := &types.Issue{Title: "Epic 2", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeEpic}
	child3 := &types.Issue{Title: "Child 3", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

	for _, issue := range []*types.Issue{epic1, child1, child2, epic2, child3} {
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}
	}

	// Close the children properly
	for _, child := range []*types.Issue{child1, child2} {
		if err := store.CloseIssue(ctx, child.ID, "Done", "test", ""); err != nil {
			t.Fatalf("CloseIssue failed: %v", err)
		}
	}

	// Add parent-child dependencies
	for _, dep := range []*types.Dependency{
		{IssueID: child1.ID, DependsOnID: epic1.ID, Type: types.DepParentChild, CreatedAt: time.Now(), CreatedBy: "test"},
		{IssueID: child2.ID, DependsOnID: epic1.ID, Type: types.DepParentChild, CreatedAt: time.Now(), CreatedBy: "test"},
		{IssueID: child3.ID, DependsOnID: epic2.ID, Type: types.DepParentChild, CreatedAt: time.Now(), CreatedBy: "test"},
	} {
		if err := store.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatalf("AddDependency failed: %v", err)
		}
	}

	stats, err := store.GetStatistics(ctx)
	if err != nil {
		t.Fatalf("GetStatistics failed: %v", err)
	}

	// Only epic1 should be eligible (all children closed)
	if stats.EpicsEligibleForClosure != 1 {
		t.Errorf("Expected 1 epic eligible for closure, got %d", stats.EpicsEligibleForClosure)
	}
}

func TestStatistics_DeletedExcludedFromTotal(t *testing.T) {
	store := setupTestMemory(t)
	defer store.Close()

	ctx := context.Background()
	now := time.Now()

	// Create 2 regular issues
	issues := []*types.Issue{
		{Title: "Open Issue", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
		{Title: "Closed Issue", Status: types.StatusClosed, Priority: 1, IssueType: types.TypeTask, ClosedAt: &now},
	}

	for _, issue := range issues {
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}
	}

	// Close the closed issue properly
	if err := store.CloseIssue(ctx, issues[1].ID, "Done", "test", ""); err != nil {
		t.Fatalf("CloseIssue failed: %v", err)
	}

	stats, err := store.GetStatistics(ctx)
	if err != nil {
		t.Fatalf("GetStatistics failed: %v", err)
	}

	if stats.TotalIssues != 2 {
		t.Errorf("Expected 2 total issues, got %d", stats.TotalIssues)
	}
	if stats.OpenIssues != 1 {
		t.Errorf("Expected 1 open issue, got %d", stats.OpenIssues)
	}
	if stats.ClosedIssues != 1 {
		t.Errorf("Expected 1 closed issue, got %d", stats.ClosedIssues)
	}
}

func TestDeleteIssueMemory(t *testing.T) {
	store := setupTestMemory(t)
	defer store.Close()

	ctx := context.Background()

	// Create an issue
	issue := &types.Issue{
		Title:     "Test Issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}
	issueID := issue.ID

	// Delete the issue
	if err := store.DeleteIssue(ctx, issueID); err != nil {
		t.Fatalf("DeleteIssue failed: %v", err)
	}

	// Verify the issue is gone
	deleted, err := store.GetIssue(ctx, issueID)
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}
	if deleted != nil {
		t.Errorf("Expected issue to be deleted, but got %+v", deleted)
	}
}

func TestDeleteIssueMemory_NotFound(t *testing.T) {
	store := setupTestMemory(t)
	defer store.Close()

	ctx := context.Background()

	// Try to delete a non-existent issue
	err := store.DeleteIssue(ctx, "nonexistent")
	if err == nil {
		t.Fatal("Expected error for non-existent issue")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("Expected 'not found' error, got: %v", err)
	}
}

func TestConfigOperations(t *testing.T) {
	store := setupTestMemory(t)
	defer store.Close()

	ctx := context.Background()

	// Set config
	if err := store.SetConfig(ctx, "test_key", "test_value"); err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}

	// Get config
	value, err := store.GetConfig(ctx, "test_key")
	if err != nil {
		t.Fatalf("GetConfig failed: %v", err)
	}

	if value != "test_value" {
		t.Errorf("Expected test_value, got %v", value)
	}

	// Get all config
	allConfig, err := store.GetAllConfig(ctx)
	if err != nil {
		t.Fatalf("GetAllConfig failed: %v", err)
	}

	if len(allConfig) < 1 {
		t.Error("Expected at least 1 config entry")
	}

	// Delete config
	if err := store.DeleteConfig(ctx, "test_key"); err != nil {
		t.Fatalf("DeleteConfig failed: %v", err)
	}

	value, err = store.GetConfig(ctx, "test_key")
	if err != nil {
		t.Fatalf("GetConfig failed: %v", err)
	}

	if value != "" {
		t.Errorf("Expected empty value after delete, got %v", value)
	}
}

func TestMetadataOperations(t *testing.T) {
	store := setupTestMemory(t)
	defer store.Close()

	ctx := context.Background()

	// Set metadata
	if err := store.SetMetadata(ctx, "hash", "abc123"); err != nil {
		t.Fatalf("SetMetadata failed: %v", err)
	}

	// Get metadata
	value, err := store.GetMetadata(ctx, "hash")
	if err != nil {
		t.Fatalf("GetMetadata failed: %v", err)
	}

	if value != "abc123" {
		t.Errorf("Expected abc123, got %v", value)
	}
}

func TestThreadSafety(t *testing.T) {
	store := setupTestMemory(t)
	defer store.Close()

	ctx := context.Background()
	const numGoroutines = 10

	// Run concurrent creates
	done := make(chan bool)
	for i := 0; i < numGoroutines; i++ {
		go func(n int) {
			issue := &types.Issue{
				Title:     "Concurrent",
				Status:    types.StatusOpen,
				Priority:  1,
				IssueType: types.TypeTask,
			}
			store.CreateIssue(ctx, issue, "test-user")
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < numGoroutines; i++ {
		<-done
	}

	// Verify all created
	stats, err := store.GetStatistics(ctx)
	if err != nil {
		t.Fatalf("GetStatistics failed: %v", err)
	}

	if stats.TotalIssues != numGoroutines {
		t.Errorf("Expected %d issues, got %d", numGoroutines, stats.TotalIssues)
	}
}

func TestClose(t *testing.T) {
	store := setupTestMemory(t)

	if store.closed {
		t.Error("Store should not be closed initially")
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	if !store.closed {
		t.Error("Store should be closed")
	}
}

func TestGetIssueByExternalRef(t *testing.T) {
	store := setupTestMemory(t)
	defer store.Close()

	ctx := context.Background()

	// Create an issue with external ref
	extRef := "github#123"
	issue := &types.Issue{
		Title:       "Test issue with external ref",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
		ExternalRef: &extRef,
	}

	if err := store.CreateIssue(ctx, issue, "test-user"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Lookup by external ref should find it
	found, err := store.GetIssueByExternalRef(ctx, "github#123")
	if err != nil {
		t.Fatalf("GetIssueByExternalRef failed: %v", err)
	}
	if found == nil {
		t.Fatal("Expected to find issue by external ref")
	}
	if found.ID != issue.ID {
		t.Errorf("Expected issue ID %s, got %s", issue.ID, found.ID)
	}

	// Lookup by non-existent ref should return nil
	notFound, err := store.GetIssueByExternalRef(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("GetIssueByExternalRef failed: %v", err)
	}
	if notFound != nil {
		t.Error("Expected nil for non-existent external ref")
	}

	// Update external ref and verify index is updated
	newRef := "github#456"
	if err := store.UpdateIssue(ctx, issue.ID, map[string]interface{}{
		"external_ref": newRef,
	}, "test-user"); err != nil {
		t.Fatalf("UpdateIssue failed: %v", err)
	}

	// Old ref should not find anything
	oldRefResult, err := store.GetIssueByExternalRef(ctx, "github#123")
	if err != nil {
		t.Fatalf("GetIssueByExternalRef failed: %v", err)
	}
	if oldRefResult != nil {
		t.Error("Old external ref should not find issue after update")
	}

	// New ref should find the issue
	newRefResult, err := store.GetIssueByExternalRef(ctx, "github#456")
	if err != nil {
		t.Fatalf("GetIssueByExternalRef failed: %v", err)
	}
	if newRefResult == nil {
		t.Fatal("New external ref should find issue")
	}
	if newRefResult.ID != issue.ID {
		t.Errorf("Expected issue ID %s, got %s", issue.ID, newRefResult.ID)
	}

	// Delete issue and verify index is cleaned up
	if err := store.DeleteIssue(ctx, issue.ID); err != nil {
		t.Fatalf("DeleteIssue failed: %v", err)
	}

	// External ref should not find anything after delete
	deletedResult, err := store.GetIssueByExternalRef(ctx, "github#456")
	if err != nil {
		t.Fatalf("GetIssueByExternalRef failed: %v", err)
	}
	if deletedResult != nil {
		t.Error("External ref should not find issue after delete")
	}
}

func TestGetIssueByExternalRefLoadFromIssues(t *testing.T) {
	store := New("")
	defer store.Close()

	ctx := context.Background()

	// Load issues with external refs
	extRef1 := "jira#100"
	extRef2 := "jira#200"
	issues := []*types.Issue{
		{
			ID:          "bd-1",
			Title:       "Issue 1",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeTask,
			ExternalRef: &extRef1,
		},
		{
			ID:          "bd-2",
			Title:       "Issue 2",
			Status:      types.StatusOpen,
			Priority:    2,
			IssueType:   types.TypeBug,
			ExternalRef: &extRef2,
		},
		{
			ID:        "bd-3",
			Title:     "Issue 3 (no external ref)",
			Status:    types.StatusOpen,
			Priority:  3,
			IssueType: types.TypeFeature,
		},
	}

	if err := store.LoadFromIssues(issues); err != nil {
		t.Fatalf("LoadFromIssues failed: %v", err)
	}

	// Both external refs should be indexed
	found1, err := store.GetIssueByExternalRef(ctx, "jira#100")
	if err != nil {
		t.Fatalf("GetIssueByExternalRef failed: %v", err)
	}
	if found1 == nil || found1.ID != "bd-1" {
		t.Errorf("Expected to find bd-1 by external ref jira#100")
	}

	found2, err := store.GetIssueByExternalRef(ctx, "jira#200")
	if err != nil {
		t.Fatalf("GetIssueByExternalRef failed: %v", err)
	}
	if found2 == nil || found2.ID != "bd-2" {
		t.Errorf("Expected to find bd-2 by external ref jira#200")
	}
}

// TestGetNextChildID_ConfigurableMaxDepth tests that hierarchy.max-depth config is respected (GH#995)
func TestGetNextChildID_ConfigurableMaxDepth(t *testing.T) {
	// Initialize config for testing
	if err := config.Initialize(); err != nil {
		t.Fatalf("failed to initialize config: %v", err)
	}

	// Ensure config is reset even if test fails or panics
	t.Cleanup(func() {
		config.Set("hierarchy.max-depth", 3)
	})

	store := setupTestMemory(t)
	defer store.Close()
	ctx := context.Background()

	// Create a chain of issues up to depth 3
	issues := []struct {
		id    string
		title string
	}{
		{"bd-depth", "Root"},
		{"bd-depth.1", "Level 1"},
		{"bd-depth.1.1", "Level 2"},
		{"bd-depth.1.1.1", "Level 3"},
	}

	for _, issue := range issues {
		iss := &types.Issue{
			ID:          issue.id,
			Title:       issue.title,
			Description: "Test issue",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeTask,
		}
		if err := store.CreateIssue(ctx, iss, "test"); err != nil {
			t.Fatalf("failed to create issue %s: %v", issue.id, err)
		}
	}

	// Test 1: With default max-depth (3), depth 4 should fail
	config.Set("hierarchy.max-depth", 3)
	_, err := store.GetNextChildID(ctx, "bd-depth.1.1.1")
	if err == nil {
		t.Errorf("expected error for depth 4 with max-depth=3, got nil")
	}
	if err != nil && err.Error() != "maximum hierarchy depth (3) exceeded for parent bd-depth.1.1.1" {
		t.Errorf("unexpected error message: %v", err)
	}

	// Test 2: With max-depth=5, depth 4 should succeed
	config.Set("hierarchy.max-depth", 5)
	childID, err := store.GetNextChildID(ctx, "bd-depth.1.1.1")
	if err != nil {
		t.Errorf("depth 4 should be allowed with max-depth=5, got error: %v", err)
	}
	expectedID := "bd-depth.1.1.1.1"
	if childID != expectedID {
		t.Errorf("expected %s, got %s", expectedID, childID)
	}

	// Test 3: With max-depth=2, depth 3 should fail
	config.Set("hierarchy.max-depth", 2)
	_, err = store.GetNextChildID(ctx, "bd-depth.1.1")
	if err == nil {
		t.Errorf("expected error for depth 3 with max-depth=2, got nil")
	}
	if err != nil && err.Error() != "maximum hierarchy depth (2) exceeded for parent bd-depth.1.1" {
		t.Errorf("unexpected error message: %v", err)
	}
}
