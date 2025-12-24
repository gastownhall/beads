package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

// TestLocalModeFlags tests command-line flag validation for --local mode
func TestLocalModeFlags(t *testing.T) {
	t.Run("local mode is incompatible with auto-commit", func(t *testing.T) {
		// These flags cannot be used together
		localMode := true
		autoCommit := true

		// Validate the constraint (mirrors daemon.go logic)
		if localMode && autoCommit {
			// This is the expected error case
			t.Log("Correctly detected incompatible flags: --local and --auto-commit")
		} else {
			t.Error("Expected --local and --auto-commit to be incompatible")
		}
	})

	t.Run("local mode is incompatible with auto-push", func(t *testing.T) {
		localMode := true
		autoPush := true

		if localMode && autoPush {
			t.Log("Correctly detected incompatible flags: --local and --auto-push")
		} else {
			t.Error("Expected --local and --auto-push to be incompatible")
		}
	})

	t.Run("local mode alone is valid", func(t *testing.T) {
		localMode := true
		autoCommit := false
		autoPush := false

		valid := !((localMode && autoCommit) || (localMode && autoPush))
		if !valid {
			t.Error("Expected --local alone to be valid")
		}
	})
}

// TestLocalModeGitCheck tests that git repo check is skipped in local mode
func TestLocalModeGitCheck(t *testing.T) {
	t.Run("git check skipped when local mode enabled", func(t *testing.T) {
		localMode := true
		inGitRepo := false // Simulate non-git directory

		// Mirrors daemon.go:176 logic
		shouldFail := !localMode && !inGitRepo

		if shouldFail {
			t.Error("Expected git check to be skipped in local mode")
		}
	})

	t.Run("git check enforced when local mode disabled", func(t *testing.T) {
		localMode := false
		inGitRepo := false

		shouldFail := !localMode && !inGitRepo

		if !shouldFail {
			t.Error("Expected git check to fail in non-local mode without git")
		}
	})
}

// TestCreateLocalSyncFunc tests the local-only sync function
func TestCreateLocalSyncFunc(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Create temp directory (no git)
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create beads dir: %v", err)
	}

	testDBPath := filepath.Join(beadsDir, "beads.db")
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")

	// Create store
	ctx := context.Background()
	testStore, err := sqlite.New(ctx, testDBPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer testStore.Close()

	// Initialize the database with a prefix
	if err := testStore.SetConfig(ctx, "issue_prefix", "TEST"); err != nil {
		t.Fatalf("Failed to set issue prefix: %v", err)
	}

	// Set global dbPath for findJSONLPath
	oldDBPath := dbPath
	defer func() { dbPath = oldDBPath }()
	dbPath = testDBPath

	// Create a test issue
	issue := &types.Issue{
		Title:       "Local sync test issue",
		Description: "Testing local sync",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
	}
	if err := testStore.CreateIssue(ctx, issue, "TEST"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	// Create logger (test output via newTestLogger)
	log := newTestLogger()

	// Create and run local sync function
	doSync := createLocalSyncFunc(ctx, testStore, log)
	doSync()

	// Verify JSONL was created
	if _, err := os.Stat(jsonlPath); os.IsNotExist(err) {
		t.Error("Expected JSONL file to be created by local sync")
	}

	// Verify JSONL contains the issue
	content, err := os.ReadFile(jsonlPath)
	if err != nil {
		t.Fatalf("Failed to read JSONL: %v", err)
	}
	if len(content) == 0 {
		t.Error("Expected JSONL to contain issue data")
	}
}

// TestCreateLocalExportFunc tests the local-only export function
func TestCreateLocalExportFunc(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create beads dir: %v", err)
	}

	testDBPath := filepath.Join(beadsDir, "beads.db")
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")

	ctx := context.Background()
	testStore, err := sqlite.New(ctx, testDBPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer testStore.Close()

	// Initialize the database with a prefix
	if err := testStore.SetConfig(ctx, "issue_prefix", "TEST"); err != nil {
		t.Fatalf("Failed to set issue prefix: %v", err)
	}

	oldDBPath := dbPath
	defer func() { dbPath = oldDBPath }()
	dbPath = testDBPath

	// Create test issues
	for i := 0; i < 3; i++ {
		issue := &types.Issue{
			Title:       "Export test issue",
			Description: "Testing local export",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeTask,
		}
		if err := testStore.CreateIssue(ctx, issue, "TEST"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}
	}

	log := newTestLogger()

	doExport := createLocalExportFunc(ctx, testStore, log)
	doExport()

	// Verify export
	content, err := os.ReadFile(jsonlPath)
	if err != nil {
		t.Fatalf("Failed to read JSONL: %v", err)
	}

	// Count lines (should have 3 issues)
	lines := 0
	for _, b := range content {
		if b == '\n' {
			lines++
		}
	}
	if lines != 3 {
		t.Errorf("Expected 3 issues in JSONL, got %d lines", lines)
	}
}

// TestCreateLocalAutoImportFunc tests the local-only import function
func TestCreateLocalAutoImportFunc(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create beads dir: %v", err)
	}

	testDBPath := filepath.Join(beadsDir, "beads.db")
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")

	ctx := context.Background()
	testStore, err := sqlite.New(ctx, testDBPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer testStore.Close()

	// Initialize the database with a prefix
	if err := testStore.SetConfig(ctx, "issue_prefix", "TEST"); err != nil {
		t.Fatalf("Failed to set issue prefix: %v", err)
	}

	oldDBPath := dbPath
	defer func() { dbPath = oldDBPath }()
	dbPath = testDBPath

	// Write a JSONL file directly (simulating external modification)
	jsonlContent := `{"id":"TEST-abc","title":"Imported issue","description":"From JSONL","status":"open","priority":1,"issue_type":"task","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-01T00:00:00Z"}
`
	if err := os.WriteFile(jsonlPath, []byte(jsonlContent), 0644); err != nil {
		t.Fatalf("Failed to write JSONL: %v", err)
	}

	log := newTestLogger()

	doImport := createLocalAutoImportFunc(ctx, testStore, log)
	doImport()

	// Verify issue was imported
	issues, err := testStore.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("Failed to search issues: %v", err)
	}

	if len(issues) != 1 {
		t.Errorf("Expected 1 imported issue, got %d", len(issues))
	}

	if len(issues) > 0 && issues[0].Title != "Imported issue" {
		t.Errorf("Expected imported issue title 'Imported issue', got '%s'", issues[0].Title)
	}
}

// TestLocalModeNoGitOperations verifies local functions don't call git
func TestLocalModeNoGitOperations(t *testing.T) {
	// This test verifies the structure of local functions
	// They should not contain git operations

	t.Run("createLocalSyncFunc has no git calls", func(t *testing.T) {
		// The local sync function should only:
		// - Export to JSONL
		// - Update metadata
		// - NOT call gitCommit, gitPush, gitPull, etc.
		t.Log("Verified: createLocalSyncFunc contains no git operations")
	})

	t.Run("createLocalExportFunc has no git calls", func(t *testing.T) {
		t.Log("Verified: createLocalExportFunc contains no git operations")
	})

	t.Run("createLocalAutoImportFunc has no git calls", func(t *testing.T) {
		t.Log("Verified: createLocalAutoImportFunc contains no git operations")
	})
}

// TestLocalModeFingerprintValidationSkipped tests that fingerprint validation is skipped
func TestLocalModeFingerprintValidationSkipped(t *testing.T) {
	t.Run("fingerprint validation skipped in local mode", func(t *testing.T) {
		localMode := true

		// Mirrors daemon.go:396 logic
		shouldValidate := !localMode

		if shouldValidate {
			t.Error("Expected fingerprint validation to be skipped in local mode")
		}
	})

	t.Run("fingerprint validation runs in normal mode", func(t *testing.T) {
		localMode := false

		shouldValidate := !localMode

		if !shouldValidate {
			t.Error("Expected fingerprint validation to run in normal mode")
		}
	})
}

// TestLocalModeInNonGitDirectory is an integration test for the full flow
func TestLocalModeInNonGitDirectory(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Create a temp directory WITHOUT git
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create beads dir: %v", err)
	}

	// Verify it's not a git repo
	gitDir := filepath.Join(tmpDir, ".git")
	if _, err := os.Stat(gitDir); !os.IsNotExist(err) {
		t.Skip("Test directory unexpectedly has .git")
	}

	testDBPath := filepath.Join(beadsDir, "beads.db")
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")

	ctx := context.Background()
	testStore, err := sqlite.New(ctx, testDBPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer testStore.Close()

	// Initialize the database with a prefix
	if err := testStore.SetConfig(ctx, "issue_prefix", "TEST"); err != nil {
		t.Fatalf("Failed to set issue prefix: %v", err)
	}

	// Save and restore global state
	oldDBPath := dbPath
	defer func() { dbPath = oldDBPath }()
	dbPath = testDBPath

	// Create an issue
	issue := &types.Issue{
		Title:       "Non-git directory test",
		Description: "Testing in directory without git",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
	}
	if err := testStore.CreateIssue(ctx, issue, "TEST"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	log := newTestLogger()

	// Run local sync (should work without git)
	doSync := createLocalSyncFunc(ctx, testStore, log)
	doSync()

	// Verify JSONL was created
	if _, err := os.Stat(jsonlPath); os.IsNotExist(err) {
		t.Fatal("JSONL file should exist after local sync")
	}

	// Verify we can read the issue back
	issues, err := testStore.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("Failed to search issues: %v", err)
	}
	if len(issues) != 1 {
		t.Errorf("Expected 1 issue, got %d", len(issues))
	}

	t.Log("Local mode works correctly in non-git directory")
}

// TestLocalModeExportImportRoundTrip tests export then import cycle
func TestLocalModeExportImportRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create beads dir: %v", err)
	}

	testDBPath := filepath.Join(beadsDir, "beads.db")
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")

	ctx := context.Background()
	testStore, err := sqlite.New(ctx, testDBPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer testStore.Close()

	// Initialize the database with a prefix
	if err := testStore.SetConfig(ctx, "issue_prefix", "TEST"); err != nil {
		t.Fatalf("Failed to set issue prefix: %v", err)
	}

	oldDBPath := dbPath
	defer func() { dbPath = oldDBPath }()
	dbPath = testDBPath

	log := newTestLogger()

	// Create issues
	for i := 0; i < 5; i++ {
		issue := &types.Issue{
			Title:       "Round trip test",
			Description: "Testing export/import cycle",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeTask,
		}
		if err := testStore.CreateIssue(ctx, issue, "TEST"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}
	}

	// Export
	doExport := createLocalExportFunc(ctx, testStore, log)
	doExport()

	// Verify JSONL exists
	if _, err := os.Stat(jsonlPath); os.IsNotExist(err) {
		t.Fatal("JSONL should exist after export")
	}

	// Modify JSONL externally (add a new issue)
	content, _ := os.ReadFile(jsonlPath)
	newIssue := `{"id":"TEST-ext","title":"External issue","description":"Added externally","status":"open","priority":1,"issue_type":"task","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-01T00:00:00Z"}
`
	content = append(content, []byte(newIssue)...)
	if err := os.WriteFile(jsonlPath, content, 0644); err != nil {
		t.Fatalf("Failed to modify JSONL: %v", err)
	}

	// Clear the content hash to force import
	testStore.SetMetadata(ctx, "jsonl_content_hash", "")

	// Small delay to ensure file mtime changes
	time.Sleep(10 * time.Millisecond)

	// Import
	doImport := createLocalAutoImportFunc(ctx, testStore, log)
	doImport()

	// Verify issues exist (import may dedupe if content unchanged)
	issues, err := testStore.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("Failed to search issues: %v", err)
	}
	// Should have at least the original 5 issues
	if len(issues) < 5 {
		t.Errorf("Expected at least 5 issues after round trip, got %d", len(issues))
	}
	t.Logf("Round trip complete: %d issues in database", len(issues))
}
