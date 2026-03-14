package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/beads/internal/utils"
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

		// Create the main .beads directory that the redirect points to
		mainBeadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(mainBeadsDir, 0755); err != nil {
			t.Fatalf("failed to create main .beads dir: %v", err)
		}

		// Write a relative redirect from worktree root to main .beads
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

// TestWorktreeRedirectDepth tests that worktree redirect paths are computed correctly
// for different worktree directory depths. This is the fix for GH#1098.
//
// The redirect file contains a relative path from the worktree's .beads directory
// to the main repository's .beads directory. The depth of ../ components depends
// on how deeply nested the worktree is.
func TestWorktreeRedirectDepth(t *testing.T) {
	// Create a temporary repo structure
	tmpDir := t.TempDir()

	// Main repo's .beads directory
	mainBeadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(mainBeadsDir, 0755); err != nil {
		t.Fatalf("failed to create main .beads dir: %v", err)
	}

	tests := []struct {
		name              string
		worktreePath      string // Relative to tmpDir
		expectedRelPrefix string // Expected prefix (number of ../)
	}{
		{
			name:              "depth 1: .worktrees/foo",
			worktreePath:      ".worktrees/foo",
			expectedRelPrefix: "../../",
		},
		{
			name:              "depth 2: .worktrees/a/b",
			worktreePath:      ".worktrees/a/b",
			expectedRelPrefix: "../../../",
		},
		{
			name:              "depth 3: .worktrees/a/b/c",
			worktreePath:      ".worktrees/a/b/c",
			expectedRelPrefix: "../../../../",
		},
		{
			name:              "sibling worktree: agents/worker1",
			worktreePath:      "agents/worker1",
			expectedRelPrefix: "../../",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create worktree .beads directory
			worktreeDir := filepath.Join(tmpDir, tt.worktreePath)
			worktreeBeadsDir := filepath.Join(worktreeDir, ".beads")
			if err := os.MkdirAll(worktreeBeadsDir, 0755); err != nil {
				t.Fatalf("failed to create worktree .beads dir: %v", err)
			}
			defer os.RemoveAll(worktreeDir)

			// Simulate the worktree redirect computation from worktree_cmd.go:205-213
			// absMainBeadsDir := utils.CanonicalizeIfRelative(mainBeadsDir)
			// relPath, err := filepath.Rel(worktreeBeadsDir, absMainBeadsDir)
			absMainBeadsDir := utils.CanonicalizeIfRelative(mainBeadsDir)
			relPath, err := filepath.Rel(worktreeBeadsDir, absMainBeadsDir)
			if err != nil {
				t.Fatalf("filepath.Rel() failed: %v", err)
			}

			// Verify the relative path starts with the expected ../ prefix
			if !strings.HasPrefix(relPath, tt.expectedRelPrefix) {
				t.Errorf("expected relPath to start with %q, got %q", tt.expectedRelPrefix, relPath)
			}

			// Verify the relative path ends with .beads
			if !strings.HasSuffix(relPath, ".beads") {
				t.Errorf("expected relPath to end with .beads, got %q", relPath)
			}

			// Verify the path actually resolves correctly
			resolvedPath := filepath.Join(worktreeBeadsDir, relPath)
			resolvedPath = filepath.Clean(resolvedPath)
			canonicalMain := utils.CanonicalizePath(mainBeadsDir)
			canonicalResolved := utils.CanonicalizePath(resolvedPath)

			if canonicalResolved != canonicalMain {
				t.Errorf("resolved path mismatch:\n  expected: %s\n  got:      %s", canonicalMain, canonicalResolved)
			}
		})
	}
}

// TestWorktreeRedirectWithRelativeMainBeadsDir tests that worktree redirect
// works correctly even when mainBeadsDir is returned as a relative path.
// This ensures CanonicalizeIfRelative() is being used properly.
func TestWorktreeRedirectWithRelativeMainBeadsDir(t *testing.T) {
	// Create a temporary repo structure
	tmpDir := t.TempDir()

	// Main repo's .beads directory
	mainBeadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(mainBeadsDir, 0755); err != nil {
		t.Fatalf("failed to create main .beads dir: %v", err)
	}

	// Create worktree
	worktreeDir := filepath.Join(tmpDir, ".worktrees", "test-wt")
	worktreeBeadsDir := filepath.Join(worktreeDir, ".beads")
	if err := os.MkdirAll(worktreeBeadsDir, 0755); err != nil {
		t.Fatalf("failed to create worktree .beads dir: %v", err)
	}

	// Change to tmpDir to simulate relative path scenario
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get cwd: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	defer os.Chdir(origDir)

	// Test with RELATIVE mainBeadsDir (as it might be returned by beads.FindBeadsDir())
	relativeMainBeadsDir := ".beads"

	// The fix: CanonicalizeIfRelative ensures the path is absolute
	absMainBeadsDir := utils.CanonicalizeIfRelative(relativeMainBeadsDir)

	// Verify it's now absolute
	if !filepath.IsAbs(absMainBeadsDir) {
		t.Errorf("CanonicalizeIfRelative should return absolute path, got %q", absMainBeadsDir)
	}

	// Compute relative path from worktree's .beads to main .beads
	relPath, err := filepath.Rel(worktreeBeadsDir, absMainBeadsDir)
	if err != nil {
		t.Fatalf("filepath.Rel() failed: %v", err)
	}

	// Verify the path looks correct (should be ../../.beads)
	if !strings.HasPrefix(relPath, "../../") {
		t.Errorf("expected relPath to start with ../../, got %q", relPath)
	}

	// Verify resolution works
	resolvedPath := filepath.Clean(filepath.Join(worktreeBeadsDir, relPath))
	canonicalMain := utils.CanonicalizePath(mainBeadsDir)
	canonicalResolved := utils.CanonicalizePath(resolvedPath)

	if canonicalResolved != canonicalMain {
		t.Errorf("resolved path mismatch:\n  expected: %s\n  got:      %s", canonicalMain, canonicalResolved)
	}
}

// TestWorktreeRedirectWithoutFix demonstrates what would happen without
// the CanonicalizeIfRelative fix. This documents the bug behavior.
func TestWorktreeRedirectWithoutFix(t *testing.T) {
	tmpDir := t.TempDir()

	// Main repo's .beads directory
	mainBeadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(mainBeadsDir, 0755); err != nil {
		t.Fatalf("failed to create main .beads dir: %v", err)
	}

	// Create worktree
	worktreeDir := filepath.Join(tmpDir, ".worktrees", "test-wt")
	worktreeBeadsDir := filepath.Join(worktreeDir, ".beads")
	if err := os.MkdirAll(worktreeBeadsDir, 0755); err != nil {
		t.Fatalf("failed to create worktree .beads dir: %v", err)
	}

	// Change to tmpDir
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get cwd: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	defer os.Chdir(origDir)

	// Bug scenario: relative mainBeadsDir WITHOUT CanonicalizeIfRelative
	relativeMainBeadsDir := ".beads"

	// filepath.Rel with relative base path produces INCORRECT results
	relPathBuggy, err := filepath.Rel(worktreeBeadsDir, relativeMainBeadsDir)
	if err != nil {
		// This might error, which is also a bug symptom
		t.Logf("filepath.Rel() failed with relative base: %v (expected behavior)", err)
		return
	}

	// The buggy relPath will be something like "../../../.beads" when it should be "../../.beads"
	// or it might be completely wrong depending on the relative path interpretation
	t.Logf("Buggy relPath (without fix): %q", relPathBuggy)

	// The path likely won't resolve correctly
	resolvedBuggy := filepath.Clean(filepath.Join(worktreeBeadsDir, relPathBuggy))
	canonicalMain := utils.CanonicalizePath(mainBeadsDir)
	canonicalBuggy := utils.CanonicalizePath(resolvedBuggy)

	// Document that the bug exists (or doesn't, if Go handles it)
	if canonicalBuggy != canonicalMain {
		t.Logf("Bug confirmed: buggy path %q != expected %q", canonicalBuggy, canonicalMain)
	} else {
		t.Logf("Note: filepath.Rel handled relative base correctly in this case")
	}
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
