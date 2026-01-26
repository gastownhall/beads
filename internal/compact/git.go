package compact

import (
	"context"
	"strings"

	"github.com/steveyegge/beads/internal/beads"
)

// gitExec is a function hook for executing git commands.
// In production, it uses RepoContext. In tests, it can be swapped for mocking.
var gitExec = defaultGitExec

// defaultGitExec uses RepoContext to execute git commands in the beads repository.
func defaultGitExec(name string, args ...string) ([]byte, error) {
	// name is always "git" when called from GetCurrentCommitHash
	rc, err := beads.GetRepoContext()
	if err != nil {
		return nil, err
	}

	cmd := rc.GitCmd(context.Background(), args...)
	return cmd.Output()
}

// GetCurrentCommitHash returns the current git HEAD commit hash for the beads repository.
// Returns empty string if not in a git repository or if git command fails.
func GetCurrentCommitHash() string {
	output, err := gitExec("git", "rev-parse", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}
