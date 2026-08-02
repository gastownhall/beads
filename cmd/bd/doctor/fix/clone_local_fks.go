package fix

import (
	"database/sql"
	"fmt"
)

// CloneLocalFK describes a foreign key held by a clone-local (dolt_ignored,
// working-set-only) table.
//
// bd-7bpkd: CALL DOLT_RESET('--hard') silently DROPS every FK on every
// dolt_ignored table — including FKs whose referenced table is itself
// clone-local and was never touched by the reset (verified on dolt-sql-server
// 2.2.0 and 2.2.2). Enforcement stops, the loss survives server restarts, and
// nothing re-links it. Every production hard-reset site inherits this:
// flatten/compact squash, merge abort recovery, and the migration-lock error
// path. Orphan rows then accumulate until the constraint is re-added.
type CloneLocalFK struct {
	Table      string
	Constraint string
	Column     string
	RefTable   string
	RefColumn  string
}

// CloneLocalFKs lists every foreign key on a clone-local table (all use
// ON DELETE CASCADE ON UPDATE CASCADE). Keep in sync with the migrations that
// define them — main 0005/0042/0062 (events), 0021/0047/0058 (wisp aux), and
// their ignored-plane twins (ignored/0002, ignored/0004, ignored/0019).
// leases, local_metadata, repo_mtimes, and wisps carry no FKs;
// child_counters is tracked-plane (its FK survives resets).
// The cgo test's drift guard asserts this list matches a freshly migrated
// store exactly.
var CloneLocalFKs = []CloneLocalFK{
	{Table: "events", Constraint: "fk_events_issue", Column: "issue_id", RefTable: "issues", RefColumn: "id"},
	{Table: "wisp_dependencies", Constraint: "fk_wisp_dep_issue", Column: "issue_id", RefTable: "wisps", RefColumn: "id"},
	{Table: "wisp_dependencies", Constraint: "fk_wisp_dep_wisp_target", Column: "depends_on_wisp_id", RefTable: "wisps", RefColumn: "id"},
	{Table: "wisp_dependencies", Constraint: "fk_wisp_dep_issue_target", Column: "depends_on_issue_id", RefTable: "issues", RefColumn: "id"},
	{Table: "wisp_labels", Constraint: "fk_wisp_labels_issue", Column: "issue_id", RefTable: "wisps", RefColumn: "id"},
	{Table: "wisp_comments", Constraint: "fk_wisp_comments_issue", Column: "issue_id", RefTable: "wisps", RefColumn: "id"},
	{Table: "wisp_events", Constraint: "fk_wisp_events_issue", Column: "issue_id", RefTable: "wisps", RefColumn: "id"},
	{Table: "wisp_child_counters", Constraint: "fk_wisp_child_counters_parent", Column: "parent_id", RefTable: "wisps", RefColumn: "id"},
}

// SeveredCloneLocalFK is one severed constraint found by the scan, with the
// number of orphaned rows that accumulated while enforcement was off.
type SeveredCloneLocalFK struct {
	CloneLocalFK
	Orphans int
}

// ScanSeveredCloneLocalFKs reports which clone-local FKs are missing from the
// live schema. Used by CheckCloneLocalFKs in the doctor package.
func ScanSeveredCloneLocalFKs(path string) ([]SeveredCloneLocalFK, error) {
	beadsDir, err := resolvedWorkspaceBeadsDir(path)
	if err != nil {
		return nil, err
	}

	db, _, err := openDoltDB(beadsDir)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	return scanSeveredCloneLocalFKs(db)
}

func scanSeveredCloneLocalFKs(db *sql.DB) ([]SeveredCloneLocalFK, error) {
	var severed []SeveredCloneLocalFK
	for _, fk := range CloneLocalFKs {
		var tables int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?`,
			fk.Table,
		).Scan(&tables); err != nil {
			return nil, fmt.Errorf("check %s exists: %w", fk.Table, err)
		}
		if tables == 0 {
			continue
		}

		var constraints int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
			 WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND CONSTRAINT_NAME = ? AND CONSTRAINT_TYPE = 'FOREIGN KEY'`,
			fk.Table, fk.Constraint,
		).Scan(&constraints); err != nil {
			return nil, fmt.Errorf("check %s.%s: %w", fk.Table, fk.Constraint, err)
		}
		if constraints > 0 {
			continue
		}

		var orphans int
		//nolint:gosec // G201: identifiers come from the fixed CloneLocalFKs spec above, not user input.
		orphanCount := fmt.Sprintf(
			`SELECT COUNT(*) FROM %s t WHERE t.%s IS NOT NULL AND NOT EXISTS (SELECT 1 FROM %s r WHERE r.%s = t.%s)`,
			fk.Table, fk.Column, fk.RefTable, fk.RefColumn, fk.Column,
		)
		if err := db.QueryRow(orphanCount).Scan(&orphans); err != nil {
			return nil, fmt.Errorf("count %s orphans: %w", fk.Table, err)
		}

		severed = append(severed, SeveredCloneLocalFK{CloneLocalFK: fk, Orphans: orphans})
	}
	return severed, nil
}

// CloneLocalFKEnforcement re-links severed clone-local FKs: for each missing
// constraint it deletes the orphaned rows that accumulated while enforcement
// was off (ADD CONSTRAINT validates existing rows, so they must go first),
// then re-adds the constraint in place. Verified on dolt 2.2.2: the re-added
// FK resolves against the current tracked root and enforces again.
func CloneLocalFKEnforcement(path string, verbose bool) error {
	beadsDir, err := resolvedWorkspaceBeadsDir(path)
	if err != nil {
		return err
	}

	db, cfg, err := openDoltDB(beadsDir)
	if err != nil {
		fmt.Printf("  Clone-local FK fix skipped (%v)\n", err)
		return nil
	}
	defer db.Close()

	if skip, err := guardFixTarget("Clone-local FK fix", db, beadsDir, cfg); skip {
		return err
	}

	return relinkSeveredCloneLocalFKs(db, verbose)
}

// relinkSeveredCloneLocalFKs is the core of CloneLocalFKEnforcement, split out
// so tests can drive it against a database handle directly.
func relinkSeveredCloneLocalFKs(db *sql.DB, verbose bool) error {
	severed, err := scanSeveredCloneLocalFKs(db)
	if err != nil {
		return err
	}
	if len(severed) == 0 {
		fmt.Println("  ✓ All clone-local FKs present")
		return nil
	}

	for _, fk := range severed {
		if fk.Orphans > 0 {
			//nolint:gosec // G201: identifiers come from the fixed CloneLocalFKs spec, not user input.
			deleteOrphans := fmt.Sprintf(
				`DELETE FROM %s WHERE %s IS NOT NULL AND NOT EXISTS (SELECT 1 FROM %s r WHERE r.%s = %s.%s)`,
				fk.Table, fk.Column, fk.RefTable, fk.RefColumn, fk.Table, fk.Column,
			)
			result, err := db.Exec(deleteOrphans)
			if err != nil {
				return fmt.Errorf("delete %s orphans: %w", fk.Table, err)
			}
			if verbose {
				removed, _ := result.RowsAffected()
				fmt.Printf("  Removed %d orphaned row(s) from %s\n", removed, fk.Table)
			}
		}

		//nolint:gosec // G201: identifiers come from the fixed CloneLocalFKs spec, not user input.
		addConstraint := fmt.Sprintf(
			`ALTER TABLE %s ADD CONSTRAINT %s FOREIGN KEY (%s) REFERENCES %s (%s) ON DELETE CASCADE ON UPDATE CASCADE`,
			fk.Table, fk.Constraint, fk.Column, fk.RefTable, fk.RefColumn,
		)
		if _, err := db.Exec(addConstraint); err != nil {
			return fmt.Errorf("re-add %s.%s: %w", fk.Table, fk.Constraint, err)
		}
		fmt.Printf("  ✓ Re-linked %s.%s (%d orphaned row(s) removed)\n", fk.Table, fk.Constraint, fk.Orphans)
	}
	return nil
}
