package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/git"
)

func TestConfigureBeadsHooksPath_WorktreeUsesMainRepo(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-hooks-worktree-test-*")
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
		if err := cmd.Run(); err != nil {
			t.Skipf("git %v failed: %v", args, err)
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
	if err := cmd.Run(); err != nil {
		t.Fatalf("git worktree add failed: %v", err)
	}
	t.Cleanup(func() {
		cmd := exec.Command("git", "worktree", "remove", "--force", worktreeDir)
		cmd.Dir = mainRepoDir
		_ = cmd.Run()
	})

	if err := os.MkdirAll(filepath.Join(mainRepoDir, ".beads", "hooks"), 0755); err != nil {
		t.Fatal(err)
	}

	t.Chdir(worktreeDir)
	git.ResetCaches()

	if !git.IsWorktree() {
		t.Fatal("expected git.IsWorktree() to return true")
	}

	if err := configureBeadsHooksPath(); err != nil {
		t.Fatalf("configureBeadsHooksPath failed: %v", err)
	}

	cmd = exec.Command("git", "config", "--get", "core.hooksPath")
	cmd.Dir = mainRepoDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git config --get core.hooksPath failed: %v", err)
	}
	hooksPath := filepath.Clean(strings.TrimSpace(string(out)))
	expected := filepath.Join(mainRepoDir, ".beads", "hooks")
	hooksPath, _ = filepath.EvalSymlinks(hooksPath)
	expected, _ = filepath.EvalSymlinks(expected)
	if hooksPath != expected {
		t.Errorf("core.hooksPath = %q, want %q", hooksPath, expected)
	}
}

func TestConfigureSharedHooksPath_WorktreeUsesMainRepo(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-shared-hooks-worktree-test-*")
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
		if err := cmd.Run(); err != nil {
			t.Skipf("git %v failed: %v", args, err)
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
	if err := cmd.Run(); err != nil {
		t.Fatalf("git worktree add failed: %v", err)
	}
	t.Cleanup(func() {
		cmd := exec.Command("git", "worktree", "remove", "--force", worktreeDir)
		cmd.Dir = mainRepoDir
		_ = cmd.Run()
	})

	if err := os.MkdirAll(filepath.Join(mainRepoDir, ".beads-hooks"), 0755); err != nil {
		t.Fatal(err)
	}

	t.Chdir(worktreeDir)
	git.ResetCaches()

	if !git.IsWorktree() {
		t.Fatal("expected git.IsWorktree() to return true")
	}

	if err := configureSharedHooksPath(); err != nil {
		t.Fatalf("configureSharedHooksPath failed: %v", err)
	}

	cmd = exec.Command("git", "config", "--get", "core.hooksPath")
	cmd.Dir = mainRepoDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git config --get core.hooksPath failed: %v", err)
	}
	hooksPath := filepath.Clean(strings.TrimSpace(string(out)))
	expected := filepath.Join(mainRepoDir, ".beads-hooks")
	hooksPath, _ = filepath.EvalSymlinks(hooksPath)
	expected, _ = filepath.EvalSymlinks(expected)
	if hooksPath != expected {
		t.Errorf("core.hooksPath = %q, want %q", hooksPath, expected)
	}
}

func TestResetHooksPathIfBeadsManaged_Worktree(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-reset-hooks-worktree-test-*")
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
		if err := cmd.Run(); err != nil {
			t.Skipf("git %v failed: %v", args, err)
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
	if err := cmd.Run(); err != nil {
		t.Fatalf("git worktree add failed: %v", err)
	}
	t.Cleanup(func() {
		cmd := exec.Command("git", "worktree", "remove", "--force", worktreeDir)
		cmd.Dir = mainRepoDir
		_ = cmd.Run()
	})

	if err := os.MkdirAll(filepath.Join(mainRepoDir, ".beads", "hooks"), 0755); err != nil {
		t.Fatal(err)
	}

	hooksPathToSet := filepath.Join(mainRepoDir, ".beads", "hooks")
	evaluated, _ := filepath.EvalSymlinks(hooksPathToSet)
	if evaluated != "" {
		hooksPathToSet = evaluated
	}
	cmd = exec.Command("git", "config", "core.hooksPath", hooksPathToSet)
	cmd.Dir = mainRepoDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git config core.hooksPath failed: %v", err)
	}

	run("config", "beads.role", "primary")
	run("config", "extensions.worktreeConfig", "true")
	cmd = exec.Command("git", "config", "--worktree", "beads.role", "worktree-only")
	cmd.Dir = worktreeDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("configure worktree role: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "config", "--worktree", "core.hooksPath", ".beads/hooks")
	cmd.Dir = worktreeDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("configure worktree hooksPath: %v\n%s", err, out)
	}
	t.Chdir(worktreeDir)
	git.ResetCaches()

	if !git.IsWorktree() {
		t.Fatal("expected git.IsWorktree() to return true")
	}

	git.ResetCaches() // Exercise the actual reset with a cold context.
	if err := resetHooksPathIfBeadsManaged(); err != nil {
		t.Fatalf("resetHooksPathIfBeadsManaged failed: %v", err)
	}

	cmd = exec.Command("git", "config", "--local", "--get", "core.hooksPath")
	cmd.Dir = mainRepoDir
	out, _ := cmd.Output()
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("core.hooksPath = %q after reset, want empty", strings.TrimSpace(string(out)))
	}
	cmd = exec.Command("git", "config", "--local", "--get", "beads.role")
	cmd.Dir = mainRepoDir
	out, err = cmd.Output()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
		t.Errorf("main role remains after reset: %q, %v", out, err)
	}
	cmd = exec.Command("git", "config", "--worktree", "--get", "beads.role")
	cmd.Dir = worktreeDir
	out, err = cmd.Output()
	if err != nil || strings.TrimSpace(string(out)) != "worktree-only" {
		t.Errorf("worktree-specific role changed: %q, %v", out, err)
	}
	cmd = exec.Command("git", "config", "--worktree", "--get", "core.hooksPath")
	cmd.Dir = worktreeDir
	out, err = cmd.Output()
	if err != nil || strings.TrimSpace(string(out)) != ".beads/hooks" {
		t.Errorf("worktree-specific hooksPath changed: %q, %v", out, err)
	}
	t.Run("preserve_worktree_role", func(t *testing.T) {
		// Seed a new common role so this reset proves removal independently of the first call.
		run("config", "--local", "beads.role", "primary")
		privateDir, err := git.GetGitDir()
		if err != nil {
			t.Fatal(err)
		}
		privateConfig := filepath.Join(privateDir, "config.worktree")
		before, err := os.ReadFile(privateConfig)
		if err != nil {
			t.Fatal(err)
		}
		git.ResetCaches()
		t.Cleanup(git.ResetCaches)
		if err := resetHooksPathIfBeadsManaged(); err != nil {
			t.Fatalf("reset while preserving a worktree role failed: %v", err)
		}
		if after, err := os.ReadFile(privateConfig); err != nil || string(after) != string(before) {
			t.Fatalf("private worktree config changed: %v", err)
		}
		get := exec.Command("git", "config", "--local", "--get", "beads.role")
		get.Dir = mainRepoDir
		out, err := get.Output()
		if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
			t.Errorf("common role after reset = %q, %v; want absent", out, err)
		}
	})
}

func TestConfigureBeadsHooksPath_NormalRepoUnchanged(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-hooks-normal-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	repoDir := filepath.Join(tmpDir, "repo")
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		if err := cmd.Run(); err != nil {
			t.Skipf("git %v failed: %v", args, err)
		}
	}
	run("init")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-m", "Initial commit")

	if err := os.MkdirAll(filepath.Join(repoDir, ".beads", "hooks"), 0755); err != nil {
		t.Fatal(err)
	}

	t.Chdir(repoDir)
	git.ResetCaches()

	if git.IsWorktree() {
		t.Fatal("expected git.IsWorktree() to return false in normal repo")
	}

	if err := configureBeadsHooksPath(); err != nil {
		t.Fatalf("configureBeadsHooksPath failed: %v", err)
	}

	cmd := exec.Command("git", "config", "--get", "core.hooksPath")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git config --get core.hooksPath failed: %v", err)
	}
	hooksPath := filepath.Clean(strings.TrimSpace(string(out)))
	expected := filepath.Join(repoDir, ".beads", "hooks")
	hooksPath, _ = filepath.EvalSymlinks(hooksPath)
	expected, _ = filepath.EvalSymlinks(expected)
	if hooksPath != expected {
		t.Errorf("core.hooksPath = %q, want %q", hooksPath, expected)
	}
}
