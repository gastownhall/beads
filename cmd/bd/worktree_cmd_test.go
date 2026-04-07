package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/utils"
)

// TestGetRedirectTarget tests that getRedirectTarget resolves redirect paths correctly.
// This is the fix for GH#1266: relative paths must be resolved from the worktree root
// (parent of .beads/), not from .beads/ itself, matching FollowRedirect behavior.
func TestGetRedirectTarget(t *testing.T) {
	tmpDir := t.TempDir()

	t.Run("relative path resolved from worktree root", func(t *testing.T) {
		worktreeDir := filepath.Join(tmpDir, "worktrees", "feat-branch")
		worktreeBeadsDir := filepath.Join(worktreeDir, ".beads")
		if err := os.MkdirAll(worktreeBeadsDir, 0755); err != nil {
			t.Fatalf("failed to create worktree .beads dir: %v", err)
		}

		mainBeadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(mainBeadsDir, 0755); err != nil {
			t.Fatalf("failed to create main .beads dir: %v", err)
		}

		redirectFile := filepath.Join(worktreeBeadsDir, "redirect")
		if err := os.WriteFile(redirectFile, []byte("../../.beads\n"), 0644); err != nil {
			t.Fatalf("failed to write redirect file: %v", err)
		}

		got := getRedirectTarget(worktreeDir)
		if got == "" {
			t.Fatal("getRedirectTarget returned empty string")
		}

		canonicalGot := utils.CanonicalizePath(got)
		canonicalExpected := utils.CanonicalizePath(mainBeadsDir)

		if canonicalGot != canonicalExpected {
			t.Errorf("getRedirectTarget() mismatch:\n  got:      %s\n  expected: %s", canonicalGot, canonicalExpected)
		}
	})

	t.Run("absolute path returned as-is", func(t *testing.T) {
		worktreeDir := filepath.Join(tmpDir, "worktrees", "abs-test")
		worktreeBeadsDir := filepath.Join(worktreeDir, ".beads")
		if err := os.MkdirAll(worktreeBeadsDir, 0755); err != nil {
			t.Fatalf("failed to create worktree .beads dir: %v", err)
		}

		absTarget := filepath.Join(tmpDir, "abs-target-beads")
		if err := os.MkdirAll(absTarget, 0755); err != nil {
			t.Fatalf("failed to create abs target dir: %v", err)
		}

		redirectFile := filepath.Join(worktreeBeadsDir, "redirect")
		if err := os.WriteFile(redirectFile, []byte(absTarget+"\n"), 0644); err != nil {
			t.Fatalf("failed to write redirect file: %v", err)
		}

		got := getRedirectTarget(worktreeDir)
		canonicalGot := utils.CanonicalizePath(got)
		canonicalExpected := utils.CanonicalizePath(absTarget)

		if canonicalGot != canonicalExpected {
			t.Errorf("getRedirectTarget() mismatch for absolute path:\n  got:      %s\n  expected: %s", canonicalGot, canonicalExpected)
		}
	})

	t.Run("missing redirect file returns empty", func(t *testing.T) {
		worktreeDir := filepath.Join(tmpDir, "worktrees", "no-redirect")
		worktreeBeadsDir := filepath.Join(worktreeDir, ".beads")
		if err := os.MkdirAll(worktreeBeadsDir, 0755); err != nil {
			t.Fatalf("failed to create worktree .beads dir: %v", err)
		}

		got := getRedirectTarget(worktreeDir)
		if got != "" {
			t.Errorf("expected empty string for missing redirect, got %q", got)
		}
	})
}

func TestAddToGitignore(t *testing.T) {
	t.Run("skips append when path already ignored by broader pattern", func(t *testing.T) {
		repoRoot := initGitRepoForGitignoreTest(t)
		gitignorePath := filepath.Join(repoRoot, ".gitignore")
		initial := ".worktrees/\n"
		if err := os.WriteFile(gitignorePath, []byte(initial), 0644); err != nil {
			t.Fatalf("failed to write .gitignore: %v", err)
		}

		if err := addToGitignore(context.Background(), repoRoot, ".worktrees/worktree-one"); err != nil {
			t.Fatalf("addToGitignore failed: %v", err)
		}

		updated, err := os.ReadFile(gitignorePath)
		if err != nil {
			t.Fatalf("failed to read .gitignore: %v", err)
		}

		if string(updated) != initial {
			t.Fatalf(".gitignore should be unchanged when entry is already ignored:\nwant:\n%s\ngot:\n%s", initial, string(updated))
		}
	})

	t.Run("appends exactly once when path is not ignored", func(t *testing.T) {
		repoRoot := initGitRepoForGitignoreTest(t)
		gitignorePath := filepath.Join(repoRoot, ".gitignore")
		if err := os.WriteFile(gitignorePath, []byte("node_modules/\n"), 0644); err != nil {
			t.Fatalf("failed to write .gitignore: %v", err)
		}

		entry := "worktree-feature"
		if err := addToGitignore(context.Background(), repoRoot, entry); err != nil {
			t.Fatalf("first addToGitignore failed: %v", err)
		}
		if err := addToGitignore(context.Background(), repoRoot, entry); err != nil {
			t.Fatalf("second addToGitignore failed: %v", err)
		}

		updated, err := os.ReadFile(gitignorePath)
		if err != nil {
			t.Fatalf("failed to read .gitignore: %v", err)
		}
		content := string(updated)

		if count := strings.Count(content, "# bd worktree"); count != 1 {
			t.Fatalf("expected one worktree marker, got %d:\n%s", count, content)
		}
		if count := strings.Count(content, entry+"/"); count != 1 {
			t.Fatalf("expected one worktree entry, got %d:\n%s", count, content)
		}
	})
}

func initGitRepoForGitignoreTest(t *testing.T) string {
	t.Helper()
	repoRoot := t.TempDir()

	cmd := exec.Command("git", "init")
	cmd.Dir = repoRoot
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to init git repo: %v\n%s", err, string(output))
	}

	return repoRoot
}
