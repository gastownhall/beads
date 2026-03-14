package main

import (
	"os"
	"testing"

	"github.com/gastownhall/beads/internal/git"
)

// runInDir changes into dir, resets git caches before/after, and executes fn.
// It ensures tests that mutate git repositories don't leak state across cases.
func runInDir(t *testing.T, dir string, fn func()) {
	t.Helper()
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("failed to change to temp directory: %v", err)
	}
	git.ResetCaches()
	defer func() {
		if err := os.Chdir(origDir); err != nil {
			t.Fatalf("failed to restore working directory: %v", err)
		}
		git.ResetCaches()
	}()
	fn()
}
