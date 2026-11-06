package doctor

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
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
	// Read beads.role from git config (canonical location)
	role, present, err := readGitBeadsRole(path)
	if err != nil {
		return checkBeadsRoleReadFailed(err)
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
	// Read beads.role from git config (canonical location)
	role, present, err := readGitBeadsRole(path)
	if err != nil {
		return checkBeadsRoleReadFailed(err)
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

// readGitBeadsRole is the one beads.role read both entry points share. A
// successful empty value stays present, so validateRole reports it as invalid
// rather than letting the database fallback answer for it.
//
// A read that git fails (a malformed or unreadable config file, for example)
// is returned as an error. The exceptions are reads git never ran, which go on
// to the database fallback and then N/A, as they always have: no git
// executable (git-free usage), or a supplied path that does not exist.
func readGitBeadsRole(path string) (role string, present bool, err error) {
	cmd := exec.Command("git", "config", "--get", "beads.role")
	cmd.Env = gitenv.ScrubRoleRead(os.Environ())
	if path != "" {
		cmd.Dir = path
	}
	output, runErr := cmd.Output()
	role, present, err = git.ClassifyConfigGet(output, runErr)
	if git.IsExecutableNotFound(err) {
		return "", false, nil
	}
	if err != nil && path != "" {
		if _, statErr := os.Stat(path); errors.Is(statErr, fs.ErrNotExist) {
			return "", false, nil
		}
	}
	return role, present, err
}

// beadsRoleReadFailedMessage is the Message of checkBeadsRoleReadFailed. The
// agent enricher keys on it, so a read failure gets its own remediation
// rather than the role write that suits an unset role.
const beadsRoleReadFailedMessage = "beads.role could not be read from git config"

// checkBeadsRoleReadFailed reports a beads.role read git could not complete.
// It is decided before the database fallback and the repository probe: both
// would otherwise answer for a config git cannot parse (the probe fails on the
// same file and reports "not a git repository"), and `bd init` cannot repair
// a corrupt config file.
func checkBeadsRoleReadFailed(err error) DoctorCheck {
	return DoctorCheck{
		Name:     "Role Configuration",
		Status:   StatusWarning,
		Message:  beadsRoleReadFailedMessage,
		Detail:   fmt.Sprintf("%v. Routing cannot use the role until git can read its config files.", err),
		Fix:      "git config --list --show-origin",
		Category: CategoryData,
	}
}

func checkBeadsRoleNotInGit(path string) DoctorCheck {
	// Check if we're even in a git repository. If not, skip the check rather
	// than warn about missing config that may be correctly set in a worktree
	// (e.g., rig roots use .repo.git instead of .git).
	if !isGitRepo(path) {
		return DoctorCheck{
			Name:     "Role Configuration",
			Status:   StatusOK,
			Message:  "N/A (not a git repository)",
			Category: CategoryData,
		}
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
