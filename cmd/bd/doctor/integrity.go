package doctor

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/git"
	storagefactory "github.com/steveyegge/beads/internal/storage/factory"
)

// CheckIDFormat checks whether issues use hash-based or sequential IDs
func CheckIDFormat(path string) DoctorCheck {
	backend, beadsDir := getBackendAndBeadsDir(path)

	// Determine the on-disk location (file for SQLite, directory for Dolt).
	dbPath := filepath.Join(beadsDir, beads.CanonicalDatabaseName)
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil {
		dbPath = cfg.DatabasePath(beadsDir)
	}

	// Check if using JSONL-only mode (or uninitialized DB).
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		// Check if JSONL exists (--no-db mode)
		jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
		if _, err := os.Stat(jsonlPath); err == nil {
			return DoctorCheck{
				Name:    "Issue IDs",
				Status:  StatusOK,
				Message: "N/A (JSONL-only mode)",
			}
		}
		// No database and no JSONL
		return DoctorCheck{
			Name:    "Issue IDs",
			Status:  StatusOK,
			Message: "No issues yet (will use hash-based IDs)",
		}
	}

	// Open the configured backend in read-only mode.
	// This must work for both SQLite and Dolt.
	ctx := context.Background()
	store, err := storagefactory.NewFromConfigWithOptions(ctx, beadsDir, storagefactory.Options{ReadOnly: true})
	if err != nil {
		return DoctorCheck{
			Name:    "Issue IDs",
			Status:  StatusError,
			Message: "Unable to open database",
			Detail:  err.Error(),
		}
	}
	defer func() { _ = store.Close() }() // Intentionally ignore close error
	db := store.UnderlyingDB()

	// Get sample of issues to check ID format (up to 10 for pattern analysis)
	rows, err := db.QueryContext(ctx, "SELECT id FROM issues ORDER BY created_at LIMIT 10")
	if err != nil {
		return DoctorCheck{
			Name:    "Issue IDs",
			Status:  StatusError,
			Message: "Unable to query issues",
			Detail:  err.Error(),
		}
	}
	defer rows.Close()

	var issueIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			issueIDs = append(issueIDs, id)
		}
	}

	if len(issueIDs) == 0 {
		return DoctorCheck{
			Name:    "Issue IDs",
			Status:  StatusOK,
			Message: "No issues yet (will use hash-based IDs)",
		}
	}

	// Detect ID format using robust heuristic
	if DetectHashBasedIDs(db, issueIDs) {
		return DoctorCheck{
			Name:    "Issue IDs",
			Status:  StatusOK,
			Message: "hash-based ✓",
		}
	}

	// Sequential IDs - recommend migration
	if backend == configfile.BackendDolt {
		return DoctorCheck{
			Name:    "Issue IDs",
			Status:  StatusOK,
			Message: "hash-based ✓",
		}
	}
	return DoctorCheck{
		Name:    "Issue IDs",
		Status:  StatusWarning,
		Message: "sequential (e.g., bd-1, bd-2, ...)",
		Fix:     "Run 'bd migrate hash-ids' to upgrade (prevents ID collisions in multi-worker scenarios)",
	}
}

// CheckDependencyCycles checks for circular dependencies in the issue graph
func CheckDependencyCycles(path string) DoctorCheck {
	_, beadsDir := getBackendAndBeadsDir(path)

	// Determine database path
	dbPath := filepath.Join(beadsDir, beads.CanonicalDatabaseName)
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil {
		dbPath = cfg.DatabasePath(beadsDir)
	}

	// If no database, skip this check
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return DoctorCheck{
			Name:    "Dependency Cycles",
			Status:  StatusOK,
			Message: "N/A (no database)",
		}
	}

	// Open the configured backend in read-only mode (works for both SQLite and Dolt)
	ctx := context.Background()
	store, err := storagefactory.NewFromConfigWithOptions(ctx, beadsDir, storagefactory.Options{ReadOnly: true})
	if err != nil {
		return DoctorCheck{
			Name:    "Dependency Cycles",
			Status:  StatusWarning,
			Message: "Unable to open database",
			Detail:  err.Error(),
		}
	}
	defer func() { _ = store.Close() }()
	db := store.UnderlyingDB()

	// Query for cycles using simplified SQL (CONCAT for Dolt/MySQL compatibility)
	query := `
		WITH RECURSIVE paths AS (
			SELECT
				issue_id,
				depends_on_id,
				issue_id as start_id,
				CONCAT(issue_id, '→', depends_on_id) as path,
				0 as depth
			FROM dependencies

			UNION ALL

			SELECT
				d.issue_id,
				d.depends_on_id,
				p.start_id,
				CONCAT(p.path, '→', d.depends_on_id),
				p.depth + 1
			FROM dependencies d
			JOIN paths p ON d.issue_id = p.depends_on_id
			WHERE p.depth < 100
			  AND p.path NOT LIKE CONCAT('%', d.depends_on_id, '→%')
		)
		SELECT DISTINCT start_id
		FROM paths
		WHERE depends_on_id = start_id`

	rows, err := db.Query(query)
	if err != nil {
		return DoctorCheck{
			Name:    "Dependency Cycles",
			Status:  StatusWarning,
			Message: "Unable to check for cycles",
			Detail:  err.Error(),
		}
	}
	defer rows.Close()

	cycleCount := 0
	var firstCycle string
	for rows.Next() {
		var startID string
		if err := rows.Scan(&startID); err != nil {
			continue
		}
		cycleCount++
		if cycleCount == 1 {
			firstCycle = startID
		}
	}

	if cycleCount == 0 {
		return DoctorCheck{
			Name:    "Dependency Cycles",
			Status:  StatusOK,
			Message: "No circular dependencies detected",
		}
	}

	return DoctorCheck{
		Name:    "Dependency Cycles",
		Status:  StatusError,
		Message: fmt.Sprintf("Found %d circular dependency cycle(s)", cycleCount),
		Detail:  fmt.Sprintf("First cycle involves: %s", firstCycle),
		Fix:     "Run 'bd dep cycles' to see full cycle paths, then 'bd dep remove' to break cycles",
	}
}

// CheckDeletionsManifest checks the status of the legacy deletions.jsonl file
func CheckDeletionsManifest(path string) DoctorCheck {
	// Follow redirect to resolve actual beads directory (bd-tvus fix)
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))

	// Skip if .beads doesn't exist
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		return DoctorCheck{
			Name:    "Deletions Manifest",
			Status:  StatusOK,
			Message: "N/A (no .beads directory)",
		}
	}

	// Check if we're in a git repository using worktree-aware detection
	_, err := git.GetGitDir()
	if err != nil {
		return DoctorCheck{
			Name:    "Deletions Manifest",
			Status:  StatusOK,
			Message: "N/A (not a git repository)",
		}
	}

	deletionsPath := filepath.Join(beadsDir, "deletions.jsonl")

	// Check if deletions.jsonl exists
	info, err := os.Stat(deletionsPath)
	if err == nil {
		// File exists - count entries (empty file is valid, means no deletions)
		if info.Size() == 0 {
			return DoctorCheck{
				Name:    "Deletions Manifest",
				Status:  StatusOK,
				Message: "Empty (no legacy deletions)",
			}
		}
		file, err := os.Open(deletionsPath) // #nosec G304 - controlled path
		if err == nil {
			defer file.Close()
			count := 0
			scanner := bufio.NewScanner(file)
			for scanner.Scan() {
				if len(scanner.Bytes()) > 0 {
					count++
				}
			}
			if count > 0 {
				return DoctorCheck{
					Name:    "Deletions Manifest",
					Status:  StatusWarning,
					Message: fmt.Sprintf("Legacy format (%d entries)", count),
					Detail:  "deletions.jsonl is a legacy format no longer used",
					Fix:     "Safe to delete deletions.jsonl (Dolt handles delete propagation natively)",
				}
			}
			return DoctorCheck{
				Name:    "Deletions Manifest",
				Status:  StatusOK,
				Message: "Empty (no legacy deletions)",
			}
		}
	}

	// deletions.jsonl doesn't exist - this is the expected state
	// Check for .migrated file to confirm migration happened
	migratedPath := filepath.Join(beadsDir, "deletions.jsonl.migrated")
	if _, err := os.Stat(migratedPath); err == nil {
		return DoctorCheck{
			Name:    "Deletions Manifest",
			Status:  StatusOK,
			Message: "Migrated (legacy file removed)",
		}
	}

	// No deletions.jsonl and no .migrated file - check if JSONL exists
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if _, err := os.Stat(jsonlPath); os.IsNotExist(err) {
		jsonlPath = filepath.Join(beadsDir, "beads.jsonl")
		if _, err := os.Stat(jsonlPath); os.IsNotExist(err) {
			return DoctorCheck{
				Name:    "Deletions Manifest",
				Status:  StatusOK,
				Message: "N/A (no JSONL file)",
			}
		}
	}

	// JSONL exists but no deletions tracking - expected for Dolt-native repos
	return DoctorCheck{
		Name:    "Deletions Manifest",
		Status:  StatusOK,
		Message: "Not needed (Dolt-native)",
	}
}

// CheckRepoFingerprint validates that the database belongs to this repository.
// This detects when a .beads directory was copied from another repo or when
// the git remote URL changed. A mismatch can cause data loss during sync.
func CheckRepoFingerprint(path string) DoctorCheck {
	backend, beadsDir := getBackendAndBeadsDir(path)

	// Backend-aware existence check
	switch backend {
	case configfile.BackendDolt:
		if info, err := os.Stat(filepath.Join(beadsDir, "dolt")); err != nil || !info.IsDir() {
			return DoctorCheck{
				Name:    "Repo Fingerprint",
				Status:  StatusOK,
				Message: "N/A (no database)",
			}
		}
	default:
		// SQLite backend: needs a .db file
		var dbPath string
		if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil && cfg.Database != "" {
			dbPath = cfg.DatabasePath(beadsDir)
		} else {
			dbPath = filepath.Join(beadsDir, beads.CanonicalDatabaseName)
		}
		if _, err := os.Stat(dbPath); os.IsNotExist(err) {
			return DoctorCheck{
				Name:    "Repo Fingerprint",
				Status:  StatusOK,
				Message: "N/A (no database)",
			}
		}
	}

	// For Dolt, read fingerprint from storage metadata (no sqlite assumptions).
	if backend == configfile.BackendDolt {
		ctx := context.Background()
		store, err := storagefactory.NewFromConfigWithOptions(ctx, beadsDir, storagefactory.Options{ReadOnly: true})
		if err != nil {
			return DoctorCheck{
				Name:    "Repo Fingerprint",
				Status:  StatusWarning,
				Message: "Unable to open database",
				Detail:  err.Error(),
			}
		}
		defer func() { _ = store.Close() }()

		storedRepoID, err := store.GetMetadata(ctx, "repo_id")
		if err != nil {
			return DoctorCheck{
				Name:    "Repo Fingerprint",
				Status:  StatusWarning,
				Message: "Unable to read repo fingerprint",
				Detail:  err.Error(),
			}
		}

		// If missing, warn (not the legacy sqlite messaging).
		if storedRepoID == "" {
			return DoctorCheck{
				Name:    "Repo Fingerprint",
				Status:  StatusWarning,
				Message: "Missing repo fingerprint metadata",
				Detail:  "Storage: Dolt",
				Fix:     "Run 'bd doctor --fix' to repair metadata",
			}
		}

		currentRepoID, err := beads.ComputeRepoID()
		if err != nil {
			return DoctorCheck{
				Name:    "Repo Fingerprint",
				Status:  StatusWarning,
				Message: "Unable to compute current repo ID",
				Detail:  err.Error(),
			}
		}

		if storedRepoID != currentRepoID {
			return DoctorCheck{
				Name:    "Repo Fingerprint",
				Status:  StatusError,
				Message: "Database belongs to different repository",
				Detail:  fmt.Sprintf("stored: %s, current: %s", storedRepoID[:8], currentRepoID[:8]),
				Fix:     "Run 'bd migrate --update-repo-id' if URL changed, or 'rm -rf .beads && bd init --backend dolt' if wrong database",
			}
		}

		return DoctorCheck{
			Name:    "Repo Fingerprint",
			Status:  StatusOK,
			Message: fmt.Sprintf("Verified (%s)", currentRepoID[:8]),
		}
	}

	// SQLite path (existing behavior)
	// Get database path
	var dbPath string
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil && cfg.Database != "" {
		dbPath = cfg.DatabasePath(beadsDir)
	} else {
		dbPath = filepath.Join(beadsDir, beads.CanonicalDatabaseName)
	}

	// Skip if database doesn't exist
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return DoctorCheck{
			Name:    "Repo Fingerprint",
			Status:  StatusOK,
			Message: "N/A (no database)",
		}
	}

	// Open database
	db, err := sql.Open("sqlite3", sqliteConnString(dbPath, true))
	if err != nil {
		return DoctorCheck{
			Name:    "Repo Fingerprint",
			Status:  StatusWarning,
			Message: "Unable to open database",
			Detail:  err.Error(),
		}
	}
	defer db.Close()

	// Get stored repo ID
	var storedRepoID string
	err = db.QueryRow("SELECT value FROM metadata WHERE key = 'repo_id'").Scan(&storedRepoID)
	if err != nil {
		if err == sql.ErrNoRows || strings.Contains(err.Error(), "no such table") {
			// Legacy database without repo_id - this is an error because daemon won't start
			return DoctorCheck{
				Name:    "Repo Fingerprint",
				Status:  StatusError,
				Message: "Legacy database (no fingerprint)",
				Detail:  "Database was created before version 0.17.5. Daemon will fail to start.",
				Fix:     "Run 'bd migrate --update-repo-id' to add fingerprint",
			}
		}
		return DoctorCheck{
			Name:    "Repo Fingerprint",
			Status:  StatusWarning,
			Message: "Unable to read repo fingerprint",
			Detail:  err.Error(),
		}
	}

	// If repo_id is empty, treat as legacy - this is an error because daemon won't start
	if storedRepoID == "" {
		return DoctorCheck{
			Name:    "Repo Fingerprint",
			Status:  StatusError,
			Message: "Legacy database (empty fingerprint)",
			Detail:  "Database was created before version 0.17.5. Daemon will fail to start.",
			Fix:     "Run 'bd migrate --update-repo-id' to add fingerprint",
		}
	}

	// Compute current repo ID
	currentRepoID, err := beads.ComputeRepoID()
	if err != nil {
		return DoctorCheck{
			Name:    "Repo Fingerprint",
			Status:  StatusWarning,
			Message: "Unable to compute current repo ID",
			Detail:  err.Error(),
		}
	}

	// Compare
	if storedRepoID != currentRepoID {
		return DoctorCheck{
			Name:    "Repo Fingerprint",
			Status:  StatusError,
			Message: "Database belongs to different repository",
			Detail:  fmt.Sprintf("stored: %s, current: %s", storedRepoID[:8], currentRepoID[:8]),
			Fix:     "Run 'bd migrate --update-repo-id' if URL changed, or 'rm -rf .beads && bd init' if wrong database",
		}
	}

	return DoctorCheck{
		Name:    "Repo Fingerprint",
		Status:  StatusOK,
		Message: fmt.Sprintf("Verified (%s)", currentRepoID[:8]),
	}
}

// Helper functions

// DetectHashBasedIDs uses multiple heuristics to determine if the database uses hash-based IDs.
// This is more robust than checking a single ID's format, since base36 hash IDs can be all-numeric.
func DetectHashBasedIDs(db *sql.DB, sampleIDs []string) bool {
	// Heuristic 1: Check for child_counters table (added for hash ID support)
	// Use a direct query instead of sqlite_master so this works with both SQLite and Dolt.
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM child_counters").Scan(&count)
	if err == nil {
		// child_counters table exists - this is a strong indicator of hash IDs
		return true
	}

	// Heuristic 2: Check if any sample ID clearly contains letters (a-z)
	// Hash IDs use base36 (0-9, a-z), sequential IDs are purely numeric
	for _, id := range sampleIDs {
		if isHashID(id) {
			return true
		}
	}

	// Heuristic 3: Look for patterns that indicate hash IDs
	if len(sampleIDs) >= 2 {
		// Extract suffixes (part after prefix-) for analysis
		var suffixes []string
		for _, id := range sampleIDs {
			parts := strings.SplitN(id, "-", 2)
			if len(parts) == 2 {
				// Strip hierarchical suffix like .1 or .1.2
				baseSuffix := strings.Split(parts[1], ".")[0]
				suffixes = append(suffixes, baseSuffix)
			}
		}

		if len(suffixes) >= 2 {
			// Check for variable lengths (strong indicator of adaptive hash IDs)
			// BUT: sequential IDs can also have variable length (1, 10, 100)
			// So we need to check if the length variation is natural (1→2→3 digits)
			// or random (3→8→4 chars typical of adaptive hash IDs)
			lengths := make(map[int]int) // length -> count
			for _, s := range suffixes {
				lengths[len(s)]++
			}

			// If we have 3+ different lengths, likely hash IDs (adaptive length)
			// Sequential IDs typically have 1-2 lengths (e.g., 1-9, 10-99, 100-999)
			if len(lengths) >= 3 {
				return true
			}

			// Check for leading zeros (rare in sequential IDs, common in hash IDs)
			// Sequential IDs: bd-1, bd-2, bd-10, bd-100
			// Hash IDs: bd-0088, bd-02a4, bd-05a1
			hasLeadingZero := false
			for _, s := range suffixes {
				if len(s) > 1 && s[0] == '0' {
					hasLeadingZero = true
					break
				}
			}
			if hasLeadingZero {
				return true
			}

			// Check for non-sequential ordering
			// Try to parse as integers - if they're not sequential, likely hash IDs
			allNumeric := true
			var nums []int
			for _, s := range suffixes {
				var num int
				if _, err := fmt.Sscanf(s, "%d", &num); err == nil {
					nums = append(nums, num)
				} else {
					allNumeric = false
					break
				}
			}

			if allNumeric && len(nums) >= 2 {
				// Check if they form a roughly sequential pattern (1,2,3 or 10,11,12)
				// Hash IDs would be more random (e.g., 88, 13452, 676)
				isSequentialPattern := true
				for i := 1; i < len(nums); i++ {
					diff := nums[i] - nums[i-1]
					// Allow for some gaps (deleted issues), but should be mostly sequential
					if diff < 0 || diff > 100 {
						isSequentialPattern = false
						break
					}
				}
				// If the numbers are NOT sequential, they're likely hash IDs
				if !isSequentialPattern {
					return true
				}
			}
		}
	}

	// If we can't determine for sure, default to assuming sequential IDs
	// This is conservative - better to recommend migration than miss sequential IDs
	return false
}

// isHashID checks if a single ID contains hash characteristics
// Hash IDs contain hex letters (a-f), sequential IDs are only digits
// May have hierarchical suffix like .1 or .1.2
func isHashID(id string) bool {
	lastSeperatorIndex := strings.LastIndex(id, "-")
	if lastSeperatorIndex == -1 {
		return false
	}

	suffix := id[lastSeperatorIndex+1:]
	// Strip hierarchical suffix like .1 or .1.2
	baseSuffix := strings.Split(suffix, ".")[0]

	if len(baseSuffix) == 0 {
		return false
	}

	// Must be valid Base36 (0-9, a-z)
	if !regexp.MustCompile(`^[0-9a-z]+$`).MatchString(baseSuffix) {
		return false
	}

	// If it's 5+ characters long, it's almost certainly a hash ID
	// (sequential IDs rarely exceed 9999 = 4 digits)
	if len(baseSuffix) >= 5 {
		return true
	}

	// For shorter IDs, check if it contains any letter (a-z)
	// Sequential IDs are purely numeric
	return regexp.MustCompile(`[a-z]`).MatchString(baseSuffix)
}
