package doctor

import (
	"os"
	"os/exec"
	"testing"

	"github.com/steveyegge/beads/internal/git"
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

// noAutoMaintenance keeps a fixture's git commit from spawning the detached
// "git maintenance run --auto" child whose worktree-prune races the fixture's
// next "git worktree add" (gastownhall/beads#7314, #7349). Flags ride the
// command line, never env config: the routing-key scrub drops env config.
var noAutoMaintenance = []string{"-c", "maintenance.auto=false", "-c", "gc.auto=0"}

// gitCommand builds a git command for a fixture with noAutoMaintenance applied.
func gitCommand(args ...string) *exec.Cmd {
	return exec.Command("git", append(append([]string{}, noAutoMaintenance...), args...)...)
}
