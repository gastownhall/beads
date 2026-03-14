package doctor

import (
	"os"
	"testing"

	"github.com/gastownhall/beads/internal/git"
)

// runInDir changes directories for git-dependent doctor tests and resets caches
// so git helpers don't retain state across subtests.
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
