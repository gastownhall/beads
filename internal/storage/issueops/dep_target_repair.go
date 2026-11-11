package issueops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"maps"
	"slices"
	"strings"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

// DepRow is one dependency row a dependency-target re-file reads or rewrites
// (the upgrade repair and the arrival re-file below): the edge table it lives
// in, its primary key, its source and resolved target, its type, and the
// typed column the target belongs in.
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
SELECT '%[1]s', d.id, d.issue_id, d.depends_on_external, d.type,
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
// this database holds its target, with the typed column it belongs in. It
// returns the rows to move, and apart from them the blocking rows a move
// would turn into a gate that never clears (splitHierarchyConflictsInTx),
// which stay external.
//
// Only a row whose target a local table holds moves; one held nowhere stays
// external whatever its prefix, since the write-time fallback that files a
// missing same-prefix target as an issue is not evidence the row can bind.
// The versioned dependencies table binds only to targets in the versioned
// issues table, and its rows are checked against the versioned hierarchy
// alone: a clone-local wisp must not decide a synced row, or two clones with
// the same history would rewrite it differently. A row there whose target is
// only a local wisp stays external. wisp_dependencies is clone-local and
// follows DepTargetKindFor, wisp first; without a wisps table its rows can
// still bind to issues. A database without wisp_dependencies has no wisp rows
// to repair.
func PlanLocalExternalDepRepairInTx(ctx context.Context, tx DBTX) (moves, conflicts []DepRow, err error) {
	wispProbe := "FALSE"
	if hasWisps, err := optionalTableExistsInTx(ctx, tx, "wisps"); err != nil {
		return nil, nil, fmt.Errorf("wisps: plan local external dependency repair: %w", err)
	} else if hasWisps {
		wispProbe = wispHeldProbe
	}
	var plan []DepRow
	edgeTables := []string{"dependencies"}
	for _, leg := range []struct{ table, wispProbe string }{
		{"dependencies", "FALSE"},
		{"wisp_dependencies", wispProbe},
	} {
		//nolint:gosec // G201: table and probe are the hardcoded constants above.
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(localExternalDepSQL, leg.table, leg.wispProbe))
		if err != nil {
			if optionalBlockedTable(leg.table) && isTableNotExistError(err) {
				continue
			}
			return nil, nil, fmt.Errorf("%s: plan local external dependency repair: %w", leg.table, err)
		}
		if leg.table != "dependencies" {
			edgeTables = append(edgeTables, leg.table)
		}
		if plan, err = scanHeldExternalDeps(rows, plan); err != nil {
			return nil, nil, fmt.Errorf("%s: %w", leg.table, err)
		}
	}
	return splitHierarchyConflictsInTx(ctx, tx, plan, edgeTables)
}

// scanHeldExternalDeps appends to out each row of a planner read (table, id,
// source, target, type, held as a wisp, held as an issue) whose target a
// local table holds, with the typed column DepTargetKindFor files it in. It
// closes rows.
func scanHeldExternalDeps(rows *sql.Rows, out []DepRow) ([]DepRow, error) {
	for rows.Next() {
		var row DepRow
		var depType string
		var heldAsWisp, heldAsIssue bool
		if err := rows.Scan(&row.Table, &row.ID, &row.IssueID, &row.Target, &depType, &heldAsWisp, &heldAsIssue); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan external dependency: %w", err)
		}
		row.Type = types.DependencyType(depType)
		row.Kind = DepTargetKindFor(row.IssueID, row.Target, heldAsWisp, heldAsIssue)
		if (heldAsWisp || heldAsIssue) && row.Kind != DepTargetExternal {
			out = append(out, row)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("read external dependencies: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close external dependencies: %w", err)
	}
	return out, nil
}

// splitHierarchyConflictsInTx separates plan into the rows that can move and
// the blocking rows whose move would block an issue on its own ancestor or
// descendant: a gate that never clears, which every write path refuses
// (CheckBlockingHierarchyInTx) and the pre-GH#8299 paths let through for a
// target of another prefix. Such a row stays in depends_on_external, where it
// gates nothing, as it always has. The hierarchy walk reads every target
// column, so moving rows cannot create or hide a conflict.
//
// A row of the versioned dependencies table bound to an issue is checked
// against the versioned hierarchy alone (versionedHierarchyTables): each clone
// that holds the target can re-file it, so a clone-local wisp edge must not
// decide whether it moves, or two clones with the same history would file the
// synced row differently. Every other row is clone-local or binds to a
// clone-local wisp already, and is checked against every table of edgeTables.
func splitHierarchyConflictsInTx(ctx context.Context, tx DBTX, plan []DepRow, edgeTables []string) (moves, conflicts []DepRow, err error) {
	for _, row := range plan {
		hierarchy := edgeTables
		if row.Table == "dependencies" && row.Kind == DepTargetIssue {
			hierarchy = versionedHierarchyTables
		}
		err := CheckBlockingHierarchyInTx(ctx, tx, &types.Dependency{IssueID: row.IssueID, DependsOnID: row.Target, Type: row.Type}, hierarchy)
		var conflict *domain.DependencyHierarchyConflictError
		switch {
		case err == nil:
			moves = append(moves, row)
		case errors.As(err, &conflict):
			conflicts = append(conflicts, row)
		default:
			return nil, nil, fmt.Errorf("%s: check %s -> %s against its hierarchy: %w", row.Table, row.IssueID, row.Target, err)
		}
	}
	return moves, conflicts, nil
}

// versionedHierarchyTables is the hierarchy a synced row is checked against:
// the parent-child edges every clone shares.
var versionedHierarchyTables = []string{"dependencies"}

// ApplyLocalExternalDepRepairInTx moves each planned row's target from
// depends_on_external into its typed column in one bounded UPDATE, so the
// row never holds zero or two targets. The id, type, metadata, thread and
// audit columns are untouched; the resolved target, and so the deterministic
// id, is unchanged. A row that no longer has its planned shape is left alone.
// It reports how many rows it moved.
func ApplyLocalExternalDepRepairInTx(ctx context.Context, tx DBTX, plan []DepRow) (int, error) {
	moved, err := moveDepTargetsInTx(ctx, tx, plan)
	return len(moved), err
}

// moveDepTargetsInTx is ApplyLocalExternalDepRepairInTx returning the rows
// it moved.
func moveDepTargetsInTx(ctx context.Context, tx DBTX, plan []DepRow) ([]DepRow, error) {
	var moved []DepRow
	for _, row := range plan {
		//nolint:gosec // G201: the table is a planned constant and the column comes from DepTargetKind.Column.
		res, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET %s = ?, depends_on_external = NULL
			WHERE id = ? AND depends_on_external = ? AND depends_on_issue_id IS NULL AND depends_on_wisp_id IS NULL`,
			row.Table, row.Kind.Column()), row.Target, row.ID, row.Target)
		if err != nil {
			return moved, fmt.Errorf("%s: move %s -> %s into %s: %w", row.Table, row.IssueID, row.Target, row.Kind.Column(), err)
		}
		if n, err := res.RowsAffected(); err == nil && n > 0 {
			moved = append(moved, row)
		}
	}
	return moved, nil
}

// arrivedExternalDepSQL is one edge table's part of the arrival re-file's
// planner read: the rows of %[1]s naming one of a chunk of targets (%[2]s,
// its placeholders) in depends_on_external, with whether each target is held
// as a wisp (%[3]s, the probe) and as an issue. It filters on
// depends_on_external alone, so the read rides that column's index.
const arrivedExternalDepSQL = `
SELECT '%[1]s', d.id, d.issue_id, d.depends_on_external, d.type,
       %[3]s,
       EXISTS (SELECT 1 FROM issues i WHERE i.id = d.depends_on_external)
FROM %[1]s d
WHERE d.depends_on_external IN (%[2]s)`

// DepTargetRefile is what RefileArrivedDepTargetsInTx changed: the rows it
// moved, and the blocked flags it recomputed for them.
type DepTargetRefile struct {
	Moved   []DepRow
	Blocked RecomputeIsBlockedResult
}

// ChangedTables names the tables the re-file wrote: the edge tables of the
// rows it moved, and issues or wisps where it flipped a blocked flag.
func (r DepTargetRefile) ChangedTables() map[string]bool {
	changed := map[string]bool{}
	for _, row := range r.Moved {
		changed[row.Table] = true
	}
	if r.Blocked.IssueRowsChanged {
		changed["issues"] = true
	}
	if r.Blocked.WispRowsChanged {
		changed["wisps"] = true
	}
	return changed
}

// RefileArrivedDepTargetsInTx moves the dependency rows that name one of
// targets in depends_on_external into the typed column a write made now
// would choose, once this database holds the target, and recomputes the
// blocked state those edges move (GH#8299).
//
// An edge's target is classified when the edge is written (DepTargetKindFor),
// so an edge onto an id of another prefix this database does not hold yet is
// filed external: the documented "add now, route later" edge, an edge a
// repo's sync order wrote before the repo holding its target was synced, or
// one `bd repo remove` kept (DeleteIssuesBySourceRepoInTx). There readiness
// never sees it. Each write that brings ids into this database (a create that
// names its id, a batch create, import or repo sync, a promotion or plane
// move) re-files them, so such an edge gates its source from then on. A merge
// re-files the edges onto the ids it brings in (moveMergedDepTargetsInTx).
//
// The rows keep their id and payload. A row of either edge table binds as
// the write path binds it, a held wisp first: unlike the upgrade repair, the
// write that brings the target in is what every clone receives. A blocking
// row whose move would block an issue on its own ancestor or descendant,
// which the write path refuses, stays external (splitHierarchyConflictsInTx).
// For a synced row bound to an issue that hierarchy is the synced one, as
// another clone's merge of the target judges it.
func RefileArrivedDepTargetsInTx(ctx context.Context, tx DBTX, targets []string) (DepTargetRefile, error) {
	return refileArrivedDepTargetsInTx(ctx, tx, targets, []string{"dependencies", "wisp_dependencies"})
}

// refileArrivedDepTargetsInTx is RefileArrivedDepTargetsInTx re-filing only
// the rows of edgeTables.
func refileArrivedDepTargetsInTx(ctx context.Context, tx DBTX, targets, edgeTables []string) (DepTargetRefile, error) {
	moved, err := moveArrivedDepTargetsInTx(ctx, tx, targets, edgeTables)
	if err != nil || len(moved) == 0 {
		return DepTargetRefile{}, err
	}
	issueIDs, wispIDs, err := AffectedByDepRowsInTx(ctx, tx, moved)
	if err != nil {
		return DepTargetRefile{}, err
	}
	blocked, err := RecomputeIsBlockedInTxWithResult(ctx, tx, issueIDs, wispIDs)
	if err != nil {
		return DepTargetRefile{}, fmt.Errorf("recompute blocked state after re-filing arrived dependency targets: %w", err)
	}
	return DepTargetRefile{Moved: moved, Blocked: blocked}, nil
}

// moveArrivedDepTargetsInTx is the re-file without the blocked-state
// recompute, for a caller that folds the moved rows into its own (the batch
// create). It returns the rows it moved.
func moveArrivedDepTargetsInTx(ctx context.Context, tx DBTX, targets, edgeTables []string) ([]DepRow, error) {
	return moveHeldExternalDepTargetsInTx(ctx, tx, targets, edgeTables, true)
}

// moveMergedDepTargetsInTx is the re-file a merge runs (see
// RecomputeIsBlockedAfterMergeInTx) for the targets it brought in or whose
// edges it brought in. Every clone runs it on its own, so a row of the
// versioned dependencies table binds only to a versioned issue and is checked
// against the versioned hierarchy alone, as in the upgrade repair: a
// clone-local wisp must not decide a synced row, or two clones that merged
// the same history would rewrite it differently. It returns the rows it
// moved.
func moveMergedDepTargetsInTx(ctx context.Context, tx DBTX, targets []string) ([]DepRow, error) {
	return moveHeldExternalDepTargetsInTx(ctx, tx, targets, []string{"dependencies", "wisp_dependencies"}, false)
}

// moveAllHeldExternalDepTargetsInTx is moveMergedDepTargetsInTx for every
// target, for the merge's full pass: it moves each row the upgrade repair
// would (PlanLocalExternalDepRepairInTx) and returns the rows it moved.
func moveAllHeldExternalDepTargetsInTx(ctx context.Context, tx DBTX) ([]DepRow, error) {
	moves, conflicts, err := PlanLocalExternalDepRepairInTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	logHierarchyConflicts(conflicts)
	return fileDepTargetsInTx(ctx, tx, moves)
}

// moveHeldExternalDepTargetsInTx moves the rows of edgeTables that name one
// of targets in depends_on_external, once this database holds it, into
// their typed column, except a blocking row that would block an issue on
// its own ancestor or descendant (splitHierarchyConflictsInTx), which stays
// external and is logged. versionedBindsWisps says whether a row of the
// versioned dependencies table may bind to a clone-local wisp. It returns
// the rows it moved.
func moveHeldExternalDepTargetsInTx(ctx context.Context, tx DBTX, targets, edgeTables []string, versionedBindsWisps bool) ([]DepRow, error) {
	targets = slices.DeleteFunc(slices.Compact(slices.Sorted(slices.Values(targets))), func(id string) bool { return id == "" })
	if len(targets) == 0 || len(edgeTables) == 0 {
		return nil, nil
	}
	plan, edgeTables, err := planArrivedExternalDepsInTx(ctx, tx, targets, edgeTables, versionedBindsWisps)
	if err != nil || len(plan) == 0 {
		return nil, err
	}
	moves, conflicts, err := splitHierarchyConflictsInTx(ctx, tx, plan, edgeTables)
	if err != nil {
		return nil, err
	}
	logHierarchyConflicts(conflicts)
	return fileDepTargetsInTx(ctx, tx, moves)
}

// fileDepTargetsInTx is moveDepTargetsInTx for a re-file that runs beside
// other writes. A typed parent-child edge is what a close of its parent
// checks for open children, so each one it moves takes the coordination
// cell a new one takes.
func fileDepTargetsInTx(ctx context.Context, tx DBTX, moves []DepRow) ([]DepRow, error) {
	moved, err := moveDepTargetsInTx(ctx, tx, moves)
	if err != nil {
		return nil, err
	}
	for _, row := range moved {
		if row.Type == types.DepParentChild {
			if err := TouchDependencyCoordinationTableInTx(ctx, tx, row.Target, row.Table); err != nil {
				return nil, err
			}
		}
	}
	return moved, nil
}

// logHierarchyConflicts logs each blocking row a re-file leaves external
// because, as a local edge, it would block an issue on its own ancestor or
// descendant: nothing else says why an edge onto a held target never gates.
func logHierarchyConflicts(conflicts []DepRow) {
	for _, row := range conflicts {
		log.Printf("warning: GH#8299: %s: leaving %s edge %s -> %s external: as a local edge it would block an issue on its own ancestor or descendant",
			row.Table, row.Type, row.IssueID, row.Target)
	}
}

// wispHeldProbe is the planner's probe for a target held as a wisp.
const wispHeldProbe = "EXISTS (SELECT 1 FROM wisps w WHERE w.id = d.depends_on_external)"

// planArrivedExternalDepsInTx reads the rows of edgeTables that name one of
// targets in depends_on_external and whose target this database holds, in
// table and id order. A row of the versioned dependencies table counts a
// target held as a wisp only when versionedBindsWisps. It returns the edge
// tables it read: a database without the optional wisp tables (one not yet
// migrated) is read around them, as the upgrade repair reads it.
func planArrivedExternalDepsInTx(ctx context.Context, tx DBTX, targets, edgeTables []string, versionedBindsWisps bool) ([]DepRow, []string, error) {
	plan, err := readArrivedExternalDepsInTx(ctx, tx, targets, edgeTables, wispHeldProbe, versionedBindsWisps)
	if err != nil && isTableNotExistError(err) {
		var wispProbe string
		if edgeTables, wispProbe, err = presentArrivalTablesInTx(ctx, tx, edgeTables); err != nil {
			return nil, nil, err
		}
		plan, err = readArrivedExternalDepsInTx(ctx, tx, targets, edgeTables, wispProbe, versionedBindsWisps)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("plan arrived dependency targets: %w", err)
	}
	slices.SortFunc(plan, func(a, b DepRow) int {
		return strings.Compare(a.Table+"\x00"+a.ID, b.Table+"\x00"+b.ID)
	})
	return plan, edgeTables, nil
}

// readArrivedExternalDepsInTx is planArrivedExternalDepsInTx's read: one
// statement per chunk of targets covering every edge table, each probing
// wisps with wispProbe, except the versioned dependencies table when
// versionedBindsWisps is false.
func readArrivedExternalDepsInTx(ctx context.Context, tx DBTX, targets, edgeTables []string, wispProbe string, versionedBindsWisps bool) ([]DepRow, error) {
	if len(edgeTables) == 0 {
		return nil, nil
	}
	var plan []DepRow
	for start := 0; start < len(targets); start += queryBatchSize {
		placeholders, chunkArgs := buildSQLInClause(targets[start:min(start+queryBatchSize, len(targets))])
		parts := make([]string, 0, len(edgeTables))
		args := make([]any, 0, len(edgeTables)*len(chunkArgs))
		for _, table := range edgeTables {
			probe := wispProbe
			if table == "dependencies" && !versionedBindsWisps {
				probe = "FALSE"
			}
			parts = append(parts, fmt.Sprintf(arrivedExternalDepSQL, table, placeholders, probe))
			args = append(args, chunkArgs...)
		}
		//nolint:gosec // G201: tables and probe are hardcoded constants; placeholders holds only "?".
		rows, err := tx.QueryContext(ctx, strings.Join(parts, "\nUNION ALL\n"), args...)
		if err != nil {
			return nil, err
		}
		if plan, err = scanHeldExternalDeps(rows, plan); err != nil {
			return nil, err
		}
	}
	return plan, nil
}

// presentArrivalTablesInTx narrows edgeTables to the edge tables this
// database has, and returns the wisp probe the arrival planner can run: a
// constant when there is no wisps table to probe.
func presentArrivalTablesInTx(ctx context.Context, tx DBTX, edgeTables []string) ([]string, string, error) {
	wispProbe := "FALSE"
	if hasWisps, err := optionalTableExistsInTx(ctx, tx, "wisps"); err != nil {
		return nil, "", fmt.Errorf("wisps: plan arrived dependency targets: %w", err)
	} else if hasWisps {
		wispProbe = wispHeldProbe
	}
	present := make([]string, 0, len(edgeTables))
	for _, table := range edgeTables {
		if optionalBlockedTable(table) {
			if exists, err := optionalTableExistsInTx(ctx, tx, table); err != nil {
				return nil, "", fmt.Errorf("%s: plan arrived dependency targets: %w", table, err)
			} else if !exists {
				continue
			}
		}
		present = append(present, table)
	}
	return present, wispProbe, nil
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
