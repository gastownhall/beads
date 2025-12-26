package git

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// GetGitDir returns the actual .git directory path for the current repository.
// In a normal repo, this is ".git". In a worktree, .git is a file
// containing "gitdir: /path/to/actual/git/dir", so we use git rev-parse.
//
// This function uses Git's native worktree-aware APIs and should be used
// instead of direct filepath.Join(path, ".git") throughout the codebase.
func GetGitDir() (string, error) {
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("not a git repository: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
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

// isWorktreeOnce ensures we only check worktree status once per process.
// This is safe because worktree status cannot change during a single command execution.
var (
	isWorktreeOnce   sync.Once
	isWorktreeResult bool
)

// IsWorktree returns true if the current directory is in a Git worktree.
// This is determined by comparing --git-dir and --git-common-dir.
// The result is cached after the first call since worktree status doesn't
// change during a single command execution.
func IsWorktree() bool {
	isWorktreeOnce.Do(func() {
		isWorktreeResult = isWorktreeUncached()
	})
	return isWorktreeResult
}

// isWorktreeUncached performs the actual worktree check without caching.
// Called once by IsWorktree and cached for subsequent calls.
func isWorktreeUncached() bool {
	gitDir := getGitDirNoError("--git-dir")
	if gitDir == "" {
		return false
	}

	commonDir := getGitDirNoError("--git-common-dir")
	if commonDir == "" {
		return false
	}

	absGit, err1 := filepath.Abs(gitDir)
	absCommon, err2 := filepath.Abs(commonDir)
	if err1 != nil || err2 != nil {
		return false
	}

	return absGit != absCommon
}

// mainRepoRootOnce ensures we only get main repo root once per process.
var (
	mainRepoRootOnce   sync.Once
	mainRepoRootResult string
	mainRepoRootErr    error
)

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
	mainRepoRootOnce.Do(func() {
		mainRepoRootResult, mainRepoRootErr = getMainRepoRootUncached()
	})
	return mainRepoRootResult, mainRepoRootErr
}

// getMainRepoRootUncached performs the actual main repo root lookup without caching.
func getMainRepoRootUncached() (string, error) {
	// Use --git-common-dir which always returns the main repo's .git directory,
	// even when running from within a worktree or its subdirectories.
	// This is the most reliable method for finding the main repo root.
	commonDir := getGitDirNoError("--git-common-dir")
	if commonDir == "" {
		return "", fmt.Errorf("not a git repository")
	}

	// Convert to absolute path to handle relative paths correctly
	absCommonDir, err := filepath.Abs(commonDir)
	if err != nil {
		return "", fmt.Errorf("failed to resolve common dir path: %w", err)
	}

	// The main repo root is the parent of the .git directory
	mainRepoRoot := filepath.Dir(absCommonDir)

	return mainRepoRoot, nil
}

// repoRootOnce ensures we only get repo root once per process.
var (
	repoRootOnce   sync.Once
	repoRootResult string
)

// GetRepoRoot returns the root directory of the current git repository.
// Returns empty string if not in a git repository.
//
// This function is worktree-aware and handles Windows path normalization
// (Git on Windows may return paths like /c/Users/... or C:/Users/...).
// It also resolves symlinks to get the canonical path.
// The result is cached after the first call.
func GetRepoRoot() string {
	repoRootOnce.Do(func() {
		repoRootResult = getRepoRootUncached()
	})
	return repoRootResult
}

// getRepoRootUncached performs the actual repo root lookup without caching.
func getRepoRootUncached() string {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	root := strings.TrimSpace(string(output))

	// Normalize Windows paths from Git
	// Git on Windows may return /c/Users/... or C:/Users/...
	root = NormalizePath(root)

	// Resolve symlinks to get canonical path (fixes macOS /var -> /private/var)
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		return resolved
	}
	return root
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

// getGitDirNoError is a helper that returns empty string on error
// to avoid cluttering code with error handling for simple checks.
func getGitDirNoError(flag string) string {
	cmd := exec.Command("git", "rev-parse", flag)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ResetCaches resets all cached git information. This is intended for use
// by tests that need to change directory between subtests.
// In production, these caches are safe because the working directory
// doesn't change during a single command execution.
//
// WARNING: Not thread-safe. Only call from single-threaded test contexts.
func ResetCaches() {
	isWorktreeOnce = sync.Once{}
	isWorktreeResult = false
	mainRepoRootOnce = sync.Once{}
	mainRepoRootResult = ""
	mainRepoRootErr = nil
	repoRootOnce = sync.Once{}
	repoRootResult = ""
}