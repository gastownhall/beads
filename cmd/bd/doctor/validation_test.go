//go:build cgo

package doctor

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/types"
)

// TestCheckDuplicateIssues_ClosedIssuesExcluded verifies that closed issues
// are not flagged as duplicates (bug fix: bd-sali).
func TestCheckDuplicateIssues_ClosedIssuesExcluded(t *testing.T) {
	store := newTestDoltStore(t, "test")
	ctx := context.Background()

	issues := []*types.Issue{
		{Title: "mol-feature-dev", Description: "Molecule for feature", Status: types.StatusClosed, Priority: 2, IssueType: types.TypeTask},
		{Title: "mol-feature-dev", Description: "Molecule for feature", Status: types.StatusClosed, Priority: 2, IssueType: types.TypeTask},
		{Title: "mol-feature-dev", Description: "Molecule for feature", Status: types.StatusClosed, Priority: 2, IssueType: types.TypeTask},
	}

	for _, issue := range issues {
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}
	}

	check := checkDuplicateIssuesDB(store.DB(), false, 1000)

	if check.Status != StatusOK {
		t.Errorf("Status = %q, want %q (closed issues should be excluded)", check.Status, StatusOK)
		t.Logf("Message: %s", check.Message)
	}
}

// TestCheckDuplicateIssues_OpenDuplicatesDetected verifies that open issues
// with identical content ARE flagged as duplicates.
func TestCheckDuplicateIssues_OpenDuplicatesDetected(t *testing.T) {
	store := newTestDoltStore(t, "test")
	ctx := context.Background()

	issues := []*types.Issue{
		{Title: "Fix auth bug", Description: "Users cannot login", Design: "Use OAuth", AcceptanceCriteria: "User can login", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeBug},
		{Title: "Fix auth bug", Description: "Users cannot login", Design: "Use OAuth", AcceptanceCriteria: "User can login", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeBug},
	}

	for _, issue := range issues {
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}
	}

	check := checkDuplicateIssuesDB(store.DB(), false, 1000)

	if check.Status != StatusWarning {
		t.Errorf("Status = %q, want %q (open duplicates should be detected)", check.Status, StatusWarning)
	}
	if check.Message != "1 duplicate issue(s) in 1 group(s)" {
		t.Errorf("Message = %q, want '1 duplicate issue(s) in 1 group(s)'", check.Message)
	}
}

// TestCheckDuplicateIssues_DifferentDesignNotDuplicate verifies that issues
// with same title+description but different design are NOT duplicates.
func TestCheckDuplicateIssues_DifferentDesignNotDuplicate(t *testing.T) {
	store := newTestDoltStore(t, "test")
	ctx := context.Background()

	issues := []*types.Issue{
		{Title: "Fix auth bug", Description: "Users cannot login", Design: "Use OAuth", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeBug},
		{Title: "Fix auth bug", Description: "Users cannot login", Design: "Use SAML", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeBug},
	}

	for _, issue := range issues {
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}
	}

	check := checkDuplicateIssuesDB(store.DB(), false, 1000)

	if check.Status != StatusOK {
		t.Errorf("Status = %q, want %q (different design = not duplicates)", check.Status, StatusOK)
		t.Logf("Message: %s", check.Message)
	}
}

// TestCheckDuplicateIssues_MixedOpenClosed verifies correct behavior when
// there are both open and closed issues with same content.
func TestCheckDuplicateIssues_MixedOpenClosed(t *testing.T) {
	store := newTestDoltStore(t, "test")
	ctx := context.Background()

	openIssues := []*types.Issue{
		{Title: "Task A", Description: "Do something", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{Title: "Task A", Description: "Do something", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	}

	for _, issue := range openIssues {
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}
	}

	closedIssue := &types.Issue{Title: "Task A", Description: "Do something", Status: types.StatusClosed, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, closedIssue, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	check := checkDuplicateIssuesDB(store.DB(), false, 1000)

	if check.Status != StatusWarning {
		t.Errorf("Status = %q, want %q", check.Status, StatusWarning)
	}
	if check.Message != "1 duplicate issue(s) in 1 group(s)" {
		t.Errorf("Message = %q, want '1 duplicate issue(s) in 1 group(s)'", check.Message)
	}
}

// TestCheckDuplicateIssues_DeletedExcluded verifies deleted issues
// are excluded from duplicate detection.
func TestCheckDuplicateIssues_DeletedExcluded(t *testing.T) {
	store := newTestDoltStore(t, "test")
	ctx := context.Background()

	issues := []*types.Issue{
		{Title: "Deleted issue", Description: "Was deleted", Status: types.StatusClosed, Priority: 2, IssueType: types.TypeTask},
		{Title: "Deleted issue", Description: "Was deleted", Status: types.StatusClosed, Priority: 2, IssueType: types.TypeTask},
	}

	for _, issue := range issues {
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}
	}

	check := checkDuplicateIssuesDB(store.DB(), false, 1000)

	if check.Status != StatusOK {
		t.Errorf("Status = %q, want %q (closed/deleted issues should be excluded)", check.Status, StatusOK)
	}
}

// TestCheckDuplicateIssues_NoDatabase verifies graceful handling when no database exists.
func TestCheckDuplicateIssues_NoDatabase(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write metadata.json pointing to a unique nonexistent database so that
	// openStoreDB doesn't fall back to the shared default "beads" database.
	h := sha256.Sum256([]byte(t.Name() + fmt.Sprintf("%d", time.Now().UnixNano())))
	noDbName := "doctest_nodb_" + hex.EncodeToString(h[:6])
	cfg := configfile.DefaultConfig()
	cfg.Backend = configfile.BackendDolt
	cfg.DoltDatabase = noDbName
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("Failed to save config: %v", err)
	}

	check := CheckDuplicateIssues(tmpDir, false, 1000)

	if check.Status != StatusOK {
		t.Errorf("Status = %q, want %q", check.Status, StatusOK)
	}
	// When no Dolt database exists, openStoreDB may create an empty one but
	// the duplicate query will fail since no schema exists.
	wantMessages := []string{"N/A (no database)", "N/A (unable to query issues)"}
	found := false
	for _, msg := range wantMessages {
		if check.Message == msg {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Message = %q, want one of %v", check.Message, wantMessages)
	}
}

// TestCheckDuplicateIssues_OrchestratorUnderThreshold verifies that with orchestrator mode enabled,
// duplicates under the threshold are OK.
func TestCheckDuplicateIssues_OrchestratorUnderThreshold(t *testing.T) {
	store := newTestDoltStore(t, "test")
	ctx := context.Background()

	for i := 0; i < 51; i++ {
		issue := &types.Issue{
			Title:       "Check own context limit",
			Description: "Wisp for patrol cycle",
			Status:      types.StatusOpen,
			Priority:    2,
			IssueType:   types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}
	}

	check := checkDuplicateIssuesDB(store.DB(), true, 1000)

	if check.Status != StatusOK {
		t.Errorf("Status = %q, want %q (under orchestrator threshold)", check.Status, StatusOK)
		t.Logf("Message: %s", check.Message)
	}
	if check.Message != "50 duplicate(s) detected (within orchestrator threshold of 1000)" {
		t.Errorf("Message = %q, want message about being within threshold", check.Message)
	}
}

// TestCheckDuplicateIssues_OrchestratorOverThreshold verifies that with orchestrator mode enabled,
// duplicates over the threshold still warn.
func TestCheckDuplicateIssues_OrchestratorOverThreshold(t *testing.T) {
	store := newTestDoltStore(t, "test")
	ctx := context.Background()

	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}

	// Insert 51 duplicate issues (over threshold of 25) via raw SQL for speed.
	db := store.DB()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("Failed to begin transaction: %v", err)
	}
	for i := 0; i < 51; i++ {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, created_at, updated_at)
			 VALUES (?, 'Runaway wisps', 'Too many wisps', '', '', '', 'open', 2, 'task', NOW(), NOW())`,
			fmt.Sprintf("test-%06d", i))
		if err != nil {
			_ = tx.Rollback()
			t.Fatalf("Failed to insert issue %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Failed to commit transaction: %v", err)
	}

	check := checkDuplicateIssuesDB(db, true, 25)

	if check.Status != StatusWarning {
		t.Errorf("Status = %q, want %q (over orchestrator threshold)", check.Status, StatusWarning)
	}
	if check.Message != "50 duplicate issue(s) in 1 group(s)" {
		t.Errorf("Message = %q, want '50 duplicate issue(s) in 1 group(s)'", check.Message)
	}
}

// TestCheckDuplicateIssues_OrchestratorCustomThreshold verifies custom threshold works.
func TestCheckDuplicateIssues_OrchestratorCustomThreshold(t *testing.T) {
	store := newTestDoltStore(t, "test")
	ctx := context.Background()

	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}

	// Insert 21 duplicate issues (over custom threshold of 10) via raw SQL.
	db := store.DB()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("Failed to begin transaction: %v", err)
	}
	for i := 0; i < 21; i++ {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, created_at, updated_at)
			 VALUES (?, 'Custom threshold test', 'Test custom threshold', '', '', '', 'open', 2, 'task', NOW(), NOW())`,
			fmt.Sprintf("test-%06d", i))
		if err != nil {
			_ = tx.Rollback()
			t.Fatalf("Failed to insert issue %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Failed to commit transaction: %v", err)
	}

	check := checkDuplicateIssuesDB(db, true, 10)

	if check.Status != StatusWarning {
		t.Errorf("Status = %q, want %q (over custom threshold of 10)", check.Status, StatusWarning)
	}
	if check.Message != "20 duplicate issue(s) in 1 group(s)" {
		t.Errorf("Message = %q, want '20 duplicate issue(s) in 1 group(s)'", check.Message)
	}
}

// TestCheckDuplicateIssues_NonOrchestratorMode verifies that without orchestrator mode,
// any duplicates are warnings (backward compatibility).
func TestCheckDuplicateIssues_NonOrchestratorMode(t *testing.T) {
	store := newTestDoltStore(t, "test")
	ctx := context.Background()

	for i := 0; i < 51; i++ {
		issue := &types.Issue{
			Title:       "Duplicate task",
			Description: "Some task",
			Status:      types.StatusOpen,
			Priority:    2,
			IssueType:   types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}
	}

	check := checkDuplicateIssuesDB(store.DB(), false, 1000)

	if check.Status != StatusWarning {
		t.Errorf("Status = %q, want %q (non-orchestrator should warn on any duplicates)", check.Status, StatusWarning)
	}
	if check.Message != "50 duplicate issue(s) in 1 group(s)" {
		t.Errorf("Message = %q, want '50 duplicate issue(s) in 1 group(s)'", check.Message)
	}
}

// TestCheckDuplicateIssues_MultipleDuplicateGroups verifies correct counting
// when there are multiple distinct groups of duplicates.
func TestCheckDuplicateIssues_MultipleDuplicateGroups(t *testing.T) {
	store := newTestDoltStore(t, "test")
	ctx := context.Background()

	// Group A: 3 identical issues (2 duplicates)
	for i := 0; i < 3; i++ {
		issue := &types.Issue{
			Title:       "Auth bug",
			Description: "Login fails",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeBug,
		}
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}
	}

	// Group B: 2 identical issues (1 duplicate), different content from A
	for i := 0; i < 2; i++ {
		issue := &types.Issue{
			Title:       "Add dark mode",
			Description: "Users want dark mode",
			Status:      types.StatusOpen,
			Priority:    2,
			IssueType:   types.TypeFeature,
		}
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}
	}

	check := checkDuplicateIssuesDB(store.DB(), false, 1000)

	if check.Status != StatusWarning {
		t.Errorf("Status = %q, want %q", check.Status, StatusWarning)
	}
	if check.Message != "3 duplicate issue(s) in 2 group(s)" {
		t.Errorf("Message = %q, want '3 duplicate issue(s) in 2 group(s)'", check.Message)
	}
}

// TestCheckDuplicateIssues_ZeroDuplicatesNullHandling verifies that when no
// duplicates exist, the SQL SUM() returning NULL is handled correctly.
func TestCheckDuplicateIssues_ZeroDuplicatesNullHandling(t *testing.T) {
	store := newTestDoltStore(t, "test")
	ctx := context.Background()

	issues := []*types.Issue{
		{Title: "Issue A", Description: "Unique A", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
		{Title: "Issue B", Description: "Unique B", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
		{Title: "Issue C", Description: "Unique C", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
	}

	for _, issue := range issues {
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}
	}

	check := checkDuplicateIssuesDB(store.DB(), false, 1000)

	if check.Status != StatusOK {
		t.Errorf("Status = %q, want %q (no duplicates should be OK)", check.Status, StatusOK)
		t.Logf("Message: %s", check.Message)
	}
	if check.Message != "No duplicate issues" {
		t.Errorf("Message = %q, want 'No duplicate issues'", check.Message)
	}
}

// TestCheckChildParentDependenciesDB_NoDeps verifies OK when no dependencies exist.
func TestCheckChildParentDependenciesDB_NoDeps(t *testing.T) {
	store := newTestDoltStore(t, "test")
	ctx := context.Background()

	// Create a parent issue and child issue with no dependencies
	parent := &types.Issue{Title: "Parent", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeEpic}
	if err := store.CreateIssue(ctx, parent, "test"); err != nil {
		t.Fatalf("Failed to create parent: %v", err)
	}

	check := checkChildParentDependenciesDB(store.DB())

	if check.Status != StatusOK {
		t.Errorf("Status = %q, want %q", check.Status, StatusOK)
	}
	if check.Message != "No child→parent dependencies" {
		t.Errorf("Message = %q, want %q", check.Message, "No child→parent dependencies")
	}
}

// TestCheckChildParentDependenciesDB_BlockingDetected verifies warning when
// a child issue has a blocking dependency on its parent (deadlock pattern).
func TestCheckChildParentDependenciesDB_BlockingDetected(t *testing.T) {
	store := newTestDoltStore(t, "test")
	ctx := context.Background()

	// Create parent and child issues
	parent := &types.Issue{Title: "Parent epic", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeEpic}
	if err := store.CreateIssue(ctx, parent, "test"); err != nil {
		t.Fatalf("Failed to create parent: %v", err)
	}

	child := &types.Issue{Title: "Child task", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, child, "test"); err != nil {
		t.Fatalf("Failed to create child: %v", err)
	}

	// Set up child→parent blocking dependency via raw SQL.
	// The child ID must start with parent ID + "." to trigger the check.
	db := store.DB()
	childID := parent.ID + ".1"

	// Insert child issue with hierarchical ID
	_, err := db.ExecContext(ctx,
		`INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, created_at, updated_at)
		 VALUES (?, 'Hierarchical child', '', '', '', '', 'open', 2, 'task', NOW(), NOW())`,
		childID)
	if err != nil {
		t.Fatalf("Failed to insert child: %v", err)
	}

	// Add blocking dependency: child depends on parent
	_, err = db.ExecContext(ctx,
		`INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by) VALUES (UUID(), ?, ?, 'blocks', NOW(), 'test')`,
		childID, parent.ID)
	if err != nil {
		t.Fatalf("Failed to insert dependency: %v", err)
	}

	check := checkChildParentDependenciesDB(db)

	if check.Status != StatusWarning {
		t.Errorf("Status = %q, want %q", check.Status, StatusWarning)
	}
	if check.Message == "" {
		t.Error("Message should not be empty")
	}
}

// TestCheckChildParentDependenciesDB_ParentGateOnOwnChildNotFlagged pins the
// check's scope against the reverse shape GH#4814 reported: a parent that
// waits for, or blocks on, its own child across a real parent-child edge.
// Since gastownhall/beads#6506 that edge holds back the parent, not the
// child, so the children stay ready and the check must stay OK; a warning
// would point --fix-child-parent at a working close gate. Parent Close Gates
// reports the blocks form instead (adapted from PR#5131's mirror test).
func TestCheckChildParentDependenciesDB_ParentGateOnOwnChildNotFlagged(t *testing.T) {
	store := newTestDoltStore(t, "test")
	ctx := context.Background()

	// The shape bd create --parent writes: dotted ids AND real parent-child
	// edges. test-gate.1 has an open child, so the waits-for gate is live.
	issues := []*types.Issue{
		{ID: "test-gate", Title: "Parent epic", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeEpic},
		{ID: "test-gate.1", Title: "Child task", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "test-gate.1.1", Title: "Grandchild task", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "test-gate.2", Title: "Second child task", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	}
	for _, issue := range issues {
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("CreateIssue %s: %v", issue.ID, err)
		}
	}
	for _, dep := range []*types.Dependency{
		{IssueID: "test-gate.1", DependsOnID: "test-gate", Type: types.DepParentChild},
		{IssueID: "test-gate.1.1", DependsOnID: "test-gate.1", Type: types.DepParentChild},
		{IssueID: "test-gate.2", DependsOnID: "test-gate", Type: types.DepParentChild},
		{IssueID: "test-gate", DependsOnID: "test-gate.1", Type: types.DepWaitsFor},
	} {
		if err := store.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatalf("AddDependency %s -> %s (%s): %v", dep.IssueID, dep.DependsOnID, dep.Type, err)
		}
	}

	// A legacy blocks row onto the other child goes in raw, because the write
	// path refuses it now. The derived flags are then inverted, so the
	// ready-work checks below pass only if the full recompute really derived
	// them from these edges.
	db := store.DB()
	for _, stmt := range []string{
		"INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by) VALUES (UUID(), 'test-gate', 'test-gate.2', 'blocks', NOW(), 'test')",
		"UPDATE issues SET is_blocked = CASE WHEN id = 'test-gate' THEN 0 ELSE 1 END WHERE id LIKE 'test-gate%'",
		"CALL DOLT_COMMIT('-Am', 'test: seed parent close gates')",
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if _, err := store.RecomputeAllBlocked(ctx); err != nil {
		t.Fatalf("RecomputeAllBlocked: %v", err)
	}

	ready, err := store.GetReadyWork(ctx, types.WorkFilter{Status: types.StatusOpen})
	if err != nil {
		t.Fatalf("GetReadyWork: %v", err)
	}
	inReady := map[string]bool{}
	for _, issue := range ready {
		inReady[issue.ID] = true
	}
	for _, id := range []string{"test-gate.1", "test-gate.1.1", "test-gate.2"} {
		if !inReady[id] {
			t.Errorf("%s missing from ready work: the parent's gate must not hold back its children", id)
		}
	}
	if inReady["test-gate"] {
		t.Error("test-gate is ready, but it waits for and blocks on open children")
	}

	check := checkChildParentDependenciesDB(db)
	if check.Status != StatusOK {
		t.Errorf("Status = %q, want %q (detail %q): a parent's gate on its own child is not a deadlock", check.Status, StatusOK, check.Detail)
	}
	if check.Message != "No child→parent dependencies" {
		t.Errorf("Message = %q, want %q", check.Message, "No child→parent dependencies")
	}

	gates := checkParentBlocksOwnChildDB(db)
	if !strings.Contains(gates.Detail, "test-gate→test-gate.2") {
		t.Errorf("Parent Close Gates detail = %q, want it to list the legacy blocks row test-gate→test-gate.2", gates.Detail)
	}
}

// TestCheckChildParentDependenciesDB_ParentBlocksChildIgnoredWithoutRealEdge
// verifies a parent blocking on an issue whose id only looks like its child's
// is not flagged. IDs and edges diverge by design after a reparent or import;
// a parent blocking on an orphaned-dotted issue with no structural link is the
// ordinary "epic waits for its subtask" shape, not a deadlock (bee-ghosttrack
// review, PR#5131).
func TestCheckChildParentDependenciesDB_ParentBlocksChildIgnoredWithoutRealEdge(t *testing.T) {
	store := newTestDoltStore(t, "test")
	ctx := context.Background()

	parent := &types.Issue{Title: "Parent epic", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeEpic}
	if err := store.CreateIssue(ctx, parent, "test"); err != nil {
		t.Fatalf("Failed to create parent: %v", err)
	}

	db := store.DB()
	childID := parent.ID + ".1"

	_, err := db.ExecContext(ctx,
		`INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, created_at, updated_at)
		 VALUES (?, 'Orphaned dotted issue', '', '', '', '', 'open', 2, 'task', NOW(), NOW())`,
		childID)
	if err != nil {
		t.Fatalf("Failed to insert child: %v", err)
	}

	// No parent-child edge: childID's dotted ID is coincidental, not structural.
	_, err = db.ExecContext(ctx,
		`INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by) VALUES (UUID(), ?, ?, 'blocks', NOW(), 'test')`,
		parent.ID, childID)
	if err != nil {
		t.Fatalf("Failed to insert dependency: %v", err)
	}

	check := checkChildParentDependenciesDB(db)
	if check.Status != StatusOK {
		t.Fatalf("Status = %q, want %q (dotted ID alone, no real parent-child edge, must not be flagged)", check.Status, StatusOK)
	}
}

// TestCheckChildParentDependenciesDB_NonBlockingIgnored verifies that
// parent-child type dependencies (structural hierarchy) are NOT flagged.
func TestCheckChildParentDependenciesDB_NonBlockingIgnored(t *testing.T) {
	store := newTestDoltStore(t, "test")
	ctx := context.Background()

	parent := &types.Issue{Title: "Parent", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeEpic}
	if err := store.CreateIssue(ctx, parent, "test"); err != nil {
		t.Fatalf("Failed to create parent: %v", err)
	}

	db := store.DB()
	childID := parent.ID + ".1"

	_, err := db.ExecContext(ctx,
		`INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, created_at, updated_at)
		 VALUES (?, 'Hierarchical child', '', '', '', '', 'open', 2, 'task', NOW(), NOW())`,
		childID)
	if err != nil {
		t.Fatalf("Failed to insert child: %v", err)
	}

	// Add parent-child type dependency (NOT blocking — should be ignored)
	_, err = db.ExecContext(ctx,
		`INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by) VALUES (UUID(), ?, ?, 'parent-child', NOW(), 'test')`,
		childID, parent.ID)
	if err != nil {
		t.Fatalf("Failed to insert dependency: %v", err)
	}

	check := checkChildParentDependenciesDB(db)

	if check.Status != StatusOK {
		t.Errorf("Status = %q, want %q (parent-child type should be ignored)", check.Status, StatusOK)
	}
}

func TestCheckOrphanedDependenciesDB_IssueToWispTargetIsNotOrphan(t *testing.T) {
	store := newTestDoltStore(t, "test")
	ctx := context.Background()

	issue := &types.Issue{ID: "test-mixed-source", Title: "Source", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("CreateIssue source: %v", err)
	}

	wisp := &types.Issue{ID: "test-wisp-target", Title: "Target wisp", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, NoHistory: true}
	if err := store.CreateIssue(ctx, wisp, "test"); err != nil {
		t.Fatalf("CreateIssue wisp: %v", err)
	}

	dep := &types.Dependency{
		IssueID:     issue.ID,
		DependsOnID: wisp.ID,
		Type:        types.DepBlocks,
		CreatedAt:   time.Now(),
		CreatedBy:   "test",
	}
	if err := store.AddDependency(ctx, dep, "test"); err != nil {
		t.Fatalf("AddDependency issue->wisp: %v", err)
	}

	check := checkOrphanedDependenciesDB(store.DB())
	if check.Status != StatusOK {
		t.Fatalf("Status = %q, want %q; detail=%s", check.Status, StatusOK, check.Detail)
	}
}

// TestCheckOrphanedDependenciesDB_CrossPrefixTargetIsNotOrphan pins #4647: a
// target whose prefix names another repository is stored in
// depends_on_external and lives in that rig's database, so a missing local row
// is expected and the edge is not reported as an orphan.
func TestCheckOrphanedDependenciesDB_CrossPrefixTargetIsNotOrphan(t *testing.T) {
	store := newTestDoltStore(t, "test")
	ctx := context.Background()

	issue := &types.Issue{ID: "test-cross-source", Title: "Source", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("CreateIssue source: %v", err)
	}

	dep := &types.Dependency{
		IssueID:     issue.ID,
		DependsOnID: "other-target",
		Type:        types.DepBlocks,
		CreatedAt:   time.Now(),
		CreatedBy:   "test",
	}
	if err := store.AddDependency(ctx, dep, "test"); err != nil {
		t.Fatalf("AddDependency cross-prefix: %v", err)
	}

	var external sql.NullString
	if err := store.DB().QueryRowContext(ctx,
		"SELECT depends_on_external FROM dependencies WHERE issue_id = ?", issue.ID).Scan(&external); err != nil {
		t.Fatalf("read dependency row: %v", err)
	}
	if external.String != "other-target" {
		t.Fatalf("depends_on_external = %q, want %q", external.String, "other-target")
	}

	check := checkOrphanedDependenciesDB(store.DB())
	if check.Status != StatusOK {
		t.Fatalf("Status = %q, want %q; detail=%s", check.Status, StatusOK, check.Detail)
	}
}

// insertDepOnExternalColumn stores issueID -> target in depends_on_external,
// the column that carries no foreign key, as migrations 0041 and 0047 do for
// a target that was already missing whatever its prefix.
func insertDepOnExternalColumn(t *testing.T, db *sql.DB, issueID, target string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO dependencies (id, issue_id, depends_on_external, type, created_at, created_by)
		 VALUES (UUID(), ?, ?, 'blocks', NOW(), 'test')`, issueID, target); err != nil {
		t.Fatalf("insert %s -> %s: %v", issueID, target, err)
	}
}

// TestCheckOrphanedDependenciesDB_SamePrefixExternalColumnTargetIsOrphan pins
// that the storage column does not decide #4647's exemption: a same-prefix
// target with no row, stored in depends_on_external, is still an orphan.
func TestCheckOrphanedDependenciesDB_SamePrefixExternalColumnTargetIsOrphan(t *testing.T) {
	store := newTestDoltStore(t, "test")
	ctx := context.Background()

	issue := &types.Issue{ID: "test-src", Title: "Source", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("CreateIssue source: %v", err)
	}
	insertDepOnExternalColumn(t, store.DB(), issue.ID, "test-gone")

	check := checkOrphanedDependenciesDB(store.DB())
	if check.Status != StatusWarning {
		t.Fatalf("Status = %q, want %q; message=%s", check.Status, StatusWarning, check.Message)
	}
	if !strings.Contains(check.Detail, "test-src→test-gone") {
		t.Fatalf("Detail = %q, want test-src→test-gone", check.Detail)
	}
}

// TestCheckOrphanedDependenciesDB_DeletedHeldPrefixTargetIsOrphan pins the
// mixed-prefix case: with zz in allowed_prefixes, test-a -> zz-b is stored in
// depends_on_external, and deleting zz-b leaves an edge to a row this
// database held, so it is an orphan.
func TestCheckOrphanedDependenciesDB_DeletedHeldPrefixTargetIsOrphan(t *testing.T) {
	store := newTestDoltStore(t, "test")
	ctx := context.Background()

	if err := store.SetConfig(ctx, "allowed_prefixes", "zz"); err != nil {
		t.Fatalf("SetConfig allowed_prefixes: %v", err)
	}
	for _, id := range []string{"test-a", "zz-b"} {
		issue := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("CreateIssue %s: %v", id, err)
		}
	}
	dep := &types.Dependency{IssueID: "test-a", DependsOnID: "zz-b", Type: types.DepBlocks, CreatedAt: time.Now(), CreatedBy: "test"}
	if err := store.AddDependency(ctx, dep, "test"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}
	if check := checkOrphanedDependenciesDB(store.DB()); check.Status != StatusOK {
		t.Fatalf("before delete: Status = %q, want %q; detail=%s", check.Status, StatusOK, check.Detail)
	}
	if err := store.DeleteIssue(ctx, "zz-b"); err != nil {
		t.Fatalf("DeleteIssue zz-b: %v", err)
	}

	var external sql.NullString
	if err := store.DB().QueryRowContext(ctx,
		"SELECT depends_on_external FROM dependencies WHERE issue_id = 'test-a'").Scan(&external); err != nil {
		t.Fatalf("read dangling edge: %v", err)
	}
	if external.String != "zz-b" {
		t.Fatalf("depends_on_external = %q, want zz-b", external.String)
	}

	check := checkOrphanedDependenciesDB(store.DB())
	if check.Status != StatusWarning {
		t.Fatalf("Status = %q, want %q; message=%s", check.Status, StatusWarning, check.Message)
	}
	if !strings.Contains(check.Detail, "test-a→zz-b") {
		t.Fatalf("Detail = %q, want test-a→zz-b", check.Detail)
	}
}

// TestCheckOrphanedDependenciesDB_CountsUnverifiableTargets pins the
// non-deleting line: an external: ref and another repository's target are
// counted, not reported, and a target with no hyphen is still an orphan.
func TestCheckOrphanedDependenciesDB_CountsUnverifiableTargets(t *testing.T) {
	store := newTestDoltStore(t, "test")
	ctx := context.Background()

	issue := &types.Issue{ID: "test-src", Title: "Source", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("CreateIssue source: %v", err)
	}
	dep := &types.Dependency{IssueID: issue.ID, DependsOnID: "other-target", Type: types.DepBlocks, CreatedAt: time.Now(), CreatedBy: "test"}
	if err := store.AddDependency(ctx, dep, "test"); err != nil {
		t.Fatalf("AddDependency cross-prefix: %v", err)
	}
	insertDepOnExternalColumn(t, store.DB(), issue.ID, "external:proj:cap")

	check := checkOrphanedDependenciesDB(store.DB())
	if check.Status != StatusOK {
		t.Fatalf("Status = %q, want %q; detail=%s", check.Status, StatusOK, check.Detail)
	}
	if want := "(2 cross-repository edge(s) not verifiable here)"; !strings.Contains(check.Message, want) {
		t.Fatalf("Message = %q, want it to contain %q", check.Message, want)
	}

	insertDepOnExternalColumn(t, store.DB(), issue.ID, "gone")
	check = checkOrphanedDependenciesDB(store.DB())
	if check.Status != StatusWarning {
		t.Fatalf("with no-hyphen target: Status = %q, want %q", check.Status, StatusWarning)
	}
	if check.Detail != "test-src→gone" {
		t.Fatalf("Detail = %q, want only test-src→gone", check.Detail)
	}
	if want := "1 orphaned dependency reference(s) (2 cross-repository edge(s) not verifiable here)"; check.Message != want {
		t.Fatalf("Message = %q, want %q", check.Message, want)
	}
}

// orphanCheckFingerprint is the sha256 of the check as bd doctor renders it:
// the --json bytes, then each field the text printer shows. Equal
// fingerprints mean byte-identical output on both paths.
func orphanCheckFingerprint(t *testing.T, c DoctorCheck) string {
	t.Helper()
	js, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal check: %v", err)
	}
	p := c.Printed()
	sum := sha256.Sum256([]byte(strings.Join([]string{string(js), p.Name, p.Status, p.Message, p.Detail, p.Fix, p.Category}, "\x00")))
	return hex.EncodeToString(sum[:])
}

// dependencyRowsFingerprint is the sha256 of every dependency and wisp
// dependency row, in a fixed order, so an equal fingerprint means no edge
// was added, changed or deleted.
func dependencyRowsFingerprint(t *testing.T, db *sql.DB) string {
	t.Helper()
	//nolint:gosec // G202: doctorDependencyUnionSQL returns a fixed internal SELECT fragment.
	rows, err := db.Query(`SELECT d.dep_table, d.issue_id, COALESCE(d.depends_on_id, ''), d.type
		FROM (` + doctorDependencyUnionSQL() + `) d ORDER BY 1, 2, 3, 4`)
	if err != nil {
		t.Fatalf("read dependency rows: %v", err)
	}
	defer rows.Close()
	h := sha256.New()
	for rows.Next() {
		var table, issueID, target, depType string
		if err := rows.Scan(&table, &issueID, &target, &depType); err != nil {
			t.Fatalf("scan dependency row: %v", err)
		}
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\n", table, issueID, target, depType)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate dependency rows: %v", err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// insertWispOrphan stores a wisp dependency of source on a target with no row.
func insertWispOrphan(t *testing.T, db *sql.DB, source, target string) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 0"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO wisp_dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by)
		 VALUES (UUID(), ?, ?, 'blocks', NOW(), 'test')`, source, target); err != nil {
		t.Fatalf("insert wisp orphan %s -> %s: %v", source, target, err)
	}
	if _, err := db.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 1"); err != nil {
		t.Fatal(err)
	}
}

// TestCheckOrphanedDependenciesDB_OutputUnchangedWithoutUnverifiableTargets
// pins the path the count does not touch: with no cross-repository edge the
// check's output is byte-identical to the output before the count existed
// (these literals pass unchanged at the PR's previous head and at its base),
// both clean and with one orphan.
func TestCheckOrphanedDependenciesDB_OutputUnchangedWithoutUnverifiableTargets(t *testing.T) {
	store := newTestDoltStore(t, "test")
	ctx := context.Background()

	for _, id := range []string{"test-a", "test-b"} {
		issue := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("CreateIssue %s: %v", id, err)
		}
	}
	dep := &types.Dependency{IssueID: "test-a", DependsOnID: "test-b", Type: types.DepBlocks, CreatedAt: time.Now(), CreatedBy: "test"}
	if err := store.AddDependency(ctx, dep, "test"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}
	clean := DoctorCheck{Name: "Orphaned Dependencies", Status: "ok", Message: "No orphaned dependencies"}
	if got := checkOrphanedDependenciesDB(store.DB()); orphanCheckFingerprint(t, got) != orphanCheckFingerprint(t, clean) {
		t.Fatalf("clean: got %#v, want byte-identical %#v", got, clean)
	}

	wisp := &types.Issue{ID: "test-wisp-src", Title: "Source wisp", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, NoHistory: true}
	if err := store.CreateIssue(ctx, wisp, "test"); err != nil {
		t.Fatalf("CreateIssue wisp: %v", err)
	}
	insertWispOrphan(t, store.DB(), wisp.ID, "test-missing")
	orphaned := DoctorCheck{
		Name:    "Orphaned Dependencies",
		Status:  "warning",
		Message: "1 orphaned dependency reference(s)",
		Detail:  "test-wisp-src→test-missing",
		Fix:     "Run 'bd doctor --fix' to remove orphaned dependencies",
	}
	if got := checkOrphanedDependenciesDB(store.DB()); orphanCheckFingerprint(t, got) != orphanCheckFingerprint(t, orphaned) {
		t.Fatalf("one orphan: got %#v, want byte-identical %#v", got, orphaned)
	}
}

// TestCheckOrphanedDependenciesDB_CountNoteIdentity pins what the count may
// change: adding cross-repository edges leaves the rendered check identical
// to the one without them except for the appended note, clean and with an
// orphan, and the check leaves the dependency rows' fingerprint as it was
// (it counts those edges, it never deletes them).
func TestCheckOrphanedDependenciesDB_CountNoteIdentity(t *testing.T) {
	store := newTestDoltStore(t, "test")
	ctx := context.Background()
	db := store.DB()
	const note = " (2 cross-repository edge(s) not verifiable here)"

	issue := &types.Issue{ID: "test-src", Title: "Source", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("CreateIssue source: %v", err)
	}
	addCrossRepositoryEdges := func() {
		t.Helper()
		dep := &types.Dependency{IssueID: issue.ID, DependsOnID: "other-target", Type: types.DepBlocks, CreatedAt: time.Now(), CreatedBy: "test"}
		if err := store.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatalf("AddDependency cross-prefix: %v", err)
		}
		insertDepOnExternalColumn(t, db, issue.ID, "external:proj:cap")
	}
	removeCrossRepositoryEdges := func() {
		t.Helper()
		if _, err := db.ExecContext(ctx, "DELETE FROM dependencies WHERE issue_id = ?", issue.ID); err != nil {
			t.Fatalf("delete cross-repository edges: %v", err)
		}
	}
	checkKeepsRows := func(stage string) DoctorCheck {
		t.Helper()
		before := dependencyRowsFingerprint(t, db)
		check := checkOrphanedDependenciesDB(db)
		if after := dependencyRowsFingerprint(t, db); after != before {
			t.Fatalf("%s: dependency rows fingerprint %s after the check, want %s", stage, after, before)
		}
		return check
	}

	for _, stage := range []string{"clean", "with orphan"} {
		if stage == "with orphan" {
			wisp := &types.Issue{ID: "test-wisp-src", Title: "Source wisp", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, NoHistory: true}
			if err := store.CreateIssue(ctx, wisp, "test"); err != nil {
				t.Fatalf("CreateIssue wisp: %v", err)
			}
			insertWispOrphan(t, db, wisp.ID, "test-missing")
		}
		without := checkKeepsRows(stage + ", no cross-repository edge")
		if strings.Contains(without.Message, "cross-repository") {
			t.Fatalf("%s: Message = %q, want no note without cross-repository edges", stage, without.Message)
		}
		addCrossRepositoryEdges()
		with := checkKeepsRows(stage + ", two cross-repository edges")
		want := without
		want.Message += note
		if orphanCheckFingerprint(t, with) != orphanCheckFingerprint(t, want) {
			t.Fatalf("%s: got %#v, want identity with %#v plus the note", stage, with, without)
		}
		if again := checkKeepsRows(stage + ", rerun"); orphanCheckFingerprint(t, again) != orphanCheckFingerprint(t, with) {
			t.Fatalf("%s: rerun rendered %#v, want the identical %#v", stage, again, with)
		}
		removeCrossRepositoryEdges()
	}
}

func TestCheckOrphanedDependenciesDB_WispDependencyMissingTargetDetected(t *testing.T) {
	store := newTestDoltStore(t, "test")
	ctx := context.Background()

	wisp := &types.Issue{ID: "test-wisp-source", Title: "Source wisp", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, NoHistory: true}
	if err := store.CreateIssue(ctx, wisp, "test"); err != nil {
		t.Fatalf("CreateIssue wisp: %v", err)
	}

	db := store.DB()
	if _, err := db.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 0"); err != nil {
		t.Fatal(err)
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO wisp_dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by)
		 VALUES (UUID(), ?, ?, 'blocks', NOW(), 'test')`,
		wisp.ID, "test-missing-target")
	if err != nil {
		t.Fatalf("insert wisp orphan dependency: %v", err)
	}
	if _, err := db.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 1"); err != nil {
		t.Fatal(err)
	}

	check := checkOrphanedDependenciesDB(db)
	if check.Status != StatusWarning {
		t.Fatalf("Status = %q, want %q", check.Status, StatusWarning)
	}
	if !strings.Contains(check.Detail, wisp.ID+"→test-missing-target") {
		t.Fatalf("Detail = %q, want missing wisp dependency", check.Detail)
	}
}

func TestCheckChildParentDependenciesDB_WispChildBlockingParentDetected(t *testing.T) {
	store := newTestDoltStore(t, "test")
	ctx := context.Background()

	parent := &types.Issue{ID: "test-parent", Title: "Parent", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeEpic}
	if err := store.CreateIssue(ctx, parent, "test"); err != nil {
		t.Fatalf("CreateIssue parent: %v", err)
	}

	child := &types.Issue{ID: "test-parent.1", Title: "Child wisp", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, NoHistory: true}
	if err := store.CreateIssue(ctx, child, "test"); err != nil {
		t.Fatalf("CreateIssue child wisp: %v", err)
	}

	dep := &types.Dependency{
		IssueID:     child.ID,
		DependsOnID: parent.ID,
		Type:        types.DepBlocks,
		CreatedAt:   time.Now(),
		CreatedBy:   "test",
	}
	if err := store.AddDependency(ctx, dep, "test"); err != nil {
		t.Fatalf("AddDependency wisp child->parent: %v", err)
	}

	check := checkChildParentDependenciesDB(store.DB())
	if check.Status != StatusWarning {
		t.Fatalf("Status = %q, want %q", check.Status, StatusWarning)
	}
	if !strings.Contains(check.Detail, child.ID+"→"+parent.ID) {
		t.Fatalf("Detail = %q, want child-parent dependency", check.Detail)
	}
}

// TestCheckTestPollution_NoTestIssues_NoServer verifies StatusOK when no Dolt
// server is reachable. This isolates BEADS_DOLT_PORT set by TestMain (which
// starts a Docker-based Dolt container on Ubuntu but not macOS) so the test
// exercises the "no database" code path deterministically on all platforms.
func TestCheckTestPollution_NoTestIssues_NoServer(t *testing.T) {
	for _, key := range []string{"BEADS_DOLT_PORT", "BEADS_DOLT_SERVER_PORT"} {
		if orig, ok := os.LookupEnv(key); ok {
			t.Cleanup(func() { os.Setenv(key, orig) })
			os.Unsetenv(key)
		}
	}

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	check := CheckTestPollution(tmpDir)

	if check.Status != StatusOK {
		t.Errorf("Status = %q, want %q", check.Status, StatusOK)
	}
	if check.Message != "N/A (no database)" {
		t.Errorf("Message = %q, want %q", check.Message, "N/A (no database)")
	}
}

// TestCheckTestPollution_NoTestIssues_EmptyDB verifies StatusWarning when a
// Dolt server is running but the target database has no issues table (the
// "query failed" code path). Only runs when TestMain started a Dolt container.
func TestCheckTestPollution_NoTestIssues_EmptyDB(t *testing.T) {
	if doctorTestServerPort() == 0 {
		t.Skip("Dolt test server not available")
	}

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	check := CheckTestPollution(tmpDir)

	if check.Status != StatusWarning {
		t.Errorf("Status = %q, want %q", check.Status, StatusWarning)
	}
	if check.Message != "N/A (query failed)" {
		t.Errorf("Message = %q, want %q", check.Message, "N/A (query failed)")
	}
}

// TestCheckGitConflicts_DoltBackend_NoDB verifies CheckGitConflicts returns N/A
// when the Dolt database directory doesn't exist.
func TestCheckGitConflicts_DoltBackend_NoDB(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Default backend is Dolt when no config exists
	check := CheckGitConflicts(tmpDir)

	if check.Status != StatusOK {
		t.Errorf("Status = %q, want %q", check.Status, StatusOK)
	}
	// Without a Dolt database, should report N/A
	wantMessages := []string{"N/A (no Dolt database)", "N/A (unable to open database)"}
	found := false
	for _, msg := range wantMessages {
		if check.Message == msg {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Message = %q, want one of %v", check.Message, wantMessages)
	}
}

// TestCheckGitConflicts_DoltBackend_Clean verifies CheckGitConflicts reports
// OK when a Dolt database exists with no merge conflicts (GH-2249).
func TestCheckGitConflicts_DoltBackend_Clean(t *testing.T) {
	store := newTestDoltStore(t, "test")
	ctx := context.Background()

	// Create an issue so the DB isn't empty
	issue := &types.Issue{
		Title:     "Test issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	// Query dolt_conflicts directly — should be empty
	db := store.DB()
	rows, err := db.Query("SELECT `table`, num_conflicts FROM dolt_conflicts")
	if err != nil {
		t.Fatalf("Failed to query dolt_conflicts: %v", err)
	}
	hasConflicts := rows.Next()
	rows.Close()

	if hasConflicts {
		t.Fatal("Expected no conflicts in clean database")
	}
}

// TestCheckParentBlocksOwnChildDB_NoGates verifies the informational check is
// quiet on a store with no close gates — including one that has an ordinary
// parent-child hierarchy, which must not be mistaken for a gate.
func TestCheckParentBlocksOwnChildDB_NoGates(t *testing.T) {
	store := newTestDoltStore(t, "test")
	ctx := context.Background()

	parent := &types.Issue{Title: "Parent epic", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeEpic}
	if err := store.CreateIssue(ctx, parent, "test"); err != nil {
		t.Fatalf("Failed to create parent: %v", err)
	}
	child := &types.Issue{Title: "Child task", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, child, "test"); err != nil {
		t.Fatalf("Failed to create child: %v", err)
	}
	db := store.DB()
	if _, err := db.ExecContext(ctx,
		`INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by) VALUES (UUID(), ?, ?, 'parent-child', NOW(), 'test')`,
		child.ID, parent.ID); err != nil {
		t.Fatalf("Failed to insert parent-child edge: %v", err)
	}

	check := checkParentBlocksOwnChildDB(db)

	if check.Status != StatusOK {
		t.Errorf("Status = %q, want %q", check.Status, StatusOK)
	}
	if check.Message != "No parent→own-descendant blocking edges" {
		t.Errorf("Message = %q, want the empty-inventory message", check.Message)
	}
	if check.Category != CategoryMetadata {
		t.Errorf("Category = %q, want %q", check.Category, CategoryMetadata)
	}
}

// TestCheckParentBlocksOwnChildDB_GateReported verifies the inventory: a parent
// that blocks on its own parent-child child OR grandchild is listed, by both
// ids, and the check stays OK — these edges are legal since gastownhall/beads#6506 and the
// check has no fix.
func TestCheckParentBlocksOwnChildDB_GateReported(t *testing.T) {
	store := newTestDoltStore(t, "test")
	ctx := context.Background()

	parent := &types.Issue{Title: "Parent epic", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeEpic}
	if err := store.CreateIssue(ctx, parent, "test"); err != nil {
		t.Fatalf("Failed to create parent: %v", err)
	}
	child := &types.Issue{Title: "Child task", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, child, "test"); err != nil {
		t.Fatalf("Failed to create child: %v", err)
	}
	grandchild := &types.Issue{Title: "Grandchild task", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, grandchild, "test"); err != nil {
		t.Fatalf("Failed to create grandchild: %v", err)
	}
	other := &types.Issue{Title: "Unrelated blocker", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, other, "test"); err != nil {
		t.Fatalf("Failed to create unrelated blocker: %v", err)
	}

	db := store.DB()
	edges := []struct{ from, to, depType string }{
		{child.ID, parent.ID, "parent-child"},
		{grandchild.ID, child.ID, "parent-child"},
		{parent.ID, child.ID, "blocks"},      // the close gate
		{parent.ID, grandchild.ID, "blocks"}, // a gate two levels down: listed too
		{parent.ID, other.ID, "blocks"},      // exogenous: must NOT be listed
		{child.ID, other.ID, "blocks"},       // an ordinary blocker on the child
	}
	for _, e := range edges {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by) VALUES (UUID(), ?, ?, ?, NOW(), 'test')`,
			e.from, e.to, e.depType); err != nil {
			t.Fatalf("Failed to insert %s edge %s->%s: %v", e.depType, e.from, e.to, err)
		}
	}

	check := checkParentBlocksOwnChildDB(db)

	if check.Status != StatusOK {
		t.Errorf("Status = %q, want %q: the idiom is legal, the check only reports it", check.Status, StatusOK)
	}
	if check.Fix != "" {
		t.Errorf("Fix = %q, want empty: this check has no --fix", check.Fix)
	}
	if check.Category != CategoryMetadata {
		t.Errorf("Category = %q, want %q", check.Category, CategoryMetadata)
	}
	if !strings.Contains(check.Message, "2 parent→own-descendant") {
		t.Errorf("Message = %q, want a count of exactly the two close gates", check.Message)
	}
	for _, wantDetail := range []string{parent.ID + "→" + child.ID, parent.ID + "→" + grandchild.ID} {
		if !strings.Contains(check.Detail, wantDetail) {
			t.Errorf("Detail = %q, want it to name %q", check.Detail, wantDetail)
		}
	}
	if strings.Contains(check.Detail, other.ID) {
		t.Errorf("Detail = %q, must not list the exogenous blocker %s", check.Detail, other.ID)
	}
}

// TestCheckParentBlocksOwnChildDB_QueryFailureWarns verifies that an inventory
// query that fails is a warning, not an OK that reads as "no gates". A closed
// handle fails every query without reaching a server.
func TestCheckParentBlocksOwnChildDB_QueryFailureWarns(t *testing.T) {
	db, err := sql.Open("mysql", "root@tcp(127.0.0.1:1)/none")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	_ = db.Close()

	check := checkParentBlocksOwnChildDB(db)

	if check.Status != StatusWarning {
		t.Errorf("Status = %q, want %q", check.Status, StatusWarning)
	}
	if check.Message != "N/A (query failed)" {
		t.Errorf("Message = %q, want %q", check.Message, "N/A (query failed)")
	}
	if check.Detail == "" {
		t.Error("Detail is empty, want the query error")
	}
}

// TestTruncateDetail_CutsOnARuneBoundary pins the --json contract: a cut
// detail line is still valid UTF-8. The gate inventory joins its entries with
// U+2192, three bytes wide, so a byte-indexed cut lands mid-rune for two of
// every three offsets and emits a replacement-character sequence into JSON.
func TestTruncateDetail_CutsOnARuneBoundary(t *testing.T) {
	// One entry is 6 bytes: "a", U+2192 (3), "b", ",", " " — so successive
	// totals step past 200 at offsets that are not rune starts.
	entry := "a→b"
	for n := 1; n < 120; n++ {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = entry
		}
		got := truncateDetail(strings.Join(parts, ", "))
		if !utf8.ValidString(got) {
			t.Fatalf("n=%d: truncateDetail produced invalid UTF-8: %q", n, got)
		}
		if len(got) > detailMaxBytes+len("...") {
			t.Fatalf("n=%d: truncateDetail returned %d bytes, want <= %d",
				n, len(got), detailMaxBytes+len("..."))
		}
	}

	// Short input is returned whole, with no ellipsis.
	if got := truncateDetail("wr-p→wr-c"); got != "wr-p→wr-c" {
		t.Errorf("short detail was rewritten: %q", got)
	}
}
