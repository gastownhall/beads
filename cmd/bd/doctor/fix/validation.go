package fix

import (
	"database/sql"
	"fmt"
	"path/filepath"

	_ "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/configfile"
)

// getDatabasePath returns the actual database directory path, respecting dolt_data_dir.
// When dolt_data_dir is configured (e.g. ext4 redirect for WSL), the database lives
// outside .beads/dolt/ — this function resolves the correct location.
func getDatabasePath(beadsDir string) string {
	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		return filepath.Join(beadsDir, "dolt") // fallback to default
	}
	return cfg.DatabasePath(beadsDir)
}

// OrphanedDependencies removes dependencies pointing to non-existent issues.
// If verbose is true, prints each removed dependency; otherwise shows only summary.
func OrphanedDependencies(path string, verbose bool) error {
	beadsDir, err := resolvedWorkspaceBeadsDir(path)
	if err != nil {
		return err
	}

	db, err := openDoltDB(beadsDir)
	if err != nil {
		fmt.Printf("  Orphaned dependencies fix skipped (%v)\n", err)
		return nil
	}
	defer db.Close()

	// Find orphaned dependencies (exclude external: cross-rig tracking refs, #1593)
	//nolint:gosec // G202: fixDependencyUnionSQL returns a fixed internal SELECT fragment.
	query := `
		SELECT d.dep_table, d.issue_id, d.depends_on_id
		FROM (` + fixDependencyUnionSQL() + `) d
		WHERE NOT EXISTS (SELECT 1 FROM issues i WHERE i.id = d.depends_on_id)
		  AND NOT EXISTS (SELECT 1 FROM wisps w WHERE w.id = d.depends_on_id)
		  AND d.depends_on_id NOT LIKE 'external:%'
	`
	rows, err := db.Query(query)
	if err != nil {
		return fmt.Errorf("failed to query orphaned dependencies: %w", err)
	}
	defer rows.Close()

	type orphan struct {
		depTable    string
		issueID     string
		dependsOnID string
	}
	var orphans []orphan

	for rows.Next() {
		var o orphan
		if err := rows.Scan(&o.depTable, &o.issueID, &o.dependsOnID); err == nil {
			orphans = append(orphans, o)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("row iteration error: %w", err)
	}

	if len(orphans) == 0 {
		fmt.Println("  No orphaned dependencies to fix")
		return nil
	}

	// Delete orphaned dependencies
	// Uses explicit transaction so writes persist when @@autocommit is OFF
	// (e.g. Dolt server started with --no-auto-commit).
	showIndividual := verbose || len(orphans) < 20
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	var removed int
	for _, o := range orphans {
		var err error
		switch o.depTable {
		case "dependencies":
			_, err = tx.Exec("DELETE FROM dependencies WHERE issue_id = ? AND "+fixDependencyTargetExpr+" = ?", o.issueID, o.dependsOnID)
		case "wisp_dependencies":
			_, err = tx.Exec("DELETE FROM wisp_dependencies WHERE issue_id = ? AND "+fixDependencyTargetExpr+" = ?", o.issueID, o.dependsOnID)
		default:
			fmt.Printf("  Warning: skipped orphaned dependency from unexpected table %s\n", o.depTable)
			continue
		}
		if err != nil {
			fmt.Printf("  Warning: failed to remove %s→%s: %v\n", o.issueID, o.dependsOnID, err)
		} else {
			removed++
			if showIndividual {
				fmt.Printf("  Removed orphaned dependency: %s→%s\n", o.issueID, o.dependsOnID)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit orphaned dependency removals: %w", err)
	}

	// Commit changes in Dolt
	_, _ = db.Exec("CALL DOLT_COMMIT('-Am', 'doctor: remove orphaned dependencies')") // Best effort: commit advisory; schema fix already applied in-memory

	fmt.Printf("  Fixed %d orphaned dependency reference(s)\n", removed)
	return nil
}

// ChildParentDependencies removes child→parent blocking dependencies.
// These often indicate a modeling mistake (deadlock: child waits for parent, parent waits for children).
// Requires explicit opt-in via --fix-child-parent flag since some workflows may use these intentionally.
// If verbose is true, prints each removed dependency; otherwise shows only summary.
func ChildParentDependencies(path string, verbose bool) error {
	beadsDir, err := resolvedWorkspaceBeadsDir(path)
	if err != nil {
		return err
	}

	db, err := openDoltDB(beadsDir)
	if err != nil {
		fmt.Printf("  Child-parent dependencies fix skipped (%v)\n", err)
		return nil
	}
	defer db.Close()

	// Find child→parent BLOCKING dependencies where issue_id starts with depends_on_id + "."
	// Only matches blocking types (blocks, conditional-blocks, waits-for) that cause deadlock.
	// Excludes 'parent-child' type which is a legitimate structural hierarchy relationship.
	//nolint:gosec // G202: fixDependencyUnionSQL returns a fixed internal SELECT fragment.
	query := `
		SELECT d.dep_table, d.issue_id, d.depends_on_id, d.type
		FROM (` + fixDependencyUnionSQL() + `) d
		WHERE d.issue_id LIKE CONCAT(d.depends_on_id, '.%')
		  AND d.type IN ('blocks', 'conditional-blocks', 'waits-for')
	`
	rows, err := db.Query(query)
	if err != nil {
		return fmt.Errorf("failed to query child-parent dependencies: %w", err)
	}
	defer rows.Close()

	type badDep struct {
		depTable    string
		issueID     string
		dependsOnID string
		depType     string
	}
	var badDeps []badDep

	for rows.Next() {
		var d badDep
		if err := rows.Scan(&d.depTable, &d.issueID, &d.dependsOnID, &d.depType); err == nil {
			badDeps = append(badDeps, d)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("row iteration error: %w", err)
	}

	if len(badDeps) == 0 {
		fmt.Println("  No child→parent dependencies to fix")
		return nil
	}

	// Delete child→parent blocking dependencies (preserving parent-child type)
	// Uses explicit transaction so writes persist when @@autocommit is OFF
	// (e.g. Dolt server started with --no-auto-commit).
	showIndividual := verbose || len(badDeps) < 20
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	var removed int
	for _, d := range badDeps {
		var err error
		switch d.depTable {
		case "dependencies":
			_, err = tx.Exec("DELETE FROM dependencies WHERE issue_id = ? AND "+fixDependencyTargetExpr+" = ? AND type = ?", d.issueID, d.dependsOnID, d.depType)
		case "wisp_dependencies":
			_, err = tx.Exec("DELETE FROM wisp_dependencies WHERE issue_id = ? AND "+fixDependencyTargetExpr+" = ? AND type = ?", d.issueID, d.dependsOnID, d.depType)
		default:
			fmt.Printf("  Warning: skipped child→parent dependency from unexpected table %s\n", d.depTable)
			continue
		}
		if err != nil {
			fmt.Printf("  Warning: failed to remove %s→%s: %v\n", d.issueID, d.dependsOnID, err)
		} else {
			removed++
			if showIndividual {
				fmt.Printf("  Removed child→parent dependency: %s→%s\n", d.issueID, d.dependsOnID)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit dependency removals: %w", err)
	}

	// Commit changes in Dolt
	_, _ = db.Exec("CALL DOLT_COMMIT('-Am', 'doctor: remove child-parent dependency anti-patterns')") // Best effort: commit advisory; schema fix already applied in-memory

	fmt.Printf("  Fixed %d child→parent dependency anti-pattern(s)\n", removed)
	return nil
}

// CrossTableDuplicates removes issues-table rows whose IDs also exist in the
// wisps table. The wisps copy is canonical (be-iabdi); stale issues rows are
// deleted along with their child rows (labels, events, dependencies, comments).
func CrossTableDuplicates(path string, verbose bool) error {
	beadsDir, err := resolvedWorkspaceBeadsDir(path)
	if err != nil {
		return err
	}

	db, err := openDoltDB(beadsDir)
	if err != nil {
		fmt.Printf("  Cross-table duplicates fix skipped (%v)\n", err)
		return nil
	}
	defer db.Close()

	// Find IDs present in both tables — the wisp copy is canonical.
	rows, err := db.Query(`SELECT id FROM issues WHERE id IN (SELECT id FROM wisps)`)
	if err != nil {
		return fmt.Errorf("failed to query cross-table duplicates: %w", err)
	}
	var dupIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			dupIDs = append(dupIDs, id)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("row iteration error: %w", err)
	}
	_ = rows.Close()

	if len(dupIDs) == 0 {
		fmt.Println("  No cross-table duplicates to fix")
		return nil
	}

	showIndividual := verbose || len(dupIDs) < 20
	tx, txErr := db.Begin()
	if txErr != nil {
		return fmt.Errorf("failed to begin transaction: %w", txErr)
	}

	var removed int
	for _, id := range dupIDs {
		// Delete child rows first (FK-safe order), then the issues row.
		for _, childTable := range []string{"labels", "events", "dependencies", "comments"} {
			//nolint:gosec // G202: childTable is from a hardcoded list above.
			if _, err := tx.Exec(fmt.Sprintf("DELETE FROM %s WHERE issue_id = ?", childTable), id); err != nil {
				fmt.Printf("  Warning: failed to delete %s rows for %s: %v\n", childTable, id, err)
			}
		}
		if _, err := tx.Exec("DELETE FROM issues WHERE id = ?", id); err != nil {
			fmt.Printf("  Warning: failed to delete issues row for %s: %v\n", id, err)
		} else {
			removed++
			if showIndividual {
				fmt.Printf("  Removed stale issues-table copy of %s (canonical in wisps)\n", id)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit cross-table duplicate removals: %w", err)
	}

	_, _ = db.Exec("CALL DOLT_COMMIT('-Am', 'doctor: remove stale issues copies of wisps (be-iabdi)')") // Best effort

	fmt.Printf("  Fixed %d cross-table duplicate(s)\n", removed)
	return nil
}

// CountCrossTableDuplicates returns the number of IDs present in both the
// issues and wisps tables. Returns 0 and an error if the database is
// unreachable. Used by CheckCrossTableDuplicates in the doctor package.
func CountCrossTableDuplicates(path string) (int, error) {
	beadsDir, err := resolvedWorkspaceBeadsDir(path)
	if err != nil {
		return 0, err
	}

	db, err := openDoltDB(beadsDir)
	if err != nil {
		return 0, err
	}
	defer db.Close()

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM issues WHERE id IN (SELECT id FROM wisps)`).Scan(&count); err != nil {
		return 0, fmt.Errorf("query cross-table duplicates: %w", err)
	}
	return count, nil
}

// openDoltDB opens a Dolt database connection via MySQL protocol.
// Delegates to openFixDB for DSN construction (timeout + password support).
func openDoltDB(beadsDir string) (*sql.DB, error) {
	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		return nil, fmt.Errorf("no database configuration found")
	}

	db, err := openFixDB(beadsDir, cfg)
	if err != nil {
		return nil, fmt.Errorf("dolt server connection failed: %w", err)
	}

	// Verify the connection actually works
	if err := db.Ping(); err != nil {
		_ = db.Close() // Best effort cleanup
		return nil, fmt.Errorf("dolt server not reachable: %w", err)
	}

	return db, nil
}
