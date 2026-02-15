package doctor

import (
	"github.com/steveyegge/beads/internal/configfile"
)

// CheckBackendMigration checks if the backend needs migration from SQLite to Dolt.
// As of v0.50, Dolt is the default backend. All SQLite users are nudged to migrate.
func CheckBackendMigration(path string) DoctorCheck {
	backend, _ := getBackendAndBeadsDir(path) // Best effort: empty backend handled by caller

	// Already on Dolt — all good
	if backend == configfile.BackendDolt {
		return DoctorCheck{
			Name:     "Backend Migration",
			Status:   StatusOK,
			Message:  "Backend: Dolt (current default)",
			Category: CategoryCore,
		}
	}

	// SQLite backend → recommend migration
	return DoctorCheck{
		Name:     "Backend Migration",
		Status:   StatusWarning,
		Message:  "SQLite backend detected. bd v0.50+ defaults to Dolt for better versioning and multi-agent support.",
		Fix:      "Run 'bd migrate --to-dolt' to upgrade (your data will be fully preserved, backup created automatically)",
		Category: CategoryCore,
	}
}
