package schema

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// depRepairTables are the two edge tables the cross-prefix repair covers, in
// execution order. Both are probed and moved by the same rule; only the
// evidence each plane may act on differs (see planCrossPrefixDepRepair).
var depRepairTables = []string{"dependencies", "wisp_dependencies"}

// depRepairMove is one edge row whose target moves from depends_on_external to
// the typed column the database's own evidence names. destColumn is that typed
// column; the row id never changes, because depid keys on (issue_id, resolved
// target), not on which column held the target.
type depRepairMove struct {
	rowID      string
	source     string
	target     string
	depType    string
	destColumn string
}

// depRepairPlan is the validated statement plan for one edge table. Planning is
// read-only, so both tables are planned before either executes — the same
// contract rekeyDependencyIDs keeps.
type depRepairPlan struct {
	table string
	moves []depRepairMove
}

func (p *depRepairPlan) writes() bool { return len(p.moves) > 0 }

// repairCrossPrefixDepTargets is the data half of the GH#8299 fix: legacy edge
// rows that the old prefix-comparing classifier stored in depends_on_external
// even though this database holds the target are moved into the typed column
// the evidence names, so readiness, close gates and hierarchy checks see them.
//
// Before the write-path fix, any cross-prefix target was classified External
// before a single read, so `bd dep add bd-a hq-b` on a database holding both
// prefixes recorded a resolvable same-database blocker as an unresolvable
// external string — it never gated readiness and never answered `bd close`.
// New writes go to the typed column; this pass heals the rows the old
// classifier already wrote.
//
// Which evidence a plane may act on is per table, and the asymmetry is the
// point (the AC-8 convergence contract):
//
//   - dependencies is synced. wisps are clone-local, so moving a versioned row
//     onto a wisp reference would make two clones of the same lineage disagree
//     about the versioned table. The versioned plane therefore only moves a
//     row when the target is held in issues, which syncs; a target held only
//     as a wisp stays external on every clone.
//   - wisp_dependencies is dolt-ignored, so it is already clone-local: a target
//     held as a wisp moves to depends_on_wisp_id (wisp first, matching
//     ClassifyDepTargetInTx's ranking), a target held only as an issue moves
//     to depends_on_issue_id.
//
// A target no local row answers is left exactly as it is: the missing
// foreign-prefix "add now, route later" shape is legal, and the missing
// same-prefix shape was never writable through the old classifier either.
// Explicit "external:" references are not candidates at all.
//
// Like rekeyDependencyIDs, this is idempotent and runs on every pass where
// migration work is needed — ignored migration 0032 is what makes that true
// once on every already-converged clone. IgnoredSource.migrate records the
// marker AFTER this returns, so an aborted repair is retried on the next open
// instead of the database claiming it migrated. Each move is a final-state
// mutation, so a partially applied plan is benign: the next pass finishes it.
//
// depid keys on the resolved target, so a move never rewrites the primary key,
// and the re-key that runs immediately before this pass has already merged any
// duplicate typed/external twins of the same logical edge (same derived id,
// per-column unique keys), so no uk_dep_*_target conflict can oppose a move.
//
// dirtyBefore is MigrateUp's set of committable tables that were already dirty
// when the pass started. The refusal that consults it lands with the
// dirty-table contract (AC-6), which is why it is not a parameter yet.
func repairCrossPrefixDepTargets(ctx context.Context, db DBConn) (bool, error) {
	plans := make([]*depRepairPlan, 0, len(depRepairTables))
	for _, table := range depRepairTables {
		plan, err := planCrossPrefixDepRepair(ctx, db, table)
		if err != nil {
			return false, err
		}
		plans = append(plans, plan)
	}

	issueAffected := map[string]bool{}
	wispAffected := map[string]bool{}
	wrote := false
	for _, plan := range plans {
		if !plan.writes() {
			continue
		}
		wrote = true
		for _, m := range plan.moves {
			//nolint:gosec // G201: table and destColumn are hardcoded constants, never user input.
			if _, err := db.ExecContext(ctx, fmt.Sprintf(
				`UPDATE %s SET %s = ?, depends_on_external = NULL WHERE id = ?`,
				plan.table, m.destColumn), m.target, m.rowID); err != nil {
				return true, fmt.Errorf("%s: move edge %s -> %s into %s: %w",
					plan.table, m.source, m.target, m.destColumn, err)
			}
		}
		// Post-move cascade, the same shape the write path runs after an
		// insert: the edge just became resolvable, so the blocked state of its
		// source (and any parent-child subtree it darkens, GH#6506) must be
		// recomputed. The mark/unmark templates pin updated_at, so a flip does
		// not churn it; the journal is keyed to registered transactions, which
		// a bare migration connection never is, so the journal tail is a no-op.
		for _, m := range plan.moves {
			var issueIDs, wispIDs []string
			var err error
			if plan.table == "dependencies" {
				issueIDs, wispIDs, err = issueops.AffectedByDepChangeInTx(ctx, db, m.source, m.target, types.DependencyType(m.depType))
			} else {
				issueIDs, wispIDs, err = issueops.AffectedByDepChangeForWispInTx(ctx, db, m.source, m.target, types.DependencyType(m.depType))
			}
			if err != nil {
				return true, fmt.Errorf("%s: expand affected set for %s -> %s: %w", plan.table, m.source, m.target, err)
			}
			for _, id := range issueIDs {
				issueAffected[id] = true
			}
			for _, id := range wispIDs {
				wispAffected[id] = true
			}
		}
	}
	if !wrote {
		return false, nil
	}
	issueIDs := sortedKeys(issueAffected)
	wispIDs := sortedKeys(wispAffected)
	if _, err := issueops.MarkIsBlockedInTxWithResult(ctx, db, issueIDs, wispIDs); err != nil {
		return true, fmt.Errorf("recompute is_blocked after cross-prefix repair: %w", err)
	}
	return true, nil
}

// depRepairCandidate is one scanned legacy row awaiting its evidence probes.
// depType is scanned as a nullable column: some legacy shapes predate the type
// NOT NULL constraint, and the affected-set walk treats an unknown type as
// "no recompute needed" rather than refusing the whole repair for it.
type depRepairCandidate struct {
	id      string
	issueID string
	depType sql.NullString
	target  string
}

// planCrossPrefixDepRepair scans one edge table for legacy misclassified rows
// and returns the validated plan. It is read-only. table must be a hardcoded
// constant ("dependencies" or "wisp_dependencies").
func planCrossPrefixDepRepair(ctx context.Context, db DBConn, table string) (*depRepairPlan, error) {
	plan := &depRepairPlan{table: table}
	// Skip cleanly if the table or its id column is absent (an older or partial
	// schema, or a clone that lost its dolt-ignored wisp plane): nothing to move.
	hasID, err := columnExists(ctx, db, table, "id")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", table, err)
	}
	if !hasID {
		return plan, nil
	}
	wispsPresent := false
	if table == "wisp_dependencies" {
		wispsPresent, err = columnExists(ctx, db, "wisps", "id")
		if err != nil {
			return nil, fmt.Errorf("wisps: %w", err)
		}
	}

	//nolint:gosec // G201: table is a hardcoded constant, never user input.
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`
SELECT id, issue_id, type, depends_on_external FROM %s
WHERE depends_on_external IS NOT NULL
  AND depends_on_issue_id IS NULL
  AND depends_on_wisp_id IS NULL
  AND depends_on_external NOT LIKE 'external:%%'`, table))
	if err != nil {
		return nil, fmt.Errorf("%s: scan legacy external-only edges: %w", table, err)
	}
	// Materialize the candidates before probing: the migration conn is a
	// pinned single connection, so a probe issued while the scan's Rows is
	// still open cannot be served.
	var candidates []depRepairCandidate
	for rows.Next() {
		var c depRepairCandidate
		if err := rows.Scan(&c.id, &c.issueID, &c.depType, &c.target); err != nil {
			return nil, fmt.Errorf("%s: scan legacy edge row: %w", table, err)
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%s: iterate legacy edge rows: %w", table, err)
	}
	_ = rows.Close()

	for _, c := range candidates {
		heldIssue, err := depRepairTargetHeld(ctx, db, "issues", c.target)
		if err != nil {
			return nil, err
		}
		heldWisp := false
		if wispsPresent {
			heldWisp, err = depRepairTargetHeld(ctx, db, "wisps", c.target)
			if err != nil {
				return nil, err
			}
		}
		destColumn := ""
		switch {
		case table == "dependencies" && heldIssue:
			// The versioned plane only trusts evidence that syncs (see the
			// repairCrossPrefixDepTargets doc comment).
			destColumn = "depends_on_issue_id"
		case table == "wisp_dependencies" && heldWisp:
			destColumn = "depends_on_wisp_id"
		case table == "wisp_dependencies" && heldIssue:
			destColumn = "depends_on_issue_id"
		}
		if destColumn == "" {
			continue
		}
		plan.moves = append(plan.moves, depRepairMove{
			rowID: c.id, source: c.issueID, target: c.target, depType: c.depType.String, destColumn: destColumn,
		})
	}
	// Deterministic execution order: two clones with the same data run the
	// same statements in the same order.
	sort.Slice(plan.moves, func(i, j int) bool { return plan.moves[i].rowID < plan.moves[j].rowID })
	return plan, nil
}

// depRepairTargetHeld reports whether id has a row in table, which is the
// evidence the repair acts on. Only sql.ErrNoRows means "not held".
func depRepairTargetHeld(ctx context.Context, db DBConn, table, id string) (bool, error) {
	var one int
	//nolint:gosec // G201: table is a hardcoded constant, never user input.
	err := db.QueryRowContext(ctx, fmt.Sprintf(`SELECT 1 FROM %s WHERE id = ? LIMIT 1`, table), id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("%s: probe dependency target %s: %w", table, id, err)
	}
	return true, nil
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
