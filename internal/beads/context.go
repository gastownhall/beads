// Package beads context.go provides centralized repository context resolution.
//
// Problem: 50+ git commands across the codebase assume CWD is the repository root.
// When BEADS_DIR points to a different repo, or when running from a worktree,
// these commands execute in the wrong directory.
//
// Solution: RepoContext provides a single source of truth for repository paths,
// with methods that ensure git commands run in the correct repository.
//
// Usage:
//
//	rc, err := beads.GetRepoContext()
//	if err != nil {
//	    return err
//	}
//	cmd := rc.GitCmd(ctx, "status")  // Runs in beads repo, not CWD
//
// See docs/REPO_CONTEXT.md for detailed documentation.
package beads

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/steveyegge/beads/internal/git"
)

// UserRole represents the user's relationship to a repository.
// Used to determine appropriate behaviors for fork contributors vs maintainers.
type UserRole string

// Role constants for user relationship to repository.
const (
	// Contributor indicates the user is contributing to a fork (not the maintainer).
	// BEADS_DIR redirection implies contributor role automatically.
	Contributor UserRole = "contributor"

	// Maintainer indicates the user owns/maintains the repository.
	Maintainer UserRole = "maintainer"
)

// ErrRoleNotConfigured is returned when beads.role is not set in git config.
// This signals that the init prompt should be shown to configure the role.
var ErrRoleNotConfigured = errors.New("beads.role not configured in git config")

// RepoContext holds resolved repository paths for beads operations.
//
// The struct distinguishes between:
//   - RepoRoot: where .beads/ lives (for git operations on beads data)
//   - CWDRepoRoot: where user is working (for status display, etc.)
//
// These may differ when BEADS_DIR points to a different repository,
// or when running from a git worktree.
type RepoContext struct {
	// BeadsDir is the actual .beads directory path (after following redirects).
	BeadsDir string

	// RepoRoot is the repository root containing BeadsDir.
	// Git commands for beads operations should run here.
	RepoRoot string

	// CWDRepoRoot is the repository root containing the current working directory.
	// May differ from RepoRoot when BEADS_DIR points elsewhere.
	CWDRepoRoot string

	// IsRedirected is true if BeadsDir was resolved via BEADS_DIR env var
	// pointing to a different repository than CWD.
	IsRedirected bool

	// IsWorktree is true if CWD is in a git worktree.
	IsWorktree bool
}

var (
	repoCtx     *RepoContext
	repoCtxOnce sync.Once
	repoCtxErr  error
)

// GetRepoContext returns the cached repository context, initializing it on first call.
//
// The context is cached because:
// 1. CWD doesn't change during command execution
// 2. BEADS_DIR doesn't change during command execution
// 3. Repeated filesystem access would be wasteful
//
// Returns an error if no .beads directory can be found.
func GetRepoContext() (*RepoContext, error) {
	repoCtxOnce.Do(func() {
		repoCtx, repoCtxErr = buildRepoContext()
	})
	return repoCtx, repoCtxErr
}

// buildRepoContext constructs the RepoContext by resolving all paths.
// This is called once per process via sync.Once.
func buildRepoContext() (*RepoContext, error) {
	// 1. Find .beads directory (respects BEADS_DIR env var)
	beadsDir := FindBeadsDir()
	if beadsDir == "" {
		return nil, fmt.Errorf("no .beads directory found")
	}

	// 2. Security: Validate path boundary (SEC-003)
	if !isPathInSafeBoundary(beadsDir) {
		return nil, fmt.Errorf("BEADS_DIR points to unsafe location: %s", beadsDir)
	}

	// 3. Check for redirect
	redirectInfo := GetRedirectInfo()

	// 3. Determine RepoRoot based on redirect status
	var repoRoot string
	if redirectInfo.IsRedirected {
		// BEADS_DIR points to different repo - use that repo's root
		repoRoot = filepath.Dir(beadsDir)
	} else {
		// Normal case - find repo root via git
		var err error
		repoRoot, err = git.GetMainRepoRoot()
		if err != nil {
			return nil, fmt.Errorf("cannot determine repository root: %w", err)
		}
	}

	// 4. Get CWD's repo root (may differ from RepoRoot)
	cwdRepoRoot := git.GetRepoRoot() // Returns "" if not in git repo

	// 5. Check worktree status
	isWorktree := git.IsWorktree()

	return &RepoContext{
		BeadsDir:     beadsDir,
		RepoRoot:     repoRoot,
		CWDRepoRoot:  cwdRepoRoot,
		IsRedirected: redirectInfo.IsRedirected,
		IsWorktree:   isWorktree,
	}, nil
}

// GitCmd creates an exec.Cmd configured to run git in the beads repository.
//
// This method sets cmd.Dir to RepoRoot, ensuring git commands operate on
// the correct repository regardless of CWD.
//
// Security: Git hooks and templates are disabled to prevent code execution
// in potentially malicious repositories (SEC-001, SEC-002).
//
// Pattern:
//
//	cmd := rc.GitCmd(ctx, "add", ".beads/")
//	output, err := cmd.Output()
//
// Equivalent to running: cd $RepoRoot && git add .beads/
//
// GH#2538: When running from a git worktree, git may inherit environment
// variables that point to the worktree's .git instead of the main repo.
// We explicitly set GIT_DIR and GIT_WORK_TREE to ensure git operates on
// the correct repository (the one containing .beads/).
func (rc *RepoContext) GitCmd(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = rc.RepoRoot

	// GH#2538: Ensure git uses the target repository, not the worktree we may be running from.
	// This fixes "pathspec outside repository" errors when bd sync runs from a worktree.
	gitDir := filepath.Join(rc.RepoRoot, ".git")

	// Security: Disable git hooks and templates to prevent code execution
	// in potentially malicious repositories (SEC-001, SEC-002)
	cmd.Env = append(os.Environ(),
		"GIT_HOOKS_PATH=",              // Disable hooks
		"GIT_TEMPLATE_DIR=",            // Disable templates
		"GIT_DIR="+gitDir,              // Ensure git uses the correct .git directory
		"GIT_WORK_TREE="+rc.RepoRoot,   // Ensure git uses the correct work tree
	)
	return cmd
}

// GitCmdCWD creates an exec.Cmd configured to run git in the user's working repository.
//
// Use this for git commands that should reflect the user's current context,
// such as showing status or checking for uncommitted changes in their working repo.
//
// If CWD is not in a git repository, cmd.Dir is left unset (uses process CWD).
func (rc *RepoContext) GitCmdCWD(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	if rc.CWDRepoRoot != "" {
		cmd.Dir = rc.CWDRepoRoot
	}
	return cmd
}

// RelPath returns the given absolute path relative to the beads repository root.
//
// Useful for displaying paths to users in a consistent, repo-relative format.
// Returns an error if the path is not within the repository.
func (rc *RepoContext) RelPath(absPath string) (string, error) {
	return filepath.Rel(rc.RepoRoot, absPath)
}

// ResetCaches clears the cached RepoContext, forcing re-resolution on next call.
//
// This is intended for tests that need to change directory or BEADS_DIR
// between test cases. In production, the cache is safe because these
// values don't change during command execution.
//
// WARNING: Not thread-safe. Only call from single-threaded test contexts.
//
// Usage in tests:
//
//	t.Cleanup(func() {
//	    beads.ResetCaches()
//	    git.ResetCaches()
//	})
func ResetCaches() {
	repoCtxOnce = sync.Once{}
	repoCtx = nil
	repoCtxErr = nil
}

// unsafePrefixes lists system directories that BEADS_DIR should never point to.
// This prevents path traversal attacks (SEC-003).
var unsafePrefixes = []string{
	"/etc", "/usr", "/var", "/root", "/System", "/Library",
	"/bin", "/sbin", "/opt", "/private",
}

// isPathInSafeBoundary validates that a path is not in sensitive system directories.
// Returns false if the path is in an unsafe location (SEC-003).
func isPathInSafeBoundary(path string) bool {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}

	// Allow OS-designated temp directories (e.g., /var/folders on macOS)
	// On macOS, TempDir() returns paths under /var/folders which symlinks to /private/var/folders
	tempDir := os.TempDir()
	resolvedTemp, _ := filepath.EvalSymlinks(tempDir)
	resolvedPath, _ := filepath.EvalSymlinks(absPath)
	if resolvedTemp != "" && strings.HasPrefix(resolvedPath, resolvedTemp) {
		return true
	}
	// Also check unresolved paths (in case symlink resolution fails)
	if strings.HasPrefix(absPath, tempDir) {
		return true
	}

	for _, prefix := range unsafePrefixes {
		if strings.HasPrefix(absPath, prefix+"/") || absPath == prefix {
			return false
		}
	}
	// Also reject other users' home directories
	homeDir, _ := os.UserHomeDir()
	if strings.HasPrefix(absPath, "/Users/") || strings.HasPrefix(absPath, "/home/") {
		if homeDir != "" && !strings.HasPrefix(absPath, homeDir) {
			return false
		}
	}
	return true
}

// GetRepoContextForWorkspace returns a fresh RepoContext for a specific workspace.
//
// Unlike GetRepoContext(), this function:
//   - Does NOT cache results (daemon handles multiple workspaces)
//   - Does NOT respect BEADS_DIR (workspace path is explicit)
//   - Resolves worktree relationships correctly
//
// This is designed for long-running processes like the daemon that need to handle
// multiple workspaces or detect context changes (DMN-001).
//
// The function temporarily changes to the workspace directory to resolve paths,
// then restores the original directory.
func GetRepoContextForWorkspace(workspacePath string) (*RepoContext, error) {
	// Normalize workspace path
	absWorkspace, err := filepath.Abs(workspacePath)
	if err != nil {
		return nil, fmt.Errorf("cannot resolve workspace path %s: %w", workspacePath, err)
	}

	// Change to workspace directory temporarily
	originalDir, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.Chdir(originalDir) }()

	if err := os.Chdir(absWorkspace); err != nil {
		return nil, fmt.Errorf("cannot access workspace %s: %w", absWorkspace, err)
	}

	// Clear git caches for fresh resolution
	git.ResetCaches()

	// Build context fresh, specifically for this workspace (ignores BEADS_DIR)
	return buildRepoContextForWorkspace(absWorkspace)
}

// buildRepoContextForWorkspace constructs RepoContext for a specific workspace.
// Unlike buildRepoContext(), this ignores BEADS_DIR env var since the workspace
// path is explicitly provided (used by daemon).
func buildRepoContextForWorkspace(workspacePath string) (*RepoContext, error) {
	// 1. Determine if we're in a worktree and find the main repo root
	var repoRoot string
	var isWorktree bool

	if git.IsWorktree() {
		isWorktree = true
		var err error
		repoRoot, err = git.GetMainRepoRoot()
		if err != nil {
			return nil, fmt.Errorf("cannot determine main repository root: %w", err)
		}
	} else {
		isWorktree = false
		repoRoot = git.GetRepoRoot()
		if repoRoot == "" {
			return nil, fmt.Errorf("workspace %s is not in a git repository", workspacePath)
		}
	}

	// 2. Find .beads directory in the appropriate location
	beadsDir := filepath.Join(repoRoot, ".beads")

	// Check if .beads exists
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		return nil, fmt.Errorf("no .beads directory found at %s", beadsDir)
	}

	// 3. Follow redirect if present
	beadsDir = FollowRedirect(beadsDir)

	// 4. Security: Validate path boundary (SEC-003)
	if !isPathInSafeBoundary(beadsDir) {
		return nil, fmt.Errorf("beads directory in unsafe location: %s", beadsDir)
	}

	// 5. Validate directory contains actual project files
	if !hasBeadsProjectFiles(beadsDir) {
		return nil, fmt.Errorf("beads directory missing required files: %s", beadsDir)
	}

	// 6. Get CWD's repo root (same as workspace in this case)
	cwdRepoRoot := git.GetRepoRoot()

	return &RepoContext{
		BeadsDir:     beadsDir,
		RepoRoot:     repoRoot,
		CWDRepoRoot:  cwdRepoRoot,
		IsRedirected: false, // Workspace-specific context is never "redirected"
		IsWorktree:   isWorktree,
	}, nil
}

// Validate checks if the cached context is still valid.
//
// Returns an error if BeadsDir or RepoRoot no longer exist. This is useful
// for long-running processes that need to detect when context becomes stale (DMN-002).
func (rc *RepoContext) Validate() error {
	if _, err := os.Stat(rc.BeadsDir); os.IsNotExist(err) {
		return fmt.Errorf("BeadsDir no longer exists: %s", rc.BeadsDir)
	}
	if _, err := os.Stat(rc.RepoRoot); os.IsNotExist(err) {
		return fmt.Errorf("RepoRoot no longer exists: %s", rc.RepoRoot)
	}
	return nil
}

// GitOutput runs a git command in the beads repository and returns its output.
//
// This is a convenience wrapper around GitCmd that captures stdout.
// Returns an error if the command fails or produces no output.
//
// Pattern:
//
//	output, err := rc.GitOutput(ctx, "config", "--get", "beads.role")
//	if err != nil {
//	    // Config key not set or git error
//	}
func (rc *RepoContext) GitOutput(ctx context.Context, args ...string) (string, error) {
	cmd := rc.GitCmd(ctx, args...)
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(output), nil
}

// Role reads beads.role from git config (fresh each call, ~1ms).
//
// If BEADS_DIR is set (IsRedirected), returns Contributor implicitly
// because external repo mode always indicates a contributor workflow.
//
// Returns ("", false) if role is not configured and not redirected.
// The bool return indicates whether a role was determined.
func (rc *RepoContext) Role() (UserRole, bool) {
	// BEADS_DIR implies contributor (external repo mode)
	if rc.IsRedirected {
		return Contributor, true
	}

	output, err := rc.GitOutput(context.Background(), "config", "--get", "beads.role")
	if err != nil {
		return "", false // Not configured
	}
	return UserRole(strings.TrimSpace(output)), true
}

// IsContributor returns true if user is configured as contributor.
//
// This includes both explicit configuration (git config beads.role contributor)
// and implicit detection (BEADS_DIR redirect active).
func (rc *RepoContext) IsContributor() bool {
	role, ok := rc.Role()
	return ok && role == Contributor
}

// IsMaintainer returns true if user is configured as maintainer.
//
// Only returns true for explicit configuration (git config beads.role maintainer).
// BEADS_DIR redirect always implies contributor, never maintainer.
func (rc *RepoContext) IsMaintainer() bool {
	role, ok := rc.Role()
	return ok && role == Maintainer
}

// RequireRole returns error if role not configured (forces init prompt).
//
// Use this at command entry points that need role-aware behavior.
// If BEADS_DIR is set, role is implicitly determined (contributor),
// so this will not return an error.
func (rc *RepoContext) RequireRole() error {
	if _, ok := rc.Role(); !ok {
		return ErrRoleNotConfigured
	}
	return nil
}
