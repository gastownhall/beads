package doctor

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/steveyegge/beads/cmd/bd/doctor/fix"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"gopkg.in/yaml.v3"
)

// localConfig represents the config.yaml structure for no-db mode detection
type localConfig struct {
	SyncBranch string `yaml:"sync-branch"`
	NoDb       bool   `yaml:"no-db"`
}

// CheckDatabaseVersion checks the database version and migration status
func CheckDatabaseVersion(path string, cliVersion string) DoctorCheck {
	// Follow redirect to resolve actual beads directory
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))

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
	// Follow redirect to resolve actual beads directory
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))

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

	// Run schema probe (defined in internal/storage/sqlite/schema_probe.go)
	// This is a simplified version since we can't import the internal package directly
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
			Fix:     "Run 'bd migrate' to upgrade schema, or if daemon is running an old version, run 'bd daemons killall' to restart",
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
	// Follow redirect to resolve actual beads directory
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))

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

// CheckDatabaseJSONLSync checks if database and JSONL are in sync
func CheckDatabaseJSONLSync(path string) DoctorCheck {
	// Follow redirect to resolve actual beads directory
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))

	// Resolve database path (respects metadata.json override).
	dbPath := filepath.Join(beadsDir, beads.CanonicalDatabaseName)
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil && cfg.Database != "" {
		dbPath = cfg.DatabasePath(beadsDir)
	}

	// Find JSONL file (respects metadata.json override when set).
	jsonlPath := ""
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil {
		if cfg.JSONLExport != "" && !isSystemJSONLFilename(cfg.JSONLExport) {
			p := cfg.JSONLPath(beadsDir)
			if _, err := os.Stat(p); err == nil {
				jsonlPath = p
			}
		}
	}
	if jsonlPath == "" {
		for _, name := range []string{"issues.jsonl", "beads.jsonl"} {
			testPath := filepath.Join(beadsDir, name)
			if _, err := os.Stat(testPath); err == nil {
				jsonlPath = testPath
				break
			}
		}
	}

	// If no database, skip this check
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return DoctorCheck{
			Name:    "DB-JSONL Sync",
			Status:  StatusOK,
			Message: "N/A (no database)",
		}
	}

	// If no JSONL, skip this check
	if jsonlPath == "" {
		return DoctorCheck{
			Name:    "DB-JSONL Sync",
			Status:  StatusOK,
			Message: "N/A (no JSONL file)",
		}
	}

	// Try to read JSONL first (doesn't depend on database)
	jsonlCount, jsonlPrefixes, jsonlErr := CountJSONLIssues(jsonlPath)

	// Single database open for all queries (instead of 3 separate opens)
	db, err := sql.Open("sqlite3", sqliteConnString(dbPath, true))
	if err != nil {
		// Database can't be opened. If JSONL has issues, suggest recovery.
		if jsonlErr == nil && jsonlCount > 0 {
			return DoctorCheck{
				Name:    "DB-JSONL Sync",
				Status:  StatusWarning,
				Message: fmt.Sprintf("Database cannot be opened but JSONL contains %d issues", jsonlCount),
				Detail:  err.Error(),
				Fix:     fmt.Sprintf("Run 'bd import -i %s --rename-on-import' to recover issues from JSONL", filepath.Base(jsonlPath)),
			}
		}
		return DoctorCheck{
			Name:    "DB-JSONL Sync",
			Status:  StatusWarning,
			Message: "Unable to open database",
			Detail:  err.Error(),
		}
	}
	defer db.Close()

	// Get database count
	var dbCount int
	err = db.QueryRow("SELECT COUNT(*) FROM issues").Scan(&dbCount)
	if err != nil {
		// Database opened but can't query. If JSONL has issues, suggest recovery.
		if jsonlErr == nil && jsonlCount > 0 {
			return DoctorCheck{
				Name:    "DB-JSONL Sync",
				Status:  StatusWarning,
				Message: fmt.Sprintf("Database cannot be queried but JSONL contains %d issues", jsonlCount),
				Detail:  err.Error(),
				Fix:     fmt.Sprintf("Run 'bd import -i %s --rename-on-import' to recover issues from JSONL", filepath.Base(jsonlPath)),
			}
		}
		return DoctorCheck{
			Name:    "DB-JSONL Sync",
			Status:  StatusWarning,
			Message: "Unable to query database",
			Detail:  err.Error(),
		}
	}

	// Get database prefix
	var dbPrefix string
	err = db.QueryRow("SELECT value FROM config WHERE key = ?", "issue_prefix").Scan(&dbPrefix)
	if err != nil && err != sql.ErrNoRows {
		return DoctorCheck{
			Name:    "DB-JSONL Sync",
			Status:  StatusWarning,
			Message: "Unable to read database prefix",
			Detail:  err.Error(),
		}
	}

	// Use JSONL error if we got it earlier
	if jsonlErr != nil {
		fixMsg := "Run 'bd doctor --fix' to attempt recovery"
		if strings.Contains(jsonlErr.Error(), "malformed") {
			fixMsg = "Run 'bd doctor --fix' to back up and regenerate the JSONL from the database"
		}
		return DoctorCheck{
			Name:    "DB-JSONL Sync",
			Status:  StatusWarning,
			Message: "Unable to read JSONL file",
			Detail:  jsonlErr.Error(),
			Fix:     fixMsg,
		}
	}

	// Check for issues
	var issues []string

	// Count mismatch
	if dbCount != jsonlCount {
		issues = append(issues, fmt.Sprintf("Count mismatch: database has %d issues, JSONL has %d", dbCount, jsonlCount))
	}

	// Prefix mismatch (only check most common prefix in JSONL)
	if dbPrefix != "" && len(jsonlPrefixes) > 0 {
		var mostCommonPrefix string
		maxCount := 0
		for prefix, count := range jsonlPrefixes {
			if count > maxCount {
				maxCount = count
				mostCommonPrefix = prefix
			}
		}

		// Only warn if majority of issues have wrong prefix
		// BUT: recognize that <prefix>-mol and <prefix>-wisp are valid variants
		// created by molecule/wisp workflows (see internal/storage/sqlite/queries.go:166-170)
		if mostCommonPrefix != dbPrefix && maxCount > jsonlCount/2 {
			// Check if the common prefix is a known workflow variant of the db prefix
			isValidVariant := false
			for _, suffix := range []string{"-mol", "-wisp", "-eph"} {
				if mostCommonPrefix == dbPrefix+suffix {
					isValidVariant = true
					break
				}
			}

			if !isValidVariant {
				issues = append(issues, fmt.Sprintf("Prefix mismatch: database uses %q but most JSONL issues use %q", dbPrefix, mostCommonPrefix))
			}
		}
	}

	// If we found issues, report them
	if len(issues) > 0 {
		// Provide direction-specific guidance
		var fixMsg string
		if dbCount > jsonlCount {
			fixMsg = "Run 'bd doctor --fix' to automatically export DB to JSONL, or manually run 'bd export'"
		} else if jsonlCount > dbCount {
			fixMsg = "Run 'bd doctor --fix' to automatically import JSONL to DB, or manually run 'bd sync --import-only'"
		} else {
			// Equal counts but other issues (like prefix mismatch)
			fixMsg = "Run 'bd doctor --fix' to fix automatically, or manually run 'bd sync --import-only' or 'bd export' depending on which has newer data"
		}
		if strings.Contains(strings.Join(issues, " "), "Prefix mismatch") {
			fixMsg = "Run 'bd import -i " + filepath.Base(jsonlPath) + " --rename-on-import' to fix prefixes"
		}

		return DoctorCheck{
			Name:    "DB-JSONL Sync",
			Status:  StatusWarning,
			Message: strings.Join(issues, "; "),
			Fix:     fixMsg,
		}
	}

	// Check modification times (only if counts match)
	dbInfo, err := os.Stat(dbPath)
	if err != nil {
		return DoctorCheck{
			Name:    "DB-JSONL Sync",
			Status:  StatusWarning,
			Message: "Unable to check database file",
		}
	}

	jsonlInfo, err := os.Stat(jsonlPath)
	if err != nil {
		return DoctorCheck{
			Name:    "DB-JSONL Sync",
			Status:  StatusWarning,
			Message: "Unable to check JSONL file",
		}
	}

	if jsonlInfo.ModTime().After(dbInfo.ModTime()) {
		timeDiff := jsonlInfo.ModTime().Sub(dbInfo.ModTime())
		if timeDiff > 30*time.Second {
			return DoctorCheck{
				Name:    "DB-JSONL Sync",
				Status:  StatusWarning,
				Message: "JSONL is newer than database",
				Fix:     "Run 'bd sync --import-only' to import JSONL updates",
			}
		}
	}

	return DoctorCheck{
		Name:    "DB-JSONL Sync",
		Status:  StatusOK,
		Message: "Database and JSONL are in sync",
	}
}

// Fix functions

// FixDatabaseConfig auto-detects and fixes metadata.json database/JSONL config mismatches
func FixDatabaseConfig(path string) error {
	return fix.DatabaseConfig(path)
}

// FixDBJSONLSync fixes database-JSONL sync issues by running bd sync --import-only
func FixDBJSONLSync(path string) error {
	return fix.DBJSONLSync(path)
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
			"3. Remove stale locks: rm .beads/beads.db-shm .beads/beads.db-wal .beads/daemon.lock\n" +
			"4. Retry: bd doctor --fix"

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
	count, _, _ := CountJSONLIssues(jsonlPath)
	return count
}

// detectPrefixFromJSONL detects the most common prefix in a JSONL file
func detectPrefixFromJSONL(jsonlPath string) string {
	_, prefixes, _ := CountJSONLIssues(jsonlPath)
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
	// Follow redirect to resolve actual beads directory
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))

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
