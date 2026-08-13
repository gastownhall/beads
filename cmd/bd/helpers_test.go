package main

import (
	"os/exec"
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

func TestGetWorktreeGitDirInRepository(t *testing.T) {
	git.ResetCaches()
	t.Cleanup(git.ResetCaches)
	repo := t.TempDir()
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = repo
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, output)
	}
	cmd = exec.Command("git", "config", "core.hooksPath", ".git/hooks")
	cmd.Dir = repo
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("configure repository-local hooks: %v\n%s", err, output)
	}
	t.Chdir(repo)

	if got := getWorktreeGitDir(); got != ".git" {
		t.Errorf("getWorktreeGitDir() = %q, want .git", got)
	}
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
