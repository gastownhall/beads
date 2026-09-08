package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/git"
)

func TestBeadsPollutionCheck_WorktreeSkips(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-worktree-preflight-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	mainRepoDir := filepath.Join(tmpDir, "main-repo")
	if err := os.MkdirAll(mainRepoDir, 0755); err != nil {
		t.Fatal(err)
	}

	run := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	run(mainRepoDir, "init")
	run(mainRepoDir, "config", "user.email", "test@example.com")
	run(mainRepoDir, "config", "user.name", "Test User")

	if err := os.WriteFile(filepath.Join(mainRepoDir, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run(mainRepoDir, "add", "README.md")
	run(mainRepoDir, "commit", "-m", "Initial commit")

	worktreeDir := filepath.Join(tmpDir, "worktree")
	cmd := exec.Command("git", "worktree", "add", worktreeDir, "HEAD")
	cmd.Dir = mainRepoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add failed: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		cleanupCmd := exec.Command("git", "worktree", "remove", "--force", worktreeDir)
		cleanupCmd.Dir = mainRepoDir
		_ = cleanupCmd.Run()
	})

	if err := os.MkdirAll(filepath.Join(mainRepoDir, ".beads"), 0755); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(filepath.Join(worktreeDir, ".beads"))

	t.Chdir(worktreeDir)
	git.ResetCaches()

	result := runBeadsPollutionCheck()
	if !result.Passed {
		t.Errorf("runBeadsPollutionCheck() in worktree: Passed=%v, want true (should skip when .beads is outside worktree)", result.Passed)
	}
}
