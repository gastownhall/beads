package fix

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/storage/depid"
)

// depKeyTables are the edge tables carrying the deterministic surrogate key
// derived by depid.New (see #4259).
var depKeyTables = []string{"dependencies", "wisp_dependencies"}

// DepKeyAnomalies summarizes one table's leftovers from the #4259 rekey
// backfill (bd-6dnrw.17): rows whose surrogate id is not the deterministic
// depid.New(issue_id, target) — per-clone-random keys that break bd dolt pull —
// and rows with no target at all (NULL in all three typed columns), which the
// backfill deliberately leaves untouched.
type DepKeyAnomalies struct {
	Table      string
	MisKeyed   [][2]string // {currentID, deterministicID} pairs
	NullTarget []string    // ids of rows with no dependency target
}

// ScanDependencyKeys reports rekey-backfill leftovers in both edge tables.
// Tables without an id column (older or partial schemas) are skipped; only
// tables with anomalies appear in the result.
func ScanDependencyKeys(ctx context.Context, db *sql.DB) ([]DepKeyAnomalies, error) {
	var out []DepKeyAnomalies
	for _, table := range depKeyTables {
		hasID, err := depKeyColumnExists(ctx, db, table, "id")
		if err != nil {
			return nil, fmt.Errorf("%s: %w", table, err)
		}
		if !hasID {
			continue
		}

		a := DepKeyAnomalies{Table: table}
		//nolint:gosec // G201: table is a hardcoded constant, never user input.
		rows, err := db.QueryContext(ctx, fmt.Sprintf(
			`SELECT id, issue_id, %s FROM %s`, fixDependencyTargetExpr, table))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", table, err)
		}
		for rows.Next() {
			var id, issueID string
			var target sql.NullString
			if err := rows.Scan(&id, &issueID, &target); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("%s: %w", table, err)
			}
			if !target.Valid {
				a.NullTarget = append(a.NullTarget, id)
				continue
			}
			if want := depid.New(issueID, target.String); want != id {
				a.MisKeyed = append(a.MisKeyed, [2]string{id, want})
			}
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("%s: %w", table, err)
		}
		if len(a.MisKeyed)+len(a.NullTarget) > 0 {
			out = append(out, a)
		}
	}
	return out, nil
}

// DependencyKeys repairs rekey-backfill leftovers (bd-6dnrw.17): mis-keyed
// rows are re-keyed to their deterministic id, and rows with no target are
// removed — they point at nothing, so their natural identity (and therefore
// their deterministic key) is unknowable, and ck_dep_one_target forbids
// creating them today.
// If verbose is true, prints each repaired row; otherwise shows only a summary.
func DependencyKeys(path string, verbose bool) error {
	beadsDir, err := resolvedWorkspaceBeadsDir(path)
	if err != nil {
		return err
	}

	db, err := openDoltDB(beadsDir)
	if err != nil {
		fmt.Printf("  Dependency key fix skipped (%v)\n", err)
		return nil
	}
	defer db.Close()

	return repairDependencyKeys(context.Background(), db, verbose)
}

// repairDependencyKeys scans and repairs rekey-backfill leftovers on an open
// connection. Split from DependencyKeys so the repair logic is testable
// against an existing store handle.
func repairDependencyKeys(ctx context.Context, db *sql.DB, verbose bool) error {
	anomalies, err := ScanDependencyKeys(ctx, db)
	if err != nil {
		return fmt.Errorf("failed to scan dependency keys: %w", err)
	}
	if len(anomalies) == 0 {
		fmt.Println("  No dependency key anomalies to fix")
		return nil
	}

	// Uses explicit transaction so writes persist when @@autocommit is OFF
	// (e.g. Dolt server started with --no-auto-commit).
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	var rekeyed, removed, failed int
	repairedTables := make(map[string]bool)
	for _, a := range anomalies {
		showIndividual := verbose || len(a.MisKeyed)+len(a.NullTarget) < 20
		for _, mk := range a.MisKeyed {
			//nolint:gosec // G201: table is a hardcoded constant, never user input.
			if _, err := tx.Exec(fmt.Sprintf(`UPDATE %s SET id = ? WHERE id = ?`, a.Table), mk[1], mk[0]); err != nil {
				fmt.Printf("  Warning: failed to re-key %s row %s (row keeps its old id): %v\n", a.Table, mk[0], err)
				failed++
				continue
			}
			rekeyed++
			repairedTables[a.Table] = true
			if showIndividual {
				fmt.Printf("  Re-keyed %s row %s → %s\n", a.Table, mk[0], mk[1])
			}
		}
		for _, id := range a.NullTarget {
			//nolint:gosec // G201: table is a hardcoded constant, never user input.
			if _, err := tx.Exec(fmt.Sprintf(`DELETE FROM %s WHERE id = ?`, a.Table), id); err != nil {
				fmt.Printf("  Warning: failed to remove %s row %s: %v\n", a.Table, id, err)
				failed++
				continue
			}
			removed++
			repairedTables[a.Table] = true
			if showIndividual {
				fmt.Printf("  Removed %s row %s (no dependency target)\n", a.Table, id)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit dependency key repairs: %w", err)
	}

	// Commit changes in Dolt, staging only the repaired tables so an unrelated
	// dirty working set is not swept under this message. Best effort: commit
	// advisory; repair already applied.
	if len(repairedTables) > 0 {
		for table := range repairedTables {
			_, _ = db.Exec("CALL DOLT_ADD(?)", table)
		}
		_, _ = db.Exec("CALL DOLT_COMMIT('-m', 'doctor: re-key dependency ids to deterministic values')")
	}

	if failed > 0 {
		fmt.Printf("  Dependency keys: %d re-keyed, %d removed, %d FAILED — failed rows keep their old keys; resolve the warnings above and re-run bd doctor\n",
			rekeyed, removed, failed)
		return nil
	}
	fmt.Printf("  Fixed dependency keys: %d re-keyed, %d removed\n", rekeyed, removed)
	return nil
}

// depKeyColumnExists reports whether table.column is present in the current schema.
func depKeyColumnExists(ctx context.Context, db *sql.DB, table, column string) (bool, error) {
	var count int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
		 WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ?`,
		table, column).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}
