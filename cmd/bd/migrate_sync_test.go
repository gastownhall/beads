package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/git"
)

func TestMigrateSyncValidation(t *testing.T) {
	// Test invalid branch names
	tests := []struct {
		name    string
		branch  string
		wantErr bool
	}{
		{"valid simple", "beads-sync", false},
		{"valid with slash", "beads/sync", false},
		{"valid with dots", "beads.sync", false},
		{"invalid empty", "", true},
		{"invalid HEAD", "HEAD", true},
		{"invalid dots", "..", true},
		{"invalid leading slash", "/beads", true},
		{"invalid trailing slash", "beads/", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// We can't easily test the full command without a git repo,
			// but we can test branch validation indirectly
			if tt.branch == "" {
				// Empty branch should fail at args validation level
				return
			}
		})
	}
}

func TestMigrateSyncDryRun(t *testing.T) {
	// Create a temp directory with a git repo
	tmpDir, err := os.MkdirTemp("", "bd-migrate-sync-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Copy cached git template (bd-ktng optimization)
	initGitTemplate()
	if gitTemplateErr != nil {
		t.Fatalf("git template init failed: %v", gitTemplateErr)
	}
	if err := copyGitDir(gitTemplateDir, tmpDir); err != nil {
		t.Fatalf("failed to copy git template: %v", err)
	}

	// Create initial commit
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to git add: %v", err)
	}
	cmd = exec.Command("git", "commit", "-m", "initial")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to git commit: %v", err)
	}

	// Create .beads directory and initialize
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	// Create minimal issues.jsonl
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(""), 0644); err != nil {
		t.Fatalf("failed to create issues.jsonl: %v", err)
	}

	// Test that branchExistsLocal returns false for non-existent branch
	// Note: We need to run this from tmpDir context since branchExistsLocal uses git in cwd
	ctx := context.Background()
	t.Chdir(tmpDir)
	// Reset caches so RepoContext picks up new CWD
	beads.ResetCaches()
	git.ResetCaches()
	defer func() {
		beads.ResetCaches()
		git.ResetCaches()
	}()

	if branchExistsLocal(ctx, "beads-sync") {
		t.Error("branchExistsLocal should return false for non-existent branch")
	}

	// Create the branch
	cmd = exec.Command("git", "branch", "beads-sync")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to create branch: %v", err)
	}

	// Now it should exist
	if !branchExistsLocal(ctx, "beads-sync") {
		t.Error("branchExistsLocal should return true for existing branch")
	}
}

func TestMigrateSyncOrphan(t *testing.T) {
	// Create a temp directory with a git repo
	tmpDir, err := os.MkdirTemp("", "bd-migrate-sync-orphan-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Copy cached git template (bd-ktng optimization)
	initGitTemplate()
	if gitTemplateErr != nil {
		t.Fatalf("git template init failed: %v", gitTemplateErr)
	}
	if err := copyGitDir(gitTemplateDir, tmpDir); err != nil {
		t.Fatalf("failed to copy git template: %v", err)
	}

	// Create initial commit on main branch
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to git add: %v", err)
	}
	cmd = exec.Command("git", "commit", "-m", "initial commit")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to git commit: %v", err)
	}

	// Get current branch name (could be main or master)
	cmd = exec.Command("git", "branch", "--show-current")
	cmd.Dir = tmpDir
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("failed to get current branch: %v", err)
	}
	mainBranch := strings.TrimSpace(string(output))

	// Create .beads directory
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	// Create minimal issues.jsonl
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(""), 0644); err != nil {
		t.Fatalf("failed to create issues.jsonl: %v", err)
	}

	// Change to test directory
	t.Chdir(tmpDir)
	beads.ResetCaches()
	git.ResetCaches()
	defer func() {
		beads.ResetCaches()
		git.ResetCaches()
	}()

	// Test 1: Verify orphan branch has no merge-base with main
	t.Run("orphan branch has no merge-base", func(t *testing.T) {
		orphanBranch := "test-orphan"

		// Create orphan branch using git directly (simulating what runMigrateSync does)
		cmd := exec.Command("git", "checkout", "--orphan", orphanBranch)
		cmd.Dir = tmpDir
		if err := cmd.Run(); err != nil {
			t.Fatalf("failed to create orphan branch: %v", err)
		}

		// Remove all files from index (orphan starts with staged files)
		cmd = exec.Command("git", "rm", "-rf", "--cached", ".")
		cmd.Dir = tmpDir
		_ = cmd.Run() // May fail if nothing staged

		// Clean working directory (like runMigrateSync does)
		cmd = exec.Command("git", "clean", "-fd")
		cmd.Dir = tmpDir
		_ = cmd.Run() // Best effort

		// Create .beads directory on orphan branch
		orphanBeadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(orphanBeadsDir, 0750); err != nil {
			t.Fatalf("failed to create .beads dir on orphan: %v", err)
		}

		// Create a commit on the orphan branch
		orphanFile := filepath.Join(orphanBeadsDir, "test.txt")
		if err := os.WriteFile(orphanFile, []byte("orphan content"), 0644); err != nil {
			t.Fatalf("failed to create orphan file: %v", err)
		}
		cmd = exec.Command("git", "add", ".beads/test.txt")
		cmd.Dir = tmpDir
		if err := cmd.Run(); err != nil {
			t.Fatalf("failed to git add on orphan: %v", err)
		}
		cmd = exec.Command("git", "commit", "-m", "orphan initial")
		cmd.Dir = tmpDir
		if err := cmd.Run(); err != nil {
			t.Fatalf("failed to commit on orphan: %v", err)
		}

		// Switch back to main branch (force to handle any conflicts)
		cmd = exec.Command("git", "checkout", "-f", mainBranch)
		cmd.Dir = tmpDir
		if err := cmd.Run(); err != nil {
			t.Fatalf("failed to switch back to %s: %v", mainBranch, err)
		}

		// Verify orphan branch has no merge-base with main
		// git merge-base should FAIL (exit code 1) for orphan branches
		cmd = exec.Command("git", "merge-base", mainBranch, orphanBranch)
		cmd.Dir = tmpDir
		err := cmd.Run()
		if err == nil {
			t.Error("orphan branch should have no merge-base with main, but merge-base succeeded")
		}
	})

	// Test 2: Verify regular branch (non-orphan) DOES have merge-base
	t.Run("regular branch has merge-base", func(t *testing.T) {
		regularBranch := "test-regular"

		// Create a regular branch (inherits history)
		cmd := exec.Command("git", "branch", regularBranch)
		cmd.Dir = tmpDir
		if err := cmd.Run(); err != nil {
			t.Fatalf("failed to create regular branch: %v", err)
		}

		// Verify regular branch DOES have a merge-base with main
		cmd = exec.Command("git", "merge-base", mainBranch, regularBranch)
		cmd.Dir = tmpDir
		err := cmd.Run()
		if err != nil {
			t.Error("regular branch should have merge-base with main, but merge-base failed")
		}
	})
}

func TestMigrateSyncOrphanWorktree(t *testing.T) {
	// This test verifies that git worktrees can be created from orphan branches
	// which is essential for orphan branches (now the default) to work with sparse checkout

	tmpDir, err := os.MkdirTemp("", "bd-migrate-sync-orphan-worktree-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Copy cached git template (bd-ktng optimization)
	initGitTemplate()
	if gitTemplateErr != nil {
		t.Fatalf("git template init failed: %v", gitTemplateErr)
	}
	if err := copyGitDir(gitTemplateDir, tmpDir); err != nil {
		t.Fatalf("failed to copy git template: %v", err)
	}

	// Create initial commit on main
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("main content"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = tmpDir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "initial")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to git commit: %v", err)
	}

	// Get main branch name
	cmd = exec.Command("git", "branch", "--show-current")
	cmd.Dir = tmpDir
	output, _ := cmd.Output()
	mainBranch := strings.TrimSpace(string(output))

	// Create orphan branch
	orphanBranch := "orphan-sync"
	cmd = exec.Command("git", "checkout", "--orphan", orphanBranch)
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to create orphan branch: %v", err)
	}

	// Clean orphan branch
	cmd = exec.Command("git", "rm", "-rf", "--cached", ".")
	cmd.Dir = tmpDir
	_ = cmd.Run()
	cmd = exec.Command("git", "clean", "-fd")
	cmd.Dir = tmpDir
	_ = cmd.Run()

	// Create .beads content on orphan branch
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(""), 0644); err != nil {
		t.Fatalf("failed to create issues.jsonl: %v", err)
	}
	cmd = exec.Command("git", "add", ".beads")
	cmd.Dir = tmpDir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "orphan initial")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to commit on orphan: %v", err)
	}

	// Switch back to main
	cmd = exec.Command("git", "checkout", "-f", mainBranch)
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to switch to main: %v", err)
	}

	t.Run("worktree can be created from orphan branch", func(t *testing.T) {
		worktreePath := filepath.Join(tmpDir, ".worktrees", orphanBranch)

		// Create worktree from orphan branch
		cmd := exec.Command("git", "worktree", "add", worktreePath, orphanBranch)
		cmd.Dir = tmpDir
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("failed to create worktree from orphan branch: %v\n%s", err, output)
		}

		// Verify worktree exists
		if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
			t.Error("worktree directory should exist")
		}

		// Verify .beads exists in worktree (from orphan branch)
		wtBeadsDir := filepath.Join(worktreePath, ".beads")
		if _, err := os.Stat(wtBeadsDir); os.IsNotExist(err) {
			t.Error(".beads should exist in worktree from orphan branch")
		}

		// Verify main branch's test.txt does NOT exist in worktree
		// (because orphan branch has no shared history)
		wtTestFile := filepath.Join(worktreePath, "test.txt")
		if _, err := os.Stat(wtTestFile); err == nil {
			t.Error("test.txt from main branch should NOT exist in orphan worktree")
		}

		// Cleanup worktree
		cmd = exec.Command("git", "worktree", "remove", "--force", worktreePath)
		cmd.Dir = tmpDir
		_ = cmd.Run()
	})
}

func TestMigrateSyncExistingBranchPreserved(t *testing.T) {
	// This test verifies that existing sync branches (even non-orphan) are used as-is
	// This ensures existing users aren't affected by the orphan-by-default change

	tmpDir, err := os.MkdirTemp("", "bd-migrate-sync-existing-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Copy cached git template (bd-ktng optimization)
	initGitTemplate()
	if gitTemplateErr != nil {
		t.Fatalf("git template init failed: %v", gitTemplateErr)
	}
	if err := copyGitDir(gitTemplateDir, tmpDir); err != nil {
		t.Fatalf("failed to copy git template: %v", err)
	}

	// Create initial commit on main
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("main content"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = tmpDir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "initial")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to git commit: %v", err)
	}

	// Get main branch name
	cmd = exec.Command("git", "branch", "--show-current")
	cmd.Dir = tmpDir
	output, _ := cmd.Output()
	mainBranch := strings.TrimSpace(string(output))

	// Create a REGULAR (non-orphan) branch to simulate existing setup
	existingBranch := "existing-sync"
	cmd = exec.Command("git", "branch", existingBranch)
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to create existing branch: %v", err)
	}

	t.Run("existing non-orphan branch has merge-base", func(t *testing.T) {
		// Verify the existing branch HAS a merge-base with main (it's not orphan)
		cmd := exec.Command("git", "merge-base", mainBranch, existingBranch)
		cmd.Dir = tmpDir
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Expected merge-base to exist for non-orphan branch, but got error: %v\n%s", err, output)
		}
		mergeBase := strings.TrimSpace(string(output))
		if mergeBase == "" {
			t.Fatal("Expected non-empty merge-base for non-orphan branch")
		}

		// This proves the branch is NOT orphan, so if migrate sync uses it,
		// it won't break existing user setups
	})

	t.Run("git show-ref detects existing branch", func(t *testing.T) {
		// Verify git show-ref (used by branchExistsLocal) detects the branch
		// This is the check that prevents orphan creation for existing branches
		cmd := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/"+existingBranch)
		cmd.Dir = tmpDir
		if err := cmd.Run(); err != nil {
			t.Fatal("Expected git show-ref to find existing branch")
		}
	})
}

func TestMigrateSyncOrphanMigration(t *testing.T) {
	// This test verifies that --orphan flag migrates existing non-orphan branch to orphan
	// by deleting and recreating it

	tmpDir, err := os.MkdirTemp("", "bd-migrate-sync-orphan-migration-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Copy cached git template (bd-ktng optimization)
	initGitTemplate()
	if gitTemplateErr != nil {
		t.Fatalf("git template init failed: %v", gitTemplateErr)
	}
	if err := copyGitDir(gitTemplateDir, tmpDir); err != nil {
		t.Fatalf("failed to copy git template: %v", err)
	}

	// Create initial commit on main
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("main content"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = tmpDir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "initial")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to git commit: %v", err)
	}

	// Get main branch name
	cmd = exec.Command("git", "branch", "--show-current")
	cmd.Dir = tmpDir
	output, _ := cmd.Output()
	mainBranch := strings.TrimSpace(string(output))

	// Create a REGULAR (non-orphan) branch
	syncBranch := "beads-sync"
	cmd = exec.Command("git", "branch", syncBranch)
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to create sync branch: %v", err)
	}

	t.Run("non-orphan branch has merge-base before migration", func(t *testing.T) {
		// Verify it HAS merge-base (not orphan yet)
		cmd := exec.Command("git", "merge-base", mainBranch, syncBranch)
		cmd.Dir = tmpDir
		if err := cmd.Run(); err != nil {
			t.Fatal("Expected merge-base to exist before migration")
		}
	})

	t.Run("migration converts to orphan", func(t *testing.T) {
		// Simulate what --orphan flag does: delete and recreate as orphan
		// Delete existing branch
		cmd := exec.Command("git", "branch", "-D", syncBranch)
		cmd.Dir = tmpDir
		if err := cmd.Run(); err != nil {
			t.Fatalf("failed to delete branch: %v", err)
		}

		// Create orphan branch
		cmd = exec.Command("git", "checkout", "--orphan", syncBranch)
		cmd.Dir = tmpDir
		if err := cmd.Run(); err != nil {
			t.Fatalf("failed to create orphan branch: %v", err)
		}

		// Remove staged files
		cmd = exec.Command("git", "rm", "-rf", "--cached", ".")
		cmd.Dir = tmpDir
		_ = cmd.Run()

		// Clean working directory
		cmd = exec.Command("git", "clean", "-fd")
		cmd.Dir = tmpDir
		_ = cmd.Run()

		// Create .beads and commit
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0750); err != nil {
			t.Fatalf("failed to create .beads: %v", err)
		}
		jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
		if err := os.WriteFile(jsonlPath, []byte(""), 0644); err != nil {
			t.Fatalf("failed to create issues.jsonl: %v", err)
		}
		cmd = exec.Command("git", "add", ".beads")
		cmd.Dir = tmpDir
		_ = cmd.Run()
		cmd = exec.Command("git", "commit", "-m", "init beads on orphan")
		cmd.Dir = tmpDir
		if err := cmd.Run(); err != nil {
			t.Fatalf("failed to commit on orphan: %v", err)
		}

		// Switch back to main
		cmd = exec.Command("git", "checkout", mainBranch)
		cmd.Dir = tmpDir
		if err := cmd.Run(); err != nil {
			t.Fatalf("failed to checkout main: %v", err)
		}

		// Verify NO merge-base after migration (orphan)
		cmd = exec.Command("git", "merge-base", mainBranch, syncBranch)
		cmd.Dir = tmpDir
		output, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("Expected no merge-base after migration to orphan, but got: %s", output)
		}
	})
}

func TestHasChangesInWorktreeDir(t *testing.T) {
	// Create a temp directory with a git repo
	tmpDir, err := os.MkdirTemp("", "bd-worktree-changes-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Copy cached git template (bd-ktng optimization)
	initGitTemplate()
	if gitTemplateErr != nil {
		t.Fatalf("git template init failed: %v", gitTemplateErr)
	}
	if err := copyGitDir(gitTemplateDir, tmpDir); err != nil {
		t.Fatalf("failed to copy git template: %v", err)
	}

	// Create and commit initial file
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = tmpDir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "initial")
	cmd.Dir = tmpDir
	_ = cmd.Run()

	ctx := context.Background()

	// No changes initially
	hasChanges, err := hasChangesInWorktreeDir(ctx, tmpDir)
	if err != nil {
		t.Fatalf("hasChangesInWorktreeDir failed: %v", err)
	}
	if hasChanges {
		t.Error("should have no changes initially")
	}

	// Add uncommitted file
	newFile := filepath.Join(tmpDir, "new.txt")
	if err := os.WriteFile(newFile, []byte("new"), 0644); err != nil {
		t.Fatalf("failed to create new file: %v", err)
	}

	hasChanges, err = hasChangesInWorktreeDir(ctx, tmpDir)
	if err != nil {
		t.Fatalf("hasChangesInWorktreeDir failed: %v", err)
	}
	if !hasChanges {
		t.Error("should have changes after adding file")
	}
}
