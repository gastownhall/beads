package syncbranch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGetDivergence tests the divergence detection function
func TestGetDivergence(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()

	t.Run("no divergence when synced", func(t *testing.T) {
		// Create a test repo with a branch
		repoDir := setupTestRepo(t)
		defer os.RemoveAll(repoDir)

		// Create and checkout test branch
		runGit(t, repoDir, "checkout", "-b", "test-branch")
		writeFile(t, filepath.Join(repoDir, ".beads", "issues.jsonl"), `{"id":"test-1"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "initial commit")

		// Simulate remote by creating a local ref
		runGit(t, repoDir, "update-ref", "refs/remotes/origin/test-branch", "HEAD")

		localAhead, remoteAhead, err := getDivergence(ctx, repoDir, "test-branch", "origin")
		if err != nil {
			t.Fatalf("getDivergence() error = %v", err)
		}
		if localAhead != 0 || remoteAhead != 0 {
			t.Errorf("getDivergence() = (%d, %d), want (0, 0)", localAhead, remoteAhead)
		}
	})

	t.Run("local ahead of remote", func(t *testing.T) {
		repoDir := setupTestRepo(t)
		defer os.RemoveAll(repoDir)

		runGit(t, repoDir, "checkout", "-b", "test-branch")
		writeFile(t, filepath.Join(repoDir, ".beads", "issues.jsonl"), `{"id":"test-1"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "initial commit")

		// Set remote ref to current HEAD
		runGit(t, repoDir, "update-ref", "refs/remotes/origin/test-branch", "HEAD")

		// Add more local commits
		writeFile(t, filepath.Join(repoDir, ".beads", "issues.jsonl"), `{"id":"test-1"}
{"id":"test-2"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "second commit")

		localAhead, remoteAhead, err := getDivergence(ctx, repoDir, "test-branch", "origin")
		if err != nil {
			t.Fatalf("getDivergence() error = %v", err)
		}
		if localAhead != 1 || remoteAhead != 0 {
			t.Errorf("getDivergence() = (%d, %d), want (1, 0)", localAhead, remoteAhead)
		}
	})

	t.Run("remote ahead of local", func(t *testing.T) {
		repoDir := setupTestRepo(t)
		defer os.RemoveAll(repoDir)

		runGit(t, repoDir, "checkout", "-b", "test-branch")
		writeFile(t, filepath.Join(repoDir, ".beads", "issues.jsonl"), `{"id":"test-1"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "initial commit")

		// Save current HEAD as "local"
		localHead := strings.TrimSpace(getGitOutput(t, repoDir, "rev-parse", "HEAD"))

		// Create more commits and set as remote
		writeFile(t, filepath.Join(repoDir, ".beads", "issues.jsonl"), `{"id":"test-1"}
{"id":"test-2"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "remote commit")
		runGit(t, repoDir, "update-ref", "refs/remotes/origin/test-branch", "HEAD")

		// Reset local to previous commit
		runGit(t, repoDir, "reset", "--hard", localHead)

		localAhead, remoteAhead, err := getDivergence(ctx, repoDir, "test-branch", "origin")
		if err != nil {
			t.Fatalf("getDivergence() error = %v", err)
		}
		if localAhead != 0 || remoteAhead != 1 {
			t.Errorf("getDivergence() = (%d, %d), want (0, 1)", localAhead, remoteAhead)
		}
	})

	t.Run("diverged histories", func(t *testing.T) {
		repoDir := setupTestRepo(t)
		defer os.RemoveAll(repoDir)

		runGit(t, repoDir, "checkout", "-b", "test-branch")
		writeFile(t, filepath.Join(repoDir, ".beads", "issues.jsonl"), `{"id":"test-1"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "base commit")

		// Save base commit
		baseCommit := strings.TrimSpace(getGitOutput(t, repoDir, "rev-parse", "HEAD"))

		// Create local commit
		writeFile(t, filepath.Join(repoDir, ".beads", "issues.jsonl"), `{"id":"test-1"}
{"id":"local-2"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "local commit")
		localHead := strings.TrimSpace(getGitOutput(t, repoDir, "rev-parse", "HEAD"))

		// Create remote commit from base
		runGit(t, repoDir, "checkout", baseCommit)
		writeFile(t, filepath.Join(repoDir, ".beads", "issues.jsonl"), `{"id":"test-1"}
{"id":"remote-2"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "remote commit")
		runGit(t, repoDir, "update-ref", "refs/remotes/origin/test-branch", "HEAD")

		// Go back to local branch
		runGit(t, repoDir, "checkout", "-B", "test-branch", localHead)

		localAhead, remoteAhead, err := getDivergence(ctx, repoDir, "test-branch", "origin")
		if err != nil {
			t.Fatalf("getDivergence() error = %v", err)
		}
		if localAhead != 1 || remoteAhead != 1 {
			t.Errorf("getDivergence() = (%d, %d), want (1, 1)", localAhead, remoteAhead)
		}
	})
}

// TestExtractJSONLFromCommit tests extracting JSONL content from git commits
func TestExtractJSONLFromCommit(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()

	t.Run("extracts file from HEAD", func(t *testing.T) {
		repoDir := setupTestRepo(t)
		defer os.RemoveAll(repoDir)

		content := `{"id":"test-1","title":"Test Issue"}`
		writeFile(t, filepath.Join(repoDir, ".beads", "issues.jsonl"), content)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "test commit")

		extracted, err := extractJSONLFromCommit(ctx, repoDir, "HEAD", ".beads/issues.jsonl")
		if err != nil {
			t.Fatalf("extractJSONLFromCommit() error = %v", err)
		}
		if strings.TrimSpace(string(extracted)) != content {
			t.Errorf("extractJSONLFromCommit() = %q, want %q", extracted, content)
		}
	})

	t.Run("extracts file from specific commit", func(t *testing.T) {
		repoDir := setupTestRepo(t)
		defer os.RemoveAll(repoDir)

		// First commit
		content1 := `{"id":"test-1"}`
		writeFile(t, filepath.Join(repoDir, ".beads", "issues.jsonl"), content1)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "first commit")
		firstCommit := strings.TrimSpace(getGitOutput(t, repoDir, "rev-parse", "HEAD"))

		// Second commit
		content2 := `{"id":"test-1"}
{"id":"test-2"}`
		writeFile(t, filepath.Join(repoDir, ".beads", "issues.jsonl"), content2)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "second commit")

		// Extract from first commit
		extracted, err := extractJSONLFromCommit(ctx, repoDir, firstCommit, ".beads/issues.jsonl")
		if err != nil {
			t.Fatalf("extractJSONLFromCommit() error = %v", err)
		}
		if strings.TrimSpace(string(extracted)) != content1 {
			t.Errorf("extractJSONLFromCommit() = %q, want %q", extracted, content1)
		}
	})

	t.Run("returns error for nonexistent file", func(t *testing.T) {
		repoDir := setupTestRepo(t)
		defer os.RemoveAll(repoDir)

		writeFile(t, filepath.Join(repoDir, "dummy.txt"), "dummy")
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "test commit")

		_, err := extractJSONLFromCommit(ctx, repoDir, "HEAD", ".beads/issues.jsonl")
		if err == nil {
			t.Error("extractJSONLFromCommit() expected error for nonexistent file")
		}
	})
}

// TestPerformContentMerge tests the content-based merge function
func TestPerformContentMerge(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()

	t.Run("merges diverged content", func(t *testing.T) {
		repoDir := setupTestRepo(t)
		defer os.RemoveAll(repoDir)

		runGit(t, repoDir, "checkout", "-b", "test-branch")

		// Base content
		baseContent := `{"id":"test-1","title":"Base","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`
		writeFile(t, filepath.Join(repoDir, ".beads", "issues.jsonl"), baseContent)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "base commit")
		baseCommit := strings.TrimSpace(getGitOutput(t, repoDir, "rev-parse", "HEAD"))

		// Create local changes (add issue)
		localContent := `{"id":"test-1","title":"Base","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}
{"id":"local-1","title":"Local Issue","created_at":"2024-01-02T00:00:00Z","created_by":"user1"}`
		writeFile(t, filepath.Join(repoDir, ".beads", "issues.jsonl"), localContent)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "local commit")
		localHead := strings.TrimSpace(getGitOutput(t, repoDir, "rev-parse", "HEAD"))

		// Create remote changes from base (add different issue)
		runGit(t, repoDir, "checkout", baseCommit)
		remoteContent := `{"id":"test-1","title":"Base","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}
{"id":"remote-1","title":"Remote Issue","created_at":"2024-01-02T00:00:00Z","created_by":"user2"}`
		writeFile(t, filepath.Join(repoDir, ".beads", "issues.jsonl"), remoteContent)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "remote commit")
		runGit(t, repoDir, "update-ref", "refs/remotes/origin/test-branch", "HEAD")

		// Go back to local
		runGit(t, repoDir, "checkout", "-B", "test-branch", localHead)

		// Perform merge
		merged, err := performContentMerge(ctx, repoDir, "test-branch", "origin", ".beads/issues.jsonl")
		if err != nil {
			t.Fatalf("performContentMerge() error = %v", err)
		}

		// Check that merged content contains all three issues
		mergedStr := string(merged)
		if !strings.Contains(mergedStr, "test-1") {
			t.Error("merged content missing base issue test-1")
		}
		if !strings.Contains(mergedStr, "local-1") {
			t.Error("merged content missing local issue local-1")
		}
		if !strings.Contains(mergedStr, "remote-1") {
			t.Error("merged content missing remote issue remote-1")
		}
	})

	t.Run("handles deletion correctly", func(t *testing.T) {
		repoDir := setupTestRepo(t)
		defer os.RemoveAll(repoDir)

		runGit(t, repoDir, "checkout", "-b", "test-branch")

		// Base content with two issues
		baseContent := `{"id":"test-1","title":"Issue 1","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}
{"id":"test-2","title":"Issue 2","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`
		writeFile(t, filepath.Join(repoDir, ".beads", "issues.jsonl"), baseContent)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "base commit")
		baseCommit := strings.TrimSpace(getGitOutput(t, repoDir, "rev-parse", "HEAD"))

		// Local keeps both
		localHead := baseCommit

		// Remote deletes test-2
		runGit(t, repoDir, "checkout", baseCommit)
		remoteContent := `{"id":"test-1","title":"Issue 1","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`
		writeFile(t, filepath.Join(repoDir, ".beads", "issues.jsonl"), remoteContent)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "remote delete")
		runGit(t, repoDir, "update-ref", "refs/remotes/origin/test-branch", "HEAD")

		// Go back to local
		runGit(t, repoDir, "checkout", "-B", "test-branch", localHead)

		// Perform merge
		merged, err := performContentMerge(ctx, repoDir, "test-branch", "origin", ".beads/issues.jsonl")
		if err != nil {
			t.Fatalf("performContentMerge() error = %v", err)
		}

		// Deletion should win - test-2 should be gone
		mergedStr := string(merged)
		if !strings.Contains(mergedStr, "test-1") {
			t.Error("merged content missing issue test-1")
		}
		if strings.Contains(mergedStr, "test-2") {
			t.Error("merged content still contains deleted issue test-2")
		}
	})
}

// TestPerformDeletionsMerge tests the deletions merge function
func TestPerformDeletionsMerge(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()

	t.Run("merges deletions from both sides", func(t *testing.T) {
		repoDir := setupTestRepo(t)
		defer os.RemoveAll(repoDir)

		runGit(t, repoDir, "checkout", "-b", "test-branch")

		// Base: no deletions
		writeFile(t, filepath.Join(repoDir, ".beads", "issues.jsonl"), `{"id":"test-1"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "base commit")
		baseCommit := strings.TrimSpace(getGitOutput(t, repoDir, "rev-parse", "HEAD"))

		// Local: delete issue-A
		writeFile(t, filepath.Join(repoDir, ".beads", "deletions.jsonl"), `{"id":"issue-A","deleted_at":"2024-01-01T00:00:00Z"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "local deletion")
		localHead := strings.TrimSpace(getGitOutput(t, repoDir, "rev-parse", "HEAD"))

		// Remote: delete issue-B
		runGit(t, repoDir, "checkout", baseCommit)
		writeFile(t, filepath.Join(repoDir, ".beads", "deletions.jsonl"), `{"id":"issue-B","deleted_at":"2024-01-02T00:00:00Z"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "remote deletion")
		runGit(t, repoDir, "update-ref", "refs/remotes/origin/test-branch", "HEAD")

		// Go back to local
		runGit(t, repoDir, "checkout", "-B", "test-branch", localHead)

		// Perform merge
		merged, err := performDeletionsMerge(ctx, repoDir, "test-branch", "origin", ".beads/deletions.jsonl")
		if err != nil {
			t.Fatalf("performDeletionsMerge() error = %v", err)
		}

		// Both deletions should be present
		mergedStr := string(merged)
		if !strings.Contains(mergedStr, "issue-A") {
			t.Error("merged deletions missing issue-A")
		}
		if !strings.Contains(mergedStr, "issue-B") {
			t.Error("merged deletions missing issue-B")
		}
	})

	t.Run("handles only local deletions", func(t *testing.T) {
		repoDir := setupTestRepo(t)
		defer os.RemoveAll(repoDir)

		runGit(t, repoDir, "checkout", "-b", "test-branch")

		// Base: no deletions
		writeFile(t, filepath.Join(repoDir, ".beads", "issues.jsonl"), `{"id":"test-1"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "base commit")
		baseCommit := strings.TrimSpace(getGitOutput(t, repoDir, "rev-parse", "HEAD"))

		// Local: has deletions
		writeFile(t, filepath.Join(repoDir, ".beads", "deletions.jsonl"), `{"id":"issue-A"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "local deletion")
		localHead := strings.TrimSpace(getGitOutput(t, repoDir, "rev-parse", "HEAD"))

		// Remote: no deletions file
		runGit(t, repoDir, "checkout", baseCommit)
		runGit(t, repoDir, "update-ref", "refs/remotes/origin/test-branch", "HEAD")

		// Go back to local
		runGit(t, repoDir, "checkout", "-B", "test-branch", localHead)

		// Perform merge
		merged, err := performDeletionsMerge(ctx, repoDir, "test-branch", "origin", ".beads/deletions.jsonl")
		if err != nil {
			t.Fatalf("performDeletionsMerge() error = %v", err)
		}

		// Local deletions should be present
		if !strings.Contains(string(merged), "issue-A") {
			t.Error("merged deletions missing issue-A")
		}
	})
}

// Helper functions

func setupTestRepo(t *testing.T) string {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "bd-test-repo-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	// Initialize git repo
	runGit(t, tmpDir, "init")
	runGit(t, tmpDir, "config", "user.email", "test@test.com")
	runGit(t, tmpDir, "config", "user.name", "Test User")

	// Create .beads directory
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	return tmpDir
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, output)
	}
}

func getGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v failed: %v", args, err)
	}
	return string(output)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0750); err != nil {
		t.Fatalf("Failed to create directory %s: %v", dir, err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write file %s: %v", path, err)
	}
}

// TestCountIssuesInContent tests the issue counting helper function (bd-7ch)
func TestCountIssuesInContent(t *testing.T) {
	tests := []struct {
		name    string
		content []byte
		want    int
	}{
		{
			name:    "empty content",
			content: []byte{},
			want:    0,
		},
		{
			name:    "nil content",
			content: nil,
			want:    0,
		},
		{
			name:    "single issue",
			content: []byte(`{"id":"test-1"}`),
			want:    1,
		},
		{
			name:    "multiple issues",
			content: []byte(`{"id":"test-1"}` + "\n" + `{"id":"test-2"}` + "\n" + `{"id":"test-3"}`),
			want:    3,
		},
		{
			name:    "trailing newline",
			content: []byte(`{"id":"test-1"}` + "\n" + `{"id":"test-2"}` + "\n"),
			want:    2,
		},
		{
			name:    "empty lines ignored",
			content: []byte(`{"id":"test-1"}` + "\n" + "\n" + `{"id":"test-2"}` + "\n" + "   " + "\n"),
			want:    2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := countIssuesInContent(tt.content)
			if got != tt.want {
				t.Errorf("countIssuesInContent() = %d, want %d", got, tt.want)
			}
		})
	}
}
