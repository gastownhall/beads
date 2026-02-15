package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/git"
)

// setupGitRepoWithBeads creates a temporary git repository with a .beads directory.
// Returns the repo path and cleanup function.
func setupGitRepoWithBeads(t *testing.T) (repoPath string, cleanup func()) {
	t.Helper()

	tmpDir := newGitRepo(t)
	originalWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change to temp directory: %v", err)
	}

	git.ResetCaches()
	beads.ResetCaches()

	// Create .beads directory with issues.jsonl
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		_ = os.Chdir(originalWd)
		t.Fatalf("failed to create .beads directory: %v", err)
	}

	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(`{"id":"test-1"}`+"\n"), 0644); err != nil {
		_ = os.Chdir(originalWd)
		t.Fatalf("failed to write issues.jsonl: %v", err)
	}

	// Create initial commit
	_ = exec.Command("git", "add", ".beads").Run()
	if err := exec.Command("git", "commit", "-m", "initial").Run(); err != nil {
		_ = os.Chdir(originalWd)
		t.Fatalf("failed to create initial commit: %v", err)
	}

	cleanup = func() {
		_ = os.Chdir(originalWd)
		git.ResetCaches()
		beads.ResetCaches()
	}

	return tmpDir, cleanup
}

// setupRedirectedBeadsRepo creates two git repos: source with redirect, target with actual .beads.
// Returns source path, target path, and cleanup function.
func setupRedirectedBeadsRepo(t *testing.T) (sourcePath, targetPath string, cleanup func()) {
	t.Helper()

	baseDir := t.TempDir()
	originalWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	// Create target repo with actual .beads
	targetPath = filepath.Join(baseDir, "target")
	if err := os.MkdirAll(targetPath, 0755); err != nil {
		t.Fatalf("failed to create target directory: %v", err)
	}

	targetBeadsDir := filepath.Join(targetPath, ".beads")
	if err := os.MkdirAll(targetBeadsDir, 0755); err != nil {
		t.Fatalf("failed to create target .beads directory: %v", err)
	}

	// Copy cached git template into target (bd-ktng optimization)
	initGitTemplate()
	if gitTemplateErr != nil {
		t.Fatalf("git template init failed: %v", gitTemplateErr)
	}
	if err := copyGitDir(gitTemplateDir, targetPath); err != nil {
		t.Fatalf("failed to copy git template to target: %v", err)
	}

	// Write issues.jsonl in target
	jsonlPath := filepath.Join(targetBeadsDir, "issues.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(`{"id":"test-1"}`+"\n"), 0644); err != nil {
		t.Fatalf("failed to write issues.jsonl: %v", err)
	}

	// Commit in target
	cmd := exec.Command("git", "add", ".beads")
	cmd.Dir = targetPath
	_ = cmd.Run()

	cmd = exec.Command("git", "commit", "-m", "initial")
	cmd.Dir = targetPath
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to create initial commit in target: %v", err)
	}

	// Create source repo with redirect
	sourcePath = filepath.Join(baseDir, "source")
	if err := os.MkdirAll(sourcePath, 0755); err != nil {
		t.Fatalf("failed to create source directory: %v", err)
	}

	sourceBeadsDir := filepath.Join(sourcePath, ".beads")
	if err := os.MkdirAll(sourceBeadsDir, 0755); err != nil {
		t.Fatalf("failed to create source .beads directory: %v", err)
	}

	// Write redirect file pointing to target
	redirectPath := filepath.Join(sourceBeadsDir, "redirect")
	// Use relative path: ../target/.beads
	if err := os.WriteFile(redirectPath, []byte("../target/.beads\n"), 0644); err != nil {
		t.Fatalf("failed to write redirect file: %v", err)
	}

	// Copy cached git template into source (bd-ktng optimization)
	if err := copyGitDir(gitTemplateDir, sourcePath); err != nil {
		t.Fatalf("failed to copy git template to source: %v", err)
	}

	// Commit redirect in source
	cmd = exec.Command("git", "add", ".beads")
	cmd.Dir = sourcePath
	_ = cmd.Run()

	cmd = exec.Command("git", "commit", "-m", "initial with redirect")
	cmd.Dir = sourcePath
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to create initial commit in source: %v", err)
	}

	// Change to source directory
	if err := os.Chdir(sourcePath); err != nil {
		t.Fatalf("failed to change to source directory: %v", err)
	}
	git.ResetCaches()
	beads.ResetCaches()

	cleanup = func() {
		_ = os.Chdir(originalWd)
		git.ResetCaches()
		beads.ResetCaches()
	}

	return sourcePath, targetPath, cleanup
}

func TestGitHasBeadsChanges_NoChanges(t *testing.T) {
	ctx := context.Background()
	_, cleanup := setupGitRepoWithBeads(t)
	defer cleanup()

	hasChanges, err := gitHasBeadsChanges(ctx)
	if err != nil {
		t.Fatalf("gitHasBeadsChanges() error = %v", err)
	}
	if hasChanges {
		t.Error("expected no changes for clean repo")
	}
}

func TestGitHasBeadsChanges_WithChanges(t *testing.T) {
	ctx := context.Background()
	repoPath, cleanup := setupGitRepoWithBeads(t)
	defer cleanup()

	// Modify the issues.jsonl file
	jsonlPath := filepath.Join(repoPath, ".beads", "issues.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(`{"id":"test-2"}`+"\n"), 0644); err != nil {
		t.Fatalf("failed to modify issues.jsonl: %v", err)
	}

	hasChanges, err := gitHasBeadsChanges(ctx)
	if err != nil {
		t.Fatalf("gitHasBeadsChanges() error = %v", err)
	}
	if !hasChanges {
		t.Error("expected changes for modified file")
	}
}

func TestGitHasBeadsChanges_WithRedirect_NoChanges(t *testing.T) {
	ctx := context.Background()
	sourcePath, _, cleanup := setupRedirectedBeadsRepo(t)
	defer cleanup()

	// Set BEADS_DIR to point to source's .beads (which has the redirect)
	oldBeadsDir := os.Getenv("BEADS_DIR")
	os.Setenv("BEADS_DIR", filepath.Join(sourcePath, ".beads"))
	defer os.Setenv("BEADS_DIR", oldBeadsDir)

	hasChanges, err := gitHasBeadsChanges(ctx)
	if err != nil {
		t.Fatalf("gitHasBeadsChanges() error = %v", err)
	}
	if hasChanges {
		t.Error("expected no changes for clean redirected repo")
	}
}

func TestGitHasBeadsChanges_WithRedirect_WithChanges(t *testing.T) {
	ctx := context.Background()
	sourcePath, targetPath, cleanup := setupRedirectedBeadsRepo(t)
	defer cleanup()

	// Set BEADS_DIR to point to source's .beads (which has the redirect)
	oldBeadsDir := os.Getenv("BEADS_DIR")
	os.Setenv("BEADS_DIR", filepath.Join(sourcePath, ".beads"))
	defer os.Setenv("BEADS_DIR", oldBeadsDir)

	// Modify the issues.jsonl file in target (where actual beads is)
	jsonlPath := filepath.Join(targetPath, ".beads", "issues.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(`{"id":"test-2"}`+"\n"), 0644); err != nil {
		t.Fatalf("failed to modify issues.jsonl: %v", err)
	}

	hasChanges, err := gitHasBeadsChanges(ctx)
	if err != nil {
		t.Fatalf("gitHasBeadsChanges() error = %v", err)
	}
	if !hasChanges {
		t.Error("expected changes for modified file in redirected repo")
	}
}

func TestGitHasUncommittedBeadsChanges_NoChanges(t *testing.T) {
	ctx := context.Background()
	_, cleanup := setupGitRepoWithBeads(t)
	defer cleanup()

	hasChanges, err := gitHasUncommittedBeadsChanges(ctx)
	if err != nil {
		t.Fatalf("gitHasUncommittedBeadsChanges() error = %v", err)
	}
	if hasChanges {
		t.Error("expected no changes for clean repo")
	}
}

func TestGitHasUncommittedBeadsChanges_WithChanges(t *testing.T) {
	ctx := context.Background()
	repoPath, cleanup := setupGitRepoWithBeads(t)
	defer cleanup()

	// Modify the issues.jsonl file
	jsonlPath := filepath.Join(repoPath, ".beads", "issues.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(`{"id":"test-2"}`+"\n"), 0644); err != nil {
		t.Fatalf("failed to modify issues.jsonl: %v", err)
	}

	hasChanges, err := gitHasUncommittedBeadsChanges(ctx)
	if err != nil {
		t.Fatalf("gitHasUncommittedBeadsChanges() error = %v", err)
	}
	if !hasChanges {
		t.Error("expected changes for modified file")
	}
}

func TestGitHasUncommittedBeadsChanges_WithRedirect_NoChanges(t *testing.T) {
	ctx := context.Background()
	sourcePath, _, cleanup := setupRedirectedBeadsRepo(t)
	defer cleanup()

	// Set BEADS_DIR to point to source's .beads (which has the redirect)
	oldBeadsDir := os.Getenv("BEADS_DIR")
	os.Setenv("BEADS_DIR", filepath.Join(sourcePath, ".beads"))
	defer os.Setenv("BEADS_DIR", oldBeadsDir)

	hasChanges, err := gitHasUncommittedBeadsChanges(ctx)
	if err != nil {
		t.Fatalf("gitHasUncommittedBeadsChanges() error = %v", err)
	}
	if hasChanges {
		t.Error("expected no changes for clean redirected repo")
	}
}

func TestGitHasUncommittedBeadsChanges_WithRedirect_WithChanges(t *testing.T) {
	ctx := context.Background()
	sourcePath, targetPath, cleanup := setupRedirectedBeadsRepo(t)
	defer cleanup()

	// Set BEADS_DIR to point to source's .beads (which has the redirect)
	oldBeadsDir := os.Getenv("BEADS_DIR")
	os.Setenv("BEADS_DIR", filepath.Join(sourcePath, ".beads"))
	defer os.Setenv("BEADS_DIR", oldBeadsDir)

	// Modify the issues.jsonl file in target (where actual beads is)
	jsonlPath := filepath.Join(targetPath, ".beads", "issues.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(`{"id":"test-2"}`+"\n"), 0644); err != nil {
		t.Fatalf("failed to modify issues.jsonl: %v", err)
	}

	hasChanges, err := gitHasUncommittedBeadsChanges(ctx)
	if err != nil {
		t.Fatalf("gitHasUncommittedBeadsChanges() error = %v", err)
	}
	if !hasChanges {
		t.Error("expected changes for modified file in redirected repo")
	}
}

func TestParseGitStatusForBeadsChanges(t *testing.T) {
	tests := []struct {
		name     string
		status   string
		expected bool
	}{
		// No changes
		{
			name:     "empty status",
			status:   "",
			expected: false,
		},
		{
			name:     "whitespace only",
			status:   "   \n",
			expected: false,
		},

		// Modified (should return true)
		{
			name:     "staged modified",
			status:   "M  .beads/issues.jsonl",
			expected: true,
		},
		{
			name:     "unstaged modified",
			status:   " M .beads/issues.jsonl",
			expected: true,
		},
		{
			name:     "staged and unstaged modified",
			status:   "MM .beads/issues.jsonl",
			expected: true,
		},

		// Added (should return true)
		{
			name:     "staged added",
			status:   "A  .beads/issues.jsonl",
			expected: true,
		},
		{
			name:     "added then modified",
			status:   "AM .beads/issues.jsonl",
			expected: true,
		},

		// Untracked (should return false)
		{
			name:     "untracked file",
			status:   "?? .beads/issues.jsonl",
			expected: false,
		},

		// Deleted (should return false)
		{
			name:     "staged deleted",
			status:   "D  .beads/issues.jsonl",
			expected: false,
		},
		{
			name:     "unstaged deleted",
			status:   " D .beads/issues.jsonl",
			expected: false,
		},

		// Edge cases
		{
			name:     "renamed file",
			status:   "R  old.jsonl -> .beads/issues.jsonl",
			expected: false,
		},
		{
			name:     "copied file",
			status:   "C  source.jsonl -> .beads/issues.jsonl",
			expected: false,
		},
		{
			name:     "status too short",
			status:   "M",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseGitStatusForBeadsChanges(tt.status)
			if result != tt.expected {
				t.Errorf("parseGitStatusForBeadsChanges(%q) = %v, want %v",
					tt.status, result, tt.expected)
			}
		})
	}
}

// TestGitBranchHasUpstream tests the gitBranchHasUpstream function
// which checks if a specific branch (not current HEAD) has upstream configured.
// This is critical for jj/jujutsu compatibility where HEAD is always detached
// but the sync-branch may have proper upstream tracking.
func TestGitBranchHasUpstream(t *testing.T) {
	// Create temp directory for test repos
	tmpDir, err := os.MkdirTemp("", "beads-upstream-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a bare "remote" repo
	remoteDir := filepath.Join(tmpDir, "remote.git")
	if err := exec.Command("git", "init", "--bare", remoteDir).Run(); err != nil {
		t.Fatalf("Failed to create bare repo: %v", err)
	}

	// Create local repo using cached git template (bd-ktng optimization)
	localDir := filepath.Join(tmpDir, "local")
	if err := os.MkdirAll(localDir, 0755); err != nil {
		t.Fatalf("Failed to create local dir: %v", err)
	}
	initGitTemplate()
	if gitTemplateErr != nil {
		t.Fatalf("git template init failed: %v", gitTemplateErr)
	}
	if err := copyGitDir(gitTemplateDir, localDir); err != nil {
		t.Fatalf("failed to copy git template: %v", err)
	}
	cmds := [][]string{
		{"git", "remote", "add", "origin", remoteDir},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = localDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("Failed to run %v: %v\n%s", args, err, out)
		}
	}

	// Create .beads directory (required for RepoContext)
	beadsDir := filepath.Join(localDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte{}, 0644); err != nil {
		t.Fatalf("Failed to write issues.jsonl: %v", err)
	}

	// Create initial commit on main
	testFile := filepath.Join(localDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}
	cmds = [][]string{
		{"git", "add", "."},
		{"git", "commit", "-m", "initial"},
		{"git", "push", "-u", "origin", "main"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = localDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("Failed to run %v: %v\n%s", args, err, out)
		}
	}

	// Create beads-sync branch with upstream
	cmds = [][]string{
		{"git", "checkout", "-b", "beads-sync"},
		{"git", "push", "-u", "origin", "beads-sync"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = localDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("Failed to run %v: %v\n%s", args, err, out)
		}
	}

	// Save current dir and change to local repo
	origDir, _ := os.Getwd()
	if err := os.Chdir(localDir); err != nil {
		t.Fatalf("Failed to chdir: %v", err)
	}
	// Reset caches after changing directory so RepoContext uses this repo
	git.ResetCaches()
	beads.ResetCaches()
	defer func() {
		os.Chdir(origDir)
		git.ResetCaches()
		beads.ResetCaches()
	}()

	// Test 1: beads-sync branch should have upstream
	t.Run("branch with upstream returns true", func(t *testing.T) {
		if !gitBranchHasUpstream("beads-sync") {
			t.Error("gitBranchHasUpstream('beads-sync') = false, want true")
		}
	})

	// Test 2: non-existent branch should return false
	t.Run("non-existent branch returns false", func(t *testing.T) {
		if gitBranchHasUpstream("no-such-branch") {
			t.Error("gitBranchHasUpstream('no-such-branch') = true, want false")
		}
	})

	// Test 3: Simulate jj detached HEAD - beads-sync should still work
	t.Run("works with detached HEAD (jj scenario)", func(t *testing.T) {
		// Detach HEAD (simulating jj's behavior)
		cmd := exec.Command("git", "checkout", "--detach", "HEAD")
		cmd.Dir = localDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("Failed to detach HEAD: %v\n%s", err, out)
		}

		// gitHasUpstream() should fail (detached HEAD)
		if gitHasUpstream() {
			t.Error("gitHasUpstream() = true with detached HEAD, want false")
		}

		// But gitBranchHasUpstream("beads-sync") should still work
		if !gitBranchHasUpstream("beads-sync") {
			t.Error("gitBranchHasUpstream('beads-sync') = false with detached HEAD, want true")
		}
	})

	// Test 4: branch without upstream should return false
	t.Run("branch without upstream returns false", func(t *testing.T) {
		// Create a local-only branch (no upstream)
		cmd := exec.Command("git", "checkout", "-b", "local-only")
		cmd.Dir = localDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("Failed to create local branch: %v\n%s", err, out)
		}

		if gitBranchHasUpstream("local-only") {
			t.Error("gitBranchHasUpstream('local-only') = true, want false (no upstream)")
		}
	})
}

// TestGetCurrentBranchOrHEAD tests getCurrentBranchOrHEAD which returns "HEAD"
// when in detached HEAD state (e.g., jj/jujutsu) instead of failing.
// TestConfigPreservedDuringSync is a regression test for GH#1100.
// It verifies that config.yaml in .beads/ is not overwritten during sync operations.
// The bug was caused by restoreBeadsDirFromBranch() which ran:
//
//	git checkout HEAD -- .beads/
//
// This restored the ENTIRE .beads/ directory, including user's uncommitted config.yaml.
// The function was removed in PR #918 (pull-first refactor).
// This test ensures similar restoration logic is never reintroduced.
func TestConfigPreservedDuringSync(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	t.Run("uncommitted config.yaml not overwritten by git operations", func(t *testing.T) {
		// Setup: Create git repo with .beads directory
		repoPath, cleanup := setupGitRepoWithBeads(t)
		defer cleanup()

		beadsDir := filepath.Join(repoPath, ".beads")

		// Create config.yaml and commit it
		configPath := filepath.Join(beadsDir, "config.yaml")
		originalContent := "sync:\n  branch: beads-sync\n"
		if err := os.WriteFile(configPath, []byte(originalContent), 0644); err != nil {
			t.Fatalf("failed to write config.yaml: %v", err)
		}

		// Commit the config
		_ = exec.Command("git", "add", ".beads/config.yaml").Run()
		if err := exec.Command("git", "commit", "-m", "add config").Run(); err != nil {
			t.Fatalf("failed to commit config: %v", err)
		}

		// Now modify config.yaml with UNCOMMITTED changes (the bug scenario)
		modifiedContent := originalContent + "# User's uncommitted customization\ntest-marker: preserved\n"
		if err := os.WriteFile(configPath, []byte(modifiedContent), 0644); err != nil {
			t.Fatalf("failed to modify config.yaml: %v", err)
		}

		// Verify the modification exists before any operations
		beforeSync, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("failed to read config.yaml: %v", err)
		}
		if !strings.Contains(string(beforeSync), "test-marker: preserved") {
			t.Fatal("expected test-marker in config before operations")
		}

		// Simulate what the old buggy code did: git checkout HEAD -- .beads/
		// This should NOT happen during normal sync, but we test that IF it did,
		// it would restore committed state (losing uncommitted changes).
		// The fact that no code calls this anymore is the fix.

		// Verify config.yaml still has uncommitted changes
		// (This passes because nothing calls restoreBeadsDirFromBranch anymore)
		afterContent, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("failed to read config.yaml after operations: %v", err)
		}

		if !strings.Contains(string(afterContent), "test-marker: preserved") {
			t.Errorf("REGRESSION: uncommitted config.yaml changes were lost!\n"+
				"Before: %s\nAfter: %s", beforeSync, afterContent)
		}

		// Also verify that git status shows the file as modified (uncommitted)
		cmd := exec.Command("git", "status", "--porcelain", ".beads/config.yaml")
		output, err := cmd.Output()
		if err != nil {
			t.Fatalf("git status failed: %v", err)
		}

		// Should show " M" (modified, unstaged)
		if !strings.Contains(string(output), "M") {
			t.Errorf("expected config.yaml to show as modified in git status, got: %q", output)
		}
	})

	t.Run("restoreBeadsDirFromBranch function does not exist", func(t *testing.T) {
		// This test documents that the function was intentionally removed.
		// If someone adds it back, they should also update this test with justification.

		// Read the sync_git.go source file
		syncGitPath := filepath.Join(getProjectRoot(t), "cmd", "bd", "sync_git.go")
		content, err := os.ReadFile(syncGitPath)
		if err != nil {
			t.Fatalf("failed to read sync_git.go: %v", err)
		}

		// The function should NOT exist
		if strings.Contains(string(content), "func restoreBeadsDirFromBranch") {
			t.Error("REGRESSION: restoreBeadsDirFromBranch function was reintroduced!\n" +
				"This function caused GH#1100 by restoring entire .beads/ directory.\n" +
				"If you need this functionality, use selective restoration that excludes config.yaml.")
		}
	})
}

// getProjectRoot returns the project root directory for test file access.
// Uses runtime.Caller to find the source file location, which is immune to
// os.Chdir side effects from other subtests.
func getProjectRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("failed to get current file path via runtime.Caller")
	}
	// thisFile is cmd/bd/sync_git_test.go; walk up to find go.mod
	dir := filepath.Dir(thisFile)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find project root (go.mod)")
		}
		dir = parent
	}
}

func TestGetCurrentBranchOrHEAD(t *testing.T) {
	ctx := context.Background()

	// Create temp directory for test repo
	tmpDir, err := os.MkdirTemp("", "beads-branch-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize git repo
	cmds := [][]string{
		{"git", "init", "-b", "main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = tmpDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("Failed to run %v: %v\n%s", args, err, out)
		}
	}

	// Create initial commit
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}
	cmds = [][]string{
		{"git", "add", "test.txt"},
		{"git", "commit", "-m", "initial"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = tmpDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("Failed to run %v: %v\n%s", args, err, out)
		}
	}

	// Save current dir and change to test repo
	origDir, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Failed to chdir: %v", err)
	}
	defer os.Chdir(origDir)

	// Test 1: Normal branch returns branch name
	t.Run("returns branch name when on branch", func(t *testing.T) {
		branch := getCurrentBranchOrHEAD(ctx)
		if branch != "main" {
			t.Errorf("getCurrentBranchOrHEAD() = %q, want %q", branch, "main")
		}
	})

	// Test 2: Detached HEAD returns "HEAD"
	t.Run("returns HEAD when detached", func(t *testing.T) {
		// Detach HEAD
		cmd := exec.Command("git", "checkout", "--detach", "HEAD")
		cmd.Dir = tmpDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("Failed to detach HEAD: %v\n%s", err, out)
		}

		branch := getCurrentBranchOrHEAD(ctx)
		if branch != "HEAD" {
			t.Errorf("getCurrentBranchOrHEAD() = %q, want %q", branch, "HEAD")
		}
	})
}
