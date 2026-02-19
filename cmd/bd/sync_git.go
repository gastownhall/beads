package main

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/steveyegge/beads/internal/beads"
)

// isGitRepo checks if the current working directory is in a git repository.
// NOTE: This intentionally checks CWD, not the beads repo. It's used as a guard
// before calling other git functions to prevent hangs on Windows (GH#727).
// Does not use RepoContext because it's a prerequisite check for git availability.
func isGitRepo() bool {
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	return cmd.Run() == nil
}

// gitHasUpstream checks if the current branch has an upstream configured in the beads repository.
// Uses RepoContext to ensure git commands run in the correct repository.
// Uses git config directly for compatibility with Git for Windows.
func gitHasUpstream() bool {
	rc, err := beads.GetRepoContext()
	if err != nil {
		return false
	}

	ctx := context.Background()
	// Get current branch name
	branchCmd := rc.GitCmd(ctx, "symbolic-ref", "--short", "HEAD")
	branchOutput, err := branchCmd.Output()
	if err != nil {
		return false
	}
	branch := strings.TrimSpace(string(branchOutput))

	return gitBranchHasUpstream(branch)
}

// gitBranchHasUpstream checks if a specific branch has an upstream configured.
// Unlike gitHasUpstream(), this works even when HEAD is detached (e.g., jj/jujutsu).
// Uses RepoContext to ensure git commands run in the correct repository.
func gitBranchHasUpstream(branch string) bool {
	rc, err := beads.GetRepoContext()
	if err != nil {
		return false
	}

	ctx := context.Background()
	// Check if remote and merge refs are configured for the branch
	remoteCmd := rc.GitCmd(ctx, "config", "--get", fmt.Sprintf("branch.%s.remote", branch)) //nolint:gosec // G204: branch from caller
	mergeCmd := rc.GitCmd(ctx, "config", "--get", fmt.Sprintf("branch.%s.merge", branch))   //nolint:gosec // G204: branch from caller

	remoteErr := remoteCmd.Run()
	mergeErr := mergeCmd.Run()

	return remoteErr == nil && mergeErr == nil
}
