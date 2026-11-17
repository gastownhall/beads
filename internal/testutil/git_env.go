package testutil

import (
	"fmt"
	"os"

	"github.com/steveyegge/beads/internal/execenv"
)

// gitRepoLocationKeys are the git environment variables that name a
// repository, work tree, index, object store, ref namespace or config file
// outright. Each one overrides what git would find from cmd.Dir or -C, so a
// value leaked in from the invoking shell or a git hook (git exports GIT_DIR to
// every hook it runs) points a fixture's `git init`/`git config` at a
// repository or file the test does not own: the r880 host .git/config clobber
// (wy-qlvlmf). GIT_CONFIG is the legacy single-file override: `git config`
// reads and writes the file it names instead of the repository's config.
//
// The set is deliberately narrower than internal/gitenv's routing keys. It
// leaves GIT_CEILING_DIRECTORIES, GIT_CONFIG_GLOBAL and GIT_CONFIG_NOSYSTEM
// alone because the Bazel and CI test wrappers set those on purpose: a
// discovery fence, and a hermetic global config that carries the test
// identity. Keys match exactly, so dropping GIT_CONFIG keeps all three.
var gitRepoLocationKeys = []string{
	"GIT_ALTERNATE_OBJECT_DIRECTORIES",
	"GIT_COMMON_DIR",
	"GIT_CONFIG",
	"GIT_DIR",
	"GIT_INDEX_FILE",
	"GIT_NAMESPACE",
	"GIT_OBJECT_DIRECTORY",
	"GIT_WORK_TREE",
}

// WithoutGitRepoLocationEnv returns a copy of env without the variables that
// name a git repository location, for a fixture command's cmd.Env: the command
// then operates on the repository its cmd.Dir resolves to and nothing else.
func WithoutGitRepoLocationEnv(env []string) []string {
	return execenv.Without(env, gitRepoLocationKeys...)
}

// UnsetGitRepoLocationEnv removes the same variables from the current process.
// Suites call it first in TestMain so no fixture git command inherits a leaked
// location; a test that needs one sets it with t.Setenv.
func UnsetGitRepoLocationEnv() error {
	for _, key := range gitRepoLocationKeys {
		if err := os.Unsetenv(key); err != nil {
			return fmt.Errorf("unset %s: %w", key, err)
		}
	}
	return nil
}
