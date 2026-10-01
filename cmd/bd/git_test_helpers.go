package main

import (
	"os"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/git"
)

// runInDir changes into dir, resets the beads and git caches before/after, and
// executes fn. It ensures tests that mutate git repositories don't leak state
// across cases. Both caches are keyed on the working directory, so a chdir
// fixture has to clear the pair going in and coming out — clearing only one
// leaves the other answering for the previous directory.
func runInDir(t *testing.T, dir string, fn func()) {
	t.Helper()
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("failed to change to temp directory: %v", err)
	}
	beads.ResetCaches()
	git.ResetCaches()
	defer func() {
		if err := os.Chdir(origDir); err != nil {
			t.Fatalf("failed to restore working directory: %v", err)
		}
		beads.ResetCaches()
		git.ResetCaches()
	}()
	fn()
}
