// Package sqlite - migration safety invariants
package sqlite

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// Snapshot captures database state before migrations for validation
type Snapshot struct {
	IssueCount      int
	ConfigKeys      []string
	DependencyCount int
	LabelCount      int
}

// MigrationInvariant represents a database invariant that must hold after migrations
type MigrationInvariant struct {
	Name        string
	Description string
	Check       func(*sql.DB, *Snapshot) error
}

// invariants is the list of all invariants checked after migrations
var invariants = []MigrationInvariant{
	{
		Name:        "required_config_present",
		Description: "Required config keys must exist",
		Check:       checkRequiredConfig,
	},
	{
		Name:        "foreign_keys_valid",
		Description: "No orphaned dependencies or labels",
		Check:       checkForeignKeys,
	},
	{
		Name:        "issue_count_stable",
		Description: "Issue count should not decrease unexpectedly",
		Check:       checkIssueCount,
	},
}

// captureSnapshot takes a snapshot of the database state before migrations
func captureSnapshot(db *sql.DB) (*Snapshot, error) {
	snapshot := &Snapshot{}

	// Count issues
	err := db.QueryRow("SELECT COUNT(*) FROM issues").Scan(&snapshot.IssueCount)
	if err != nil {
		return nil, fmt.Errorf("failed to count issues: %w", err)
	}

	// Get config keys
	rows, err := db.Query("SELECT key FROM config ORDER BY key")
	if err != nil {
		return nil, fmt.Errorf("failed to query config keys: %w", err)
	}
	defer rows.Close()

	snapshot.ConfigKeys = []string{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, fmt.Errorf("failed to scan config key: %w", err)
		}
		snapshot.ConfigKeys = append(snapshot.ConfigKeys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error reading config keys: %w", err)
	}

	// Count dependencies
	err = db.QueryRow("SELECT COUNT(*) FROM dependencies").Scan(&snapshot.DependencyCount)
	if err != nil {
		return nil, fmt.Errorf("failed to count dependencies: %w", err)
	}

	// Count labels
	err = db.QueryRow("SELECT COUNT(*) FROM labels").Scan(&snapshot.LabelCount)
	if err != nil {
		return nil, fmt.Errorf("failed to count labels: %w", err)
	}

	return snapshot, nil
}

// verifyInvariants checks all migration invariants and returns error if any fail
func verifyInvariants(db *sql.DB, snapshot *Snapshot) error {
	var failures []string

	for _, invariant := range invariants {
		if err := invariant.Check(db, snapshot); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", invariant.Name, err))
		}
	}

	if len(failures) > 0 {
		return fmt.Errorf("migration invariants failed:\n  - %s", strings.Join(failures, "\n  - "))
	}

	return nil
}

// checkRequiredConfig ensures required config keys exist (would have caught GH #201)
// Only enforces issue_prefix requirement if there are issues in the database
func checkRequiredConfig(db *sql.DB, snapshot *Snapshot) error {
	// Check current issue count (not snapshot, since migrations may add/remove issues)
	var currentCount int
	err := db.QueryRow("SELECT COUNT(*) FROM issues").Scan(&currentCount)
	if err != nil {
		return fmt.Errorf("failed to count issues: %w", err)
	}

	// Only require issue_prefix if there are issues in the database
	// New databases can exist without issue_prefix until first issue is created
	if currentCount == 0 {
		return nil
	}

	// Check for required config keys
	var value string
	err = db.QueryRow("SELECT value FROM config WHERE key = 'issue_prefix'").Scan(&value)
	if err == sql.ErrNoRows || value == "" {
		return fmt.Errorf("required config key missing: issue_prefix (database has %d issues)", currentCount)
	} else if err != nil {
		return fmt.Errorf("failed to check config key issue_prefix: %w", err)
	}

	return nil
}

// checkForeignKeys ensures no orphaned dependencies or labels exist
func checkForeignKeys(db *sql.DB, snapshot *Snapshot) error {
	// Check for orphaned dependencies (issue_id not in issues)
	var orphanedDepsIssue int
	err := db.QueryRow(`
		SELECT COUNT(*)
		FROM dependencies d
		WHERE NOT EXISTS (SELECT 1 FROM issues WHERE id = d.issue_id)
	`).Scan(&orphanedDepsIssue)
	if err != nil {
		return fmt.Errorf("failed to check orphaned dependencies (issue_id): %w", err)
	}
	if orphanedDepsIssue > 0 {
		return fmt.Errorf("found %d orphaned dependencies (issue_id not in issues)", orphanedDepsIssue)
	}

	// Check for orphaned dependencies (depends_on_id not in issues)
	// Exclude external dependencies (external:<project>:<capability>) which reference
	// issues in other projects and are expected to not exist locally
	var orphanedDepsDependsOn int
	err = db.QueryRow(`
		SELECT COUNT(*)
		FROM dependencies d
		WHERE NOT EXISTS (SELECT 1 FROM issues WHERE id = d.depends_on_id)
		  AND d.depends_on_id NOT LIKE 'external:%'
	`).Scan(&orphanedDepsDependsOn)
	if err != nil {
		return fmt.Errorf("failed to check orphaned dependencies (depends_on_id): %w", err)
	}
	if orphanedDepsDependsOn > 0 {
		return fmt.Errorf("found %d orphaned dependencies (depends_on_id not in issues)", orphanedDepsDependsOn)
	}

	// Check for orphaned labels (issue_id not in issues)
	var orphanedLabels int
	err = db.QueryRow(`
		SELECT COUNT(*)
		FROM labels l
		WHERE NOT EXISTS (SELECT 1 FROM issues WHERE id = l.issue_id)
	`).Scan(&orphanedLabels)
	if err != nil {
		return fmt.Errorf("failed to check orphaned labels: %w", err)
	}
	if orphanedLabels > 0 {
		return fmt.Errorf("found %d orphaned labels (issue_id not in issues)", orphanedLabels)
	}

	return nil
}

// checkIssueCount ensures issue count doesn't decrease unexpectedly
func checkIssueCount(db *sql.DB, snapshot *Snapshot) error {
	var currentCount int
	err := db.QueryRow("SELECT COUNT(*) FROM issues").Scan(&currentCount)
	if err != nil {
		return fmt.Errorf("failed to count issues: %w", err)
	}

	if currentCount < snapshot.IssueCount {
		return fmt.Errorf("issue count decreased from %d to %d (potential data loss)", snapshot.IssueCount, currentCount)
	}

	return nil
}

// GetInvariantNames returns the names of all registered invariants (for testing/inspection)
func GetInvariantNames() []string {
	names := make([]string, len(invariants))
	for i, inv := range invariants {
		names[i] = inv.Name
	}
	sort.Strings(names)
	return names
}

// CleanOrphanedRefs removes orphaned dependencies and labels that reference non-existent issues.
// This runs BEFORE migrations to prevent the chicken-and-egg problem where:
// 1. bd doctor --fix tries to open the database
// 2. Opening triggers migrations with invariant checks
// 3. Invariant check fails due to orphaned refs from prior tombstone deletion
// 4. Fix never runs because database won't open
//
// Returns counts of cleaned items for logging.
func CleanOrphanedRefs(db *sql.DB) (deps int, labels int, err error) {
	// Clean orphaned dependencies (issue_id not in issues)
	result, err := db.Exec(`
		DELETE FROM dependencies
		WHERE NOT EXISTS (SELECT 1 FROM issues WHERE id = issue_id)
	`)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to clean orphaned dependencies (issue_id): %w", err)
	}
	depsIssue, _ := result.RowsAffected()

	// Clean orphaned dependencies (depends_on_id not in issues, excluding external refs)
	result, err = db.Exec(`
		DELETE FROM dependencies
		WHERE NOT EXISTS (SELECT 1 FROM issues WHERE id = depends_on_id)
		  AND depends_on_id NOT LIKE 'external:%'
	`)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to clean orphaned dependencies (depends_on_id): %w", err)
	}
	depsDependsOn, _ := result.RowsAffected()

	// Clean orphaned labels (issue_id not in issues)
	result, err = db.Exec(`
		DELETE FROM labels
		WHERE NOT EXISTS (SELECT 1 FROM issues WHERE id = issue_id)
	`)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to clean orphaned labels: %w", err)
	}
	labelsCount, _ := result.RowsAffected()

	return int(depsIssue + depsDependsOn), int(labelsCount), nil
}
