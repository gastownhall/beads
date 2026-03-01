package doctor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// PendingMigration represents a single pending migration
type PendingMigration struct {
	Name        string // e.g., "sync"
	Description string // e.g., "Configure sync branch for multi-clone setup"
	Command     string // e.g., "bd migrate sync beads-sync"
	Priority    int    // 1 = critical, 2 = recommended, 3 = optional
}

// DetectPendingMigrations detects all pending migrations for a beads directory
func DetectPendingMigrations(path string) []PendingMigration {
	var pending []PendingMigration

	// Follow redirect to resolve actual beads directory
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))

	// Skip if .beads doesn't exist
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		return pending
	}

	hookPlan, err := PlanHookMigration(path)
	if err == nil && hookPlan.IsGitRepo && hookPlan.NeedsMigrationCount > 0 {
		description := fmt.Sprintf("Git hook migration needed for %d hook(s)", hookPlan.NeedsMigrationCount)
		priority := 2
		if hookPlan.BrokenMarkerCount > 0 {
			description = fmt.Sprintf(
				"%s (%d with broken markers)",
				description,
				hookPlan.BrokenMarkerCount,
			)
			// Phase 1 is planning-only. Keep this at warning level for now.
			// TODO(gh-1380): raise to critical when apply mode is implemented.
			priority = 2
		}

		pending = append(pending, PendingMigration{
			Name:        "hooks",
			Description: description,
			Command:     "bd migrate hooks --dry-run",
			Priority:    priority,
		})
	}

	return pending
}

// CheckPendingMigrations returns a doctor check summarizing all pending migrations
func CheckPendingMigrations(path string) DoctorCheck {
	pending := DetectPendingMigrations(path)

	if len(pending) == 0 {
		return DoctorCheck{
			Name:     "Pending Migrations",
			Status:   StatusOK,
			Message:  "None required",
			Category: CategoryMaintenance,
		}
	}

	// Build message with count
	message := fmt.Sprintf("%d available", len(pending))

	// Build detail with list of migrations
	var details []string
	var fixes []string
	for _, m := range pending {
		priority := ""
		switch m.Priority {
		case 1:
			priority = " [critical]"
		case 2:
			priority = " [recommended]"
		case 3:
			priority = " [optional]"
		}
		details = append(details, fmt.Sprintf("• %s: %s%s", m.Name, m.Description, priority))
		fixes = append(fixes, m.Command)
	}

	// Determine status based on highest priority migration
	status := StatusOK
	for _, m := range pending {
		if m.Priority == 1 {
			status = StatusError
			break
		} else if m.Priority == 2 && status != StatusError {
			status = StatusWarning
		}
	}

	return DoctorCheck{
		Name:     "Pending Migrations",
		Status:   status,
		Message:  message,
		Detail:   strings.Join(details, "\n"),
		Fix:      strings.Join(fixes, "\n"),
		Category: CategoryMaintenance,
	}
}

// hasGitRemote checks if the repository has a git remote
func hasGitRemote(repoPath string) bool {
	cmd := exec.Command("git", "remote")
	cmd.Dir = repoPath
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	return len(strings.TrimSpace(string(output))) > 0
}
