package routing

import (
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
// 1. Check if user has push access to origin (git remote -v shows write URL)
// 2. Check git config for beads.role setting (explicit override)
// 3. Fall back to maintainer for local projects (no remote configured)
func DetectUserRole(repoPath string) (UserRole, error) {
	// First check for explicit role in git config
	output, err := gitCommandRunner(repoPath, "config", "--get", "beads.role")
	if err == nil {
		role := strings.TrimSpace(string(output))
		if role == string(Maintainer) {
			return Maintainer, nil
		}
		if role == string(Contributor) {
			return Contributor, nil
		}
	}

	// Check push access by examining remote URL
	output, err = gitCommandRunner(repoPath, "remote", "get-url", "--push", "origin")
	if err != nil {
		// Fallback to standard fetch URL if push URL fails (some git versions/configs)
		output, err = gitCommandRunner(repoPath, "remote", "get-url", "origin")
		if err != nil {
			// No remote means local project - default to maintainer
			return Maintainer, nil
		}
	}

	pushURL := strings.TrimSpace(string(output))

	// Check if URL indicates write access
	// SSH URLs (git@github.com:user/repo.git) typically indicate write access
	// HTTPS with token/password also indicates write access
	if strings.HasPrefix(pushURL, "git@") ||
		strings.HasPrefix(pushURL, "ssh://") ||
		strings.Contains(pushURL, "@") {
		return Maintainer, nil
	}

	// HTTPS without credentials likely means read-only contributor
	return Contributor, nil
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
