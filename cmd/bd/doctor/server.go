package doctor

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	// Import MySQL driver for server mode connections
	_ "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
)

// ServerHealthResult holds the results of all server health checks
type ServerHealthResult struct {
	Checks    []DoctorCheck `json:"checks"`
	OverallOK bool          `json:"overall_ok"`
}

// RunServerHealthChecks runs all server-mode health checks and returns the result.
// This is called when `bd doctor --server` is used.
func RunServerHealthChecks(path string) ServerHealthResult {
	result := ServerHealthResult{
		OverallOK: true,
	}

	// Load config to check if server mode is configured
	_, beadsDir := getBackendAndBeadsDir(path)
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		result.Checks = append(result.Checks, DoctorCheck{
			Name:     "Server Config",
			Status:   StatusError,
			Message:  "Failed to load config",
			Detail:   err.Error(),
			Category: CategoryFederation,
		})
		result.OverallOK = false
		return result
	}

	if cfg == nil {
		result.Checks = append(result.Checks, DoctorCheck{
			Name:     "Server Config",
			Status:   StatusError,
			Message:  "No metadata.json found",
			Fix:      "Run 'bd init' to initialize beads",
			Category: CategoryFederation,
		})
		result.OverallOK = false
		return result
	}

	// Check if Dolt backend is configured
	if cfg.GetBackend() != configfile.BackendDolt {
		result.Checks = append(result.Checks, DoctorCheck{
			Name:     "Server Config",
			Status:   StatusWarning,
			Message:  fmt.Sprintf("Backend is '%s', not Dolt", cfg.GetBackend()),
			Detail:   "Server mode health checks are only relevant for Dolt backend",
			Fix:      "Set backend: dolt in metadata.json to use Dolt server mode",
			Category: CategoryFederation,
		})
		result.OverallOK = false
		return result
	}

	// Check if server mode is configured
	if !cfg.IsDoltServerMode() {
		result.Checks = append(result.Checks, DoctorCheck{
			Name:     "Server Config",
			Status:   StatusOK,
			Message:  fmt.Sprintf("Dolt mode is '%s' (embedded is the default)", cfg.GetDoltMode()),
			Detail:   "Server health checks only apply when dolt_mode is explicitly set to 'server'",
			Category: CategoryFederation,
		})
		return result
	}

	// Server mode is configured - run health checks
	host := cfg.GetDoltServerHost()
	// Use doltserver.DefaultConfig for port resolution (env > config > DerivePort).
	// cfg.GetDoltServerPort() falls back to 3307 which is wrong for standalone mode.
	port := doltserver.DefaultConfig(beadsDir).Port

	// Check 1: Server reachability (TCP connect)
	reachCheck := checkServerReachable(host, port)
	result.Checks = append(result.Checks, reachCheck)
	if reachCheck.Status == StatusError {
		result.OverallOK = false
		// Can't continue without connectivity
		return result
	}

	// Check 2: Connect and verify it's Dolt (get version)
	versionCheck, db := checkDoltVersion(cfg, beadsDir)
	result.Checks = append(result.Checks, versionCheck)
	if versionCheck.Status == StatusError {
		result.OverallOK = false
		if db != nil {
			_ = db.Close() // Best effort cleanup
		}
		return result
	}
	defer func() {
		if db != nil {
			_ = db.Close() // Best effort cleanup
		}
	}()

	// Get database name from config (uses dolt_database field, default: "beads")
	database := cfg.GetDoltDatabase()

	// Check 3: Database exists and is queryable
	dbExistsCheck := checkDatabaseExists(db, database)
	result.Checks = append(result.Checks, dbExistsCheck)
	if dbExistsCheck.Status == StatusError {
		result.OverallOK = false
	}

	// Check 4: Schema compatible (can query beads tables)
	schemaCheck := checkSchemaCompatible(db, database)
	result.Checks = append(result.Checks, schemaCheck)
	if schemaCheck.Status == StatusError {
		result.OverallOK = false
	}

	// Check 5: Connection pool health
	poolCheck := checkConnectionPool(db)
	result.Checks = append(result.Checks, poolCheck)
	if poolCheck.Status == StatusError {
		result.OverallOK = false
	}

	// Check 6: Stale databases (test/polecat leftovers)
	staleCheck := checkStaleDatabases(db)
	result.Checks = append(result.Checks, staleCheck)
	if staleCheck.Status == StatusError {
		result.OverallOK = false
	}

	return result
}

// staleDatabasePrefixes are prefixes that indicate test/polecat databases that
// should not exist on the production Dolt server. These accumulate from interrupted
// test runs and terminated polecats, wasting server memory and potentially
// contributing to performance degradation under concurrent load.
// - testdb_*: BEADS_TEST_MODE=1 FNV hash of temp paths
// - doctest_*: doctor test helpers
// - doctortest_*: doctor test helpers
// - beads_pt*: gastown patrol_helpers_test.go random prefixes
// - beads_vr*: gastown mail/router_test.go random prefixes
// - beads_t[0-9a-f]*: protocol test random prefixes (t + 8 hex chars)
var staleDatabasePrefixes = []string{
	"testdb_",
	"doctest_",
	"doctortest_",
	"beads_pt",
	"beads_vr",
	"beads_t",
}

// knownProductionDatabases are the databases that should exist on a production server.
// Everything else matching a stale prefix is a candidate for cleanup.
var knownProductionDatabases = map[string]bool{
	"information_schema": true,
	"mysql":              true,
}

// checkStaleDatabases identifies leftover test/polecat databases on the shared server.
// These waste memory and can degrade performance under concurrent load.
func checkStaleDatabases(db *sql.DB) DoctorCheck {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rows, err := db.QueryContext(ctx, "SHOW DATABASES")
	if err != nil {
		return DoctorCheck{
			Name:     "Stale Databases",
			Status:   StatusError,
			Message:  "Failed to list databases",
			Detail:   err.Error(),
			Category: CategoryMaintenance,
		}
	}
	defer rows.Close()

	var stale []string
	var total int
	for rows.Next() {
		var dbName string
		if err := rows.Scan(&dbName); err != nil {
			continue
		}
		total++
		if knownProductionDatabases[dbName] {
			continue
		}
		for _, prefix := range staleDatabasePrefixes {
			if strings.HasPrefix(dbName, prefix) {
				stale = append(stale, dbName)
				break
			}
		}
	}

	if len(stale) == 0 {
		return DoctorCheck{
			Name:     "Stale Databases",
			Status:   StatusOK,
			Message:  fmt.Sprintf("%d databases, no stale test/polecat databases found", total),
			Category: CategoryMaintenance,
		}
	}

	// Build detail string showing first few stale databases
	detail := fmt.Sprintf("Found %d stale databases (of %d total):\n", len(stale), total)
	shown := len(stale)
	if shown > 10 {
		shown = 10
	}
	for _, name := range stale[:shown] {
		detail += fmt.Sprintf("  %s\n", name)
	}
	if len(stale) > 10 {
		detail += fmt.Sprintf("  ... and %d more\n", len(stale)-10)
	}

	return DoctorCheck{
		Name:     "Stale Databases",
		Status:   StatusWarning,
		Message:  fmt.Sprintf("%d stale test/polecat databases found", len(stale)),
		Detail:   strings.TrimSpace(detail),
		Fix:      "Run 'bd dolt clean-databases' to drop stale databases",
		Category: CategoryMaintenance,
	}
}

// checkServerReachable checks if the server is reachable via TCP
func checkServerReachable(host string, port int) DoctorCheck {
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return DoctorCheck{
			Name:     "Server Reachable",
			Status:   StatusError,
			Message:  fmt.Sprintf("Cannot connect to %s", addr),
			Detail:   err.Error(),
			Fix:      "Ensure dolt sql-server is running and accessible",
			Category: CategoryFederation,
		}
	}
	_ = conn.Close() // Best effort cleanup

	return DoctorCheck{
		Name:     "Server Reachable",
		Status:   StatusOK,
		Message:  fmt.Sprintf("Connected to %s", addr),
		Category: CategoryFederation,
	}
}

// checkDoltVersion connects to the server and checks if it's a Dolt server
// Returns the DoctorCheck and an open database connection (caller must close)
func checkDoltVersion(cfg *configfile.Config, beadsDir string) (DoctorCheck, *sql.DB) {
	host := cfg.GetDoltServerHost()
	port := doltserver.DefaultConfig(beadsDir).Port
	user := cfg.GetDoltServerUser()

	// Get password from environment (more secure than config file)
	password := os.Getenv("BEADS_DOLT_PASSWORD")

	// Build DSN without database (just to test server connectivity)
	var connStr string
	if password != "" {
		connStr = fmt.Sprintf("%s:%s@tcp(%s:%d)/?parseTime=true&timeout=5s",
			user, password, host, port)
	} else {
		connStr = fmt.Sprintf("%s@tcp(%s:%d)/?parseTime=true&timeout=5s",
			user, host, port)
	}

	db, err := sql.Open("mysql", connStr)
	if err != nil {
		return DoctorCheck{
			Name:     "Dolt Version",
			Status:   StatusError,
			Message:  "Failed to open connection",
			Detail:   err.Error(),
			Fix:      "Check MySQL driver and connection settings",
			Category: CategoryFederation,
		}, nil
	}

	// Set connection pool limits
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(30 * time.Second)

	// Test connectivity
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close() // Best effort cleanup
		return DoctorCheck{
			Name:     "Dolt Version",
			Status:   StatusError,
			Message:  "Server not responding",
			Detail:   err.Error(),
			Fix:      "Ensure dolt sql-server is running",
			Category: CategoryFederation,
		}, nil
	}

	// Query Dolt version
	var version string
	err = db.QueryRowContext(ctx, "SELECT dolt_version()").Scan(&version)
	if err != nil {
		// If dolt_version() doesn't exist, it's not a Dolt server
		if strings.Contains(err.Error(), "Unknown") || strings.Contains(err.Error(), "doesn't exist") {
			_ = db.Close() // Best effort cleanup
			return DoctorCheck{
				Name:     "Dolt Version",
				Status:   StatusError,
				Message:  "Server is not Dolt",
				Detail:   "dolt_version() function not found - this may be a MySQL server, not Dolt",
				Fix:      "Ensure you're connecting to a Dolt sql-server, not vanilla MySQL",
				Category: CategoryFederation,
			}, nil
		}
		_ = db.Close() // Best effort cleanup
		return DoctorCheck{
			Name:     "Dolt Version",
			Status:   StatusError,
			Message:  "Failed to query version",
			Detail:   err.Error(),
			Category: CategoryFederation,
		}, nil
	}

	return DoctorCheck{
		Name:     "Dolt Version",
		Status:   StatusOK,
		Message:  fmt.Sprintf("Dolt %s", version),
		Category: CategoryFederation,
	}, db
}

// checkDatabaseExists checks if the beads database exists
func checkDatabaseExists(db *sql.DB, database string) DoctorCheck {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Validate database name
	if !isValidIdentifier(database) {
		// Check if it's just hyphens (legacy names from before GH#2142 fix)
		if strings.ContainsRune(database, '-') && isValidIdentifier(strings.ReplaceAll(database, "-", "_")) {
			// Hyphenated name — functional but not recommended.
			// Continue with the check but we'll add a warning after.
		} else {
			return DoctorCheck{
				Name:     "Database Exists",
				Status:   StatusError,
				Message:  fmt.Sprintf("Invalid database name '%s'", database),
				Detail:   "Database name must be alphanumeric with underscores only",
				Category: CategoryFederation,
			}
		}
	}

	// Use SHOW DATABASES instead of INFORMATION_SCHEMA.SCHEMATA to avoid
	// crashing on phantom catalog entries (R-006, GH#2051, GH#2091).
	rows, err := db.QueryContext(ctx, "SHOW DATABASES")
	if err != nil {
		return DoctorCheck{
			Name:     "Database Exists",
			Status:   StatusError,
			Message:  "Failed to query databases",
			Detail:   err.Error(),
			Category: CategoryFederation,
		}
	}
	defer rows.Close()

	found := false
	for rows.Next() {
		var dbName string
		if err := rows.Scan(&dbName); err != nil {
			continue
		}
		if dbName == database {
			found = true
			break
		}
	}

	if !found {
		return DoctorCheck{
			Name:     "Database Exists",
			Status:   StatusError,
			Message:  fmt.Sprintf("Database '%s' not found", database),
			Fix:      fmt.Sprintf("Run 'bd init' to create the '%s' database", database),
			Category: CategoryFederation,
		}
	}

	// Switch to the database
	// Note: USE cannot use parameterized queries, but we validated the identifier above.
	// Backtick-quote to support hyphenated legacy names (GH#2142).
	_, err = db.ExecContext(ctx, "USE `"+database+"`") // #nosec G201 - database validated by isValidIdentifier
	if err != nil {
		return DoctorCheck{
			Name:     "Database Exists",
			Status:   StatusError,
			Message:  fmt.Sprintf("Cannot access database '%s'", database),
			Detail:   err.Error(),
			Category: CategoryFederation,
		}
	}

	// Warn about hyphenated names — functional but new projects use underscores
	if strings.ContainsRune(database, '-') {
		return DoctorCheck{
			Name:     "Database Exists",
			Status:   StatusWarning,
			Message:  fmt.Sprintf("Database '%s' uses hyphens (legacy naming)", database),
			Detail:   "New projects use underscores. To migrate: export data, run 'bd init --force', re-import.",
			Category: CategoryFederation,
		}
	}

	return DoctorCheck{
		Name:     "Database Exists",
		Status:   StatusOK,
		Message:  fmt.Sprintf("Database '%s' accessible", database),
		Category: CategoryFederation,
	}
}

// isValidIdentifier checks if a string is a valid SQL identifier
// (alphanumeric and underscore only, doesn't start with a number)
func isValidIdentifier(s string) bool {
	if len(s) == 0 {
		return false
	}
	for i, c := range s {
		if i == 0 && c >= '0' && c <= '9' {
			return false // Can't start with a number
		}
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_') {
			return false
		}
	}
	return true
}

// checkSchemaCompatible checks if the beads tables are queryable
func checkSchemaCompatible(db *sql.DB, database string) DoctorCheck {
	// Note: database parameter reserved for future use (e.g., multi-database support)
	_ = database
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Try to query the issues table
	var count int
	err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues").Scan(&count)
	if err != nil {
		if strings.Contains(err.Error(), "doesn't exist") || strings.Contains(err.Error(), "Unknown table") {
			return DoctorCheck{
				Name:     "Schema Compatible",
				Status:   StatusError,
				Message:  "Issues table not found",
				Fix:      "Run 'bd init' to create schema",
				Category: CategoryFederation,
			}
		}
		return DoctorCheck{
			Name:     "Schema Compatible",
			Status:   StatusError,
			Message:  "Cannot query issues table",
			Detail:   err.Error(),
			Category: CategoryFederation,
		}
	}

	// Query metadata table for bd_version
	var bdVersion string
	err = db.QueryRowContext(ctx, "SELECT value FROM metadata WHERE `key` = 'bd_version'").Scan(&bdVersion)
	if err != nil && err != sql.ErrNoRows {
		if strings.Contains(err.Error(), "doesn't exist") || strings.Contains(err.Error(), "Unknown table") {
			return DoctorCheck{
				Name:     "Schema Compatible",
				Status:   StatusWarning,
				Message:  fmt.Sprintf("%d issues found (no metadata table)", count),
				Fix:      "Run 'bd migrate' to update schema",
				Category: CategoryFederation,
			}
		}
	}

	detail := fmt.Sprintf("%d issues", count)
	if bdVersion != "" {
		detail = fmt.Sprintf("%d issues (bd %s)", count, bdVersion)
	}

	return DoctorCheck{
		Name:     "Schema Compatible",
		Status:   StatusOK,
		Message:  detail,
		Category: CategoryFederation,
	}
}

// checkConnectionPool checks the connection pool health
func checkConnectionPool(db *sql.DB) DoctorCheck {
	stats := db.Stats()

	// Report pool statistics
	detail := fmt.Sprintf("open: %d, in_use: %d, idle: %d",
		stats.OpenConnections,
		stats.InUse,
		stats.Idle,
	)

	// Check for connection errors
	if stats.MaxIdleClosed > 0 || stats.MaxLifetimeClosed > 0 {
		detail += fmt.Sprintf("\nclosed: idle=%d, lifetime=%d",
			stats.MaxIdleClosed,
			stats.MaxLifetimeClosed,
		)
	}

	return DoctorCheck{
		Name:     "Connection Pool",
		Status:   StatusOK,
		Message:  "Pool healthy",
		Detail:   detail,
		Category: CategoryFederation,
	}
}
