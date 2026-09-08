package routing

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/git"
)

var gitCommandRunner = func(repoPath string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	if repoPath != "" {
		cmd.Dir = repoPath
	}
	return cmd.Output()
}

// UserRole represents whether the user is a maintainer or contributor
type UserRole string

const (
	Maintainer  UserRole = "maintainer"
	Contributor UserRole = "contributor"
)

// DetectUserRole determines if the user is a maintainer or contributor
// based on git configuration and repository permissions.
//
// Detection strategy:
// 1. Check git config for beads.role setting (preferred source of truth)
// 2. Fall back to URL heuristic with deprecation warning (graceful degradation)
// 3. Default to maintainer for local projects (no remote configured)
func DetectUserRole(repoPath string) (UserRole, error) {
	// First check for explicit role in git config (preferred source)
	if role, ok := roleFromGitConfig(repoPath); ok {
		return role, nil
	}

	// jj secondary workspaces have no .git of their own, so the lookup above
	// fails even when the primary workspace has beads.role set. Resolve the
	// primary workspace and retry there, then run the remaining fallbacks
	// against the primary's git repo since the secondary has no usable git
	// context. The resolution is anchored at repoPath (not cwd) so it stays
	// consistent with the git-config lookup above. (GH#2950)
	if _, isSecondary := git.JJSecondaryWorkspaceRootFrom(repoPath); isSecondary {
		if primaryRoot, err := git.GetJJPrimaryWorkspaceRootFrom(repoPath); err == nil {
			if role, ok := roleFromGitConfig(primaryRoot); ok {
				return role, nil
			}
			repoPath = primaryRoot
		}
	}

	// Fallback to URL heuristic (deprecated, with warning)
	// This keeps existing users working while encouraging migration
	fmt.Fprintln(os.Stderr, "warning: beads.role not configured (GH#2950).")
	fmt.Fprintln(os.Stderr, "  Fix: git config beads.role maintainer")
	fmt.Fprintln(os.Stderr, "  Or:  git config beads.role contributor")
	return detectFromURL(repoPath), nil
}

// roleFromGitConfig reads beads.role from the git config of repoPath.
// Returns (role, true) only for a valid explicit value; (_, false) when the
// config is unset, git is unavailable, or the value is not a recognized role.
func roleFromGitConfig(repoPath string) (UserRole, bool) {
	output, err := gitCommandRunner(repoPath, "config", "--get", "beads.role")
	if err != nil {
		return "", false
	}
	switch UserRole(strings.TrimSpace(string(output))) {
	case Maintainer:
		return Maintainer, true
	case Contributor:
		return Contributor, true
	}
	return "", false
}

// detectFromURL uses remote URL patterns to infer user role.
// This heuristic is deprecated - SSH URLs don't reliably indicate write access
// (e.g., fork contributors often use SSH).
func detectFromURL(repoPath string) UserRole {
	originURL, hasOrigin := getOriginURL(repoPath)
	if !hasOrigin {
		// No remote means local project - default to maintainer
		return Maintainer
	}

	// Fork heuristic: if both origin and upstream are configured and point to
	// different repos, user is almost certainly contributing via fork workflow.
	if upstreamOutput, err := gitCommandRunner(repoPath, "remote", "get-url", "upstream"); err == nil {
		upstreamURL := strings.TrimSpace(string(upstreamOutput))
		if upstreamURL != "" && !sameRemoteRepository(originURL, upstreamURL) {
			return Contributor
		}
	}

	// Check if URL indicates write access
	// SSH URLs (git@github.com:user/repo.git) typically indicate write access
	// HTTPS with token/password also indicates write access
	if strings.HasPrefix(originURL, "git@") ||
		strings.HasPrefix(originURL, "ssh://") ||
		strings.Contains(originURL, "@") {
		return Maintainer
	}

	// HTTPS without credentials likely means read-only contributor
	return Contributor
}

func getOriginURL(repoPath string) (string, bool) {
	output, err := gitCommandRunner(repoPath, "remote", "get-url", "--push", "origin")
	if err != nil {
		// Fallback to standard fetch URL if push URL fails (some git versions/configs)
		output, err = gitCommandRunner(repoPath, "remote", "get-url", "origin")
		if err != nil {
			return "", false
		}
	}
	return strings.TrimSpace(string(output)), true
}

func sameRemoteRepository(a, b string) bool {
	slugA, okA := remoteRepositorySlug(a)
	slugB, okB := remoteRepositorySlug(b)
	if okA && okB {
		return slugA == slugB
	}
	return strings.TrimSpace(a) == strings.TrimSpace(b)
}

func remoteRepositorySlug(remote string) (string, bool) {
	remote = strings.TrimSpace(strings.TrimSuffix(remote, "/"))
	if remote == "" {
		return "", false
	}

	// SCP-like URL: git@github.com:owner/repo.git
	if strings.Contains(remote, "@") && strings.Contains(remote, ":") && !strings.Contains(remote, "://") {
		parts := strings.SplitN(remote, ":", 2)
		if len(parts) != 2 {
			return "", false
		}
		return normalizeRemotePath(parts[1])
	}

	u, err := url.Parse(remote)
	if err != nil {
		return "", false
	}
	return normalizeRemotePath(u.Path)
}

func normalizeRemotePath(path string) (string, bool) {
	path = strings.TrimSpace(strings.Trim(path, "/"))
	path = strings.TrimSuffix(path, ".git")
	if path == "" {
		return "", false
	}
	return path, true
}

// RoutingConfig defines routing rules for issues
type RoutingConfig struct {
	Mode             string // "auto" or "explicit"
	DefaultRepo      string // Default repo for new issues
	MaintainerRepo   string // Repo for maintainers (in auto mode)
	ContributorRepo  string // Repo for contributors (in auto mode)
	ExplicitOverride string // Explicit --repo flag override
}

// DetermineTargetRepo determines which repo should receive a new issue
// based on routing configuration and user role
func DetermineTargetRepo(config *RoutingConfig, userRole UserRole, repoPath string) string {
	// Explicit override takes precedence
	if config.ExplicitOverride != "" {
		return config.ExplicitOverride
	}

	// Auto mode: route based on user role
	if config.Mode == "auto" {
		if userRole == Maintainer && config.MaintainerRepo != "" {
			return config.MaintainerRepo
		}
		if userRole == Contributor && config.ContributorRepo != "" {
			return config.ContributorRepo
		}
	}

	// Fall back to default repo
	if config.DefaultRepo != "" {
		return config.DefaultRepo
	}

	// No routing configured - use current repo
	return "."
}

// ExpandPath expands ~ to home directory and resolves relative paths to absolute.
// Returns the original path if expansion fails.
func ExpandPath(path string) string {
	if path == "" || path == "." {
		return path
	}

	// Expand ~ to home directory
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			path = filepath.Join(home, path[2:])
		}
	}

	// Convert relative paths to absolute
	if !filepath.IsAbs(path) {
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
	}

	return path
}
