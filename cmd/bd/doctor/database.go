package doctor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/steveyegge/beads/cmd/bd/doctor/fix"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"gopkg.in/yaml.v3"
)

// localConfig represents the config.yaml structure for no-db and prefer-dolt detection
type localConfig struct {
	SyncBranch string `yaml:"sync-branch"`
	NoDb       bool   `yaml:"no-db"`
	PreferDolt bool   `yaml:"prefer-dolt"`
}

// CheckDatabaseVersion checks the database version and migration status
func CheckDatabaseVersion(path string, cliVersion string) DoctorCheck {
	backend, beadsDir := getBackendAndBeadsDir(path)

	if backend != configfile.BackendDolt {
		return sqliteBackendWarning("Database")
	}

	doltPath := getDatabasePath(beadsDir)
	if _, err := os.Stat(doltPath); os.IsNotExist(err) {
		return DoctorCheck{
			Name:    "Database",
			Status:  StatusError,
			Message: "No dolt database found",
			Detail:  "Storage: Dolt",
			Fix:     "Run 'bd init' to create database (will clone from remote if configured)",
		}
	}

	ctx := context.Background()
	store, err := dolt.NewFromConfigWithOptions(ctx, beadsDir, &dolt.Config{ReadOnly: true})
	if err != nil {
		return DoctorCheck{
			Name:    "Database",
			Status:  StatusError,
			Message: "Unable to open database",
			Detail:  fmt.Sprintf("Storage: Dolt\n\nError: %v", err),
			Fix:     "Run 'bd doctor --fix' or manually: rm -rf .beads/dolt && bd init",
		}
	}
	defer func() { _ = store.Close() }()

	dbVersion, err := store.GetMetadata(ctx, "bd_version")
	if err != nil {
		return DoctorCheck{
			Name:    "Database",
			Status:  StatusError,
			Message: "Unable to read database version",
			Detail:  fmt.Sprintf("Storage: Dolt\n\nError: %v", err),
			Fix:     "Database may be corrupted. Run 'bd doctor --fix' to recover",
		}
	}
	if dbVersion == "" {
		return DoctorCheck{
			Name:    "Database",
			Status:  StatusWarning,
			Message: "Database missing version metadata",
			Detail:  "Storage: Dolt",
			Fix:     "Run 'bd doctor --fix' to repair metadata",
		}
	}

	if dbVersion != cliVersion {
		return DoctorCheck{
			Name:    "Database",
			Status:  StatusWarning,
			Message: fmt.Sprintf("version %s (CLI: %s)", dbVersion, cliVersion),
			Detail:  "Storage: Dolt",
			Fix:     "Update bd CLI and re-run (dolt metadata will be updated automatically)",
		}
	}

	return DoctorCheck{
		Name:    "Database",
		Status:  StatusOK,
		Message: fmt.Sprintf("version %s", dbVersion),
		Detail:  "Storage: Dolt",
	}
}

// CheckSchemaCompatibility checks if all required tables and columns are present
func CheckSchemaCompatibility(path string) DoctorCheck {
	backend, beadsDir := getBackendAndBeadsDir(path)

	if backend != configfile.BackendDolt {
		return sqliteBackendWarning("Schema Compatibility")
	}

	if info, err := os.Stat(getDatabasePath(beadsDir)); err != nil || !info.IsDir() {
		return DoctorCheck{
			Name:    "Schema Compatibility",
			Status:  StatusOK,
			Message: "N/A (no database)",
		}
	}

	ctx := context.Background()
	store, err := dolt.NewFromConfigWithOptions(ctx, beadsDir, &dolt.Config{ReadOnly: true})
	if err != nil {
		return DoctorCheck{
			Name:    "Schema Compatibility",
			Status:  StatusError,
			Message: "Failed to open database",
			Detail:  fmt.Sprintf("Storage: Dolt\n\nError: %v", err),
		}
	}
	defer func() { _ = store.Close() }()

	// Exercise core tables/views.
	if _, err := store.GetStatistics(ctx); err != nil {
		return DoctorCheck{
			Name:    "Schema Compatibility",
			Status:  StatusError,
			Message: "Database schema is incomplete or incompatible",
			Detail:  fmt.Sprintf("Storage: Dolt\n\nError: %v", err),
			Fix:     "Run: rm -rf .beads/dolt && bd init",
		}
	}

	return DoctorCheck{
		Name:    "Schema Compatibility",
		Status:  StatusOK,
		Message: "Basic queries succeeded",
		Detail:  "Storage: Dolt",
	}
}

// CheckDatabaseIntegrity runs a basic integrity check on the database
func CheckDatabaseIntegrity(path string) DoctorCheck {
	backend, beadsDir := getBackendAndBeadsDir(path)

	if backend != configfile.BackendDolt {
		return sqliteBackendWarning("Database Integrity")
	}

	if info, err := os.Stat(getDatabasePath(beadsDir)); err != nil || !info.IsDir() {
		return DoctorCheck{
			Name:    "Database Integrity",
			Status:  StatusOK,
			Message: "N/A (no database)",
		}
	}

	ctx := context.Background()
	store, err := dolt.NewFromConfigWithOptions(ctx, beadsDir, &dolt.Config{ReadOnly: true})
	if err != nil {
		return DoctorCheck{
			Name:    "Database Integrity",
			Status:  StatusError,
			Message: "Failed to open database",
			Detail:  fmt.Sprintf("Storage: Dolt\n\nError: %v", err),
			Fix:     "Run: rm -rf .beads/dolt && bd init (will clone from remote if configured)",
		}
	}
	defer func() { _ = store.Close() }()

	// Minimal checks: metadata + statistics. If these work, the store is at least readable.
	if _, err := store.GetMetadata(ctx, "bd_version"); err != nil {
		return DoctorCheck{
			Name:    "Database Integrity",
			Status:  StatusError,
			Message: "Basic query failed",
			Detail:  fmt.Sprintf("Storage: Dolt\n\nError: %v", err),
		}
	}
	if _, err := store.GetStatistics(ctx); err != nil {
		return DoctorCheck{
			Name:    "Database Integrity",
			Status:  StatusError,
			Message: "Basic query failed",
			Detail:  fmt.Sprintf("Storage: Dolt\n\nError: %v", err),
		}
	}

	return DoctorCheck{
		Name:    "Database Integrity",
		Status:  StatusOK,
		Message: "Basic query check passed",
		Detail:  "Storage: Dolt",
	}
}

// Fix functions

// FixDatabaseConfig auto-detects and fixes metadata.json database config mismatches
func FixDatabaseConfig(path string) error {
	return fix.DatabaseConfig(path)
}

// sqliteBackendWarning returns a standard warning for legacy SQLite backends
func sqliteBackendWarning(checkName string) DoctorCheck {
	return DoctorCheck{
		Name:    checkName,
		Status:  StatusWarning,
		Message: "SQLite backend detected",
		Fix:     "Run 'bd migrate --to-dolt' to upgrade to Dolt backend",
	}
}

// getDatabasePath returns the actual database directory path, respecting dolt_data_dir.
// When dolt_data_dir is configured (e.g. ext4 redirect for WSL), the database lives
// outside .beads/dolt/ — this function resolves the correct location.
func getDatabasePath(beadsDir string) string {
	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		return filepath.Join(beadsDir, "dolt") // fallback to default
	}
	return cfg.DatabasePath(beadsDir)
}

// isNoDbModeConfigured checks if no-db: true is set in config.yaml
// Uses proper YAML parsing to avoid false matches in comments or nested keys
func isNoDbModeConfigured(beadsDir string) bool {
	configPath := filepath.Join(beadsDir, "config.yaml")
	data, err := os.ReadFile(configPath) // #nosec G304 - config file path from beadsDir
	if err != nil {
		return false
	}

	var cfg localConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return false
	}

	return cfg.NoDb
}

// CheckDatabaseSize warns when the database has accumulated many closed issues.
// This is purely informational - pruning is NEVER auto-fixed because it
// permanently deletes data. Users must explicitly run 'bd cleanup' to prune.
//
// Config: doctor.suggest_pruning_issue_count (default: 5000, 0 = disabled)
//
// DESIGN NOTE: This check intentionally has NO auto-fix. Unlike other doctor
// checks that fix configuration or sync issues, pruning is destructive and
// irreversible. The user must make an explicit decision to delete their
// closed issue history. We only provide guidance, never action.
func CheckDatabaseSize(path string) DoctorCheck {
	backend, beadsDir := getBackendAndBeadsDir(path)

	if backend != configfile.BackendDolt {
		return sqliteBackendWarning("Large Database")
	}

	doltPath := getDatabasePath(beadsDir)
	if _, err := os.Stat(doltPath); os.IsNotExist(err) {
		return DoctorCheck{
			Name:    "Large Database",
			Status:  StatusOK,
			Message: "N/A (no database)",
		}
	}

	ctx := context.Background()
	store, err := dolt.NewFromConfigWithOptions(ctx, beadsDir, &dolt.Config{ReadOnly: true})
	if err != nil {
		return DoctorCheck{
			Name:    "Large Database",
			Status:  StatusOK,
			Message: "N/A (unable to open database)",
		}
	}
	defer func() { _ = store.Close() }()

	// Read threshold from config (default 5000, 0 = disabled)
	threshold := 5000
	thresholdStr, err := store.GetConfig(ctx, "doctor.suggest_pruning_issue_count")
	if err == nil && thresholdStr != "" {
		if _, err := fmt.Sscanf(thresholdStr, "%d", &threshold); err != nil {
			threshold = 5000 // Reset to default on parse error
		}
	}

	if threshold == 0 {
		return DoctorCheck{
			Name:    "Large Database",
			Status:  StatusOK,
			Message: "Check disabled (threshold = 0)",
		}
	}

	stats, err := store.GetStatistics(ctx)
	if err != nil {
		return DoctorCheck{
			Name:    "Large Database",
			Status:  StatusOK,
			Message: "N/A (unable to count issues)",
		}
	}

	if stats.ClosedIssues > threshold {
		return DoctorCheck{
			Name:    "Large Database",
			Status:  StatusWarning,
			Message: fmt.Sprintf("%d closed issues (threshold: %d)", stats.ClosedIssues, threshold),
			Detail:  "Large number of closed issues may impact performance",
			Fix:     "Consider running 'bd cleanup --older-than 90' to prune old closed issues",
		}
	}

	return DoctorCheck{
		Name:    "Large Database",
		Status:  StatusOK,
		Message: fmt.Sprintf("%d closed issues (threshold: %d)", stats.ClosedIssues, threshold),
	}
}
