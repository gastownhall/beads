package routing

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/gitenv"
)

// Role detection is an authority lookup, so it also discards inherited config
// suppression: detectFromURL below answers a missed read with Maintainer, which
// would let a caller escalate by blinding the lookup rather than forging it.
var gitCommandRunner = func(repoPath string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Env = gitenv.ScrubRoutingAndSuppression(os.Environ())
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
//
// A read that genuinely fails — a malformed or unreadable config, a permission
// denial, a cancellation — returns ("", err) before any fallback: an
// unreadable authority source must not be reported as an unconfigured role and
// answered by heuristic. Two narrow cases keep the documented unavailable-probe
// fallback (gitProbeUnavailable): git itself is missing, or the working path
// names no directory. A missing or unrecognized value still falls through to
// the notice and heuristic below.
func DetectUserRole(repoPath string) (UserRole, error) {
	// First check for explicit role in git config (preferred source)
	if role, ok, err := roleFromGitConfig(repoPath); err != nil {
		if !gitProbeUnavailable(repoPath, err) {
			return "", err
		}
	} else if ok {
		return role, nil
	}

	// jj secondary workspaces have no .git of their own, so the lookup above
	// misses even when the primary workspace has beads.role set. Resolve the
	// primary workspace and retry there, then run the remaining fallbacks
	// against the primary's git repo since the secondary has no usable git
	// context. The resolution is anchored at repoPath (not cwd) so it stays
	// consistent with the git-config lookup above. (GH#2950)
	if _, isSecondary := git.JJSecondaryWorkspaceRootFrom(repoPath); isSecondary {
		if primaryRoot, err := git.GetJJPrimaryWorkspaceRootFrom(repoPath); err == nil {
			if role, ok, err := roleFromGitConfig(primaryRoot); err != nil {
				if !gitProbeUnavailable(primaryRoot, err) {
					return "", err
				}
			} else if ok {
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

// gitProbeUnavailable reports whether a failed role read means the optional
// probe could not run at all, rather than a selected repository answering with
// a failure. Only two cases qualify: the git executable is missing
// (exec.ErrNotFound through exec.Error), and the working path naming no
// directory to run in (a chdir failure). Every other failure — a permission
// denial, a cancellation, a malformed config — is a real answer from a real
// probe and stays an error. This is routing's compatibility exception for its
// documented unavailable-git fallback (TestDetectUserRole_Fallback); the
// shared classifier and the domain adapter remain strict.
func gitProbeUnavailable(repoPath string, err error) bool {
	if errors.Is(err, exec.ErrNotFound) {
		return true
	}
	if repoPath == "" {
		return false
	}
	info, statErr := os.Stat(repoPath)
	return errors.Is(statErr, os.ErrNotExist) || (statErr == nil && !info.IsDir())
}

// roleFromGitConfig reads beads.role from the git config of repoPath.
// Returns (role, true, nil) only for a valid explicit value; ("", false, nil)
// when the config is unset or the value is not a recognized role; and
// ("", false, err) when the read itself failed, classified by the shared rule
// every beads.role reader uses. Whether a read failure is fatal or an
// optional-probe miss is DetectUserRole's decision (gitProbeUnavailable), not
// this reader's.
func roleFromGitConfig(repoPath string) (UserRole, bool, error) {
	output, err := gitCommandRunner(repoPath, "config", "--get", "beads.role")
	value, present, readErr := git.ClassifyConfigGet(output, err)
	if readErr != nil {
		return "", false, readErr
	}
	if !present {
		return "", false, nil
	}
	switch UserRole(value) {
	case Maintainer:
		return Maintainer, true, nil
	case Contributor:
		return Contributor, true, nil
	}
	return "", false, nil
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

// RoutingRule identifies which clause of DetermineTargetRepo matched, so
// callers can explain *why* a repo was routed instead of assuming a single
// cause (e.g. the contributor-role rule).
type RoutingRule int

const (
	// RuleNone means no routing rule matched; the current repo was used.
	RuleNone RoutingRule = iota
	// RuleExplicitOverride means config.ExplicitOverride took precedence.
	RuleExplicitOverride
	// RuleMaintainer means auto mode routed via MaintainerRepo for a
	// maintainer-role user.
	RuleMaintainer
	// RuleContributor means auto mode routed via ContributorRepo for a
	// contributor-role user.
	RuleContributor
	// RuleDefault means the unconditional DefaultRepo fallback matched,
	// independent of user role.
	RuleDefault
)

// DetermineTargetRepo determines which repo should receive a new issue
// based on routing configuration and user role.
func DetermineTargetRepo(config *RoutingConfig, userRole UserRole, repoPath string) string {
	repo, _ := DetermineTargetRepoWithRule(config, userRole, repoPath)
	return repo
}

// DetermineTargetRepoWithRule is DetermineTargetRepo, but also reports which
// rule matched so callers can produce an accurate diagnosis (e.g. a notice
// explaining why a read was routed elsewhere) instead of assuming a single
// hardcoded cause.
func DetermineTargetRepoWithRule(config *RoutingConfig, userRole UserRole, repoPath string) (string, RoutingRule) {
	// Explicit override takes precedence
	if config.ExplicitOverride != "" {
		return config.ExplicitOverride, RuleExplicitOverride
	}

	// Auto mode: route based on user role. UsesUserRole mirrors this clause;
	// change the two together.
	if config.Mode == "auto" {
		if userRole == Maintainer && config.MaintainerRepo != "" {
			return config.MaintainerRepo, RuleMaintainer
		}
		if userRole == Contributor && config.ContributorRepo != "" {
			return config.ContributorRepo, RuleContributor
		}
	}

	// Fall back to default repo
	if config.DefaultRepo != "" {
		return config.DefaultRepo, RuleDefault
	}

	// No routing configured - use current repo
	return ".", RuleNone
}

// UsesUserRole reports whether DetermineTargetRepoWithRule's answer for c can
// depend on the user role: only auto mode consults it, only when no explicit
// override wins first, and only when a role-specific repo is configured.
// Callers use it to skip role detection, which runs git and can print
// diagnostics, when the detected role would be discarded.
func (c *RoutingConfig) UsesUserRole() bool {
	return c.ExplicitOverride == "" && c.Mode == "auto" &&
		(c.MaintainerRepo != "" || c.ContributorRepo != "")
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
