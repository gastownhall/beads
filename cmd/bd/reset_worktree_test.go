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

func TestReset_WorktreeFindsBeadsDir(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-reset-worktree-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	mainRepoDir := filepath.Join(tmpDir, "main-repo")
	if err := os.MkdirAll(mainRepoDir, 0755); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = mainRepoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	run("init")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test User")

	if err := os.WriteFile(filepath.Join(mainRepoDir, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-m", "Initial commit")

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
	if err := os.WriteFile(filepath.Join(mainBeadsDir, "metadata.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}

	worktreeBeadsDir := filepath.Join(worktreeDir, ".beads")
	os.RemoveAll(worktreeBeadsDir)

	t.Chdir(worktreeDir)
	git.ResetCaches()

	if !isGitRepo() {
		t.Fatal("expected isGitRepo() to return true")
	}
	if !git.IsWorktree() {
		t.Fatal("expected git.IsWorktree() to return true")
	}

	found := beads.FindBeadsDir()
	if found == "" {
		t.Fatal("FindBeadsDir() returned empty string")
	}

	expected, err := filepath.EvalSymlinks(mainBeadsDir)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q) failed: %v", mainBeadsDir, err)
	}
	actual, err := filepath.EvalSymlinks(found)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q) failed: %v", found, err)
	}

	expected = filepath.Clean(strings.TrimSpace(expected))
	actual = filepath.Clean(strings.TrimSpace(actual))

	if actual != expected {
		t.Errorf("FindBeadsDir() = %q, want %q", actual, expected)
	}

	if _, err := os.Stat(actual); err != nil {
		t.Errorf("os.Stat(FindBeadsDir()) failed: %v — runReset would report \"not initialized\"", err)
	}
}

func TestReset_WorktreeNoBeadsReturnsEmpty(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-reset-no-beads-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	mainRepoDir := filepath.Join(tmpDir, "main-repo")
	if err := os.MkdirAll(mainRepoDir, 0755); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = mainRepoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(mainRepoDir, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-m", "Initial commit")

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

	t.Chdir(worktreeDir)
	git.ResetCaches()

	found := beads.FindBeadsDir()
	if found != "" {
		t.Errorf("FindBeadsDir() = %q, want empty string when no .beads exists anywhere", found)
	}
}

func TestReset_WorktreeSubdirFindsBeadsDir(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-reset-subdir-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	mainRepoDir := filepath.Join(tmpDir, "main-repo")
	if err := os.MkdirAll(mainRepoDir, 0755); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = mainRepoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(mainRepoDir, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-m", "Initial commit")

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
	os.RemoveAll(filepath.Join(worktreeDir, ".beads"))

	subDir := filepath.Join(worktreeDir, "sub", "dir")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	t.Chdir(subDir)
	git.ResetCaches()

	found := beads.FindBeadsDir()
	if found != "" {
		actual, _ := filepath.EvalSymlinks(found)
		expected, _ := filepath.EvalSymlinks(mainBeadsDir)
		if filepath.Clean(actual) != filepath.Clean(expected) {
			t.Errorf("FindBeadsDir() from subdirectory = %q, want %q", actual, expected)
		}
	}
}
