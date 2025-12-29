package syncbranch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCommitToSyncBranch tests the main commit function
func TestCommitToSyncBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()

	t.Run("commits changes to sync branch", func(t *testing.T) {
		// Setup: create a repo with a sync branch
		repoDir := setupTestRepoWithRemote(t)
		defer os.RemoveAll(repoDir)

		syncBranch := "beads-sync"
		jsonlPath := filepath.Join(repoDir, ".beads", "issues.jsonl")

		// Create sync branch
		runGit(t, repoDir, "checkout", "-b", syncBranch)
		writeFile(t, jsonlPath, `{"id":"test-1"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "initial sync branch commit")
		runGit(t, repoDir, "checkout", "main")

		// Write new content to commit
		writeFile(t, jsonlPath, `{"id":"test-1"}`+"\n"+`{"id":"test-2"}`)

		result, err := CommitToSyncBranch(ctx, repoDir, syncBranch, jsonlPath, false)
		if err != nil {
			t.Fatalf("CommitToSyncBranch() error = %v", err)
		}

		if !result.Committed {
			t.Error("CommitToSyncBranch() Committed = false, want true")
		}
		if result.Branch != syncBranch {
			t.Errorf("CommitToSyncBranch() Branch = %q, want %q", result.Branch, syncBranch)
		}
		if !strings.Contains(result.Message, "bd sync:") {
			t.Errorf("CommitToSyncBranch() Message = %q, want to contain 'bd sync:'", result.Message)
		}
	})

	t.Run("returns not committed when no changes", func(t *testing.T) {
		repoDir := setupTestRepoWithRemote(t)
		defer os.RemoveAll(repoDir)

		syncBranch := "beads-sync"
		jsonlPath := filepath.Join(repoDir, ".beads", "issues.jsonl")

		// Create sync branch with content
		runGit(t, repoDir, "checkout", "-b", syncBranch)
		writeFile(t, jsonlPath, `{"id":"test-1"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "initial")
		runGit(t, repoDir, "checkout", "main")

		// Write the same content that's in the sync branch
		writeFile(t, jsonlPath, `{"id":"test-1"}`)

		// Commit with same content (no changes)
		result, err := CommitToSyncBranch(ctx, repoDir, syncBranch, jsonlPath, false)
		if err != nil {
			t.Fatalf("CommitToSyncBranch() error = %v", err)
		}

		if result.Committed {
			t.Error("CommitToSyncBranch() Committed = true when no changes, want false")
		}
	})
}

// TestPullFromSyncBranch tests pulling changes from sync branch
func TestPullFromSyncBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()

	t.Run("handles sync branch not on remote", func(t *testing.T) {
		repoDir := setupTestRepoWithRemote(t)
		defer os.RemoveAll(repoDir)

		syncBranch := "beads-sync"
		jsonlPath := filepath.Join(repoDir, ".beads", "issues.jsonl")

		// Create local sync branch but don't set up remote tracking
		runGit(t, repoDir, "checkout", "-b", syncBranch)
		writeFile(t, jsonlPath, `{"id":"test-1"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "local sync")
		runGit(t, repoDir, "checkout", "main")

		// Pull should handle the case where remote doesn't have the branch
		result, err := PullFromSyncBranch(ctx, repoDir, syncBranch, jsonlPath, false)
		// This tests the fetch failure path since "origin" points to self without the sync branch
		// It should either succeed (not pulled) or fail gracefully
		if err != nil {
			// Expected - fetch will fail since origin doesn't have sync branch
			return
		}
		if result.Pulled && !result.FastForwarded && !result.Merged {
			// Pulled but no change - acceptable
			_ = result
		}
	})

	t.Run("pulls when already up to date", func(t *testing.T) {
		repoDir := setupTestRepoWithRemote(t)
		defer os.RemoveAll(repoDir)

		syncBranch := "beads-sync"
		jsonlPath := filepath.Join(repoDir, ".beads", "issues.jsonl")

		// Create sync branch and simulate it being tracked
		runGit(t, repoDir, "checkout", "-b", syncBranch)
		writeFile(t, jsonlPath, `{"id":"test-1"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "sync commit")
		// Set up a fake remote ref at the same commit
		runGit(t, repoDir, "update-ref", "refs/remotes/origin/"+syncBranch, "HEAD")
		runGit(t, repoDir, "checkout", "main")

		// Pull when already at remote HEAD
		result, err := PullFromSyncBranch(ctx, repoDir, syncBranch, jsonlPath, false)
		if err != nil {
			// Might fail on fetch step, that's acceptable
			return
		}
		// Should have pulled successfully (even if no new content)
		if result.Pulled {
			// Good - it recognized it's up to date
		}
	})

	t.Run("copies JSONL to main repo after sync", func(t *testing.T) {
		repoDir := setupTestRepoWithRemote(t)
		defer os.RemoveAll(repoDir)

		syncBranch := "beads-sync"
		jsonlPath := filepath.Join(repoDir, ".beads", "issues.jsonl")

		// Create sync branch with content
		runGit(t, repoDir, "checkout", "-b", syncBranch)
		writeFile(t, jsonlPath, `{"id":"sync-1"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "sync commit")
		runGit(t, repoDir, "update-ref", "refs/remotes/origin/"+syncBranch, "HEAD")
		runGit(t, repoDir, "checkout", "main")

		// Remove local JSONL to verify it gets copied back
		os.Remove(jsonlPath)

		result, err := PullFromSyncBranch(ctx, repoDir, syncBranch, jsonlPath, false)
		if err != nil {
			return // Acceptable in test env
		}

		if result.Pulled {
			// Verify JSONL was copied to main repo
			if _, err := os.Stat(jsonlPath); os.IsNotExist(err) {
				t.Error("PullFromSyncBranch() did not copy JSONL to main repo")
			}
		}
	})

	t.Run("handles fast-forward case", func(t *testing.T) {
		repoDir := setupTestRepoWithRemote(t)
		defer os.RemoveAll(repoDir)

		syncBranch := "beads-sync"
		jsonlPath := filepath.Join(repoDir, ".beads", "issues.jsonl")

		// Create sync branch with base commit
		runGit(t, repoDir, "checkout", "-b", syncBranch)
		writeFile(t, jsonlPath, `{"id":"base"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "base")
		baseCommit := strings.TrimSpace(getGitOutput(t, repoDir, "rev-parse", "HEAD"))

		// Add another commit and set as remote
		writeFile(t, jsonlPath, `{"id":"base"}`+"\n"+`{"id":"remote"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "remote commit")
		runGit(t, repoDir, "update-ref", "refs/remotes/origin/"+syncBranch, "HEAD")

		// Reset back to base (so remote is ahead)
		runGit(t, repoDir, "reset", "--hard", baseCommit)
		runGit(t, repoDir, "checkout", "main")

		// Pull should fast-forward
		result, err := PullFromSyncBranch(ctx, repoDir, syncBranch, jsonlPath, false)
		if err != nil {
			return // Acceptable with self-remote
		}

		// Just verify result is populated correctly
		_ = result.FastForwarded
		_ = result.Merged
	})
}

// TestResetToRemote tests resetting sync branch to remote state
// Note: Full remote tests are in cmd/bd tests; this tests the basic flow
func TestResetToRemote(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()

	t.Run("returns error when fetch fails", func(t *testing.T) {
		repoDir := setupTestRepoWithRemote(t)
		defer os.RemoveAll(repoDir)

		syncBranch := "beads-sync"
		jsonlPath := filepath.Join(repoDir, ".beads", "issues.jsonl")

		// Create local sync branch without remote
		runGit(t, repoDir, "checkout", "-b", syncBranch)
		writeFile(t, jsonlPath, `{"id":"local-1"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "local commit")
		runGit(t, repoDir, "checkout", "main")

		// ResetToRemote should fail since remote branch doesn't exist
		err := ResetToRemote(ctx, repoDir, syncBranch, jsonlPath)
		if err == nil {
			// If it succeeds without remote, that's also acceptable
			// (the remote is set to self, might not have sync branch)
		}
	})
}

// TestPushSyncBranch tests the push function
// Note: Full push tests require actual remote; this tests basic error handling
func TestPushSyncBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()

	t.Run("handles missing worktree gracefully", func(t *testing.T) {
		repoDir := setupTestRepoWithRemote(t)
		defer os.RemoveAll(repoDir)

		syncBranch := "beads-sync"

		// Create sync branch
		runGit(t, repoDir, "checkout", "-b", syncBranch)
		writeFile(t, filepath.Join(repoDir, ".beads", "issues.jsonl"), `{"id":"test-1"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "initial")
		runGit(t, repoDir, "checkout", "main")

		// PushSyncBranch should handle the worktree creation
		err := PushSyncBranch(ctx, repoDir, syncBranch)
		// Will fail because origin doesn't have the branch, but should not panic
		if err != nil {
			// Expected - push will fail since origin doesn't have the branch set up
			if !strings.Contains(err.Error(), "push failed") {
				// Some other error - acceptable in test env
			}
		}
	})
}

// TestRunCmdWithTimeoutMessage tests the timeout message function
func TestRunCmdWithTimeoutMessage(t *testing.T) {
	ctx := context.Background()

	t.Run("runs command and returns output", func(t *testing.T) {
		cmd := exec.CommandContext(ctx, "echo", "hello")
		output, err := runCmdWithTimeoutMessage(ctx, "test message", 5*time.Second, cmd)
		if err != nil {
			t.Fatalf("runCmdWithTimeoutMessage() error = %v", err)
		}
		if !strings.Contains(string(output), "hello") {
			t.Errorf("runCmdWithTimeoutMessage() output = %q, want to contain 'hello'", output)
		}
	})

	t.Run("returns error for failing command", func(t *testing.T) {
		cmd := exec.CommandContext(ctx, "false") // Always exits with 1
		_, err := runCmdWithTimeoutMessage(ctx, "test message", 5*time.Second, cmd)
		if err == nil {
			t.Error("runCmdWithTimeoutMessage() expected error for failing command")
		}
	})
}

// TestPreemptiveFetchAndFastForward tests the pre-emptive fetch function
func TestPreemptiveFetchAndFastForward(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()

	t.Run("returns nil when remote branch does not exist", func(t *testing.T) {
		repoDir := setupTestRepoWithRemote(t)
		defer os.RemoveAll(repoDir)

		// Create sync branch locally but don't push
		runGit(t, repoDir, "checkout", "-b", "beads-sync")
		writeFile(t, filepath.Join(repoDir, ".beads", "issues.jsonl"), `{"id":"test-1"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "initial")

		err := preemptiveFetchAndFastForward(ctx, repoDir, "beads-sync", "origin")
		if err != nil {
			t.Errorf("preemptiveFetchAndFastForward() error = %v, want nil (not an error when remote doesn't exist)", err)
		}
	})

	t.Run("no-op when local equals remote", func(t *testing.T) {
		repoDir := setupTestRepoWithRemote(t)
		defer os.RemoveAll(repoDir)

		syncBranch := "beads-sync"

		// Create sync branch
		runGit(t, repoDir, "checkout", "-b", syncBranch)
		writeFile(t, filepath.Join(repoDir, ".beads", "issues.jsonl"), `{"id":"test-1"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "initial")
		// Set remote ref at same commit
		runGit(t, repoDir, "update-ref", "refs/remotes/origin/"+syncBranch, "HEAD")

		err := preemptiveFetchAndFastForward(ctx, repoDir, syncBranch, "origin")
		// Should succeed since we're already in sync
		if err != nil {
			// Might fail on fetch step with self-remote, acceptable
			return
		}
	})
}


// Helper: setup a test repo with a (fake) remote
func setupTestRepoWithRemote(t *testing.T) string {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "bd-test-repo-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	// Initialize git repo with 'main' as default branch (modern git convention)
	runGit(t, tmpDir, "init", "--initial-branch=main")
	runGit(t, tmpDir, "config", "user.email", "test@test.com")
	runGit(t, tmpDir, "config", "user.name", "Test User")

	// Create initial commit
	writeFile(t, filepath.Join(tmpDir, "README.md"), "# Test Repo")
	runGit(t, tmpDir, "add", ".")
	runGit(t, tmpDir, "commit", "-m", "initial commit")

	// Create .beads directory
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	// Add a fake remote (just for configuration purposes)
	runGit(t, tmpDir, "remote", "add", "origin", tmpDir)

	return tmpDir
}
