package doctor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/gitenv"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

// CheckBeadsRole verifies that beads.role is configured.
// Checks git config first (canonical location), then falls back to the
// database config for users who ran "bd config set role maintainer"
// (without the "beads." prefix) or "bd config set beads.role maintainer"
// before GH#1531 moved storage to git config.
// Opens its own store; prefer CheckBeadsRoleWithStore when a shared store is available.
func CheckBeadsRole(path string) DoctorCheck {
	if suppliedPathMissing(path) {
		return notAGitRepoNA()
	}

	// Read beads.role from git config (canonical location)
	role, present, readErr := readBeadsRoleFromGit(path)
	if readErr != nil {
		return roleReadFailureCheck(readErr)
	}
	if present {
		return validateRole(role)
	}

	// Git config not set — check database config as fallback.
	// Users may have set it via "bd config set role maintainer" (stored in Dolt)
	// or the git config may be unavailable (e.g., worktree without local config).
	if role := getRoleFromDatabase(path); role != "" {
		return validateRole(role)
	}

	return checkBeadsRoleNotInGit(path)
}

// CheckBeadsRoleWithStore verifies beads.role using a shared store (GH#2636).
func CheckBeadsRoleWithStore(path string, ss *SharedStore) DoctorCheck {
	if suppliedPathMissing(path) {
		return notAGitRepoNA()
	}

	// Read beads.role from git config (canonical location)
	role, present, readErr := readBeadsRoleFromGit(path)
	if readErr != nil {
		return roleReadFailureCheck(readErr)
	}
	if present {
		return validateRole(role)
	}

	// Git config not set — check database config via shared store
	if role := getRoleFromStore(ss.Store()); role != "" {
		return validateRole(role)
	}

	return checkBeadsRoleNotInGit(path)
}

// readBeadsRoleFromGit is the one local beads.role read both entry points
// share, on the boundary they already shared: the supplied path (or the
// process directory) and the routing-and-suppression-scrubbed environment.
// Classification is the shared rule every beads.role reader uses
// (internal/git.ClassifyConfigGet), so a config git cannot parse is a read
// failure here, not an unconfigured role. Doctor validates a present empty
// value as invalid (validateRole), which the domain adapter's
// absence-collapsing semantics cannot express — hence the classifier rather
// than the use case.
func readBeadsRoleFromGit(path string) (string, bool, error) {
	cmd := exec.Command("git", "config", "--get", "beads.role")
	cmd.Env = gitenv.ScrubRoutingAndSuppression(os.Environ())
	if path != "" {
		cmd.Dir = path
	}
	out, err := cmd.Output()
	return git.ClassifyConfigGet(out, err)
}

// suppliedPathMissing reports whether an explicitly supplied path does not
// exist. The check is deliberately narrow: only os.ErrNotExist keeps the
// existing N/A answer for a path that names nothing. A permission error, or
// an existing directory whose config-sensitive rev-parse fails, is a real
// condition to report, not this exception.
func suppliedPathMissing(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return errors.Is(err, os.ErrNotExist)
}

// roleReadFailureCheck reports a failed beads.role read as a warning naming
// git's diagnostic, before the database fallback or the repository probe:
// with a malformed config neither can answer (rev-parse fails on the same
// parse error, which is what used to turn this into "N/A (not a git
// repository)"). Init cannot repair a config git cannot parse, so the
// guidance points at inspecting the config's origin instead.
func roleReadFailureCheck(readErr error) DoctorCheck {
	return DoctorCheck{
		Name:     "Role Configuration",
		Status:   StatusWarning,
		Message:  fmt.Sprintf("Failed to read beads.role from git config: %v", readErr),
		Detail:   "Git could not read its configuration. Inspect it with 'git config --list --show-origin' and repair the file it names.",
		Category: CategoryData,
	}
}

func notAGitRepoNA() DoctorCheck {
	return DoctorCheck{
		Name:     "Role Configuration",
		Status:   StatusOK,
		Message:  "N/A (not a git repository)",
		Category: CategoryData,
	}
}

func checkBeadsRoleNotInGit(path string) DoctorCheck {
	// Check if we're even in a git repository. If not, skip the check rather
	// than warn about missing config that may be correctly set in a worktree
	// (e.g., rig roots use .repo.git instead of .git).
	if !isGitRepo(path) {
		return notAGitRepoNA()
	}

	// Neither git config nor database has the role configured
	return DoctorCheck{
		Name:     "Role Configuration",
		Status:   StatusWarning,
		Message:  "beads.role not configured",
		Detail:   "Role must be set for routing to work. Run one of the commands below.",
		Fix:      "git config beads.role maintainer",
		Category: CategoryData,
	}
}

// isGitRepo checks whether the given path is inside a git repository.
//
// Shares the boundary of the two role reads above, which is what this probe
// gates: rev-parse is config-sensitive through safe.directory, so retaining
// suppression here could answer false for a repository that is only reachable
// via a global safe.directory entry and silently downgrade the "beads.role not
// configured" warning to "N/A (not a git repository)".
func isGitRepo(path string) bool {
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	cmd.Env = gitenv.ScrubRoutingAndSuppression(os.Environ())
	if path != "" {
		cmd.Dir = path
	}
	return cmd.Run() == nil
}

// validateRole checks that the role value is valid and returns the appropriate check.
func validateRole(role string) DoctorCheck {
	if role != "maintainer" && role != "contributor" {
		return DoctorCheck{
			Name:     "Role Configuration",
			Status:   StatusWarning,
			Message:  fmt.Sprintf("Invalid beads.role value: %q", role),
			Detail:   "Valid values are 'maintainer' or 'contributor'. Run 'bd init' to reconfigure.",
			Fix:      "bd init",
			Category: CategoryData,
		}
	}

	return DoctorCheck{
		Name:     "Role Configuration",
		Status:   StatusOK,
		Message:  fmt.Sprintf("Configured as %s", role),
		Category: CategoryData,
	}
}

// getRoleFromDatabase checks for role in the database config.
// Checks both "beads.role" and "role" keys since users may have used either.
func getRoleFromDatabase(path string) string {
	_, beadsDir := getBackendAndBeadsDir(path)
	if beadsDir == "" {
		return ""
	}

	ctx := context.Background()
	store, err := dolt.NewFromConfigWithCLIOptions(ctx, beadsDir, &dolt.Config{ReadOnly: true})
	if err != nil {
		return ""
	}
	defer func() { _ = store.Close() }()

	return getRoleFromStore(store)
}

// getRoleFromStore checks for role in the database config using a provided store.
func getRoleFromStore(store *dolt.DoltStore) string {
	if store == nil {
		return ""
	}

	ctx := context.Background()
	// Check "beads.role" first, then "role"
	for _, key := range []string{"beads.role", "role"} {
		if val, err := store.GetConfig(ctx, key); err == nil && val != "" {
			return strings.TrimSpace(val)
		}
	}

	return ""
}
