package sqlite

import (
	"context"
	"strconv"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestDetectCyclesSimple tests simple 2-node cycles
func TestDetectCyclesSimple(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create two issues
	issue1 := &types.Issue{Title: "Issue 1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issue2 := &types.Issue{Title: "Issue 2", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

	if err := store.CreateIssue(ctx, issue1, "test-user"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}
	if err := store.CreateIssue(ctx, issue2, "test-user"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Manually create a cycle by inserting directly into dependencies table
	// (bypassing AddDependency's cycle prevention)
	_, err := store.db.ExecContext(ctx, `
		INSERT INTO dependencies (issue_id, depends_on_id, type, created_by, created_at)
		VALUES (?, ?, ?, 'test-user', CURRENT_TIMESTAMP)
	`, issue1.ID, issue2.ID, types.DepBlocks)
	if err != nil {
		t.Fatalf("Insert dependency failed: %v", err)
	}

	_, err = store.db.ExecContext(ctx, `
		INSERT INTO dependencies (issue_id, depends_on_id, type, created_by, created_at)
		VALUES (?, ?, ?, 'test-user', CURRENT_TIMESTAMP)
	`, issue2.ID, issue1.ID, types.DepBlocks)
	if err != nil {
		t.Fatalf("Insert dependency failed: %v", err)
	}

	// Detect cycles
	cycles, err := store.DetectCycles(ctx)
	if err != nil {
		t.Fatalf("DetectCycles failed: %v", err)
	}

	if len(cycles) == 0 {
		t.Fatal("Expected to detect a cycle, but found none")
	}

	// Verify the cycle contains both issues
	cycle := cycles[0]
	if len(cycle) != 2 {
		t.Logf("Cycle issues: %v", cycle)
		for i, iss := range cycle {
			t.Logf("  [%d] ID=%s Title=%s", i, iss.ID, iss.Title)
		}
		t.Errorf("Expected cycle of length 2, got %d", len(cycle))
	}

	// Verify both issues are in the cycle
	foundIDs := make(map[string]bool)
	for _, issue := range cycle {
		foundIDs[issue.ID] = true
	}

	if !foundIDs[issue1.ID] || !foundIDs[issue2.ID] {
		t.Errorf("Cycle missing expected issues. Got: %v", foundIDs)
	}
}

// TestDetectCyclesComplex tests a more complex multi-node cycle
func TestDetectCyclesComplex(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create a 4-node cycle: A → B → C → D → A
	issues := make([]*types.Issue, 4)
	for i := 0; i < 4; i++ {
		issues[i] = &types.Issue{
			Title:     "Issue " + string(rune('A'+i)),
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issues[i], "test-user"); err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}
	}

	// Create cycle: 0→1→2→3→0
	for i := 0; i < 4; i++ {
		nextIdx := (i + 1) % 4
		_, err := store.db.ExecContext(ctx, `
			INSERT INTO dependencies (issue_id, depends_on_id, type, created_by, created_at)
			VALUES (?, ?, ?, 'test-user', CURRENT_TIMESTAMP)
		`, issues[i].ID, issues[nextIdx].ID, types.DepBlocks)
		if err != nil {
			t.Fatalf("Insert dependency failed: %v", err)
		}
	}

	// Detect cycles
	cycles, err := store.DetectCycles(ctx)
	if err != nil {
		t.Fatalf("DetectCycles failed: %v", err)
	}

	if len(cycles) == 0 {
		t.Fatal("Expected to detect a cycle, but found none")
	}

	// Verify the cycle contains all 4 issues
	cycle := cycles[0]
	if len(cycle) != 4 {
		t.Errorf("Expected cycle of length 4, got %d", len(cycle))
	}

	// Verify all issues are in the cycle
	foundIDs := make(map[string]bool)
	for _, issue := range cycle {
		foundIDs[issue.ID] = true
	}

	for _, issue := range issues {
		if !foundIDs[issue.ID] {
			t.Errorf("Cycle missing issue %s", issue.ID)
		}
	}
}

// TestDetectCyclesSelfLoop tests detection of self-loops (A → A)
func TestDetectCyclesSelfLoop(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	issue := &types.Issue{Title: "Self Loop", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, issue, "test-user"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Create self-loop
	_, err := store.db.ExecContext(ctx, `
		INSERT INTO dependencies (issue_id, depends_on_id, type, created_by, created_at)
		VALUES (?, ?, ?, 'test-user', CURRENT_TIMESTAMP)
	`, issue.ID, issue.ID, types.DepBlocks)
	if err != nil {
		t.Fatalf("Insert dependency failed: %v", err)
	}

	// Detect cycles
	cycles, err := store.DetectCycles(ctx)
	if err != nil {
		t.Fatalf("DetectCycles failed: %v", err)
	}

	if len(cycles) == 0 {
		t.Fatal("Expected to detect a self-loop cycle, but found none")
	}

	// Verify the cycle contains the issue
	cycle := cycles[0]
	if len(cycle) != 1 {
		t.Errorf("Expected self-loop cycle of length 1, got %d", len(cycle))
	}

	if cycle[0].ID != issue.ID {
		t.Errorf("Expected cycle to contain issue %s, got %s", issue.ID, cycle[0].ID)
	}
}

// TestDetectCyclesMultipleIndependent tests detection of multiple independent cycles
func TestDetectCyclesMultipleIndependent(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create two independent cycles:
	// Cycle 1: A → B → A
	// Cycle 2: C → D → C

	cycle1 := make([]*types.Issue, 2)
	cycle2 := make([]*types.Issue, 2)

	for i := 0; i < 2; i++ {
		cycle1[i] = &types.Issue{
			Title:     "Cycle1-" + string(rune('A'+i)),
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
		}
		cycle2[i] = &types.Issue{
			Title:     "Cycle2-" + string(rune('A'+i)),
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, cycle1[i], "test-user"); err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}
		if err := store.CreateIssue(ctx, cycle2[i], "test-user"); err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}
	}

	// Create first cycle: 0→1→0
	_, err := store.db.ExecContext(ctx, `
		INSERT INTO dependencies (issue_id, depends_on_id, type, created_by, created_at)
		VALUES (?, ?, ?, 'test-user', CURRENT_TIMESTAMP)
	`, cycle1[0].ID, cycle1[1].ID, types.DepBlocks)
	if err != nil {
		t.Fatalf("Insert dependency failed: %v", err)
	}
	_, err = store.db.ExecContext(ctx, `
		INSERT INTO dependencies (issue_id, depends_on_id, type, created_by, created_at)
		VALUES (?, ?, ?, 'test-user', CURRENT_TIMESTAMP)
	`, cycle1[1].ID, cycle1[0].ID, types.DepBlocks)
	if err != nil {
		t.Fatalf("Insert dependency failed: %v", err)
	}

	// Create second cycle: 0→1→0
	_, err = store.db.ExecContext(ctx, `
		INSERT INTO dependencies (issue_id, depends_on_id, type, created_by, created_at)
		VALUES (?, ?, ?, 'test-user', CURRENT_TIMESTAMP)
	`, cycle2[0].ID, cycle2[1].ID, types.DepBlocks)
	if err != nil {
		t.Fatalf("Insert dependency failed: %v", err)
	}
	_, err = store.db.ExecContext(ctx, `
		INSERT INTO dependencies (issue_id, depends_on_id, type, created_by, created_at)
		VALUES (?, ?, ?, 'test-user', CURRENT_TIMESTAMP)
	`, cycle2[1].ID, cycle2[0].ID, types.DepBlocks)
	if err != nil {
		t.Fatalf("Insert dependency failed: %v", err)
	}

	// Detect cycles
	cycles, err := store.DetectCycles(ctx)
	if err != nil {
		t.Fatalf("DetectCycles failed: %v", err)
	}

	// The SQL may detect the same cycle from different entry points,
	// so we might get more than 2 cycles reported. Verify we have at least 2.
	if len(cycles) < 2 {
		t.Errorf("Expected to detect at least 2 independent cycles, got %d", len(cycles))
	}

	// Verify we found cycles involving all 4 issues
	foundIssues := make(map[string]bool)
	for _, cycle := range cycles {
		for _, issue := range cycle {
			foundIssues[issue.ID] = true
		}
	}

	allCycleIssues := append(cycle1, cycle2...)
	for _, issue := range allCycleIssues {
		if !foundIssues[issue.ID] {
			t.Errorf("Cycle detection missing issue %s", issue.ID)
		}
	}
}

// TestDetectCyclesAcyclicGraph tests that no cycles are detected in an acyclic graph
func TestDetectCyclesAcyclicGraph(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create a DAG: A → B → C → D (no cycles)
	issues := make([]*types.Issue, 4)
	for i := 0; i < 4; i++ {
		issues[i] = &types.Issue{
			Title:     "Issue " + string(rune('A'+i)),
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issues[i], "test-user"); err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}
	}

	// Create chain: 0→1→2→3 (no cycle)
	for i := 0; i < 3; i++ {
		_, err := store.db.ExecContext(ctx, `
			INSERT INTO dependencies (issue_id, depends_on_id, type, created_by, created_at)
			VALUES (?, ?, ?, 'test-user', CURRENT_TIMESTAMP)
		`, issues[i].ID, issues[i+1].ID, types.DepBlocks)
		if err != nil {
			t.Fatalf("Insert dependency failed: %v", err)
		}
	}

	// Detect cycles
	cycles, err := store.DetectCycles(ctx)
	if err != nil {
		t.Fatalf("DetectCycles failed: %v", err)
	}

	if len(cycles) != 0 {
		t.Errorf("Expected no cycles in acyclic graph, but found %d", len(cycles))
	}
}

// TestDetectCyclesEmptyGraph tests cycle detection on empty graph
func TestDetectCyclesEmptyGraph(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Detect cycles on empty database
	cycles, err := store.DetectCycles(ctx)
	if err != nil {
		t.Fatalf("DetectCycles failed: %v", err)
	}

	if len(cycles) != 0 {
		t.Errorf("Expected no cycles in empty graph, but found %d", len(cycles))
	}
}

// TestDetectCyclesSingleNode tests cycle detection with a single node (no dependencies)
func TestDetectCyclesSingleNode(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create a single issue with no dependencies
	issue := &types.Issue{Title: "Lonely Issue", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, issue, "test-user"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Detect cycles
	cycles, err := store.DetectCycles(ctx)
	if err != nil {
		t.Fatalf("DetectCycles failed: %v", err)
	}

	if len(cycles) != 0 {
		t.Errorf("Expected no cycles for single node with no dependencies, but found %d", len(cycles))
	}
}

// TestDetectCyclesDiamond tests cycle detection in a diamond pattern (no cycle)
func TestDetectCyclesDiamond(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create a diamond pattern: A → B → D, A → C → D (no cycle)
	issues := make([]*types.Issue, 4)
	names := []string{"A", "B", "C", "D"}
	for i := 0; i < 4; i++ {
		issues[i] = &types.Issue{
			Title:     "Issue " + names[i],
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issues[i], "test-user"); err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}
	}

	// Create dependencies: A→B, A→C, B→D, C→D
	deps := [][2]int{{0, 1}, {0, 2}, {1, 3}, {2, 3}}
	for _, dep := range deps {
		_, err := store.db.ExecContext(ctx, `
			INSERT INTO dependencies (issue_id, depends_on_id, type, created_by, created_at)
			VALUES (?, ?, ?, 'test-user', CURRENT_TIMESTAMP)
		`, issues[dep[0]].ID, issues[dep[1]].ID, types.DepBlocks)
		if err != nil {
			t.Fatalf("Insert dependency failed: %v", err)
		}
	}

	// Detect cycles
	cycles, err := store.DetectCycles(ctx)
	if err != nil {
		t.Fatalf("DetectCycles failed: %v", err)
	}

	if len(cycles) != 0 {
		t.Errorf("Expected no cycles in diamond pattern, but found %d", len(cycles))
	}
}

// TestDetectCyclesLongCycle tests detection of a long cycle (10 nodes)
func TestDetectCyclesLongCycle(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create a 10-node cycle
	const cycleLength = 10
	issues := make([]*types.Issue, cycleLength)
	for i := 0; i < cycleLength; i++ {
		issues[i] = &types.Issue{
			Title:     "Issue " + strconv.Itoa(i),
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issues[i], "test-user"); err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}
	}

	// Create cycle: 0→1→2→...→9→0
	for i := 0; i < cycleLength; i++ {
		nextIdx := (i + 1) % cycleLength
		_, err := store.db.ExecContext(ctx, `
			INSERT INTO dependencies (issue_id, depends_on_id, type, created_by, created_at)
			VALUES (?, ?, ?, 'test-user', CURRENT_TIMESTAMP)
		`, issues[i].ID, issues[nextIdx].ID, types.DepBlocks)
		if err != nil {
			t.Fatalf("Insert dependency failed: %v", err)
		}
	}

	// Detect cycles
	cycles, err := store.DetectCycles(ctx)
	if err != nil {
		t.Fatalf("DetectCycles failed: %v", err)
	}

	if len(cycles) == 0 {
		t.Fatal("Expected to detect a cycle, but found none")
	}

	// Verify the cycle contains all 10 issues
	cycle := cycles[0]
	if len(cycle) != cycleLength {
		t.Errorf("Expected cycle of length %d, got %d", cycleLength, len(cycle))
	}
}

// TestDetectCyclesMixedTypes tests cycle detection with different dependency types
func TestDetectCyclesMixedTypes(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create a cycle using different dependency types
	issues := make([]*types.Issue, 3)
	for i := 0; i < 3; i++ {
		issues[i] = &types.Issue{
			Title:     "Issue " + string(rune('A'+i)),
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issues[i], "test-user"); err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}
	}

	// Create cycle with mixed types: A -blocks-> B -related-> C -parent-child-> A
	depTypes := []types.DependencyType{types.DepBlocks, types.DepRelated, types.DepParentChild}
	for i := 0; i < 3; i++ {
		nextIdx := (i + 1) % 3
		_, err := store.db.ExecContext(ctx, `
			INSERT INTO dependencies (issue_id, depends_on_id, type, created_by, created_at)
			VALUES (?, ?, ?, 'test-user', CURRENT_TIMESTAMP)
		`, issues[i].ID, issues[nextIdx].ID, depTypes[i])
		if err != nil {
			t.Fatalf("Insert dependency failed: %v", err)
		}
	}

	// Detect cycles
	cycles, err := store.DetectCycles(ctx)
	if err != nil {
		t.Fatalf("DetectCycles failed: %v", err)
	}

	if len(cycles) == 0 {
		t.Fatal("Expected to detect a cycle with mixed types, but found none")
	}

	// Verify the cycle contains all 3 issues
	cycle := cycles[0]
	if len(cycle) != 3 {
		t.Errorf("Expected cycle of length 3, got %d", len(cycle))
	}
}
