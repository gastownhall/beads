package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// setupTestRepo creates a temporary git repository for testing
func setupTestRepo(t *testing.T) (repoPath string, cleanup func()) {
	t.Helper()

	tmpDir := t.TempDir()
	repoPath = filepath.Join(tmpDir, "test-repo")

	// Create repo directory
	if err := os.MkdirAll(repoPath, 0750); err != nil {
		t.Fatalf("Failed to create test repo directory: %v", err)
	}

	// Initialize git repo
	cmd := exec.Command("git", "init")
	cmd.Dir = repoPath
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to init git repo: %v\nOutput: %s", err, string(output))
	}

	// Configure git user for commits
	cmd = exec.Command("git", "config", "user.email", "test@example.com")
	cmd.Dir = repoPath
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to set git user.email: %v", err)
	}

	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = repoPath
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to set git user.name: %v", err)
	}

	// Create .beads directory and a test file
	beadsDir := filepath.Join(repoPath, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("Failed to create .beads directory: %v", err)
	}

	testFile := filepath.Join(beadsDir, "test.jsonl")
	if err := os.WriteFile(testFile, []byte("test data\n"), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	// Create a file outside .beads to test sparse checkout
	otherFile := filepath.Join(repoPath, "other.txt")
	if err := os.WriteFile(otherFile, []byte("other data\n"), 0644); err != nil {
		t.Fatalf("Failed to write other file: %v", err)
	}

	// Initial commit
	cmd = exec.Command("git", "add", ".")
	cmd.Dir = repoPath
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to git add: %v", err)
	}

	cmd = exec.Command("git", "commit", "-m", "Initial commit")
	cmd.Dir = repoPath
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to commit: %v\nOutput: %s", err, string(output))
	}

	cleanup = func() {
		// Cleanup is handled by t.TempDir()
	}

	return repoPath, cleanup
}

func TestCreateBeadsWorktree(t *testing.T) {
	repoPath, cleanup := setupTestRepo(t)
	defer cleanup()

	wm := NewWorktreeManager(repoPath)
	worktreePath := filepath.Join(t.TempDir(), "beads-worktree")

	t.Run("creates new branch worktree", func(t *testing.T) {
		err := wm.CreateBeadsWorktree("beads-metadata", worktreePath)
		if err != nil {
			t.Fatalf("CreateBeadsWorktree failed: %v", err)
		}

		// Verify worktree exists
		if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
			t.Errorf("Worktree directory was not created")
		}

		// Verify .git file exists
		gitFile := filepath.Join(worktreePath, ".git")
		if _, err := os.Stat(gitFile); os.IsNotExist(err) {
			t.Errorf("Worktree .git file was not created")
		}

		// Verify .beads directory exists in worktree
		beadsDir := filepath.Join(worktreePath, ".beads")
		if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
			t.Errorf(".beads directory not found in worktree")
		}

		// Verify sparse checkout: other.txt should NOT exist
		otherFile := filepath.Join(worktreePath, "other.txt")
		if _, err := os.Stat(otherFile); err == nil {
			t.Errorf("Sparse checkout failed: other.txt should not exist in worktree")
		}
	})

	t.Run("idempotent - calling twice succeeds", func(t *testing.T) {
		worktreePath2 := filepath.Join(t.TempDir(), "beads-worktree-idempotent")
		
		// Create once
		if err := wm.CreateBeadsWorktree("beads-metadata-idempotent", worktreePath2); err != nil {
			t.Fatalf("First CreateBeadsWorktree failed: %v", err)
		}

		// Create again with same path (should succeed and be a no-op)
		if err := wm.CreateBeadsWorktree("beads-metadata-idempotent", worktreePath2); err != nil {
			t.Errorf("Second CreateBeadsWorktree failed (should be idempotent): %v", err)
		}
		
		// Verify worktree still exists and is valid
		if valid, err := wm.isValidWorktree(worktreePath2); err != nil || !valid {
			t.Errorf("Worktree should still be valid after idempotent call: valid=%v, err=%v", valid, err)
		}
	})
}

func TestRemoveBeadsWorktree(t *testing.T) {
	repoPath, cleanup := setupTestRepo(t)
	defer cleanup()

	wm := NewWorktreeManager(repoPath)
	worktreePath := filepath.Join(t.TempDir(), "beads-worktree")

	// Create worktree first
	if err := wm.CreateBeadsWorktree("beads-metadata", worktreePath); err != nil {
		t.Fatalf("CreateBeadsWorktree failed: %v", err)
	}

	// Verify it exists
	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		t.Fatalf("Worktree was not created")
	}

	// Remove it
	if err := wm.RemoveBeadsWorktree(worktreePath); err != nil {
		t.Fatalf("RemoveBeadsWorktree failed: %v", err)
	}

	// Verify it's gone
	if _, err := os.Stat(worktreePath); err == nil {
		t.Errorf("Worktree directory still exists after removal")
	}
}

func TestCheckWorktreeHealth(t *testing.T) {
	repoPath, cleanup := setupTestRepo(t)
	defer cleanup()

	wm := NewWorktreeManager(repoPath)

	t.Run("healthy worktree passes check", func(t *testing.T) {
		worktreePath := filepath.Join(t.TempDir(), "beads-worktree")
		
		if err := wm.CreateBeadsWorktree("beads-metadata", worktreePath); err != nil {
			t.Fatalf("CreateBeadsWorktree failed: %v", err)
		}

		if err := wm.CheckWorktreeHealth(worktreePath); err != nil {
			t.Errorf("CheckWorktreeHealth failed for healthy worktree: %v", err)
		}
	})

	t.Run("non-existent path fails check", func(t *testing.T) {
		nonExistentPath := filepath.Join(t.TempDir(), "does-not-exist")
		
		err := wm.CheckWorktreeHealth(nonExistentPath)
		if err == nil {
			t.Error("CheckWorktreeHealth should fail for non-existent path")
		}
		if !strings.Contains(err.Error(), "does not exist") {
			t.Errorf("Expected 'does not exist' error, got: %v", err)
		}
	})

	t.Run("invalid worktree fails check", func(t *testing.T) {
		invalidPath := filepath.Join(t.TempDir(), "invalid-worktree")
		if err := os.MkdirAll(invalidPath, 0750); err != nil {
			t.Fatalf("Failed to create invalid path: %v", err)
		}

		err := wm.CheckWorktreeHealth(invalidPath)
		if err == nil {
			t.Error("CheckWorktreeHealth should fail for invalid worktree")
		}
	})
}

func TestSyncJSONLToWorktree(t *testing.T) {
	repoPath, cleanup := setupTestRepo(t)
	defer cleanup()

	wm := NewWorktreeManager(repoPath)
	worktreePath := filepath.Join(t.TempDir(), "beads-worktree")

	// Create worktree
	if err := wm.CreateBeadsWorktree("beads-metadata", worktreePath); err != nil {
		t.Fatalf("CreateBeadsWorktree failed: %v", err)
	}

	// Update the JSONL in the main repo
	mainJSONL := filepath.Join(repoPath, ".beads", "test.jsonl")
	newData := []byte("updated data\n")
	if err := os.WriteFile(mainJSONL, newData, 0644); err != nil {
		t.Fatalf("Failed to update main JSONL: %v", err)
	}

	// Sync to worktree
	if err := wm.SyncJSONLToWorktree(worktreePath, ".beads/test.jsonl"); err != nil {
		t.Fatalf("SyncJSONLToWorktree failed: %v", err)
	}

	// Verify the data was synced
	worktreeJSONL := filepath.Join(worktreePath, ".beads", "test.jsonl")
	data, err := os.ReadFile(worktreeJSONL)
	if err != nil {
		t.Fatalf("Failed to read worktree JSONL: %v", err)
	}

	if string(data) != string(newData) {
		t.Errorf("JSONL data mismatch.\nExpected: %s\nGot: %s", string(newData), string(data))
	}
}

func TestBranchExists(t *testing.T) {
	repoPath, cleanup := setupTestRepo(t)
	defer cleanup()

	wm := NewWorktreeManager(repoPath)

	t.Run("main branch exists", func(t *testing.T) {
		// Get the default branch name (might be 'main' or 'master')
		cmd := exec.Command("git", "branch", "--show-current")
		cmd.Dir = repoPath
		output, err := cmd.Output()
		if err != nil {
			t.Fatalf("Failed to get current branch: %v", err)
		}
		currentBranch := strings.TrimSpace(string(output))

		exists := wm.branchExists(currentBranch)
		if !exists {
			t.Errorf("Current branch %s should exist", currentBranch)
		}
	})

	t.Run("non-existent branch returns false", func(t *testing.T) {
		exists := wm.branchExists("does-not-exist-branch")
		if exists {
			t.Error("Non-existent branch should return false")
		}
	})
}

func TestIsValidWorktree(t *testing.T) {
	repoPath, cleanup := setupTestRepo(t)
	defer cleanup()

	wm := NewWorktreeManager(repoPath)

	t.Run("created worktree is valid", func(t *testing.T) {
		worktreePath := filepath.Join(t.TempDir(), "beads-worktree")
		
		if err := wm.CreateBeadsWorktree("beads-metadata", worktreePath); err != nil {
			t.Fatalf("CreateBeadsWorktree failed: %v", err)
		}

		valid, err := wm.isValidWorktree(worktreePath)
		if err != nil {
			t.Fatalf("isValidWorktree failed: %v", err)
		}
		if !valid {
			t.Error("Created worktree should be valid")
		}
	})

	t.Run("non-worktree path is invalid", func(t *testing.T) {
		invalidPath := filepath.Join(t.TempDir(), "not-a-worktree")
		if err := os.MkdirAll(invalidPath, 0750); err != nil {
			t.Fatalf("Failed to create test directory: %v", err)
		}

		valid, err := wm.isValidWorktree(invalidPath)
		if err != nil {
			t.Fatalf("isValidWorktree failed: %v", err)
		}
		if valid {
			t.Error("Non-worktree path should be invalid")
		}
	})
}

func TestSparseCheckoutConfiguration(t *testing.T) {
	repoPath, cleanup := setupTestRepo(t)
	defer cleanup()

	wm := NewWorktreeManager(repoPath)
	worktreePath := filepath.Join(t.TempDir(), "beads-worktree")

	// Create worktree
	if err := wm.CreateBeadsWorktree("beads-metadata", worktreePath); err != nil {
		t.Fatalf("CreateBeadsWorktree failed: %v", err)
	}

	t.Run("sparse checkout includes .beads", func(t *testing.T) {
		if err := wm.verifySparseCheckout(worktreePath); err != nil {
			t.Errorf("verifySparseCheckout failed: %v", err)
		}
	})

	t.Run("can reconfigure sparse checkout", func(t *testing.T) {
		if err := wm.configureSparseCheckout(worktreePath); err != nil {
			t.Errorf("configureSparseCheckout failed: %v", err)
		}

		// Verify it's still correct
		if err := wm.verifySparseCheckout(worktreePath); err != nil {
			t.Errorf("verifySparseCheckout failed after reconfigure: %v", err)
		}
	})
}

func TestRemoveBeadsWorktreeManualCleanup(t *testing.T) {
	repoPath, cleanup := setupTestRepo(t)
	defer cleanup()

	wm := NewWorktreeManager(repoPath)
	worktreePath := filepath.Join(t.TempDir(), "beads-worktree")

	// Create worktree
	if err := wm.CreateBeadsWorktree("beads-metadata", worktreePath); err != nil {
		t.Fatalf("CreateBeadsWorktree failed: %v", err)
	}

	// Manually corrupt the worktree to force manual cleanup path
	// Remove the .git file which will cause git worktree remove to fail
	gitFile := filepath.Join(worktreePath, ".git")
	if err := os.Remove(gitFile); err != nil {
		t.Fatalf("Failed to remove .git file: %v", err)
	}

	// Now remove should use the manual cleanup path
	err := wm.RemoveBeadsWorktree(worktreePath)
	if err != nil {
		t.Errorf("RemoveBeadsWorktree should succeed with manual cleanup: %v", err)
	}

	// Verify directory is gone
	if _, err := os.Stat(worktreePath); err == nil {
		t.Error("Worktree directory should be removed")
	}
}

func TestRemoveBeadsWorktreeNonExistent(t *testing.T) {
	repoPath, cleanup := setupTestRepo(t)
	defer cleanup()

	wm := NewWorktreeManager(repoPath)
	nonExistentPath := filepath.Join(t.TempDir(), "does-not-exist")

	// Removing a non-existent worktree should succeed (no-op)
	err := wm.RemoveBeadsWorktree(nonExistentPath)
	if err != nil {
		t.Errorf("RemoveBeadsWorktree should succeed for non-existent path: %v", err)
	}
}

func TestSyncJSONLToWorktreeErrors(t *testing.T) {
	repoPath, cleanup := setupTestRepo(t)
	defer cleanup()

	wm := NewWorktreeManager(repoPath)
	worktreePath := filepath.Join(t.TempDir(), "beads-worktree")

	// Create worktree
	if err := wm.CreateBeadsWorktree("beads-metadata", worktreePath); err != nil {
		t.Fatalf("CreateBeadsWorktree failed: %v", err)
	}

	t.Run("fails when source file does not exist", func(t *testing.T) {
		err := wm.SyncJSONLToWorktree(worktreePath, ".beads/nonexistent.jsonl")
		if err == nil {
			t.Error("SyncJSONLToWorktree should fail when source file does not exist")
		}
		if !strings.Contains(err.Error(), "failed to read source JSONL") {
			t.Errorf("Expected 'failed to read source JSONL' error, got: %v", err)
		}
	})
}

func TestCreateBeadsWorktreeWithExistingBranch(t *testing.T) {
	repoPath, cleanup := setupTestRepo(t)
	defer cleanup()

	wm := NewWorktreeManager(repoPath)

	// Create a branch first
	branchName := "existing-branch"
	cmd := exec.Command("git", "branch", branchName)
	cmd.Dir = repoPath
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to create branch: %v\nOutput: %s", err, string(output))
	}

	// Now create worktree with this existing branch
	worktreePath := filepath.Join(t.TempDir(), "beads-worktree")
	if err := wm.CreateBeadsWorktree(branchName, worktreePath); err != nil {
		t.Fatalf("CreateBeadsWorktree failed with existing branch: %v", err)
	}

	// Verify worktree was created
	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		t.Error("Worktree directory was not created")
	}

	// Verify .beads exists
	beadsDir := filepath.Join(worktreePath, ".beads")
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		t.Error(".beads directory not found in worktree")
	}
}

func TestCreateBeadsWorktreeInvalidPath(t *testing.T) {
	repoPath, cleanup := setupTestRepo(t)
	defer cleanup()

	wm := NewWorktreeManager(repoPath)
	worktreePath := filepath.Join(t.TempDir(), "beads-worktree")

	// Create a file where the worktree directory should be (but not a valid worktree)
	if err := os.WriteFile(worktreePath, []byte("not a worktree"), 0644); err != nil {
		t.Fatalf("Failed to create blocking file: %v", err)
	}

	// CreateBeadsWorktree should handle this - it should remove the invalid path
	err := wm.CreateBeadsWorktree("beads-metadata", worktreePath)
	if err != nil {
		t.Fatalf("CreateBeadsWorktree should handle invalid path: %v", err)
	}

	// Verify worktree was created
	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		t.Error("Worktree directory was not created")
	}

	// Verify it's now a valid worktree (directory, not file)
	info, err := os.Stat(worktreePath)
	if err != nil {
		t.Fatalf("Failed to stat worktree path: %v", err)
	}
	if !info.IsDir() {
		t.Error("Worktree path should be a directory")
	}
}

func TestCheckWorktreeHealthWithBrokenSparseCheckout(t *testing.T) {
	repoPath, cleanup := setupTestRepo(t)
	defer cleanup()

	wm := NewWorktreeManager(repoPath)
	worktreePath := filepath.Join(t.TempDir(), "beads-worktree")

	// Create worktree
	if err := wm.CreateBeadsWorktree("beads-metadata", worktreePath); err != nil {
		t.Fatalf("CreateBeadsWorktree failed: %v", err)
	}

	// Read the .git file to find the git directory
	gitFile := filepath.Join(worktreePath, ".git")
	gitContent, err := os.ReadFile(gitFile)
	if err != nil {
		t.Fatalf("Failed to read .git file: %v", err)
	}

	// Parse "gitdir: /path/to/git/dir"
	gitDirLine := strings.TrimSpace(string(gitContent))
	gitDir := strings.TrimPrefix(gitDirLine, "gitdir: ")

	// Corrupt the sparse-checkout file
	sparseFile := filepath.Join(gitDir, "info", "sparse-checkout")
	if err := os.WriteFile(sparseFile, []byte("invalid\n"), 0644); err != nil {
		t.Fatalf("Failed to corrupt sparse-checkout: %v", err)
	}

	// CheckWorktreeHealth should detect the problem and attempt to fix it
	err = wm.CheckWorktreeHealth(worktreePath)
	if err != nil {
		t.Errorf("CheckWorktreeHealth should repair broken sparse checkout: %v", err)
	}

	// Verify sparse checkout was repaired
	if err := wm.verifySparseCheckout(worktreePath); err != nil {
		t.Errorf("Sparse checkout should be repaired: %v", err)
	}
}

func TestVerifySparseCheckoutErrors(t *testing.T) {
	repoPath, cleanup := setupTestRepo(t)
	defer cleanup()

	wm := NewWorktreeManager(repoPath)

	t.Run("fails with missing .git file", func(t *testing.T) {
		invalidPath := filepath.Join(t.TempDir(), "no-git-file")
		if err := os.MkdirAll(invalidPath, 0750); err != nil {
			t.Fatalf("Failed to create test directory: %v", err)
		}

		err := wm.verifySparseCheckout(invalidPath)
		if err == nil {
			t.Error("verifySparseCheckout should fail with missing .git file")
		}
	})

	t.Run("fails with invalid .git file format", func(t *testing.T) {
		invalidPath := filepath.Join(t.TempDir(), "invalid-git-file")
		if err := os.MkdirAll(invalidPath, 0750); err != nil {
			t.Fatalf("Failed to create test directory: %v", err)
		}

		// Create an invalid .git file (missing "gitdir: " prefix)
		gitFile := filepath.Join(invalidPath, ".git")
		if err := os.WriteFile(gitFile, []byte("invalid format"), 0644); err != nil {
			t.Fatalf("Failed to create invalid .git file: %v", err)
		}

		err := wm.verifySparseCheckout(invalidPath)
		if err == nil {
			t.Error("verifySparseCheckout should fail with invalid .git file format")
		}
		if !strings.Contains(err.Error(), "invalid .git file format") {
			t.Errorf("Expected 'invalid .git file format' error, got: %v", err)
		}
	})
}

func TestConfigureSparseCheckoutErrors(t *testing.T) {
	repoPath, cleanup := setupTestRepo(t)
	defer cleanup()

	wm := NewWorktreeManager(repoPath)

	t.Run("fails with missing .git file", func(t *testing.T) {
		invalidPath := filepath.Join(t.TempDir(), "no-git-file")
		if err := os.MkdirAll(invalidPath, 0750); err != nil {
			t.Fatalf("Failed to create test directory: %v", err)
		}

		err := wm.configureSparseCheckout(invalidPath)
		if err == nil {
			t.Error("configureSparseCheckout should fail with missing .git file")
		}
	})

	t.Run("fails with invalid .git file format", func(t *testing.T) {
		invalidPath := filepath.Join(t.TempDir(), "invalid-git-file")
		if err := os.MkdirAll(invalidPath, 0750); err != nil {
			t.Fatalf("Failed to create test directory: %v", err)
		}

		// Create an invalid .git file
		gitFile := filepath.Join(invalidPath, ".git")
		if err := os.WriteFile(gitFile, []byte("invalid format"), 0644); err != nil {
			t.Fatalf("Failed to create invalid .git file: %v", err)
		}

		err := wm.configureSparseCheckout(invalidPath)
		if err == nil {
			t.Error("configureSparseCheckout should fail with invalid .git file format")
		}
	})
}
