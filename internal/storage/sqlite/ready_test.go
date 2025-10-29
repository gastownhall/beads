package sqlite

import (
	"context"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestGetReadyWork(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create issues:
	// bd-1: open, no dependencies → READY
	// bd-2: open, depends on bd-1 (open) → BLOCKED
	// bd-3: open, no dependencies → READY
	// bd-4: closed, no dependencies → NOT READY (closed)
	// bd-5: open, depends on bd-4 (closed) → READY (blocker is closed)

	issue1 := &types.Issue{Title: "Ready 1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issue2 := &types.Issue{Title: "Blocked", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issue3 := &types.Issue{Title: "Ready 2", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	issue4 := &types.Issue{Title: "Closed", Status: types.StatusClosed, Priority: 1, IssueType: types.TypeTask}
	issue5 := &types.Issue{Title: "Ready 3", Status: types.StatusOpen, Priority: 0, IssueType: types.TypeTask}

	store.CreateIssue(ctx, issue1, "test-user")
	store.CreateIssue(ctx, issue2, "test-user")
	store.CreateIssue(ctx, issue3, "test-user")
	store.CreateIssue(ctx, issue4, "test-user")
	store.CloseIssue(ctx, issue4.ID, "Done", "test-user")
	store.CreateIssue(ctx, issue5, "test-user")

	// Add dependencies
	store.AddDependency(ctx, &types.Dependency{IssueID: issue2.ID, DependsOnID: issue1.ID, Type: types.DepBlocks}, "test-user")
	store.AddDependency(ctx, &types.Dependency{IssueID: issue5.ID, DependsOnID: issue4.ID, Type: types.DepBlocks}, "test-user")

	// Get ready work
	ready, err := store.GetReadyWork(ctx, types.WorkFilter{Status: types.StatusOpen})
	if err != nil {
		t.Fatalf("GetReadyWork failed: %v", err)
	}

	// Should have 3 ready issues: bd-1, bd-3, bd-5
	if len(ready) != 3 {
		t.Fatalf("Expected 3 ready issues, got %d", len(ready))
	}

	// Verify ready issues
	readyIDs := make(map[string]bool)
	for _, issue := range ready {
		readyIDs[issue.ID] = true
	}

	if !readyIDs[issue1.ID] {
		t.Errorf("Expected %s to be ready", issue1.ID)
	}
	if !readyIDs[issue3.ID] {
		t.Errorf("Expected %s to be ready", issue3.ID)
	}
	if !readyIDs[issue5.ID] {
		t.Errorf("Expected %s to be ready", issue5.ID)
	}
	if readyIDs[issue2.ID] {
		t.Errorf("Expected %s to be blocked, but it was ready", issue2.ID)
	}
}

func TestGetReadyWorkPriorityOrder(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create issues with different priorities
	issueP0 := &types.Issue{Title: "Highest", Status: types.StatusOpen, Priority: 0, IssueType: types.TypeTask}
	issueP2 := &types.Issue{Title: "Medium", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	issueP1 := &types.Issue{Title: "High", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

	store.CreateIssue(ctx, issueP2, "test-user")
	store.CreateIssue(ctx, issueP0, "test-user")
	store.CreateIssue(ctx, issueP1, "test-user")

	// Get ready work
	ready, err := store.GetReadyWork(ctx, types.WorkFilter{Status: types.StatusOpen})
	if err != nil {
		t.Fatalf("GetReadyWork failed: %v", err)
	}

	if len(ready) != 3 {
		t.Fatalf("Expected 3 ready issues, got %d", len(ready))
	}

	// Verify priority ordering (P0 first, then P1, then P2)
	if ready[0].Priority != 0 {
		t.Errorf("Expected first issue to be P0, got P%d", ready[0].Priority)
	}
	if ready[1].Priority != 1 {
		t.Errorf("Expected second issue to be P1, got P%d", ready[1].Priority)
	}
	if ready[2].Priority != 2 {
		t.Errorf("Expected third issue to be P2, got P%d", ready[2].Priority)
	}
}

func TestGetReadyWorkWithPriorityFilter(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create issues with different priorities
	issueP0 := &types.Issue{Title: "P0", Status: types.StatusOpen, Priority: 0, IssueType: types.TypeTask}
	issueP1 := &types.Issue{Title: "P1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issueP2 := &types.Issue{Title: "P2", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}

	store.CreateIssue(ctx, issueP0, "test-user")
	store.CreateIssue(ctx, issueP1, "test-user")
	store.CreateIssue(ctx, issueP2, "test-user")

	// Filter for P0 only
	priority0 := 0
	ready, err := store.GetReadyWork(ctx, types.WorkFilter{Status: types.StatusOpen, Priority: &priority0})
	if err != nil {
		t.Fatalf("GetReadyWork failed: %v", err)
	}

	if len(ready) != 1 {
		t.Fatalf("Expected 1 P0 issue, got %d", len(ready))
	}

	if ready[0].Priority != 0 {
		t.Errorf("Expected P0 issue, got P%d", ready[0].Priority)
	}
}

func TestGetReadyWorkWithAssigneeFilter(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create issues with different assignees
	issueAlice := &types.Issue{Title: "Alice's task", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, Assignee: "alice"}
	issueBob := &types.Issue{Title: "Bob's task", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, Assignee: "bob"}
	issueUnassigned := &types.Issue{Title: "Unassigned", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

	store.CreateIssue(ctx, issueAlice, "test-user")
	store.CreateIssue(ctx, issueBob, "test-user")
	store.CreateIssue(ctx, issueUnassigned, "test-user")

	// Filter for alice
	assignee := "alice"
	ready, err := store.GetReadyWork(ctx, types.WorkFilter{Status: types.StatusOpen, Assignee: &assignee})
	if err != nil {
		t.Fatalf("GetReadyWork failed: %v", err)
	}

	if len(ready) != 1 {
		t.Fatalf("Expected 1 issue for alice, got %d", len(ready))
	}

	if ready[0].Assignee != "alice" {
		t.Errorf("Expected alice's issue, got %s", ready[0].Assignee)
	}
}

func TestGetReadyWorkWithLimit(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create 5 ready issues
	for i := 0; i < 5; i++ {
		issue := &types.Issue{Title: "Task", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		store.CreateIssue(ctx, issue, "test-user")
	}

	// Limit to 3
	ready, err := store.GetReadyWork(ctx, types.WorkFilter{Status: types.StatusOpen, Limit: 3})
	if err != nil {
		t.Fatalf("GetReadyWork failed: %v", err)
	}

	if len(ready) != 3 {
		t.Errorf("Expected 3 issues (limit), got %d", len(ready))
	}
}

func TestGetReadyWorkIgnoresRelatedDeps(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create two issues with "related" dependency (should not block)
	issue1 := &types.Issue{Title: "First", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issue2 := &types.Issue{Title: "Second", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

	store.CreateIssue(ctx, issue1, "test-user")
	store.CreateIssue(ctx, issue2, "test-user")

	// Add "related" dependency (not blocking)
	store.AddDependency(ctx, &types.Dependency{IssueID: issue2.ID, DependsOnID: issue1.ID, Type: types.DepRelated}, "test-user")

	// Both should be ready (related deps don't block)
	ready, err := store.GetReadyWork(ctx, types.WorkFilter{Status: types.StatusOpen})
	if err != nil {
		t.Fatalf("GetReadyWork failed: %v", err)
	}

	if len(ready) != 2 {
		t.Fatalf("Expected 2 ready issues (related deps don't block), got %d", len(ready))
	}
}

func TestGetBlockedIssues(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create issues:
	// bd-1: open, no dependencies → not blocked
	// bd-2: open, depends on bd-1 (open) → blocked by bd-1
	// bd-3: open, depends on bd-1 and bd-2 (both open) → blocked by 2 issues

	issue1 := &types.Issue{Title: "Foundation", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issue2 := &types.Issue{Title: "Blocked by 1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issue3 := &types.Issue{Title: "Blocked by 2", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

	store.CreateIssue(ctx, issue1, "test-user")
	store.CreateIssue(ctx, issue2, "test-user")
	store.CreateIssue(ctx, issue3, "test-user")

	store.AddDependency(ctx, &types.Dependency{IssueID: issue2.ID, DependsOnID: issue1.ID, Type: types.DepBlocks}, "test-user")
	store.AddDependency(ctx, &types.Dependency{IssueID: issue3.ID, DependsOnID: issue1.ID, Type: types.DepBlocks}, "test-user")
	store.AddDependency(ctx, &types.Dependency{IssueID: issue3.ID, DependsOnID: issue2.ID, Type: types.DepBlocks}, "test-user")

	// Get blocked issues
	blocked, err := store.GetBlockedIssues(ctx)
	if err != nil {
		t.Fatalf("GetBlockedIssues failed: %v", err)
	}

	if len(blocked) != 2 {
		t.Fatalf("Expected 2 blocked issues, got %d", len(blocked))
	}

	// Find issue3 in blocked list
	var issue3Blocked *types.BlockedIssue
	for i := range blocked {
		if blocked[i].ID == issue3.ID {
			issue3Blocked = blocked[i]
			break
		}
	}

	if issue3Blocked == nil {
		t.Fatal("Expected issue3 to be in blocked list")
	}

	if issue3Blocked.BlockedByCount != 2 {
		t.Errorf("Expected issue3 to be blocked by 2 issues, got %d", issue3Blocked.BlockedByCount)
	}

	// Verify the blockers are correct
	if len(issue3Blocked.BlockedBy) != 2 {
		t.Errorf("Expected 2 blocker IDs, got %d", len(issue3Blocked.BlockedBy))
	}
}

// TestParentBlockerBlocksChildren tests that children inherit blockage from parents
func TestParentBlockerBlocksChildren(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create:
	// blocker: open
	// epic1: open, blocked by 'blocker'
	// task1: open, child of epic1 (via parent-child)
	//
	// Expected: task1 should NOT be ready (parent is blocked)

	blocker := &types.Issue{Title: "Blocker", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	epic1 := &types.Issue{Title: "Epic 1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeEpic}
	task1 := &types.Issue{Title: "Task 1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

	store.CreateIssue(ctx, blocker, "test-user")
	store.CreateIssue(ctx, epic1, "test-user")
	store.CreateIssue(ctx, task1, "test-user")

	// epic1 blocked by blocker
	store.AddDependency(ctx, &types.Dependency{IssueID: epic1.ID, DependsOnID: blocker.ID, Type: types.DepBlocks}, "test-user")
	// task1 is child of epic1
	store.AddDependency(ctx, &types.Dependency{IssueID: task1.ID, DependsOnID: epic1.ID, Type: types.DepParentChild}, "test-user")

	// Get ready work
	ready, err := store.GetReadyWork(ctx, types.WorkFilter{Status: types.StatusOpen})
	if err != nil {
		t.Fatalf("GetReadyWork failed: %v", err)
	}

	// Should have only blocker ready
	readyIDs := make(map[string]bool)
	for _, issue := range ready {
		readyIDs[issue.ID] = true
	}

	if readyIDs[epic1.ID] {
		t.Errorf("Expected epic1 to be blocked, but it was ready")
	}
	if readyIDs[task1.ID] {
		t.Errorf("Expected task1 to be blocked (parent is blocked), but it was ready")
	}
	if !readyIDs[blocker.ID] {
		t.Errorf("Expected blocker to be ready")
	}
}

// TestGrandparentBlockerBlocksGrandchildren tests multi-level propagation
func TestGrandparentBlockerBlocksGrandchildren(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create:
	// blocker: open
	// epic1: open, blocked by 'blocker'
	// epic2: open, child of epic1
	// task1: open, child of epic2
	//
	// Expected: task1 should NOT be ready (grandparent is blocked)

	blocker := &types.Issue{Title: "Blocker", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	epic1 := &types.Issue{Title: "Epic 1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeEpic}
	epic2 := &types.Issue{Title: "Epic 2", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeEpic}
	task1 := &types.Issue{Title: "Task 1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

	store.CreateIssue(ctx, blocker, "test-user")
	store.CreateIssue(ctx, epic1, "test-user")
	store.CreateIssue(ctx, epic2, "test-user")
	store.CreateIssue(ctx, task1, "test-user")

	// epic1 blocked by blocker
	store.AddDependency(ctx, &types.Dependency{IssueID: epic1.ID, DependsOnID: blocker.ID, Type: types.DepBlocks}, "test-user")
	// epic2 is child of epic1
	store.AddDependency(ctx, &types.Dependency{IssueID: epic2.ID, DependsOnID: epic1.ID, Type: types.DepParentChild}, "test-user")
	// task1 is child of epic2
	store.AddDependency(ctx, &types.Dependency{IssueID: task1.ID, DependsOnID: epic2.ID, Type: types.DepParentChild}, "test-user")

	// Get ready work
	ready, err := store.GetReadyWork(ctx, types.WorkFilter{Status: types.StatusOpen})
	if err != nil {
		t.Fatalf("GetReadyWork failed: %v", err)
	}

	// Should have only blocker ready
	readyIDs := make(map[string]bool)
	for _, issue := range ready {
		readyIDs[issue.ID] = true
	}

	if readyIDs[epic1.ID] {
		t.Errorf("Expected epic1 to be blocked, but it was ready")
	}
	if readyIDs[epic2.ID] {
		t.Errorf("Expected epic2 to be blocked (parent is blocked), but it was ready")
	}
	if readyIDs[task1.ID] {
		t.Errorf("Expected task1 to be blocked (grandparent is blocked), but it was ready")
	}
	if !readyIDs[blocker.ID] {
		t.Errorf("Expected blocker to be ready")
	}
}

// TestMultipleParentsOneBlocked tests that a child is blocked if ANY parent is blocked
func TestMultipleParentsOneBlocked(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create:
	// blocker: open
	// epic1: open, blocked by 'blocker'
	// epic2: open, no blockers
	// task1: open, child of BOTH epic1 and epic2
	//
	// Expected: task1 should NOT be ready (one parent is blocked)

	blocker := &types.Issue{Title: "Blocker", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	epic1 := &types.Issue{Title: "Epic 1 (blocked)", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeEpic}
	epic2 := &types.Issue{Title: "Epic 2 (ready)", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeEpic}
	task1 := &types.Issue{Title: "Task 1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

	store.CreateIssue(ctx, blocker, "test-user")
	store.CreateIssue(ctx, epic1, "test-user")
	store.CreateIssue(ctx, epic2, "test-user")
	store.CreateIssue(ctx, task1, "test-user")

	// epic1 blocked by blocker
	store.AddDependency(ctx, &types.Dependency{IssueID: epic1.ID, DependsOnID: blocker.ID, Type: types.DepBlocks}, "test-user")
	// task1 is child of both epic1 and epic2
	store.AddDependency(ctx, &types.Dependency{IssueID: task1.ID, DependsOnID: epic1.ID, Type: types.DepParentChild}, "test-user")
	store.AddDependency(ctx, &types.Dependency{IssueID: task1.ID, DependsOnID: epic2.ID, Type: types.DepParentChild}, "test-user")

	// Get ready work
	ready, err := store.GetReadyWork(ctx, types.WorkFilter{Status: types.StatusOpen})
	if err != nil {
		t.Fatalf("GetReadyWork failed: %v", err)
	}

	// Should have blocker and epic2 ready, but NOT epic1 or task1
	readyIDs := make(map[string]bool)
	for _, issue := range ready {
		readyIDs[issue.ID] = true
	}

	if readyIDs[epic1.ID] {
		t.Errorf("Expected epic1 to be blocked, but it was ready")
	}
	if readyIDs[task1.ID] {
		t.Errorf("Expected task1 to be blocked (one parent is blocked), but it was ready")
	}
	if !readyIDs[blocker.ID] {
		t.Errorf("Expected blocker to be ready")
	}
	if !readyIDs[epic2.ID] {
		t.Errorf("Expected epic2 to be ready")
	}
}

// TestBlockerClosedUnblocksChildren tests that closing a blocker unblocks descendants
func TestBlockerClosedUnblocksChildren(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create:
	// blocker: initially open, then closed
	// epic1: open, blocked by 'blocker'
	// task1: open, child of epic1
	//
	// After closing blocker: both epic1 and task1 should be ready

	blocker := &types.Issue{Title: "Blocker", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	epic1 := &types.Issue{Title: "Epic 1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeEpic}
	task1 := &types.Issue{Title: "Task 1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

	store.CreateIssue(ctx, blocker, "test-user")
	store.CreateIssue(ctx, epic1, "test-user")
	store.CreateIssue(ctx, task1, "test-user")

	// epic1 blocked by blocker
	store.AddDependency(ctx, &types.Dependency{IssueID: epic1.ID, DependsOnID: blocker.ID, Type: types.DepBlocks}, "test-user")
	// task1 is child of epic1
	store.AddDependency(ctx, &types.Dependency{IssueID: task1.ID, DependsOnID: epic1.ID, Type: types.DepParentChild}, "test-user")

	// Initially, epic1 and task1 should be blocked
	ready, err := store.GetReadyWork(ctx, types.WorkFilter{Status: types.StatusOpen})
	if err != nil {
		t.Fatalf("GetReadyWork failed: %v", err)
	}

	readyIDs := make(map[string]bool)
	for _, issue := range ready {
		readyIDs[issue.ID] = true
	}

	if readyIDs[epic1.ID] || readyIDs[task1.ID] {
		t.Errorf("Expected epic1 and task1 to be blocked initially")
	}

	// Close the blocker
	store.CloseIssue(ctx, blocker.ID, "Done", "test-user")

	// Now epic1 and task1 should be ready
	ready, err = store.GetReadyWork(ctx, types.WorkFilter{Status: types.StatusOpen})
	if err != nil {
		t.Fatalf("GetReadyWork failed after closing blocker: %v", err)
	}

	readyIDs = make(map[string]bool)
	for _, issue := range ready {
		readyIDs[issue.ID] = true
	}

	if !readyIDs[epic1.ID] {
		t.Errorf("Expected epic1 to be ready after blocker closed")
	}
	if !readyIDs[task1.ID] {
		t.Errorf("Expected task1 to be ready after blocker closed")
	}
}

// TestRelatedDoesNotPropagate tests that 'related' deps don't cause blocking propagation
func TestRelatedDoesNotPropagate(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create:
	// blocker: open
	// epic1: open, blocked by 'blocker'
	// task1: open, related to epic1 (NOT parent-child)
	//
	// Expected: task1 SHOULD be ready (related doesn't propagate blocking)

	blocker := &types.Issue{Title: "Blocker", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	epic1 := &types.Issue{Title: "Epic 1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeEpic}
	task1 := &types.Issue{Title: "Task 1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

	store.CreateIssue(ctx, blocker, "test-user")
	store.CreateIssue(ctx, epic1, "test-user")
	store.CreateIssue(ctx, task1, "test-user")

	// epic1 blocked by blocker
	store.AddDependency(ctx, &types.Dependency{IssueID: epic1.ID, DependsOnID: blocker.ID, Type: types.DepBlocks}, "test-user")
	// task1 is related to epic1 (NOT parent-child)
	store.AddDependency(ctx, &types.Dependency{IssueID: task1.ID, DependsOnID: epic1.ID, Type: types.DepRelated}, "test-user")

	// Get ready work
	ready, err := store.GetReadyWork(ctx, types.WorkFilter{Status: types.StatusOpen})
	if err != nil {
		t.Fatalf("GetReadyWork failed: %v", err)
	}

	// Should have blocker AND task1 ready (related doesn't propagate)
	readyIDs := make(map[string]bool)
	for _, issue := range ready {
		readyIDs[issue.ID] = true
	}

	if readyIDs[epic1.ID] {
		t.Errorf("Expected epic1 to be blocked, but it was ready")
	}
	if !readyIDs[task1.ID] {
		t.Errorf("Expected task1 to be ready (related deps don't propagate blocking), but it was blocked")
	}
	if !readyIDs[blocker.ID] {
		t.Errorf("Expected blocker to be ready")
	}
}

// TestCompositeIndexExists verifies the composite index is created
func TestCompositeIndexExists(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Query sqlite_master to check if the index exists
	var indexName string
	err := store.db.QueryRowContext(ctx, `
		SELECT name FROM sqlite_master
		WHERE type='index' AND name='idx_dependencies_depends_on_type'
	`).Scan(&indexName)

	if err != nil {
		t.Fatalf("Composite index idx_dependencies_depends_on_type not found: %v", err)
	}

	if indexName != "idx_dependencies_depends_on_type" {
		t.Errorf("Expected index name 'idx_dependencies_depends_on_type', got '%s'", indexName)
	}
}

// TestReadyIssuesViewMatchesGetReadyWork verifies the ready_issues VIEW produces same results as GetReadyWork
func TestReadyIssuesViewMatchesGetReadyWork(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create hierarchy: blocker → epic1 → task1
	blocker := &types.Issue{Title: "Blocker", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	epic1 := &types.Issue{Title: "Epic 1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeEpic}
	task1 := &types.Issue{Title: "Task 1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	task2 := &types.Issue{Title: "Task 2", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

	store.CreateIssue(ctx, blocker, "test-user")
	store.CreateIssue(ctx, epic1, "test-user")
	store.CreateIssue(ctx, task1, "test-user")
	store.CreateIssue(ctx, task2, "test-user")

	// epic1 blocked by blocker
	store.AddDependency(ctx, &types.Dependency{IssueID: epic1.ID, DependsOnID: blocker.ID, Type: types.DepBlocks}, "test-user")
	// task1 is child of epic1 (should be blocked)
	store.AddDependency(ctx, &types.Dependency{IssueID: task1.ID, DependsOnID: epic1.ID, Type: types.DepParentChild}, "test-user")
	// task2 has no dependencies (should be ready)

	// Get ready work via GetReadyWork function
	ready, err := store.GetReadyWork(ctx, types.WorkFilter{Status: types.StatusOpen})
	if err != nil {
		t.Fatalf("GetReadyWork failed: %v", err)
	}

	readyIDsFromFunc := make(map[string]bool)
	for _, issue := range ready {
		readyIDsFromFunc[issue.ID] = true
	}

	// Get ready work via VIEW
	rows, err := store.db.QueryContext(ctx, `SELECT id FROM ready_issues ORDER BY id`)
	if err != nil {
		t.Fatalf("Query ready_issues VIEW failed: %v", err)
	}
	defer rows.Close()

	readyIDsFromView := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("Scan failed: %v", err)
		}
		readyIDsFromView[id] = true
	}

	// Verify they match
	if len(readyIDsFromFunc) != len(readyIDsFromView) {
		t.Errorf("Mismatch: GetReadyWork returned %d issues, VIEW returned %d", 
			len(readyIDsFromFunc), len(readyIDsFromView))
	}

	for id := range readyIDsFromFunc {
		if !readyIDsFromView[id] {
			t.Errorf("Issue %s in GetReadyWork but NOT in VIEW", id)
		}
	}

	for id := range readyIDsFromView {
		if !readyIDsFromFunc[id] {
			t.Errorf("Issue %s in VIEW but NOT in GetReadyWork", id)
		}
	}

	// Verify specific expectations
	if !readyIDsFromView[blocker.ID] {
		t.Errorf("Expected blocker to be ready in VIEW")
	}
	if !readyIDsFromView[task2.ID] {
		t.Errorf("Expected task2 to be ready in VIEW")
	}
	if readyIDsFromView[epic1.ID] {
		t.Errorf("Expected epic1 to be blocked in VIEW (has blocker)")
	}
	if readyIDsFromView[task1.ID] {
		t.Errorf("Expected task1 to be blocked in VIEW (parent is blocked)")
	}
}

// TestDeepHierarchyBlocking tests blocking propagation through 50-level deep hierarchy
func TestDeepHierarchyBlocking(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create a blocker at the root
	blocker := &types.Issue{Title: "Root Blocker", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	store.CreateIssue(ctx, blocker, "test-user")

	// Create 50-level hierarchy: root → level1 → level2 → ... → level50
	var issues []*types.Issue
	for i := 0; i < 50; i++ {
		issue := &types.Issue{
			Title:      "Level " + string(rune(i)),
			Status:     types.StatusOpen,
			Priority:   1,
			IssueType:  types.TypeEpic,
		}
		store.CreateIssue(ctx, issue, "test-user")
		issues = append(issues, issue)

		if i == 0 {
			// First level: blocked by blocker
			store.AddDependency(ctx, &types.Dependency{
				IssueID:     issue.ID,
				DependsOnID: blocker.ID,
				Type:        types.DepBlocks,
			}, "test-user")
		} else {
			// Each subsequent level: child of previous level
			store.AddDependency(ctx, &types.Dependency{
				IssueID:     issue.ID,
				DependsOnID: issues[i-1].ID,
				Type:        types.DepParentChild,
			}, "test-user")
		}
	}

	// Get ready work
	ready, err := store.GetReadyWork(ctx, types.WorkFilter{Status: types.StatusOpen})
	if err != nil {
		t.Fatalf("GetReadyWork failed: %v", err)
	}

	// Build set of ready IDs
	readyIDs := make(map[string]bool)
	for _, issue := range ready {
		readyIDs[issue.ID] = true
	}

	// Only the blocker should be ready
	if len(ready) != 1 {
		t.Errorf("Expected exactly 1 ready issue (the blocker), got %d", len(ready))
	}

	if !readyIDs[blocker.ID] {
		t.Errorf("Expected blocker to be ready")
	}

	// All 50 levels should be blocked
	for i, issue := range issues {
		if readyIDs[issue.ID] {
			t.Errorf("Expected level %d (issue %s) to be blocked, but it was ready", i, issue.ID)
		}
	}

	// Now close the blocker and verify all levels become ready
	store.CloseIssue(ctx, blocker.ID, "Done", "test-user")

	ready, err = store.GetReadyWork(ctx, types.WorkFilter{Status: types.StatusOpen})
	if err != nil {
		t.Fatalf("GetReadyWork failed after closing blocker: %v", err)
	}

	// All 50 levels should now be ready
	if len(ready) != 50 {
		t.Errorf("Expected 50 ready issues after closing blocker, got %d", len(ready))
	}

	readyIDs = make(map[string]bool)
	for _, issue := range ready {
		readyIDs[issue.ID] = true
	}

	for i, issue := range issues {
		if !readyIDs[issue.ID] {
			t.Errorf("Expected level %d (issue %s) to be ready after blocker closed, but it was blocked", i, issue.ID)
		}
	}
}

func TestGetReadyWorkIncludesInProgress(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create issues:
	// bd-1: open, no dependencies → READY
	// bd-2: in_progress, no dependencies → READY (bd-165)
	// bd-3: in_progress, depends on open issue → BLOCKED
	// bd-4: closed, no dependencies → NOT READY (closed)

	issue1 := &types.Issue{Title: "Open Ready", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issue2 := &types.Issue{Title: "In Progress Ready", Status: types.StatusInProgress, Priority: 2, IssueType: types.TypeEpic}
	issue3 := &types.Issue{Title: "In Progress Blocked", Status: types.StatusInProgress, Priority: 1, IssueType: types.TypeTask}
	issue4 := &types.Issue{Title: "Blocker", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issue5 := &types.Issue{Title: "Closed", Status: types.StatusClosed, Priority: 1, IssueType: types.TypeTask}

	store.CreateIssue(ctx, issue1, "test-user")
	store.CreateIssue(ctx, issue2, "test-user")
	store.UpdateIssue(ctx, issue2.ID, map[string]interface{}{"status": types.StatusInProgress}, "test-user")
	store.CreateIssue(ctx, issue3, "test-user")
	store.UpdateIssue(ctx, issue3.ID, map[string]interface{}{"status": types.StatusInProgress}, "test-user")
	store.CreateIssue(ctx, issue4, "test-user")
	store.CreateIssue(ctx, issue5, "test-user")
	store.CloseIssue(ctx, issue5.ID, "Done", "test-user")

	// Add dependency: issue3 blocks on issue4
	store.AddDependency(ctx, &types.Dependency{IssueID: issue3.ID, DependsOnID: issue4.ID, Type: types.DepBlocks}, "test-user")

	// Get ready work (default filter - no status specified)
	ready, err := store.GetReadyWork(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetReadyWork failed: %v", err)
	}

	// Should have 3 ready issues:
	// - issue1 (open, no blockers)
	// - issue2 (in_progress, no blockers) ← this is the key test case for bd-165
	// - issue4 (open blocker, but itself has no blockers so it's ready to work on)
	if len(ready) != 3 {
		t.Logf("Ready issues:")
		for _, r := range ready {
			t.Logf("  - %s: %s (status: %s)", r.ID, r.Title, r.Status)
		}
		t.Fatalf("Expected 3 ready issues, got %d", len(ready))
	}

	// Verify ready issues
	readyIDs := make(map[string]bool)
	for _, issue := range ready {
		readyIDs[issue.ID] = true
	}

	if !readyIDs[issue1.ID] {
		t.Errorf("Expected %s (open, no blockers) to be ready", issue1.ID)
	}
	if !readyIDs[issue2.ID] {
		t.Errorf("Expected %s (in_progress, no blockers) to be ready - this is bd-165!", issue2.ID)
	}
	if !readyIDs[issue4.ID] {
		t.Errorf("Expected %s (open blocker, but itself unblocked) to be ready", issue4.ID)
	}
	if readyIDs[issue3.ID] {
		t.Errorf("Expected %s (in_progress, blocked) to NOT be ready", issue3.ID)
	}
	if readyIDs[issue5.ID] {
		t.Errorf("Expected %s (closed) to NOT be ready", issue5.ID)
	}
}

func TestExplainQueryPlanReadyWork(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	blocker := &types.Issue{Title: "Blocker", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	epic1 := &types.Issue{Title: "Epic", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeEpic}
	task1 := &types.Issue{Title: "Task", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	task2 := &types.Issue{Title: "Ready Task", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}

	store.CreateIssue(ctx, blocker, "test-user")
	store.CreateIssue(ctx, epic1, "test-user")
	store.CreateIssue(ctx, task1, "test-user")
	store.CreateIssue(ctx, task2, "test-user")

	store.AddDependency(ctx, &types.Dependency{IssueID: epic1.ID, DependsOnID: blocker.ID, Type: types.DepBlocks}, "test-user")
	store.AddDependency(ctx, &types.Dependency{IssueID: task1.ID, DependsOnID: epic1.ID, Type: types.DepParentChild}, "test-user")

	query := `
		EXPLAIN QUERY PLAN
		WITH RECURSIVE
		  blocked_directly AS (
		    SELECT DISTINCT d.issue_id
		    FROM dependencies d
		    JOIN issues blocker ON d.depends_on_id = blocker.id
		    WHERE d.type = 'blocks'
		      AND blocker.status IN ('open', 'in_progress', 'blocked')
		  ),
		  blocked_transitively AS (
		    SELECT issue_id, 0 as depth
		    FROM blocked_directly
		    UNION ALL
		    SELECT d.issue_id, bt.depth + 1
		    FROM blocked_transitively bt
		    JOIN dependencies d ON d.depends_on_id = bt.issue_id
		    WHERE d.type = 'parent-child'
		      AND bt.depth < 50
		  )
		SELECT i.id, i.content_hash, i.title, i.description, i.design, i.acceptance_criteria, i.notes,
		       i.status, i.priority, i.issue_type, i.assignee, i.estimated_minutes,
		       i.created_at, i.updated_at, i.closed_at, i.external_ref
		FROM issues i
		WHERE i.status IN ('open', 'in_progress')
		  AND NOT EXISTS (
		    SELECT 1 FROM blocked_transitively WHERE issue_id = i.id
		  )
		ORDER BY 
		  CASE WHEN datetime(i.created_at) >= datetime('now', '-48 hours') THEN 0 ELSE 1 END ASC,
		  CASE WHEN datetime(i.created_at) >= datetime('now', '-48 hours') THEN i.priority ELSE NULL END ASC,
		  CASE WHEN datetime(i.created_at) < datetime('now', '-48 hours') THEN i.created_at ELSE NULL END ASC,
		  i.created_at ASC
	`

	rows, err := store.db.QueryContext(ctx, query)
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN failed: %v", err)
	}
	defer rows.Close()

	var planLines []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatalf("Failed to scan EXPLAIN output: %v", err)
		}
		planLines = append(planLines, detail)
	}

	if len(planLines) == 0 {
		t.Fatal("No query plan output received")
	}

	t.Logf("Query plan:")
	for i, line := range planLines {
		t.Logf("  %d: %s", i, line)
	}

	foundTableScan := false
	for _, line := range planLines {
		if strings.Contains(line, "SCAN TABLE issues") || 
		   strings.Contains(line, "SCAN TABLE dependencies") {
			foundTableScan = true
			t.Errorf("Found table scan in query plan: %s", line)
		}
	}

	if foundTableScan {
		t.Error("Query plan contains table scans - indexes may not be used efficiently")
	}
}

// TestSortPolicyPriority tests strict priority-first sorting
func TestSortPolicyPriority(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create issues with mixed ages and priorities
	// Old issues (72 hours ago)
	issueP0Old := &types.Issue{Title: "old-P0", Status: types.StatusOpen, Priority: 0, IssueType: types.TypeTask}
	issueP2Old := &types.Issue{Title: "old-P2", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	issueP1Old := &types.Issue{Title: "old-P1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

	// Recent issues (12 hours ago)
	issueP3New := &types.Issue{Title: "new-P3", Status: types.StatusOpen, Priority: 3, IssueType: types.TypeTask}
	issueP1New := &types.Issue{Title: "new-P1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

	// Create old issues first (to have older created_at)
	store.CreateIssue(ctx, issueP0Old, "test-user")
	store.CreateIssue(ctx, issueP2Old, "test-user")
	store.CreateIssue(ctx, issueP1Old, "test-user")

	// Create new issues
	store.CreateIssue(ctx, issueP3New, "test-user")
	store.CreateIssue(ctx, issueP1New, "test-user")

	// Use priority sort policy
	ready, err := store.GetReadyWork(ctx, types.WorkFilter{
		Status:     types.StatusOpen,
		SortPolicy: types.SortPolicyPriority,
	})
	if err != nil {
		t.Fatalf("GetReadyWork failed: %v", err)
	}

	if len(ready) != 5 {
		t.Fatalf("Expected 5 ready issues, got %d", len(ready))
	}

	// Verify strict priority ordering: P0, P1, P1, P2, P3
	// Within same priority, older created_at comes first
	expectedOrder := []struct {
		title    string
		priority int
	}{
		{"old-P0", 0},
		{"old-P1", 1},
		{"new-P1", 1},
		{"old-P2", 2},
		{"new-P3", 3},
	}

	for i, expected := range expectedOrder {
		if ready[i].Title != expected.title {
			t.Errorf("Position %d: expected %s, got %s", i, expected.title, ready[i].Title)
		}
		if ready[i].Priority != expected.priority {
			t.Errorf("Position %d: expected P%d, got P%d", i, expected.priority, ready[i].Priority)
		}
	}
}

// TestSortPolicyOldest tests oldest-first sorting (ignoring priority)
func TestSortPolicyOldest(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create issues in order: P2, P0, P1 (mixed priority, chronological creation)
	issueP2 := &types.Issue{Title: "first-P2", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	issueP0 := &types.Issue{Title: "second-P0", Status: types.StatusOpen, Priority: 0, IssueType: types.TypeTask}
	issueP1 := &types.Issue{Title: "third-P1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

	store.CreateIssue(ctx, issueP2, "test-user")
	store.CreateIssue(ctx, issueP0, "test-user")
	store.CreateIssue(ctx, issueP1, "test-user")

	// Use oldest sort policy
	ready, err := store.GetReadyWork(ctx, types.WorkFilter{
		Status:     types.StatusOpen,
		SortPolicy: types.SortPolicyOldest,
	})
	if err != nil {
		t.Fatalf("GetReadyWork failed: %v", err)
	}

	if len(ready) != 3 {
		t.Fatalf("Expected 3 ready issues, got %d", len(ready))
	}

	// Should be sorted by creation time only (oldest first)
	expectedTitles := []string{"first-P2", "second-P0", "third-P1"}
	for i, expected := range expectedTitles {
		if ready[i].Title != expected {
			t.Errorf("Position %d: expected %s, got %s", i, expected, ready[i].Title)
		}
	}
}

// TestSortPolicyHybrid tests hybrid sort (default behavior)
func TestSortPolicyHybrid(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create issues with different priorities
	// All created recently (within 48 hours in test), so should sort by priority
	issueP0 := &types.Issue{Title: "issue-P0", Status: types.StatusOpen, Priority: 0, IssueType: types.TypeTask}
	issueP2 := &types.Issue{Title: "issue-P2", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	issueP1 := &types.Issue{Title: "issue-P1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issueP3 := &types.Issue{Title: "issue-P3", Status: types.StatusOpen, Priority: 3, IssueType: types.TypeTask}

	store.CreateIssue(ctx, issueP2, "test-user")
	store.CreateIssue(ctx, issueP0, "test-user")
	store.CreateIssue(ctx, issueP3, "test-user")
	store.CreateIssue(ctx, issueP1, "test-user")

	// Use hybrid sort policy (explicit)
	ready, err := store.GetReadyWork(ctx, types.WorkFilter{
		Status:     types.StatusOpen,
		SortPolicy: types.SortPolicyHybrid,
	})
	if err != nil {
		t.Fatalf("GetReadyWork failed: %v", err)
	}

	if len(ready) != 4 {
		t.Fatalf("Expected 4 ready issues, got %d", len(ready))
	}

	// Since all issues are created recently (< 48 hours in test context),
	// hybrid sort should order by priority: P0, P1, P2, P3
	expectedPriorities := []int{0, 1, 2, 3}
	for i, expected := range expectedPriorities {
		if ready[i].Priority != expected {
			t.Errorf("Position %d: expected P%d, got P%d", i, expected, ready[i].Priority)
		}
	}
}

// TestSortPolicyDefault tests that empty sort policy defaults to hybrid
func TestSortPolicyDefault(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create test issues with different priorities
	issueP1 := &types.Issue{Title: "issue-P1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issueP2 := &types.Issue{Title: "issue-P2", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}

	store.CreateIssue(ctx, issueP2, "test-user")
	store.CreateIssue(ctx, issueP1, "test-user")

	// Use default (empty) sort policy
	ready, err := store.GetReadyWork(ctx, types.WorkFilter{
		Status: types.StatusOpen,
		// SortPolicy not specified - should default to hybrid
	})
	if err != nil {
		t.Fatalf("GetReadyWork failed: %v", err)
	}

	if len(ready) != 2 {
		t.Fatalf("Expected 2 ready issues, got %d", len(ready))
	}

	// Should behave like hybrid: since both are recent, sort by priority (P1 first)
	if ready[0].Priority != 1 {
		t.Errorf("Expected P1 first (hybrid default, recent by priority), got P%d", ready[0].Priority)
	}
	if ready[1].Priority != 2 {
		t.Errorf("Expected P2 second, got P%d", ready[1].Priority)
	}
}
