package syncbranch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

		// 3-way merge removed: performContentMerge now returns remote content (remote wins)
		mergedStr := string(merged)
		if !strings.Contains(mergedStr, "test-1") {
			t.Error("merged content missing base issue test-1")
		}
		if !strings.Contains(mergedStr, "remote-1") {
			t.Error("merged content missing remote issue remote-1")
		}
		// local-1 is NOT expected since remote-wins doesn't include local-only issues
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

// TestParseIssuesFromContent tests the issue parsing helper function (bd-lsa)
func TestParseIssuesFromContent(t *testing.T) {
	tests := []struct {
		name    string
		content []byte
		wantIDs []string
	}{
		{
			name:    "empty content",
			content: []byte{},
			wantIDs: []string{},
		},
		{
			name:    "nil content",
			content: nil,
			wantIDs: []string{},
		},
		{
			name:    "single issue",
			content: []byte(`{"id":"test-1","title":"Test Issue"}`),
			wantIDs: []string{"test-1"},
		},
		{
			name:    "multiple issues",
			content: []byte(`{"id":"test-1","title":"Issue 1"}` + "\n" + `{"id":"test-2","title":"Issue 2"}`),
			wantIDs: []string{"test-1", "test-2"},
		},
		{
			name:    "malformed line skipped",
			content: []byte(`{"id":"test-1","title":"Valid"}` + "\n" + `invalid json` + "\n" + `{"id":"test-2","title":"Also Valid"}`),
			wantIDs: []string{"test-1", "test-2"},
		},
		{
			name:    "empty id skipped",
			content: []byte(`{"id":"","title":"No ID"}` + "\n" + `{"id":"test-1","title":"Has ID"}`),
			wantIDs: []string{"test-1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseIssuesFromContent(tt.content)
			if len(got) != len(tt.wantIDs) {
				t.Errorf("parseIssuesFromContent() returned %d issues, want %d", len(got), len(tt.wantIDs))
				return
			}
			for _, wantID := range tt.wantIDs {
				if _, exists := got[wantID]; !exists {
					t.Errorf("parseIssuesFromContent() missing expected ID %q", wantID)
				}
			}
		})
	}
}

// TestSafetyCheckMassDeletion tests the safety check behavior for mass deletions (bd-cnn)
func TestSafetyCheckMassDeletion(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	t.Run("safety check triggers when >50% issues vanish and >5 existed", func(t *testing.T) {
		// Test the countIssuesInContent and formatVanishedIssues functions
		// Create local content with 10 issues
		// bd-c5m: Use strconv.Itoa instead of string(rune()) which only works for single digits
		var localLines []string
		for i := 1; i <= 10; i++ {
			localLines = append(localLines, `{"id":"test-`+strconv.Itoa(i)+`","title":"Issue `+strconv.Itoa(i)+`"}`)
		}
		localContent := []byte(strings.Join(localLines, "\n"))

		// Create merged content with only 4 issues (60% vanished)
		mergedContent := []byte(`{"id":"test-1","title":"Issue 1"}
{"id":"test-2","title":"Issue 2"}
{"id":"test-3","title":"Issue 3"}
{"id":"test-4","title":"Issue 4"}`)

		localCount := countIssuesInContent(localContent)
		mergedCount := countIssuesInContent(mergedContent)

		if localCount != 10 {
			t.Errorf("localCount = %d, want 10", localCount)
		}
		if mergedCount != 4 {
			t.Errorf("mergedCount = %d, want 4", mergedCount)
		}

		// Verify safety check would trigger: >50% vanished AND >5 existed
		if localCount <= 5 {
			t.Error("localCount should be > 5 for safety check to apply")
		}
		vanishedPercent := float64(localCount-mergedCount) / float64(localCount) * 100
		if vanishedPercent <= 50 {
			t.Errorf("vanishedPercent = %.0f%%, want > 50%%", vanishedPercent)
		}

		// Verify forensic info can be generated
		localIssues := parseIssuesFromContent(localContent)
		mergedIssues := parseIssuesFromContent(mergedContent)
		forensicLines := formatVanishedIssues(localIssues, mergedIssues, localCount, mergedCount)
		if len(forensicLines) == 0 {
			t.Error("formatVanishedIssues returned empty lines")
		}

		// bd-8uk: Verify SafetyWarnings would be populated correctly
		// Simulate what PullFromSyncBranch does when safety check triggers
		var safetyWarnings []string
		safetyWarnings = append(safetyWarnings,
			"⚠️  Warning: "+strconv.FormatFloat(vanishedPercent, 'f', 0, 64)+"% of issues vanished during merge ("+
				strconv.Itoa(localCount)+" → "+strconv.Itoa(mergedCount)+" issues)")
		safetyWarnings = append(safetyWarnings, forensicLines...)

		// Verify warnings contains expected content
		if len(safetyWarnings) < 2 {
			t.Errorf("SafetyWarnings should have at least 2 entries (warning + forensics), got %d", len(safetyWarnings))
		}
		if !strings.Contains(safetyWarnings[0], "Warning") {
			t.Error("First SafetyWarning should contain 'Warning'")
		}
		if !strings.Contains(safetyWarnings[0], "60%") {
			t.Errorf("First SafetyWarning should contain '60%%', got: %s", safetyWarnings[0])
		}
	})

	t.Run("safety check does NOT trigger when <50% issues vanish", func(t *testing.T) {
		// 10 issues, 6 remain = 40% vanished (should NOT trigger)
		// bd-c5m: Use strconv.Itoa instead of string(rune()) which only works for single digits
		var localLines []string
		for i := 1; i <= 10; i++ {
			localLines = append(localLines, `{"id":"test-`+strconv.Itoa(i)+`"}`)
		}
		localContent := []byte(strings.Join(localLines, "\n"))

		// 6 issues remain (40% vanished)
		var mergedLines []string
		for i := 1; i <= 6; i++ {
			mergedLines = append(mergedLines, `{"id":"test-`+strconv.Itoa(i)+`"}`)
		}
		mergedContent := []byte(strings.Join(mergedLines, "\n"))

		localCount := countIssuesInContent(localContent)
		mergedCount := countIssuesInContent(mergedContent)

		vanishedPercent := float64(localCount-mergedCount) / float64(localCount) * 100
		if vanishedPercent > 50 {
			t.Errorf("vanishedPercent = %.0f%%, want <= 50%%", vanishedPercent)
		}
	})

	t.Run("safety check does NOT trigger when <5 issues existed", func(t *testing.T) {
		// 4 issues, 1 remains = 75% vanished, but only 4 existed (should NOT trigger)
		localContent := []byte(`{"id":"test-1"}
{"id":"test-2"}
{"id":"test-3"}
{"id":"test-4"}`)

		mergedContent := []byte(`{"id":"test-1"}`)

		localCount := countIssuesInContent(localContent)
		mergedCount := countIssuesInContent(mergedContent)

		if localCount != 4 {
			t.Errorf("localCount = %d, want 4", localCount)
		}
		if mergedCount != 1 {
			t.Errorf("mergedCount = %d, want 1", mergedCount)
		}

		// localCount > 5 is false, so safety check should NOT trigger
		if localCount > 5 {
			t.Error("localCount should be <= 5 for this test case")
		}
	})
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

// TestIsSyncBranchSameAsCurrent tests detection of sync.branch == current branch (GH#519)
func TestIsSyncBranchSameAsCurrent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()

	t.Run("returns true when sync branch equals current branch", func(t *testing.T) {
		repoDir := setupTestRepo(t)
		defer os.RemoveAll(repoDir)

		// Create initial commit so we can get current branch
		writeFile(t, filepath.Join(repoDir, ".beads", "issues.jsonl"), `{"id":"test-1"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "initial commit")

		// Get current branch name
		currentBranch := strings.TrimSpace(getGitOutput(t, repoDir, "symbolic-ref", "--short", "HEAD"))

		// Save original dir and change to test repo
		origDir, _ := os.Getwd()
		os.Chdir(repoDir)
		defer os.Chdir(origDir)

		// Should return true when sync branch == current branch
		if !IsSyncBranchSameAsCurrent(ctx, currentBranch) {
			t.Errorf("IsSyncBranchSameAsCurrent(%q) = false, want true", currentBranch)
		}
	})

	t.Run("returns false when sync branch differs from current branch", func(t *testing.T) {
		repoDir := setupTestRepo(t)
		defer os.RemoveAll(repoDir)

		// Create initial commit
		writeFile(t, filepath.Join(repoDir, ".beads", "issues.jsonl"), `{"id":"test-1"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "initial commit")

		// Save original dir and change to test repo
		origDir, _ := os.Getwd()
		os.Chdir(repoDir)
		defer os.Chdir(origDir)

		// Should return false when sync branch != current branch
		if IsSyncBranchSameAsCurrent(ctx, "beads-sync") {
			t.Error("IsSyncBranchSameAsCurrent(\"beads-sync\") = true, want false")
		}
	})

	t.Run("returns false on error getting current branch", func(t *testing.T) {
		// Test in a non-git directory
		tmpDir, _ := os.MkdirTemp("", "non-git-*")
		defer os.RemoveAll(tmpDir)

		origDir, _ := os.Getwd()
		os.Chdir(tmpDir)
		defer os.Chdir(origDir)

		// Should return false when not in a git repo
		if IsSyncBranchSameAsCurrent(ctx, "any-branch") {
			t.Error("IsSyncBranchSameAsCurrent in non-git dir = true, want false")
		}
	})
}

// TestContentMergeRecoveryRemoteWins tests that contentMergeRecovery
// uses a remote-wins strategy where remote content always takes precedence.
// This is the fix for bd-kpy: Sync race where rebase-based divergence recovery
// overwrites remote changes.
func TestContentMergeRecoveryRemoteWins(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()

	t.Run("remote content wins during merge recovery", func(t *testing.T) {
		// This test simulates the race condition described in bd-kpy:
		// 1. Repo A updates the issue, pushes successfully
		// 2. Repo B (with different local changes) tries to push, fails (non-fast-forward)
		// 3. Repo B uses contentMergeRecovery
		// 4. Verify remote content from A wins over B's local changes

		// Create a bare remote repo
		remoteDir, err := os.MkdirTemp("", "bd-test-remote-*")
		if err != nil {
			t.Fatalf("Failed to create remote dir: %v", err)
		}
		defer os.RemoveAll(remoteDir)
		runGit(t, remoteDir, "init", "--bare")

		// Create local repo
		repoDir := setupTestRepo(t)
		defer os.RemoveAll(repoDir)
		runGit(t, repoDir, "remote", "add", "origin", remoteDir)

		syncBranch := "beads-sync"
		jsonlPath := filepath.Join(repoDir, ".beads", "issues.jsonl")

		// Create base commit: issue with status="closed"
		runGit(t, repoDir, "checkout", "-b", syncBranch)
		writeFile(t, jsonlPath, `{"id":"test-1","status":"closed","title":"Test Issue"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "base: issue closed")

		// Push base to remote
		runGit(t, repoDir, "push", "-u", "origin", syncBranch)
		baseCommit := strings.TrimSpace(getGitOutput(t, repoDir, "rev-parse", "HEAD"))

		// Create remote update (simulating what Repo A did)
		writeFile(t, jsonlPath, `{"id":"test-1","status":"resolved","title":"Test Issue"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "remote: issue resolved")

		// Push update to remote (simulating Repo A's successful push)
		runGit(t, repoDir, "push", "origin", syncBranch)

		// Now simulate Repo B: reset to base and make different changes
		runGit(t, repoDir, "reset", "--hard", baseCommit)
		writeFile(t, jsonlPath, `{"id":"test-1","status":"closed","title":"Test Issue","notes":"local change"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "local: added notes")

		// Now local has diverged from remote:
		// - Local HEAD: status=closed with notes
		// - Remote origin/beads-sync: status=resolved
		// - Common ancestor (base): status=closed

		// Run contentMergeRecovery - this should use remote-wins strategy
		err = contentMergeRecovery(ctx, repoDir, syncBranch, "origin")
		if err != nil {
			t.Fatalf("contentMergeRecovery() error = %v", err)
		}

		// Read the merged content
		mergedData, err := os.ReadFile(jsonlPath)
		if err != nil {
			t.Fatalf("Failed to read merged JSONL: %v", err)
		}
		merged := string(mergedData)

		// Remote content should win (remote-wins strategy):
		// performContentMerge returns remote content directly.
		if !strings.Contains(merged, `"status":"resolved"`) {
			t.Errorf("contentMergeRecovery() did not use remote content.\nGot: %s\nWant: status=resolved (remote wins)", merged)
		}
	})

	t.Run("remote content wins when both sides diverge", func(t *testing.T) {
		// Create a bare remote repo
		remoteDir, err := os.MkdirTemp("", "bd-test-remote-*")
		if err != nil {
			t.Fatalf("Failed to create remote dir: %v", err)
		}
		defer os.RemoveAll(remoteDir)
		runGit(t, remoteDir, "init", "--bare")

		repoDir := setupTestRepo(t)
		defer os.RemoveAll(repoDir)
		runGit(t, repoDir, "remote", "add", "origin", remoteDir)

		syncBranch := "beads-sync"
		jsonlPath := filepath.Join(repoDir, ".beads", "issues.jsonl")

		// Create base: issue open
		runGit(t, repoDir, "checkout", "-b", syncBranch)
		writeFile(t, jsonlPath, `{"id":"test-1","status":"open","title":"Test"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "base")
		runGit(t, repoDir, "push", "-u", "origin", syncBranch)
		baseCommit := strings.TrimSpace(getGitOutput(t, repoDir, "rev-parse", "HEAD"))

		// Remote: set status to resolved (push to remote)
		writeFile(t, jsonlPath, `{"id":"test-1","status":"resolved","title":"Test"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "remote resolved")
		runGit(t, repoDir, "push", "origin", syncBranch)

		// Local: reset and set status to closed (divergent change)
		runGit(t, repoDir, "reset", "--hard", baseCommit)
		writeFile(t, jsonlPath, `{"id":"test-1","status":"closed","title":"Test"}`)
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "local closed")

		err = contentMergeRecovery(ctx, repoDir, syncBranch, "origin")
		if err != nil {
			t.Fatalf("contentMergeRecovery() error = %v", err)
		}

		mergedData, _ := os.ReadFile(jsonlPath)
		if !strings.Contains(string(mergedData), `"status":"resolved"`) {
			t.Errorf("Expected remote content to win, got: %s", mergedData)
		}
	})
}
