package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/doltremote"
)

// beadsWorkspaceFiles are the only files inside .beads/ that the bootstrap
// sync path writes, and therefore the only files commitBeadsWorkspaceFiles is
// allowed to commit. Everything else under .beads/ (formulas, a git-tracked
// issues.jsonl, ...) is ordinary user content that may be mid-edit, and must
// never be swept into bootstrap's commit.
var beadsWorkspaceFiles = []string{"metadata.json", "config.yaml", ".gitignore"}

// commitBeadsWorkspaceFiles stages and commits the .beads/ workspace files
// that the bootstrap sync path writes (metadata.json, config.yaml, and any
// patterns appended to .beads/.gitignore), so adopting a project from a remote
// does not leave the working tree dirty — matching what `bd init` does on a
// fresh repo (GH#4644).
//
// Best-effort and side-effect-scoped:
//
//   - Exact file pathspecs, never the .beads/ directory: an unrelated change
//     the user has pending under .beads/ stays out of the commit.
//   - Committed in the checkout that physically contains beadsDir, resolved
//     with `git -C <beadsDir> rev-parse --show-toplevel`. This is deliberately
//     NOT beads.GetRepoContext(): RepoContext.RepoRoot resolves to the MAIN
//     checkout for a linked worktree (buildRepoContext -> git.GetMainRepoRoot),
//     so binding git operations to it would commit on the other checkout's
//     branch and advance a HEAD the user never asked bootstrap to touch.
//   - No-ops outside a git repo, in a bare repo (no worktree, so
//     --show-toplevel fails), and when nothing under the owned paths changed.
//   - Commits with `-c core.hooksPath=` in addition to --no-verify, mirroring
//     bd init: --no-verify alone does not skip prepare-commit-msg, and a
//     bootstrap-installed hook can recurse into bd while the embedded Dolt
//     lock is still held.
func commitBeadsWorkspaceFiles(beadsDir string) {
	root, relBeadsDir, ok := gitWorktreeRootContaining(beadsDir)
	if !ok {
		return
	}

	var pathspecs []string
	for _, name := range beadsWorkspaceFiles {
		if _, err := os.Stat(filepath.Join(beadsDir, name)); err != nil {
			continue
		}
		pathspecs = append(pathspecs, path.Join(relBeadsDir, name))
	}
	if len(pathspecs) == 0 {
		return
	}

	// args are fixed git subcommands; the only variables are repo-relative
	// pathspecs for bootstrap's own workspace files, always passed after a
	// "--" separator so they cannot be read as a flag or command.
	gitIn := func(args ...string) *exec.Cmd {
		c := exec.Command("git", args...) //nolint:gosec // G702: see comment above — fixed subcommands + "--"-separated internal paths
		c.Dir = root
		return c
	}
	withPaths := func(args ...string) []string {
		return append(append(args, "--"), pathspecs...)
	}

	// Nothing changed in the files bootstrap owns? Then there is nothing to
	// commit. (A repo that gitignores .beads/ entirely lands here too:
	// ignored files never show up in status.)
	status, err := gitIn(withPaths("status", "--porcelain")...).Output()
	if err != nil || strings.TrimSpace(string(status)) == "" {
		return
	}

	if err := gitIn(withPaths("add")...).Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to stage beads workspace files: %v\n", err)
		return
	}
	commit := gitIn(withPaths(
		"-c", "core.hooksPath=",
		"commit", "--no-verify",
		"-m", "bd bootstrap: sync beads workspace files",
	)...)
	if out, err := commit.CombinedOutput(); err != nil {
		if !strings.Contains(string(out), "nothing to commit") {
			fmt.Fprintf(os.Stderr, "Warning: failed to commit beads workspace files: %v\n", err)
		}
	}
}

// gitWorktreeRootContaining returns the root of the git working tree that
// physically contains dir, plus dir's slash-separated path relative to that
// root. ok is false outside a git repo, in a bare repo (no working tree), or
// if dir somehow resolves outside the reported root.
//
// Both paths go through filepath.EvalSymlinks before they are compared: git
// reports the canonical path (/private/var/... on macOS) while the caller may
// hold the symlinked one (/var/...), and an unresolved comparison would make
// every containment check fail.
func gitWorktreeRootContaining(dir string) (root, relDir string, ok bool) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output() //nolint:gosec // G204: dir is an internal .beads path, passed as an argument to a fixed subcommand
	if err != nil {
		return "", "", false
	}
	root = strings.TrimSpace(string(out))
	if root == "" {
		return "", "", false
	}

	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", false
	}
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", "", false
	}
	rel, err := filepath.Rel(resolvedRoot, resolvedDir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", false
	}
	return root, filepath.ToSlash(rel), true
}

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
	hasData, err := gitRemoteHasDoltDataRefStatus(remote)
	return err == nil && hasData
}

// gitOriginHasDoltDataRefStatus is the tri-state form: no data vs. unknown.
func gitOriginHasDoltDataRefStatus() (bool, error) {
	return gitRemoteHasDoltDataRefStatus("origin")
}

// A non-nil error means UNKNOWN, not "no data" — the bool is meaningless.
func gitRemoteHasDoltDataRefStatus(remote string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "ls-remote", gitRemoteURLForLsRemote(remote), "refs/dolt/data")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("probe refs/dolt/data on %s: %w", remote, err)
	}
	return strings.TrimSpace(string(output)) != "", nil
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
