package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

// setupValidateTestDB creates a temp .beads workspace with a configured database.
// The caller must call store.Close() when done inserting test data.
func setupValidateTestDB(t *testing.T, prefix string) (tmpDir string, store *sqlite.SQLiteStorage) {
	t.Helper()
	tmpDir = t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(beadsDir, beads.CanonicalDatabaseName)
	ctx := context.Background()

	var err error
	store, err = sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	if err := store.SetConfig(ctx, "issue_prefix", prefix); err != nil {
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}

	return tmpDir, store
}

func TestValidateCheck_AllClean(t *testing.T) {
	tmpDir, store := setupValidateTestDB(t, "val")
	ctx := context.Background()

	issues := []*types.Issue{
		{Title: "Fix login bug", Description: "Login fails", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeBug},
		{Title: "Add search", Description: "Full-text search", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	}
	for _, issue := range issues {
		if err := store.CreateIssue(ctx, issue, "val"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}
	}

	// Write clean JSONL so git conflicts check has a file to scan
	jsonlPath := filepath.Join(tmpDir, ".beads", "issues.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(""), 0644); err != nil {
		t.Fatalf("Failed to create JSONL: %v", err)
	}
	store.Close()

	checks := collectValidateChecks(tmpDir)

	for _, cr := range checks {
		if cr.check.Status != statusOK {
			t.Errorf("%s: status = %q, want %q (message: %s)", cr.check.Name, cr.check.Status, statusOK, cr.check.Message)
		}
	}
	if len(checks) != 4 {
		t.Errorf("Expected 4 checks, got %d", len(checks))
	}
}

func TestValidateCheck_DetectsDuplicates(t *testing.T) {
	tmpDir, store := setupValidateTestDB(t, "test")
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		issue := &types.Issue{
			Title:       "Duplicate task",
			Description: "Same description",
			Status:      types.StatusOpen,
			Priority:    2,
			IssueType:   types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}
	}
	store.Close()

	checks := collectValidateChecks(tmpDir)

	for _, cr := range checks {
		if cr.check.Name == "Duplicate Issues" {
			if cr.check.Status != statusWarning {
				t.Errorf("Duplicate Issues status = %q, want %q", cr.check.Status, statusWarning)
			}
			return
		}
	}
	t.Error("Duplicate Issues check not found")
}

func TestValidateCheck_DetectsOrphanedDeps(t *testing.T) {
	tmpDir, store := setupValidateTestDB(t, "test")
	ctx := context.Background()

	issue := &types.Issue{
		Title:     "Real issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	db := store.UnderlyingDB()
	_, err := db.Exec("INSERT INTO dependencies (issue_id, depends_on_id, type, created_by) VALUES (?, ?, ?, ?)",
		issue.ID, "test-nonexistent", "blocks", "test")
	if err != nil {
		t.Fatalf("Failed to insert orphaned dep: %v", err)
	}
	store.Close()

	checks := collectValidateChecks(tmpDir)

	for _, cr := range checks {
		if cr.check.Name == "Orphaned Dependencies" {
			if cr.check.Status != statusWarning {
				t.Errorf("Orphaned Dependencies status = %q, want %q", cr.check.Status, statusWarning)
			}
			if !cr.fixable {
				t.Error("Orphaned Dependencies should be marked fixable")
			}
			return
		}
	}
	t.Error("Orphaned Dependencies check not found")
}

func TestValidateCheck_DetectsGitConflicts(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	conflictContent := `{"id":"test-1","title":"Issue 1","status":"open"}
<<<<<<< HEAD
{"id":"test-2","title":"Issue 2 local","status":"open"}
=======
{"id":"test-2","title":"Issue 2 remote","status":"open"}
>>>>>>> origin/main
`
	if err := os.WriteFile(jsonlPath, []byte(conflictContent), 0644); err != nil {
		t.Fatalf("Failed to write JSONL: %v", err)
	}

	checks := collectValidateChecks(tmpDir)

	for _, cr := range checks {
		if cr.check.Name == "Git Conflicts" {
			if cr.check.Status != statusError {
				t.Errorf("Git Conflicts status = %q, want %q", cr.check.Status, statusError)
			}
			return
		}
	}
	t.Error("Git Conflicts check not found")
}

func TestValidateCheck_DetectsTestPollution(t *testing.T) {
	tmpDir, store := setupValidateTestDB(t, "test")
	ctx := context.Background()

	testIssues := []*types.Issue{
		{Title: "test-pollution-check", Description: "A test issue", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{Title: "Test Issue 1", Description: "Another test", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	}
	for _, issue := range testIssues {
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}
	}
	store.Close()

	checks := collectValidateChecks(tmpDir)

	for _, cr := range checks {
		if cr.check.Name == "Test Pollution" {
			if cr.check.Status != statusWarning {
				t.Errorf("Test Pollution status = %q, want %q", cr.check.Status, statusWarning)
			}
			return
		}
	}
	t.Error("Test Pollution check not found")
}

func TestValidateCheck_NoBeadsDir(t *testing.T) {
	tmpDir := t.TempDir()

	checks := collectValidateChecks(tmpDir)

	for _, cr := range checks {
		if cr.check.Status != statusOK {
			t.Errorf("%s: status = %q, want %q when no .beads/ exists", cr.check.Name, cr.check.Status, statusOK)
		}
	}
}

func TestValidateCheck_FixOrphanedDeps(t *testing.T) {
	tmpDir, store := setupValidateTestDB(t, "test")
	ctx := context.Background()

	issue := &types.Issue{
		Title:     "Real issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	db := store.UnderlyingDB()
	_, err := db.Exec("INSERT INTO dependencies (issue_id, depends_on_id, type, created_by) VALUES (?, ?, ?, ?)",
		issue.ID, "test-nonexistent", "blocks", "test")
	if err != nil {
		t.Fatalf("Failed to insert orphaned dep: %v", err)
	}
	store.Close()

	// Verify orphan exists before fix
	checks := collectValidateChecks(tmpDir)
	for _, cr := range checks {
		if cr.check.Name == "Orphaned Dependencies" && cr.check.Status == statusOK {
			t.Fatal("Pre-condition: expected orphaned deps to be detected")
		}
	}

	// Enable fix mode with --yes to skip confirmation
	origFix := doctorFix
	origYes := doctorYes
	doctorFix = true
	doctorYes = true
	defer func() {
		doctorFix = origFix
		doctorYes = origYes
	}()

	// runValidateCheckInner applies fixes then re-checks
	ok := runValidateCheckInner(tmpDir)

	// The orphaned dep should be fixed, but test pollution from "test-" prefix
	// means overallOK may still be false. Just verify orphans are gone.
	_ = ok
	doctorFix = false
	checks = collectValidateChecks(tmpDir)
	for _, cr := range checks {
		if cr.check.Name == "Orphaned Dependencies" {
			if cr.check.Status != statusOK {
				t.Errorf("After fix: Orphaned Dependencies status = %q, want %q", cr.check.Status, statusOK)
			}
			return
		}
	}
	t.Error("Orphaned Dependencies check not found after fix")
}

func TestValidateOverallOK(t *testing.T) {
	allPass := []validateCheckResult{
		{check: doctorCheck{Status: statusOK}},
		{check: doctorCheck{Status: statusOK}},
	}
	if !validateOverallOK(allPass) {
		t.Error("Expected true when all checks pass")
	}

	hasWarning := []validateCheckResult{
		{check: doctorCheck{Status: statusOK}},
		{check: doctorCheck{Status: statusWarning}},
	}
	if validateOverallOK(hasWarning) {
		t.Error("Expected false when a check has warning")
	}

	hasError := []validateCheckResult{
		{check: doctorCheck{Status: statusOK}},
		{check: doctorCheck{Status: statusError}},
	}
	if validateOverallOK(hasError) {
		t.Error("Expected false when a check has error")
	}
}
