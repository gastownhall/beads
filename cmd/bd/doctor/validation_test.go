package doctor

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

// TestCheckDuplicateIssues_ClosedIssuesExcluded verifies that closed issues
// are not flagged as duplicates (bug fix: bd-sali).
// Previously, doctor used title+description only and included closed issues,
// while bd duplicates excluded closed issues and used full content hash.
func TestCheckDuplicateIssues_ClosedIssuesExcluded(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(beadsDir, beads.CanonicalDatabaseName)
	ctx := context.Background()

	store, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Initialize database with prefix
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}

	// Create closed issues with same title+description
	// These should NOT be flagged as duplicates
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

	// Close the store so CheckDuplicateIssues can open it
	store.Close()

	check := CheckDuplicateIssues(tmpDir, false, 1000)

	// Should NOT report duplicates because all are closed
	if check.Status != StatusOK {
		t.Errorf("Status = %q, want %q (closed issues should be excluded from duplicate detection)", check.Status, StatusOK)
		t.Logf("Message: %s", check.Message)
	}
}

// TestCheckDuplicateIssues_OpenDuplicatesDetected verifies that open issues
// with identical content ARE flagged as duplicates.
func TestCheckDuplicateIssues_OpenDuplicatesDetected(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(beadsDir, beads.CanonicalDatabaseName)
	ctx := context.Background()

	store, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Initialize database with prefix
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}

	// Create open issues with same content - these SHOULD be flagged
	issues := []*types.Issue{
		{Title: "Fix auth bug", Description: "Users cannot login", Design: "Use OAuth", AcceptanceCriteria: "User can login", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeBug},
		{Title: "Fix auth bug", Description: "Users cannot login", Design: "Use OAuth", AcceptanceCriteria: "User can login", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeBug},
	}

	for _, issue := range issues {
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}
	}

	store.Close()

	check := CheckDuplicateIssues(tmpDir, false, 1000)

	if check.Status != StatusWarning {
		t.Errorf("Status = %q, want %q (open duplicates should be detected)", check.Status, StatusWarning)
	}
	if check.Message != "1 duplicate issue(s) in 1 group(s)" {
		t.Errorf("Message = %q, want '1 duplicate issue(s) in 1 group(s)'", check.Message)
	}
}

// TestCheckDuplicateIssues_DifferentDesignNotDuplicate verifies that issues
// with same title+description but different design are NOT duplicates.
// This tests the full content hash (title+description+design+acceptanceCriteria+status).
func TestCheckDuplicateIssues_DifferentDesignNotDuplicate(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(beadsDir, beads.CanonicalDatabaseName)
	ctx := context.Background()

	store, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Initialize database with prefix
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}

	// Create open issues with same title+description but DIFFERENT design
	// These should NOT be flagged as duplicates
	issues := []*types.Issue{
		{Title: "Fix auth bug", Description: "Users cannot login", Design: "Use OAuth", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeBug},
		{Title: "Fix auth bug", Description: "Users cannot login", Design: "Use SAML", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeBug},
	}

	for _, issue := range issues {
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}
	}

	store.Close()

	check := CheckDuplicateIssues(tmpDir, false, 1000)

	if check.Status != StatusOK {
		t.Errorf("Status = %q, want %q (different design = not duplicates)", check.Status, StatusOK)
		t.Logf("Message: %s", check.Message)
	}
}

// TestCheckDuplicateIssues_MixedOpenClosed verifies correct behavior when
// there are both open and closed issues with same content.
// Only open duplicates should be flagged.
func TestCheckDuplicateIssues_MixedOpenClosed(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(beadsDir, beads.CanonicalDatabaseName)
	ctx := context.Background()

	store, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Initialize database with prefix
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}

	// Create open issues first (will be duplicates of each other)
	openIssues := []*types.Issue{
		{Title: "Task A", Description: "Do something", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{Title: "Task A", Description: "Do something", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	}

	for _, issue := range openIssues {
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}
	}

	// Create a closed issue with same content (should NOT be part of duplicate group)
	closedIssue := &types.Issue{Title: "Task A", Description: "Do something", Status: types.StatusClosed, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, closedIssue, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	store.Close()

	check := CheckDuplicateIssues(tmpDir, false, 1000)

	// Should detect 1 duplicate (the pair of open issues)
	if check.Status != StatusWarning {
		t.Errorf("Status = %q, want %q", check.Status, StatusWarning)
	}
	if check.Message != "1 duplicate issue(s) in 1 group(s)" {
		t.Errorf("Message = %q, want '1 duplicate issue(s) in 1 group(s)'", check.Message)
	}
}

// TestCheckDuplicateIssues_TombstonesExcluded verifies tombstoned issues
// are excluded from duplicate detection.
func TestCheckDuplicateIssues_TombstonesExcluded(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(beadsDir, beads.CanonicalDatabaseName)
	ctx := context.Background()

	store, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Initialize database with prefix
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}

	// Create tombstoned issues - these should NOT be flagged
	issues := []*types.Issue{
		{Title: "Deleted issue", Description: "Was deleted", Status: types.StatusTombstone, Priority: 2, IssueType: types.TypeTask},
		{Title: "Deleted issue", Description: "Was deleted", Status: types.StatusTombstone, Priority: 2, IssueType: types.TypeTask},
	}

	for _, issue := range issues {
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}
	}

	store.Close()

	check := CheckDuplicateIssues(tmpDir, false, 1000)

	if check.Status != StatusOK {
		t.Errorf("Status = %q, want %q (tombstones should be excluded)", check.Status, StatusOK)
	}
}

// TestCheckDuplicateIssues_NoDatabase verifies graceful handling when no database exists.
func TestCheckDuplicateIssues_NoDatabase(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// No database file created

	check := CheckDuplicateIssues(tmpDir, false, 1000)

	if check.Status != StatusOK {
		t.Errorf("Status = %q, want %q", check.Status, StatusOK)
	}
	if check.Message != "N/A (no database)" {
		t.Errorf("Message = %q, want 'N/A (no database)'", check.Message)
	}
}

// TestCheckDuplicateIssues_GastownUnderThreshold verifies that with gastown mode enabled,
// duplicates under the threshold are OK.
func TestCheckDuplicateIssues_GastownUnderThreshold(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(beadsDir, beads.CanonicalDatabaseName)
	ctx := context.Background()

	store, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Initialize database with prefix
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}

	// Create 50 duplicate issues (typical gastown wisp count)
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

	store.Close()

	check := CheckDuplicateIssues(tmpDir, true, 1000)

	// With gastown mode and threshold=1000, 50 duplicates should be OK
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
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(beadsDir, beads.CanonicalDatabaseName)
	ctx := context.Background()

	store, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Initialize database with prefix
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}

	// Create 1500 duplicate issues (over threshold, indicates a problem)
	for i := 0; i < 1501; i++ {
		issue := &types.Issue{
			Title:       "Runaway wisps",
			Description: "Too many wisps",
			Status:      types.StatusOpen,
			Priority:    2,
			IssueType:   types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}
	}

	store.Close()

	check := CheckDuplicateIssues(tmpDir, true, 1000)

	// With gastown mode and threshold=1000, 1500 duplicates should warn
	if check.Status != StatusWarning {
		t.Errorf("Status = %q, want %q (over gastown threshold)", check.Status, StatusWarning)
	}
	if check.Message != "1500 duplicate issue(s) in 1 group(s)" {
		t.Errorf("Message = %q, want '1500 duplicate issue(s) in 1 group(s)'", check.Message)
	}
}

// TestCheckDuplicateIssues_GastownCustomThreshold verifies custom threshold works.
func TestCheckDuplicateIssues_GastownCustomThreshold(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(beadsDir, beads.CanonicalDatabaseName)
	ctx := context.Background()

	store, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Initialize database with prefix
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}

	// Create 500 duplicate issues
	for i := 0; i < 501; i++ {
		issue := &types.Issue{
			Title:       "Custom threshold test",
			Description: "Test custom threshold",
			Status:      types.StatusOpen,
			Priority:    2,
			IssueType:   types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}
	}

	store.Close()

	// With custom threshold of 250, 500 duplicates should warn
	check := CheckDuplicateIssues(tmpDir, true, 250)

	if check.Status != StatusWarning {
		t.Errorf("Status = %q, want %q (over custom threshold of 250)", check.Status, StatusWarning)
	}
	if check.Message != "500 duplicate issue(s) in 1 group(s)" {
		t.Errorf("Message = %q, want '500 duplicate issue(s) in 1 group(s)'", check.Message)
	}
}

// TestCheckDuplicateIssues_NonGastownMode verifies that without gastown mode,
// any duplicates are warnings (backward compatibility).
func TestCheckDuplicateIssues_NonGastownMode(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(beadsDir, beads.CanonicalDatabaseName)
	ctx := context.Background()

	store, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Initialize database with prefix
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}

	// Create 50 duplicate issues
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

	store.Close()

	// Without gastown mode, even 50 duplicates should warn
	check := CheckDuplicateIssues(tmpDir, false, 1000)

	if check.Status != StatusWarning {
		t.Errorf("Status = %q, want %q (non-gastown should warn on any duplicates)", check.Status, StatusWarning)
	}
	if check.Message != "50 duplicate issue(s) in 1 group(s)" {
		t.Errorf("Message = %q, want '50 duplicate issue(s) in 1 group(s)'", check.Message)
	}
}
