package doctor

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
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

	// Check for missing sync-branch config (sync migration)
	if needsSyncMigration(path) {
		pending = append(pending, PendingMigration{
			Name:        "sync",
			Description: "Configure sync branch for multi-clone setup",
			Command:     "bd migrate sync beads-sync",
			Priority:    3,
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

// needsSyncMigration always returns false now that sync-branch is removed.
func needsSyncMigration(repoPath string) bool {
	return false
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

// checkDatabaseVersionMismatch returns a description if database version is old
func checkDatabaseVersionMismatch(beadsDir string) string {
	var dbPath string
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil && cfg.Database != "" {
		dbPath = cfg.DatabasePath(beadsDir)
	} else {
		dbPath = filepath.Join(beadsDir, beads.CanonicalDatabaseName)
	}

	// Skip if no database
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return ""
	}

	db, err := sql.Open("sqlite3", sqliteConnString(dbPath, true))
	if err != nil {
		return ""
	}
	defer db.Close()

	// Get stored version
	var storedVersion string
	err = db.QueryRow("SELECT value FROM metadata WHERE key = 'bd_version'").Scan(&storedVersion)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return "Database schema needs update (pre-metadata table)"
		}
		// No version stored
		return ""
	}

	// Note: We can't compare to current version here since we don't have access
	// to the Version variable from main package. The individual check does this.
	// This function is just for detecting obviously old databases.
	if storedVersion == "" || storedVersion == "unknown" {
		return "Database version unknown"
	}

	return ""
}
