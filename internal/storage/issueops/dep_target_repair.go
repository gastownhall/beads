package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"maps"
	"slices"

	"github.com/steveyegge/beads/internal/types"
)

// DepRow is one dependency row the local-target repair reads or rewrites:
// the edge table it lives in, its primary key, its source and resolved
// target, its type, and the typed column the target belongs in.
type DepRow struct {
	Table   string // "dependencies" or "wisp_dependencies"
	ID      string
	IssueID string
	Target  string
	Type    types.DependencyType
	Kind    DepTargetKind
}

// localExternalDepSQL selects the well-formed rows of an edge table that
// carry their target only in depends_on_external, excluding explicit
// "external:" references, together with whether the target is held as a wisp
// and as an issue. %[1]s is the edge table, %[2]s the wisp probe: the wisps
// table, or a constant for the versioned table, which must not bind to a
// clone-local wisp.
const localExternalDepSQL = `
SELECT d.id, d.issue_id, d.depends_on_external, d.type,
       %[2]s,
       EXISTS (SELECT 1 FROM issues i WHERE i.id = d.depends_on_external)
FROM %[1]s d
WHERE d.depends_on_issue_id IS NULL
  AND d.depends_on_wisp_id IS NULL
  AND d.depends_on_external IS NOT NULL
  AND d.depends_on_external NOT LIKE 'external:%%'
ORDER BY d.id`

// PlanLocalExternalDepRepairInTx lists, read-only and in id order, every
// dependency row a pre-GH#8299 write filed in depends_on_external although
// this database holds its target, with the typed column it belongs in.
//
// The versioned dependencies table binds only to targets in the versioned
// issues table: a clone-local wisp must not decide a synced row, or two
// clones with the same history would rewrite it differently. A row there
// whose target is only a local wisp stays external. wisp_dependencies is
// clone-local and follows DepTargetKindFor, wisp first. A database without
// wisp tables has no wisp rows to repair.
func PlanLocalExternalDepRepairInTx(ctx context.Context, tx DBTX) ([]DepRow, error) {
	var out []DepRow
	for _, leg := range []struct{ table, wispProbe string }{
		{"dependencies", "FALSE"},
		{"wisp_dependencies", "EXISTS (SELECT 1 FROM wisps w WHERE w.id = d.depends_on_external)"},
	} {
		//nolint:gosec // G201: table and probe are the hardcoded constants above.
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(localExternalDepSQL, leg.table, leg.wispProbe))
		if err != nil {
			if optionalBlockedTable(leg.table) && isTableNotExistError(err) {
				continue
			}
			return nil, fmt.Errorf("%s: plan local external dependency repair: %w", leg.table, err)
		}
		for rows.Next() {
			var row DepRow
			var depType string
			var heldAsWisp, heldAsIssue bool
			if err := rows.Scan(&row.ID, &row.IssueID, &row.Target, &depType, &heldAsWisp, &heldAsIssue); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("%s: scan local external dependency: %w", leg.table, err)
			}
			row.Table, row.Type = leg.table, types.DependencyType(depType)
			row.Kind = DepTargetKindFor(row.IssueID, row.Target, heldAsWisp, heldAsIssue)
			if row.Kind != DepTargetExternal {
				out = append(out, row)
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("%s: read local external dependencies: %w", leg.table, err)
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("%s: close local external dependencies: %w", leg.table, err)
		}
	}
	return out, nil
}

// ApplyLocalExternalDepRepairInTx moves each planned row's target from
// depends_on_external into its typed column in one bounded UPDATE, so the
// row never holds zero or two targets. The id, type, metadata, thread and
// audit columns are untouched; the resolved target, and so the deterministic
// id, is unchanged. A row that no longer has its planned shape is left alone.
// It reports how many rows it moved.
func ApplyLocalExternalDepRepairInTx(ctx context.Context, tx DBTX, plan []DepRow) (int, error) {
	moved := 0
	for _, row := range plan {
		//nolint:gosec // G201: the table is a planned constant and the column comes from DepTargetKind.Column.
		res, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET %s = ?, depends_on_external = NULL
			WHERE id = ? AND depends_on_external = ? AND depends_on_issue_id IS NULL AND depends_on_wisp_id IS NULL`,
			row.Table, row.Kind.Column()), row.Target, row.ID, row.Target)
		if err != nil {
			return moved, fmt.Errorf("%s: move %s -> %s into %s: %w", row.Table, row.IssueID, row.Target, row.Kind.Column(), err)
		}
		if n, err := res.RowsAffected(); err == nil {
			moved += int(n)
		}
	}
	return moved, nil
}

// CrossPrefixTypedDepsInTx lists the dependency rows whose target is already
// in a typed column although its prefix differs from the source's. A repair
// interrupted between moving rows and recomputing blocked state leaves its
// moved rows in exactly this shape, so a retry recomputes them from this list
// instead of from a plan that no longer finds them. Missing wisp tables
// contribute nothing.
func CrossPrefixTypedDepsInTx(ctx context.Context, tx DBTX) ([]DepRow, error) {
	var out []DepRow
	for _, table := range []string{"dependencies", "wisp_dependencies"} {
		//nolint:gosec // G201: table is one of the two hardcoded values above.
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(`
			SELECT id, issue_id, depends_on_issue_id, depends_on_wisp_id, type FROM %s
			WHERE (depends_on_issue_id IS NULL) <> (depends_on_wisp_id IS NULL)
			  AND depends_on_external IS NULL
			ORDER BY id`, table))
		if err != nil {
			if optionalBlockedTable(table) && isTableNotExistError(err) {
				continue
			}
			return nil, fmt.Errorf("%s: list cross-prefix typed dependencies: %w", table, err)
		}
		for rows.Next() {
			var row DepRow
			var issueTarget, wispTarget sql.NullString
			var depType string
			if err := rows.Scan(&row.ID, &row.IssueID, &issueTarget, &wispTarget, &depType); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("%s: scan typed dependency: %w", table, err)
			}
			row.Table, row.Type = table, types.DependencyType(depType)
			row.Target, row.Kind = issueTarget.String, DepTargetIssue
			if wispTarget.Valid {
				row.Target, row.Kind = wispTarget.String, DepTargetWisp
			}
			if types.ExtractPrefix(row.IssueID) != types.ExtractPrefix(row.Target) {
				out = append(out, row)
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("%s: read typed dependencies: %w", table, err)
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("%s: close typed dependencies: %w", table, err)
		}
	}
	return out, nil
}

// AffectedByDepRowsInTx is the union, sorted, of AffectedByDepChangeInTx
// (dependencies rows) and AffectedByDepChangeForWispInTx (wisp_dependencies
// rows) over rows: every issue and wisp whose blocked state a change to one
// of those edges can move, inherited descendants included.
func AffectedByDepRowsInTx(ctx context.Context, tx DBTX, rows []DepRow) ([]string, []string, error) {
	issueSet, wispSet := map[string]bool{}, map[string]bool{}
	for _, row := range rows {
		affected := AffectedByDepChangeInTx
		if row.Table == "wisp_dependencies" {
			affected = AffectedByDepChangeForWispInTx
		}
		issueIDs, wispIDs, err := affected(ctx, tx, row.IssueID, row.Target, row.Type)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: affected by %s -> %s: %w", row.Table, row.IssueID, row.Target, err)
		}
		for _, id := range issueIDs {
			issueSet[id] = true
		}
		for _, id := range wispIDs {
			wispSet[id] = true
		}
	}
	return slices.Sorted(maps.Keys(issueSet)), slices.Sorted(maps.Keys(wispSet)), nil
}
