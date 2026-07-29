package fix

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// setupTestWorkspace creates a temporary directory with a .beads directory
func setupTestWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads directory: %v", err)
	}
	return dir
}

// setupTestGitRepo creates a temporary git repository with a .beads directory
func setupTestGitRepo(t *testing.T) string {
	t.Helper()
	dir := setupTestWorkspace(t)

	// Initialize git repo from cached template
	initGitTemplate()
	if gitTemplateErr != nil {
		t.Fatalf("git template init failed: %v", gitTemplateErr)
	}
	if err := copyGitDir(gitTemplateDir, dir); err != nil {
		t.Fatalf("failed to copy git template: %v", err)
	}

	return dir
}

func setupSharedWorktreeWorkspace(t *testing.T) (mainRepoDir, worktreeDir string) {
	t.Helper()

	tmpDir := t.TempDir()
	mainRepoDir = filepath.Join(tmpDir, "main-repo")
	if err := os.MkdirAll(mainRepoDir, 0o755); err != nil {
		t.Fatalf("failed to create main repo dir: %v", err)
	}

	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	run(mainRepoDir, "init")
	run(mainRepoDir, "config", "user.email", "test@example.com")
	run(mainRepoDir, "config", "user.name", "Test User")

	readmePath := filepath.Join(mainRepoDir, "README.md")
	if err := os.WriteFile(readmePath, []byte("# Test\n"), 0o644); err != nil {
		t.Fatalf("failed to write README.md: %v", err)
	}
	run(mainRepoDir, "add", "README.md")
	run(mainRepoDir, "commit", "-m", "Initial commit")

	worktreeDir = filepath.Join(tmpDir, "worktree")
	addWorktree := exec.Command("git", "worktree", "add", worktreeDir, "HEAD")
	addWorktree.Dir = mainRepoDir
	if out, err := addWorktree.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add failed: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		removeWorktree := exec.Command("git", "worktree", "remove", "--force", worktreeDir)
		removeWorktree.Dir = mainRepoDir
		_ = removeWorktree.Run()
	})

	return mainRepoDir, worktreeDir
}

// runGit runs a git command and returns output
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("git %v: %s", args, output)
	}
	return string(output)
}

// TestValidateBeadsWorkspace tests the workspace validation function
func TestValidateBeadsWorkspace(t *testing.T) {
	t.Run("invalid path", func(t *testing.T) {
		err := validateBeadsWorkspace("/nonexistent/path/that/does/not/exist")
		if err == nil {
			t.Error("expected error for nonexistent path")
		}
	})
}

// TestGitHooks_Validation tests GitHooks validation
func TestGitHooks_Validation(t *testing.T) {
	t.Run("not a git repository", func(t *testing.T) {
		dir := setupTestWorkspace(t)
		err := GitHooks(dir)
		if err == nil {
			t.Error("expected error for non-git repository")
		}
		if err.Error() != "not a git repository" {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

// TestUntrackedJSONL_Validation — removed: UntrackedJSONL function removed (bd-9ni.2)
func TestUntrackedJSONL_Validation(t *testing.T) {
	t.Skip("UntrackedJSONL removed as part of JSONL removal (bd-9ni.2)")
}

// TestIsWithinWorkspace tests the isWithinWorkspace helper
func TestIsWithinWorkspace(t *testing.T) {
	root := t.TempDir()

	tests := []struct {
		name      string
		candidate string
		want      bool
	}{
		{
			name:      "same directory",
			candidate: root,
			want:      true,
		},
		{
			name:      "subdirectory",
			candidate: filepath.Join(root, "subdir"),
			want:      true,
		},
		{
			name:      "nested subdirectory",
			candidate: filepath.Join(root, "sub", "dir", "nested"),
			want:      true,
		},
		{
			name:      "parent directory",
			candidate: filepath.Dir(root),
			want:      false,
		},
		{
			name:      "sibling directory",
			candidate: filepath.Join(filepath.Dir(root), "sibling"),
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isWithinWorkspace(root, tt.candidate)
			if got != tt.want {
				t.Errorf("isWithinWorkspace(%q, %q) = %v, want %v", root, tt.candidate, got, tt.want)
			}
		})
	}
}
