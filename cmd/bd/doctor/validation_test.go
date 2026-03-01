//go:build cgo

package doctor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

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
	if err := os.Mkdir(beadsDir, 0755); err != nil {
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

// TestCheckDuplicateIssues_GastownUnderThreshold verifies that with gastown mode enabled,
// duplicates under the threshold are OK.
func TestCheckDuplicateIssues_GastownUnderThreshold(t *testing.T) {
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
		t.Errorf("Status = %q, want %q (under gastown threshold)", check.Status, StatusOK)
		t.Logf("Message: %s", check.Message)
	}
	if check.Message != "50 duplicate(s) detected (within gastown threshold of 1000)" {
		t.Errorf("Message = %q, want message about being within threshold", check.Message)
	}
}

// TestCheckDuplicateIssues_GastownOverThreshold verifies that with gastown mode enabled,
// duplicates over the threshold still warn.
func TestCheckDuplicateIssues_GastownOverThreshold(t *testing.T) {
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
		t.Errorf("Status = %q, want %q (over gastown threshold)", check.Status, StatusWarning)
	}
	if check.Message != "50 duplicate issue(s) in 1 group(s)" {
		t.Errorf("Message = %q, want '50 duplicate issue(s) in 1 group(s)'", check.Message)
	}
}

// TestCheckDuplicateIssues_GastownCustomThreshold verifies custom threshold works.
func TestCheckDuplicateIssues_GastownCustomThreshold(t *testing.T) {
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

// TestCheckDuplicateIssues_NonGastownMode verifies that without gastown mode,
// any duplicates are warnings (backward compatibility).
func TestCheckDuplicateIssues_NonGastownMode(t *testing.T) {
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
		t.Errorf("Status = %q, want %q (non-gastown should warn on any duplicates)", check.Status, StatusWarning)
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
		`INSERT INTO dependencies (issue_id, depends_on_id, type, created_at, created_by) VALUES (?, ?, 'blocks', NOW(), 'test')`,
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
		`INSERT INTO dependencies (issue_id, depends_on_id, type, created_at, created_by) VALUES (?, ?, 'parent-child', NOW(), 'test')`,
		childID, parent.ID)
	if err != nil {
		t.Fatalf("Failed to insert dependency: %v", err)
	}

	check := checkChildParentDependenciesDB(db)

	if check.Status != StatusOK {
		t.Errorf("Status = %q, want %q (parent-child type should be ignored)", check.Status, StatusOK)
	}
}

// TestCheckTestPollution_NoTestIssues verifies OK when no test issues exist.
func TestCheckTestPollution_NoTestIssues(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	check := CheckTestPollution(tmpDir)

	// Without a database, should return N/A
	if check.Status != StatusOK {
		t.Errorf("Status = %q, want %q", check.Status, StatusOK)
	}
}

// TestCheckGitConflicts_DoltBackend verifies CheckGitConflicts returns N/A
// for Dolt backend (Dolt handles conflicts natively).
func TestCheckGitConflicts_DoltBackend(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Default backend is Dolt when no config exists
	check := CheckGitConflicts(tmpDir)

	if check.Status != StatusOK {
		t.Errorf("Status = %q, want %q", check.Status, StatusOK)
	}
	if check.Message != "N/A (Dolt backend handles conflicts natively)" {
		t.Errorf("Message = %q, want %q", check.Message, "N/A (Dolt backend handles conflicts natively)")
	}
}
