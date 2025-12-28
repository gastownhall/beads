package git

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// gitContext holds cached git repository information.
// All fields are populated with a single git call for efficiency.
type gitContext struct {
	gitDir     string // Result of --git-dir
	commonDir  string // Result of --git-common-dir (absolute)
	repoRoot   string // Result of --show-toplevel (normalized, symlinks resolved)
	isWorktree bool   // Derived: gitDir != commonDir
	err        error  // Any error during initialization
}

var (
	gitCtxOnce sync.Once
	gitCtx     gitContext
)

// initGitContext populates the gitContext with a single git call.
// This is called once per process via sync.Once.
func initGitContext() {
	// Get all three values with a single git call
	cmd := exec.Command("git", "rev-parse", "--git-dir", "--git-common-dir", "--show-toplevel")
	output, err := cmd.Output()
	if err != nil {
		gitCtx.err = fmt.Errorf("not a git repository: %w", err)
		return
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) < 3 {
		gitCtx.err = fmt.Errorf("unexpected git rev-parse output: got %d lines, expected 3", len(lines))
		return
	}

	gitCtx.gitDir = strings.TrimSpace(lines[0])
	commonDirRaw := strings.TrimSpace(lines[1])
	repoRootRaw := strings.TrimSpace(lines[2])

	// Convert commonDir to absolute for reliable comparison
	absCommon, err := filepath.Abs(commonDirRaw)
	if err != nil {
		gitCtx.err = fmt.Errorf("failed to resolve common dir path: %w", err)
		return
	}
	gitCtx.commonDir = absCommon

	// Convert gitDir to absolute for worktree comparison
	absGitDir, err := filepath.Abs(gitCtx.gitDir)
	if err != nil {
		gitCtx.err = fmt.Errorf("failed to resolve git dir path: %w", err)
		return
	}

	// Derive isWorktree from comparing absolute paths
	gitCtx.isWorktree = absGitDir != absCommon

	// Process repoRoot: normalize Windows paths and resolve symlinks
	repoRoot := NormalizePath(repoRootRaw)
	if resolved, err := filepath.EvalSymlinks(repoRoot); err == nil {
		repoRoot = resolved
	}
	gitCtx.repoRoot = repoRoot
}

// getGitContext returns the cached git context, initializing it if needed.
func getGitContext() (*gitContext, error) {
	gitCtxOnce.Do(initGitContext)
	if gitCtx.err != nil {
		return nil, gitCtx.err
	}
	return &gitCtx, nil
}

// GetGitDir returns the actual .git directory path for the current repository.
// In a normal repo, this is ".git". In a worktree, .git is a file
// containing "gitdir: /path/to/actual/git/dir", so we use git rev-parse.
//
// This function uses Git's native worktree-aware APIs and should be used
// instead of direct filepath.Join(path, ".git") throughout the codebase.
func GetGitDir() (string, error) {
	ctx, err := getGitContext()
	if err != nil {
		return "", err
	}
	return ctx.gitDir, nil
}

// GetGitHooksDir returns the path to the Git hooks directory.
// This function is worktree-aware and handles both regular repos and worktrees.
func GetGitHooksDir() (string, error) {
	gitDir, err := GetGitDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(gitDir, "hooks"), nil
}

// GetGitRefsDir returns the path to the Git refs directory.
// This function is worktree-aware and handles both regular repos and worktrees.
func GetGitRefsDir() (string, error) {
	gitDir, err := GetGitDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(gitDir, "refs"), nil
}

// GetGitHeadPath returns the path to the Git HEAD file.
// This function is worktree-aware and handles both regular repos and worktrees.
func GetGitHeadPath() (string, error) {
	gitDir, err := GetGitDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(gitDir, "HEAD"), nil
}

// IsWorktree returns true if the current directory is in a Git worktree.
// This is determined by comparing --git-dir and --git-common-dir.
// The result is cached after the first call since worktree status doesn't
// change during a single command execution.
func IsWorktree() bool {
	ctx, err := getGitContext()
	if err != nil {
		return false
	}
	return ctx.isWorktree
}

// GetMainRepoRoot returns the main repository root directory.
// When in a worktree, this returns the main repository root.
// Otherwise, it returns the regular repository root.
//
// For nested worktrees (worktrees located under the main repo, e.g.,
// /project/.worktrees/feature/), this correctly returns the main repo
// root (/project/) by using git rev-parse --git-common-dir which always
// points to the main repo's .git directory. (GH#509)
// The result is cached after the first call.
func GetMainRepoRoot() (string, error) {
	ctx, err := getGitContext()
	if err != nil {
		return "", err
	}
	// The main repo root is the parent of the .git directory (commonDir)
	return filepath.Dir(ctx.commonDir), nil
}

// GetRepoRoot returns the root directory of the current git repository.
// Returns empty string if not in a git repository.
//
// This function is worktree-aware and handles Windows path normalization
// (Git on Windows may return paths like /c/Users/... or C:/Users/...).
// It also resolves symlinks to get the canonical path.
// The result is cached after the first call.
func GetRepoRoot() string {
	ctx, err := getGitContext()
	if err != nil {
		return ""
	}
	return ctx.repoRoot
}

// NormalizePath converts Git's Windows path formats to native format.
// Git on Windows may return paths like /c/Users/... or C:/Users/...
// This function converts them to native Windows format (C:\Users\...).
// On non-Windows systems, this is a no-op.
func NormalizePath(path string) string {
	// Only apply Windows normalization on Windows
	if filepath.Separator != '\\' {
		return path
	}

	// Convert /c/Users/... to C:\Users\...
	if len(path) >= 3 && path[0] == '/' && path[2] == '/' {
		return strings.ToUpper(string(path[1])) + ":" + filepath.FromSlash(path[2:])
	}

	// Convert C:/Users/... to C:\Users\...
	return filepath.FromSlash(path)
}

// ResetCaches resets all cached git information. This is intended for use
// by tests that need to change directory between subtests.
// In production, these caches are safe because the working directory
// doesn't change during a single command execution.
//
// WARNING: Not thread-safe. Only call from single-threaded test contexts.
func ResetCaches() {
	gitCtxOnce = sync.Once{}
	gitCtx = gitContext{}
}