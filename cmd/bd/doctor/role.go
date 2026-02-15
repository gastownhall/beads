package doctor

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/steveyegge/beads/internal/storage/dolt"
)

// CheckBeadsRole verifies that beads.role is configured.
// Checks git config first (canonical location), then falls back to the
// database config for users who ran "bd config set role maintainer"
// (without the "beads." prefix) or "bd config set beads.role maintainer"
// before GH#1531 moved storage to git config.
func CheckBeadsRole(path string) DoctorCheck {
	// Read beads.role from git config (canonical location)
	cmd := exec.Command("git", "config", "--get", "beads.role")
	if path != "" {
		cmd.Dir = path
	}
	output, err := cmd.Output()

	if err == nil {
		role := strings.TrimSpace(string(output))
		return validateRole(role)
	}

	// Git config not set — check database config as fallback.
	// Users may have set it via "bd config set role maintainer" (stored in SQLite)
	// or the git config may be unavailable (e.g., worktree without local config).
	if role := getRoleFromDatabase(path); role != "" {
		return validateRole(role)
	}

	// Neither git config nor database has the role configured
	return DoctorCheck{
		Name:     "Role Configuration",
		Status:   StatusWarning,
		Message:  "beads.role not configured",
		Detail:   "Run 'bd init' to configure your role (maintainer or contributor).",
		Fix:      "bd config set beads.role maintainer",
		Category: CategoryData,
	}
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
	store, err := dolt.NewFromConfigWithOptions(ctx, beadsDir, &dolt.Config{ReadOnly: true})
	if err != nil {
		return ""
	}
	defer func() { _ = store.Close() }()

	// Check "beads.role" first, then "role"
	for _, key := range []string{"beads.role", "role"} {
		if val, err := store.GetConfig(ctx, key); err == nil && val != "" {
			return strings.TrimSpace(val)
		}
	}

	return ""
}
