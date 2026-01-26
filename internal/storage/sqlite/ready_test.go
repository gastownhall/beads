package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/types"
)

func TestGetReadyWork(t *testing.T) {
	// Create issues:
	// issue1: open, no dependencies → READY
	// issue2: open, depends on issue1 (open) → BLOCKED
	// issue3: open, no dependencies → READY
	// issue4: closed, no dependencies → NOT READY (closed)
	// issue5: open, depends on issue4 (closed) → READY (blocker is closed)
	env := newTestEnv(t)

	issue1 := env.CreateIssueWith("Ready 1", types.StatusOpen, 1, types.TypeTask)
	issue2 := env.CreateIssueWith("Blocked", types.StatusOpen, 1, types.TypeTask)
	issue3 := env.CreateIssueWith("Ready 2", types.StatusOpen, 2, types.TypeTask)
	issue4 := env.CreateIssueWith("Closed", types.StatusOpen, 1, types.TypeTask) // create as open first
	env.Close(issue4, "Done")
	issue5 := env.CreateIssueWith("Ready 3", types.StatusOpen, 0, types.TypeTask)

	env.AddDep(issue2, issue1) // issue2 depends on issue1
	env.AddDep(issue5, issue4) // issue5 depends on issue4 (which is closed)

	// Verify ready issues: issue1, issue3, issue5
	ready := env.GetReadyWork(types.WorkFilter{Status: types.StatusOpen})
	if len(ready) != 3 {
		t.Fatalf("Expected 3 ready issues, got %d", len(ready))
	}

	env.AssertReady(issue1)
	env.AssertReady(issue3)
	env.AssertReady(issue5)  // blocker (issue4) is closed
	env.AssertBlocked(issue2)
}

func TestGetReadyWorkPriorityOrder(t *testing.T) {
	env := newTestEnv(t)

	// Create issues with different priorities (out of order)
	env.CreateIssueWith("Medium", types.StatusOpen, 2, types.TypeTask)
	env.CreateIssueWith("Highest", types.StatusOpen, 0, types.TypeTask)
	env.CreateIssueWith("High", types.StatusOpen, 1, types.TypeTask)

	ready := env.GetReadyWork(types.WorkFilter{Status: types.StatusOpen})
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
	env := newTestEnv(t)

	// Create issues with different priorities
	env.CreateIssueWith("P0", types.StatusOpen, 0, types.TypeTask)
	env.CreateIssueWith("P1", types.StatusOpen, 1, types.TypeTask)
	env.CreateIssueWith("P2", types.StatusOpen, 2, types.TypeTask)

	// Filter for P0 only
	priority0 := 0
	ready := env.GetReadyWork(types.WorkFilter{Status: types.StatusOpen, Priority: &priority0})
	if len(ready) != 1 {
		t.Fatalf("Expected 1 P0 issue, got %d", len(ready))
	}
	if ready[0].Priority != 0 {
		t.Errorf("Expected P0 issue, got P%d", ready[0].Priority)
	}
}

func TestGetReadyWorkWithAssigneeFilter(t *testing.T) {
	env := newTestEnv(t)

	// Create issues with different assignees
	env.CreateIssueWithAssignee("Alice's task", "alice")
	env.CreateIssueWithAssignee("Bob's task", "bob")
	env.CreateIssue("Unassigned")

	// Filter for alice
	assignee := "alice"
	ready := env.GetReadyWork(types.WorkFilter{Status: types.StatusOpen, Assignee: &assignee})
	if len(ready) != 1 {
		t.Fatalf("Expected 1 issue for alice, got %d", len(ready))
	}
	if ready[0].Assignee != "alice" {
		t.Errorf("Expected alice's issue, got %s", ready[0].Assignee)
	}
}

func TestGetReadyWorkWithUnassignedFilter(t *testing.T) {
	env := newTestEnv(t)

	// Create issues with different assignees
	env.CreateIssueWithAssignee("Alice's task", "alice")
	env.CreateIssueWithAssignee("Bob's task", "bob")
	unassigned := env.CreateIssue("Unassigned")

	// Filter for unassigned issues
	ready := env.GetReadyWork(types.WorkFilter{Status: types.StatusOpen, Unassigned: true})
	if len(ready) != 1 {
		t.Fatalf("Expected 1 unassigned issue, got %d", len(ready))
	}
	if ready[0].Assignee != "" {
		t.Errorf("Expected unassigned issue, got assignee %q", ready[0].Assignee)
	}
	if ready[0].ID != unassigned.ID {
		t.Errorf("Expected issue %s, got %s", unassigned.ID, ready[0].ID)
	}
}

func TestGetReadyWorkWithLimit(t *testing.T) {
	env := newTestEnv(t)

	// Create 5 ready issues
	for i := 0; i < 5; i++ {
		env.CreateIssue("Task")
	}

	// Limit to 3
	ready := env.GetReadyWork(types.WorkFilter{Status: types.StatusOpen, Limit: 3})
	if len(ready) != 3 {
		t.Errorf("Expected 3 issues (limit), got %d", len(ready))
	}
}

func TestGetReadyWorkIgnoresRelatedDeps(t *testing.T) {
	env := newTestEnv(t)

	// Create two issues with "related" dependency (should not block)
	issue1 := env.CreateIssue("First")
	issue2 := env.CreateIssue("Second")

	env.AddDepType(issue2, issue1, types.DepRelated)

	// Both should be ready (related deps don't block)
	ready := env.GetReadyWork(types.WorkFilter{Status: types.StatusOpen})
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
	blocked, err := store.GetBlockedIssues(ctx, types.WorkFilter{})
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
	// Create:
	// blocker: open
	// epic1: open, blocked by 'blocker'
	// task1: open, child of epic1 (via parent-child)
	//
	// Expected: task1 should NOT be ready (parent is blocked)
	env := newTestEnv(t)

	blocker := env.CreateIssue("Blocker")
	epic1 := env.CreateEpic("Epic 1")
	task1 := env.CreateIssue("Task 1")

	env.AddDep(epic1, blocker)        // epic1 blocked by blocker
	env.AddParentChild(task1, epic1)  // task1 is child of epic1

	env.AssertBlocked(epic1)
	env.AssertBlocked(task1)
	env.AssertReady(blocker)
}

// TestGrandparentBlockerBlocksGrandchildren tests multi-level propagation
func TestGrandparentBlockerBlocksGrandchildren(t *testing.T) {
	// Create:
	// blocker: open
	// epic1: open, blocked by 'blocker'
	// epic2: open, child of epic1
	// task1: open, child of epic2
	//
	// Expected: task1 should NOT be ready (grandparent is blocked)
	env := newTestEnv(t)

	blocker := env.CreateIssue("Blocker")
	epic1 := env.CreateEpic("Epic 1")
	epic2 := env.CreateEpic("Epic 2")
	task1 := env.CreateIssue("Task 1")

	env.AddDep(epic1, blocker)        // epic1 blocked by blocker
	env.AddParentChild(epic2, epic1)  // epic2 is child of epic1
	env.AddParentChild(task1, epic2)  // task1 is child of epic2

	env.AssertBlocked(epic1)
	env.AssertBlocked(epic2)
	env.AssertBlocked(task1)
	env.AssertReady(blocker)
}

// TestMultipleParentsOneBlocked tests that a child is blocked if ANY parent is blocked
func TestMultipleParentsOneBlocked(t *testing.T) {
	// Create:
	// blocker: open
	// epic1: open, blocked by 'blocker'
	// epic2: open, no blockers
	// task1: open, child of BOTH epic1 and epic2
	//
	// Expected: task1 should NOT be ready (one parent is blocked)
	env := newTestEnv(t)

	blocker := env.CreateIssue("Blocker")
	epic1 := env.CreateEpic("Epic 1 (blocked)")
	epic2 := env.CreateEpic("Epic 2 (ready)")
	task1 := env.CreateIssue("Task 1")

	env.AddDep(epic1, blocker)        // epic1 blocked by blocker
	env.AddParentChild(task1, epic1)  // task1 is child of both epic1 and epic2
	env.AddParentChild(task1, epic2)

	env.AssertBlocked(epic1)
	env.AssertBlocked(task1)  // blocked because one parent (epic1) is blocked
	env.AssertReady(blocker)
	env.AssertReady(epic2)
}

// TestBlockerClosedUnblocksChildren tests that closing a blocker unblocks descendants
func TestBlockerClosedUnblocksChildren(t *testing.T) {
	// Create:
	// blocker: initially open, then closed
	// epic1: open, blocked by 'blocker'
	// task1: open, child of epic1
	//
	// After closing blocker: both epic1 and task1 should be ready
	env := newTestEnv(t)

	blocker := env.CreateIssue("Blocker")
	epic1 := env.CreateEpic("Epic 1")
	task1 := env.CreateIssue("Task 1")

	env.AddDep(epic1, blocker)       // epic1 blocked by blocker
	env.AddParentChild(task1, epic1) // task1 is child of epic1

	// Initially, epic1 and task1 should be blocked
	env.AssertBlocked(epic1)
	env.AssertBlocked(task1)

	// Close the blocker
	env.Close(blocker, "Done")

	// Now epic1 and task1 should be ready
	env.AssertReady(epic1)
	env.AssertReady(task1)
}

// TestRelatedDoesNotPropagate tests that 'related' deps don't cause blocking propagation
func TestRelatedDoesNotPropagate(t *testing.T) {
	// Create:
	// blocker: open
	// epic1: open, blocked by 'blocker'
	// task1: open, related to epic1 (NOT parent-child)
	//
	// Expected: task1 SHOULD be ready (related doesn't propagate blocking)
	env := newTestEnv(t)

	blocker := env.CreateIssue("Blocker")
	epic1 := env.CreateEpic("Epic 1")
	task1 := env.CreateIssue("Task 1")

	env.AddDep(epic1, blocker)                      // epic1 blocked by blocker
	env.AddDepType(task1, epic1, types.DepRelated)  // task1 is related to epic1 (NOT parent-child)

	env.AssertBlocked(epic1)
	env.AssertReady(task1)   // related deps don't propagate blocking
	env.AssertReady(blocker)
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
	store.CloseIssue(ctx, blocker.ID, "Done", "test-user", "")

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
	store.CloseIssue(ctx, issue5.ID, "Done", "test-user", "")

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

// TestGetReadyWorkExternalDeps tests that GetReadyWork filters out issues
// with unsatisfied external dependencies (bd-zmmy)
func TestGetReadyWorkExternalDeps(t *testing.T) {
	// Create main test database
	mainStore, mainCleanup := setupTestDB(t)
	defer mainCleanup()

	ctx := context.Background()

	// Create external project directory with beads database
	externalDir, err := os.MkdirTemp("", "beads-external-test-*")
	if err != nil {
		t.Fatalf("failed to create external temp dir: %v", err)
	}
	defer os.RemoveAll(externalDir)

	// Create .beads directory and config in external project
	beadsDir := filepath.Join(externalDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	// Create config file for external project
	cfg := configfile.DefaultConfig()
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("failed to save external config: %v", err)
	}

	// Create external database (must match configfile.DefaultConfig().Database)
	externalDBPath := filepath.Join(beadsDir, "beads.db")
	externalStore, err := New(ctx, externalDBPath)
	if err != nil {
		t.Fatalf("failed to create external store: %v", err)
	}
	defer externalStore.Close()

	// Set issue_prefix in external store
	if err := externalStore.SetConfig(ctx, "issue_prefix", "ext"); err != nil {
		t.Fatalf("failed to set external issue_prefix: %v", err)
	}

	// Initialize config if not already done (required for Set to work)
	if err := config.Initialize(); err != nil {
		t.Fatalf("failed to initialize config: %v", err)
	}

	// Configure external_projects to point to our temp external project
	// Save current value to restore later
	oldProjects := config.GetExternalProjects()
	defer func() {
		if oldProjects != nil {
			config.Set("external_projects", oldProjects)
		} else {
			config.Set("external_projects", map[string]string{})
		}
	}()

	config.Set("external_projects", map[string]string{
		"external-test": externalDir,
	})

	// Create an issue in main DB with external dependency
	issueWithExtDep := &types.Issue{
		Title:     "Has external dep",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := mainStore.CreateIssue(ctx, issueWithExtDep, "test-user"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	// Add external dependency
	extDep := &types.Dependency{
		IssueID:     issueWithExtDep.ID,
		DependsOnID: "external:external-test:test-capability",
		Type:        types.DepBlocks,
	}
	if err := mainStore.AddDependency(ctx, extDep, "test-user"); err != nil {
		t.Fatalf("failed to add external dependency: %v", err)
	}

	// Create a regular issue without external dep
	regularIssue := &types.Issue{
		Title:     "Regular issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := mainStore.CreateIssue(ctx, regularIssue, "test-user"); err != nil {
		t.Fatalf("failed to create regular issue: %v", err)
	}

	// Debug: check config
	projects := config.GetExternalProjects()
	t.Logf("External projects config: %v", projects)

	resolvedPath := config.ResolveExternalProjectPath("external-test")
	t.Logf("Resolved path for 'external-test': %s", resolvedPath)

	// Test 1: External dep is not satisfied - issue should be blocked
	ready, err := mainStore.GetReadyWork(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetReadyWork failed: %v", err)
	}

	// Debug: log what we got
	for _, issue := range ready {
		t.Logf("Ready issue: %s - %s", issue.ID, issue.Title)
	}

	// Should only have the regular issue (external dep not satisfied)
	if len(ready) != 1 {
		t.Errorf("Expected 1 ready issue (external dep not satisfied), got %d", len(ready))
	}
	if len(ready) > 0 && ready[0].ID != regularIssue.ID {
		t.Errorf("Expected regular issue %s to be ready, got %s", regularIssue.ID, ready[0].ID)
	}

	// Test 2: Ship the capability in external project
	// Create an issue with provides:test-capability label and close it
	capabilityIssue := &types.Issue{
		Title:     "Ship test-capability",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := externalStore.CreateIssue(ctx, capabilityIssue, "test-user"); err != nil {
		t.Fatalf("failed to create capability issue: %v", err)
	}

	// Add the provides: label
	if err := externalStore.AddLabel(ctx, capabilityIssue.ID, "provides:test-capability", "test-user"); err != nil {
		t.Fatalf("failed to add provides label: %v", err)
	}

	// Close the capability issue
	if err := externalStore.CloseIssue(ctx, capabilityIssue.ID, "Shipped", "test-user", ""); err != nil {
		t.Fatalf("failed to close capability issue: %v", err)
	}

	// Debug: verify the capability issue was properly set up
	capIssue, err := externalStore.GetIssue(ctx, capabilityIssue.ID)
	if err != nil {
		t.Fatalf("failed to get capability issue: %v", err)
	}
	t.Logf("Capability issue status: %s", capIssue.Status)
	labels, _ := externalStore.GetLabels(ctx, capabilityIssue.ID)
	t.Logf("Capability issue labels: %v", labels)

	// Close external store to checkpoint WAL before read-only access
	externalStore.Close()

	// Debug: check what path configfile.Load returns
	testCfg, _ := configfile.Load(beadsDir)
	if testCfg != nil {
		t.Logf("Config database path: %s", testCfg.DatabasePath(beadsDir))
		t.Logf("External DB path we created: %s", externalDBPath)
	}

	// Re-verify: manually check the external dep
	status := CheckExternalDep(ctx, "external:external-test:test-capability")
	t.Logf("External dep check: satisfied=%v, reason=%s", status.Satisfied, status.Reason)

	// Now the external dep should be satisfied
	ready, err = mainStore.GetReadyWork(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetReadyWork failed after shipping: %v", err)
	}

	// Should now have both issues
	if len(ready) != 2 {
		t.Errorf("Expected 2 ready issues (external dep now satisfied), got %d", len(ready))
		for _, issue := range ready {
			t.Logf("Ready issue after shipping: %s - %s", issue.ID, issue.Title)
		}
	}

	// Verify both issues are present
	foundExtDep := false
	foundRegular := false
	for _, issue := range ready {
		if issue.ID == issueWithExtDep.ID {
			foundExtDep = true
		}
		if issue.ID == regularIssue.ID {
			foundRegular = true
		}
	}
	if !foundExtDep {
		t.Error("Issue with external dep should now be ready")
	}
	if !foundRegular {
		t.Error("Regular issue should still be ready")
	}
}

// TestGetReadyWorkNoExternalProjectsConfigured tests that GetReadyWork
// works normally when no external_projects are configured (bd-zmmy)
func TestGetReadyWorkNoExternalProjectsConfigured(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Initialize config if not already done
	if err := config.Initialize(); err != nil {
		t.Fatalf("failed to initialize config: %v", err)
	}

	// Ensure no external_projects configured
	oldProjects := config.GetExternalProjects()
	defer func() {
		if oldProjects != nil {
			config.Set("external_projects", oldProjects)
		}
	}()
	config.Set("external_projects", map[string]string{})

	// Create an issue with an external dependency (shouldn't matter since no config)
	issue := &types.Issue{
		Title:     "Has external dep but no config",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "test-user"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	// Add external dependency (will be ignored since no external_projects configured)
	extDep := &types.Dependency{
		IssueID:     issue.ID,
		DependsOnID: "external:unconfigured-project:some-capability",
		Type:        types.DepBlocks,
	}
	if err := store.AddDependency(ctx, extDep, "test-user"); err != nil {
		t.Fatalf("failed to add external dependency: %v", err)
	}

	// Should skip external dep checking since no external_projects configured
	ready, err := store.GetReadyWork(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetReadyWork failed: %v", err)
	}

	// Issue should be ready (external deps skipped when no config)
	if len(ready) != 1 {
		t.Errorf("Expected 1 ready issue (external deps skipped), got %d", len(ready))
	}
}

// TestGetBlockedIssuesFiltersExternalDeps tests that GetBlockedIssues filters
// satisfied external dependencies from BlockedBy lists (bd-396j)
func TestGetBlockedIssuesFiltersExternalDeps(t *testing.T) {
	// Create main test database
	mainStore, mainCleanup := setupTestDB(t)
	defer mainCleanup()

	ctx := context.Background()

	// Create external project directory with beads database
	externalDir, err := os.MkdirTemp("", "beads-blocked-external-test-*")
	if err != nil {
		t.Fatalf("failed to create external temp dir: %v", err)
	}
	defer os.RemoveAll(externalDir)

	// Create .beads directory and config in external project
	beadsDir := filepath.Join(externalDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	// Create config file for external project
	cfg := configfile.DefaultConfig()
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("failed to save external config: %v", err)
	}

	// Create external database
	externalDBPath := filepath.Join(beadsDir, "beads.db")
	externalStore, err := New(ctx, externalDBPath)
	if err != nil {
		t.Fatalf("failed to create external store: %v", err)
	}
	defer externalStore.Close()

	// Set issue_prefix in external store
	if err := externalStore.SetConfig(ctx, "issue_prefix", "ext"); err != nil {
		t.Fatalf("failed to set external issue_prefix: %v", err)
	}

	// Initialize config if not already done
	if err := config.Initialize(); err != nil {
		t.Fatalf("failed to initialize config: %v", err)
	}

	// Configure external_projects
	oldProjects := config.GetExternalProjects()
	defer func() {
		if oldProjects != nil {
			config.Set("external_projects", oldProjects)
		} else {
			config.Set("external_projects", map[string]string{})
		}
	}()

	config.Set("external_projects", map[string]string{
		"external-test": externalDir,
	})

	// Create an issue with external dependency
	issueWithExtDep := &types.Issue{
		Title:     "Blocked by external",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := mainStore.CreateIssue(ctx, issueWithExtDep, "test-user"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	// Add external dependency
	extDep := &types.Dependency{
		IssueID:     issueWithExtDep.ID,
		DependsOnID: "external:external-test:test-capability",
		Type:        types.DepBlocks,
	}
	if err := mainStore.AddDependency(ctx, extDep, "test-user"); err != nil {
		t.Fatalf("failed to add external dependency: %v", err)
	}

	// Test 1: External dep not satisfied - issue should appear as blocked
	blocked, err := mainStore.GetBlockedIssues(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetBlockedIssues failed: %v", err)
	}

	if len(blocked) != 1 {
		t.Errorf("Expected 1 blocked issue (external dep not satisfied), got %d", len(blocked))
	}
	if len(blocked) > 0 {
		if len(blocked[0].BlockedBy) != 1 || blocked[0].BlockedBy[0] != "external:external-test:test-capability" {
			t.Errorf("Expected BlockedBy to contain external ref, got %v", blocked[0].BlockedBy)
		}
	}

	// Test 2: Ship the capability in external project
	capabilityIssue := &types.Issue{
		Title:     "Ship test-capability",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := externalStore.CreateIssue(ctx, capabilityIssue, "test-user"); err != nil {
		t.Fatalf("failed to create capability issue: %v", err)
	}

	// Add the provides: label
	if err := externalStore.AddLabel(ctx, capabilityIssue.ID, "provides:test-capability", "test-user"); err != nil {
		t.Fatalf("failed to add provides label: %v", err)
	}

	// Close the capability issue
	if err := externalStore.CloseIssue(ctx, capabilityIssue.ID, "Shipped", "test-user", ""); err != nil {
		t.Fatalf("failed to close capability issue: %v", err)
	}

	// Close external store to checkpoint WAL before read-only access
	externalStore.Close()

	// Verify external dep is now satisfied
	status := CheckExternalDep(ctx, "external:external-test:test-capability")
	if !status.Satisfied {
		t.Fatalf("Expected external dep to be satisfied, got: %s", status.Reason)
	}

	// Now GetBlockedIssues should NOT show the issue (external dep satisfied)
	blocked, err = mainStore.GetBlockedIssues(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetBlockedIssues failed after shipping: %v", err)
	}

	// Issue should no longer be blocked
	if len(blocked) != 0 {
		t.Errorf("Expected 0 blocked issues (external dep now satisfied), got %d", len(blocked))
		for _, b := range blocked {
			t.Logf("Still blocked: %s - %s, blockers: %v", b.ID, b.Title, b.BlockedBy)
		}
	}
}

// TestGetBlockedIssuesPartialExternalDeps tests that GetBlockedIssues keeps
// issues blocked when only SOME external deps are satisfied (bd-396j)
func TestGetBlockedIssuesPartialExternalDeps(t *testing.T) {
	// Create main test database
	mainStore, mainCleanup := setupTestDB(t)
	defer mainCleanup()

	ctx := context.Background()

	// Create external project directory
	externalDir, err := os.MkdirTemp("", "beads-blocked-partial-test-*")
	if err != nil {
		t.Fatalf("failed to create external temp dir: %v", err)
	}
	defer os.RemoveAll(externalDir)

	// Create .beads directory and config in external project
	beadsDir := filepath.Join(externalDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	cfg := configfile.DefaultConfig()
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("failed to save external config: %v", err)
	}

	externalDBPath := filepath.Join(beadsDir, "beads.db")
	externalStore, err := New(ctx, externalDBPath)
	if err != nil {
		t.Fatalf("failed to create external store: %v", err)
	}
	defer externalStore.Close()

	if err := externalStore.SetConfig(ctx, "issue_prefix", "ext"); err != nil {
		t.Fatalf("failed to set external issue_prefix: %v", err)
	}

	if err := config.Initialize(); err != nil {
		t.Fatalf("failed to initialize config: %v", err)
	}

	oldProjects := config.GetExternalProjects()
	defer func() {
		if oldProjects != nil {
			config.Set("external_projects", oldProjects)
		} else {
			config.Set("external_projects", map[string]string{})
		}
	}()

	config.Set("external_projects", map[string]string{
		"external-test": externalDir,
	})

	// Create an issue with TWO external dependencies
	issueWithExtDeps := &types.Issue{
		Title:     "Blocked by two external deps",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := mainStore.CreateIssue(ctx, issueWithExtDeps, "test-user"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	// Add first external dependency
	if err := mainStore.AddDependency(ctx, &types.Dependency{
		IssueID:     issueWithExtDeps.ID,
		DependsOnID: "external:external-test:cap1",
		Type:        types.DepBlocks,
	}, "test-user"); err != nil {
		t.Fatalf("failed to add first external dependency: %v", err)
	}

	// Add second external dependency
	if err := mainStore.AddDependency(ctx, &types.Dependency{
		IssueID:     issueWithExtDeps.ID,
		DependsOnID: "external:external-test:cap2",
		Type:        types.DepBlocks,
	}, "test-user"); err != nil {
		t.Fatalf("failed to add second external dependency: %v", err)
	}

	// Ship only the first capability
	cap1Issue := &types.Issue{
		Title:     "Ship cap1",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := externalStore.CreateIssue(ctx, cap1Issue, "test-user"); err != nil {
		t.Fatalf("failed to create cap1 issue: %v", err)
	}
	if err := externalStore.AddLabel(ctx, cap1Issue.ID, "provides:cap1", "test-user"); err != nil {
		t.Fatalf("failed to add provides label: %v", err)
	}
	if err := externalStore.CloseIssue(ctx, cap1Issue.ID, "Shipped", "test-user", ""); err != nil {
		t.Fatalf("failed to close cap1 issue: %v", err)
	}

	// Close external store to checkpoint WAL
	externalStore.Close()

	// Issue should still be blocked (cap2 not satisfied)
	blocked, err := mainStore.GetBlockedIssues(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetBlockedIssues failed: %v", err)
	}

	if len(blocked) != 1 {
		t.Errorf("Expected 1 blocked issue (cap2 still not satisfied), got %d", len(blocked))
	}
	if len(blocked) > 0 {
		// Should only show cap2 in BlockedBy (cap1 is satisfied and filtered out)
		if len(blocked[0].BlockedBy) != 1 {
			t.Errorf("Expected 1 blocker (cap2), got %d: %v", len(blocked[0].BlockedBy), blocked[0].BlockedBy)
		}
		if len(blocked[0].BlockedBy) == 1 && blocked[0].BlockedBy[0] != "external:external-test:cap2" {
			t.Errorf("Expected BlockedBy to be cap2, got %v", blocked[0].BlockedBy)
		}
		if blocked[0].BlockedByCount != 1 {
			t.Errorf("Expected BlockedByCount to be 1, got %d", blocked[0].BlockedByCount)
		}
	}
}

// TestCheckExternalDepNoBeadsDirectory verifies that CheckExternalDep
// correctly reports "no beads database" when the target project exists
// but has no .beads directory (bd-mv6h).
func TestCheckExternalDepNoBeadsDirectory(t *testing.T) {
	ctx := context.Background()

	// Create a project directory WITHOUT .beads
	projectDir, err := os.MkdirTemp("", "beads-no-beads-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(projectDir)

	// Initialize config if not already done
	if err := config.Initialize(); err != nil {
		t.Fatalf("failed to initialize config: %v", err)
	}

	// Configure external_projects to point to the directory
	oldProjects := config.GetExternalProjects()
	defer func() {
		if oldProjects != nil {
			config.Set("external_projects", oldProjects)
		} else {
			config.Set("external_projects", map[string]string{})
		}
	}()

	config.Set("external_projects", map[string]string{
		"no-beads-project": projectDir,
	})

	// Check the external dep - should report "no beads database"
	status := CheckExternalDep(ctx, "external:no-beads-project:some-capability")

	if status.Satisfied {
		t.Error("Expected external dep to be unsatisfied when target has no .beads directory")
	}
	if status.Reason != "project has no beads database" {
		t.Errorf("Expected reason 'project has no beads database', got: %s", status.Reason)
	}
}

// TestCheckExternalDepInvalidFormats verifies that CheckExternalDep
// correctly handles various invalid external ref formats (bd-mv6h).
func TestCheckExternalDepInvalidFormats(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name       string
		ref        string
		wantReason string
	}{
		{
			name:       "not external prefix",
			ref:        "bd-xyz",
			wantReason: "not an external reference",
		},
		{
			name:       "missing capability",
			ref:        "external:project",
			wantReason: "invalid format (expected external:project:capability)",
		},
		{
			name:       "empty project",
			ref:        "external::capability",
			wantReason: "missing project or capability",
		},
		{
			name:       "empty capability",
			ref:        "external:project:",
			wantReason: "missing project or capability",
		},
		{
			name:       "only external prefix",
			ref:        "external:",
			wantReason: "invalid format (expected external:project:capability)",
		},
		{
			name:       "unconfigured project",
			ref:        "external:unconfigured-project:capability",
			wantReason: "project not configured in external_projects",
		},
	}

	// Initialize config if not already done
	if err := config.Initialize(); err != nil {
		t.Fatalf("failed to initialize config: %v", err)
	}

	// Ensure no external projects are configured for some tests
	oldProjects := config.GetExternalProjects()
	defer func() {
		if oldProjects != nil {
			config.Set("external_projects", oldProjects)
		}
	}()
	config.Set("external_projects", map[string]string{})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := CheckExternalDep(ctx, tt.ref)
			if status.Satisfied {
				t.Errorf("Expected unsatisfied for %q", tt.ref)
			}
			if status.Reason != tt.wantReason {
				t.Errorf("Expected reason %q, got %q", tt.wantReason, status.Reason)
			}
		})
	}
}

// TestCheckExternalDepsBatching verifies that CheckExternalDeps correctly
// batches multiple refs to the same project and deduplicates refs (bd-687v).
func TestCheckExternalDepsBatching(t *testing.T) {
	ctx := context.Background()

	// Initialize config (required for config.Set to work)
	if err := config.Initialize(); err != nil {
		t.Fatalf("failed to initialize config: %v", err)
	}

	// Create external project directory with beads database
	externalDir, err := os.MkdirTemp("", "beads-batch-test-*")
	if err != nil {
		t.Fatalf("failed to create external temp dir: %v", err)
	}
	defer os.RemoveAll(externalDir)

	// Create .beads directory and config
	beadsDir := filepath.Join(externalDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create beads dir: %v", err)
	}
	cfg := configfile.DefaultConfig()
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	// Create external database
	externalDBPath := filepath.Join(beadsDir, "beads.db")
	externalStore, err := New(ctx, externalDBPath)
	if err != nil {
		t.Fatalf("failed to create external store: %v", err)
	}

	if err := externalStore.SetConfig(ctx, "issue_prefix", "ext"); err != nil {
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	// Ship capability "cap1" (closed issue with provides:cap1 label)
	cap1Issue := &types.Issue{
		ID:        "ext-cap1",
		Title:     "Capability 1",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeFeature,
	}
	if err := externalStore.CreateIssue(ctx, cap1Issue, "test-user"); err != nil {
		t.Fatalf("failed to create cap1 issue: %v", err)
	}
	if err := externalStore.AddLabel(ctx, cap1Issue.ID, "provides:cap1", "test-user"); err != nil {
		t.Fatalf("failed to add provides:cap1 label: %v", err)
	}
	if err := externalStore.CloseIssue(ctx, cap1Issue.ID, "Shipped", "test-user", ""); err != nil {
		t.Fatalf("failed to close cap1 issue: %v", err)
	}

	// Ship capability "cap2"
	cap2Issue := &types.Issue{
		ID:        "ext-cap2",
		Title:     "Capability 2",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeFeature,
	}
	if err := externalStore.CreateIssue(ctx, cap2Issue, "test-user"); err != nil {
		t.Fatalf("failed to create cap2 issue: %v", err)
	}
	if err := externalStore.AddLabel(ctx, cap2Issue.ID, "provides:cap2", "test-user"); err != nil {
		t.Fatalf("failed to add provides:cap2 label: %v", err)
	}
	if err := externalStore.CloseIssue(ctx, cap2Issue.ID, "Shipped", "test-user", ""); err != nil {
		t.Fatalf("failed to close cap2 issue: %v", err)
	}

	// Close to checkpoint WAL before read-only access
	externalStore.Close()

	// Configure external_projects
	oldProjects := config.GetExternalProjects()
	t.Cleanup(func() {
		if oldProjects != nil {
			config.Set("external_projects", oldProjects)
		} else {
			config.Set("external_projects", map[string]string{})
		}
	})
	config.Set("external_projects", map[string]string{
		"batch-test": externalDir,
	})

	// Test: Check multiple refs including duplicates and mixed satisfied/unsatisfied
	refs := []string{
		"external:batch-test:cap1",           // satisfied
		"external:batch-test:cap2",           // satisfied
		"external:batch-test:cap3",           // NOT satisfied
		"external:batch-test:cap1",           // duplicate - should still work
		"external:unconfigured-project:cap1", // unconfigured project
		"invalid-ref",                        // invalid format
	}

	statuses := CheckExternalDeps(ctx, refs)

	// Verify we got results for all unique refs (5 unique, since cap1 appears twice)
	expectedUnique := 5
	if len(statuses) != expectedUnique {
		t.Errorf("Expected %d unique statuses, got %d", expectedUnique, len(statuses))
	}

	// cap1 should be satisfied
	if s := statuses["external:batch-test:cap1"]; s == nil || !s.Satisfied {
		t.Error("Expected external:batch-test:cap1 to be satisfied")
	}

	// cap2 should be satisfied
	if s := statuses["external:batch-test:cap2"]; s == nil || !s.Satisfied {
		t.Error("Expected external:batch-test:cap2 to be satisfied")
	}

	// cap3 should NOT be satisfied
	if s := statuses["external:batch-test:cap3"]; s == nil || s.Satisfied {
		t.Error("Expected external:batch-test:cap3 to be unsatisfied")
	}

	// unconfigured project should NOT be satisfied
	if s := statuses["external:unconfigured-project:cap1"]; s == nil || s.Satisfied {
		t.Error("Expected external:unconfigured-project:cap1 to be unsatisfied")
	}

	// invalid ref should NOT be satisfied
	if s := statuses["invalid-ref"]; s == nil || s.Satisfied {
		t.Error("Expected invalid-ref to be unsatisfied")
	}
}

// TestGetNewlyUnblockedByClose tests the --suggest-next functionality (GH#679)
func TestGetNewlyUnblockedByClose(t *testing.T) {
	env := newTestEnv(t)

	// Create a blocker issue
	blocker := env.CreateIssueWith("Blocker", types.StatusOpen, 1, types.TypeTask)

	// Create two issues blocked by the blocker
	blocked1 := env.CreateIssueWith("Blocked 1", types.StatusOpen, 2, types.TypeTask)
	blocked2 := env.CreateIssueWith("Blocked 2", types.StatusOpen, 3, types.TypeTask)

	// Create one issue blocked by multiple issues (blocker + another)
	otherBlocker := env.CreateIssueWith("Other Blocker", types.StatusOpen, 1, types.TypeTask)
	multiBlocked := env.CreateIssueWith("Multi Blocked", types.StatusOpen, 2, types.TypeTask)

	// Add dependencies (issue depends on blocker)
	env.AddDep(blocked1, blocker)
	env.AddDep(blocked2, blocker)
	env.AddDep(multiBlocked, blocker)
	env.AddDep(multiBlocked, otherBlocker)

	// Close the blocker
	env.Close(blocker, "Done")

	// Get newly unblocked issues
	ctx := context.Background()
	unblocked, err := env.Store.GetNewlyUnblockedByClose(ctx, blocker.ID)
	if err != nil {
		t.Fatalf("GetNewlyUnblockedByClose failed: %v", err)
	}

	// Should return blocked1 and blocked2 (but not multiBlocked, which is still blocked by otherBlocker)
	if len(unblocked) != 2 {
		t.Errorf("Expected 2 unblocked issues, got %d", len(unblocked))
	}

	// Check that the right issues are unblocked
	unblockedIDs := make(map[string]bool)
	for _, issue := range unblocked {
		unblockedIDs[issue.ID] = true
	}

	if !unblockedIDs[blocked1.ID] {
		t.Errorf("Expected %s to be unblocked", blocked1.ID)
	}
	if !unblockedIDs[blocked2.ID] {
		t.Errorf("Expected %s to be unblocked", blocked2.ID)
	}
	if unblockedIDs[multiBlocked.ID] {
		t.Errorf("Expected %s to still be blocked (has another blocker)", multiBlocked.ID)
	}
}

// TestParentIDFilterDescendants tests that ParentID filter returns all descendants of an epic
func TestParentIDFilterDescendants(t *testing.T) {
	env := newTestEnv(t)

	// Create hierarchy:
	// epic1 (root)
	//   ├── task1 (child of epic1)
	//   ├── task2 (child of epic1)
	//   └── epic2 (child of epic1)
	//         └── task3 (grandchild of epic1)
	// task4 (unrelated, should not appear in results)
	epic1 := env.CreateEpic("Epic 1")
	task1 := env.CreateIssue("Task 1")
	task2 := env.CreateIssue("Task 2")
	epic2 := env.CreateEpic("Epic 2")
	task3 := env.CreateIssue("Task 3")
	task4 := env.CreateIssue("Task 4 - unrelated")

	env.AddParentChild(task1, epic1)
	env.AddParentChild(task2, epic1)
	env.AddParentChild(epic2, epic1)
	env.AddParentChild(task3, epic2)

	// Query with ParentID = epic1
	parentID := epic1.ID
	ready := env.GetReadyWork(types.WorkFilter{ParentID: &parentID})

	// Should include task1, task2, epic2, task3 (all descendants of epic1)
	// Should NOT include epic1 itself or task4
	if len(ready) != 4 {
		t.Fatalf("Expected 4 ready issues in parent scope, got %d", len(ready))
	}

	// Verify the returned issues are the expected ones
	readyIDs := make(map[string]bool)
	for _, issue := range ready {
		readyIDs[issue.ID] = true
	}

	if !readyIDs[task1.ID] {
		t.Errorf("Expected task1 to be in results")
	}
	if !readyIDs[task2.ID] {
		t.Errorf("Expected task2 to be in results")
	}
	if !readyIDs[epic2.ID] {
		t.Errorf("Expected epic2 to be in results")
	}
	if !readyIDs[task3.ID] {
		t.Errorf("Expected task3 to be in results")
	}
	if readyIDs[epic1.ID] {
		t.Errorf("Expected epic1 (root) to NOT be in results")
	}
	if readyIDs[task4.ID] {
		t.Errorf("Expected task4 (unrelated) to NOT be in results")
	}
}

// TestParentIDWithOtherFilters tests that ParentID can be combined with other filters
func TestParentIDWithOtherFilters(t *testing.T) {
	env := newTestEnv(t)

	// Create hierarchy:
	// epic1 (root)
	//   ├── task1 (priority 0)
	//   ├── task2 (priority 1)
	//   └── task3 (priority 2)
	epic1 := env.CreateEpic("Epic 1")
	task1 := env.CreateIssueWith("Task 1 - P0", types.StatusOpen, 0, types.TypeTask)
	task2 := env.CreateIssueWith("Task 2 - P1", types.StatusOpen, 1, types.TypeTask)
	task3 := env.CreateIssueWith("Task 3 - P2", types.StatusOpen, 2, types.TypeTask)

	env.AddParentChild(task1, epic1)
	env.AddParentChild(task2, epic1)
	env.AddParentChild(task3, epic1)

	// Query with ParentID = epic1 AND priority = 1
	parentID := epic1.ID
	priority := 1
	ready := env.GetReadyWork(types.WorkFilter{ParentID: &parentID, Priority: &priority})

	// Should only include task2 (parent + priority 1)
	if len(ready) != 1 {
		t.Fatalf("Expected 1 issue with parent + priority filter, got %d", len(ready))
	}
	if ready[0].ID != task2.ID {
		t.Errorf("Expected task2, got %s", ready[0].ID)
	}
}

// TestParentIDWithBlockedDescendants tests that blocked descendants are excluded
func TestParentIDWithBlockedDescendants(t *testing.T) {
	env := newTestEnv(t)

	// Create hierarchy:
	// epic1 (root)
	//   ├── task1 (ready)
	//   ├── task2 (blocked by blocker)
	//   └── task3 (ready)
	// blocker (unrelated)
	epic1 := env.CreateEpic("Epic 1")
	task1 := env.CreateIssue("Task 1 - ready")
	task2 := env.CreateIssue("Task 2 - blocked")
	task3 := env.CreateIssue("Task 3 - ready")
	blocker := env.CreateIssue("Blocker")

	env.AddParentChild(task1, epic1)
	env.AddParentChild(task2, epic1)
	env.AddParentChild(task3, epic1)
	env.AddDep(task2, blocker) // task2 is blocked

	// Query with ParentID = epic1
	parentID := epic1.ID
	ready := env.GetReadyWork(types.WorkFilter{ParentID: &parentID})

	// Should include task1, task3 (ready descendants)
	// Should NOT include task2 (blocked)
	if len(ready) != 2 {
		t.Fatalf("Expected 2 ready descendants, got %d", len(ready))
	}

	readyIDs := make(map[string]bool)
	for _, issue := range ready {
		readyIDs[issue.ID] = true
	}

	if !readyIDs[task1.ID] {
		t.Errorf("Expected task1 to be ready")
	}
	if !readyIDs[task3.ID] {
		t.Errorf("Expected task3 to be ready")
	}
	if readyIDs[task2.ID] {
		t.Errorf("Expected task2 to be blocked")
	}
}

// TestParentIDEmptyParent tests that empty parent returns nothing
func TestParentIDEmptyParent(t *testing.T) {
	env := newTestEnv(t)

	// Create an epic with no children
	epic1 := env.CreateEpic("Epic 1 - no children")
	env.CreateIssue("Unrelated task")

	// Query with ParentID = epic1 (which has no children)
	parentID := epic1.ID
	ready := env.GetReadyWork(types.WorkFilter{ParentID: &parentID})

	// Should return empty since epic1 has no descendants
	if len(ready) != 0 {
		t.Fatalf("Expected 0 ready issues for empty parent, got %d", len(ready))
	}
}

// TestIsBlocked tests the IsBlocked method (GH#962)
func TestIsBlocked(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	// Create issues:
	// issue1: open, no dependencies → NOT BLOCKED
	// issue2: open, depends on issue1 (open) → BLOCKED by issue1
	// issue3: open, depends on issue4 (closed) → NOT BLOCKED (blocker is closed)
	issue1 := env.CreateIssue("Open No Deps")
	issue2 := env.CreateIssue("Blocked by open")
	issue3 := env.CreateIssue("Blocked by closed")
	issue4 := env.CreateIssue("Will be closed")

	env.AddDep(issue2, issue1) // issue2 depends on issue1 (open)
	env.AddDep(issue3, issue4) // issue3 depends on issue4

	env.Close(issue4, "Done") // Close issue4

	// Test issue1: not blocked
	blocked, blockers, err := env.Store.IsBlocked(ctx, issue1.ID)
	if err != nil {
		t.Fatalf("IsBlocked failed: %v", err)
	}
	if blocked {
		t.Errorf("Expected issue1 to NOT be blocked, got blocked=true with blockers=%v", blockers)
	}

	// Test issue2: blocked by issue1
	blocked, blockers, err = env.Store.IsBlocked(ctx, issue2.ID)
	if err != nil {
		t.Fatalf("IsBlocked failed: %v", err)
	}
	if !blocked {
		t.Error("Expected issue2 to be blocked")
	}
	if len(blockers) != 1 || blockers[0] != issue1.ID {
		t.Errorf("Expected blockers=[%s], got %v", issue1.ID, blockers)
	}

	// Test issue3: not blocked (issue4 is closed)
	blocked, blockers, err = env.Store.IsBlocked(ctx, issue3.ID)
	if err != nil {
		t.Fatalf("IsBlocked failed: %v", err)
	}
	if blocked {
		t.Errorf("Expected issue3 to NOT be blocked (blocker is closed), got blocked=true with blockers=%v", blockers)
	}
}

// TestGetReadyWorkExcludesMolSteps tests that molecule steps (IDs containing -mol-) are
// excluded from bd ready by default, but included when filtering by explicit type.
func TestGetReadyWorkExcludesMolSteps(t *testing.T) {
	env := newTestEnv(t)

	// Create regular tasks
	regularTask := env.CreateIssue("Regular task")

	// Create molecule steps (IDs contain -mol-)
	molStep1 := env.CreateIssueWithID("bd-mol-abc", "Mol step 1")
	molStep2 := env.CreateIssueWithID("bd-mol-xyz", "Mol step 2")

	// Default query should exclude mol steps
	ready := env.GetReadyWork(types.WorkFilter{})
	readyIDs := make(map[string]bool)
	for _, issue := range ready {
		readyIDs[issue.ID] = true
	}

	// Regular task should be included
	if !readyIDs[regularTask.ID] {
		t.Errorf("Expected regular task %s to be in ready work", regularTask.ID)
	}

	// Mol steps should be excluded
	if readyIDs[molStep1.ID] {
		t.Errorf("Expected mol step %s to be EXCLUDED from ready work by default", molStep1.ID)
	}
	if readyIDs[molStep2.ID] {
		t.Errorf("Expected mol step %s to be EXCLUDED from ready work by default", molStep2.ID)
	}

	// Explicit type=task filter should include mol steps
	readyWithType := env.GetReadyWork(types.WorkFilter{Type: "task"})
	readyWithTypeIDs := make(map[string]bool)
	for _, issue := range readyWithType {
		readyWithTypeIDs[issue.ID] = true
	}

	// All tasks should be included when filtering by type
	if !readyWithTypeIDs[regularTask.ID] {
		t.Errorf("Expected regular task %s to be in ready work with --type=task", regularTask.ID)
	}
	if !readyWithTypeIDs[molStep1.ID] {
		t.Errorf("Expected mol step %s to be INCLUDED with --type=task filter", molStep1.ID)
	}
	if !readyWithTypeIDs[molStep2.ID] {
		t.Errorf("Expected mol step %s to be INCLUDED with --type=task filter", molStep2.ID)
	}
}

// TestGetReadyWorkExcludeIDPatternsConfig tests custom exclusion patterns via config.
func TestGetReadyWorkExcludeIDPatternsConfig(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Ctx

	// Create issues with various ID patterns
	regularTask := env.CreateIssue("Regular task")
	molStep := env.CreateIssueWithID("bd-mol-abc", "Mol step")
	roleStep := env.CreateIssueWithID("bd-role-xyz", "Role step")
	wispStep := env.CreateIssueWithID("bd-wisp-123", "Wisp step")

	// Default config excludes -mol- and -wisp-
	ready := env.GetReadyWork(types.WorkFilter{})
	readyIDs := make(map[string]bool)
	for _, issue := range ready {
		readyIDs[issue.ID] = true
	}

	if !readyIDs[regularTask.ID] {
		t.Errorf("Expected regular task to be included")
	}
	if readyIDs[molStep.ID] {
		t.Errorf("Expected mol step to be excluded by default")
	}
	if readyIDs[wispStep.ID] {
		t.Errorf("Expected wisp step to be excluded by default")
	}
	if !readyIDs[roleStep.ID] {
		t.Errorf("Expected role step to be included (not in default patterns)")
	}

	// Configure custom patterns to also exclude -role-
	if err := env.Store.SetConfig(ctx, ExcludeIDPatternsConfigKey, "-mol-,-wisp-,-role-"); err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}

	ready2 := env.GetReadyWork(types.WorkFilter{})
	readyIDs2 := make(map[string]bool)
	for _, issue := range ready2 {
		readyIDs2[issue.ID] = true
	}

	if !readyIDs2[regularTask.ID] {
		t.Errorf("Expected regular task to be included with custom config")
	}
	if readyIDs2[molStep.ID] {
		t.Errorf("Expected mol step to be excluded with custom config")
	}
	if readyIDs2[wispStep.ID] {
		t.Errorf("Expected wisp step to be excluded with custom config")
	}
	if readyIDs2[roleStep.ID] {
		t.Errorf("Expected role step to be excluded with custom config")
	}

	// IncludeMolSteps should bypass all pattern exclusions
	ready3 := env.GetReadyWork(types.WorkFilter{IncludeMolSteps: true})
	readyIDs3 := make(map[string]bool)
	for _, issue := range ready3 {
		readyIDs3[issue.ID] = true
	}

	if !readyIDs3[regularTask.ID] {
		t.Errorf("Expected regular task with IncludeMolSteps")
	}
	if !readyIDs3[molStep.ID] {
		t.Errorf("Expected mol step with IncludeMolSteps: true")
	}
	if !readyIDs3[roleStep.ID] {
		t.Errorf("Expected role step with IncludeMolSteps: true")
	}
	// Note: wisp step is excluded by ephemeral flag, not pattern, so it stays excluded
}
