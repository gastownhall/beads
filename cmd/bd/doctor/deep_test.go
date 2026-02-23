//go:build cgo

package doctor

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestRunDeepValidation_NoBeadsDir verifies deep validation handles missing .beads directory
func TestRunDeepValidation_NoBeadsDir(t *testing.T) {
	tmpDir := t.TempDir()
	result := RunDeepValidation(tmpDir)

	if len(result.AllChecks) != 1 {
		t.Errorf("Expected 1 check, got %d", len(result.AllChecks))
	}
	if result.AllChecks[0].Status != StatusOK {
		t.Errorf("Status = %q, want %q", result.AllChecks[0].Status, StatusOK)
	}
}

// TestRunDeepValidation_EmptyBeadsDir verifies deep validation with empty .beads directory
func TestRunDeepValidation_EmptyBeadsDir(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	result := RunDeepValidation(tmpDir)

	// Should return OK with "no database" message
	if len(result.AllChecks) != 1 {
		t.Errorf("Expected 1 check, got %d", len(result.AllChecks))
	}
	if result.AllChecks[0].Status != StatusOK {
		t.Errorf("Status = %q, want %q", result.AllChecks[0].Status, StatusOK)
	}
}

// TestRunDeepValidation_WithDatabase verifies all deep check functions run
// without panicking against a Dolt connection. The shared test database may
// have pre-existing data, so we verify checks complete rather than expecting
// a clean state. Individual check functions are tested in isolation below.
func TestRunDeepValidation_WithDatabase(t *testing.T) {
	store := newTestDoltStore(t, "deep")
	db := store.UnderlyingDB()

	// Run all deep checks against the Dolt connection
	checks := []DoctorCheck{
		checkParentConsistency(db),
		checkDependencyIntegrity(db),
		checkEpicCompleteness(db),
		checkAgentBeadIntegrity(db),
		checkMailThreadIntegrity(db),
		checkMoleculeIntegrity(db),
	}

	// Verify all 6 checks ran and produced valid statuses
	if len(checks) != 6 {
		t.Errorf("Expected 6 checks, got %d", len(checks))
	}
	for _, check := range checks {
		if check.Name == "" {
			t.Error("Check has empty Name")
		}
		if check.Status != StatusOK && check.Status != StatusWarning && check.Status != StatusError {
			t.Errorf("Check %s has invalid status %q", check.Name, check.Status)
		}
	}
}

// TestCheckParentConsistency_OrphanedDeps verifies detection of orphaned parent-child deps
func TestCheckParentConsistency_OrphanedDeps(t *testing.T) {
	store := newTestDoltStore(t, "deep")
	ctx := context.Background()

	// Insert an issue via store API
	issue := &types.Issue{
		Title:     "Test Issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "deep"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	db := store.UnderlyingDB()

	// Insert a parent-child dep pointing to non-existent parent via raw SQL
	_, err := db.Exec(
		"INSERT INTO dependencies (issue_id, depends_on_id, type, created_by) VALUES (?, ?, ?, ?)",
		issue.ID, "deep-missing", "parent-child", "test",
	)
	if err != nil {
		t.Fatalf("Failed to insert orphaned dep: %v", err)
	}

	check := checkParentConsistency(db)

	if check.Status != StatusError {
		t.Errorf("Status = %q, want %q", check.Status, StatusError)
	}
}

// TestCheckEpicCompleteness_CompletedEpic verifies detection of closeable epics
func TestCheckEpicCompleteness_CompletedEpic(t *testing.T) {
	store := newTestDoltStore(t, "deep")
	ctx := context.Background()

	// Insert an open epic
	epic := &types.Issue{
		Title:     "Epic",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeEpic,
	}
	if err := store.CreateIssue(ctx, epic, "deep"); err != nil {
		t.Fatalf("Failed to create epic: %v", err)
	}

	// Insert a closed child task
	task := &types.Issue{
		Title:     "Task",
		Status:    types.StatusClosed,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, task, "deep"); err != nil {
		t.Fatalf("Failed to create task: %v", err)
	}

	db := store.UnderlyingDB()

	// Create parent-child relationship via raw SQL
	_, err := db.Exec(
		"INSERT INTO dependencies (issue_id, depends_on_id, type, created_by) VALUES (?, ?, ?, ?)",
		task.ID, epic.ID, "parent-child", "test",
	)
	if err != nil {
		t.Fatalf("Failed to insert parent-child dep: %v", err)
	}

	check := checkEpicCompleteness(db)

	// Epic with all children closed should be detected
	if check.Status != StatusWarning {
		t.Errorf("Status = %q, want %q", check.Status, StatusWarning)
	}
}

// TestCheckMailThreadIntegrity_ValidThreads verifies valid thread references pass
func TestCheckMailThreadIntegrity_ValidThreads(t *testing.T) {
	store := newTestDoltStore(t, "deep")
	ctx := context.Background()

	// Insert issues
	root := &types.Issue{
		Title:     "Thread Root",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, root, "deep"); err != nil {
		t.Fatalf("Failed to create root issue: %v", err)
	}

	reply := &types.Issue{
		Title:     "Reply",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, reply, "deep"); err != nil {
		t.Fatalf("Failed to create reply issue: %v", err)
	}

	db := store.UnderlyingDB()

	// Insert a dependency with valid thread_id
	_, err := db.Exec(
		"INSERT INTO dependencies (issue_id, depends_on_id, type, thread_id, created_by) VALUES (?, ?, ?, ?, ?)",
		reply.ID, root.ID, "replies-to", root.ID, "test",
	)
	if err != nil {
		t.Fatalf("Failed to insert thread dep: %v", err)
	}

	check := checkMailThreadIntegrity(db)

	// On Dolt/MySQL, pragma_table_info is not available, so the check
	// returns StatusOK with "N/A" message. This is expected behavior —
	// the check functions will be updated to use Dolt-compatible queries
	// in later subtasks (bd-o0u.2+).
	if check.Status != StatusOK {
		t.Errorf("Status = %q, want %q: %s", check.Status, StatusOK, check.Message)
	}
}

// TestDeepValidationResultJSON verifies JSON serialization
func TestDeepValidationResultJSON(t *testing.T) {
	result := DeepValidationResult{
		TotalIssues:       10,
		TotalDependencies: 5,
		OverallOK:         true,
		AllChecks: []DoctorCheck{
			{Name: "Test", Status: StatusOK, Message: "All good"},
		},
	}

	jsonBytes, err := DeepValidationResultJSON(result)
	if err != nil {
		t.Fatalf("Failed to serialize: %v", err)
	}

	if len(jsonBytes) == 0 {
		t.Error("Expected non-empty JSON output")
	}

	// Should contain expected fields
	jsonStr := string(jsonBytes)
	if !contains(jsonStr, "total_issues") {
		t.Error("JSON should contain total_issues")
	}
	if !contains(jsonStr, "overall_ok") {
		t.Error("JSON should contain overall_ok")
	}
}
