//go:build cgo

package doctor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

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

// TestCheckParentConsistency_OrphanedDeps verifies detection of orphaned parent-child deps
func TestCheckParentConsistency_OrphanedDeps(t *testing.T) {
	store := newTestDoltStore(t, "bd")
	ctx := context.Background()

	// Create an issue
	issue := &types.Issue{
		ID:        "bd-1",
		Title:     "Test Issue",
		Status:    types.StatusOpen,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatal(err)
	}

	// Insert a parent-child dep pointing to non-existent parent via raw SQL
	db := store.DB()
	_, err := db.ExecContext(ctx,
		"INSERT INTO dependencies (issue_id, depends_on_id, type, created_at, created_by) VALUES (?, ?, ?, NOW(), ?)",
		"bd-1", "bd-missing", "parent-child", "test")
	if err != nil {
		t.Fatal(err)
	}

	check := checkParentConsistency(db)

	if check.Status != StatusError {
		t.Errorf("Status = %q, want %q", check.Status, StatusError)
	}
}

// TestCheckEpicCompleteness_CompletedEpic verifies detection of closeable epics
func TestCheckEpicCompleteness_CompletedEpic(t *testing.T) {
	store := newTestDoltStore(t, "epic")
	ctx := context.Background()

	// Insert an open epic
	epic := &types.Issue{
		ID:        "epic-1",
		Title:     "Epic",
		Status:    types.StatusOpen,
		IssueType: types.TypeEpic,
		CreatedAt: time.Now(),
	}
	if err := store.CreateIssue(ctx, epic, "test"); err != nil {
		t.Fatal(err)
	}

	// Insert a closed child task
	task := &types.Issue{
		ID:        "epic-1.1",
		Title:     "Task",
		Status:    types.StatusClosed,
		IssueType: types.TypeTask,
		ClosedAt:  ptrTime(time.Now()),
		CreatedAt: time.Now(),
	}
	if err := store.CreateIssue(ctx, task, "test"); err != nil {
		t.Fatal(err)
	}

	// Create parent-child relationship
	dep := &types.Dependency{
		IssueID:     "epic-1.1",
		DependsOnID: "epic-1",
		Type:        types.DepParentChild,
		CreatedAt:   time.Now(),
		CreatedBy:   "test",
	}
	if err := store.AddDependency(ctx, dep, "test"); err != nil {
		t.Fatal(err)
	}

	db := store.DB()
	check := checkEpicCompleteness(db)

	// Epic with all children closed should be detected
	if check.Status != StatusWarning {
		t.Errorf("Status = %q, want %q", check.Status, StatusWarning)
	}
}

// TestCheckMailThreadIntegrity_ValidThreads verifies valid thread references pass
func TestCheckMailThreadIntegrity_ValidThreads(t *testing.T) {
	store := newTestDoltStore(t, "thread")
	ctx := context.Background()

	// Insert issues
	root := &types.Issue{
		ID:        "thread-root",
		Title:     "Thread Root",
		Status:    types.StatusOpen,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
	}
	if err := store.CreateIssue(ctx, root, "test"); err != nil {
		t.Fatal(err)
	}

	reply := &types.Issue{
		ID:        "thread-reply",
		Title:     "Reply",
		Status:    types.StatusOpen,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
	}
	if err := store.CreateIssue(ctx, reply, "test"); err != nil {
		t.Fatal(err)
	}

	// Insert a dependency with valid thread_id via raw SQL (replies-to with thread_id)
	db := store.DB()
	_, err := db.ExecContext(ctx,
		"INSERT INTO dependencies (issue_id, depends_on_id, type, thread_id, created_at, created_by) VALUES (?, ?, ?, ?, NOW(), ?)",
		"thread-reply", "thread-root", "replies-to", "thread-root", "test")
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
