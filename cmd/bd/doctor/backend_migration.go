package doctor

import (
	"os"
	"path/filepath"

	"github.com/steveyegge/beads/internal/configfile"
	"gopkg.in/yaml.v3"
)

// CheckBackendMigration checks if the backend needs migration from SQLite to Dolt.
// This is triggered when prefer-dolt: true is set in config.yaml but the backend is still SQLite.
func CheckBackendMigration(path string) DoctorCheck {
	backend, beadsDir := getBackendAndBeadsDir(path)

	// Already on Dolt — all good
	if backend == configfile.BackendDolt {
		return DoctorCheck{
			Name:     "Backend Migration",
			Status:   StatusOK,
			Message:  "Backend: Dolt",
			Category: CategoryCore,
		}
	}

	// SQLite backend — check if prefer-dolt is configured
	if !isPreferDoltConfigured(beadsDir) {
		return DoctorCheck{
			Name:     "Backend Migration",
			Status:   StatusOK,
			Message:  "Backend: SQLite",
			Category: CategoryCore,
		}
	}

	// SQLite + prefer-dolt set → recommend migration
	return DoctorCheck{
		Name:     "Backend Migration",
		Status:   StatusWarning,
		Message:  "SQLite backend detected, Dolt migration recommended",
		Fix:      "Run 'bd migrate dolt' to upgrade to Dolt backend",
		Category: CategoryCore,
	}
}

// isPreferDoltConfigured checks if prefer-dolt: true is set in config.yaml.
// Uses proper YAML parsing following the isNoDbModeConfigured pattern.
func isPreferDoltConfigured(beadsDir string) bool {
	configPath := filepath.Join(beadsDir, "config.yaml")
	data, err := os.ReadFile(configPath) // #nosec G304 - config file path from beadsDir
	if err != nil {
		return false
	}

	var cfg struct {
		PreferDolt bool `yaml:"prefer-dolt"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return false
	}

	return cfg.PreferDolt
}
