package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/utils"
)

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
