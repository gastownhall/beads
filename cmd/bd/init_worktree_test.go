package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonbaldie/beads/internal/beads"
	"github.com/jonbaldie/beads/internal/git"
)

func TestCountExistingIssues_WorktreeFallback(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-worktree-init-test-*")
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
		cmd := exec.Command("git", "worktree", "remove", "--force", worktreeDir)
		cmd.Dir = mainRepoDir
		_ = cmd.Run()
	})

	mainBeadsDir := filepath.Join(mainRepoDir, ".beads")
	if err := os.MkdirAll(mainBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	worktreeBeadsDir := filepath.Join(worktreeDir, ".beads")
	os.RemoveAll(worktreeBeadsDir)

	t.Chdir(worktreeDir)
	git.ResetCaches()

	if !isGitRepo() {
		t.Fatal("expected isGitRepo() to return true in worktree")
	}
	if !git.IsWorktree() {
		t.Fatal("expected git.IsWorktree() to return true in worktree")
	}

	fallback := beads.GetWorktreeFallbackBeadsDir()
	fallback = strings.TrimSpace(fallback)
	fallback = filepath.Clean(fallback)
	if fallback == "" {
		t.Fatal("expected GetWorktreeFallbackBeadsDir() to return non-empty path in worktree")
	}

	expectedBeads := filepath.Clean(mainBeadsDir)

	fallbackResolved, err := filepath.EvalSymlinks(fallback)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q) failed: %v", fallback, err)
	}
	expectedResolved, err := filepath.EvalSymlinks(expectedBeads)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q) failed: %v", expectedBeads, err)
	}

	if fallbackResolved != expectedResolved {
		t.Errorf("GetWorktreeFallbackBeadsDir() = %q (resolved: %q), want %q (resolved: %q)",
			fallback, fallbackResolved, expectedBeads, expectedResolved)
	}
}

func TestCountExistingIssues_WorktreeLocalBeadsPreferred(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-worktree-init-local-test-*")
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

	mainBeadsDir := filepath.Join(mainRepoDir, ".beads")
	worktreeBeadsDir := filepath.Join(worktreeDir, ".beads")
	if err := os.MkdirAll(mainBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(worktreeBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktreeBeadsDir, "metadata.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}

	t.Chdir(worktreeDir)
	git.ResetCaches()

	if !git.IsWorktree() {
		t.Fatal("expected git.IsWorktree() to return true")
	}

	found := beads.FindBeadsDir()
	if found == "" {
		t.Fatal("FindBeadsDir() returned empty")
	}

	foundResolved, _ := filepath.EvalSymlinks(filepath.Clean(found))
	worktreeResolved, _ := filepath.EvalSymlinks(filepath.Clean(worktreeBeadsDir))

	if foundResolved != worktreeResolved {
		t.Errorf("FindBeadsDir() = %q, want %q (local worktree .beads should take precedence over main repo fallback)",
			foundResolved, worktreeResolved)
	}
}

func TestCountExistingIssues_WorktreeNoBeadsAnywhere(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-worktree-init-empty-test-*")
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

	os.RemoveAll(filepath.Join(worktreeDir, ".beads"))

	t.Chdir(worktreeDir)
	git.ResetCaches()

	found := beads.FindBeadsDir()
	if found != "" {
		t.Errorf("FindBeadsDir() = %q, want empty string when no .beads exists anywhere", found)
	}
}
