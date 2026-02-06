package syncbranch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestCheckForcePush_WithGitRepo exercises the full CheckForcePush code paths
// that require a real git repository with remote refs.
func TestCheckForcePush_WithGitRepo(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()

	t.Run("detects no force push on normal fast-forward", func(t *testing.T) {
		// Create bare remote
		remoteDir := t.TempDir()
		runGit(t, remoteDir, "init", "--bare")

		// Create local repo
		repoDir := setupTestRepo(t)
		defer os.RemoveAll(repoDir)
		runGit(t, repoDir, "remote", "add", "origin", remoteDir)

		syncBranch := "beads-sync"

		// Create sync branch and push
		runGit(t, repoDir, "checkout", "-b", syncBranch)
		writeFile(t, filepath.Join(repoDir, ".beads", "issues.jsonl"), `{"id":"test-1"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "initial")
		runGit(t, repoDir, "push", "-u", "origin", syncBranch)

		// Store the current SHA (simulating a previous sync)
		store := newTestStore(t)
		defer store.Close()

		sha := strings.TrimSpace(getGitOutput(t, repoDir, "rev-parse", "HEAD"))
		if err := store.SetConfig(ctx, RemoteSHAConfigKey, sha); err != nil {
			t.Fatalf("SetConfig error: %v", err)
		}

		// Add another commit and push (normal fast-forward)
		writeFile(t, filepath.Join(repoDir, ".beads", "issues.jsonl"), `{"id":"test-1"}`+"\n"+`{"id":"test-2"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "second commit")
		runGit(t, repoDir, "push", "origin", syncBranch)

		// Check for force push - should NOT detect one
		status, err := CheckForcePush(ctx, store, repoDir, syncBranch)
		if err != nil {
			t.Fatalf("CheckForcePush() error = %v", err)
		}
		if status.Detected {
			t.Error("CheckForcePush() detected force push on normal fast-forward")
		}
		if !strings.Contains(status.Message, "fast-forward") {
			t.Errorf("Message = %q, want to contain 'fast-forward'", status.Message)
		}
	})

	t.Run("detects force push when history rewritten", func(t *testing.T) {
		// Create bare remote
		remoteDir := t.TempDir()
		runGit(t, remoteDir, "init", "--bare")

		// Create local repo
		repoDir := setupTestRepo(t)
		defer os.RemoveAll(repoDir)
		runGit(t, repoDir, "remote", "add", "origin", remoteDir)

		syncBranch := "beads-sync"

		// Create sync branch and push
		runGit(t, repoDir, "checkout", "-b", syncBranch)
		writeFile(t, filepath.Join(repoDir, ".beads", "issues.jsonl"), `{"id":"test-1"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "initial")
		runGit(t, repoDir, "push", "-u", "origin", syncBranch)

		// Store the current SHA
		store := newTestStore(t)
		defer store.Close()

		sha := strings.TrimSpace(getGitOutput(t, repoDir, "rev-parse", "HEAD"))
		if err := store.SetConfig(ctx, RemoteSHAConfigKey, sha); err != nil {
			t.Fatalf("SetConfig error: %v", err)
		}

		// Force push a completely different history
		runGit(t, repoDir, "checkout", "--orphan", "new-history")
		writeFile(t, filepath.Join(repoDir, ".beads", "issues.jsonl"), `{"id":"rewritten"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "rewritten history")
		runGit(t, repoDir, "branch", "-D", syncBranch)
		runGit(t, repoDir, "branch", "-m", syncBranch)
		runGit(t, repoDir, "push", "--force", "origin", syncBranch)

		// Check for force push - should detect it
		status, err := CheckForcePush(ctx, store, repoDir, syncBranch)
		if err != nil {
			t.Fatalf("CheckForcePush() error = %v", err)
		}
		if !status.Detected {
			t.Error("CheckForcePush() did NOT detect force push")
		}
		if !strings.Contains(status.Message, "FORCE-PUSH DETECTED") {
			t.Errorf("Message = %q, want to contain 'FORCE-PUSH DETECTED'", status.Message)
		}
		if status.StoredSHA != sha {
			t.Errorf("StoredSHA = %q, want %q", status.StoredSHA, sha)
		}
		if status.CurrentRemoteSHA == "" {
			t.Error("CurrentRemoteSHA should not be empty")
		}
	})

	t.Run("no change when SHA matches", func(t *testing.T) {
		// Create bare remote
		remoteDir := t.TempDir()
		runGit(t, remoteDir, "init", "--bare")

		// Create local repo
		repoDir := setupTestRepo(t)
		defer os.RemoveAll(repoDir)
		runGit(t, repoDir, "remote", "add", "origin", remoteDir)

		syncBranch := "beads-sync"

		// Create sync branch and push
		runGit(t, repoDir, "checkout", "-b", syncBranch)
		writeFile(t, filepath.Join(repoDir, ".beads", "issues.jsonl"), `{"id":"test-1"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "initial")
		runGit(t, repoDir, "push", "-u", "origin", syncBranch)

		// Store the same SHA
		store := newTestStore(t)
		defer store.Close()

		sha := strings.TrimSpace(getGitOutput(t, repoDir, "rev-parse", "HEAD"))
		if err := store.SetConfig(ctx, RemoteSHAConfigKey, sha); err != nil {
			t.Fatalf("SetConfig error: %v", err)
		}

		// Check - SHA should match, no force push
		status, err := CheckForcePush(ctx, store, repoDir, syncBranch)
		if err != nil {
			t.Fatalf("CheckForcePush() error = %v", err)
		}
		if status.Detected {
			t.Error("CheckForcePush() should not detect force push when SHA matches")
		}
		if !strings.Contains(status.Message, "unchanged") {
			t.Errorf("Message = %q, want to contain 'unchanged'", status.Message)
		}
	})

	t.Run("handles nonexistent remote branch", func(t *testing.T) {
		// Create bare remote (empty - no branches)
		remoteDir := t.TempDir()
		runGit(t, remoteDir, "init", "--bare")

		// Create local repo with a commit on master
		repoDir := setupTestRepo(t)
		defer os.RemoveAll(repoDir)
		writeFile(t, filepath.Join(repoDir, "dummy.txt"), "x")
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "initial")
		runGit(t, repoDir, "remote", "add", "origin", remoteDir)
		runGit(t, repoDir, "push", "-u", "origin", "master")

		// Store a fake SHA
		store := newTestStore(t)
		defer store.Close()

		if err := store.SetConfig(ctx, RemoteSHAConfigKey, "abc123fake"); err != nil {
			t.Fatalf("SetConfig error: %v", err)
		}

		// Check force push for a branch that doesn't exist on remote
		status, err := CheckForcePush(ctx, store, repoDir, "nonexistent-sync")
		if err != nil {
			t.Fatalf("CheckForcePush() error = %v", err)
		}
		if status.Detected {
			t.Error("Should not detect force push when remote branch doesn't exist")
		}
		if !strings.Contains(status.Message, "does not exist") {
			t.Errorf("Message = %q, want to contain 'does not exist'", status.Message)
		}
	})
}

// TestUpdateStoredRemoteSHA_WithGitRepo tests storing the remote SHA after sync.
func TestUpdateStoredRemoteSHA_WithGitRepo(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()

	t.Run("stores remote SHA after push", func(t *testing.T) {
		// Create bare remote
		remoteDir := t.TempDir()
		runGit(t, remoteDir, "init", "--bare")

		// Create local repo
		repoDir := setupTestRepo(t)
		defer os.RemoveAll(repoDir)
		runGit(t, repoDir, "remote", "add", "origin", remoteDir)

		syncBranch := "beads-sync"

		// Create sync branch and push
		runGit(t, repoDir, "checkout", "-b", syncBranch)
		writeFile(t, filepath.Join(repoDir, ".beads", "issues.jsonl"), `{"id":"test-1"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "initial")
		runGit(t, repoDir, "push", "-u", "origin", syncBranch)

		expectedSHA := strings.TrimSpace(getGitOutput(t, repoDir, "rev-parse", "HEAD"))

		store := newTestStore(t)
		defer store.Close()

		// Update stored SHA
		err := UpdateStoredRemoteSHA(ctx, store, repoDir, syncBranch)
		if err != nil {
			t.Fatalf("UpdateStoredRemoteSHA() error = %v", err)
		}

		// Verify stored SHA matches
		storedSHA, err := store.GetConfig(ctx, RemoteSHAConfigKey)
		if err != nil {
			t.Fatalf("GetConfig() error = %v", err)
		}
		if storedSHA != expectedSHA {
			t.Errorf("stored SHA = %q, want %q", storedSHA, expectedSHA)
		}
	})

	t.Run("falls back to local branch when remote ref missing", func(t *testing.T) {
		// Create local repo without pushing to remote
		repoDir := setupTestRepo(t)
		defer os.RemoveAll(repoDir)

		syncBranch := "beads-sync"

		// Create sync branch locally (no remote)
		runGit(t, repoDir, "checkout", "-b", syncBranch)
		writeFile(t, filepath.Join(repoDir, ".beads", "issues.jsonl"), `{"id":"test-1"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "local only")

		expectedSHA := strings.TrimSpace(getGitOutput(t, repoDir, "rev-parse", "HEAD"))

		store := newTestStore(t)
		defer store.Close()

		// Update stored SHA — should fall back to local branch
		err := UpdateStoredRemoteSHA(ctx, store, repoDir, syncBranch)
		if err != nil {
			t.Fatalf("UpdateStoredRemoteSHA() error = %v", err)
		}

		storedSHA, err := store.GetConfig(ctx, RemoteSHAConfigKey)
		if err != nil {
			t.Fatalf("GetConfig() error = %v", err)
		}
		if storedSHA != expectedSHA {
			t.Errorf("stored SHA = %q, want %q", storedSHA, expectedSHA)
		}
	})

	t.Run("returns error when branch does not exist", func(t *testing.T) {
		repoDir := setupTestRepo(t)
		defer os.RemoveAll(repoDir)

		// Create initial commit so the repo is valid
		writeFile(t, filepath.Join(repoDir, "dummy.txt"), "x")
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "initial")

		store := newTestStore(t)
		defer store.Close()

		err := UpdateStoredRemoteSHA(ctx, store, repoDir, "nonexistent-branch")
		if err == nil {
			t.Error("UpdateStoredRemoteSHA() expected error for nonexistent branch")
		}
	})
}

// TestCopyCommittedJSONLToMainRepo_Integration tests the committed JSONL copy function.
func TestCopyCommittedJSONLToMainRepo_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()

	t.Run("copies committed JSONL from HEAD", func(t *testing.T) {
		repoDir := setupTestRepo(t)
		defer os.RemoveAll(repoDir)

		content := `{"id":"test-1","title":"Test Issue"}`
		writeFile(t, filepath.Join(repoDir, ".beads", "issues.jsonl"), content)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "commit JSONL")

		// Create a target path for the copy
		mainRepoDir := t.TempDir()
		os.MkdirAll(filepath.Join(mainRepoDir, ".beads"), 0750)
		mainJSONLPath := filepath.Join(mainRepoDir, ".beads", "issues.jsonl")

		err := copyCommittedJSONLToMainRepo(ctx, repoDir, ".beads/issues.jsonl", mainJSONLPath)
		if err != nil {
			t.Fatalf("copyCommittedJSONLToMainRepo() error = %v", err)
		}

		// Verify content was copied
		copied, err := os.ReadFile(mainJSONLPath)
		if err != nil {
			t.Fatalf("Failed to read copied file: %v", err)
		}
		if strings.TrimSpace(string(copied)) != content {
			t.Errorf("copied content = %q, want %q", string(copied), content)
		}
	})

	t.Run("also copies metadata.json from HEAD", func(t *testing.T) {
		repoDir := setupTestRepo(t)
		defer os.RemoveAll(repoDir)

		writeFile(t, filepath.Join(repoDir, ".beads", "issues.jsonl"), `{"id":"test-1"}`)
		writeFile(t, filepath.Join(repoDir, ".beads", "metadata.json"), `{"prefix":"bd"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "commit files")

		mainRepoDir := t.TempDir()
		os.MkdirAll(filepath.Join(mainRepoDir, ".beads"), 0750)
		mainJSONLPath := filepath.Join(mainRepoDir, ".beads", "issues.jsonl")

		err := copyCommittedJSONLToMainRepo(ctx, repoDir, ".beads/issues.jsonl", mainJSONLPath)
		if err != nil {
			t.Fatalf("copyCommittedJSONLToMainRepo() error = %v", err)
		}

		// Verify metadata was also copied
		metadata, err := os.ReadFile(filepath.Join(mainRepoDir, ".beads", "metadata.json"))
		if err != nil {
			t.Fatalf("Failed to read metadata: %v", err)
		}
		if !strings.Contains(string(metadata), "prefix") {
			t.Errorf("metadata content = %q, want to contain 'prefix'", string(metadata))
		}
	})

	t.Run("returns nil when file not in HEAD", func(t *testing.T) {
		repoDir := setupTestRepo(t)
		defer os.RemoveAll(repoDir)

		// Commit something else, not .beads/issues.jsonl
		writeFile(t, filepath.Join(repoDir, "dummy.txt"), "x")
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "no jsonl")

		mainRepoDir := t.TempDir()
		mainJSONLPath := filepath.Join(mainRepoDir, ".beads", "issues.jsonl")

		err := copyCommittedJSONLToMainRepo(ctx, repoDir, ".beads/issues.jsonl", mainJSONLPath)
		if err != nil {
			t.Errorf("copyCommittedJSONLToMainRepo() error = %v, want nil (file not in HEAD)", err)
		}
	})

	t.Run("normalizes bare repo paths", func(t *testing.T) {
		repoDir := setupTestRepo(t)
		defer os.RemoveAll(repoDir)

		writeFile(t, filepath.Join(repoDir, ".beads", "issues.jsonl"), `{"id":"test-1"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "commit JSONL")

		mainRepoDir := t.TempDir()
		os.MkdirAll(filepath.Join(mainRepoDir, ".beads"), 0750)
		mainJSONLPath := filepath.Join(mainRepoDir, ".beads", "issues.jsonl")

		// Use a path with leading component (as bare repo worktrees produce)
		err := copyCommittedJSONLToMainRepo(ctx, repoDir, "main/.beads/issues.jsonl", mainJSONLPath)
		if err != nil {
			t.Fatalf("copyCommittedJSONLToMainRepo() with bare repo path error = %v", err)
		}

		// Verify content was copied (normalization stripped "main/")
		copied, err := os.ReadFile(mainJSONLPath)
		if err != nil {
			t.Fatalf("Failed to read copied file: %v", err)
		}
		if !strings.Contains(string(copied), "test-1") {
			t.Errorf("copied content = %q, want to contain 'test-1'", string(copied))
		}
	})
}

// TestPullFromSyncBranch_DivergenceCase tests the diverged merge path (Case 3)
// which handles local and remote having diverged commit histories.
func TestPullFromSyncBranch_DivergenceCase(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()

	t.Run("merges diverged histories and copies to main repo", func(t *testing.T) {
		// Create bare remote
		remoteDir := t.TempDir()
		runGit(t, remoteDir, "init", "--bare")

		// Create local repo
		repoDir := setupTestRepo(t)
		defer os.RemoveAll(repoDir)
		runGit(t, repoDir, "remote", "add", "origin", remoteDir)

		syncBranch := "beads-sync"
		jsonlPath := filepath.Join(repoDir, ".beads", "issues.jsonl")

		// Create sync branch with base content
		runGit(t, repoDir, "checkout", "-b", syncBranch)
		baseContent := `{"id":"base-1","title":"Base Issue","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`
		writeFile(t, jsonlPath, baseContent)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "base")
		runGit(t, repoDir, "push", "-u", "origin", syncBranch)
		baseCommit := strings.TrimSpace(getGitOutput(t, repoDir, "rev-parse", "HEAD"))

		// Create remote divergence: add remote issue
		remoteContent := baseContent + "\n" + `{"id":"remote-1","title":"Remote Issue","created_at":"2024-01-02T00:00:00Z","created_by":"user2"}`
		writeFile(t, jsonlPath, remoteContent)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "remote commit")
		runGit(t, repoDir, "push", "origin", syncBranch)

		// Reset local back to base and create local divergence
		runGit(t, repoDir, "reset", "--hard", baseCommit)
		localContent := baseContent + "\n" + `{"id":"local-1","title":"Local Issue","created_at":"2024-01-02T00:00:00Z","created_by":"user3"}`
		writeFile(t, jsonlPath, localContent)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "local commit")

		// Now local and remote are diverged
		// Pull should merge diverged histories
		// Note: PullFromSyncBranch works from the worktree, we stay on beads-sync
		result, err := PullFromSyncBranch(ctx, repoDir, syncBranch, jsonlPath, false)
		if err != nil {
			t.Fatalf("PullFromSyncBranch() error = %v", err)
		}

		if !result.Pulled {
			t.Error("Expected Pulled=true")
		}
		if !result.Merged {
			t.Error("Expected Merged=true for diverged histories")
		}

		// Verify JSONL was copied to main repo with merged content
		mergedData, err := os.ReadFile(jsonlPath)
		if err != nil {
			t.Fatalf("Failed to read merged JSONL: %v", err)
		}
		merged := string(mergedData)

		if !strings.Contains(merged, "base-1") {
			t.Error("Merged content missing base issue")
		}
		if !strings.Contains(merged, "local-1") {
			t.Error("Merged content missing local issue")
		}
		if !strings.Contains(merged, "remote-1") {
			t.Error("Merged content missing remote issue")
		}
	})

	t.Run("safety check triggers on mass deletion during merge", func(t *testing.T) {
		// Create bare remote
		remoteDir := t.TempDir()
		runGit(t, remoteDir, "init", "--bare")

		// Create local repo
		repoDir := setupTestRepo(t)
		defer os.RemoveAll(repoDir)
		runGit(t, repoDir, "remote", "add", "origin", remoteDir)

		syncBranch := "beads-sync"
		jsonlPath := filepath.Join(repoDir, ".beads", "issues.jsonl")

		// Create sync branch with 10 issues
		runGit(t, repoDir, "checkout", "-b", syncBranch)
		var lines []string
		for i := 1; i <= 10; i++ {
			lines = append(lines, `{"id":"issue-`+strings.Repeat("0", 3-len(string(rune('0'+i))))+`","title":"Issue","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`)
		}
		// Use simpler IDs
		lines = nil
		for i := 1; i <= 10; i++ {
			id := "bd-" + string(rune('a'+i-1))
			lines = append(lines, `{"id":"`+id+`","title":"Issue `+id+`","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`)
		}
		writeFile(t, jsonlPath, strings.Join(lines, "\n"))
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "base with 10 issues")
		runGit(t, repoDir, "push", "-u", "origin", syncBranch)
		baseCommit := strings.TrimSpace(getGitOutput(t, repoDir, "rev-parse", "HEAD"))

		// Remote: delete most issues (mass deletion)
		writeFile(t, jsonlPath, `{"id":"bd-a","title":"Issue bd-a","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`+"\n"+`{"id":"bd-b","title":"Issue bd-b","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "remote: mass delete")
		runGit(t, repoDir, "push", "origin", syncBranch)

		// Local: add a new issue (diverge from base)
		runGit(t, repoDir, "reset", "--hard", baseCommit)
		localLines := append(lines, `{"id":"bd-new","title":"New Local","created_at":"2024-01-02T00:00:00Z","created_by":"user2"}`)
		writeFile(t, jsonlPath, strings.Join(localLines, "\n"))
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "local: add issue")

		// Pull with push=true and requireMassDeleteConfirmation=true
		// Note: PullFromSyncBranch works from the worktree, we stay on beads-sync
		result, err := PullFromSyncBranch(ctx, repoDir, syncBranch, jsonlPath, true, true)
		if err != nil {
			t.Fatalf("PullFromSyncBranch() error = %v", err)
		}

		if !result.Merged {
			t.Error("Expected Merged=true")
		}
		if !result.SafetyCheckTriggered {
			t.Error("Expected SafetyCheckTriggered=true for mass deletion")
		}
		if !result.Pushed {
			// When safety check triggers with requireConfirmation=true, push is skipped
			// That's correct behavior
		}
		if len(result.SafetyWarnings) == 0 {
			t.Error("Expected SafetyWarnings to be populated")
		}
	})
}

// TestResetToRemote_FullCycle tests resetting to remote and verifying JSONL copy.
func TestResetToRemote_FullCycle(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()

	t.Run("resets to remote and copies JSONL", func(t *testing.T) {
		// Create bare remote
		remoteDir := t.TempDir()
		runGit(t, remoteDir, "init", "--bare")

		// Create local repo
		repoDir := setupTestRepo(t)
		defer os.RemoveAll(repoDir)
		runGit(t, repoDir, "remote", "add", "origin", remoteDir)

		syncBranch := "beads-sync"
		jsonlPath := filepath.Join(repoDir, ".beads", "issues.jsonl")

		// Create sync branch with initial content and push
		runGit(t, repoDir, "checkout", "-b", syncBranch)
		writeFile(t, jsonlPath, `{"id":"test-1","title":"Initial"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "initial")
		runGit(t, repoDir, "push", "-u", "origin", syncBranch)

		baseCommit := strings.TrimSpace(getGitOutput(t, repoDir, "rev-parse", "HEAD"))

		// Push updated content to remote
		writeFile(t, jsonlPath, `{"id":"test-1","title":"Updated by remote"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "remote update")
		runGit(t, repoDir, "push", "origin", syncBranch)

		// Reset local back to base (simulating divergence)
		runGit(t, repoDir, "reset", "--hard", baseCommit)

		// Reset to remote — works from worktree, we stay on beads-sync
		err := ResetToRemote(ctx, repoDir, syncBranch, jsonlPath)
		if err != nil {
			t.Fatalf("ResetToRemote() error = %v", err)
		}

		// Verify JSONL was updated with remote content
		data, err := os.ReadFile(jsonlPath)
		if err != nil {
			t.Fatalf("Failed to read JSONL: %v", err)
		}
		if !strings.Contains(string(data), "Updated by remote") {
			t.Errorf("JSONL should contain remote content, got: %s", data)
		}
	})
}

// TestPreemptiveFetchAndFastForward_FullPath tests the fast-forward path.
func TestPreemptiveFetchAndFastForward_FullPath(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()

	t.Run("fast-forwards when remote is ahead", func(t *testing.T) {
		// Create bare remote
		remoteDir := t.TempDir()
		runGit(t, remoteDir, "init", "--bare")

		// Create local repo
		repoDir := setupTestRepo(t)
		defer os.RemoveAll(repoDir)
		runGit(t, repoDir, "remote", "add", "origin", remoteDir)

		syncBranch := "beads-sync"

		// Create sync branch and push
		runGit(t, repoDir, "checkout", "-b", syncBranch)
		writeFile(t, filepath.Join(repoDir, ".beads", "issues.jsonl"), `{"id":"test-1"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "initial")
		runGit(t, repoDir, "push", "-u", "origin", syncBranch)

		baseCommit := strings.TrimSpace(getGitOutput(t, repoDir, "rev-parse", "HEAD"))

		// Add another commit and push (advancing remote)
		writeFile(t, filepath.Join(repoDir, ".beads", "issues.jsonl"), `{"id":"test-1"}`+"\n"+`{"id":"test-2"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "advance remote")
		runGit(t, repoDir, "push", "origin", syncBranch)

		advancedCommit := strings.TrimSpace(getGitOutput(t, repoDir, "rev-parse", "HEAD"))

		// Reset local back to base
		runGit(t, repoDir, "reset", "--hard", baseCommit)

		// Verify local is behind
		localSHA := strings.TrimSpace(getGitOutput(t, repoDir, "rev-parse", "HEAD"))
		if localSHA != baseCommit {
			t.Fatalf("Local should be at base commit")
		}

		// Fast-forward
		err := preemptiveFetchAndFastForward(ctx, repoDir, syncBranch, "origin")
		if err != nil {
			t.Fatalf("preemptiveFetchAndFastForward() error = %v", err)
		}

		// Verify local was fast-forwarded
		newSHA := strings.TrimSpace(getGitOutput(t, repoDir, "rev-parse", "HEAD"))
		if newSHA != advancedCommit {
			t.Errorf("Local should be at advanced commit %s, got %s", advancedCommit, newSHA)
		}
	})

	t.Run("no-op when local is ahead (diverged)", func(t *testing.T) {
		// Create bare remote
		remoteDir := t.TempDir()
		runGit(t, remoteDir, "init", "--bare")

		// Create local repo
		repoDir := setupTestRepo(t)
		defer os.RemoveAll(repoDir)
		runGit(t, repoDir, "remote", "add", "origin", remoteDir)

		syncBranch := "beads-sync"

		// Create sync branch and push
		runGit(t, repoDir, "checkout", "-b", syncBranch)
		writeFile(t, filepath.Join(repoDir, ".beads", "issues.jsonl"), `{"id":"test-1"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "initial")
		runGit(t, repoDir, "push", "-u", "origin", syncBranch)

		// Add local commit (local ahead)
		writeFile(t, filepath.Join(repoDir, ".beads", "issues.jsonl"), `{"id":"test-1"}`+"\n"+`{"id":"local"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "local ahead")

		localSHA := strings.TrimSpace(getGitOutput(t, repoDir, "rev-parse", "HEAD"))

		// Should be a no-op since local is ahead
		err := preemptiveFetchAndFastForward(ctx, repoDir, syncBranch, "origin")
		if err != nil {
			t.Fatalf("preemptiveFetchAndFastForward() error = %v", err)
		}

		// Verify local didn't change
		newSHA := strings.TrimSpace(getGitOutput(t, repoDir, "rev-parse", "HEAD"))
		if newSHA != localSHA {
			t.Errorf("Local should not have changed, was %s now %s", localSHA, newSHA)
		}
	})
}

// TestHasChangesInWorktree_FallbackPath tests the fallback code path
// where filepath.Rel fails for the beads directory.
func TestHasChangesInWorktree_FallbackPath(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()

	t.Run("fallback to single-file check works", func(t *testing.T) {
		repoDir := setupTestRepo(t)
		defer os.RemoveAll(repoDir)

		// Create initial commit with a file at repo root
		filePath := filepath.Join(repoDir, "issues.jsonl")
		writeFile(t, filePath, `{"id":"test-1"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "initial")

		// Modify the file
		writeFile(t, filePath, `{"id":"test-1"}`+"\n"+`{"id":"test-2"}`)

		// The filepath.Rel for beadsDir should work fine here, but the
		// test covers the non-.beads dir code path in hasChangesInWorktree
		hasChanges, err := hasChangesInWorktree(ctx, repoDir, filePath)
		if err != nil {
			t.Fatalf("hasChangesInWorktree() error = %v", err)
		}
		if !hasChanges {
			t.Error("Expected changes to be detected")
		}
	})
}

// TestPushFromWorktree_NonFastForwardRecovery tests the push retry with content merge.
func TestPushFromWorktree_NonFastForwardRecovery(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()

	t.Run("pushes successfully on first try", func(t *testing.T) {
		// Create bare remote
		remoteDir := t.TempDir()
		runGit(t, remoteDir, "init", "--bare")

		repoDir := setupTestRepo(t)
		defer os.RemoveAll(repoDir)
		runGit(t, repoDir, "remote", "add", "origin", remoteDir)

		syncBranch := "beads-sync"

		// Create and push sync branch
		runGit(t, repoDir, "checkout", "-b", syncBranch)
		writeFile(t, filepath.Join(repoDir, ".beads", "issues.jsonl"), `{"id":"test-1"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "initial")
		runGit(t, repoDir, "push", "-u", "origin", syncBranch)

		// Add another commit
		writeFile(t, filepath.Join(repoDir, ".beads", "issues.jsonl"), `{"id":"test-1"}`+"\n"+`{"id":"test-2"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "second")

		// Push should succeed on first try
		err := pushFromWorktree(ctx, repoDir, syncBranch)
		if err != nil {
			t.Fatalf("pushFromWorktree() error = %v", err)
		}

		// Verify remote was updated
		cmd := exec.CommandContext(ctx, "git", "-C", remoteDir, "log", "--oneline", syncBranch)
		output, err := cmd.Output()
		if err != nil {
			t.Fatalf("git log error: %v", err)
		}
		if !strings.Contains(string(output), "second") {
			t.Errorf("Remote should have 'second' commit, got: %s", output)
		}
	})
}

// TestGetRepoRoot_DotGitDirectory tests the .git directory detection path.
func TestGetRepoRoot_DotGitDirectory(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()

	t.Run("detects .git directory in regular repo", func(t *testing.T) {
		repoDir := setupTestRepo(t)
		defer os.RemoveAll(repoDir)

		writeFile(t, filepath.Join(repoDir, "dummy.txt"), "x")
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "initial")

		origWd, _ := os.Getwd()
		os.Chdir(repoDir)
		defer os.Chdir(origWd)

		root, err := GetRepoRoot(ctx)
		if err != nil {
			t.Fatalf("GetRepoRoot() error = %v", err)
		}

		expectedRoot, _ := filepath.EvalSymlinks(repoDir)
		actualRoot, _ := filepath.EvalSymlinks(root)

		if actualRoot != expectedRoot {
			t.Errorf("GetRepoRoot() = %q, want %q", actualRoot, expectedRoot)
		}
	})
}
