package doctor

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	// MySQL driver for connecting to dolt sql-server
	_ "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/lockfile"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

// openDoltDB opens a connection to the Dolt SQL server via MySQL protocol.
func openDoltDB(beadsDir string) (*sql.DB, *configfile.Config, error) {
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load config: %w", err)
	}

	host := configfile.DefaultDoltServerHost
	port := configfile.DefaultDoltServerPort
	user := configfile.DefaultDoltServerUser
	database := configfile.DefaultDoltDatabase
	password := os.Getenv("BEADS_DOLT_PASSWORD")

	if cfg != nil {
		host = cfg.GetDoltServerHost()
		port = cfg.GetDoltServerPort()
		user = cfg.GetDoltServerUser()
		database = cfg.GetDoltDatabase()
	}

	var connStr string
	if password != "" {
		connStr = fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?parseTime=true&timeout=5s",
			user, password, host, port, database)
	} else {
		connStr = fmt.Sprintf("%s@tcp(%s:%d)/%s?parseTime=true&timeout=5s",
			user, host, port, database)
	}

	db, err := sql.Open("mysql", connStr)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open server connection: %w", err)
	}

	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(30 * time.Second)

	// Verify connectivity
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close() // Best effort cleanup
		return nil, nil, fmt.Errorf("server not reachable: %w", err)
	}

	return db, cfg, nil
}

// doltConn holds an open Dolt connection.
// Used by doctor checks to coordinate database access.
type doltConn struct {
	db  *sql.DB
	cfg *configfile.Config // config for server detail (host:port)
}

// Close releases the database connection.
func (c *doltConn) Close() {
	_ = c.db.Close()
}

// openDoltConn opens a Dolt connection for doctor checks.
func openDoltConn(beadsDir string) (*doltConn, error) {
	db, cfg, err := openDoltDB(beadsDir)
	if err != nil {
		return nil, err
	}

	return &doltConn{db: db, cfg: cfg}, nil
}

// GetBackend returns the configured backend type from configuration.
// It checks config.yaml first (storage-backend key), then falls back to metadata.json.
// Returns "dolt" (default) or "sqlite" (legacy).
// hq-3446fc.17: Use dolt.GetBackendFromConfig for consistent backend detection.
func GetBackend(beadsDir string) string {
	return dolt.GetBackendFromConfig(beadsDir)
}

// IsDoltBackend returns true if the configured backend is Dolt.
func IsDoltBackend(beadsDir string) bool {
	return GetBackend(beadsDir) == configfile.BackendDolt
}

// RunDoltHealthChecks runs all Dolt-specific health checks using a single
// shared server connection. Returns one check per health dimension.
// Non-Dolt backends get N/A results for all dimensions.
func RunDoltHealthChecks(path string) []DoctorCheck {
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))

	if !IsDoltBackend(beadsDir) {
		return []DoctorCheck{
			{Name: "Dolt Connection", Status: StatusOK, Message: "N/A (SQLite backend)", Category: CategoryCore},
			{Name: "Dolt Schema", Status: StatusOK, Message: "N/A (SQLite backend)", Category: CategoryCore},
			{Name: "Dolt-JSONL Sync", Status: StatusOK, Message: "N/A (SQLite backend)", Category: CategoryData},
			{Name: "Dolt Status", Status: StatusOK, Message: "N/A (SQLite backend)", Category: CategoryData},
			{Name: "Dolt Lock Health", Status: StatusOK, Message: "N/A (SQLite backend)", Category: CategoryRuntime},
		}
	}

	// Run lock health check before opening database (it doesn't need a connection)
	lockCheck := CheckLockHealth(path)

	conn, err := openDoltConn(beadsDir)
	if err != nil {
		errCheck := DoctorCheck{
			Name:     "Dolt Connection",
			Status:   StatusError,
			Message:  "Failed to connect to Dolt server",
			Detail:   err.Error(),
			Fix:      "Ensure dolt sql-server is running, or check server host/port configuration",
			Category: CategoryCore,
		}
		return []DoctorCheck{errCheck, lockCheck}
	}
	defer conn.Close()

	return []DoctorCheck{
		checkConnectionWithDB(conn),
		checkSchemaWithDB(conn),
		checkIssueCountWithDB(conn, beadsDir),
		checkStatusWithDB(conn),
		lockCheck,
	}
}

// checkConnectionWithDB tests connectivity using an existing connection.
// Separated from CheckDoltConnection to allow connection reuse across checks.
func checkConnectionWithDB(conn *doltConn) DoctorCheck {
	ctx := context.Background()
	if err := conn.db.PingContext(ctx); err != nil {
		return DoctorCheck{
			Name:     "Dolt Connection",
			Status:   StatusError,
			Message:  "Failed to ping Dolt server",
			Detail:   err.Error(),
			Category: CategoryCore,
		}
	}

	storageDetail := "Storage: Dolt (server mode)"
	if conn.cfg != nil {
		storageDetail = fmt.Sprintf("Storage: Dolt (server %s:%d)",
			conn.cfg.GetDoltServerHost(), conn.cfg.GetDoltServerPort())
	}

	return DoctorCheck{
		Name:     "Dolt Connection",
		Status:   StatusOK,
		Message:  "Connected successfully",
		Detail:   storageDetail,
		Category: CategoryCore,
	}
}

// CheckDoltConnection verifies connectivity to the Dolt SQL server.
// This is the standalone entry point; RunDoltHealthChecks is preferred
// for coordinated access.
func CheckDoltConnection(path string) DoctorCheck {
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))

	// Only run this check for Dolt backend
	if !IsDoltBackend(beadsDir) {
		return DoctorCheck{
			Name:     "Dolt Connection",
			Status:   StatusOK,
			Message:  "N/A (not using Dolt backend)",
			Category: CategoryCore,
		}
	}

	conn, err := openDoltConn(beadsDir)
	if err != nil {
		return DoctorCheck{
			Name:     "Dolt Connection",
			Status:   StatusError,
			Message:  "Failed to connect to Dolt server",
			Detail:   err.Error(),
			Fix:      "Ensure dolt sql-server is running",
			Category: CategoryCore,
		}
	}
	defer conn.Close()

	return checkConnectionWithDB(conn)
}

// checkSchemaWithDB verifies the Dolt database has required tables using an existing connection.
// Separated from CheckDoltSchema to allow connection reuse across checks.
func checkSchemaWithDB(conn *doltConn) DoctorCheck {
	ctx := context.Background()

	// Check required tables
	requiredTables := []string{"issues", "dependencies", "config", "labels", "events"}
	var missingTables []string

	for _, table := range requiredTables {
		var count int
		err := conn.db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s LIMIT 1", table)).Scan(&count)
		if err != nil {
			missingTables = append(missingTables, table)
		}
	}

	if len(missingTables) > 0 {
		return DoctorCheck{
			Name:     "Dolt Schema",
			Status:   StatusError,
			Message:  fmt.Sprintf("Missing tables: %v", missingTables),
			Fix:      "Run 'bd init' to create schema",
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

// CheckDoltSchema verifies the Dolt database has required tables.
// This is the standalone entry point; RunDoltHealthChecks is preferred
// for coordinated access.
func CheckDoltSchema(path string) DoctorCheck {
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))

	// Only run for Dolt backend
	if !IsDoltBackend(beadsDir) {
		return DoctorCheck{
			Name:     "Dolt Schema",
			Status:   StatusOK,
			Message:  "N/A (not using Dolt backend)",
			Category: CategoryCore,
		}
	}

	conn, err := openDoltConn(beadsDir)
	if err != nil {
		return DoctorCheck{
			Name:     "Dolt Schema",
			Status:   StatusError,
			Message:  "Failed to open database",
			Detail:   err.Error(),
			Category: CategoryCore,
		}
	}
	defer conn.Close()

	return checkSchemaWithDB(conn)
}

// checkIssueCountWithDB compares issue count in Dolt vs JSONL using an existing connection.
// Separated from CheckDoltIssueCount to allow connection reuse across checks.
// Requires beadsDir to locate JSONL files.
func checkIssueCountWithDB(conn *doltConn, beadsDir string) DoctorCheck {
	// Get JSONL count (before DB query — keep original order)
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

	// Get Dolt count
	ctx := context.Background()
	var doltCount int
	err = conn.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues").Scan(&doltCount)
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

// CheckDoltIssueCount compares issue count in Dolt vs JSONL.
// This is the standalone entry point; RunDoltHealthChecks is preferred
// for coordinated access.
func CheckDoltIssueCount(path string) DoctorCheck {
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))

	// Only run for Dolt backend
	if !IsDoltBackend(beadsDir) {
		return DoctorCheck{
			Name:     "Dolt-JSONL Sync",
			Status:   StatusOK,
			Message:  "N/A (not using Dolt backend)",
			Category: CategoryData,
		}
	}

	conn, err := openDoltConn(beadsDir)
	if err != nil {
		return DoctorCheck{
			Name:     "Dolt-JSONL Sync",
			Status:   StatusError,
			Message:  "Failed to open Dolt database",
			Detail:   err.Error(),
			Category: CategoryData,
		}
	}
	defer conn.Close()

	return checkIssueCountWithDB(conn, beadsDir)
}

// checkStatusWithDB reports uncommitted changes in Dolt using an existing connection.
// Separated from CheckDoltStatus to allow connection reuse across checks.
func checkStatusWithDB(conn *doltConn) DoctorCheck {
	ctx := context.Background()

	// Check dolt_status for uncommitted changes
	rows, err := conn.db.QueryContext(ctx, "SELECT table_name, staged, status FROM dolt_status")
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
			Fix:      "Run 'bd vc commit -m \"commit changes\"' to commit, or changes will auto-commit on next bd command",
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

// CheckDoltStatus reports uncommitted changes in Dolt.
// This is the standalone entry point; RunDoltHealthChecks is preferred
// for coordinated access.
func CheckDoltStatus(path string) DoctorCheck {
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))

	// Only run for Dolt backend
	if !IsDoltBackend(beadsDir) {
		return DoctorCheck{
			Name:     "Dolt Status",
			Status:   StatusOK,
			Message:  "N/A (not using Dolt backend)",
			Category: CategoryData,
		}
	}

	conn, err := openDoltConn(beadsDir)
	if err != nil {
		return DoctorCheck{
			Name:     "Dolt Status",
			Status:   StatusWarning,
			Message:  "Could not check Dolt status",
			Detail:   err.Error(),
			Category: CategoryData,
		}
	}
	defer conn.Close()

	return checkStatusWithDB(conn)
}

// CheckLockHealth checks the health of Dolt lock files.
// It probes for stale noms LOCK files and checks whether the advisory lock
// is currently held, providing actionable guidance when issues are found.
func CheckLockHealth(path string) DoctorCheck {
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))

	if !IsDoltBackend(beadsDir) {
		return DoctorCheck{
			Name:     "Dolt Lock Health",
			Status:   StatusOK,
			Message:  "N/A (not using Dolt backend)",
			Category: CategoryRuntime,
		}
	}

	var warnings []string

	// Check for noms LOCK files that are actively held by another process.
	// Dolt's noms chunk store creates a LOCK file on open and releases the
	// flock on close, but never deletes the file. We probe the flock to
	// distinguish an actively held lock (real contention) from a stale
	// file left by a previous process (harmless).
	doltDir := filepath.Join(beadsDir, "dolt")
	if dbEntries, err := os.ReadDir(doltDir); err == nil {
		for _, dbEntry := range dbEntries {
			if !dbEntry.IsDir() {
				continue
			}
			nomsLock := filepath.Join(doltDir, dbEntry.Name(), ".dolt", "noms", "LOCK")
			if f, err := os.OpenFile(nomsLock, os.O_RDWR, 0); err == nil { //nolint:gosec // controlled path
				if lockErr := lockfile.FlockExclusiveNonBlocking(f); lockErr != nil {
					// Lock is actively held by another process
					warnings = append(warnings,
						fmt.Sprintf("noms LOCK at dolt/%s/.dolt/noms/LOCK is held by another process — may block database access", dbEntry.Name()))
				} else {
					// File exists but lock is not held — stale file, not a problem
					_ = lockfile.FlockUnlock(f)
				}
				_ = f.Close()
			}
		}
	}

	// Probe advisory lock to check if it's currently held
	accessLockPath := filepath.Join(beadsDir, "dolt-access.lock")
	if _, err := os.Stat(accessLockPath); err == nil {
		f, err := os.OpenFile(accessLockPath, os.O_RDWR, 0) //nolint:gosec // controlled path
		if err == nil {
			if lockErr := lockfile.FlockExclusiveNonBlocking(f); lockErr != nil {
				// Lock is held by another process
				warnings = append(warnings,
					"advisory lock is currently held by another bd process")
			} else {
				// We acquired it, meaning no one holds it — release immediately
				_ = lockfile.FlockUnlock(f)
			}
			_ = f.Close()
		}
	}

	if len(warnings) == 0 {
		return DoctorCheck{
			Name:     "Dolt Lock Health",
			Status:   StatusOK,
			Message:  "No lock contention detected",
			Category: CategoryRuntime,
		}
	}

	return DoctorCheck{
		Name:     "Dolt Lock Health",
		Status:   StatusWarning,
		Message:  fmt.Sprintf("%d lock issue(s) detected", len(warnings)),
		Detail:   strings.Join(warnings, "; "),
		Fix:      "Run 'bd doctor --fix' to clean stale lock files, or wait for the other process to finish",
		Category: CategoryRuntime,
	}
}
