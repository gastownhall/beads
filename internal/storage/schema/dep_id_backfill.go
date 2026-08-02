package schema

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/storage/depid"
)

// rekeyDependencyIDs rewrites the surrogate primary key of every dependencies
// and wisp_dependencies row whose id does not already equal
// depid.New(issue_id, target). It is the data half of the #4259 fix.
//
// Migration 0043 minted dependencies.id from DEFAULT (UUID()), which is
// per-clone-random; migration 0050 + the deterministic insert paths fix new
// rows, but rows that already exist on an upgrading clone still carry the random
// id. Leaving them would keep two independently-migrated clones divergent (same
// edge, different primary key) and break `bd dolt pull`. This rewrites them to
// the deterministic value so two clones converge to byte-identical dependencies.
//
// It runs from MigrateUp right after the schema migrations (so 0050 has already
// asserted the canonical schema), and only on a pass where migration work was
// needed — it is not part of the steady-state open path. It is idempotent: a row
// already keyed deterministically is skipped, so re-running on a later migration
// pass is a cheap no-op. dependencies changes are staged and committed by
// MigrateUp; wisp_dependencies is dolt-ignored, so its re-key stays clone-local
// (it only escapes on promotion, which copies the id).
func rekeyDependencyIDs(ctx context.Context, db DBConn) (bool, error) {
	wroteDeps, err := rekeyDependencyTable(ctx, db, "dependencies")
	if err != nil {
		return wroteDeps, fmt.Errorf("dependencies: %w", err)
	}
	wroteWisp, err := rekeyDependencyTable(ctx, db, "wisp_dependencies")
	if err != nil {
		return wroteDeps || wroteWisp, fmt.Errorf("wisp_dependencies: %w", err)
	}
	return wroteDeps || wroteWisp, nil
}

// rekeyDependencyTable re-derives ids for one edge table. table must be a
// hardcoded constant ("dependencies" or "wisp_dependencies").
func rekeyDependencyTable(ctx context.Context, db DBConn, table string) (bool, error) {
	// Skip cleanly if the table or its id column isn't present (e.g. an older or
	// partial schema where the surrogate key was never added): nothing to re-key.
	hasID, err := columnExists(ctx, db, table, "id")
	if err != nil {
		return false, err
	}
	if !hasID {
		return false, nil
	}

	// The natural target is the single non-null of the three typed columns —
	// exactly what depid keys on and what the uk_dep_* unique keys enforce.
	//nolint:gosec // G201: table is a hardcoded constant, never user input.
	rows, err := db.QueryContext(ctx, fmt.Sprintf(
		`SELECT id, issue_id, COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) FROM %s`,
		table))
	if err != nil {
		return false, err
	}
	type rekey struct{ oldID, newID string }
	var todo []rekey
	for rows.Next() {
		var id, issueID string
		var target sql.NullString
		if err := rows.Scan(&id, &issueID, &target); err != nil {
			_ = rows.Close()
			return false, err
		}
		if !target.Valid {
			// Malformed row with no target (ck_dep_one_target should prevent this);
			// leave it untouched for `bd doctor` to surface rather than guessing.
			continue
		}
		if want := depid.New(issueID, target.String); want != id {
			todo = append(todo, rekey{oldID: id, newID: want})
		}
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}

	for _, r := range todo {
		//nolint:gosec // G201: table is a hardcoded constant, never user input.
		if _, err := db.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET id = ? WHERE id = ?`, table),
			r.newID, r.oldID); err != nil {
			return true, fmt.Errorf("re-key id %s -> %s: %w", r.oldID, r.newID, err)
		}
	}
	return len(todo) > 0, nil
}

// columnExists reports whether table.column is present in the current schema.
func columnExists(ctx context.Context, db DBConn, table, column string) (bool, error) {
	var count int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
		 WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ?`,
		table, column).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}
