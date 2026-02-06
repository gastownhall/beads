package doctor

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/factory"
)

// GetBackend returns the configured backend type from configuration.
// It checks config.yaml first (storage-backend key), then falls back to metadata.json.
// Returns "sqlite" (default) or "dolt".
// hq-3446fc.17: Use factory.GetBackendFromConfig for consistent backend detection.
func GetBackend(beadsDir string) string {
	return factory.GetBackendFromConfig(beadsDir)
}

// IsDoltBackend returns true if the configured backend is Dolt.
func IsDoltBackend(beadsDir string) bool {
	return GetBackend(beadsDir) == configfile.BackendDolt
}

// openDoltServerDB opens a connection to the Dolt server via MySQL protocol.
// Returns the connection and any error.
func openDoltServerDB(beadsDir string) (*sql.DB, error) {
	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		// Fall back to defaults
		cfg = configfile.DefaultConfig()
	}

	host := cfg.GetDoltServerHost()
	if host == "" {
		host = "127.0.0.1"
	}
	port := cfg.GetDoltServerPort()
	if port == 0 {
		port = 3306
	}
	user := cfg.GetDoltServerUser()
	if user == "" {
		user = "root"
	}
	database := cfg.GetDoltDatabase()
	if database == "" {
		database = "beads"
	}

	connStr := fmt.Sprintf("%s@tcp(%s:%d)/%s?parseTime=true", user, host, port, database)
	db, err := sql.Open("mysql", connStr)
	if err != nil {
		return nil, fmt.Errorf("failed to open Dolt server connection: %w", err)
	}

	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(2)

	return db, nil
}

// CheckDoltConnection verifies connectivity to the Dolt database.
func CheckDoltConnection(path string) DoctorCheck {
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))

	// Only run this check for Dolt backend
	if !IsDoltBackend(beadsDir) {
		return DoctorCheck{
			Name:     "Dolt Connection",
			Status:   StatusOK,
			Message:  "N/A (SQLite backend)",
			Category: CategoryCore,
		}
	}

	// Check if Dolt database directory exists
	doltPath := filepath.Join(beadsDir, "dolt", "beads", ".dolt")
	if _, err := os.Stat(doltPath); os.IsNotExist(err) {
		return DoctorCheck{
			Name:     "Dolt Connection",
			Status:   StatusError,
			Message:  "Dolt database not found",
			Detail:   fmt.Sprintf("Expected: %s", doltPath),
			Fix:      "Run 'bd init --backend dolt' to create Dolt database",
			Category: CategoryCore,
		}
	}

	// Try to connect to Dolt server
	db, err := openDoltServerDB(beadsDir)
	if err != nil {
		return DoctorCheck{
			Name:     "Dolt Connection",
			Status:   StatusError,
			Message:  "Failed to open Dolt database",
			Detail:   err.Error(),
			Category: CategoryCore,
		}
	}
	defer db.Close()

	// Ping
	ctx := context.Background()
	if err := db.PingContext(ctx); err != nil {
		return DoctorCheck{
			Name:     "Dolt Connection",
			Status:   StatusError,
			Message:  "Failed to ping Dolt database",
			Detail:   err.Error(),
			Fix:      "Ensure dolt sql-server is running",
			Category: CategoryCore,
		}
	}

	return DoctorCheck{
		Name:     "Dolt Connection",
		Status:   StatusOK,
		Message:  "Connected successfully",
		Detail:   "Storage: Dolt (server mode)",
		Category: CategoryCore,
	}
}

// CheckDoltSchema verifies the Dolt database has required tables.
func CheckDoltSchema(path string) DoctorCheck {
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))

	// Only run for Dolt backend
	if !IsDoltBackend(beadsDir) {
		return DoctorCheck{
			Name:     "Dolt Schema",
			Status:   StatusOK,
			Message:  "N/A (SQLite backend)",
			Category: CategoryCore,
		}
	}

	db, err := openDoltServerDB(beadsDir)
	if err != nil {
		return DoctorCheck{
			Name:     "Dolt Schema",
			Status:   StatusError,
			Message:  "Failed to open database",
			Detail:   err.Error(),
			Category: CategoryCore,
		}
	}
	defer db.Close()

	ctx := context.Background()

	// Check required tables
	requiredTables := []string{"issues", "dependencies", "config", "labels", "events"}
	var missingTables []string

	for _, table := range requiredTables {
		var count int
		err := db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s LIMIT 1", table)).Scan(&count)
		if err != nil {
			missingTables = append(missingTables, table)
		}
	}

	if len(missingTables) > 0 {
		return DoctorCheck{
			Name:     "Dolt Schema",
			Status:   StatusError,
			Message:  fmt.Sprintf("Missing tables: %v", missingTables),
			Fix:      "Run 'bd init --backend dolt' to create schema",
			Category: CategoryCore,
		}
	}

	return DoctorCheck{
		Name:     "Dolt Schema",
		Status:   StatusOK,
		Message:  "All required tables present",
		Category: CategoryCore,
	}
}

// CheckDoltIssueCount compares issue count in Dolt vs JSONL.
func CheckDoltIssueCount(path string) DoctorCheck {
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))

	// Only run for Dolt backend
	if !IsDoltBackend(beadsDir) {
		return DoctorCheck{
			Name:     "Dolt-JSONL Sync",
			Status:   StatusOK,
			Message:  "N/A (SQLite backend)",
			Category: CategoryData,
		}
	}

	// Get JSONL count
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	jsonlCount, _, err := CountJSONLIssues(jsonlPath)
	if err != nil {
		// Try alternate path
		jsonlPath = filepath.Join(beadsDir, "beads.jsonl")
		jsonlCount, _, err = CountJSONLIssues(jsonlPath)
		if err != nil {
			return DoctorCheck{
				Name:     "Dolt-JSONL Sync",
				Status:   StatusWarning,
				Message:  "Could not read JSONL file",
				Detail:   err.Error(),
				Category: CategoryData,
			}
		}
	}

	// Get Dolt count via server
	db, err := openDoltServerDB(beadsDir)
	if err != nil {
		return DoctorCheck{
			Name:     "Dolt-JSONL Sync",
			Status:   StatusError,
			Message:  "Failed to open Dolt database",
			Detail:   err.Error(),
			Category: CategoryData,
		}
	}
	defer db.Close()

	ctx := context.Background()
	var doltCount int
	err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues").Scan(&doltCount)
	if err != nil {
		return DoctorCheck{
			Name:     "Dolt-JSONL Sync",
			Status:   StatusError,
			Message:  "Failed to count Dolt issues",
			Detail:   err.Error(),
			Category: CategoryData,
		}
	}

	if doltCount != jsonlCount {
		return DoctorCheck{
			Name:     "Dolt-JSONL Sync",
			Status:   StatusWarning,
			Message:  fmt.Sprintf("Count mismatch: Dolt has %d, JSONL has %d", doltCount, jsonlCount),
			Fix:      "Run 'bd sync' to synchronize",
			Category: CategoryData,
		}
	}

	return DoctorCheck{
		Name:     "Dolt-JSONL Sync",
		Status:   StatusOK,
		Message:  fmt.Sprintf("Synced (%d issues)", doltCount),
		Category: CategoryData,
	}
}

// CheckDoltStatus reports uncommitted changes in Dolt.
func CheckDoltStatus(path string) DoctorCheck {
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))

	// Only run for Dolt backend
	if !IsDoltBackend(beadsDir) {
		return DoctorCheck{
			Name:     "Dolt Status",
			Status:   StatusOK,
			Message:  "N/A (SQLite backend)",
			Category: CategoryData,
		}
	}

	db, err := openDoltServerDB(beadsDir)
	if err != nil {
		return DoctorCheck{
			Name:     "Dolt Status",
			Status:   StatusWarning,
			Message:  "Could not check Dolt status",
			Detail:   err.Error(),
			Category: CategoryData,
		}
	}
	defer db.Close()

	ctx := context.Background()

	// Check dolt_status for uncommitted changes
	rows, err := db.QueryContext(ctx, "SELECT table_name, staged, status FROM dolt_status")
	if err != nil {
		return DoctorCheck{
			Name:     "Dolt Status",
			Status:   StatusWarning,
			Message:  "Could not query dolt_status",
			Detail:   err.Error(),
			Category: CategoryData,
		}
	}
	defer rows.Close()

	var changes []string
	for rows.Next() {
		var tableName string
		var staged bool
		var status string
		if err := rows.Scan(&tableName, &staged, &status); err != nil {
			continue
		}
		stageMark := ""
		if staged {
			stageMark = "(staged)"
		}
		changes = append(changes, fmt.Sprintf("%s: %s %s", tableName, status, stageMark))
	}

	if len(changes) > 0 {
		return DoctorCheck{
			Name:     "Dolt Status",
			Status:   StatusWarning,
			Message:  fmt.Sprintf("%d uncommitted change(s)", len(changes)),
			Detail:   fmt.Sprintf("Changes: %v", changes),
			Fix:      "Dolt changes are auto-committed by bd commands",
			Category: CategoryData,
		}
	}

	return DoctorCheck{
		Name:     "Dolt Status",
		Status:   StatusOK,
		Message:  "Clean working set",
		Category: CategoryData,
	}
}
