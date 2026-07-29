package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/doltremote"
)

// isGitRepo checks if the current working directory is in a git repository.
// NOTE: This intentionally checks CWD, not the beads repo. It's used as a guard
// before calling other git functions to prevent hangs on Windows (GH#727).
// Does not use RepoContext because it's a prerequisite check for git availability.
func isGitRepo() bool {
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	return cmd.Run() == nil
}

// isBareGitRepo checks if the current git repository is bare.
// Returns false when not in a git repository.
func isBareGitRepo() bool {
	cmd := exec.Command("git", "rev-parse", "--is-bare-repository")
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(output)) == "true"
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

// gitHasAnyRemotes returns true if the git repository has any remotes configured.
// Used to distinguish between "new repo with no remotes" and "repo with origin but no upstream".
func gitHasAnyRemotes() bool {
	rc, err := beads.GetRepoContext()
	if err != nil {
		return false
	}

	ctx := context.Background()
	remoteCmd := rc.GitCmd(ctx, "remote")
	output, err := remoteCmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(output)) != ""
}

// gitOriginGetURL returns the URL for the origin git remote.
func gitOriginGetURL() (string, error) {
	cmd := exec.Command("git", "remote", "get-url", "origin")
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func gitOriginGetURLForActiveRepo(ctx context.Context) (string, error) {
	rc, err := beads.GetRepoContext()
	if err != nil {
		return "", err
	}
	cmd := rc.GitCmd(ctx, "remote", "get-url", "origin")
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// gitOriginHasDoltDataRef checks if origin has refs/dolt/data.
// Returns false on any error (network, no remote, timeout, etc).
// Uses a 10s timeout since this is a network call used for auto-detection,
// and suppresses credential prompts to avoid blocking on SSH remotes.
func gitOriginHasDoltDataRef() bool {
	return gitRemoteHasDoltDataRef("origin")
}

func gitRemoteHasDoltDataRef(remote string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "ls-remote", gitRemoteURLForLsRemote(remote), "refs/dolt/data")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(output)) != ""
}

func gitRemoteURLForLsRemote(remote string) string {
	return strings.TrimPrefix(remote, "git+")
}

// gitURLToDoltRemote converts a git remote URL to dolt's remote format.
// HTTPS URLs get "git+" prefix: https://... → git+https://...
// SCP-style SSH URLs are converted: git@host:path → git+ssh://git@host/path
// SSH URLs get "git+" prefix: ssh://... → git+ssh://...
// URLs that already have "git+" prefix are returned as-is.
func gitURLToDoltRemote(url string) string {
	return doltremote.FromGitURL(url)
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
