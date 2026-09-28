package schema

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/steveyegge/beads/internal/storage/depid"
)

// depRekeyTables are the two edge tables the re-key covers, in execution order.
var depRekeyTables = []string{"dependencies", "wisp_dependencies"}

// depTargetColumns is the COALESCE order of the three typed target columns,
// which is also the survivor priority for a duplicated edge: an issue ref beats
// a wisp ref beats an external ref. Changing this order changes both the derived
// target and which twin survives, so it is deliberately the same list for both.
var depTargetColumns = []string{"depends_on_issue_id", "depends_on_wisp_id", "depends_on_external"}

// depDeleteChunk is how many ids one DELETE ... WHERE id IN (...) carries,
// matching issueops' deleteBatchSize. The victims of #5268 are precisely the
// databases with thousands of divergent rows, and this pass runs at open.
const depDeleteChunk = 50

// DependencyRekeyConflictError reports an edge table the re-key cannot converge
// without guessing: a row outside any duplicate group is squatting on an id
// another row must move to, or a set of rows form a rotation with no free id to
// move through. Both are corruption rather than the cross-column duplicate this
// pass merges, so the pass refuses before writing anything.
//
// It is typed because a repair pass must not brick reads: embeddeddolt's
// non-strict opens (read-only commands, working-set reconcile) tolerate it and
// continue, the same way they tolerate DirtyTablesError. Nothing is lost by
// continuing — the 0026 marker stays unrecorded, so the next open retries.
type DependencyRekeyConflictError struct {
	Table  string
	Detail string
}

func (e *DependencyRekeyConflictError) Error() string {
	return fmt.Sprintf("%s: %s; run `bd doctor` to inspect the table (gastownhall/beads#5268)",
		e.Table, e.Detail)
}

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
//
// gastownhall/beads#5268: depid keys on (issue_id, resolved target) and
// deliberately not on which typed column holds the target, but the unique keys
// are per-column (uk_dep_issue_target / uk_dep_wisp_target /
// uk_dep_external_target), so a legal table can hold the same logical edge
// twice — e.g. an April wisp→issue conversion that recorded the edge as
// external before the target existed as an issue, then again as an issue ref.
// Both rows derive the same id, so the old row-at-a-time rewrite aborted the
// second UPDATE with "duplicate primary key given" partway through the table.
// planDependencyRekey now merges those duplicates instead, and ignored migration
// 0026 is the completion marker that makes the pass run once on every existing
// clone — including one already at the latest main version that a pre-1.3.0
// binary left half-rekeyed — and re-run on the next open if it ever aborts.
//
// BOTH tables are planned before EITHER is executed. Planning is read-only, so a
// refusal on wisp_dependencies must not leave the synced dependencies table
// already rewritten: those writes would sit uncommitted (the pass aborts before
// MigrateUp's DOLT_COMMIT) and the recovery pass would then classify them as
// pre-existing dirty and skip them again.
//
// dirtyBefore is MigrateUp's set of committable tables that were already dirty
// when the pass started. Writing the re-key into one of them would entangle
// migration output with uncommitted user data — the hazard changedDirtyTableSignatures
// exists to catch — but that guard runs AFTER ignoredSource.migrate has durably
// recorded the 0026 marker, so it would fail the open once and never retry,
// leaving the heal to ride an unrelated user commit. Refusing here instead, at
// plan time, keeps the contract: nothing written, marker unrecorded, retried on
// the next open once the user commits or discards the working set.
func rekeyDependencyIDs(ctx context.Context, db DBConn, dirtyBefore map[string]dirtyTableState) (bool, error) {
	plans := make([]*depPlan, 0, len(depRekeyTables))
	for _, table := range depRekeyTables {
		plan, err := planDependencyTableRekey(ctx, db, table)
		if err != nil {
			return false, err
		}
		plans = append(plans, plan)
	}

	var blocked []string
	for _, plan := range plans {
		if _, dirty := dirtyBefore[plan.table]; dirty && plan.writes() {
			blocked = append(blocked, plan.table)
		}
	}
	if len(blocked) > 0 {
		return false, &DirtyTablesError{Tables: blocked}
	}

	wrote := false
	for _, plan := range plans {
		planWrote, err := plan.execute(ctx, db)
		wrote = wrote || planWrote
		if err != nil {
			return wrote, err
		}
	}
	return wrote, nil
}

// depRow is one scanned edge: its current primary key, the natural identity
// depid keys on, which typed column carried the target, and the type the merge
// would discard if this row loses.
type depRow struct {
	id          string
	issueID     string
	target      string
	depType     string
	targetCount int // non-null typed target columns; only 1 is a well-formed edge
	priority    int // index into depTargetColumns; lower wins the merge
}

// mergeable reports whether the row has exactly one typed target, i.e. whether
// its natural identity is unambiguous enough to group and re-key. Rows with none
// (ck_dep_one_target should prevent them) and rows with several (reachable by
// merging drifted schemas or hand-repair SQL) are both left exactly as they are,
// for `bd doctor` to surface rather than for this to guess about. Guessing is
// what would be destructive now that a group can delete rows: truncating a
// multi-target row to its first non-null column could drop it as a "duplicate"
// and silently destroy the edge its other column recorded.
func (r depRow) mergeable() bool { return r.targetCount == 1 }

// describe renders the row for a diagnostic, without inventing a target the row
// does not have.
func (r depRow) describe() string {
	switch r.targetCount {
	case 0:
		return fmt.Sprintf("row %s (issue %s, no dependency target)", r.id, r.issueID)
	case 1:
		return fmt.Sprintf("row %s (issue %s -> %s)", r.id, r.issueID, r.target)
	default:
		return fmt.Sprintf("row %s (issue %s, %d dependency targets)", r.id, r.issueID, r.targetCount)
	}
}

type depRekey struct{ oldID, newID string }

// depMerge records one duplicate collapse, for the log line that keeps the
// dropped row auditable.
type depMerge struct {
	issueID, target string
	survivor, loser depRow
}

// depPlan is the fully validated statement plan for one edge table. deletes and
// updates are both in execution order, and executing them in that order is the
// only ordering that converges (see planDependencyRekey).
type depPlan struct {
	table   string
	deletes []string
	updates []depRekey
	merges  []depMerge
}

func (p *depPlan) writes() bool { return len(p.deletes)+len(p.updates) > 0 }

// execute applies an already-validated plan. Every statement is a final-state
// mutation, so a partially applied plan is benign: the rows it touched are
// already correct, the rest are untouched, and the next pass finishes the job.
//
// Deliberately not wrapped in a SQL transaction. DBConn is satisfied by pooled
// handles as well as pinned connections (lock.go and embeddeddolt/store.go feed
// it both), and BEGIN/COMMIT issued through ExecContext on a pooled handle can
// land on different sessions — which would silently drop the guarantee it was
// added for. Convergence, not atomicity, is what makes partial application safe
// here; chunking the deletes is what keeps the cost down.
func (p *depPlan) execute(ctx context.Context, db DBConn) (bool, error) {
	for _, m := range p.merges {
		log.Printf("schema: %s: merging duplicate edge %s -> %s: keeping row %s (type %q), dropping row %s (type %q)",
			p.table, m.issueID, m.target, m.survivor.id, m.survivor.depType, m.loser.id, m.loser.depType)
	}
	for start := 0; start < len(p.deletes); start += depDeleteChunk {
		end := start + depDeleteChunk
		if end > len(p.deletes) {
			end = len(p.deletes)
		}
		chunk := p.deletes[start:end]
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		//nolint:gosec // G201: table is a hardcoded constant and the placeholders are generated, never user input.
		if _, err := db.ExecContext(ctx,
			fmt.Sprintf(`DELETE FROM %s WHERE id IN (%s)`, p.table, placeholders), args...); err != nil {
			return true, fmt.Errorf("%s: drop duplicate edge rows: %w", p.table, err)
		}
	}
	for _, u := range p.updates {
		//nolint:gosec // G201: table is a hardcoded constant, never user input.
		if _, err := db.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET id = ? WHERE id = ?`, p.table),
			u.newID, u.oldID); err != nil {
			return true, fmt.Errorf("%s: re-key id %s -> %s: %w", p.table, u.oldID, u.newID, err)
		}
	}
	return p.writes(), nil
}

// planDependencyTableRekey scans one edge table and returns its validated plan.
// It is read-only: nothing is written by planning, which is what lets both
// tables be planned before either is executed. table must be a hardcoded
// constant ("dependencies" or "wisp_dependencies").
func planDependencyTableRekey(ctx context.Context, db DBConn, table string) (*depPlan, error) {
	// Skip cleanly if the table or its id column isn't present (e.g. an older or
	// partial schema where the surrogate key was never added): nothing to re-key.
	hasID, err := columnExists(ctx, db, table, "id")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", table, err)
	}
	if !hasID {
		return &depPlan{table: table}, nil
	}
	rows, err := scanDependencyRows(ctx, db, table)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", table, err)
	}
	return planDependencyRekey(table, rows)
}

// scanDependencyRows reads every edge row with its typed target columns exposed
// rather than COALESCEd away: the merge needs to know which column held the
// target, not just the resolved value. Resolution order is unchanged — the first
// non-null in depTargetColumns is exactly what the old COALESCE returned.
func scanDependencyRows(ctx context.Context, db DBConn, table string) ([]depRow, error) {
	//nolint:gosec // G201: table and the column list are hardcoded constants, never user input.
	rows, err := db.QueryContext(ctx, fmt.Sprintf(
		`SELECT id, issue_id, %s, type FROM %s`, strings.Join(depTargetColumns, ", "), table))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []depRow
	for rows.Next() {
		var id, issueID string
		var depType sql.NullString
		targets := make([]sql.NullString, len(depTargetColumns))
		dest := []any{&id, &issueID}
		for i := range targets {
			dest = append(dest, &targets[i])
		}
		dest = append(dest, &depType)
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		row := depRow{id: id, issueID: issueID, depType: depType.String, priority: len(depTargetColumns)}
		for i, t := range targets {
			if !t.Valid {
				continue
			}
			row.targetCount++
			if row.targetCount == 1 {
				row.target, row.priority = t.String, i
			}
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// planDependencyRekey turns the scanned table into an ordered, validated plan,
// or a DependencyRekeyConflictError if the table cannot be converged without
// guessing. It is a pure function of the scanned rows — group keys and
// statements are emitted in sorted order, never map-iteration order — so two
// clones with the same data produce the same plan and converge to byte-identical
// tables (#4259).
//
// Rows are grouped by their derived id. A group with more than one member is the
// same logical edge recorded in two or three typed columns, which is invalid
// state the per-column unique keys cannot catch. The merge keeps the
// highest-priority typed row (issue > wisp > external, mirroring the COALESCE
// order) and deletes the rest; the loser's type and audit columns go with it,
// since edge identity deliberately excludes them and the resolution has to be a
// pure function of table content. Nothing references dependencies.id, so
// dropping the row is referentially safe.
//
// Ordering has two rules, and both are load-bearing:
//
//   - DELETEs run before UPDATEs, because a loser may currently hold exactly the
//     id its survivor is moving to (that is what a half-re-keyed database looks
//     like), and updating first would manufacture the very duplicate-primary-key
//     abort this replaces.
//   - UPDATEs run in topological order. A row can legitimately hold the id
//     another row needs while itself needing to move — `bd rename A B` followed
//     by `bd rename C A` produces exactly that chain — so the occupant is moved
//     out of the way first rather than refused. Each id is wanted by at most one
//     row and each row vacates at most one id, so the graph is a set of simple
//     chains and rotations: chains are walked from their free end, and only a
//     true rotation (no free id to move through) is refused.
func planDependencyRekey(table string, rows []depRow) (*depPlan, error) {
	groups := make(map[string][]depRow)
	byCurrentID := make(map[string]depRow, len(rows))
	for _, r := range rows {
		byCurrentID[r.id] = r
		if !r.mergeable() {
			// Malformed row: not part of any edge group, but it still occupies a
			// primary key, so it stays in byCurrentID for the squat check below.
			continue
		}
		want := depid.New(r.issueID, r.target)
		groups[want] = append(groups[want], r)
	}

	wantIDs := make([]string, 0, len(groups))
	for want := range groups {
		wantIDs = append(wantIDs, want)
	}
	sort.Strings(wantIDs)

	plan := &depPlan{table: table}
	doomed := make(map[string]bool)
	var candidates []depRekey
	for _, want := range wantIDs {
		group := groups[want]
		// Highest-priority typed column survives. The id tiebreak is unreachable
		// while the uk_dep_* unique keys hold (they forbid two rows with the same
		// issue_id and the same typed target) but keeps the plan deterministic on
		// a database that lost one.
		sort.Slice(group, func(i, j int) bool {
			if group[i].priority != group[j].priority {
				return group[i].priority < group[j].priority
			}
			return group[i].id < group[j].id
		})
		survivor := group[0]
		for _, loser := range group[1:] {
			doomed[loser.id] = true
			plan.merges = append(plan.merges, depMerge{
				issueID: survivor.issueID, target: survivor.target,
				survivor: survivor, loser: loser,
			})
		}
		if survivor.id != want {
			candidates = append(candidates, depRekey{oldID: survivor.id, newID: want})
		}
	}

	// doomed is the single source of truth for what is being deleted; the
	// statement list is derived from it so the two can never disagree.
	plan.deletes = make([]string, 0, len(doomed))
	for id := range doomed {
		plan.deletes = append(plan.deletes, id)
	}
	sort.Strings(plan.deletes)

	ordered, err := orderDependencyRekeys(table, candidates, byCurrentID, doomed)
	if err != nil {
		return nil, err
	}
	plan.updates = ordered
	return plan, nil
}

// orderDependencyRekeys sequences the re-key UPDATEs so each one runs only once
// the id it is moving to is free, or refuses when no such sequence exists.
//
// candidates arrives sorted by newID, which is what makes the emitted order a
// pure function of table content. Every id is wanted by at most one candidate
// (group keys are unique) and every candidate vacates at most one id (its row's
// current key is unique), so "must run before" is a graph of in- and out-degree
// at most one: disjoint chains and rotations. Walking each chain from its free
// end covers every reachable node; anything left over is a rotation.
func orderDependencyRekeys(table string, candidates []depRekey, byCurrentID map[string]depRow, doomed map[string]bool) ([]depRekey, error) {
	byOldID := make(map[string]int, len(candidates))
	for i, c := range candidates {
		byOldID[c.oldID] = i
	}

	const none = -1
	blockedBy := make([]int, len(candidates))
	unblocks := make([]int, len(candidates))
	for i := range candidates {
		blockedBy[i], unblocks[i] = none, none
	}
	for i, c := range candidates {
		occupant, held := byCurrentID[c.newID]
		if !held || doomed[occupant.id] {
			continue // the id is free, or a DELETE frees it first
		}
		vacating, planned := byOldID[occupant.id]
		if !planned {
			// The occupant is staying exactly where it is, so the id will never
			// be free. A converged row cannot land here — it would derive the
			// same id and therefore be in the same group — so the occupant is a
			// malformed row squatting on another edge's key: corruption, not a
			// duplicate this can merge.
			return nil, &DependencyRekeyConflictError{
				Table: table,
				Detail: fmt.Sprintf("cannot re-key %s to %s: that id is already held by %s, which is neither a duplicate of it nor moving",
					byCurrentID[c.oldID].describe(), c.newID, occupant.describe()),
			}
		}
		blockedBy[i] = vacating
		unblocks[vacating] = i
	}

	ordered := make([]depRekey, 0, len(candidates))
	emitted := make([]bool, len(candidates))
	for i := range candidates {
		if blockedBy[i] != none {
			continue // not the head of a chain
		}
		for j := i; j != none && !emitted[j]; j = unblocks[j] {
			emitted[j] = true
			ordered = append(ordered, candidates[j])
		}
	}
	if len(ordered) != len(candidates) {
		var stuck []string
		for i, done := range emitted {
			if !done {
				stuck = append(stuck, byCurrentID[candidates[i].oldID].describe())
			}
		}
		sort.Strings(stuck)
		return nil, &DependencyRekeyConflictError{
			Table: table,
			Detail: "cannot re-key a cycle of rows that each hold the id another one needs, with no free id to move through: " +
				strings.Join(stuck, "; "),
		}
	}
	return ordered, nil
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
