package doctor

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/steveyegge/beads/cmd/bd/doctor/fix"
	"github.com/steveyegge/beads/internal/beads"
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

	// Dolt backend: directory-backed store; version lives in metadata table.
	if backend == configfile.BackendDolt {
		doltPath := filepath.Join(beadsDir, "dolt")
		if _, err := os.Stat(doltPath); os.IsNotExist(err) {
			// If JSONL exists, treat as fresh clone / needs init.
			issuesJSONL := filepath.Join(beadsDir, "issues.jsonl")
			beadsJSONL := filepath.Join(beadsDir, "beads.jsonl")
			_, issuesErr := os.Stat(issuesJSONL)
			_, beadsErr := os.Stat(beadsJSONL)
			if issuesErr == nil || beadsErr == nil {
				return DoctorCheck{
					Name:    "Database",
					Status:  StatusWarning,
					Message: "Fresh clone detected (no dolt database)",
					Detail:  "Storage: Dolt",
					Fix:     "Run 'bd init' to create and hydrate the dolt database",
				}
			}
			return DoctorCheck{
				Name:    "Database",
				Status:  StatusError,
				Message: "No dolt database found",
				Detail:  "Storage: Dolt",
				Fix:     "Run 'bd init' to create database",
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
				Fix:     "Run 'bd doctor --fix' to recover from JSONL backup, or manually: rm -rf .beads/dolt && bd init",
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
				Fix:     "Database may be corrupted. Run 'bd doctor --fix' to recover from JSONL backup",
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

	// Check metadata.json first for custom database name
	var dbPath string
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil && cfg.Database != "" {
		dbPath = cfg.DatabasePath(beadsDir)
	} else {
		// Fall back to canonical database name
		dbPath = filepath.Join(beadsDir, beads.CanonicalDatabaseName)
	}

	// Check if database file exists
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		// Check if JSONL exists
		// Check canonical (issues.jsonl) first, then legacy (beads.jsonl)
		issuesJSONL := filepath.Join(beadsDir, "issues.jsonl")
		beadsJSONL := filepath.Join(beadsDir, "beads.jsonl")

		var jsonlPath string
		if _, err := os.Stat(issuesJSONL); err == nil {
			jsonlPath = issuesJSONL
		} else if _, err := os.Stat(beadsJSONL); err == nil {
			jsonlPath = beadsJSONL
		}

		if jsonlPath != "" {
			// JSONL exists but no database - check if this is no-db mode or fresh clone
			// Use proper YAML parsing to detect no-db mode
			if isNoDbModeConfigured(beadsDir) {
				return DoctorCheck{
					Name:    "Database",
					Status:  StatusOK,
					Message: "JSONL-only mode",
					Detail:  "Using issues.jsonl (no SQLite database)",
				}
			}

			// This is a fresh clone - JSONL exists but no database and not no-db mode
			// Count issues and detect prefix for helpful suggestion
			issueCount := countIssuesInJSONLFile(jsonlPath)
			prefix := detectPrefixFromJSONL(jsonlPath)

			message := "Fresh clone detected (no database)"
			detail := fmt.Sprintf("Found %d issue(s) in JSONL that need to be imported", issueCount)
			fix := "Run 'bd init' to hydrate the database from JSONL"
			if prefix != "" {
				fix = fmt.Sprintf("Run 'bd init' to hydrate the database (detected prefix: %s)", prefix)
			}

			return DoctorCheck{
				Name:    "Database",
				Status:  StatusWarning,
				Message: message,
				Detail:  detail,
				Fix:     fix,
			}
		}

		return DoctorCheck{
			Name:    "Database",
			Status:  StatusError,
			Message: "No beads.db found",
			Fix:     "Run 'bd init' to create database",
		}
	}

	// Get database version
	dbVersion := getDatabaseVersionFromPath(dbPath)

	if dbVersion == "unknown" {
		return DoctorCheck{
			Name:    "Database",
			Status:  StatusError,
			Message: "Unable to read database version",
			Detail:  "Storage: SQLite",
			Fix:     "Database may be corrupted. Try 'bd migrate'",
		}
	}

	if dbVersion == "pre-0.17.5" {
		return DoctorCheck{
			Name:    "Database",
			Status:  StatusWarning,
			Message: fmt.Sprintf("version %s (very old)", dbVersion),
			Detail:  "Storage: SQLite",
			Fix:     "Run 'bd migrate' to upgrade database schema",
		}
	}

	if dbVersion != cliVersion {
		return DoctorCheck{
			Name:    "Database",
			Status:  StatusWarning,
			Message: fmt.Sprintf("version %s (CLI: %s)", dbVersion, cliVersion),
			Detail:  "Storage: SQLite",
			Fix:     "Run 'bd migrate' to sync database with CLI version",
		}
	}

	return DoctorCheck{
		Name:    "Database",
		Status:  StatusOK,
		Message: fmt.Sprintf("version %s", dbVersion),
		Detail:  "Storage: SQLite",
	}
}

// CheckSchemaCompatibility checks if all required tables and columns are present
func CheckSchemaCompatibility(path string) DoctorCheck {
	backend, beadsDir := getBackendAndBeadsDir(path)

	// Dolt backend: no SQLite schema probe. Instead, run a lightweight query sanity check.
	if backend == configfile.BackendDolt {
		if info, err := os.Stat(filepath.Join(beadsDir, "dolt")); err != nil || !info.IsDir() {
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
				Fix:     "Run 'bd doctor --fix' to recover from JSONL backup, or manually: rm -rf .beads/dolt && bd init",
			}
		}

		return DoctorCheck{
			Name:    "Schema Compatibility",
			Status:  StatusOK,
			Message: "Basic queries succeeded",
			Detail:  "Storage: Dolt",
		}
	}

	// Check metadata.json first for custom database name
	var dbPath string
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil && cfg.Database != "" {
		dbPath = cfg.DatabasePath(beadsDir)
	} else {
		// Fall back to canonical database name
		dbPath = filepath.Join(beadsDir, beads.CanonicalDatabaseName)
	}

	// If no database, skip this check
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return DoctorCheck{
			Name:    "Schema Compatibility",
			Status:  StatusOK,
			Message: "N/A (no database)",
		}
	}

	// Open database for schema probe
	// Note: We can't use the global 'store' because doctor can check arbitrary paths
	db, err := sql.Open("sqlite3", sqliteConnString(dbPath, true))
	if err != nil {
		return DoctorCheck{
			Name:    "Schema Compatibility",
			Status:  StatusError,
			Message: "Failed to open database",
			Detail:  err.Error(),
			Fix:     "Database may be corrupted. Try 'bd migrate' or restore from backup",
		}
	}
	defer db.Close()

	// Run schema probe against SQLite database
	// This is a simplified version for legacy SQLite databases
	// Check all critical tables and columns
	criticalChecks := map[string][]string{
		"issues":         {"id", "title", "content_hash", "external_ref", "compacted_at", "close_reason", "pinned", "sender", "ephemeral"},
		"dependencies":   {"issue_id", "depends_on_id", "type", "metadata", "thread_id"},
		"child_counters": {"parent_id", "last_child"},
		"export_hashes":  {"issue_id", "content_hash"},
	}

	var missingElements []string
	for table, columns := range criticalChecks {
		// Try to query all columns
		query := fmt.Sprintf(
			"SELECT %s FROM %s LIMIT 0",
			strings.Join(columns, ", "),
			table,
		) // #nosec G201 -- table/column names sourced from hardcoded map
		_, err := db.Exec(query)

		if err != nil {
			errMsg := err.Error()
			if strings.Contains(errMsg, "no such table") {
				missingElements = append(missingElements, fmt.Sprintf("table:%s", table))
			} else if strings.Contains(errMsg, "no such column") {
				// Find which columns are missing
				for _, col := range columns {
					colQuery := fmt.Sprintf("SELECT %s FROM %s LIMIT 0", col, table) // #nosec G201 -- names come from static schema definition
					if _, colErr := db.Exec(colQuery); colErr != nil && strings.Contains(colErr.Error(), "no such column") {
						missingElements = append(missingElements, fmt.Sprintf("%s.%s", table, col))
					}
				}
			}
		}
	}

	if len(missingElements) > 0 {
		return DoctorCheck{
			Name:    "Schema Compatibility",
			Status:  StatusError,
			Message: "Database schema is incomplete or incompatible",
			Detail:  fmt.Sprintf("Missing: %s", strings.Join(missingElements, ", ")),
			Fix:     "Run 'bd migrate' to upgrade schema",
		}
	}

	return DoctorCheck{
		Name:    "Schema Compatibility",
		Status:  StatusOK,
		Message: "All required tables and columns present",
	}
}

// CheckDatabaseIntegrity runs SQLite's PRAGMA integrity_check
func CheckDatabaseIntegrity(path string) DoctorCheck {
	backend, beadsDir := getBackendAndBeadsDir(path)

	// Dolt backend: SQLite PRAGMA integrity_check doesn't apply.
	// We do a lightweight read-only sanity check instead.
	if backend == configfile.BackendDolt {
		if info, err := os.Stat(filepath.Join(beadsDir, "dolt")); err != nil || !info.IsDir() {
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
				Fix:     "Run 'bd doctor --fix' to recover from JSONL backup, or manually: rm -rf .beads/dolt && bd init",
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
			Detail:  "Storage: Dolt (no SQLite integrity_check equivalent)",
		}
	}

	// Get database path (same logic as CheckSchemaCompatibility)
	var dbPath string
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil && cfg.Database != "" {
		dbPath = cfg.DatabasePath(beadsDir)
	} else {
		dbPath = filepath.Join(beadsDir, beads.CanonicalDatabaseName)
	}

	// If no database, skip this check
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return DoctorCheck{
			Name:    "Database Integrity",
			Status:  StatusOK,
			Message: "N/A (no database)",
		}
	}

	// Open database in read-only mode for integrity check
	db, err := sql.Open("sqlite3", sqliteConnString(dbPath, true))
	if err != nil {
		// Check if JSONL recovery is possible
		jsonlCount, _, jsonlErr := CountJSONLIssues(filepath.Join(beadsDir, "issues.jsonl"))
		if jsonlErr != nil {
			jsonlCount, _, jsonlErr = CountJSONLIssues(filepath.Join(beadsDir, "beads.jsonl"))
		}
		jsonlAvailable := jsonlErr == nil && jsonlCount > 0

		errorType, recoverySteps := classifyDatabaseError(err.Error(), jsonlCount, jsonlAvailable)
		return DoctorCheck{
			Name:    "Database Integrity",
			Status:  StatusError,
			Message: errorType,
			Detail:  fmt.Sprintf("%s\n\nError: %s", recoverySteps, err.Error()),
			Fix:     "See recovery steps above",
		}
	}
	defer db.Close()

	// Run PRAGMA integrity_check
	// This checks the entire database for corruption
	rows, err := db.Query("PRAGMA integrity_check")
	if err != nil {
		// Check if JSONL recovery is possible
		jsonlCount, _, jsonlErr := CountJSONLIssues(filepath.Join(beadsDir, "issues.jsonl"))
		if jsonlErr != nil {
			jsonlCount, _, jsonlErr = CountJSONLIssues(filepath.Join(beadsDir, "beads.jsonl"))
		}
		jsonlAvailable := jsonlErr == nil && jsonlCount > 0

		errorType, recoverySteps := classifyDatabaseError(err.Error(), jsonlCount, jsonlAvailable)
		// Override default error type for this specific case
		if errorType == "Failed to open database" {
			errorType = "Failed to run integrity check"
		}
		return DoctorCheck{
			Name:    "Database Integrity",
			Status:  StatusError,
			Message: errorType,
			Detail:  fmt.Sprintf("%s\n\nError: %s", recoverySteps, err.Error()),
			Fix:     "See recovery steps above",
		}
	}
	defer rows.Close()

	var results []string
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			continue
		}
		results = append(results, result)
	}

	// "ok" means no corruption detected
	if len(results) == 1 && results[0] == "ok" {
		return DoctorCheck{
			Name:    "Database Integrity",
			Status:  StatusOK,
			Message: "No corruption detected",
		}
	}

	// Any other result indicates corruption - check if JSONL recovery is possible
	jsonlCount, _, jsonlErr := CountJSONLIssues(filepath.Join(beadsDir, "issues.jsonl"))
	if jsonlErr != nil {
		// Try alternate name
		jsonlCount, _, jsonlErr = CountJSONLIssues(filepath.Join(beadsDir, "beads.jsonl"))
	}

	if jsonlErr == nil && jsonlCount > 0 {
		return DoctorCheck{
			Name:    "Database Integrity",
			Status:  StatusError,
			Message: fmt.Sprintf("Database corruption detected (JSONL has %d issues for recovery)", jsonlCount),
			Detail:  strings.Join(results, "; "),
			Fix:     "Run 'bd doctor --fix' to recover from JSONL backup",
		}
	}

	return DoctorCheck{
		Name:    "Database Integrity",
		Status:  StatusError,
		Message: "Database corruption detected",
		Detail:  strings.Join(results, "; "),
		Fix:     "Run 'bd doctor --fix' to back up the corrupt DB and rebuild from JSONL (if available), or restore from backup",
	}
}

// Fix functions

// FixDatabaseConfig auto-detects and fixes metadata.json database/JSONL config mismatches
func FixDatabaseConfig(path string) error {
	return fix.DatabaseConfig(path)
}

// Helper functions

// classifyDatabaseError classifies a database error and returns appropriate recovery guidance.
// Returns the error type description and recovery steps based on error message and JSONL availability.
func classifyDatabaseError(errMsg string, jsonlCount int, jsonlAvailable bool) (errorType, recoverySteps string) {
	switch {
	case strings.Contains(errMsg, "database is locked"):
		errorType = "Database is locked"
		recoverySteps = "1. Check for running bd processes: ps aux | grep bd\n" +
			"2. Kill any stale processes\n" +
			"3. Remove stale locks: bd doctor --fix\n" +
			"4. Retry: bd doctor"

	case strings.Contains(errMsg, "not a database") || strings.Contains(errMsg, "file is not a database"):
		errorType = "File is not a valid SQLite database"
		if jsonlAvailable {
			recoverySteps = fmt.Sprintf("Database file is corrupted beyond repair.\n\n"+
				"Recovery steps:\n"+
				"1. Backup corrupt database: mv .beads/beads.db .beads/beads.db.broken\n"+
				"2. Rebuild from JSONL (%d issues): bd doctor --fix --force --source=jsonl\n"+
				"3. Verify: bd stats", jsonlCount)
		} else {
			recoverySteps = "Database file is corrupted and no JSONL backup found.\n" +
				"Manual recovery required:\n" +
				"1. Restore from git: git checkout HEAD -- .beads/issues.jsonl\n" +
				"2. Rebuild database: bd doctor --fix --force"
		}

	case strings.Contains(errMsg, "migration") || strings.Contains(errMsg, "validation failed"):
		errorType = "Database migration or validation failed"
		if jsonlAvailable {
			recoverySteps = fmt.Sprintf("Database has validation errors (possibly orphaned dependencies).\n\n"+
				"Recovery steps:\n"+
				"1. Backup database: mv .beads/beads.db .beads/beads.db.broken\n"+
				"2. Rebuild from JSONL (%d issues): bd doctor --fix --force --source=jsonl\n"+
				"3. Verify: bd stats\n\n"+
				"Alternative: bd doctor --fix --force (attempts to repair in-place)", jsonlCount)
		} else {
			recoverySteps = "Database validation failed and no JSONL backup available.\n" +
				"Try: bd doctor --fix --force"
		}

	default:
		errorType = "Failed to open database"
		if jsonlAvailable {
			recoverySteps = fmt.Sprintf("Run 'bd doctor --fix --source=jsonl' to rebuild from JSONL (%d issues)", jsonlCount)
		} else {
			recoverySteps = "Run 'bd doctor --fix --force' to attempt recovery"
		}
	}
	return
}

// getDatabaseVersionFromPath reads the database version from the given path
func getDatabaseVersionFromPath(dbPath string) string {
	db, err := sql.Open("sqlite3", sqliteConnString(dbPath, true))
	if err != nil {
		return "unknown"
	}
	defer db.Close()

	// Try to read version from metadata table
	var version string
	err = db.QueryRow("SELECT value FROM metadata WHERE key = 'bd_version'").Scan(&version)
	if err == nil {
		return version
	}

	// Check if metadata table exists
	var tableName string
	err = db.QueryRow(`
		SELECT name FROM sqlite_master
		WHERE type='table' AND name='metadata'
	`).Scan(&tableName)

	if err == sql.ErrNoRows {
		return "pre-0.17.5"
	}

	return "unknown"
}

// CountJSONLIssues counts issues in the JSONL file and returns the count, prefixes, and any error
func CountJSONLIssues(jsonlPath string) (int, map[string]int, error) {
	// jsonlPath is safe: constructed from filepath.Join(beadsDir, hardcoded name)
	file, err := os.Open(jsonlPath) //nolint:gosec
	if err != nil {
		return 0, nil, fmt.Errorf("failed to open JSONL file: %w", err)
	}
	defer file.Close()

	count := 0
	prefixes := make(map[string]int)
	errorCount := 0

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		// Parse JSON to get the ID
		var issue map[string]interface{}
		if err := json.Unmarshal(line, &issue); err != nil {
			errorCount++
			continue
		}

		if id, ok := issue["id"].(string); ok && id != "" {
			count++
			// Extract prefix (everything before the last dash)
			lastDash := strings.LastIndex(id, "-")
			if lastDash != -1 {
				prefixes[id[:lastDash]]++
			} else {
				prefixes[id]++
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return count, prefixes, fmt.Errorf("failed to read JSONL file: %w", err)
	}

	if errorCount > 0 {
		return count, prefixes, fmt.Errorf("skipped %d malformed lines in JSONL", errorCount)
	}

	return count, prefixes, nil
}

// countIssuesInJSONLFile counts the number of valid issues in a JSONL file
// This is a wrapper around CountJSONLIssues that returns only the count
func countIssuesInJSONLFile(jsonlPath string) int {
	count, _, _ := CountJSONLIssues(jsonlPath) // Best effort: zero count / nil prefixes are safe defaults for diagnostic
	return count
}

// detectPrefixFromJSONL detects the most common prefix in a JSONL file
func detectPrefixFromJSONL(jsonlPath string) string {
	_, prefixes, _ := CountJSONLIssues(jsonlPath) // Best effort: zero count / nil prefixes are safe defaults for diagnostic
	if len(prefixes) == 0 {
		return ""
	}

	// Find the most common prefix
	var mostCommonPrefix string
	maxCount := 0
	for prefix, count := range prefixes {
		if count > maxCount {
			maxCount = count
			mostCommonPrefix = prefix
		}
	}
	return mostCommonPrefix
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

	// Dolt backend: this check uses SQLite-specific queries, skip for now
	if backend == configfile.BackendDolt {
		return DoctorCheck{
			Name:    "Large Database",
			Status:  StatusOK,
			Message: "N/A (dolt backend)",
		}
	}

	// Get database path
	var dbPath string
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil && cfg.Database != "" {
		dbPath = cfg.DatabasePath(beadsDir)
	} else {
		dbPath = filepath.Join(beadsDir, beads.CanonicalDatabaseName)
	}

	// If no database, skip this check
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return DoctorCheck{
			Name:    "Large Database",
			Status:  StatusOK,
			Message: "N/A (no database)",
		}
	}

	// Read threshold from config (default 5000, 0 = disabled)
	threshold := 5000
	db, err := sql.Open("sqlite3", "file:"+dbPath+"?mode=ro&_pragma=busy_timeout(30000)")
	if err != nil {
		return DoctorCheck{
			Name:    "Large Database",
			Status:  StatusOK,
			Message: "N/A (unable to open database)",
		}
	}
	defer db.Close()

	// Check for custom threshold in config table
	var thresholdStr string
	err = db.QueryRow("SELECT value FROM config WHERE key = ?", "doctor.suggest_pruning_issue_count").Scan(&thresholdStr)
	if err == nil {
		if _, err := fmt.Sscanf(thresholdStr, "%d", &threshold); err != nil {
			threshold = 5000 // Reset to default on parse error
		}
	}

	// If disabled, return OK
	if threshold == 0 {
		return DoctorCheck{
			Name:    "Large Database",
			Status:  StatusOK,
			Message: "Check disabled (threshold = 0)",
		}
	}

	// Count closed issues
	var closedCount int
	err = db.QueryRow("SELECT COUNT(*) FROM issues WHERE status = 'closed'").Scan(&closedCount)
	if err != nil {
		return DoctorCheck{
			Name:    "Large Database",
			Status:  StatusOK,
			Message: "N/A (unable to count issues)",
		}
	}

	// Check against threshold
	if closedCount > threshold {
		return DoctorCheck{
			Name:    "Large Database",
			Status:  StatusWarning,
			Message: fmt.Sprintf("%d closed issues (threshold: %d)", closedCount, threshold),
			Detail:  "Large number of closed issues may impact performance",
			Fix:     "Consider running 'bd cleanup --older-than 90' to prune old closed issues",
		}
	}

	return DoctorCheck{
		Name:    "Large Database",
		Status:  StatusOK,
		Message: fmt.Sprintf("%d closed issues (threshold: %d)", closedCount, threshold),
	}
}
