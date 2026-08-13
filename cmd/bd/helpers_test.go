package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/utils"
)

func TestIsNumericID_EdgeCases(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"0", true},
		{"123", true},
		{"999", true},
		{"abc", false},
		{"", false},
		{"12a", false},
	}

	for _, tt := range tests {
		result := isNumericID(tt.input)
		if result != tt.expected {
			t.Errorf("isNumericID(%q) = %v, want %v", tt.input, result, tt.expected)
		}
	}
}

func TestGetWorktreeGitDir(t *testing.T) {
	git.ResetCaches()
	t.Cleanup(git.ResetCaches)
	t.Chdir(t.TempDir())

	if got := getWorktreeGitDir(); got != "" {
		t.Errorf("getWorktreeGitDir() = %q outside a git repository, want empty", got)
	}
}

func TestGetWorktreeGitDirInWorktree(t *testing.T) {
	git.ResetCaches()
	t.Cleanup(git.ResetCaches)

	repo := t.TempDir()
	runGitInDir(t, repo, "init", "-q")
	runGitInDir(t, repo, "config", "core.hooksPath", ".git/hooks")
	runGitInDir(t, repo, "config", "user.email", "test@example.com")
	runGitInDir(t, repo, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("test\n"), 0o600); err != nil {
		t.Fatalf("write initial worktree file: %v", err)
	}
	runGitInDir(t, repo, "add", "README")
	runGitInDir(t, repo, "commit", "-qm", "initial")

	worktree := filepath.Join(t.TempDir(), "linked")
	runGitInDir(t, repo, "worktree", "add", "-q", "-b", "test-worktree", worktree)
	t.Chdir(worktree)

	want := runGitInDir(t, worktree, "rev-parse", "--git-dir")
	if got := getWorktreeGitDir(); got != want {
		t.Errorf("getWorktreeGitDir() = %q, want git worktree directory %q", got, want)
	}
}

func runGitInDir(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func TestExtractPrefix(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"bd-123", "bd"},
		{"custom-1", "custom"},
		{"TEST-999", "TEST"},
		{"no-number", "no"}, // Has hyphen, suffix not numeric, first hyphen
		{"nonumber", ""},    // No hyphen
		{"", ""},
		// Multi-part non-numeric suffixes (bd-fasa regression tests)
		{"vc-baseline-test", "vc"},
		{"vc-92cl-gate-test", "vc"},
		{"bd-multi-part-id", "bd"},
		{"prefix-a-b-c-d", "prefix"},
		// Multi-part prefixes with numeric suffixes
		{"beads-vscode-1", "beads-vscode"},
		{"alpha-beta-123", "alpha-beta"},
		{"my-project-42", "my-project"},
	}

	for _, tt := range tests {
		result := utils.ExtractIssuePrefix(tt.input)
		if result != tt.expected {
			t.Errorf("ExtractIssuePrefix(%q) = %q, want %q", tt.input, result, tt.expected)
		}
	}
}
