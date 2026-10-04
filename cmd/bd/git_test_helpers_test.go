package main

import (
	"os"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/git"
)

// resetRepoCaches is the single definition of this file's fixture invariant:
// the workspace and Git discovery caches are reset together, because workspace
// discovery resolves through the Git cache and would otherwise reprime from a
// previous fixture's answer.
func resetRepoCaches() {
	beads.ResetCaches()
	git.ResetCaches()
}

// resetRepoCachesForTest gives a serial fixture fresh workspace and Git
// discovery, including cached failures. Call after changing its directory.
func resetRepoCachesForTest(t *testing.T) {
	t.Helper()
	resetRepoCaches()
	t.Cleanup(resetRepoCaches)
}

// runInDir changes into dir, resets the repository discovery caches
// before/after, and executes fn. It ensures tests that mutate git repositories
// don't leak state across cases.
func runInDir(t *testing.T, dir string, fn func()) {
	t.Helper()
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("failed to change to temp directory: %v", err)
	}
	resetRepoCaches()
	defer func() {
		if err := os.Chdir(origDir); err != nil {
			t.Fatalf("failed to restore working directory: %v", err)
		}
		resetRepoCaches()
	}()
	fn()
}
