//go:build cgo

package doctor

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	// Import Dolt driver for direct connection
	_ "github.com/dolthub/driver"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/lockfile"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/doltutil"
)

// closeDoltDBWithTimeout closes a sql.DB with a timeout to prevent indefinite hangs.
// This is needed because embedded Dolt can hang on close.
func closeDoltDBWithTimeout(db *sql.DB) {
	// Use the shared helper; ignore errors since we're just cleaning up
	_ = doltutil.CloseWithTimeout("db", db.Close)
}

// openDoltDB opens a connection to the Dolt database, respecting the configured mode.
// In server mode, connects via MySQL driver to the Dolt SQL server.
// In embedded mode, uses the in-process Dolt driver.
// Returns the db, whether server mode was used, and any error.
// The caller should use closeDoltDB to properly close the connection.
func openDoltDB(beadsDir string) (*sql.DB, bool, error) {
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		return nil, false, fmt.Errorf("failed to load config: %w", err)
	}

	if cfg != nil && cfg.IsDoltServerMode() {
		db, err := openDoltDBViaServer(cfg)
		return db, true, err
	}

	db, err := openDoltDBEmbedded(beadsDir)
	return db, false, err
}

// openDoltDBViaServer connects to the Dolt SQL server using the MySQL protocol.
// The database is selected in the DSN, so no USE statement is needed.
func openDoltDBViaServer(cfg *configfile.Config) (*sql.DB, error) {
	host := cfg.GetDoltServerHost()
	port := cfg.GetDoltServerPort()
	user := cfg.GetDoltServerUser()
	database := cfg.GetDoltDatabase()
	password := os.Getenv("BEADS_DOLT_PASSWORD")

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
		return nil, fmt.Errorf("failed to open server connection: %w", err)
	}

	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(30 * time.Second)

	// Verify connectivity
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close() // Best effort cleanup
		return nil, fmt.Errorf("server not reachable: %w", err)
	}

	return db, nil
}

// openDoltDBEmbedded opens a Dolt database using the in-process embedded driver.
// Reads the configured database name from metadata.json (dolt_database field)
// and switches to it after opening.
func openDoltDBEmbedded(beadsDir string) (*sql.DB, error) {
	doltDir := filepath.Join(beadsDir, "dolt")
	connStr := fmt.Sprintf("file://%s?commitname=beads&commitemail=beads@local", doltDir)

	// Determine the database name from configuration
	dbName := configfile.DefaultDoltDatabase
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil {
		dbName = cfg.GetDoltDatabase()
	}

	db, err := sql.Open("dolt", connStr)
	if err != nil {
		return nil, err
	}

	ctx := context.Background()
	if _, err := db.ExecContext(ctx, fmt.Sprintf("USE `%s`", dbName)); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to switch to %s database: %w", dbName, err)
	}

	return db, nil
}

// closeDoltDB closes a database connection, using a timeout for embedded mode
// (which can hang on close) and a regular close for server mode.
func closeDoltDB(db *sql.DB, serverMode bool) {
	if serverMode {
		_ = db.Close() // Best effort cleanup
	} else {
		closeDoltDBWithTimeout(db)
	}
}

// doltConn holds an open Dolt connection with its advisory lock.
// Used by doctor checks to coordinate database access and prevent
// lock contention with daemons and concurrent bd processes.
type doltConn struct {
	db         *sql.DB
	serverMode bool
	cfg        *configfile.Config // config for server mode detail (host:port)
	lock       *dolt.AccessLock   // nil in server mode
}

// Close releases the database connection and advisory lock.
// Releases DB first (may take time for embedded mode), then lock.
//
// Note on CloseWithTimeout behavior: In embedded mode, closeDoltDB uses
// doltutil.CloseWithTimeout which runs db.Close() in a goroutine with a 5s
// timeout. If the timeout fires, the goroutine keeps running in the background
// and may leave a noms LOCK file behind (.beads/dolt/beads/.dolt/noms/LOCK).
// We intentionally do NOT clean up noms LOCK files here because:
//   - Doctor holds a shared AccessLock, not exclusive — other processes may be active
//   - The noms LOCK is managed by the Dolt storage engine — external removal risks corruption
//   - LOCK file cleanup belongs in doctor --fix, not in connection teardown
func (c *doltConn) Close() {
	closeDoltDB(c.db, c.serverMode)
	if c.lock != nil {
		c.lock.Release()
	}
}

// openDoltDBWithLock opens a Dolt connection with AccessLock coordination.
// In embedded mode, acquires a shared AccessLock before opening the database
// to prevent contention with daemons and other bd processes.
// In server mode, skips lock acquisition (server handles its own locking).
//
// Note: This does NOT honor the BD_SKIP_ACCESS_LOCK env var that DoltStore
// checks (store.go:265). Doctor is read-only and short-lived, so the shared
// lock is always appropriate. The env var is a debugging escape hatch for
// write-path operations where lock contention is more disruptive.
func openDoltDBWithLock(beadsDir string) (*doltConn, error) {
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	isServer := cfg != nil && cfg.IsDoltServerMode()

	var lock *dolt.AccessLock
	if !isServer {
		doltDir := filepath.Join(beadsDir, "dolt")
		absPath, err := filepath.Abs(doltDir)
		if err != nil {
			return nil, fmt.Errorf("abs path: %w", err)
		}
		lock, err = dolt.AcquireAccessLock(absPath, false, 15*time.Second)
		if err != nil {
			return nil, fmt.Errorf("acquire access lock: %w", err)
		}
	}

	db, serverMode, err := openDoltDB(beadsDir)
	if err != nil {
		if lock != nil {
			lock.Release()
		}
		return nil, err
	}

	return &doltConn{db: db, serverMode: serverMode, cfg: cfg, lock: lock}, nil
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
// shared connection with AccessLock coordination. Returns one check per
// health dimension. Non-Dolt backends get N/A results for all dimensions.
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

	conn, err := openDoltDBWithLock(beadsDir)
	if err != nil {
		errCheck := DoctorCheck{
			Name:     "Dolt Connection",
			Status:   StatusError,
			Message:  "Failed to open Dolt database",
			Detail:   err.Error(),
			Fix:      "Run 'bd doctor --fix' to clean stale lock files, or check .beads/dolt/",
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
			Message:  "Failed to ping Dolt database",
			Detail:   err.Error(),
			Category: CategoryCore,
		}
	}

	storageDetail := "Storage: Dolt"
	if conn.serverMode && conn.cfg != nil {
		storageDetail = fmt.Sprintf("Storage: Dolt (server %s:%d)",
			conn.cfg.GetDoltServerHost(), conn.cfg.GetDoltServerPort())
	} else if conn.serverMode {
		storageDetail = "Storage: Dolt (server mode)"
	}

	return DoctorCheck{
		Name:     "Dolt Connection",
		Status:   StatusOK,
		Message:  "Connected successfully",
		Detail:   storageDetail,
		Category: CategoryCore,
	}
}

// CheckDoltConnection verifies connectivity to the Dolt database.
// This is the standalone entry point; RunDoltHealthChecks is preferred
// for coordinated access with AccessLock.
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

	// Load config to check mode
	cfg, _ := configfile.Load(beadsDir) // Best effort: nil config uses default Dolt settings
	isServerMode := cfg != nil && cfg.IsDoltServerMode()

	// In embedded mode, check if Dolt database directory exists on disk
	if !isServerMode {
		dbName := configfile.DefaultDoltDatabase
		if cfg != nil {
			dbName = cfg.GetDoltDatabase()
		}
		doltPath := filepath.Join(beadsDir, "dolt", dbName, ".dolt")
		if _, err := os.Stat(doltPath); os.IsNotExist(err) {
			return DoctorCheck{
				Name:     "Dolt Connection",
				Status:   StatusError,
				Message:  "Dolt database not found",
				Detail:   fmt.Sprintf("Expected: %s", doltPath),
				Fix:      "Run 'bd init' to create Dolt database",
				Category: CategoryCore,
			}
		}
	}

	// Open with lock coordination
	conn, err := openDoltDBWithLock(beadsDir)
	if err != nil {
		return DoctorCheck{
			Name:     "Dolt Connection",
			Status:   StatusError,
			Message:  "Failed to open Dolt database",
			Detail:   err.Error(),
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
// for coordinated access with AccessLock.
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

	conn, err := openDoltDBWithLock(beadsDir)
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
// for coordinated access with AccessLock.
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

	conn, err := openDoltDBWithLock(beadsDir)
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

// CheckDoltStatus reports uncommitted changes in Dolt.
// This is the standalone entry point; RunDoltHealthChecks is preferred
// for coordinated access with AccessLock.
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

	conn, err := openDoltDBWithLock(beadsDir)
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

	// Check for stale noms LOCK files
	doltDir := filepath.Join(beadsDir, "dolt")
	if dbEntries, err := os.ReadDir(doltDir); err == nil {
		for _, dbEntry := range dbEntries {
			if !dbEntry.IsDir() {
				continue
			}
			nomsLock := filepath.Join(doltDir, dbEntry.Name(), ".dolt", "noms", "LOCK")
			if _, err := os.Stat(nomsLock); err == nil {
				warnings = append(warnings,
					fmt.Sprintf("noms LOCK file exists at dolt/%s/.dolt/noms/LOCK — may block database access", dbEntry.Name()))
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
