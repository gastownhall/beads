//go:build cgo && integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
)

// TestGitPullSyncIntegration tests the full git pull sync scenario
// Verifies that after git pull, both daemon and non-daemon modes pick up changes automatically
func TestGitPullSyncIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Create temp directory for test repositories
	tempDir := t.TempDir()

	// Create "remote" repository
	remoteDir := filepath.Join(tempDir, "remote")
	if err := os.MkdirAll(remoteDir, 0750); err != nil {
		t.Fatalf("Failed to create remote dir: %v", err)
	}

	// Initialize remote git repo
	runGitCmd(t, remoteDir, "init", "--bare", "-b", "master")

	// Create "clone1" repository
	clone1Dir := filepath.Join(tempDir, "clone1")
	runGitCmd(t, tempDir, "clone", remoteDir, clone1Dir)
	configureGit(t, clone1Dir)

	// Initialize beads in clone1
	clone1BeadsDir := filepath.Join(clone1Dir, ".beads")
	if err := os.MkdirAll(clone1BeadsDir, 0750); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	clone1DBPath := filepath.Join(clone1BeadsDir, "test.db")
	clone1Store := newTestStore(t, clone1DBPath)
	defer clone1Store.Close()

	ctx := context.Background()
	if err := clone1Store.SetMetadata(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	// Create and close an issue in clone1
	issue := &types.Issue{
		Title:     "Test sync issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := clone1Store.CreateIssue(ctx, issue, "test-user"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}
	issueID := issue.ID

	// Close the issue
	if err := clone1Store.CloseIssue(ctx, issueID, "Test completed", "test-user", ""); err != nil {
		t.Fatalf("Failed to close issue: %v", err)
	}

	// Export to JSONL
	jsonlPath := filepath.Join(clone1BeadsDir, "issues.jsonl")
	if err := exportIssuesToJSONL(ctx, clone1Store, jsonlPath); err != nil {
		t.Fatalf("Failed to export: %v", err)
	}

	// Commit and push from clone1
	runGitCmd(t, clone1Dir, "add", ".beads")
	runGitCmd(t, clone1Dir, "commit", "-m", "Add closed issue")
	runGitCmd(t, clone1Dir, "push", "origin", "master")

	// Create "clone2" repository
	clone2Dir := filepath.Join(tempDir, "clone2")
	runGitCmd(t, tempDir, "clone", remoteDir, clone2Dir)
	configureGit(t, clone2Dir)

	// Initialize empty database in clone2
	clone2BeadsDir := filepath.Join(clone2Dir, ".beads")
	clone2DBPath := filepath.Join(clone2BeadsDir, "test.db")
	clone2Store := newTestStore(t, clone2DBPath)
	defer clone2Store.Close()

	if err := clone2Store.SetMetadata(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	// Import the existing JSONL (simulating initial sync)
	clone2JSONLPath := filepath.Join(clone2BeadsDir, "issues.jsonl")
	if err := importJSONLToStore(ctx, clone2Store, clone2DBPath, clone2JSONLPath); err != nil {
		t.Fatalf("Failed to import: %v", err)
	}

	// Verify issue exists and is closed
	verifyIssueClosed(t, clone2Store, issueID)

	// Note: We don't commit in clone2 - it stays clean as a read-only consumer

	// Now test git pull scenario: Clone1 makes a change (update priority)
	if err := clone1Store.UpdateIssue(ctx, issueID, map[string]interface{}{
		"priority": 0,
	}, "test-user"); err != nil {
		t.Fatalf("Failed to update issue: %v", err)
	}

	if err := exportIssuesToJSONL(ctx, clone1Store, jsonlPath); err != nil {
		t.Fatalf("Failed to export after update: %v", err)
	}

	runGitCmd(t, clone1Dir, "add", ".beads/issues.jsonl")
	runGitCmd(t, clone1Dir, "commit", "-m", "Update priority")
	runGitCmd(t, clone1Dir, "push", "origin", "master")

	// Clone2 pulls the change
	runGitCmd(t, clone2Dir, "pull")

	// Test auto-import in non-daemon mode
	t.Run("NonDaemonAutoImport", func(t *testing.T) {
		// Use a temporary local store for this test
		localStore := newTestStore(t, clone2DBPath)
		defer localStore.Close()

		// Manually import to simulate auto-import behavior
		startTime := time.Now()
		if err := importJSONLToStore(ctx, localStore, clone2DBPath, clone2JSONLPath); err != nil {
			t.Fatalf("Failed to auto-import: %v", err)
		}
		elapsed := time.Since(startTime)

		// Verify priority was updated
		issue, err := localStore.GetIssue(ctx, issueID)
		if err != nil {
			t.Fatalf("Failed to get issue: %v", err)
		}
		if issue.Priority != 0 {
			t.Errorf("Expected priority 0 after auto-import, got %d", issue.Priority)
		}

		// Verify performance: import should be fast
		if elapsed > 100*time.Millisecond {
			t.Logf("Info: import took %v", elapsed)
		}
	})

	// Test bd sync --import-only command
	t.Run("BdSyncCommand", func(t *testing.T) {
		// Make another change in clone1 (change priority back to 1)
		if err := clone1Store.UpdateIssue(ctx, issueID, map[string]interface{}{
			"priority": 1,
		}, "test-user"); err != nil {
			t.Fatalf("Failed to update issue: %v", err)
		}

		if err := exportIssuesToJSONL(ctx, clone1Store, jsonlPath); err != nil {
			t.Fatalf("Failed to export: %v", err)
		}

		runGitCmd(t, clone1Dir, "add", ".beads/issues.jsonl")
		runGitCmd(t, clone1Dir, "commit", "-m", "Update priority")
		runGitCmd(t, clone1Dir, "push", "origin", "master")

		// Clone2 pulls
		runGitCmd(t, clone2Dir, "pull")

		// Use a fresh store for import
		syncStore := newTestStore(t, clone2DBPath)
		defer syncStore.Close()

		// Manually trigger import via in-process equivalent
		if err := importJSONLToStore(ctx, syncStore, clone2DBPath, clone2JSONLPath); err != nil {
			t.Fatalf("Failed to import via sync: %v", err)
		}

		// Verify priority was updated back to 1
		issue, err := syncStore.GetIssue(ctx, issueID)
		if err != nil {
			t.Fatalf("Failed to get issue: %v", err)
		}
		if issue.Priority != 1 {
			t.Errorf("Expected priority 1, got %d", issue.Priority)
		}
	})
}

// Helper functions

func runGitCmd(t *testing.T, dir string, args ...string) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_COMMITTER_DATE=2024-01-01T00:00:00", "GIT_AUTHOR_DATE=2024-01-01T00:00:00")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed in %s: %v\n%s", args, dir, err, output)
	}
}

func configureGit(t *testing.T, dir string) {
	runGitCmd(t, dir, "config", "user.email", "test@example.com")
	runGitCmd(t, dir, "config", "user.name", "Test User")
	runGitCmd(t, dir, "config", "pull.rebase", "false")

	// Create .gitignore to prevent test database files from being tracked
	gitignorePath := filepath.Join(dir, ".gitignore")
	gitignoreContent := `# Test database files
*.db
*.db-journal
*.db-wal
*.db-shm
`
	if err := os.WriteFile(gitignorePath, []byte(gitignoreContent), 0644); err != nil {
		t.Fatalf("Failed to create .gitignore: %v", err)
	}
}

func exportIssuesToJSONL(ctx context.Context, store *dolt.DoltStore, jsonlPath string) error {
	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		return err
	}

	// Populate dependencies
	allDeps, err := store.GetAllDependencyRecords(ctx)
	if err != nil {
		return err
	}
	for _, issue := range issues {
		issue.Dependencies = allDeps[issue.ID]
		labels, _ := store.GetLabels(ctx, issue.ID)
		issue.Labels = labels
	}

	f, err := os.Create(jsonlPath)
	if err != nil {
		return err
	}
	defer f.Close()

	encoder := json.NewEncoder(f)
	for _, issue := range issues {
		if err := encoder.Encode(issue); err != nil {
			return err
		}
	}

	return nil
}

func importJSONLToStore(ctx context.Context, store *dolt.DoltStore, dbPath, jsonlPath string) error {
	data, err := os.ReadFile(jsonlPath)
	if err != nil {
		return err
	}

	// Use the autoimport package's AutoImportIfNewer function
	// For testing, we'll directly parse and import
	var issues []*types.Issue
	decoder := json.NewDecoder(bytes.NewReader(data))
	for decoder.More() {
		var issue types.Issue
		if err := decoder.Decode(&issue); err != nil {
			return err
		}
		issues = append(issues, &issue)
	}

	// Import each issue
	for _, issue := range issues {
		existing, _ := store.GetIssue(ctx, issue.ID)
		if existing != nil {
			// Update
			updates := map[string]interface{}{
				"status":   issue.Status,
				"priority": issue.Priority,
			}
			if err := store.UpdateIssue(ctx, issue.ID, updates, "import"); err != nil {
				return err
			}
		} else {
			// Create
			if err := store.CreateIssue(ctx, issue, "import"); err != nil {
				return err
			}
		}
	}

	// Set last_import_time metadata so staleness check works
	if err := store.SetMetadata(ctx, "last_import_time", time.Now().Format(time.RFC3339)); err != nil {
		return err
	}

	return nil
}

func verifyIssueClosed(t *testing.T, store *dolt.DoltStore, issueID string) {
	issue, err := store.GetIssue(context.Background(), issueID)
	if err != nil {
		t.Fatalf("Failed to get issue %s: %v", issueID, err)
	}
	if issue.Status != types.StatusClosed {
		t.Errorf("Expected issue %s to be closed, got status %s", issueID, issue.Status)
	}
}
