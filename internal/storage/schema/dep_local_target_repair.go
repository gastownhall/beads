package schema

import (
	"context"
	"fmt"
	"log"
	"slices"

	"github.com/steveyegge/beads/internal/storage/issueops"
)

// depLocalTargetRepairMarker is ignored migration 0032, the clone-local
// marker that forces one repairLocalExternalDepTargets pass on every
// existing clone (GH#8299).
const depLocalTargetRepairMarker = 32

// repairLocalExternalDepTargets moves dependency rows that a pre-GH#8299
// write filed in depends_on_external, although this database holds their
// target, into the typed column readiness joins, and recomputes the blocked
// state those edges move. Until then the edge never gated readiness: a
// dependent stayed ready beside its open cross-prefix blocker.
//
// It runs from MigrateUp right after rekeyDependencyIDs, on every pass with
// migration work, and is idempotent: a converged database plans nothing and
// writes nothing. Ignored migration 0032 makes one such pass happen on every
// existing clone; ignoredSource.migrate records it only after this returns.
//
// Planning is read-only and covers both edge tables and the affected issue
// and wisp sets before anything is written, so a refusal writes nothing.
// dependencies changes and issues blocked flags are staged and committed by
// MigrateUp; wisp tables are dolt-ignored, so their changes stay local. A
// blocking row whose move would block an issue on its own ancestor or
// descendant, which the pre-fix writes let through for a target of another
// prefix, stays external and is logged: as a local edge it would be a gate
// that never clears.
//
// The pass is not atomic (see depPlan.execute: DBConn may be pooled). A pass
// interrupted after moving rows but before recomputing leaves those rows
// typed, uncommitted, and their dependents' blocked flags stale, and the
// next plan no longer finds them. The next open then refuses, like any pass
// over a dirty graph table (below), until those rows are committed (`bd dolt
// commit`); while the 0032 marker is pending, that retry's recompute also
// covers every typed cross-prefix edge, which includes every cross-prefix row
// this repair can have moved, so it converges. A same-prefix legacy row the
// interrupted pass moved is not in that set; `bd doctor --fix` recomputes
// any blocked flag left stale.
//
// dirtyBefore is MigrateUp's set of committable tables already dirty when the
// pass started. Writing into one would entangle migration output with
// uncommitted user data and trip changedDirtyTableSignatures after the marker
// was recorded, so the pass refuses at plan time instead, exactly as the
// re-key does, whenever it would write a versioned row while a graph table
// (issues, dependencies) is dirty.
func repairLocalExternalDepTargets(ctx context.Context, db DBConn, dirtyBefore map[string]dirtyTableState) (bool, error) {
	// A schema without the typed target columns has nothing to repair, the
	// same skip the re-key takes for a missing id column.
	hasTargets, err := columnExists(ctx, db, "dependencies", "depends_on_external")
	if err != nil {
		return false, fmt.Errorf("dependencies: %w", err)
	}
	if !hasTargets {
		return false, nil
	}
	plan, conflicts, err := issueops.PlanLocalExternalDepRepairInTx(ctx, db)
	if err != nil {
		return false, err
	}
	for _, row := range conflicts {
		log.Printf("schema: GH#8299: %s: leaving %s edge %s -> %s external: as a local edge it would block an issue on its own ancestor or descendant",
			row.Table, row.Type, row.IssueID, row.Target)
	}
	scope := plan
	pending, err := ignoredSource.pendingVersions(ctx, db)
	if err != nil {
		return false, fmt.Errorf("reading pending ignored migrations: %w", err)
	}
	if slices.Contains(pending, depLocalTargetRepairMarker) {
		typed, err := issueops.CrossPrefixTypedDepsInTx(ctx, db)
		if err != nil {
			return false, err
		}
		scope = append(slices.Clip(scope), typed...)
	}
	if len(scope) == 0 {
		return false, nil
	}
	issueIDs, wispIDs, err := issueops.AffectedByDepRowsInTx(ctx, db, scope)
	if err != nil {
		return false, err
	}

	// The recompute derives versioned flags from the graph tables, so a pass
	// that writes either one refuses while either is dirty: it would sweep
	// unrelated edits into the migration commit or commit flags derived from
	// uncommitted edges (the full recompute's own guard, bd-6dnrw.37).
	writesVersioned := len(issueIDs) > 0 || slices.ContainsFunc(plan, func(row issueops.DepRow) bool {
		return row.Table == "dependencies"
	})
	var blocked []string
	for table := range dirtyBefore {
		if writesVersioned && issueops.IsBlockedRecomputeGraphTable(table) {
			blocked = append(blocked, table)
		}
	}
	if len(blocked) > 0 {
		slices.Sort(blocked)
		return false, &DirtyTablesError{Tables: blocked}
	}

	moved, err := issueops.ApplyLocalExternalDepRepairInTx(ctx, db, plan)
	if err != nil {
		return moved > 0, err
	}
	if depLocalTargetRepairFaultHook != nil {
		if err := depLocalTargetRepairFaultHook(ctx, db); err != nil {
			return moved > 0, err
		}
	}
	result, err := issueops.RecomputeIsBlockedInTxWithResult(ctx, db, issueIDs, wispIDs)
	if err != nil {
		return moved > 0, fmt.Errorf("recompute blocked state: %w", err)
	}
	if moved > 0 {
		log.Printf("schema: GH#8299: re-filed %d dependency row(s) whose target this database holds; recomputed blocked state for %d issue(s), %d wisp(s)",
			moved, len(issueIDs), len(wispIDs))
	}
	return moved > 0 || result.IssueRowsChanged || result.WispRowsChanged, nil
}

// depLocalTargetRepairFaultHook is a test-only seam. When non-nil it runs
// after repairLocalExternalDepTargets moves its rows and before it recomputes
// blocked state; a non-nil error aborts the pass there, to simulate an
// interruption.
var depLocalTargetRepairFaultHook func(ctx context.Context, db DBConn) error

// SetDepLocalTargetRepairFaultHookForTest installs fn as the local-target
// repair fault hook and returns a function that restores the previous hook.
// Test-only.
func SetDepLocalTargetRepairFaultHookForTest(fn func(ctx context.Context, db DBConn) error) func() {
	prev := depLocalTargetRepairFaultHook
	depLocalTargetRepairFaultHook = fn
	return func() { depLocalTargetRepairFaultHook = prev }
}
