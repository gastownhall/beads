package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

// backupSQLite copies the SQLite database to a timestamped backup file in the
// same directory. The original file is preserved. Returns the backup path.
func backupSQLite(sqlitePath string) (string, error) {
	dir := filepath.Dir(sqlitePath)
	base := filepath.Base(sqlitePath)
	ext := filepath.Ext(base)
	name := base[:len(base)-len(ext)]

	timestamp := time.Now().Format("20060102-150405")
	backupName := fmt.Sprintf("%s.backup-pre-dolt-%s%s", name, timestamp, ext)
	backupPath := filepath.Join(dir, backupName)

	// If backup already exists (same second), add counter suffix.
	if _, err := os.Stat(backupPath); err == nil {
		for i := 1; i <= 100; i++ {
			backupName = fmt.Sprintf("%s.backup-pre-dolt-%s-%d%s", name, timestamp, i, ext)
			backupPath = filepath.Join(dir, backupName)
			if _, err := os.Stat(backupPath); err != nil {
				break // File doesn't exist (or stat error) — use this name
			}
			if i == 100 {
				return "", fmt.Errorf("too many backup files for timestamp %s", timestamp)
			}
		}
	}

	src, err := os.Open(sqlitePath) // #nosec G304 - user-provided path
	if err != nil {
		return "", fmt.Errorf("opening sqlite database for backup: %w", err)
	}
	defer src.Close()

	dst, err := os.OpenFile(backupPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600) // #nosec G304 - derived from user path; O_EXCL prevents TOCTOU
	if err != nil {
		return "", fmt.Errorf("creating backup file: %w", err)
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		return "", fmt.Errorf("copying sqlite database: %w", err)
	}

	return backupPath, nil
}

// doltSystemDatabases are databases that Dolt/MySQL always creates.
// These are not user databases and should be ignored when checking
// for cross-project conflicts.
var doltSystemDatabases = map[string]bool{
	"information_schema": true,
	"mysql":              true,
	"performance_schema": true,
	"sys":                true,
}

// verifyServerTarget checks whether a Dolt server on the given port is an
// appropriate target for the expected database. It queries SHOW DATABASES
// and verifies the expected database name against what the server hosts.
//
// Returns nil if:
//   - No server is listening (connection refused — will be started later)
//   - Server is running and already hosts the expected database (idempotent)
//   - Server is running with only system databases (fresh server)
//   - Server is running with other user databases (shared server model —
//     logs the database list for diagnostics)
//
// Returns error if:
//   - Server is unreachable (timeout) or unqueryable (not a Dolt server)
//   - Port is non-zero but expectedDBName is empty
func verifyServerTarget(expectedDBName string, port int) error {
	if port == 0 {
		return nil
	}

	if expectedDBName == "" {
		return fmt.Errorf("empty database name — cannot verify server target")
	}

	host := "127.0.0.1"
	addr := fmt.Sprintf("%s:%d", host, port)

	// Check if anything is listening on the port
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		// Connection refused = no server running = safe to proceed.
		// But timeouts or other errors = unknown state = warn and abort.
		if opErr, ok := err.(*net.OpError); ok {
			if sysErr, ok := opErr.Err.(*os.SyscallError); ok {
				if sysErr.Syscall == "connect" {
					// ECONNREFUSED — no server, safe
					return nil
				}
			}
		}
		return fmt.Errorf("cannot verify server on port %d (unknown error, not safe to proceed): %w", port, err)
	}
	_ = conn.Close()

	// Server is listening. Query SHOW DATABASES to verify target.
	dsn := fmt.Sprintf("root@tcp(%s)/", addr)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return fmt.Errorf("connecting to server on port %d: %w", port, err)
	}
	defer db.Close()
	db.SetConnMaxLifetime(2 * time.Second)

	rows, err := db.Query("SHOW DATABASES")
	if err != nil {
		// Can't list databases — might be non-MySQL service or auth issue.
		// Treat as unsafe since we can't verify.
		return fmt.Errorf("cannot query databases on port %d (may not be a Dolt server): %w", port, err)
	}
	defer rows.Close()

	var userDatabases []string
	found := false
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			continue
		}
		if name == expectedDBName {
			found = true
		}
		if !doltSystemDatabases[name] {
			userDatabases = append(userDatabases, name)
		}
	}

	if found {
		// Our database already exists on this server — verified target match
		debug.Logf("verifyServerTarget: database %q found on port %d (idempotent)", expectedDBName, port)
		return nil
	}

	if len(userDatabases) == 0 {
		// Only system databases — fresh/clean server, safe to create our database
		debug.Logf("verifyServerTarget: fresh server on port %d (no user databases), will create %q", port, expectedDBName)
		return nil
	}

	// Server has user databases but NOT ours. In the shared server model
	// (Gas Town), this is expected — one Dolt server hosts many project
	// databases. Log the database list for diagnostics in case migration
	// writes to the wrong server.
	debug.Logf("verifyServerTarget: server on port %d has databases %v but not %q — "+
		"will create database (shared server model)", port, userDatabases, expectedDBName)
	fmt.Fprintf(os.Stderr, "  Note: Dolt server on port %d already hosts %d database(s); will create %q\n",
		port, len(userDatabases), expectedDBName)
	return nil
}

// verifyMigrationCounts compares source (SQLite) row counts against target
// (Dolt) row counts. Dolt counts must be >= source counts since there may be
// pre-existing issues in the Dolt database.
func verifyMigrationCounts(sourceIssueCount, sourceDepsCount, doltIssueCount, doltDepsCount int) error {
	var errs []string
	if doltIssueCount < sourceIssueCount {
		errs = append(errs, fmt.Sprintf(
			"issue count mismatch: source=%d, dolt=%d", sourceIssueCount, doltIssueCount,
		))
	}
	if doltDepsCount < sourceDepsCount {
		errs = append(errs, fmt.Sprintf(
			"dependency count mismatch: source=%d, dolt=%d", sourceDepsCount, doltDepsCount,
		))
	}
	if len(errs) > 0 {
		return fmt.Errorf("migration verification failed: %s", strings.Join(errs, "; "))
	}
	return nil
}

// verifyMigrationData connects to the Dolt server independently (not via the
// DoltStore) and verifies that the migrated data matches the source. This
// catches issues that count-only verification misses: wrong server target,
// corrupted imports, partial writes.
//
// Checks performed:
//  1. Issue count matches source
//  2. Dependency count matches source
//  3. Spot-check: first and last issue IDs and titles match
func verifyMigrationData(sourceData *migrationData, dbName string, host string, port int, user string, password string) error {
	if sourceData.issueCount == 0 {
		// Empty database — nothing to verify
		return nil
	}

	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s", user, password, host, port, dbName)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return fmt.Errorf("connecting to Dolt for verification: %w", err)
	}
	defer db.Close()
	db.SetConnMaxLifetime(5 * time.Second)

	// 1. Verify issue count
	var doltIssueCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM issues").Scan(&doltIssueCount); err != nil {
		return fmt.Errorf("querying Dolt issue count: %w", err)
	}
	if doltIssueCount < sourceData.issueCount {
		return fmt.Errorf("issue count mismatch: source=%d, dolt=%d", sourceData.issueCount, doltIssueCount)
	}

	// 2. Verify dependency count
	var doltDepsCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM dependencies").Scan(&doltDepsCount); err != nil {
		// dependencies table might not exist in very old schemas
		debug.Logf("verifyMigrationData: dependencies count query failed (non-fatal): %v", err)
	} else {
		sourceDepsCount := 0
		for _, deps := range sourceData.depsMap {
			sourceDepsCount += len(deps)
		}
		if doltDepsCount < sourceDepsCount {
			return fmt.Errorf("dependency count mismatch: source=%d, dolt=%d", sourceDepsCount, doltDepsCount)
		}
	}

	// 3. Spot-check: verify first and last issue by ID and title
	if len(sourceData.issues) > 0 {
		firstSource := sourceData.issues[0]
		lastSource := sourceData.issues[len(sourceData.issues)-1]

		// Check first issue
		var doltTitle string
		err := db.QueryRow("SELECT title FROM issues WHERE id = ?", firstSource.ID).Scan(&doltTitle)
		if err != nil {
			return fmt.Errorf("spot-check failed: first issue %q not found in Dolt: %w", firstSource.ID, err)
		}
		if doltTitle != firstSource.Title {
			return fmt.Errorf("spot-check failed: first issue %q title mismatch: source=%q, dolt=%q",
				firstSource.ID, firstSource.Title, doltTitle)
		}

		// Check last issue (only if different from first)
		if lastSource.ID != firstSource.ID {
			err = db.QueryRow("SELECT title FROM issues WHERE id = ?", lastSource.ID).Scan(&doltTitle)
			if err != nil {
				return fmt.Errorf("spot-check failed: last issue %q not found in Dolt: %w", lastSource.ID, err)
			}
			if doltTitle != lastSource.Title {
				return fmt.Errorf("spot-check failed: last issue %q title mismatch: source=%q, dolt=%q",
					lastSource.ID, lastSource.Title, doltTitle)
			}
		}
	}

	debug.Logf("verifyMigrationData: verified %d issues, spot-checked first/last titles", doltIssueCount)
	return nil
}

// migrationParams holds the configuration needed for the shared migration
// safety phases (verify → import → commit → verify → finalize).
type migrationParams struct {
	beadsDir   string
	sqlitePath string
	backupPath string
	data       *migrationData
	doltCfg    *dolt.Config
	dbName     string
	// Server connection info for independent verification
	serverHost     string
	serverPort     int
	serverUser     string
	serverPassword string
}

// runMigrationPhases runs the common post-extraction migration phases shared
// by both the CGO (migrate_auto.go) and shim (migrate_shim.go) paths:
//
//  1. Verify server target (ensure we're writing to the right Dolt server)
//  2. Create Dolt store and import data
//  3. Set sync mode and commit
//  4. Verify migration data (independent re-query with spot-checks)
//  5. Finalize (update metadata, rename SQLite)
//
// On failure, performs rollback: removes dolt dir and restores original config.
// Returns (imported, skipped) counts on success, or error on failure.
func runMigrationPhases(ctx context.Context, params *migrationParams) (imported int, skipped int, err error) {
	beadsDir := params.beadsDir
	sqlitePath := params.sqlitePath
	backupPath := params.backupPath
	data := params.data
	doltCfg := params.doltCfg
	dbName := params.dbName

	// Verify server target — don't write to the wrong Dolt server
	if err := verifyServerTarget(dbName, params.serverPort); err != nil {
		return 0, 0, fmt.Errorf("server check failed: %w\n\nTo fix:\n"+
			"  1. Stop the other project's Dolt server\n"+
			"  2. Or set BEADS_DOLT_SERVER_PORT to a unique port for this project\n"+
			"  Your SQLite database is intact. Backup at: %s", err, backupPath)
	}

	// Save original config for rollback
	originalCfg, _ := configfile.Load(beadsDir)

	// Create Dolt store and import
	doltPath := doltCfg.Path
	fmt.Fprintf(os.Stderr, "Creating Dolt database...\n")
	doltStore, err := dolt.New(ctx, doltCfg)
	if err != nil {
		return 0, 0, fmt.Errorf("dolt init failed: %w\n"+
			"Hint: ensure the Dolt server is running, then retry any bd command\n"+
			"  Your SQLite database is intact. Backup at: %s", err, backupPath)
	}

	fmt.Fprintf(os.Stderr, "Importing data...\n")
	imported, skipped, importErr := importToDolt(ctx, doltStore, data)
	if importErr != nil {
		_ = doltStore.Close()
		_ = os.RemoveAll(doltPath) // Safe: doltPath was just created by us
		return 0, 0, fmt.Errorf("import failed: %w\n"+
			"  Your SQLite database is intact. Backup at: %s", importErr, backupPath)
	}

	// Set sync mode in Dolt config table
	if err := doltStore.SetConfig(ctx, "sync.mode", "dolt-native"); err != nil {
		debug.Logf("migration: failed to set sync.mode: %v", err)
	}

	// Commit the migration
	commitMsg := fmt.Sprintf("Auto-migrate from SQLite: %d issues imported", imported)
	if err := doltStore.Commit(ctx, commitMsg); err != nil {
		debug.Logf("migration: failed to create Dolt commit: %v", err)
	}

	_ = doltStore.Close()

	// Verify migration data by independently querying the Dolt server.
	// This catches issues that the import return values alone cannot:
	// wrong server target, partial writes, data corruption.
	if err := verifyMigrationData(data, dbName, params.serverHost, params.serverPort, params.serverUser, params.serverPassword); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: migration verification failed: %v\n", err)
		fmt.Fprintf(os.Stderr, "  Your SQLite database is intact. Backup at: %s\n", backupPath)
		// Rollback: remove dolt dir, restore metadata
		_ = os.RemoveAll(doltPath)
		if originalCfg != nil {
			if rbErr := rollbackMetadata(beadsDir, originalCfg); rbErr != nil {
				fmt.Fprintf(os.Stderr, "Warning: metadata rollback also failed: %v\n", rbErr)
			}
		}
		return 0, 0, fmt.Errorf("verification failed: %w", err)
	}

	// Finalize — update metadata and rename SQLite (atomic cutover)
	if err := finalizeMigration(beadsDir, sqlitePath, dbName); err != nil {
		return imported, skipped, fmt.Errorf("finalization failed: %w\n"+
			"  Data is in Dolt but metadata may be inconsistent.\n"+
			"  Run 'bd doctor --fix' to repair.", err)
	}

	return imported, skipped, nil
}

// finalizeMigration updates metadata and config to reflect the completed
// migration, then renames the SQLite file. This is the ONLY function that
// modifies metadata and should be called last after verification succeeds.
func finalizeMigration(beadsDir string, sqlitePath string, dbName string) error {
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		return fmt.Errorf("loading metadata: %w", err)
	}
	if cfg == nil {
		cfg = configfile.DefaultConfig()
	}

	cfg.Backend = configfile.BackendDolt
	cfg.Database = "dolt"
	cfg.DoltDatabase = dbName

	if err := cfg.Save(beadsDir); err != nil {
		return fmt.Errorf("saving metadata: %w", err)
	}

	// Write sync.mode to config.yaml so future runs know we migrated.
	// This is best-effort: config package may not be initialized during
	// auto-migration (which runs before full CLI setup). The metadata.json
	// Backend field is the authoritative source of truth.
	if err := config.SaveConfigValue("sync.mode", string(config.SyncModeDoltNative), beadsDir); err != nil {
		// Non-fatal — metadata.json is already updated
		debug.Logf("finalizeMigration: config.yaml sync.mode write skipped: %v", err)
	}

	// Rename SQLite file to mark it as migrated.
	migratedPath := sqlitePath + ".migrated"
	if err := os.Rename(sqlitePath, migratedPath); err != nil {
		return fmt.Errorf("renaming sqlite database: %w", err)
	}

	return nil
}

// rollbackMetadata restores metadata.json to the original configuration.
// Called when migration fails after metadata was partially modified.
func rollbackMetadata(beadsDir string, originalCfg *configfile.Config) error {
	if originalCfg == nil {
		return fmt.Errorf("no original config to restore")
	}
	return originalCfg.Save(beadsDir)
}
