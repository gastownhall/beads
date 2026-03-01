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
	"github.com/steveyegge/beads/internal/doltserver"
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
	user := configfile.DefaultDoltServerUser
	database := configfile.DefaultDoltDatabase
	password := os.Getenv("BEADS_DOLT_PASSWORD")

	// Use doltserver.DefaultConfig for port resolution (env > config > Gas Town > DerivePort).
	// cfg.GetDoltServerPort() is deprecated — it falls back to 3307 which is wrong
	// for standalone mode where the port is hash-derived from the project path.
	dsCfg := doltserver.DefaultConfig(beadsDir)
	port := dsCfg.Port

	if cfg != nil {
		host = cfg.GetDoltServerHost()
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
	db   *sql.DB
	cfg  *configfile.Config // config for server detail (host:port)
	port int                // resolved port (from doltserver.DefaultConfig, not cfg fallback)
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

	port := doltserver.DefaultConfig(beadsDir).Port
	return &doltConn{db: db, cfg: cfg, port: port}, nil
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
//
// Note: Prefer RunDoltHealthChecksWithLock when the lock check has already
// been run early (before any embedded Dolt opens) to avoid false positives.
func RunDoltHealthChecks(path string) []DoctorCheck {
	return RunDoltHealthChecksWithLock(path, CheckLockHealth(path))
}

// RunDoltHealthChecksWithLock is like RunDoltHealthChecks but accepts a
// pre-computed lock health check result. This allows the caller to run
// CheckLockHealth before any checks that open embedded Dolt databases,
// avoiding false positives from doctor's own noms LOCK files (GH#1981).
func RunDoltHealthChecksWithLock(path string, lockCheck DoctorCheck) []DoctorCheck {
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))

	if !IsDoltBackend(beadsDir) {
		return []DoctorCheck{
			{Name: "Dolt Connection", Status: StatusOK, Message: "N/A (SQLite backend)", Category: CategoryCore},
			{Name: "Dolt Schema", Status: StatusOK, Message: "N/A (SQLite backend)", Category: CategoryCore},
			{Name: "Dolt Issue Count", Status: StatusOK, Message: "N/A (SQLite backend)", Category: CategoryData},
			{Name: "Dolt Status", Status: StatusOK, Message: "N/A (SQLite backend)", Category: CategoryData},
			{Name: "Dolt Lock Health", Status: StatusOK, Message: "N/A (SQLite backend)", Category: CategoryRuntime},
			{Name: "Phantom Databases", Status: StatusOK, Message: "N/A (SQLite backend)", Category: CategoryData},
		}
	}

	conn, err := openDoltConn(beadsDir)
	if err != nil {
		connErr := err.Error()
		return []DoctorCheck{
			{Name: "Dolt Connection", Status: StatusError, Message: "Failed to connect to Dolt server", Detail: connErr, Fix: "Ensure dolt sql-server is running, or check server host/port configuration", Category: CategoryCore},
			{Name: "Dolt Schema", Status: StatusError, Message: "Skipped (no connection)", Detail: connErr, Category: CategoryCore},
			{Name: "Dolt Issue Count", Status: StatusError, Message: "Skipped (no connection)", Detail: connErr, Category: CategoryData},
			{Name: "Dolt Status", Status: StatusError, Message: "Skipped (no connection)", Detail: connErr, Category: CategoryData},
			lockCheck,
			{Name: "Phantom Databases", Status: StatusError, Message: "Skipped (no connection)", Detail: connErr, Category: CategoryData},
		}
	}
	defer conn.Close()

	return []DoctorCheck{
		checkConnectionWithDB(conn),
		checkSchemaWithDB(conn),
		checkIssueCountWithDB(conn),
		checkStatusWithDB(conn),
		lockCheck,
		checkPhantomDatabases(conn),
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
			conn.cfg.GetDoltServerHost(), conn.port)
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

// checkIssueCountWithDB reports the issue count in Dolt using an existing connection.
// Separated from CheckDoltIssueCount to allow connection reuse across checks.
func checkIssueCountWithDB(conn *doltConn) DoctorCheck {
	ctx := context.Background()
	var doltCount int
	err := conn.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues").Scan(&doltCount)
	if err != nil {
		return DoctorCheck{
			Name:     "Dolt Issue Count",
			Status:   StatusError,
			Message:  "Failed to count Dolt issues",
			Detail:   err.Error(),
			Category: CategoryData,
		}
	}

	return DoctorCheck{
		Name:     "Dolt Issue Count",
		Status:   StatusOK,
		Message:  fmt.Sprintf("%d issues", doltCount),
		Category: CategoryData,
	}
}

// CheckDoltIssueCount reports the issue count in Dolt.
// This is the standalone entry point; RunDoltHealthChecks is preferred
// for coordinated access.
func CheckDoltIssueCount(path string) DoctorCheck {
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))

	// Only run for Dolt backend
	if !IsDoltBackend(beadsDir) {
		return DoctorCheck{
			Name:     "Dolt Issue Count",
			Status:   StatusOK,
			Message:  "N/A (not using Dolt backend)",
			Category: CategoryData,
		}
	}

	conn, err := openDoltConn(beadsDir)
	if err != nil {
		return DoctorCheck{
			Name:     "Dolt Issue Count",
			Status:   StatusError,
			Message:  "Failed to open Dolt database",
			Detail:   err.Error(),
			Category: CategoryData,
		}
	}
	defer conn.Close()

	return checkIssueCountWithDB(conn)
}

// isWispTable returns true if the table name refers to a wisp (ephemeral) table.
// Wisp tables are expected to have uncommitted changes since they are excluded
// from Dolt version tracking via dolt_ignore. Reporting them as uncommitted
// produces self-fulfilling warnings that can never be cleared.
func isWispTable(tableName string) bool {
	return tableName == "wisps" || strings.HasPrefix(tableName, "wisp_")
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
		// Skip wisp tables — they are ephemeral and expected to have
		// uncommitted changes (covered by dolt_ignore).
		if isWispTable(tableName) {
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
	doltDir := getDatabasePath(beadsDir)
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

// checkPhantomDatabases detects phantom catalog entries from naming convention
// changes (beads_* prefix or *_beads suffix) that don't match the configured
// database. These phantom entries can cause INFORMATION_SCHEMA queries to crash
// (GH#2051). Complementary to checkStaleDatabases in server.go, which targets
// test/polecat leftovers with different prefixes.
func checkPhantomDatabases(conn *doltConn) DoctorCheck {
	rows, err := conn.db.Query("SHOW DATABASES")
	if err != nil {
		return DoctorCheck{
			Name:     "Phantom Databases",
			Status:   StatusWarning,
			Message:  "Could not query databases",
			Detail:   err.Error(),
			Category: CategoryData,
		}
	}
	defer rows.Close()

	configuredDB := configfile.DefaultDoltDatabase
	if conn.cfg != nil {
		configuredDB = conn.cfg.GetDoltDatabase()
	}

	var phantoms []string
	for rows.Next() {
		var dbName string
		if err := rows.Scan(&dbName); err != nil {
			continue
		}
		// Skip system databases and the configured database
		if dbName == "information_schema" || dbName == "mysql" || dbName == configuredDB {
			continue
		}
		// Flag entries matching beads naming convention patterns
		if strings.HasPrefix(dbName, "beads_") || strings.HasSuffix(dbName, "_beads") {
			phantoms = append(phantoms, dbName)
		}
	}

	if len(phantoms) > 0 {
		return DoctorCheck{
			Name:     "Phantom Databases",
			Status:   StatusWarning,
			Message:  fmt.Sprintf("%d phantom database(s) detected: %s", len(phantoms), strings.Join(phantoms, ", ")),
			Detail:   fmt.Sprintf("Phantom entries: %v", phantoms),
			Fix:      "Restart Dolt server to flush phantom entries. See GH#2051.",
			Category: CategoryData,
		}
	}

	return DoctorCheck{
		Name:     "Phantom Databases",
		Status:   StatusOK,
		Message:  "No phantom databases detected",
		Category: CategoryData,
	}
}
