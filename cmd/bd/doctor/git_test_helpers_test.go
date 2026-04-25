package doctor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/git"
)

// isolateGlobalGitConfig prevents user/system Git config from changing test
// behavior. In particular, global core.hooksPath and remote.* settings make
// temp repos look like they have hooks or remotes when they do not.
func isolateGlobalGitConfig(t *testing.T) {
	t.Helper()
	globalConfig := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(globalConfig, nil, 0600); err != nil {
		t.Fatalf("failed to create isolated git config: %v", err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", globalConfig)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	git.ResetCaches()
	t.Cleanup(git.ResetCaches)
}

// runInDir changes directories for git-dependent doctor tests and resets caches
// so git helpers don't retain state across subtests.
func runInDir(t *testing.T, dir string, fn func()) {
	t.Helper()
	isolateGlobalGitConfig(t)
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
