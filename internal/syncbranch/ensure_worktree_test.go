package syncbranch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestEnsureWorktree tests the EnsureWorktree function which creates the sync
// branch worktree if sync-branch is configured.
func TestEnsureWorktree(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()

	t.Run("returns empty when sync-branch not configured", func(t *testing.T) {
		// Create a regular git repository without sync-branch configured
		tmpDir := t.TempDir()
		setupGitRepo(t, tmpDir)

		// Change to the repo directory
		oldWd, _ := os.Getwd()
		defer os.Chdir(oldWd)
		os.Chdir(tmpDir)

		// Ensure no sync-branch is configured
		os.Unsetenv("BEADS_SYNC_BRANCH")

		path, err := EnsureWorktree(ctx)
		if err != nil {
			t.Fatalf("EnsureWorktree failed: %v", err)
		}
		if path != "" {
			t.Errorf("Expected empty path when sync-branch not configured, got %q", path)
		}
	})

	t.Run("creates worktree when sync-branch configured via env", func(t *testing.T) {
		// Create a regular git repository
		tmpDir := t.TempDir()
		setupGitRepo(t, tmpDir)

		// Change to the repo directory
		oldWd, _ := os.Getwd()
		defer os.Chdir(oldWd)
		os.Chdir(tmpDir)

		// Set sync-branch via environment variable
		os.Setenv("BEADS_SYNC_BRANCH", "beads-sync")
		defer os.Unsetenv("BEADS_SYNC_BRANCH")

		path, err := EnsureWorktree(ctx)
		if err != nil {
			t.Fatalf("EnsureWorktree failed: %v", err)
		}

		if path == "" {
			t.Fatal("Expected non-empty worktree path")
		}

		// Verify worktree was created
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("Worktree directory was not created at %s", path)
		}

		// Verify it's a valid git worktree
		cmd := exec.Command("git", "-C", path, "rev-parse", "--is-inside-work-tree")
		output, err := cmd.Output()
		if err != nil || string(output) != "true\n" {
			t.Errorf("Created path is not a valid git worktree")
		}
	})

	t.Run("creates worktree with existing remote branch", func(t *testing.T) {
		// Create a "remote" repository with beads-sync branch
		remoteDir := t.TempDir()
		setupGitRepo(t, remoteDir)

		// Create beads-sync branch in "remote"
		runCmd(t, remoteDir, "git", "checkout", "-b", "beads-sync")
		beadsDir := filepath.Join(remoteDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatalf("Failed to create .beads directory: %v", err)
		}
		issuesFile := filepath.Join(beadsDir, "issues.jsonl")
		if err := os.WriteFile(issuesFile, []byte(`{"id":"TEST-001","title":"Test issue"}`+"\n"), 0644); err != nil {
			t.Fatalf("Failed to write issues.jsonl: %v", err)
		}
		runCmd(t, remoteDir, "git", "add", ".")
		runCmd(t, remoteDir, "git", "commit", "-m", "add beads")
		runCmd(t, remoteDir, "git", "checkout", "master")

		// Clone the "remote" to create our local repo
		tmpDir := t.TempDir()
		localDir := filepath.Join(tmpDir, "local")
		runCmd(t, tmpDir, "git", "clone", remoteDir, localDir)
		runCmd(t, localDir, "git", "config", "user.email", "test@test.com")
		runCmd(t, localDir, "git", "config", "user.name", "Test User")

		// Change to the local repo directory
		oldWd, _ := os.Getwd()
		defer os.Chdir(oldWd)
		os.Chdir(localDir)

		// Set sync-branch via environment variable
		os.Setenv("BEADS_SYNC_BRANCH", "beads-sync")
		defer os.Unsetenv("BEADS_SYNC_BRANCH")

		path, err := EnsureWorktree(ctx)
		if err != nil {
			t.Fatalf("EnsureWorktree failed: %v", err)
		}

		if path == "" {
			t.Fatal("Expected non-empty worktree path")
		}

		// Verify worktree was created
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("Worktree directory was not created at %s", path)
		}

		// Verify the worktree has the beads-sync content
		worktreeIssuesPath := filepath.Join(path, ".beads", "issues.jsonl")
		if _, err := os.Stat(worktreeIssuesPath); os.IsNotExist(err) {
			t.Errorf("Expected issues.jsonl in worktree at %s", worktreeIssuesPath)
		}
	})

	t.Run("is idempotent - second call returns same path", func(t *testing.T) {
		// Create a regular git repository
		tmpDir := t.TempDir()
		setupGitRepo(t, tmpDir)

		// Change to the repo directory
		oldWd, _ := os.Getwd()
		defer os.Chdir(oldWd)
		os.Chdir(tmpDir)

		// Set sync-branch via environment variable
		os.Setenv("BEADS_SYNC_BRANCH", "beads-sync")
		defer os.Unsetenv("BEADS_SYNC_BRANCH")

		// First call
		path1, err := EnsureWorktree(ctx)
		if err != nil {
			t.Fatalf("First EnsureWorktree failed: %v", err)
		}

		// Second call should return same path without error
		path2, err := EnsureWorktree(ctx)
		if err != nil {
			t.Fatalf("Second EnsureWorktree failed: %v", err)
		}

		if path1 != path2 {
			t.Errorf("Expected same path on second call, got %q and %q", path1, path2)
		}
	})

	t.Run("returns empty when not in git repo", func(t *testing.T) {
		// Create a non-git directory
		tmpDir := t.TempDir()

		// Change to the directory
		oldWd, _ := os.Getwd()
		defer os.Chdir(oldWd)
		os.Chdir(tmpDir)

		// Set sync-branch
		os.Setenv("BEADS_SYNC_BRANCH", "beads-sync")
		defer os.Unsetenv("BEADS_SYNC_BRANCH")

		path, err := EnsureWorktree(ctx)
		// Should return empty without error (graceful handling of non-git dir)
		if err != nil {
			t.Fatalf("EnsureWorktree should not error for non-git dir: %v", err)
		}
		if path != "" {
			t.Errorf("Expected empty path for non-git dir, got %q", path)
		}
	})
}

// setupGitRepo creates a git repository with an initial commit
func setupGitRepo(t *testing.T, dir string) {
	t.Helper()

	runCmd(t, dir, "git", "init")
	runCmd(t, dir, "git", "config", "user.email", "test@test.com")
	runCmd(t, dir, "git", "config", "user.name", "Test User")

	// Create initial commit (required for worktree creation)
	testFile := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}
	runCmd(t, dir, "git", "add", ".")
	runCmd(t, dir, "git", "commit", "-m", "initial")
}

// runCmd runs a command in the given directory
func runCmd(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Command %s %v failed: %v\nOutput: %s", name, args, err, output)
	}
}

// TestFreshCloneScenario simulates the exact scenario that was broken:
// 1. Remote repo has main branch with stale .beads/issues.jsonl (2 issues)
// 2. Remote repo has beads-sync branch with current issues (47 issues)
// 3. Fresh clone checks out main
// 4. EnsureWorktree should create worktree pointing to beads-sync
// 5. findJSONLPath (via getBeadsWorktreePath) should return worktree path
func TestFreshCloneScenario(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()

	// Create a "remote" repository simulating the broken state
	remoteDir := t.TempDir()
	setupGitRepo(t, remoteDir)

	// Create stale issues on main (simulating the broken state)
	beadsDir := filepath.Join(remoteDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads directory: %v", err)
	}
	staleIssues := `{"id":"STALE-001","title":"Stale issue 1"}
{"id":"STALE-002","title":"Stale issue 2"}
`
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(staleIssues), 0644); err != nil {
		t.Fatalf("Failed to write stale issues: %v", err)
	}
	// Add config.yaml with sync-branch
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("sync-branch: beads-sync\n"), 0644); err != nil {
		t.Fatalf("Failed to write config.yaml: %v", err)
	}
	runCmd(t, remoteDir, "git", "add", ".")
	runCmd(t, remoteDir, "git", "commit", "-m", "add stale beads on main")

	// Create beads-sync branch with current issues
	runCmd(t, remoteDir, "git", "checkout", "-b", "beads-sync")
	currentIssues := `{"id":"CURRENT-001","title":"Current issue 1"}
{"id":"CURRENT-002","title":"Current issue 2"}
{"id":"CURRENT-003","title":"Current issue 3"}
`
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(currentIssues), 0644); err != nil {
		t.Fatalf("Failed to write current issues: %v", err)
	}
	runCmd(t, remoteDir, "git", "add", ".")
	runCmd(t, remoteDir, "git", "commit", "-m", "add current beads on beads-sync")

	// Go back to master on remote
	runCmd(t, remoteDir, "git", "checkout", "master")

	// Clone to simulate fresh clone (gets main branch)
	tmpDir := t.TempDir()
	localDir := filepath.Join(tmpDir, "local")
	runCmd(t, tmpDir, "git", "clone", remoteDir, localDir)
	runCmd(t, localDir, "git", "config", "user.email", "test@test.com")
	runCmd(t, localDir, "git", "config", "user.name", "Test User")

	// Verify we're on master with stale issues
	mainIssues, _ := os.ReadFile(filepath.Join(localDir, ".beads", "issues.jsonl"))
	if !contains(string(mainIssues), "STALE-001") {
		t.Fatalf("Expected stale issues on main, got: %s", mainIssues)
	}
	if contains(string(mainIssues), "CURRENT-001") {
		t.Fatalf("Should not have current issues on main")
	}

	// Change to the local repo
	oldWd, _ := os.Getwd()
	defer os.Chdir(oldWd)
	os.Chdir(localDir)

	// Set sync-branch via env (simulates what config.yaml would do)
	os.Setenv("BEADS_SYNC_BRANCH", "beads-sync")
	defer os.Unsetenv("BEADS_SYNC_BRANCH")

	// THIS IS THE FIX: EnsureWorktree creates the worktree
	worktreePath, err := EnsureWorktree(ctx)
	if err != nil {
		t.Fatalf("EnsureWorktree failed: %v", err)
	}
	if worktreePath == "" {
		t.Fatal("Expected worktree path to be returned")
	}

	// Verify the worktree exists and has current issues
	worktreeIssues, err := os.ReadFile(filepath.Join(worktreePath, ".beads", "issues.jsonl"))
	if err != nil {
		t.Fatalf("Failed to read worktree issues: %v", err)
	}
	if !contains(string(worktreeIssues), "CURRENT-001") {
		t.Errorf("Expected current issues in worktree, got: %s", worktreeIssues)
	}
	if contains(string(worktreeIssues), "STALE-001") {
		t.Errorf("Should not have stale issues in worktree")
	}

	// Verify getBeadsWorktreePath returns the worktree path
	// Note: On macOS, /var is a symlink to /private/var, so we need to resolve
	// symlinks before comparing paths
	repoRoot := localDir
	gotPath := getBeadsWorktreePath(ctx, repoRoot, "beads-sync")
	gotPathResolved, _ := filepath.EvalSymlinks(gotPath)
	worktreePathResolved, _ := filepath.EvalSymlinks(worktreePath)
	if gotPathResolved != worktreePathResolved {
		t.Errorf("getBeadsWorktreePath returned %q (resolved: %q), expected %q (resolved: %q)",
			gotPath, gotPathResolved, worktreePath, worktreePathResolved)
	}

	t.Logf("Fresh clone scenario test passed: worktree created at %s with current issues", worktreePath)
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsAt(s, substr, 0))
}

func containsAt(s, substr string, start int) bool {
	for i := start; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
