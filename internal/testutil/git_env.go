package testutil

import "github.com/steveyegge/beads/internal/gitenv"

// WithoutGitRepoLocationEnv returns a copy of env without the variables that
// name a git repository location, for a fixture command's cmd.Env: the command
// then operates on the repository its cmd.Dir resolves to and nothing else.
func WithoutGitRepoLocationEnv(env []string) []string {
	return gitenv.ScrubRepoLocation(env)
}

// UnsetGitRepoLocationEnv removes the same variables from the current process.
// Suites call it first in TestMain so no fixture git command inherits a leaked
// location; a test that needs one sets it with t.Setenv.
func UnsetGitRepoLocationEnv() error {
	return gitenv.ClearRepoLocation()
}
