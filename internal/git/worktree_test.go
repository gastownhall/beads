package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
		// git sparse-checkout list will fail when .git file is invalid
		if !strings.Contains(err.Error(), "failed to list sparse checkout patterns") {
			t.Errorf("Expected 'failed to list sparse checkout patterns' error, got: %v", err)
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

// TestSyncJSONLToWorktreeMerge tests the merge behavior when worktree has more issues
// than the local repo (bd-52q fix for GitHub #464)
func TestSyncJSONLToWorktreeMerge(t *testing.T) {
	repoPath, cleanup := setupTestRepo(t)
	defer cleanup()

	wm := NewWorktreeManager(repoPath)
	worktreePath := filepath.Join(t.TempDir(), "beads-worktree")

	// Create worktree
	if err := wm.CreateBeadsWorktree("beads-metadata", worktreePath); err != nil {
		t.Fatalf("CreateBeadsWorktree failed: %v", err)
	}

	t.Run("merges when worktree has more issues than local", func(t *testing.T) {
		// Set up: worktree has 3 issues (simulating remote state)
		worktreeJSONL := filepath.Join(worktreePath, ".beads", "issues.jsonl")
		worktreeData := `{"id":"bd-001","title":"Issue 1","status":"open","created_at":"2025-01-01T00:00:00Z","created_by":"user1"}
{"id":"bd-002","title":"Issue 2","status":"open","created_at":"2025-01-01T00:00:01Z","created_by":"user1"}
{"id":"bd-003","title":"Issue 3","status":"open","created_at":"2025-01-01T00:00:02Z","created_by":"user1"}
`
		if err := os.WriteFile(worktreeJSONL, []byte(worktreeData), 0644); err != nil {
			t.Fatalf("Failed to write worktree JSONL: %v", err)
		}

		// Local has only 1 issue (simulating fresh clone that hasn't synced)
		mainJSONL := filepath.Join(repoPath, ".beads", "issues.jsonl")
		mainData := `{"id":"bd-004","title":"New Issue","status":"open","created_at":"2025-01-02T00:00:00Z","created_by":"user2"}
`
		if err := os.WriteFile(mainJSONL, []byte(mainData), 0644); err != nil {
			t.Fatalf("Failed to write main JSONL: %v", err)
		}

		// Sync should MERGE, not overwrite
		if err := wm.SyncJSONLToWorktree(worktreePath, ".beads/issues.jsonl"); err != nil {
			t.Fatalf("SyncJSONLToWorktree failed: %v", err)
		}

		// Read the result
		resultData, err := os.ReadFile(worktreeJSONL)
		if err != nil {
			t.Fatalf("Failed to read result JSONL: %v", err)
		}

		// Should have all 4 issues (3 from worktree + 1 from local)
		resultCount := countJSONLIssues(resultData)
		if resultCount != 4 {
			t.Errorf("Expected 4 issues after merge, got %d\nContent:\n%s", resultCount, string(resultData))
		}

		// Verify specific issues are present
		resultStr := string(resultData)
		for _, id := range []string{"bd-001", "bd-002", "bd-003", "bd-004"} {
			if !strings.Contains(resultStr, id) {
				t.Errorf("Expected issue %s to be in merged result", id)
			}
		}
	})

	t.Run("overwrites when local has same or more issues", func(t *testing.T) {
		// Set up: worktree has 2 issues
		worktreeJSONL := filepath.Join(worktreePath, ".beads", "issues.jsonl")
		worktreeData := `{"id":"bd-010","title":"Old 1","status":"open","created_at":"2025-01-01T00:00:00Z","created_by":"user1"}
{"id":"bd-011","title":"Old 2","status":"open","created_at":"2025-01-01T00:00:01Z","created_by":"user1"}
`
		if err := os.WriteFile(worktreeJSONL, []byte(worktreeData), 0644); err != nil {
			t.Fatalf("Failed to write worktree JSONL: %v", err)
		}

		// Local has 3 issues (more than worktree)
		mainJSONL := filepath.Join(repoPath, ".beads", "issues.jsonl")
		mainData := `{"id":"bd-020","title":"New 1","status":"open","created_at":"2025-01-02T00:00:00Z","created_by":"user2"}
{"id":"bd-021","title":"New 2","status":"open","created_at":"2025-01-02T00:00:01Z","created_by":"user2"}
{"id":"bd-022","title":"New 3","status":"open","created_at":"2025-01-02T00:00:02Z","created_by":"user2"}
`
		if err := os.WriteFile(mainJSONL, []byte(mainData), 0644); err != nil {
			t.Fatalf("Failed to write main JSONL: %v", err)
		}

		// Sync should OVERWRITE (local is authoritative when it has more)
		if err := wm.SyncJSONLToWorktree(worktreePath, ".beads/issues.jsonl"); err != nil {
			t.Fatalf("SyncJSONLToWorktree failed: %v", err)
		}

		// Read the result
		resultData, err := os.ReadFile(worktreeJSONL)
		if err != nil {
			t.Fatalf("Failed to read result JSONL: %v", err)
		}

		// Should have exactly 3 issues (from local)
		resultCount := countJSONLIssues(resultData)
		if resultCount != 3 {
			t.Errorf("Expected 3 issues after overwrite, got %d", resultCount)
		}

		// Should have local issues, not worktree issues
		resultStr := string(resultData)
		if strings.Contains(resultStr, "bd-010") || strings.Contains(resultStr, "bd-011") {
			t.Error("Old worktree issues should have been overwritten")
		}
		if !strings.Contains(resultStr, "bd-020") || !strings.Contains(resultStr, "bd-021") || !strings.Contains(resultStr, "bd-022") {
			t.Error("New local issues should be present")
		}
	})
}

// TestSyncJSONLToWorktree_DeleteMutation is a regression test for the bug where
// intentional deletions via `bd delete` are not synced to the sync branch.
// The issue: SyncJSONLToWorktree uses issue count to decide merge vs overwrite.
// When local has fewer issues (due to deletion), it merges instead of overwrites,
// which re-adds the deleted issue. This test verifies that when forceOverwrite
// is true (indicating an intentional mutation like delete), the local state
// is copied to the worktree without merging.
// GitHub Issue: #XXX (daemon auto-sync delete mutation not reflected in sync branch)
func TestSyncJSONLToWorktree_DeleteMutation(t *testing.T) {
	repoPath, cleanup := setupTestRepo(t)
	defer cleanup()

	wm := NewWorktreeManager(repoPath)
	worktreePath := filepath.Join(t.TempDir(), "beads-worktree-delete")

	// Create worktree
	if err := wm.CreateBeadsWorktree("beads-metadata", worktreePath); err != nil {
		t.Fatalf("CreateBeadsWorktree failed: %v", err)
	}

	t.Run("forceOverwrite=true overwrites even when local has fewer issues", func(t *testing.T) {
		// Set up: worktree has 3 issues (simulating sync branch state before delete)
		worktreeJSONL := filepath.Join(worktreePath, ".beads", "issues.jsonl")
		worktreeData := `{"id":"bd-100","title":"Issue 1","status":"open","created_at":"2025-01-01T00:00:00Z","created_by":"user1"}
{"id":"bd-101","title":"Issue 2","status":"open","created_at":"2025-01-01T00:00:01Z","created_by":"user1"}
{"id":"bd-102","title":"Issue 3 - TO BE DELETED","status":"open","created_at":"2025-01-01T00:00:02Z","created_by":"user1"}
`
		if err := os.WriteFile(worktreeJSONL, []byte(worktreeData), 0644); err != nil {
			t.Fatalf("Failed to write worktree JSONL: %v", err)
		}

		// Local has 2 issues (user deleted bd-102 via `bd delete bd-102 --force`)
		mainJSONL := filepath.Join(repoPath, ".beads", "issues.jsonl")
		mainData := `{"id":"bd-100","title":"Issue 1","status":"open","created_at":"2025-01-01T00:00:00Z","created_by":"user1"}
{"id":"bd-101","title":"Issue 2","status":"open","created_at":"2025-01-01T00:00:01Z","created_by":"user1"}
`
		if err := os.WriteFile(mainJSONL, []byte(mainData), 0644); err != nil {
			t.Fatalf("Failed to write main JSONL: %v", err)
		}

		// Sync with forceOverwrite=true (simulating daemon sync after delete mutation)
		if err := wm.SyncJSONLToWorktreeWithOptions(worktreePath, ".beads/issues.jsonl", SyncOptions{ForceOverwrite: true}); err != nil {
			t.Fatalf("SyncJSONLToWorktreeWithOptions failed: %v", err)
		}

		// Read the result
		resultData, err := os.ReadFile(worktreeJSONL)
		if err != nil {
			t.Fatalf("Failed to read result JSONL: %v", err)
		}

		// Should have exactly 2 issues (deleted issue should NOT be re-added)
		resultCount := countJSONLIssues(resultData)
		if resultCount != 2 {
			t.Errorf("Expected 2 issues after delete sync, got %d\nContent:\n%s", resultCount, string(resultData))
		}

		// Verify deleted issue is NOT present
		resultStr := string(resultData)
		if strings.Contains(resultStr, "bd-102") {
			t.Error("Deleted issue bd-102 should NOT be in synced result (forceOverwrite=true)")
		}

		// Verify remaining issues are present
		if !strings.Contains(resultStr, "bd-100") || !strings.Contains(resultStr, "bd-101") {
			t.Error("Remaining issues bd-100 and bd-101 should be present")
		}
	})

	t.Run("forceOverwrite=false merges when local has fewer issues (fresh clone scenario)", func(t *testing.T) {
		// Set up: worktree has 3 issues (simulating remote state)
		worktreeJSONL := filepath.Join(worktreePath, ".beads", "issues.jsonl")
		worktreeData := `{"id":"bd-200","title":"Remote Issue 1","status":"open","created_at":"2025-01-01T00:00:00Z","created_by":"user1"}
{"id":"bd-201","title":"Remote Issue 2","status":"open","created_at":"2025-01-01T00:00:01Z","created_by":"user1"}
{"id":"bd-202","title":"Remote Issue 3","status":"open","created_at":"2025-01-01T00:00:02Z","created_by":"user1"}
`
		if err := os.WriteFile(worktreeJSONL, []byte(worktreeData), 0644); err != nil {
			t.Fatalf("Failed to write worktree JSONL: %v", err)
		}

		// Local has 1 issue (fresh clone that hasn't synced yet)
		mainJSONL := filepath.Join(repoPath, ".beads", "issues.jsonl")
		mainData := `{"id":"bd-203","title":"Local New Issue","status":"open","created_at":"2025-01-02T00:00:00Z","created_by":"user2"}
`
		if err := os.WriteFile(mainJSONL, []byte(mainData), 0644); err != nil {
			t.Fatalf("Failed to write main JSONL: %v", err)
		}

		// Sync with forceOverwrite=false (default behavior for non-mutation syncs)
		if err := wm.SyncJSONLToWorktreeWithOptions(worktreePath, ".beads/issues.jsonl", SyncOptions{ForceOverwrite: false}); err != nil {
			t.Fatalf("SyncJSONLToWorktreeWithOptions failed: %v", err)
		}

		// Read the result
		resultData, err := os.ReadFile(worktreeJSONL)
		if err != nil {
			t.Fatalf("Failed to read result JSONL: %v", err)
		}

		// Should have all 4 issues (3 from remote + 1 from local, merged)
		resultCount := countJSONLIssues(resultData)
		if resultCount != 4 {
			t.Errorf("Expected 4 issues after merge, got %d\nContent:\n%s", resultCount, string(resultData))
		}

		// Verify all issues are present
		resultStr := string(resultData)
		for _, id := range []string{"bd-200", "bd-201", "bd-202", "bd-203"} {
			if !strings.Contains(resultStr, id) {
				t.Errorf("Expected issue %s to be in merged result", id)
			}
		}
	})
}

func TestCountJSONLIssues(t *testing.T) {
	tests := []struct {
		name     string
		data     string
		expected int
	}{
		{
			name:     "empty file",
			data:     "",
			expected: 0,
		},
		{
			name:     "single issue",
			data:     `{"id":"bd-001","title":"Test"}`,
			expected: 1,
		},
		{
			name: "multiple issues",
			data: `{"id":"bd-001","title":"Test 1"}
{"id":"bd-002","title":"Test 2"}
{"id":"bd-003","title":"Test 3"}`,
			expected: 3,
		},
		{
			name: "with blank lines",
			data: `{"id":"bd-001","title":"Test 1"}

{"id":"bd-002","title":"Test 2"}

`,
			expected: 2,
		},
		{
			name:     "non-JSON lines ignored",
			data:     "# comment\n{\"id\":\"bd-001\"}\nnot json",
			expected: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			count := countJSONLIssues([]byte(tt.data))
			if count != tt.expected {
				t.Errorf("countJSONLIssues() = %d, want %d", count, tt.expected)
			}
		})
	}
}

// TestGetMainRepoRoot tests the GetMainRepoRoot function for various scenarios
func TestGetMainRepoRoot(t *testing.T) {
	t.Run("returns correct root for regular repo", func(t *testing.T) {
		ResetCaches() // Reset caches from previous subtests
		repoPath, cleanup := setupTestRepo(t)
		defer cleanup()

		// Save current dir and change to repo
		originalDir, err := os.Getwd()
		if err != nil {
			t.Fatalf("Failed to get current dir: %v", err)
		}
		defer func() { _ = os.Chdir(originalDir) }()

		if err := os.Chdir(repoPath); err != nil {
			t.Fatalf("Failed to chdir to repo: %v", err)
		}
		ResetCaches() // Reset after chdir

		root, err := GetMainRepoRoot()
		if err != nil {
			t.Fatalf("GetMainRepoRoot failed: %v", err)
		}

		// Resolve symlinks for comparison (e.g., /tmp -> /private/tmp on macOS)
		expectedRoot, _ := filepath.EvalSymlinks(repoPath)
		actualRoot, _ := filepath.EvalSymlinks(root)

		if actualRoot != expectedRoot {
			t.Errorf("GetMainRepoRoot() = %s, want %s", actualRoot, expectedRoot)
		}
	})

	t.Run("returns correct root for submodule repo", func(t *testing.T) {
		ResetCaches() // Reset caches from previous subtests
		superRepoPath, superCleanup := setupTestRepo(t)
		defer superCleanup()

		submoduleRepoPath, submoduleCleanup := setupTestRepo(t)
		defer submoduleCleanup()

		addCmd := exec.Command("git", "-c", "protocol.file.allow=always", "submodule", "add", submoduleRepoPath, "core")
		addCmd.Dir = superRepoPath
		if output, err := addCmd.CombinedOutput(); err != nil {
			t.Fatalf("Failed to add submodule: %v\nOutput: %s", err, string(output))
		}

		commitCmd := exec.Command("git", "commit", "-m", "Add submodule")
		commitCmd.Dir = superRepoPath
		if output, err := commitCmd.CombinedOutput(); err != nil {
			t.Fatalf("Failed to commit submodule: %v\nOutput: %s", err, string(output))
		}

		submodulePath := filepath.Join(superRepoPath, "core")

		// Save current dir and change to submodule
		originalDir, err := os.Getwd()
		if err != nil {
			t.Fatalf("Failed to get current dir: %v", err)
		}
		defer func() { _ = os.Chdir(originalDir) }()

		if err := os.Chdir(submodulePath); err != nil {
			t.Fatalf("Failed to chdir to submodule: %v", err)
		}
		ResetCaches() // Reset after chdir

		root, err := GetMainRepoRoot()
		if err != nil {
			t.Fatalf("GetMainRepoRoot failed: %v", err)
		}

		// Resolve symlinks for comparison (e.g., /tmp -> /private/tmp on macOS)
		expectedRoot, _ := filepath.EvalSymlinks(submodulePath)
		actualRoot, _ := filepath.EvalSymlinks(root)

		if actualRoot != expectedRoot {
			t.Errorf("GetMainRepoRoot() = %s, want %s (submodule repo)", actualRoot, expectedRoot)
		}
	})

	t.Run("returns main repo root from worktree", func(t *testing.T) {
		ResetCaches() // Reset caches from previous subtests
		repoPath, cleanup := setupTestRepo(t)
		defer cleanup()

		wm := NewWorktreeManager(repoPath)
		worktreePath := filepath.Join(t.TempDir(), "test-worktree")

		if err := wm.CreateBeadsWorktree("test-branch", worktreePath); err != nil {
			t.Fatalf("CreateBeadsWorktree failed: %v", err)
		}

		// Save current dir and change to worktree
		originalDir, err := os.Getwd()
		if err != nil {
			t.Fatalf("Failed to get current dir: %v", err)
		}
		defer func() { _ = os.Chdir(originalDir) }()

		if err := os.Chdir(worktreePath); err != nil {
			t.Fatalf("Failed to chdir to worktree: %v", err)
		}
		ResetCaches() // Reset after chdir

		root, err := GetMainRepoRoot()
		if err != nil {
			t.Fatalf("GetMainRepoRoot failed: %v", err)
		}

		// Resolve symlinks for comparison
		expectedRoot, _ := filepath.EvalSymlinks(repoPath)
		actualRoot, _ := filepath.EvalSymlinks(root)

		if actualRoot != expectedRoot {
			t.Errorf("GetMainRepoRoot() = %s, want %s (main repo)", actualRoot, expectedRoot)
		}
	})

	t.Run("returns main repo root from nested worktree (GH#509)", func(t *testing.T) {
		ResetCaches() // Reset caches from previous subtests
		repoPath, cleanup := setupTestRepo(t)
		defer cleanup()

		// Create a nested worktree directory structure: repo/.worktrees/feature/
		nestedWorktreePath := filepath.Join(repoPath, ".worktrees", "feature-branch")

		wm := NewWorktreeManager(repoPath)
		if err := wm.CreateBeadsWorktree("feature-branch", nestedWorktreePath); err != nil {
			t.Fatalf("CreateBeadsWorktree failed: %v", err)
		}

		// Save current dir and change to nested worktree
		originalDir, err := os.Getwd()
		if err != nil {
			t.Fatalf("Failed to get current dir: %v", err)
		}
		defer func() { _ = os.Chdir(originalDir) }()

		if err := os.Chdir(nestedWorktreePath); err != nil {
			t.Fatalf("Failed to chdir to nested worktree: %v", err)
		}
		ResetCaches() // Reset after chdir

		root, err := GetMainRepoRoot()
		if err != nil {
			t.Fatalf("GetMainRepoRoot failed: %v", err)
		}

		// Resolve symlinks for comparison
		expectedRoot, _ := filepath.EvalSymlinks(repoPath)
		actualRoot, _ := filepath.EvalSymlinks(root)

		if actualRoot != expectedRoot {
			t.Errorf("GetMainRepoRoot() = %s, want %s (main repo, not worktree)", actualRoot, expectedRoot)
		}
	})

	t.Run("returns main repo root from subdirectory of nested worktree", func(t *testing.T) {
		ResetCaches() // Reset caches from previous subtests
		repoPath, cleanup := setupTestRepo(t)
		defer cleanup()

		// Create a nested worktree
		nestedWorktreePath := filepath.Join(repoPath, ".worktrees", "feature-branch")

		wm := NewWorktreeManager(repoPath)
		if err := wm.CreateBeadsWorktree("feature-branch", nestedWorktreePath); err != nil {
			t.Fatalf("CreateBeadsWorktree failed: %v", err)
		}

		// Create a subdirectory in the worktree
		subDir := filepath.Join(nestedWorktreePath, "some", "nested", "dir")
		if err := os.MkdirAll(subDir, 0750); err != nil {
			t.Fatalf("Failed to create subdir: %v", err)
		}

		// Save current dir and change to subdirectory
		originalDir, err := os.Getwd()
		if err != nil {
			t.Fatalf("Failed to get current dir: %v", err)
		}
		defer func() { _ = os.Chdir(originalDir) }()

		if err := os.Chdir(subDir); err != nil {
			t.Fatalf("Failed to chdir to subdir: %v", err)
		}
		ResetCaches() // Reset after chdir

		root, err := GetMainRepoRoot()
		if err != nil {
			t.Fatalf("GetMainRepoRoot failed: %v", err)
		}

		// Resolve symlinks for comparison
		expectedRoot, _ := filepath.EvalSymlinks(repoPath)
		actualRoot, _ := filepath.EvalSymlinks(root)

		if actualRoot != expectedRoot {
			t.Errorf("GetMainRepoRoot() = %s, want %s (main repo)", actualRoot, expectedRoot)
		}
	})
}

// TestIsWorktree tests the IsWorktree function
func TestIsWorktree(t *testing.T) {
	t.Run("returns false for regular repo", func(t *testing.T) {
		ResetCaches() // Reset caches from previous subtests
		repoPath, cleanup := setupTestRepo(t)
		defer cleanup()

		originalDir, err := os.Getwd()
		if err != nil {
			t.Fatalf("Failed to get current dir: %v", err)
		}
		defer func() { _ = os.Chdir(originalDir) }()

		if err := os.Chdir(repoPath); err != nil {
			t.Fatalf("Failed to chdir to repo: %v", err)
		}
		ResetCaches() // Reset after chdir

		if IsWorktree() {
			t.Error("IsWorktree() should return false for regular repo")
		}
	})

	t.Run("returns true for worktree", func(t *testing.T) {
		ResetCaches() // Reset caches from previous subtests
		repoPath, cleanup := setupTestRepo(t)
		defer cleanup()

		wm := NewWorktreeManager(repoPath)
		worktreePath := filepath.Join(t.TempDir(), "test-worktree")

		if err := wm.CreateBeadsWorktree("test-branch", worktreePath); err != nil {
			t.Fatalf("CreateBeadsWorktree failed: %v", err)
		}

		originalDir, err := os.Getwd()
		if err != nil {
			t.Fatalf("Failed to get current dir: %v", err)
		}
		defer func() { _ = os.Chdir(originalDir) }()

		if err := os.Chdir(worktreePath); err != nil {
			t.Fatalf("Failed to chdir to worktree: %v", err)
		}

		ResetCaches() // Reset after chdir to worktree
		if !IsWorktree() {
			t.Error("IsWorktree() should return true for worktree")
		}
	})
}

// TestCreateBeadsWorktree_MissingButRegistered tests the issue #609 scenario where
// the worktree directory is deleted but git still has it registered in .git/worktrees/.
// The -f flag on git worktree add should handle this gracefully.
func TestCreateBeadsWorktree_MissingButRegistered(t *testing.T) {
	repoPath, cleanup := setupTestRepo(t)
	defer cleanup()

	wm := NewWorktreeManager(repoPath)
	worktreePath := filepath.Join(t.TempDir(), "beads-worktree-gh609")
	branch := "beads-metadata-gh609"

	// Step 1: Create a worktree
	if err := wm.CreateBeadsWorktree(branch, worktreePath); err != nil {
		t.Fatalf("Initial CreateBeadsWorktree failed: %v", err)
	}

	// Verify it exists and is registered
	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		t.Fatal("Worktree was not created")
	}

	// Step 2: Manually delete the worktree directory (simulating the bug scenario)
	// but leave the git registration in .git/worktrees/
	if err := os.RemoveAll(worktreePath); err != nil {
		t.Fatalf("Failed to remove worktree directory: %v", err)
	}

	// Verify the directory is gone
	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Fatal("Worktree directory should not exist after removal")
	}

	// Verify git still has it registered (this is the "missing but registered" state)
	cmd := exec.Command("git", "worktree", "list", "--porcelain")
	cmd.Dir = repoPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to list worktrees: %v", err)
	}
	if !strings.Contains(string(output), "worktree") {
		t.Log("Note: git worktree list shows no worktrees - prune may have run")
	}

	// Step 3: Try to recreate the worktree - this should succeed with -f flag (issue #609 fix)
	// Without the fix, this would fail with:
	// "fatal: '.git/beads-worktrees/...' is a missing but already registered worktree"
	if err := wm.CreateBeadsWorktree(branch, worktreePath); err != nil {
		t.Errorf("CreateBeadsWorktree failed for missing-but-registered worktree (issue #609): %v", err)
	}

	// Verify the worktree was recreated successfully
	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		t.Error("Worktree was not recreated after issue #609 fix")
	}

	// Verify it's a valid worktree
	valid, err := wm.isValidWorktree(worktreePath)
	if err != nil || !valid {
		t.Errorf("Recreated worktree should be valid: valid=%v, err=%v", valid, err)
	}
}

// TestCreateBeadsWorktree_MainRepoSparseCheckoutDisabled tests that creating a worktree
// does not leave core.sparseCheckout enabled on the main repo (GH#886).
// Git 2.38+ enables sparse checkout on the main repo as a side effect of worktree creation,
// which causes confusing "You are in a sparse checkout with 100% of tracked files present"
// message in git status.
func TestCreateBeadsWorktree_MainRepoSparseCheckoutDisabled(t *testing.T) {
	repoPath, cleanup := setupTestRepo(t)
	defer cleanup()

	wm := NewWorktreeManager(repoPath)
	worktreePath := filepath.Join(t.TempDir(), "beads-worktree-gh886")

	// Verify sparse checkout is not enabled before worktree creation
	cmd := exec.Command("git", "config", "--get", "core.sparseCheckout")
	cmd.Dir = repoPath
	output, _ := cmd.Output()
	initialValue := strings.TrimSpace(string(output))
	// Empty or "false" are both acceptable initial states
	if initialValue == "true" {
		t.Log("Note: sparse checkout was already enabled before test")
	}

	// Create worktree
	if err := wm.CreateBeadsWorktree("beads-gh886", worktreePath); err != nil {
		t.Fatalf("CreateBeadsWorktree failed: %v", err)
	}

	// Verify sparse checkout is disabled on main repo after worktree creation
	cmd = exec.Command("git", "config", "--get", "core.sparseCheckout")
	cmd.Dir = repoPath
	output, _ = cmd.Output()
	finalValue := strings.TrimSpace(string(output))

	// Should be either empty (unset) or "false"
	if finalValue == "true" {
		t.Errorf("GH#886: Main repo has core.sparseCheckout=true after worktree creation. "+
			"This causes confusing git status message. Value should be 'false' or unset, got: %q", finalValue)
	}

	// Verify that sparse checkout functionality STILL WORKS in the worktree
	// (the patterns were applied during checkout, before we disabled the config)
	// Check that .beads exists but other.txt does not
	beadsDir := filepath.Join(worktreePath, ".beads")
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		t.Error(".beads directory should exist in worktree (sparse checkout should include it)")
	}

	otherFile := filepath.Join(worktreePath, "other.txt")
	if _, err := os.Stat(otherFile); err == nil {
		t.Error("other.txt should NOT exist in worktree (sparse checkout should exclude it)")
	}
}

// TestGetRepoRootCanonicalCase tests that GetRepoRoot returns paths with correct
// filesystem case on case-insensitive filesystems (GH#880)
func TestGetRepoRootCanonicalCase(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("case canonicalization test only runs on macOS")
	}

	// Create a repo with mixed-case directory
	tmpDir := t.TempDir()
	mixedCaseDir := filepath.Join(tmpDir, "TestRepo")
	if err := os.MkdirAll(mixedCaseDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Initialize git repo
	cmd := exec.Command("git", "init")
	cmd.Dir = mixedCaseDir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to init git repo: %v\nOutput: %s", err, string(output))
	}

	// Save cwd and change to the repo using WRONG case
	originalDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get current dir: %v", err)
	}
	defer func() { _ = os.Chdir(originalDir) }()

	// Access the repo with lowercase (wrong case)
	wrongCasePath := filepath.Join(tmpDir, "testrepo")

	// Verify the wrong case path works on macOS (case-insensitive)
	if _, err := os.Stat(wrongCasePath); err != nil {
		t.Fatalf("Wrong case path should be accessible on macOS: %v", err)
	}

	if err := os.Chdir(wrongCasePath); err != nil {
		t.Fatalf("Failed to chdir with wrong case: %v", err)
	}

	ResetCaches() // Reset git context cache

	// GetRepoRoot should return the canonical case (TestRepo, not testrepo)
	repoRoot := GetRepoRoot()
	if repoRoot == "" {
		t.Fatal("GetRepoRoot returned empty string")
	}

	// The path should end with "TestRepo" (correct case), not "testrepo"
	if !strings.HasSuffix(repoRoot, "TestRepo") {
		t.Errorf("GetRepoRoot() = %q, want path ending in 'TestRepo' (correct case)", repoRoot)
	}
}

// TestNormalizeBeadsRelPath tests path normalization for bare repo worktrees (GH#785, GH#810)
func TestNormalizeBeadsRelPath(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "already normalized path",
			input:    ".beads/issues.jsonl",
			expected: ".beads/issues.jsonl",
		},
		{
			name:     "worktree prefix - main",
			input:    "main/.beads/issues.jsonl",
			expected: ".beads/issues.jsonl",
		},
		{
			name:     "worktree prefix - feature branch",
			input:    "feat-tts-config/.beads/issues.jsonl",
			expected: ".beads/issues.jsonl",
		},
		{
			name:     "nested worktree prefix",
			input:    "worktrees/feature/.beads/issues.jsonl",
			expected: ".beads/issues.jsonl",
		},
		{
			name:     "metadata file",
			input:    "main/.beads/metadata.json",
			expected: ".beads/metadata.json",
		},
		{
			name:     "no .beads in path",
			input:    "some/other/path.jsonl",
			expected: "some/other/path.jsonl",
		},
		{
			name:     "only .beads dir (no trailing slash - not normalized)",
			input:    ".beads",
			expected: ".beads",
		},
		{
			name:     "empty path",
			input:    "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := NormalizeBeadsRelPath(tt.input)
			if result != tt.expected {
				t.Errorf("NormalizeBeadsRelPath(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// TestSyncJSONLToWorktree_SelfCopy tests that syncing a file to itself is a no-op.
// GH#1298: When sync-branch is configured, findJSONLPath() returns the worktree path
// due to GH#1103. This causes the sync to attempt copying the worktree JSONL to itself.
// The fix ensures this is detected and skipped without error.
func TestSyncJSONLToWorktree_SelfCopy(t *testing.T) {
	repoPath, cleanup := setupTestRepo(t)
	defer cleanup()

	wm := NewWorktreeManager(repoPath)
	worktreePath := filepath.Join(t.TempDir(), "beads-worktree")

	// Create worktree
	if err := wm.CreateBeadsWorktree("beads-metadata", worktreePath); err != nil {
		t.Fatalf("CreateBeadsWorktree failed: %v", err)
	}

	// Create a JSONL file in the worktree
	worktreeJSONL := filepath.Join(worktreePath, ".beads", "issues.jsonl")
	originalData := []byte(`{"id":"bd-001","title":"Test Issue","status":"open"}` + "\n")
	if err := os.WriteFile(worktreeJSONL, originalData, 0644); err != nil {
		t.Fatalf("Failed to write worktree JSONL: %v", err)
	}

	// Get the file's modification time before sync
	statBefore, err := os.Stat(worktreeJSONL)
	if err != nil {
		t.Fatalf("Failed to stat worktree JSONL: %v", err)
	}

	// Simulate GH#1298: jsonlRelPath includes the worktree path prefix
	// This happens when findJSONLPath() returns the worktree path due to GH#1103
	// e.g., ".git/beads-worktrees/beads-metadata/.beads/issues.jsonl"
	worktreeRelPath, err := filepath.Rel(repoPath, worktreeJSONL)
	if err != nil {
		t.Fatalf("Failed to get relative path: %v", err)
	}

	// Sync with the worktree-relative path (simulates the GH#1298 bug scenario)
	// This should detect srcPath == dstPath and return nil without modifying the file
	if err := wm.SyncJSONLToWorktree(worktreePath, worktreeRelPath); err != nil {
		t.Fatalf("SyncJSONLToWorktree failed: %v", err)
	}

	// Verify the file was not modified (content unchanged)
	resultData, err := os.ReadFile(worktreeJSONL)
	if err != nil {
		t.Fatalf("Failed to read worktree JSONL after sync: %v", err)
	}

	if string(resultData) != string(originalData) {
		t.Errorf("File content changed unexpectedly.\nExpected: %s\nGot: %s", string(originalData), string(resultData))
	}

	// Verify the file's modification time didn't change (file wasn't rewritten)
	statAfter, err := os.Stat(worktreeJSONL)
	if err != nil {
		t.Fatalf("Failed to stat worktree JSONL after sync: %v", err)
	}

	if !statAfter.ModTime().Equal(statBefore.ModTime()) {
		t.Errorf("File was modified when it should have been skipped (self-copy detected)")
	}
}
