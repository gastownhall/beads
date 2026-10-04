package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// DBTX is the minimal statement-execution surface the blocked-state
// recompute needs. *sql.Tx satisfies it (the classic embedded path) and so
// does the domain/db Runner (the server/proxied path): is_blocked is derived
// state shared by both stacks, so they must derive it with the same code
// (bd-6dnrw.44 item 3).
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

const waitsForGateBlockedSQL = `
		(
		  (
		    EXISTS (
		      SELECT 1 FROM dependencies cd JOIN issues child ON child.id = cd.issue_id
		      WHERE cd.type = 'parent-child'
		        AND ((d.depends_on_issue_id IS NOT NULL AND cd.depends_on_issue_id = d.depends_on_issue_id)
		          OR (d.depends_on_wisp_id IS NOT NULL AND cd.depends_on_wisp_id = d.depends_on_wisp_id))
		        AND child.status <> 'closed' AND child.status <> 'pinned'
		    )
		    OR EXISTS (
		      SELECT 1 FROM wisp_dependencies cd JOIN wisps child ON child.id = cd.issue_id
		      WHERE cd.type = 'parent-child'
		        AND ((d.depends_on_issue_id IS NOT NULL AND cd.depends_on_issue_id = d.depends_on_issue_id)
		          OR (d.depends_on_wisp_id IS NOT NULL AND cd.depends_on_wisp_id = d.depends_on_wisp_id))
		        AND child.status <> 'closed' AND child.status <> 'pinned'
		    )
		  )
		  AND NOT (
		    -- COALESCE: metadata without a gate key (legacy '{}' rows) means the
		    -- all-children default; a NULL here would poison the AND/NOT chain
		    -- and unblock the gate as soon as any child closes.
		    COALESCE(JSON_UNQUOTE(JSON_EXTRACT(d.metadata, '$.gate')), 'all-children') = 'any-children'
		    AND (
		      EXISTS (
		        SELECT 1 FROM dependencies cd JOIN issues child ON child.id = cd.issue_id
		        WHERE cd.type = 'parent-child'
		          AND ((d.depends_on_issue_id IS NOT NULL AND cd.depends_on_issue_id = d.depends_on_issue_id)
		            OR (d.depends_on_wisp_id IS NOT NULL AND cd.depends_on_wisp_id = d.depends_on_wisp_id))
		          AND child.status = 'closed'
		      )
		      OR EXISTS (
		        SELECT 1 FROM wisp_dependencies cd JOIN wisps child ON child.id = cd.issue_id
		        WHERE cd.type = 'parent-child'
		          AND ((d.depends_on_issue_id IS NOT NULL AND cd.depends_on_issue_id = d.depends_on_issue_id)
		            OR (d.depends_on_wisp_id IS NOT NULL AND cd.depends_on_wisp_id = d.depends_on_wisp_id))
		          AND child.status = 'closed'
		      )
		    )
		  )
		)
		OR (
		  -- also_blocks (GH#3783/GH#3875): a waits-for edge collapsed from a
		  -- redundant needs/depends_on blocks edge onto this same spawner
		  -- (cmd/bd/cook.go collectDependencies) additionally carries classic
		  -- blocking semantics — it must block while the spawner itself is
		  -- open, not only while the spawner has an open parent-child child.
		  -- This closes the pre-fanout window where the waiter could become
		  -- ready before the spawner (and its fanout) ever completed. Legacy
		  -- rows and plain (non-collapsed) waits-for edges lack the
		  -- also_blocks key, so COALESCE defaults to 'false' and this branch
		  -- is a no-op for them (zero behavior change).
		  --
		  -- This is a top-level OR, deliberately outside (and overriding) the
		  -- any-children early-open carve-out above: a collapsed edge means
		  -- the caller's needs/depends_on required the spawner itself to
		  -- close, so an early-open child close must NOT unblock the waiter
		  -- while the spawner remains open.
		  COALESCE(JSON_UNQUOTE(JSON_EXTRACT(d.metadata, '$.also_blocks')), 'false') = 'true'
		  AND (
		    EXISTS (
		      SELECT 1 FROM issues sp
		      WHERE sp.id = d.depends_on_issue_id
		        AND sp.status <> 'closed' AND sp.status <> 'pinned'
		    )
		    OR EXISTS (
		      SELECT 1 FROM wisps sp
		      WHERE sp.id = d.depends_on_wisp_id
		        AND sp.status <> 'closed' AND sp.status <> 'pinned'
		    )
		  )
		)
`

// RecomputeIsBlockedResult reports which issue tables had rows changed while
// the blocked-state fixpoint converged.
type RecomputeIsBlockedResult struct {
	IssueRowsChanged bool
	WispRowsChanged  bool
}

// RecomputeIsBlockedInTx recomputes blocked state and discards the per-table
// change result retained by RecomputeIsBlockedInTxWithResult.
func RecomputeIsBlockedInTx(ctx context.Context, tx DBTX, issueIDs, wispIDs []string) error {
	_, err := RecomputeIsBlockedInTxWithResult(ctx, tx, issueIDs, wispIDs)
	return err
}

// RecomputeIsBlockedInTxWithResult recomputes blocked state to a fixpoint and
// reports whether an UPDATE changed rows in each issue table.
func RecomputeIsBlockedInTxWithResult(
	ctx context.Context, tx DBTX, issueIDs, wispIDs []string,
) (RecomputeIsBlockedResult, error) {
	return recomputeIsBlockedInTxWithResult(ctx, tx, issueIDs, wispIDs, false)
}

// recomputeIsBlockedInTxWithResult is RecomputeIsBlockedInTxWithResult with
// the caller's say on the no-edge shortcut (planRecomputeInTx): a create
// passes splitEdgeless, because its ids are mostly fresh rows without edges;
// every other caller leaves it to the chunk size.
func recomputeIsBlockedInTxWithResult(
	ctx context.Context, tx DBTX, issueIDs, wispIDs []string, splitEdgeless bool,
) (RecomputeIsBlockedResult, error) {
	var result RecomputeIsBlockedResult
	if len(issueIDs) == 0 && len(wispIDs) == 0 {
		return result, nil
	}
	before, err := captureBlockedJournalSnapshot(ctx, tx, issueIDs, wispIDs)
	if err != nil {
		return result, err
	}
	issuePlan, err := planRecomputeInTx(ctx, tx, "issues", "dependencies",
		markBlockedTemplateForIssues(), unmarkBlockedTemplateForIssues(), issueIDs, splitEdgeless)
	if err != nil {
		return result, err
	}
	wispPlan, err := planRecomputeInTx(ctx, tx, "wisps", "wisp_dependencies",
		markBlockedTemplateForWisps(), unmarkBlockedTemplateForWisps(), wispIDs, splitEdgeless)
	if err != nil {
		return result, err
	}
	for pass := 0; ; pass++ {
		var changed int64

		n, err := issuePlan.runPassInTx(ctx, tx, pass == 0)
		if err != nil {
			return result, err
		}
		changed += n
		result.IssueRowsChanged = result.IssueRowsChanged || n > 0

		n, err = wispPlan.runPassInTx(ctx, tx, pass == 0)
		if err != nil {
			return result, err
		}
		changed += n
		result.WispRowsChanged = result.WispRowsChanged || n > 0

		if changed == 0 {
			return result, recordBlockedJournalChanges(ctx, tx, before, issueIDs, wispIDs)
		}
	}
}

func MarkIsBlockedInTx(ctx context.Context, tx DBTX, issueIDs, wispIDs []string) error {
	if len(issueIDs) == 0 && len(wispIDs) == 0 {
		return nil
	}
	before, err := captureBlockedJournalSnapshot(ctx, tx, issueIDs, wispIDs)
	if err != nil {
		return err
	}
	for {
		var changed int64

		n, err := markIsBlockedPassForIssuesInTx(ctx, tx, issueIDs)
		if err != nil {
			return err
		}
		changed += n

		n, err = markIsBlockedPassForWispsInTx(ctx, tx, wispIDs)
		if err != nil {
			return err
		}
		changed += n

		if changed == 0 {
			return recordBlockedJournalChanges(ctx, tx, before, issueIDs, wispIDs)
		}
	}
}

func RecomputeIsBlockedForIDsInTx(ctx context.Context, tx DBTX, ids []string) error {
	return RecomputeIsBlockedInTx(ctx, tx, ids, nil)
}

func RecomputeIsBlockedForWispIDsInTx(ctx context.Context, tx DBTX, ids []string) error {
	return RecomputeIsBlockedInTx(ctx, tx, nil, ids)
}

func markIsBlockedPassForIssuesInTx(ctx context.Context, tx DBTX, ids []string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	return runMarkBatchedInTx(ctx, tx, markBlockedTemplateForIssues(), ids)
}

// The mark/unmark templates explicitly assign updated_at to itself:
// issues.updated_at (and wisps.updated_at) carry ON UPDATE CURRENT_TIMESTAMP,
// and is_blocked is DERIVED state - letting a recompute bump updated_at
// plants per-clone wall clock in a synced table (merge conflicts between
// clones that recomputed the same flip at different times, bd-578h9.19) and
// makes stale-guard/conflict-guard consumers treat the row as user-edited.
// An explicit assignment suppresses the ON UPDATE clause.
//
// Both templates decide membership through shouldBeBlockedIDsUnionScopedSQL,
// the same uncorrelated union the full repair and the doctor count use
// (blocked_consistency.go), scoped to the batch: one derived blocked set per
// batch, computed once and probed by hash. The previous shape — five
// correlated EXISTS per outer row — was re-executed per row by the engine:
// ~4 s per 200-id batch on committed, indexed data, and unbounded (>69 min
// observed on dolt 2.1.8) over a large uncommitted working set, where every
// probe re-read the uncommitted overlay (gastownhall/beads#6288).
//
// The batch IN-list therefore appears more than once per statement — the
// outer row filter plus one per union leg; expandBatchTemplate repeats the
// placeholders and the bound ids to match.

// batchScopeSQL is the per-leg predicate that confines the should-be-blocked
// union to the batch (see shouldBeBlockedIDsUnionScopedSQL); its %s is filled
// with the batch placeholders by expandBatchTemplate, never by fmt here.
const batchScopeSQL = "AND d.issue_id IN (%s)"

func markBlockedTemplateForIssues() string {
	return markBlockedTemplate("issues", "i", "dependencies")
}

func unmarkBlockedTemplateForIssues() string {
	return unmarkBlockedTemplate("issues", "i", "dependencies")
}

func markBlockedTemplateForWisps() string {
	return markBlockedTemplate("wisps", "w", "wisp_dependencies")
}

func unmarkBlockedTemplateForWisps() string {
	return unmarkBlockedTemplate("wisps", "w", "wisp_dependencies")
}

// markBlockedTemplate is the batched mark statement for one table: the
// batch-scoped analog of markAllBlockedSQL. The union is confined to the
// batch's own dependency rows, so `<alias>.id IN (union)` is exactly the
// old correlated disjunction for every id in the batch.
//
//nolint:gosec // G201: table, alias, and depTable are constants from the four callers above.
func markBlockedTemplate(table, alias, depTable string) string {
	return fmt.Sprintf(`
		UPDATE %[1]s %[2]s SET %[2]s.is_blocked = 1, %[2]s.updated_at = %[2]s.updated_at
		WHERE %[2]s.id IN (%%s)
		  AND %[2]s.is_blocked = 0
		  AND %[2]s.status <> 'closed' AND %[2]s.status <> 'pinned'
		  AND %[2]s.id IN (%[3]s)
	`, table, alias, shouldBeBlockedIDsUnionScopedSQL(depTable, batchScopeSQL))
}

// unmarkBlockedTemplate is the batched unmark statement for one table: the
// batch-scoped analog of unmarkAllBlockedSQL. NOT IN is null-hostile; the
// union's d.issue_id IS NOT NULL guards keep it total.
//
//nolint:gosec // G201: table, alias, and depTable are constants from the four callers above.
func unmarkBlockedTemplate(table, alias, depTable string) string {
	return fmt.Sprintf(`
		UPDATE %[1]s %[2]s SET %[2]s.is_blocked = 0, %[2]s.updated_at = %[2]s.updated_at
		WHERE %[2]s.id IN (%%s)
		  AND %[2]s.is_blocked = 1
		  AND ( %[2]s.status = 'closed' OR %[2]s.status = 'pinned'
		        OR %[2]s.id NOT IN (%[3]s) )
	`, table, alias, shouldBeBlockedIDsUnionScopedSQL(depTable, batchScopeSQL))
}

// expandBatchTemplate fills every %s in a batched template with the same
// IN-list placeholders and repeats the bound ids once per occurrence, in
// order. Templates carry the batch list in the outer row filter and in each
// leg of the scoped union (six occurrences today); a template with a single
// %s degrades to the plain Sprintf it always was. The count is textual, so a
// template must contain no other percent sign (no LIKE 'x%' pattern, no %%)
// and at least one %s — a template with none is a programmer error that
// surfaces as an %!(EXTRA …) syntax error at exec.
//
//nolint:gosec // G201: tmpl is a constant template; only IN-clause placeholders are formatted in.
func expandBatchTemplate(tmpl, placeholders string, args []interface{}) (string, []interface{}) {
	n := strings.Count(tmpl, "%s")
	if n <= 1 {
		return fmt.Sprintf(tmpl, placeholders), args
	}
	fills := make([]interface{}, n)
	expanded := make([]interface{}, 0, n*len(args))
	for k := range fills {
		fills[k] = placeholders
		expanded = append(expanded, args...)
	}
	return fmt.Sprintf(tmpl, fills...), expanded
}

func markIsBlockedPassForWispsInTx(ctx context.Context, tx DBTX, ids []string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	return runMarkBatchedInTx(ctx, tx, markBlockedTemplateForWisps(), ids)
}

func runMarkUnmarkBatchedInTx(ctx context.Context, tx DBTX, markTmpl, unmarkTmpl string, ids []string) (int64, error) {
	var changed int64
	for start := 0; start < len(ids); start += queryBatchSize {
		end := start + queryBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		placeholders, args := buildSQLInClause(ids[start:end])

		stmt, stmtArgs := expandBatchTemplate(markTmpl, placeholders, args)
		res, err := tx.ExecContext(ctx, stmt, stmtArgs...)
		if err != nil {
			return changed, fmt.Errorf("recompute is_blocked (mark): %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return changed, fmt.Errorf("recompute is_blocked (mark rows affected): %w", err)
		}
		changed += n

		stmt, stmtArgs = expandBatchTemplate(unmarkTmpl, placeholders, args)
		res, err = tx.ExecContext(ctx, stmt, stmtArgs...)
		if err != nil {
			return changed, fmt.Errorf("recompute is_blocked (unmark): %w", err)
		}
		n, err = res.RowsAffected()
		if err != nil {
			return changed, fmt.Errorf("recompute is_blocked (unmark rows affected): %w", err)
		}
		changed += n
	}
	return changed, nil
}

// recomputeSplitMinChunk is the chunk size from which planRecomputeInTx
// probes for edgeless ids without being asked to.
const recomputeSplitMinChunk = 32

// recomputeChunk is one queryBatchSize chunk of a recompute's ids, split by
// whether each id has a dependency row of its own (see planRecomputeInTx).
type recomputeChunk struct {
	withEdges, edgeless []string
}

// recomputePlan is a recompute's chunks for one table, split once per
// RecomputeIsBlockedInTxWithResult call: the fixpoint loop writes only
// is_blocked, never dependency rows, so the split holds for every pass.
type recomputePlan struct {
	table, markTmpl, unmarkTmpl string
	chunks                      []recomputeChunk
}

// planRecomputeInTx chunks ids exactly as runMarkUnmarkBatchedInTx would and
// splits each chunk by whether depTable holds a row with that id as its
// issue_id.
//
// Every leg of the should-be-blocked union the mark/unmark templates probe is
// scoped to `d.issue_id IN (batch)` on depTable
// (shouldBeBlockedIDsUnionScopedSQL), so an id with no depTable row of its own
// is never in that union: mark cannot select it, and unmark reduces to
// "is_blocked = 1 -> 0". Those ids — every freshly created issue without
// edges, which is most of what a large create or apply-batch recomputes —
// need not ride the two union statements, whose cost grows with the table.
//
// The split costs one probe per chunk, which only pays when the chunk holds
// edgeless ids worth skipping. So it runs when the caller asks (splitEdgeless:
// the create paths) or when the chunk is large; the single-issue recomputes of
// close, reopen, update and dependency edits keep exactly the statements they
// ran before the shortcut existed.
func planRecomputeInTx(ctx context.Context, tx DBTX, table, depTable, markTmpl, unmarkTmpl string, ids []string, splitEdgeless bool) (recomputePlan, error) {
	plan := recomputePlan{table: table, markTmpl: markTmpl, unmarkTmpl: unmarkTmpl}
	for start := 0; start < len(ids); start += queryBatchSize {
		end := min(start+queryBatchSize, len(ids))
		chunk := recomputeChunk{withEdges: ids[start:end]}
		if !createFastPathsDisabled.Load() && (splitEdgeless || end-start >= recomputeSplitMinChunk) {
			var err error
			chunk.withEdges, chunk.edgeless, err = splitByOwnDependencyRowsInTx(ctx, tx, depTable, ids[start:end])
			if err != nil {
				return plan, err
			}
		}
		plan.chunks = append(plan.chunks, chunk)
	}
	return plan, nil
}

// runPassInTx is one fixpoint pass over the plan, statement-for-statement
// equivalent to runMarkUnmarkBatchedInTx over the plan's full ids. Per chunk:
// the mark and unmark templates run over the ids with edges — the union, scoped
// to them, holds exactly what it held scoped to the whole chunk — and then, on
// the first pass only, a plain unmark clears the edgeless ids. It runs after
// both templates so their unions see the edgeless rows' pre-pass is_blocked,
// as they did when those rows were cleared by the chunk's own unmark
// statement; on later passes it would match no row (nothing marks an edgeless
// id), so it is skipped.
func (p recomputePlan) runPassInTx(ctx context.Context, tx DBTX, firstPass bool) (int64, error) {
	var changed int64
	for _, chunk := range p.chunks {
		if len(chunk.withEdges) > 0 {
			n, err := runMarkUnmarkBatchedInTx(ctx, tx, p.markTmpl, p.unmarkTmpl, chunk.withEdges)
			changed += n
			if err != nil {
				return changed, err
			}
		}
		if !firstPass || len(chunk.edgeless) == 0 {
			continue
		}
		placeholders, args := buildSQLInClause(chunk.edgeless)
		//nolint:gosec // G201: table is one of two constants; only placeholders are formatted in.
		res, err := tx.ExecContext(ctx, fmt.Sprintf(`
			UPDATE %[1]s t SET t.is_blocked = 0, t.updated_at = t.updated_at
			WHERE t.id IN (%[2]s) AND t.is_blocked = 1
		`, p.table, placeholders), args...)
		if err != nil {
			return changed, fmt.Errorf("recompute is_blocked (unmark): %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return changed, fmt.Errorf("recompute is_blocked (unmark rows affected): %w", err)
		}
		changed += n
	}
	return changed, nil
}

// splitByOwnDependencyRowsInTx partitions ids by whether depTable holds at
// least one row with that id as its issue_id, preserving input order.
//
//nolint:gosec // G201: depTable is one of two constants; only placeholders are formatted in.
func splitByOwnDependencyRowsInTx(ctx context.Context, tx DBTX, depTable string, ids []string) (withEdges, withoutEdges []string, err error) {
	placeholders, args := buildSQLInClause(ids)
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(
		"SELECT DISTINCT issue_id FROM %s WHERE issue_id IN (%s)", depTable, placeholders), args...)
	if err != nil {
		return nil, nil, fmt.Errorf("recompute is_blocked: probe %s: %w", depTable, err)
	}
	has := make(map[string]bool, len(ids))
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, nil, fmt.Errorf("recompute is_blocked: probe %s: %w", depTable, err)
		}
		has[id] = true
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("recompute is_blocked: probe %s: %w", depTable, err)
	}
	for _, id := range ids {
		if has[id] {
			withEdges = append(withEdges, id)
		} else {
			withoutEdges = append(withoutEdges, id)
		}
	}
	return withEdges, withoutEdges, nil
}

func runMarkBatchedInTx(ctx context.Context, tx DBTX, markTmpl string, ids []string) (int64, error) {
	var changed int64
	for start := 0; start < len(ids); start += queryBatchSize {
		end := start + queryBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		placeholders, args := buildSQLInClause(ids[start:end])

		stmt, stmtArgs := expandBatchTemplate(markTmpl, placeholders, args)
		res, err := tx.ExecContext(ctx, stmt, stmtArgs...)
		if err != nil {
			return changed, fmt.Errorf("mark is_blocked: %w", err)
		}
		n, _ := res.RowsAffected()
		changed += n
	}
	return changed, nil
}

func AffectedByStatusChangeInTx(ctx context.Context, tx DBTX, id string) ([]string, []string, error) {
	issueSeed := []string{id}
	issueSeen := map[string]bool{id: true}
	var wispSeed []string
	wispSeen := make(map[string]bool)

	if err := loadBlockingDependersInTx(ctx, tx, "depends_on_issue_id", id, &issueSeed, issueSeen, &wispSeed, wispSeen); err != nil {
		return nil, nil, err
	}
	if err := loadWaitersWhoseSpawnerIsParentOfInTx(ctx, tx, id, false, &issueSeed, issueSeen, &wispSeed, wispSeen); err != nil {
		return nil, nil, err
	}
	// id's own status just changed, and an also_blocks waits-for edge blocks
	// while the spawner itself is open (pre-fanout window, GH#3783/GH#3875).
	// A waiter with only a DepWaitsFor edge on id (no DepBlocks edge — the
	// blocking semantics were collapsed into also_blocks) would otherwise
	// never get recomputed when its spawner closes.
	if err := loadWaitersOnSpawnerIDsInTx(ctx, tx, []string{id}, &issueSeed, issueSeen, &wispSeed, wispSeen); err != nil {
		return nil, nil, err
	}
	return expandByParentChildDescendantsInTx(ctx, tx, issueSeed, wispSeed, issueSeen, wispSeen)
}

func AffectedByStatusChangeForWispInTx(ctx context.Context, tx DBTX, id string) ([]string, []string, error) {
	var issueSeed []string
	issueSeen := make(map[string]bool)
	wispSeed := []string{id}
	wispSeen := map[string]bool{id: true}

	if err := loadBlockingDependersInTx(ctx, tx, "depends_on_wisp_id", id, &issueSeed, issueSeen, &wispSeed, wispSeen); err != nil {
		return nil, nil, err
	}
	if err := loadWaitersWhoseSpawnerIsParentOfInTx(ctx, tx, id, true, &issueSeed, issueSeen, &wispSeed, wispSeen); err != nil {
		return nil, nil, err
	}
	// See the issue-id sibling above: id's own status just changed, and a
	// waiter that waits directly on this wisp id as spawner needs to be
	// recomputed too.
	if err := loadWaitersOnSpawnerIDsInTx(ctx, tx, []string{id}, &issueSeed, issueSeen, &wispSeed, wispSeen); err != nil {
		return nil, nil, err
	}
	return expandByParentChildDescendantsInTx(ctx, tx, issueSeed, wispSeed, issueSeen, wispSeen)
}

func AffectedByDepChangeInTx(ctx context.Context, tx DBTX, source, target string, depType types.DependencyType) ([]string, []string, error) {
	switch depType {
	case types.DepBlocks, types.DepConditionalBlocks, types.DepWaitsFor, types.DepParentChild:
		issueSeed := []string{source}
		issueSeen := map[string]bool{source: true}
		var wispSeed []string
		wispSeen := map[string]bool{}
		if depType == types.DepParentChild && target != "" {
			if err := loadWaitersOnSpawnerIDsInTx(ctx, tx, []string{target}, &issueSeed, issueSeen, &wispSeed, wispSeen); err != nil {
				return nil, nil, err
			}
		}
		return expandByParentChildDescendantsInTx(ctx, tx, issueSeed, wispSeed, issueSeen, wispSeen)
	default:
		return nil, nil, nil
	}
}

func AffectedByDepChangeForWispInTx(ctx context.Context, tx DBTX, source, target string, depType types.DependencyType) ([]string, []string, error) {
	switch depType {
	case types.DepBlocks, types.DepConditionalBlocks, types.DepWaitsFor, types.DepParentChild:
		var issueSeed []string
		issueSeen := map[string]bool{}
		wispSeed := []string{source}
		wispSeen := map[string]bool{source: true}
		if depType == types.DepParentChild && target != "" {
			if err := loadWaitersOnSpawnerIDsInTx(ctx, tx, []string{target}, &issueSeed, issueSeen, &wispSeed, wispSeen); err != nil {
				return nil, nil, err
			}
		}
		return expandByParentChildDescendantsInTx(ctx, tx, issueSeed, wispSeed, issueSeen, wispSeen)
	default:
		return nil, nil, nil
	}
}

func loadBlockingDependersInTx(
	ctx context.Context, tx DBTX,
	targetCol, id string,
	issueSeed *[]string, issueSeen map[string]bool,
	wispSeed *[]string, wispSeen map[string]bool,
) error {
	return loadBlockingDependersForIDsInTx(ctx, tx, targetCol, []string{id}, issueSeed, issueSeen, wispSeed, wispSeen)
}

//nolint:gosec // G201: targetCol is one of two constant column names.
func loadBlockingDependersForIDsInTx(
	ctx context.Context, tx DBTX,
	targetCol string, ids []string,
	issueSeed *[]string, issueSeen map[string]bool,
	wispSeed *[]string, wispSeen map[string]bool,
) error {
	if len(ids) == 0 {
		return nil
	}
	tables := []struct {
		table  string
		seed   *[]string
		seen   map[string]bool
		errCtx string
	}{
		{"dependencies", issueSeed, issueSeen, "load issue dependers"},
		{"wisp_dependencies", wispSeed, wispSeen, "load wisp dependers"},
	}
	for _, id := range ids {
		for _, t := range tables {
			query := fmt.Sprintf(`
				SELECT issue_id FROM %s
				WHERE %s = ?
				  AND (type = 'blocks' OR type = 'conditional-blocks')
			`, t.table, targetCol)
			rows, err := tx.QueryContext(ctx, query, id)
			if err != nil {
				return fmt.Errorf("%s: query: %w", t.errCtx, err)
			}
			for rows.Next() {
				var dependerID string
				if err := rows.Scan(&dependerID); err != nil {
					_ = rows.Close()
					return fmt.Errorf("%s: scan: %w", t.errCtx, err)
				}
				if !t.seen[dependerID] {
					t.seen[dependerID] = true
					*t.seed = append(*t.seed, dependerID)
				}
			}
			_ = rows.Close()
			if err := rows.Err(); err != nil {
				return fmt.Errorf("%s: rows: %w", t.errCtx, err)
			}
		}
	}
	return nil
}

func AffectedByDeletionInTx(
	ctx context.Context, tx DBTX,
	deletedIssues, deletedWisps []string,
) ([]string, []string, error) {
	if len(deletedIssues) == 0 && len(deletedWisps) == 0 {
		return nil, nil, nil
	}

	issueSeen := make(map[string]bool, len(deletedIssues))
	wispSeen := make(map[string]bool, len(deletedWisps))
	for _, id := range deletedIssues {
		issueSeen[id] = true
	}
	for _, id := range deletedWisps {
		wispSeen[id] = true
	}
	var issueSeed, wispSeed []string

	if err := loadBlockingDependersForIDsInTx(ctx, tx, "depends_on_issue_id", deletedIssues, &issueSeed, issueSeen, &wispSeed, wispSeen); err != nil {
		return nil, nil, err
	}
	if err := loadBlockingDependersForIDsInTx(ctx, tx, "depends_on_wisp_id", deletedWisps, &issueSeed, issueSeen, &wispSeed, wispSeen); err != nil {
		return nil, nil, err
	}

	if err := loadWaitersOnSpawnerIDsByColInTx(ctx, tx, "depends_on_issue_id", deletedIssues, &issueSeed, issueSeen, &wispSeed, wispSeen); err != nil {
		return nil, nil, err
	}
	if err := loadWaitersOnSpawnerIDsByColInTx(ctx, tx, "depends_on_wisp_id", deletedWisps, &issueSeed, issueSeen, &wispSeed, wispSeen); err != nil {
		return nil, nil, err
	}
	for _, id := range deletedIssues {
		if err := loadWaitersWhoseSpawnerIsParentOfInTx(ctx, tx, id, false, &issueSeed, issueSeen, &wispSeed, wispSeen); err != nil {
			return nil, nil, err
		}
	}
	for _, id := range deletedWisps {
		if err := loadWaitersWhoseSpawnerIsParentOfInTx(ctx, tx, id, true, &issueSeed, issueSeen, &wispSeed, wispSeen); err != nil {
			return nil, nil, err
		}
	}

	for _, w := range []struct {
		depTable, parentCol string
		parentIDs           []string
		seed                *[]string
		seen                map[string]bool
	}{
		{"dependencies", "depends_on_issue_id", deletedIssues, &issueSeed, issueSeen},
		{"wisp_dependencies", "depends_on_issue_id", deletedIssues, &wispSeed, wispSeen},
		{"dependencies", "depends_on_wisp_id", deletedWisps, &issueSeed, issueSeen},
		{"wisp_dependencies", "depends_on_wisp_id", deletedWisps, &wispSeed, wispSeen},
	} {
		if err := appendChildrenInTx(ctx, tx, w.depTable, w.parentCol, w.parentIDs, w.seen, w.seed); err != nil {
			return nil, nil, err
		}
	}

	return expandByParentChildDescendantsInTx(ctx, tx, issueSeed, wispSeed, issueSeen, wispSeen)
}

func expandByParentChildDescendantsInTx(
	ctx context.Context, tx DBTX,
	issueSeed, wispSeed []string,
	issueSeen, wispSeen map[string]bool,
) ([]string, []string, error) {
	issueQueue := issueSeed
	wispQueue := wispSeed
	issueHead, wispHead := 0, 0

	for issueHead < len(issueQueue) || wispHead < len(wispQueue) {
		if issueHead < len(issueQueue) {
			end := issueHead + queryBatchSize
			if end > len(issueQueue) {
				end = len(issueQueue)
			}
			batch := issueQueue[issueHead:end]
			issueHead = end

			if err := appendChildrenInTx(ctx, tx, "dependencies", "depends_on_issue_id", batch, issueSeen, &issueQueue); err != nil {
				return nil, nil, err
			}
			if err := appendChildrenInTx(ctx, tx, "wisp_dependencies", "depends_on_issue_id", batch, wispSeen, &wispQueue); err != nil {
				return nil, nil, err
			}
		}
		if wispHead < len(wispQueue) {
			end := wispHead + queryBatchSize
			if end > len(wispQueue) {
				end = len(wispQueue)
			}
			batch := wispQueue[wispHead:end]
			wispHead = end

			if err := appendChildrenInTx(ctx, tx, "dependencies", "depends_on_wisp_id", batch, issueSeen, &issueQueue); err != nil {
				return nil, nil, err
			}
			if err := appendChildrenInTx(ctx, tx, "wisp_dependencies", "depends_on_wisp_id", batch, wispSeen, &wispQueue); err != nil {
				return nil, nil, err
			}
		}
	}
	return issueQueue, wispQueue, nil
}

//nolint:gosec // G201: depTable and parentCol come from constant call sites.
func appendChildrenInTx(
	ctx context.Context, tx DBTX,
	depTable, parentCol string,
	parentIDs []string,
	seen map[string]bool, queue *[]string,
) error {
	if len(parentIDs) == 0 {
		return nil
	}
	// One IN-list read per chunk of parents rather than one read per parent.
	// The set of children found does not depend on how the parents are
	// grouped; only the order they join the queue can, and every caller
	// treats the queue as a set to recompute.
	for start := 0; start < len(parentIDs); start += queryBatchSize {
		end := min(start+queryBatchSize, len(parentIDs))
		placeholders, args := buildSQLInClause(parentIDs[start:end])
		query := fmt.Sprintf(`
			SELECT issue_id FROM %s
			WHERE type = 'parent-child'
			  AND %s IN (%s)
		`, depTable, parentCol, placeholders)
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("expand children from %s on %s: %w", depTable, parentCol, err)
		}
		for rows.Next() {
			var childID string
			if err := rows.Scan(&childID); err != nil {
				_ = rows.Close()
				return fmt.Errorf("expand children: scan: %w", err)
			}
			if !seen[childID] {
				seen[childID] = true
				*queue = append(*queue, childID)
			}
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("expand children: rows: %w", err)
		}
	}
	return nil
}

func loadWaitersWhoseSpawnerIsParentOfInTx(
	ctx context.Context, tx DBTX,
	childID string, childIsWisp bool,
	issueSeed *[]string, issueSeen map[string]bool,
	wispSeed *[]string, wispSeen map[string]bool,
) error {
	depTable := "dependencies"
	if childIsWisp {
		depTable = "wisp_dependencies"
	}
	//nolint:gosec // G201: depTable is one of two constant values.
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(`
		SELECT depends_on_issue_id, depends_on_wisp_id
		FROM %s
		WHERE issue_id = ? AND type = 'parent-child'
	`, depTable), childID)
	if err != nil {
		return fmt.Errorf("waiters on parent of %s: load parents: %w", childID, err)
	}
	var issueParentIDs, wispParentIDs []string
	for rows.Next() {
		var ip, wp sql.NullString
		if err := rows.Scan(&ip, &wp); err != nil {
			_ = rows.Close()
			return fmt.Errorf("waiters on parent of %s: scan: %w", childID, err)
		}
		if ip.Valid {
			issueParentIDs = append(issueParentIDs, ip.String)
		}
		if wp.Valid {
			wispParentIDs = append(wispParentIDs, wp.String)
		}
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("waiters on parent of %s: rows: %w", childID, err)
	}

	if len(issueParentIDs) > 0 {
		if err := loadWaitersOnSpawnerIDsByColInTx(ctx, tx, "depends_on_issue_id", issueParentIDs, issueSeed, issueSeen, wispSeed, wispSeen); err != nil {
			return err
		}
	}
	if len(wispParentIDs) > 0 {
		if err := loadWaitersOnSpawnerIDsByColInTx(ctx, tx, "depends_on_wisp_id", wispParentIDs, issueSeed, issueSeen, wispSeed, wispSeen); err != nil {
			return err
		}
	}
	return nil
}

func loadWaitersOnSpawnerIDsInTx(
	ctx context.Context, tx DBTX,
	spawnerIDs []string,
	issueSeed *[]string, issueSeen map[string]bool,
	wispSeed *[]string, wispSeen map[string]bool,
) error {
	if err := loadWaitersOnSpawnerIDsByColInTx(ctx, tx, "depends_on_issue_id", spawnerIDs, issueSeed, issueSeen, wispSeed, wispSeen); err != nil {
		return err
	}
	return loadWaitersOnSpawnerIDsByColInTx(ctx, tx, "depends_on_wisp_id", spawnerIDs, issueSeed, issueSeen, wispSeed, wispSeen)
}

//nolint:gosec // G201: targetCol is one of two constant column names.
func loadWaitersOnSpawnerIDsByColInTx(
	ctx context.Context, tx DBTX,
	targetCol string, spawnerIDs []string,
	issueSeed *[]string, issueSeen map[string]bool,
	wispSeed *[]string, wispSeen map[string]bool,
) error {
	if len(spawnerIDs) == 0 {
		return nil
	}
	tables := []struct {
		table  string
		seed   *[]string
		seen   map[string]bool
		errCtx string
	}{
		{"dependencies", issueSeed, issueSeen, "load issue waiters"},
		{"wisp_dependencies", wispSeed, wispSeen, "load wisp waiters"},
	}
	for _, spawnerID := range spawnerIDs {
		for _, t := range tables {
			query := fmt.Sprintf(`
				SELECT issue_id FROM %s
				WHERE type = 'waits-for' AND %s = ?
			`, t.table, targetCol)
			rows, err := tx.QueryContext(ctx, query, spawnerID)
			if err != nil {
				if optionalBlockedTable(t.table) && isTableNotExistError(err) {
					continue
				}
				return fmt.Errorf("%s: query: %w", t.errCtx, err)
			}
			for rows.Next() {
				var waiterID string
				if err := rows.Scan(&waiterID); err != nil {
					_ = rows.Close()
					return fmt.Errorf("%s: scan: %w", t.errCtx, err)
				}
				if !t.seen[waiterID] {
					t.seen[waiterID] = true
					*t.seed = append(*t.seed, waiterID)
				}
			}
			_ = rows.Close()
			if err := rows.Err(); err != nil {
				return fmt.Errorf("%s: rows: %w", t.errCtx, err)
			}
		}
	}
	return nil
}
