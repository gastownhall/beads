package syncbranch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInitThenSyncWorkflow tests the end-to-end wiring:
// EnsureWorktree creates the worktree (bd init --branch), then
// CommitToSyncBranch commits to it (bd sync). This is the exact
// integration path that was broken before the wiring fix.
func TestInitThenSyncWorkflow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()

	t.Run("init creates worktree then sync commits to it", func(t *testing.T) {
		repoDir := setupTestRepoWithRemote(t)
		defer os.RemoveAll(repoDir)

		syncBranch := "beads-sync"
		jsonlPath := filepath.Join(repoDir, ".beads", "issues.jsonl")

		// Initial commit on main
		writeFile(t, jsonlPath, `{"id":"test-1"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "initial")

		// Step 1: EnsureWorktree (simulates bd init --branch)
		origEnv := os.Getenv(EnvVar)
		os.Setenv(EnvVar, syncBranch)
		defer func() {
			if origEnv != "" {
				os.Setenv(EnvVar, origEnv)
			} else {
				os.Unsetenv(EnvVar)
			}
		}()

		origWd, _ := os.Getwd()
		os.Chdir(repoDir)
		defer os.Chdir(origWd)

		wtPath, err := EnsureWorktree(ctx)
		if err != nil {
			t.Fatalf("EnsureWorktree() error = %v", err)
		}
		if wtPath == "" {
			t.Fatal("EnsureWorktree() returned empty path")
		}

		// Step 2: Write updated JSONL (simulates creating an issue)
		writeFile(t, jsonlPath, `{"id":"test-1"}`+"\n"+`{"id":"test-2"}`)

		// Step 3: CommitToSyncBranch (simulates bd sync)
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

		// Step 4: Verify commit exists on sync branch
		output := getGitOutput(t, repoDir, "log", syncBranch, "--oneline", "-1")
		if !strings.Contains(output, "bd sync:") {
			t.Errorf("sync branch HEAD commit = %q, want to contain 'bd sync:'", strings.TrimSpace(output))
		}

		// Step 5: Verify main branch is unaffected
		mainLog := getGitOutput(t, repoDir, "log", "main", "--oneline")
		if strings.Contains(mainLog, "bd sync:") {
			t.Error("sync commit leaked to main branch")
		}
	})

	t.Run("hook mode commits without push", func(t *testing.T) {
		repoDir := setupTestRepoWithRemote(t)
		defer os.RemoveAll(repoDir)

		syncBranch := "beads-sync"
		jsonlPath := filepath.Join(repoDir, ".beads", "issues.jsonl")

		writeFile(t, jsonlPath, `{"id":"hook-1"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "initial")

		origEnv := os.Getenv(EnvVar)
		os.Setenv(EnvVar, syncBranch)
		defer func() {
			if origEnv != "" {
				os.Setenv(EnvVar, origEnv)
			} else {
				os.Unsetenv(EnvVar)
			}
		}()

		origWd, _ := os.Getwd()
		os.Chdir(repoDir)
		defer os.Chdir(origWd)

		// Create worktree
		if _, err := EnsureWorktree(ctx); err != nil {
			t.Fatalf("EnsureWorktree() error = %v", err)
		}

		// Simulate hook: export JSONL then commit with push=false
		writeFile(t, jsonlPath, `{"id":"hook-1"}`+"\n"+`{"id":"hook-2"}`)

		result, err := CommitToSyncBranch(ctx, repoDir, syncBranch, jsonlPath, false)
		if err != nil {
			t.Fatalf("CommitToSyncBranch(push=false) error = %v", err)
		}
		if !result.Committed {
			t.Error("CommitToSyncBranch(push=false) Committed = false, want true")
		}
		if result.Pushed {
			t.Error("CommitToSyncBranch(push=false) Pushed = true, want false")
		}
	})

	t.Run("multiple syncs accumulate commits on sync branch", func(t *testing.T) {
		repoDir := setupTestRepoWithRemote(t)
		defer os.RemoveAll(repoDir)

		syncBranch := "beads-sync"
		jsonlPath := filepath.Join(repoDir, ".beads", "issues.jsonl")

		writeFile(t, jsonlPath, `{"id":"iter-1"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "initial")

		origEnv := os.Getenv(EnvVar)
		os.Setenv(EnvVar, syncBranch)
		defer func() {
			if origEnv != "" {
				os.Setenv(EnvVar, origEnv)
			} else {
				os.Unsetenv(EnvVar)
			}
		}()

		origWd, _ := os.Getwd()
		os.Chdir(repoDir)
		defer os.Chdir(origWd)

		if _, err := EnsureWorktree(ctx); err != nil {
			t.Fatalf("EnsureWorktree() error = %v", err)
		}

		// First sync
		writeFile(t, jsonlPath, `{"id":"iter-1"}`+"\n"+`{"id":"iter-2"}`)
		r1, err := CommitToSyncBranch(ctx, repoDir, syncBranch, jsonlPath, false)
		if err != nil {
			t.Fatalf("first CommitToSyncBranch() error = %v", err)
		}
		if !r1.Committed {
			t.Error("first sync: Committed = false")
		}

		// Second sync with more changes
		writeFile(t, jsonlPath, `{"id":"iter-1"}`+"\n"+`{"id":"iter-2"}`+"\n"+`{"id":"iter-3"}`)
		r2, err := CommitToSyncBranch(ctx, repoDir, syncBranch, jsonlPath, false)
		if err != nil {
			t.Fatalf("second CommitToSyncBranch() error = %v", err)
		}
		if !r2.Committed {
			t.Error("second sync: Committed = false")
		}

		// Verify both commits are on the sync branch
		logOutput := getGitOutput(t, repoDir, "log", syncBranch, "--oneline")
		syncCommits := 0
		for _, line := range strings.Split(logOutput, "\n") {
			if strings.Contains(line, "bd sync:") {
				syncCommits++
			}
		}
		if syncCommits < 2 {
			t.Errorf("expected at least 2 sync commits on branch, got %d\n%s", syncCommits, logOutput)
		}
	})
}
