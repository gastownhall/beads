package doctor

import (
	"fmt"
	"os/exec"
	"strings"
)

// CheckBeadsRole verifies that beads.role is configured in git config.
// This check helps users migrate from the deprecated URL-heuristic role detection
// to explicit configuration.
func CheckBeadsRole(path string) DoctorCheck {
	// Read beads.role from git config
	cmd := exec.Command("git", "config", "--get", "beads.role")
	if path != "" {
		cmd.Dir = path
	}
	output, err := cmd.Output()

	if err != nil {
		// Config not set - this is a warning, not an error
		// Existing users can still work with URL heuristic fallback
		return DoctorCheck{
			Name:     "Role Configuration",
			Status:   StatusWarning,
			Message:  "beads.role not configured",
			Detail:   "Run 'bd init' to configure your role (maintainer or contributor).",
			Fix:      "bd init",
			Category: CategoryData,
		}
	}

	role := strings.TrimSpace(string(output))

	// Validate the role value
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
