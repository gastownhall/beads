package syncbranch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestIsNonFastForwardError tests the non-fast-forward error detection
func TestIsNonFastForwardError(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{
			name:   "non-fast-forward message",
			output: "error: failed to push some refs to 'origin'\n! [rejected] main -> main (non-fast-forward)",
			want:   true,
		},
		{
			name:   "fetch first message",
			output: "error: failed to push some refs to 'origin'\nhint: Updates were rejected because the remote contains work that you do\nhint: not have locally. This is usually caused by another repository pushing\nhint: to the same ref. You may want to first integrate the remote changes\nhint: (e.g., 'git pull ...') before pushing again.\nhint: See the 'Note about fast-forwards' in 'git push --help' for details.\nfetch first",
			want:   true,
		},
		{
			name:   "rejected behind message",
			output: "To github.com:user/repo.git\n! [rejected] main -> main (non-fast-forward)\nerror: failed to push some refs\nhint: rejected because behind remote",
			want:   true,
		},
		{
			name:   "normal push success",
			output: "Everything up-to-date",
			want:   false,
		},
		{
			name:   "authentication error",
			output: "fatal: Authentication failed for 'https://github.com/user/repo.git/'",
			want:   false,
		},
		{
			name:   "permission denied",
			output: "ERROR: Permission to user/repo.git denied to user.",
			want:   false,
		},
		{
			name:   "empty output",
			output: "",
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isNonFastForwardError(tt.output)
			if got != tt.want {
				t.Errorf("isNonFastForwardError(%q) = %v, want %v", tt.output, got, tt.want)
			}
		})
	}
}

// TestHasChangesInWorktree tests change detection in worktree
func TestHasChangesInWorktree(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()

	t.Run("no changes in clean worktree", func(t *testing.T) {
		repoDir := setupTestRepo(t)
		defer os.RemoveAll(repoDir)

		// Create initial commit
		jsonlPath := filepath.Join(repoDir, ".beads", "issues.jsonl")
		writeFile(t, jsonlPath, `{"id":"test-1"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "initial")

		hasChanges, err := hasChangesInWorktree(ctx, repoDir, jsonlPath)
		if err != nil {
			t.Fatalf("hasChangesInWorktree() error = %v", err)
		}
		if hasChanges {
			t.Error("hasChangesInWorktree() = true for clean worktree, want false")
		}
	})

	t.Run("detects uncommitted changes", func(t *testing.T) {
		repoDir := setupTestRepo(t)
		defer os.RemoveAll(repoDir)

		// Create initial commit
		jsonlPath := filepath.Join(repoDir, ".beads", "issues.jsonl")
		writeFile(t, jsonlPath, `{"id":"test-1"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "initial")

		// Modify file without committing
		writeFile(t, jsonlPath, `{"id":"test-1"}`+"\n"+`{"id":"test-2"}`)

		hasChanges, err := hasChangesInWorktree(ctx, repoDir, jsonlPath)
		if err != nil {
			t.Fatalf("hasChangesInWorktree() error = %v", err)
		}
		if !hasChanges {
			t.Error("hasChangesInWorktree() = false with uncommitted changes, want true")
		}
	})

	t.Run("detects new untracked files", func(t *testing.T) {
		repoDir := setupTestRepo(t)
		defer os.RemoveAll(repoDir)

		// Create initial commit
		jsonlPath := filepath.Join(repoDir, ".beads", "issues.jsonl")
		writeFile(t, jsonlPath, `{"id":"test-1"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "initial")

		// Add new file in .beads
		writeFile(t, filepath.Join(repoDir, ".beads", "metadata.json"), `{}`)

		hasChanges, err := hasChangesInWorktree(ctx, repoDir, jsonlPath)
		if err != nil {
			t.Fatalf("hasChangesInWorktree() error = %v", err)
		}
		if !hasChanges {
			t.Error("hasChangesInWorktree() = false with new file, want true")
		}
	})

	t.Run("handles file outside .beads dir", func(t *testing.T) {
		repoDir := setupTestRepo(t)
		defer os.RemoveAll(repoDir)

		// Create initial commit
		jsonlPath := filepath.Join(repoDir, "issues.jsonl") // Not in .beads
		writeFile(t, jsonlPath, `{"id":"test-1"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "initial")

		// Modify file
		writeFile(t, jsonlPath, `{"id":"test-1"}`+"\n"+`{"id":"test-2"}`)

		hasChanges, err := hasChangesInWorktree(ctx, repoDir, jsonlPath)
		if err != nil {
			t.Fatalf("hasChangesInWorktree() error = %v", err)
		}
		if !hasChanges {
			t.Error("hasChangesInWorktree() = false with modified file outside .beads, want true")
		}
	})
}

// TestCommitInWorktree tests committing changes in worktree
func TestCommitInWorktree(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()

	t.Run("commits staged changes", func(t *testing.T) {
		repoDir := setupTestRepo(t)
		defer os.RemoveAll(repoDir)

		// Create initial commit
		jsonlPath := filepath.Join(repoDir, ".beads", "issues.jsonl")
		writeFile(t, jsonlPath, `{"id":"test-1"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "initial")

		// Modify file
		writeFile(t, jsonlPath, `{"id":"test-1"}`+"\n"+`{"id":"test-2"}`)

		// Commit using our function
		err := commitInWorktree(ctx, repoDir, ".beads/issues.jsonl", "test commit message")
		if err != nil {
			t.Fatalf("commitInWorktree() error = %v", err)
		}

		// Verify commit was made
		output := getGitOutput(t, repoDir, "log", "-1", "--format=%s")
		if !strings.Contains(output, "test commit message") {
			t.Errorf("commit message = %q, want to contain 'test commit message'", output)
		}
	})

	t.Run("commits entire .beads directory", func(t *testing.T) {
		repoDir := setupTestRepo(t)
		defer os.RemoveAll(repoDir)

		// Create initial commit
		jsonlPath := filepath.Join(repoDir, ".beads", "issues.jsonl")
		writeFile(t, jsonlPath, `{"id":"test-1"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "initial")

		// Add multiple files
		writeFile(t, filepath.Join(repoDir, ".beads", "metadata.json"), `{"version":"1"}`)
		writeFile(t, jsonlPath, `{"id":"test-1"}`+"\n"+`{"id":"test-2"}`)

		err := commitInWorktree(ctx, repoDir, ".beads/issues.jsonl", "multi-file commit")
		if err != nil {
			t.Fatalf("commitInWorktree() error = %v", err)
		}

		// Verify both files were committed
		output := getGitOutput(t, repoDir, "diff", "--name-only", "HEAD~1")
		if !strings.Contains(output, "issues.jsonl") {
			t.Error("issues.jsonl not in commit")
		}
		if !strings.Contains(output, "metadata.json") {
			t.Error("metadata.json not in commit")
		}
	})
}

// TestCopyJSONLToMainRepo tests copying JSONL between worktree and main repo
func TestCopyJSONLToMainRepo(t *testing.T) {
	t.Run("copies JSONL file successfully", func(t *testing.T) {
		// Setup worktree directory
		worktreeDir, _ := os.MkdirTemp("", "test-worktree-*")
		defer os.RemoveAll(worktreeDir)

		// Setup main repo directory
		mainRepoDir, _ := os.MkdirTemp("", "test-mainrepo-*")
		defer os.RemoveAll(mainRepoDir)

		// Create .beads directories
		os.MkdirAll(filepath.Join(worktreeDir, ".beads"), 0750)
		os.MkdirAll(filepath.Join(mainRepoDir, ".beads"), 0750)

		// Write content to worktree JSONL
		worktreeContent := `{"id":"test-1","title":"Test Issue"}`
		if err := os.WriteFile(filepath.Join(worktreeDir, ".beads", "issues.jsonl"), []byte(worktreeContent), 0600); err != nil {
			t.Fatalf("Failed to write worktree JSONL: %v", err)
		}

		mainJSONLPath := filepath.Join(mainRepoDir, ".beads", "issues.jsonl")

		err := copyJSONLToMainRepo(worktreeDir, ".beads/issues.jsonl", mainJSONLPath)
		if err != nil {
			t.Fatalf("copyJSONLToMainRepo() error = %v", err)
		}

		// Verify content was copied
		copied, err := os.ReadFile(mainJSONLPath)
		if err != nil {
			t.Fatalf("Failed to read copied file: %v", err)
		}
		if string(copied) != worktreeContent {
			t.Errorf("copied content = %q, want %q", string(copied), worktreeContent)
		}
	})

	t.Run("returns nil when worktree JSONL does not exist", func(t *testing.T) {
		worktreeDir, _ := os.MkdirTemp("", "test-worktree-*")
		defer os.RemoveAll(worktreeDir)

		mainRepoDir, _ := os.MkdirTemp("", "test-mainrepo-*")
		defer os.RemoveAll(mainRepoDir)

		mainJSONLPath := filepath.Join(mainRepoDir, ".beads", "issues.jsonl")

		err := copyJSONLToMainRepo(worktreeDir, ".beads/issues.jsonl", mainJSONLPath)
		if err != nil {
			t.Errorf("copyJSONLToMainRepo() for nonexistent file = %v, want nil", err)
		}
	})

	t.Run("also copies metadata.json if present", func(t *testing.T) {
		worktreeDir, _ := os.MkdirTemp("", "test-worktree-*")
		defer os.RemoveAll(worktreeDir)

		mainRepoDir, _ := os.MkdirTemp("", "test-mainrepo-*")
		defer os.RemoveAll(mainRepoDir)

		// Create .beads directories
		os.MkdirAll(filepath.Join(worktreeDir, ".beads"), 0750)
		os.MkdirAll(filepath.Join(mainRepoDir, ".beads"), 0750)

		// Write JSONL and metadata to worktree
		if err := os.WriteFile(filepath.Join(worktreeDir, ".beads", "issues.jsonl"), []byte(`{}`), 0600); err != nil {
			t.Fatalf("Failed to write worktree JSONL: %v", err)
		}
		metadataContent := `{"prefix":"bd"}`
		if err := os.WriteFile(filepath.Join(worktreeDir, ".beads", "metadata.json"), []byte(metadataContent), 0600); err != nil {
			t.Fatalf("Failed to write metadata: %v", err)
		}

		mainJSONLPath := filepath.Join(mainRepoDir, ".beads", "issues.jsonl")

		err := copyJSONLToMainRepo(worktreeDir, ".beads/issues.jsonl", mainJSONLPath)
		if err != nil {
			t.Fatalf("copyJSONLToMainRepo() error = %v", err)
		}

		// Verify metadata was also copied
		metadata, err := os.ReadFile(filepath.Join(mainRepoDir, ".beads", "metadata.json"))
		if err != nil {
			t.Fatalf("Failed to read metadata: %v", err)
		}
		if string(metadata) != metadataContent {
			t.Errorf("metadata content = %q, want %q", string(metadata), metadataContent)
		}
	})
}

// TestGetRemoteForBranch tests remote detection for branches
func TestGetRemoteForBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()

	t.Run("returns origin as default", func(t *testing.T) {
		repoDir := setupTestRepo(t)
		defer os.RemoveAll(repoDir)

		// Create initial commit
		writeFile(t, filepath.Join(repoDir, ".beads", "issues.jsonl"), `{"id":"test-1"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "initial")

		remote := getRemoteForBranch(ctx, repoDir, "nonexistent-branch")
		if remote != "origin" {
			t.Errorf("getRemoteForBranch() = %q, want 'origin'", remote)
		}
	})

	t.Run("returns configured remote", func(t *testing.T) {
		repoDir := setupTestRepo(t)
		defer os.RemoveAll(repoDir)

		// Create initial commit
		writeFile(t, filepath.Join(repoDir, ".beads", "issues.jsonl"), `{"id":"test-1"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "initial")

		// Configure a custom remote for a branch
		runGit(t, repoDir, "config", "branch.test-branch.remote", "upstream")

		remote := getRemoteForBranch(ctx, repoDir, "test-branch")
		if remote != "upstream" {
			t.Errorf("getRemoteForBranch() = %q, want 'upstream'", remote)
		}
	})
}

// TestGetRepoRoot tests repository root detection
func TestGetRepoRoot(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()

	t.Run("returns repo root for regular repository", func(t *testing.T) {
		repoDir := setupTestRepo(t)
		defer os.RemoveAll(repoDir)

		// Create initial commit
		writeFile(t, filepath.Join(repoDir, ".beads", "issues.jsonl"), `{"id":"test-1"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "initial")

		// Change to repo directory
		origWd, _ := os.Getwd()
		os.Chdir(repoDir)
		defer os.Chdir(origWd)

		root, err := GetRepoRoot(ctx)
		if err != nil {
			t.Fatalf("GetRepoRoot() error = %v", err)
		}

		// Resolve symlinks for comparison
		expectedRoot, _ := filepath.EvalSymlinks(repoDir)
		actualRoot, _ := filepath.EvalSymlinks(root)

		if actualRoot != expectedRoot {
			t.Errorf("GetRepoRoot() = %q, want %q", actualRoot, expectedRoot)
		}
	})

	t.Run("returns error for non-git directory", func(t *testing.T) {
		tmpDir, _ := os.MkdirTemp("", "non-git-*")
		defer os.RemoveAll(tmpDir)

		origWd, _ := os.Getwd()
		os.Chdir(tmpDir)
		defer os.Chdir(origWd)

		_, err := GetRepoRoot(ctx)
		if err == nil {
			t.Error("GetRepoRoot() expected error for non-git directory")
		}
	})

	t.Run("returns repo root from subdirectory", func(t *testing.T) {
		repoDir := setupTestRepo(t)
		defer os.RemoveAll(repoDir)

		// Create initial commit
		writeFile(t, filepath.Join(repoDir, ".beads", "issues.jsonl"), `{"id":"test-1"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "initial")

		// Create and change to subdirectory
		subDir := filepath.Join(repoDir, "subdir", "nested")
		os.MkdirAll(subDir, 0750)

		origWd, _ := os.Getwd()
		os.Chdir(subDir)
		defer os.Chdir(origWd)

		root, err := GetRepoRoot(ctx)
		if err != nil {
			t.Fatalf("GetRepoRoot() error = %v", err)
		}

		// Resolve symlinks for comparison
		expectedRoot, _ := filepath.EvalSymlinks(repoDir)
		actualRoot, _ := filepath.EvalSymlinks(root)

		if actualRoot != expectedRoot {
			t.Errorf("GetRepoRoot() from subdirectory = %q, want %q", actualRoot, expectedRoot)
		}
	})

	t.Run("handles worktree correctly", func(t *testing.T) {
		// Create main repo
		mainRepoDir := setupTestRepo(t)
		defer os.RemoveAll(mainRepoDir)

		// Create initial commit
		writeFile(t, filepath.Join(mainRepoDir, ".beads", "issues.jsonl"), `{"id":"test-1"}`)
		runGit(t, mainRepoDir, "add", ".")
		runGit(t, mainRepoDir, "commit", "-m", "initial")

		// Create a worktree
		worktreeDir, _ := os.MkdirTemp("", "test-worktree-*")
		defer os.RemoveAll(worktreeDir)
		runGit(t, mainRepoDir, "worktree", "add", worktreeDir, "-b", "feature")

		// Test from worktree - should return main repo root
		origWd, _ := os.Getwd()
		os.Chdir(worktreeDir)
		defer os.Chdir(origWd)

		root, err := GetRepoRoot(ctx)
		if err != nil {
			t.Fatalf("GetRepoRoot() from worktree error = %v", err)
		}

		// Should return the main repo root, not the worktree
		expectedRoot, _ := filepath.EvalSymlinks(mainRepoDir)
		actualRoot, _ := filepath.EvalSymlinks(root)

		if actualRoot != expectedRoot {
			t.Errorf("GetRepoRoot() from worktree = %q, want main repo %q", actualRoot, expectedRoot)
		}
	})
}

// TestHasGitRemote tests remote detection
func TestHasGitRemote(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()

	t.Run("returns false for repo without remote", func(t *testing.T) {
		repoDir := setupTestRepo(t)
		defer os.RemoveAll(repoDir)

		// Create initial commit
		writeFile(t, filepath.Join(repoDir, ".beads", "issues.jsonl"), `{"id":"test-1"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "initial")

		origWd, _ := os.Getwd()
		os.Chdir(repoDir)
		defer os.Chdir(origWd)

		if HasGitRemote(ctx) {
			t.Error("HasGitRemote() = true for repo without remote, want false")
		}
	})

	t.Run("returns true for repo with remote", func(t *testing.T) {
		repoDir := setupTestRepo(t)
		defer os.RemoveAll(repoDir)

		// Create initial commit
		writeFile(t, filepath.Join(repoDir, ".beads", "issues.jsonl"), `{"id":"test-1"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "initial")

		// Add a remote
		runGit(t, repoDir, "remote", "add", "origin", "https://github.com/test/repo.git")

		origWd, _ := os.Getwd()
		os.Chdir(repoDir)
		defer os.Chdir(origWd)

		if !HasGitRemote(ctx) {
			t.Error("HasGitRemote() = false for repo with remote, want true")
		}
	})

	t.Run("returns false for non-git directory", func(t *testing.T) {
		tmpDir, _ := os.MkdirTemp("", "non-git-*")
		defer os.RemoveAll(tmpDir)

		origWd, _ := os.Getwd()
		os.Chdir(tmpDir)
		defer os.Chdir(origWd)

		if HasGitRemote(ctx) {
			t.Error("HasGitRemote() = true for non-git directory, want false")
		}
	})
}

// TestGetCurrentBranch tests current branch detection
func TestGetCurrentBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()

	t.Run("returns current branch name", func(t *testing.T) {
		repoDir := setupTestRepo(t)
		defer os.RemoveAll(repoDir)

		// Create initial commit
		writeFile(t, filepath.Join(repoDir, ".beads", "issues.jsonl"), `{"id":"test-1"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "initial")

		origWd, _ := os.Getwd()
		os.Chdir(repoDir)
		defer os.Chdir(origWd)

		branch, err := GetCurrentBranch(ctx)
		if err != nil {
			t.Fatalf("GetCurrentBranch() error = %v", err)
		}

		// The default branch is usually "master" or "main" depending on git config
		if branch != "master" && branch != "main" {
			// Could also be a user-defined default, just verify it's not empty
			if branch == "" {
				t.Error("GetCurrentBranch() returned empty string")
			}
		}
	})

	t.Run("returns correct branch after checkout", func(t *testing.T) {
		repoDir := setupTestRepo(t)
		defer os.RemoveAll(repoDir)

		// Create initial commit
		writeFile(t, filepath.Join(repoDir, ".beads", "issues.jsonl"), `{"id":"test-1"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "initial")

		// Create and checkout new branch
		runGit(t, repoDir, "checkout", "-b", "feature-branch")

		origWd, _ := os.Getwd()
		os.Chdir(repoDir)
		defer os.Chdir(origWd)

		branch, err := GetCurrentBranch(ctx)
		if err != nil {
			t.Fatalf("GetCurrentBranch() error = %v", err)
		}

		if branch != "feature-branch" {
			t.Errorf("GetCurrentBranch() = %q, want 'feature-branch'", branch)
		}
	})
}

// TestFormatVanishedIssues tests the forensic logging formatter
func TestFormatVanishedIssues(t *testing.T) {
	t.Run("formats vanished issues correctly", func(t *testing.T) {
		localIssues := map[string]issueSummary{
			"bd-1": {ID: "bd-1", Title: "First Issue"},
			"bd-2": {ID: "bd-2", Title: "Second Issue"},
			"bd-3": {ID: "bd-3", Title: "Third Issue"},
		}
		mergedIssues := map[string]issueSummary{
			"bd-1": {ID: "bd-1", Title: "First Issue"},
		}

		lines := formatVanishedIssues(localIssues, mergedIssues, 3, 1)

		// Should contain header
		found := false
		for _, line := range lines {
			if strings.Contains(line, "Mass deletion forensic log") {
				found = true
				break
			}
		}
		if !found {
			t.Error("formatVanishedIssues() missing header")
		}

		// Should list vanished issues
		foundBd2 := false
		foundBd3 := false
		for _, line := range lines {
			if strings.Contains(line, "bd-2") {
				foundBd2 = true
			}
			if strings.Contains(line, "bd-3") {
				foundBd3 = true
			}
		}
		if !foundBd2 || !foundBd3 {
			t.Errorf("formatVanishedIssues() missing vanished issues: bd-2=%v, bd-3=%v", foundBd2, foundBd3)
		}

		// Should show totals
		foundTotal := false
		for _, line := range lines {
			if strings.Contains(line, "Total vanished: 2") {
				foundTotal = true
				break
			}
		}
		if !foundTotal {
			t.Error("formatVanishedIssues() missing total count")
		}
	})

	t.Run("truncates long titles", func(t *testing.T) {
		longTitle := strings.Repeat("A", 100)
		localIssues := map[string]issueSummary{
			"bd-1": {ID: "bd-1", Title: longTitle},
		}
		mergedIssues := map[string]issueSummary{}

		lines := formatVanishedIssues(localIssues, mergedIssues, 1, 0)

		// Find the line with bd-1 and check title is truncated
		for _, line := range lines {
			if strings.Contains(line, "bd-1") {
				if len(line) > 80 { // Line should be reasonably short
					// Verify it ends with "..."
					if !strings.Contains(line, "...") {
						t.Error("formatVanishedIssues() should truncate long titles with '...'")
					}
				}
				break
			}
		}
	})
}

// TestCheckDivergence tests the public CheckDivergence function
func TestCheckDivergence(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()

	t.Run("returns no divergence when remote does not exist", func(t *testing.T) {
		repoDir := setupTestRepo(t)
		defer os.RemoveAll(repoDir)

		// Create initial commit
		writeFile(t, filepath.Join(repoDir, ".beads", "issues.jsonl"), `{"id":"test-1"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "initial")

		// Add remote but don't create the branch on it
		runGit(t, repoDir, "remote", "add", "origin", repoDir) // Use self as remote

		info, err := CheckDivergence(ctx, repoDir, "beads-sync")
		if err != nil {
			// Expected to fail since remote branch doesn't exist
			return
		}

		// If it succeeds, verify no divergence
		if info.IsDiverged {
			t.Error("CheckDivergence() should not report divergence when remote doesn't exist")
		}
	})
}

// helper to run git with error handling (already exists but needed for this file)
func runGitHelper(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, output)
	}
}
