package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

// TestDatabaseReinitialization tests all database reinitialization scenarios
// covered in DATABASE_REINIT_BUG.md
func TestDatabaseReinitialization(t *testing.T) {
	// Skip on Windows due to git hook autoimport flakiness
	if runtime.GOOS == "windows" {
		t.Skip("Skipping on Windows: git hook autoimport is flaky in CI")
	}

	// Skip in Nix build environment where git isn't available
	if os.Getenv("NIX_BUILD_TOP") != "" {
		t.Skip("Skipping test in Nix build environment (git not available)")
	}

	// Check if git is available
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available in PATH, skipping test")
	}

	t.Run("fresh_clone_auto_import", testFreshCloneAutoImport)
	t.Run("database_removal_scenario", testDatabaseRemovalScenario)
	t.Run("legacy_filename_support", testLegacyFilenameSupport)
	t.Run("precedence_test", testPrecedenceTest)
	t.Run("init_safety_check", testInitSafetyCheck)
}

// testFreshCloneAutoImport verifies auto-import works on fresh clone
func testFreshCloneAutoImport(t *testing.T) {
	dir := t.TempDir()

	// Initialize git repo
	runCmd(t, dir, "git", "init")
	runCmd(t, dir, "git", "config", "user.email", "test@example.com")
	runCmd(t, dir, "git", "config", "user.name", "Test User")

	// Create .beads directory with issues.jsonl (canonical name)
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	// Create test issue data
	issue := &types.Issue{
		ID:          "test-1",
		Title:       "Test issue",
		Description: "Test description",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
	}

	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := writeJSONL(jsonlPath, []*types.Issue{issue}); err != nil {
		t.Fatalf("Failed to write JSONL: %v", err)
	}

	// Commit to git (use forward slashes for git path)
	runCmd(t, dir, "git", "add", ".beads/issues.jsonl")
	runCmd(t, dir, "git", "commit", "-m", "Initial commit")

	// Remove database to simulate fresh clone
	dbPath := filepath.Join(beadsDir, "test.db")
	os.Remove(dbPath)

	// Run bd init with auto-import disabled to test checkGitForIssues
	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("Failed to create database: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	// Test checkGitForIssues detects issues.jsonl
	t.Chdir(dir)

	git.ResetCaches() // Reset git caches after changing directory

	git.ResetCaches()


	count, path, gitRef := checkGitForIssues()
	if count != 1 {
		t.Errorf("Expected 1 issue in git, got %d", count)
	}
	// Normalize path for comparison (handle both forward and backslash)
	expectedPath := normalizeGitPath(".beads/issues.jsonl")
	if normalizeGitPath(path) != expectedPath {
		t.Errorf("Expected path %s, got %s", expectedPath, path)
	}

	// Import from git
	if err := importFromGit(ctx, dbPath, store, path, gitRef); err != nil {
		t.Fatalf("Import failed: %v", err)
	}

	// Verify issue was imported
	stats, err := store.GetStatistics(ctx)
	if err != nil {
		t.Fatalf("Failed to get stats: %v", err)
	}
	if stats.TotalIssues != 1 {
		t.Errorf("Expected 1 issue after import, got %d", stats.TotalIssues)
	}
}

// testDatabaseRemovalScenario tests the primary bug scenario
func testDatabaseRemovalScenario(t *testing.T) {
	dir := t.TempDir()

	// Initialize git repo
	runCmd(t, dir, "git", "init")
	runCmd(t, dir, "git", "config", "user.email", "test@example.com")
	runCmd(t, dir, "git", "config", "user.name", "Test User")

	// Create .beads directory with issues.jsonl (canonical name)
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	// Create multiple test issues
	issues := []*types.Issue{
		{
			ID:        "test-1",
			Title:     "First issue",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
		},
		{
			ID:        "test-2",
			Title:     "Second issue",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeBug,
		},
	}

	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := writeJSONL(jsonlPath, issues); err != nil {
		t.Fatalf("Failed to write JSONL: %v", err)
	}

	// Commit to git
	runCmd(t, dir, "git", "add", ".beads/issues.jsonl")
	runCmd(t, dir, "git", "commit", "-m", "Add issues")

	// Simulate rm -rf .beads/ followed by partial bd init
	// (in practice, bd init creates config.yaml before auto-import)
	os.RemoveAll(beadsDir)
	os.MkdirAll(beadsDir, 0755)
	// Create minimal config so FindBeadsDir recognizes this as a beads directory
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("issue-prefix: test\n"), 0644); err != nil {
		t.Fatalf("Failed to write config.yaml: %v", err)
	}

	// Change to test directory
	t.Chdir(dir)

	git.ResetCaches() // Reset git caches after changing directory

	git.ResetCaches()


	// Test checkGitForIssues finds issues.jsonl (canonical name)
	count, path, gitRef := checkGitForIssues()
	if count != 2 {
		t.Errorf("Expected 2 issues in git, got %d", count)
	}
	expectedPath := normalizeGitPath(".beads/issues.jsonl")
	if normalizeGitPath(path) != expectedPath {
		t.Errorf("Expected %s, got %s", expectedPath, path)
	}

	// Initialize database and import
	dbPath := filepath.Join(beadsDir, "test.db")
	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("Failed to create database: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	if err := importFromGit(ctx, dbPath, store, path, gitRef); err != nil {
		t.Fatalf("Import failed: %v", err)
	}

	// Verify correct filename was detected
	if filepath.Base(path) != "issues.jsonl" {
		t.Errorf("Should have imported from issues.jsonl, got %s", path)
	}

	// Verify stats show >0 issues
	stats, err := store.GetStatistics(ctx)
	if err != nil {
		t.Fatalf("Failed to get stats: %v", err)
	}
	if stats.TotalIssues != 2 {
		t.Errorf("Expected 2 issues, got %d", stats.TotalIssues)
	}
}

// testLegacyFilenameSupport tests issues.jsonl fallback
func testLegacyFilenameSupport(t *testing.T) {
	dir := t.TempDir()

	// Initialize git repo
	runCmd(t, dir, "git", "init")
	runCmd(t, dir, "git", "config", "user.email", "test@example.com")
	runCmd(t, dir, "git", "config", "user.name", "Test User")

	// Create .beads directory with issues.jsonl (legacy)
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	issue := &types.Issue{
		ID:        "test-1",
		Title:     "Legacy issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}

	// Use legacy filename
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := writeJSONL(jsonlPath, []*types.Issue{issue}); err != nil {
		t.Fatalf("Failed to write JSONL: %v", err)
	}

	// Commit to git
	runCmd(t, dir, "git", "add", ".beads/issues.jsonl")
	runCmd(t, dir, "git", "commit", "-m", "Add legacy issue")

	// Change to test directory
	t.Chdir(dir)

	git.ResetCaches() // Reset git caches after changing directory

	git.ResetCaches()


	// Test checkGitForIssues finds issues.jsonl
	count, path, gitRef := checkGitForIssues()
	if count != 1 {
		t.Errorf("Expected 1 issue in git, got %d", count)
	}
	expectedPath := normalizeGitPath(".beads/issues.jsonl")
	if normalizeGitPath(path) != expectedPath {
		t.Errorf("Expected %s, got %s", expectedPath, path)
	}

	// Initialize and import
	dbPath := filepath.Join(beadsDir, "test.db")
	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("Failed to create database: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	if err := importFromGit(ctx, dbPath, store, path, gitRef); err != nil {
		t.Fatalf("Import failed: %v", err)
	}

	// Verify import succeeded
	stats, err := store.GetStatistics(ctx)
	if err != nil {
		t.Fatalf("Failed to get stats: %v", err)
	}
	if stats.TotalIssues != 1 {
		t.Errorf("Expected 1 issue, got %d", stats.TotalIssues)
	}
}

// testPrecedenceTest verifies issues.jsonl is preferred over beads.jsonl
func testPrecedenceTest(t *testing.T) {
	dir := t.TempDir()

	// Initialize git repo
	runCmd(t, dir, "git", "init")
	runCmd(t, dir, "git", "config", "user.email", "test@example.com")
	runCmd(t, dir, "git", "config", "user.name", "Test User")

	// Create .beads directory with BOTH files
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	// Create issues.jsonl with 2 issues (canonical, should be preferred)
	canonicalIssues := []*types.Issue{
		{ID: "test-1", Title: "From issues.jsonl", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
		{ID: "test-2", Title: "Also from issues.jsonl", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
	}
	if err := writeJSONL(filepath.Join(beadsDir, "issues.jsonl"), canonicalIssues); err != nil {
		t.Fatalf("Failed to write issues.jsonl: %v", err)
	}

	// Create beads.jsonl with 1 issue (should be ignored)
	legacyIssues := []*types.Issue{
		{ID: "test-99", Title: "From beads.jsonl", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
	}
	if err := writeJSONL(filepath.Join(beadsDir, "beads.jsonl"), legacyIssues); err != nil {
		t.Fatalf("Failed to write beads.jsonl: %v", err)
	}

	// Commit both files
	runCmd(t, dir, "git", "add", ".beads/")
	runCmd(t, dir, "git", "commit", "-m", "Add both files")

	// Change to test directory
	t.Chdir(dir)

	git.ResetCaches() // Reset git caches after changing directory

	git.ResetCaches()


	// Test checkGitForIssues prefers issues.jsonl
	count, path, _ := checkGitForIssues()
	if count != 2 {
		t.Errorf("Expected 2 issues (from issues.jsonl), got %d", count)
	}
	expectedPath := normalizeGitPath(".beads/issues.jsonl")
	if normalizeGitPath(path) != expectedPath {
		t.Errorf("Expected issues.jsonl to be preferred, got %s", path)
	}
}

// testInitSafetyCheck tests the safety check that prevents silent data loss
func testInitSafetyCheck(t *testing.T) {
	dir := t.TempDir()

	// Initialize git repo
	runCmd(t, dir, "git", "init")
	runCmd(t, dir, "git", "config", "user.email", "test@example.com")
	runCmd(t, dir, "git", "config", "user.name", "Test User")

	// Create .beads directory with issues.jsonl (canonical name)
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	issue := &types.Issue{
		ID:        "test-1",
		Title:     "Test issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}

	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := writeJSONL(jsonlPath, []*types.Issue{issue}); err != nil {
		t.Fatalf("Failed to write JSONL: %v", err)
	}

	// Commit to git
	runCmd(t, dir, "git", "add", ".beads/issues.jsonl")
	runCmd(t, dir, "git", "commit", "-m", "Add issue")

	// Change to test directory
	t.Chdir(dir)

	git.ResetCaches() // Reset git caches after changing directory

	git.ResetCaches()


	// Create empty database (simulating failed import)
	dbPath := filepath.Join(beadsDir, "test.db")
	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("Failed to create database: %v", err)
	}
	ctx := context.Background()
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	// Verify safety check would detect the problem
	stats, err := store.GetStatistics(ctx)
	if err != nil {
		t.Fatalf("Failed to get stats: %v", err)
	}

	if stats.TotalIssues == 0 {
		// Database is empty - check if git has issues
		recheck, recheckPath, _ := checkGitForIssues()
		if recheck == 0 {
			t.Error("Safety check should have detected issues in git")
		}
		expectedPath := normalizeGitPath(".beads/issues.jsonl")
		if normalizeGitPath(recheckPath) != expectedPath {
			t.Errorf("Safety check found wrong path: %s", recheckPath)
		}
		// This would trigger the error exit in real init.go
		t.Logf("Safety check correctly detected %d issues in git at %s", recheck, recheckPath)
	} else {
		t.Error("Database should be empty for this test")
	}

	store.Close()
}

// Helper functions

// runCmd runs a command and fails the test if it returns an error
// If the command is "git init", it automatically adds --initial-branch=main
// for modern git compatibility.
func runCmd(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	// Add --initial-branch=main to git init for modern git compatibility
	if name == "git" && len(args) > 0 && args[0] == "init" {
		args = append(args, "--initial-branch=main")
	}
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Command %s %v failed: %v\nOutput: %s", name, args, err, output)
	}
}

// writeJSONL writes issues to a JSONL file
func writeJSONL(path string, issues []*types.Issue) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	for _, issue := range issues {
		if err := enc.Encode(issue); err != nil {
			return err
		}
	}
	return nil
}

// normalizeGitPath converts a path to use forward slashes for git compatibility
// Git always uses forward slashes internally, even on Windows
func normalizeGitPath(path string) string {
	if runtime.GOOS == windowsOS {
		return filepath.ToSlash(path)
	}
	return path
}
