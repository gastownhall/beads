//go:build cgo

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

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
	storagefactory "github.com/steveyegge/beads/internal/storage/factory"
	"github.com/steveyegge/beads/internal/utils"
)

// MigrationValidationResult provides machine-parseable migration validation output.
// This struct is designed to be consumed by Claude and other automation tools.
type MigrationValidationResult struct {
	Phase              string         `json:"phase"`                // "pre-migration" or "post-migration"
	Ready              bool           `json:"ready"`                // true if migration can proceed/succeeded
	Backend            string         `json:"backend"`              // current backend: "sqlite", "dolt", or "jsonl-only"
	JSONLCount         int            `json:"jsonl_count"`          // issue count in JSONL
	SQLiteCount        int            `json:"sqlite_count"`         // issue count in SQLite (pre-migration)
	DoltCount          int            `json:"dolt_count"`           // issue count in Dolt (post-migration)
	MissingInDB        []string       `json:"missing_in_db"`        // issue IDs in JSONL but not in DB (sample)
	MissingInJSONL     []string       `json:"missing_in_jsonl"`     // issue IDs in DB but not in JSONL (sample)
	Errors             []string       `json:"errors"`               // blocking errors
	Warnings           []string       `json:"warnings"`             // non-blocking warnings
	JSONLValid         bool           `json:"jsonl_valid"`          // true if JSONL is parseable
	JSONLMalformed     int            `json:"jsonl_malformed"`      // count of malformed JSONL lines
	DoltHealthy        bool           `json:"dolt_healthy"`         // true if Dolt DB is healthy
	DoltLocked         bool           `json:"dolt_locked"`          // true if Dolt has uncommitted changes
	SchemaValid        bool           `json:"schema_valid"`         // true if schema is complete
	RecommendedFix     string         `json:"recommended_fix"`      // suggested command to fix issues
	ForeignPrefixCount int            `json:"foreign_prefix_count"` // count of issues with non-local prefixes (cross-rig contamination)
	ForeignPrefixes    map[string]int `json:"foreign_prefixes"`     // prefix -> count for foreign-prefix issues
}

// CheckMigrationReadiness validates that a beads installation is ready for Dolt migration.
// This is a pre-migration check that ensures:
// 1. JSONL file exists and is valid (parseable, no corruption)
// 2. All issues in JSONL are also in SQLite (or explains discrepancies)
// 3. No blocking issues prevent migration
//
// Returns a doctor check suitable for standard output and a detailed result for automation.
func CheckMigrationReadiness(path string) (DoctorCheck, MigrationValidationResult) {
	result := MigrationValidationResult{
		Phase:       "pre-migration",
		Ready:       true,
		JSONLValid:  true,
		SchemaValid: true,
	}

	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))

	// Check if .beads exists
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		result.Ready = false
		result.Errors = append(result.Errors, "No .beads directory found")
		return DoctorCheck{
			Name:     "Migration Readiness",
			Status:   StatusError,
			Message:  "No beads installation found",
			Fix:      "Run 'bd init' first to create a beads installation",
			Category: CategoryMaintenance,
		}, result
	}

	// Detect current backend
	result.Backend = GetBackend(beadsDir)

	// Already on Dolt - no migration needed
	if result.Backend == configfile.BackendDolt {
		return DoctorCheck{
			Name:     "Migration Readiness",
			Status:   StatusOK,
			Message:  "Already using Dolt backend",
			Category: CategoryMaintenance,
		}, result
	}

	// Find JSONL file
	jsonlPath := findJSONLFile(beadsDir)
	if jsonlPath == "" {
		result.Ready = false
		result.Errors = append(result.Errors, "No JSONL file found")
		return DoctorCheck{
			Name:     "Migration Readiness",
			Status:   StatusError,
			Message:  "No JSONL file found",
			Detail:   "Migration requires issues.jsonl or beads.jsonl",
			Fix:      "Run 'bd export' to create JSONL file from database",
			Category: CategoryMaintenance,
		}, result
	}

	// Validate JSONL integrity
	jsonlCount, malformed, ids, err := validateJSONLForMigration(jsonlPath)
	result.JSONLCount = jsonlCount
	result.JSONLMalformed = malformed
	if err != nil {
		result.Ready = false
		result.JSONLValid = false
		result.Errors = append(result.Errors, fmt.Sprintf("JSONL validation failed: %v", err))
		return DoctorCheck{
			Name:     "Migration Readiness",
			Status:   StatusError,
			Message:  fmt.Sprintf("JSONL has %d malformed lines", malformed),
			Detail:   err.Error(),
			Fix:      "Run 'bd doctor --fix' to repair JSONL from database",
			Category: CategoryMaintenance,
		}, result
	}

	if malformed > 0 {
		result.JSONLValid = false
		result.Warnings = append(result.Warnings, fmt.Sprintf("%d malformed lines in JSONL (skipped)", malformed))
	}

	// Check SQLite database if it exists
	dbPath := getSQLiteDBPath(beadsDir)
	if _, err := os.Stat(dbPath); err == nil {
		result.Backend = "sqlite"

		// Compare JSONL with SQLite
		sqliteCount, missingInDB, missingInJSONL, err := compareSQLiteWithJSONL(dbPath, ids)
		result.SQLiteCount = sqliteCount
		result.MissingInDB = missingInDB
		result.MissingInJSONL = missingInJSONL

		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("SQLite comparison failed: %v", err))
		}

		if len(missingInDB) > 0 {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("%d issues in JSONL not in SQLite (will be imported during migration)", len(missingInDB)))
		}

		if len(missingInJSONL) > 0 {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("%d issues in SQLite not in JSONL (ephemeral or deleted)", len(missingInJSONL)))
		}
	} else {
		result.Backend = "jsonl-only"
	}

	// Build status message
	if len(result.Errors) > 0 {
		result.Ready = false
		return DoctorCheck{
			Name:     "Migration Readiness",
			Status:   StatusError,
			Message:  fmt.Sprintf("Not ready: %d error(s)", len(result.Errors)),
			Detail:   strings.Join(result.Errors, "\n"),
			Fix:      "Fix errors before running migration",
			Category: CategoryMaintenance,
		}, result
	}

	status := StatusOK
	message := fmt.Sprintf("Ready (%d issues in JSONL)", jsonlCount)
	if len(result.Warnings) > 0 {
		status = StatusWarning
		message = fmt.Sprintf("Ready with warnings (%d issues)", jsonlCount)
	}

	return DoctorCheck{
		Name:     "Migration Readiness",
		Status:   status,
		Message:  message,
		Detail:   strings.Join(result.Warnings, "\n"),
		Fix:      "Run 'bd migrate dolt' to start migration",
		Category: CategoryMaintenance,
	}, result
}

// CheckMigrationCompletion validates that a Dolt migration completed successfully.
// This is a post-migration check that ensures:
// 1. Dolt database exists and is healthy
// 2. All issues from JSONL are present in Dolt
// 3. No data was lost during migration
// 4. Dolt database has no locks or uncommitted changes
func CheckMigrationCompletion(path string) (DoctorCheck, MigrationValidationResult) {
	result := MigrationValidationResult{
		Phase:       "post-migration",
		Ready:       true,
		DoltHealthy: true,
		SchemaValid: true,
	}

	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))

	// Check if .beads exists
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		result.Ready = false
		result.DoltHealthy = false
		result.Errors = append(result.Errors, "No .beads directory found")
		return DoctorCheck{
			Name:     "Migration Completion",
			Status:   StatusError,
			Message:  "No beads installation found",
			Category: CategoryMaintenance,
		}, result
	}

	// Detect current backend
	result.Backend = GetBackend(beadsDir)

	// Not on Dolt - migration incomplete or not started
	if result.Backend != configfile.BackendDolt {
		result.Ready = false
		result.DoltHealthy = false
		result.Errors = append(result.Errors, fmt.Sprintf("Backend is %s, not Dolt", result.Backend))
		return DoctorCheck{
			Name:     "Migration Completion",
			Status:   StatusError,
			Message:  "Not using Dolt backend",
			Detail:   fmt.Sprintf("Current backend: %s", result.Backend),
			Fix:      "Run 'bd migrate dolt' to migrate to Dolt",
			Category: CategoryMaintenance,
		}, result
	}

	// Check Dolt database health
	ctx := context.Background()
	store, err := storagefactory.NewFromConfigWithOptions(ctx, beadsDir, storagefactory.Options{ReadOnly: true})
	if err != nil {
		result.Ready = false
		result.DoltHealthy = false
		result.Errors = append(result.Errors, fmt.Sprintf("Failed to open Dolt: %v", err))
		return DoctorCheck{
			Name:     "Migration Completion",
			Status:   StatusError,
			Message:  "Cannot open Dolt database",
			Detail:   err.Error(),
			Fix:      "Check Dolt database integrity or re-run migration",
			Category: CategoryMaintenance,
		}, result
	}
	defer func() { _ = store.Close() }()

	// Get Dolt issue count
	stats, err := store.GetStatistics(ctx)
	if err != nil {
		result.Ready = false
		result.SchemaValid = false
		result.Errors = append(result.Errors, fmt.Sprintf("Failed to query Dolt: %v", err))
		return DoctorCheck{
			Name:     "Migration Completion",
			Status:   StatusError,
			Message:  "Cannot query Dolt database",
			Detail:   err.Error(),
			Fix:      "Database schema may be incomplete",
			Category: CategoryMaintenance,
		}, result
	}
	result.DoltCount = stats.TotalIssues

	// Check for Dolt locks/uncommitted changes
	doltLocked, lockDetail := checkDoltLocks(beadsDir)
	result.DoltLocked = doltLocked
	if doltLocked {
		result.Warnings = append(result.Warnings, fmt.Sprintf("Dolt has uncommitted changes: %s", lockDetail))
	}

	// Find JSONL file for comparison
	jsonlPath := findJSONLFile(beadsDir)
	if jsonlPath != "" {
		jsonlCount, _, jsonlIDs, err := validateJSONLForMigration(jsonlPath)
		result.JSONLCount = jsonlCount
		if err == nil {
			// Compare Dolt with JSONL
			missingInDolt := compareDoltWithJSONL(ctx, store, jsonlIDs)
			result.MissingInDB = missingInDolt

			if len(missingInDolt) > 0 {
				result.Ready = false
				result.Errors = append(result.Errors,
					fmt.Sprintf("%d issues in JSONL missing from Dolt", len(missingInDolt)))
			}

			// Count comparison
			if result.DoltCount != jsonlCount {
				// Only error if Dolt has fewer issues than JSONL
				if result.DoltCount < jsonlCount {
					result.Ready = false
					result.Errors = append(result.Errors,
						fmt.Sprintf("Count mismatch: Dolt has %d, JSONL has %d", result.DoltCount, jsonlCount))
				} else {
					// Dolt has more - check if extra issues are cross-rig contamination or ephemeral
					foreignCount, foreignPrefixes, ephemeralCount := categorizeDoltExtras(ctx, store, jsonlIDs)
					result.ForeignPrefixCount = foreignCount
					result.ForeignPrefixes = foreignPrefixes

					if foreignCount > 0 {
						prefixList := formatPrefixCounts(foreignPrefixes)
						result.Warnings = append(result.Warnings,
							fmt.Sprintf("Dolt has %d issues from other rigs (cross-rig contamination): %s", foreignCount, prefixList))
					}
					if ephemeralCount > 0 {
						result.Warnings = append(result.Warnings,
							fmt.Sprintf("Dolt has %d ephemeral issues not in JSONL", ephemeralCount))
					}
				}
			}
		} else {
			result.Warnings = append(result.Warnings, fmt.Sprintf("Could not validate JSONL: %v", err))
		}
	} else {
		result.Warnings = append(result.Warnings, "No JSONL file found for comparison")
	}

	// Build status
	if len(result.Errors) > 0 {
		result.Ready = false
		return DoctorCheck{
			Name:     "Migration Completion",
			Status:   StatusError,
			Message:  fmt.Sprintf("Migration incomplete: %d error(s)", len(result.Errors)),
			Detail:   strings.Join(result.Errors, "\n"),
			Fix:      "Re-run 'bd migrate dolt' or check for data issues",
			Category: CategoryMaintenance,
		}, result
	}

	status := StatusOK
	message := fmt.Sprintf("Complete (%d issues in Dolt)", result.DoltCount)
	if len(result.Warnings) > 0 {
		status = StatusWarning
		message = fmt.Sprintf("Complete with warnings (%d issues)", result.DoltCount)
	}

	return DoctorCheck{
		Name:     "Migration Completion",
		Status:   status,
		Message:  message,
		Detail:   strings.Join(result.Warnings, "\n"),
		Category: CategoryMaintenance,
	}, result
}

// CheckDoltLocks checks if the Dolt database has any locks or uncommitted changes.
func CheckDoltLocks(path string) DoctorCheck {
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))

	// Only run for Dolt backend
	if !IsDoltBackend(beadsDir) {
		return DoctorCheck{
			Name:     "Dolt Locks",
			Status:   StatusOK,
			Message:  "N/A (not Dolt backend)",
			Category: CategoryMaintenance,
		}
	}

	locked, detail := checkDoltLocks(beadsDir)
	if locked {
		return DoctorCheck{
			Name:     "Dolt Locks",
			Status:   StatusWarning,
			Message:  "Uncommitted changes detected",
			Detail:   detail,
			Fix:      "Changes will be auto-committed on next bd command",
			Category: CategoryMaintenance,
		}
	}

	return DoctorCheck{
		Name:     "Dolt Locks",
		Status:   StatusOK,
		Message:  "No locks or uncommitted changes",
		Category: CategoryMaintenance,
	}
}

// Helper functions

// Note: findJSONLFile is defined in sync_divergence.go

// getSQLiteDBPath returns the path to the SQLite database.
func getSQLiteDBPath(beadsDir string) string {
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil && cfg.Database != "" {
		return cfg.DatabasePath(beadsDir)
	}
	return filepath.Join(beadsDir, beads.CanonicalDatabaseName)
}

// validateJSONLForMigration validates a JSONL file for migration readiness.
// Returns: count of valid issues, count of malformed lines, set of valid IDs, and error if blocking.
func validateJSONLForMigration(jsonlPath string) (int, int, map[string]bool, error) {
	file, err := os.Open(jsonlPath) //nolint:gosec
	if err != nil {
		return 0, 0, nil, fmt.Errorf("failed to open JSONL: %w", err)
	}
	defer file.Close()

	ids := make(map[string]bool)
	var malformed int
	var parseErrors []string

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1024), 2*1024*1024) // 2MB buffer for large lines
	lineNo := 0

	for scanner.Scan() {
		lineNo++
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var issue struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(line, &issue); err != nil {
			malformed++
			if len(parseErrors) < 5 {
				parseErrors = append(parseErrors, fmt.Sprintf("line %d: %v", lineNo, err))
			}
			continue
		}

		if issue.ID == "" {
			malformed++
			if len(parseErrors) < 5 {
				parseErrors = append(parseErrors, fmt.Sprintf("line %d: missing id field", lineNo))
			}
			continue
		}

		ids[issue.ID] = true
	}

	if err := scanner.Err(); err != nil {
		return len(ids), malformed, ids, fmt.Errorf("failed to read JSONL: %w", err)
	}

	// Return error only if ALL lines are malformed (blocking)
	if len(ids) == 0 && malformed > 0 {
		return 0, malformed, ids, fmt.Errorf("JSONL file is completely corrupt: %d malformed lines", malformed)
	}

	return len(ids), malformed, ids, nil
}

// compareSQLiteWithJSONL compares SQLite database with JSONL file.
// Returns: SQLite count, IDs in JSONL but not SQLite, IDs in SQLite but not JSONL, error.
func compareSQLiteWithJSONL(dbPath string, jsonlIDs map[string]bool) (int, []string, []string, error) {
	db, err := sql.Open("sqlite3", sqliteConnString(dbPath, true))
	if err != nil {
		return 0, nil, nil, fmt.Errorf("failed to open SQLite: %w", err)
	}
	defer db.Close()

	// Get all non-ephemeral IDs from SQLite
	rows, err := db.Query("SELECT id FROM issues WHERE ephemeral = 0 OR ephemeral IS NULL")
	if err != nil {
		return 0, nil, nil, fmt.Errorf("failed to query SQLite: %w", err)
	}
	defer rows.Close()

	sqliteIDs := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			continue
		}
		sqliteIDs[id] = true
	}

	// Find differences (sample first 100)
	var missingInDB []string
	var missingInJSONL []string

	for id := range jsonlIDs {
		if !sqliteIDs[id] {
			missingInDB = append(missingInDB, id)
			if len(missingInDB) >= 100 {
				break
			}
		}
	}

	for id := range sqliteIDs {
		if !jsonlIDs[id] {
			missingInJSONL = append(missingInJSONL, id)
			if len(missingInJSONL) >= 100 {
				break
			}
		}
	}

	return len(sqliteIDs), missingInDB, missingInJSONL, nil
}

// compareDoltWithJSONL compares Dolt database with JSONL IDs.
// Returns IDs in JSONL but not in Dolt (sample first 100).
func compareDoltWithJSONL(ctx context.Context, store storage.Storage, jsonlIDs map[string]bool) []string {
	var missing []string

	for id := range jsonlIDs {
		_, err := store.GetIssue(ctx, id)
		if err != nil {
			missing = append(missing, id)
			if len(missing) >= 100 {
				break
			}
		}
	}

	return missing
}

// checkDoltLocks checks for uncommitted changes in Dolt.
// Respects dolt_mode configuration: uses MySQL driver for server mode,
// embedded driver for embedded mode.
func checkDoltLocks(beadsDir string) (bool, string) {
	db, serverMode, err := openDoltDB(beadsDir)
	if err != nil {
		return false, ""
	}
	defer closeDoltDB(db, serverMode)

	ctx := context.Background()

	// Check dolt_status for uncommitted changes
	rows, err := db.QueryContext(ctx, "SELECT table_name, staged, status FROM dolt_status")
	if err != nil {
		return false, ""
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
		mark := ""
		if staged {
			mark = " (staged)"
		}
		changes = append(changes, fmt.Sprintf("%s: %s%s", tableName, status, mark))
	}

	if len(changes) > 0 {
		return true, strings.Join(changes, ", ")
	}

	return false, ""
}

// categorizeDoltExtras finds issues in Dolt that aren't in JSONL and categorizes them
// as either foreign-prefix (cross-rig contamination) or ephemeral (same-prefix).
// Returns: foreignCount, foreignPrefixes map, ephemeralCount.
func categorizeDoltExtras(ctx context.Context, store storage.Storage, jsonlIDs map[string]bool) (int, map[string]int, int) {
	// Get the configured prefix for this rig
	localPrefix, _ := store.GetConfig(ctx, "issue_prefix")

	// Query all issue IDs from Dolt
	db := store.UnderlyingDB()
	if db == nil {
		return 0, nil, 0
	}

	rows, err := db.QueryContext(ctx, "SELECT id FROM issues")
	if err != nil {
		return 0, nil, 0
	}
	defer rows.Close()

	foreignPrefixes := make(map[string]int)
	var ephemeralCount int

	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			continue
		}
		// Skip issues that are in JSONL (those are expected)
		if jsonlIDs[id] {
			continue
		}
		// This issue is in Dolt but not in JSONL - categorize it
		prefix := utils.ExtractIssuePrefix(id)
		if localPrefix != "" && prefix != "" && prefix != localPrefix {
			foreignPrefixes[prefix]++
		} else {
			ephemeralCount++
		}
	}

	var foreignCount int
	for _, count := range foreignPrefixes {
		foreignCount += count
	}

	return foreignCount, foreignPrefixes, ephemeralCount
}

// formatPrefixCounts formats a map of prefix -> count as "prefix1 (N), prefix2 (M)".
func formatPrefixCounts(prefixes map[string]int) string {
	var parts []string
	for prefix, count := range prefixes {
		parts = append(parts, fmt.Sprintf("%s (%d)", prefix, count))
	}
	return strings.Join(parts, ", ")
}
