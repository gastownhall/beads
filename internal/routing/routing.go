package routing

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	output, err := gitCommandRunner(repoPath, "config", "--get", "beads.role")
	if err == nil {
		role := strings.TrimSpace(string(output))
		if role == string(Maintainer) {
			return Maintainer, nil
		}
		if role == string(Contributor) {
			return Contributor, nil
		}
		// Invalid role value - fall through with warning
	}

	// Fallback to URL heuristic (deprecated, with warning)
	// This keeps existing users working while encouraging migration
	fmt.Fprintln(os.Stderr, "warning: beads.role not configured. Run 'bd init' to set.")
	return detectFromURL(repoPath), nil
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
