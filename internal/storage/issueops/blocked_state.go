package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"

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

// waitsForGateBlockedSQL decides whether a waits-for row d (spawner =
// d.depends_on_issue_id or d.depends_on_wisp_id) still gates its waiter.
//
// Each "the spawner has an open / a closed parent-child child" test is split
// per dependency table AND per spawner column, and pins the child lookup by
// primary key (JOIN_ORDER/LOOKUP_JOIN; on MySQL, JOIN_ORDER is honored and
// the unknown LOOKUP_JOIN hint is ignored with a warning). The earlier form
// matched both spawner columns with one OR inside each EXISTS, which no
// single index serves, so the engine scanned the whole edge table per
// correlated evaluation: closing one child of a 1000-child spawner with 20
// waiters on a 50k-issue / 100k-edge database spent ~39 s in the recompute.
// Split, each EXISTS is an index lookup on its target column (the
// idx_*_issue_target / idx_*_wisp_target indexes). EXISTS(a OR b) is
// EXISTS(a) OR EXISTS(b), so the result is unchanged; the IS NOT NULL guards
// stay, as before (cd.col = NULL never matches anyway).
const waitsForGateBlockedSQL = `
		(
		  (
		    EXISTS (
		      SELECT /*+ JOIN_ORDER(cd, child) LOOKUP_JOIN(cd, child) */ 1 FROM dependencies cd JOIN issues child ON child.id = cd.issue_id
		      WHERE cd.type = 'parent-child'
		        AND d.depends_on_issue_id IS NOT NULL AND cd.depends_on_issue_id = d.depends_on_issue_id
		        AND child.status <> 'closed' AND child.status <> 'pinned'
		    )
		    OR EXISTS (
		      SELECT /*+ JOIN_ORDER(cd, child) LOOKUP_JOIN(cd, child) */ 1 FROM dependencies cd JOIN issues child ON child.id = cd.issue_id
		      WHERE cd.type = 'parent-child'
		        AND d.depends_on_wisp_id IS NOT NULL AND cd.depends_on_wisp_id = d.depends_on_wisp_id
		        AND child.status <> 'closed' AND child.status <> 'pinned'
		    )
		    OR EXISTS (
		      SELECT /*+ JOIN_ORDER(cd, child) LOOKUP_JOIN(cd, child) */ 1 FROM wisp_dependencies cd JOIN wisps child ON child.id = cd.issue_id
		      WHERE cd.type = 'parent-child'
		        AND d.depends_on_issue_id IS NOT NULL AND cd.depends_on_issue_id = d.depends_on_issue_id
		        AND child.status <> 'closed' AND child.status <> 'pinned'
		    )
		    OR EXISTS (
		      SELECT /*+ JOIN_ORDER(cd, child) LOOKUP_JOIN(cd, child) */ 1 FROM wisp_dependencies cd JOIN wisps child ON child.id = cd.issue_id
		      WHERE cd.type = 'parent-child'
		        AND d.depends_on_wisp_id IS NOT NULL AND cd.depends_on_wisp_id = d.depends_on_wisp_id
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
		        SELECT /*+ JOIN_ORDER(cd, child) LOOKUP_JOIN(cd, child) */ 1 FROM dependencies cd JOIN issues child ON child.id = cd.issue_id
		        WHERE cd.type = 'parent-child'
		          AND d.depends_on_issue_id IS NOT NULL AND cd.depends_on_issue_id = d.depends_on_issue_id
		          AND child.status = 'closed'
		      )
		      OR EXISTS (
		        SELECT /*+ JOIN_ORDER(cd, child) LOOKUP_JOIN(cd, child) */ 1 FROM dependencies cd JOIN issues child ON child.id = cd.issue_id
		        WHERE cd.type = 'parent-child'
		          AND d.depends_on_wisp_id IS NOT NULL AND cd.depends_on_wisp_id = d.depends_on_wisp_id
		          AND child.status = 'closed'
		      )
		      OR EXISTS (
		        SELECT /*+ JOIN_ORDER(cd, child) LOOKUP_JOIN(cd, child) */ 1 FROM wisp_dependencies cd JOIN wisps child ON child.id = cd.issue_id
		        WHERE cd.type = 'parent-child'
		          AND d.depends_on_issue_id IS NOT NULL AND cd.depends_on_issue_id = d.depends_on_issue_id
		          AND child.status = 'closed'
		      )
		      OR EXISTS (
		        SELECT /*+ JOIN_ORDER(cd, child) LOOKUP_JOIN(cd, child) */ 1 FROM wisp_dependencies cd JOIN wisps child ON child.id = cd.issue_id
		        WHERE cd.type = 'parent-child'
		          AND d.depends_on_wisp_id IS NOT NULL AND cd.depends_on_wisp_id = d.depends_on_wisp_id
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

// waitsForGateRowAliasRE matches the dependency-row alias references in
// waitsForGateBlockedSQL — `d.` at a word boundary, which never matches the
// gate's own inner `cd.` rows (no boundary between c and d).
var waitsForGateRowAliasRE = regexp.MustCompile(`\bd\.`)

// waitsForGateBlockedSQLFor returns waitsForGateBlockedSQL with its
// dependency-row alias rewritten from d to alias, for callers that evaluate
// the gate somewhere the row cannot be called d — blockingReasonSQL applies it
// to a parent's own dependency rows inside a union leg whose outer row is
// already d (gastownhall/beads#6506). Rewriting beats shadowing: an inner d
// would resolve correctly by scope rules but reads as a bug at every later
// glance.
func waitsForGateBlockedSQLFor(alias string) string {
	if alias == "d" {
		return waitsForGateBlockedSQL
	}
	return waitsForGateRowAliasRE.ReplaceAllString(waitsForGateBlockedSQL, alias+".")
}

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
	issuePlan, err := planRecomputeInTx(ctx, tx, issuesBlockedSpec, issueIDs, splitEdgeless)
	if err != nil {
		return result, err
	}
	wispPlan, err := planRecomputeInTx(ctx, tx, wispsBlockedSpec, wispIDs, splitEdgeless)
	if err != nil {
		return result, err
	}
	for pass := 0; ; pass++ {
		var changed int64

		n, err := runRecomputePassInTx(ctx, tx, issuePlan, pass == 0)
		if err != nil {
			return result, err
		}
		changed += n
		result.IssueRowsChanged = result.IssueRowsChanged || n > 0

		n, err = runRecomputePassInTx(ctx, tx, wispPlan, pass == 0)
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
	return runMarkBatchedInTx(ctx, tx, issuesBlockedSpec, ids)
}

// The mark/unmark templates explicitly assign updated_at to itself:
// issues.updated_at (and wisps.updated_at) carry ON UPDATE CURRENT_TIMESTAMP,
// and is_blocked is DERIVED state - letting a recompute bump updated_at
// plants per-clone wall clock in a synced table (merge conflicts between
// clones that recomputed the same flip at different times, bd-578h9.19) and
// makes stale-guard/conflict-guard consumers treat the row as user-edited.
// An explicit assignment suppresses the ON UPDATE clause.
//
// Both templates decide membership through
// shouldBeBlockedIDsUnionScopedPrecomputedSQL, the same uncorrelated union the
// full repair and the doctor count use (blocked_consistency.go), scoped to the
// batch: one derived blocked set per batch, computed once and probed by hash.
// The previous shape — five correlated EXISTS per outer row — was re-executed
// per row by the engine: ~4 s per 200-id batch on committed, indexed data, and
// unbounded (>69 min observed on dolt 2.1.8) over a large uncommitted working
// set, where every probe re-read the uncommitted overlay
// (gastownhall/beads#6288).
//
// The parent-child legs' exogeneity set (gastownhall/beads#6506) is READ once
// per batch and BOUND into both statements as ids, never spliced as the query
// that derives it: spliced, a 200-id recompute paid for the derivation four
// times and measured 6.9x origin/main (scopedExplainedParentsInTx has the
// numbers). Which is why the templates are built per batch rather than once:
// the number of bound ids is a property of the batch.
//
// The batch IN-list therefore appears more than once per statement — the
// outer row filter and one per union leg (six occurrences today) — and the
// exogeneity ids appear as already-rendered ? placeholders inside two of those
// legs. expandBatchTemplate binds both groups in TEXT order.

// batchScopeSQL is the per-leg predicate that confines the should-be-blocked
// union to the batch (see shouldBeBlockedIDsUnionScopedPrecomputedSQL); its %s
// is filled with the batch placeholders by expandBatchTemplate, and by fmt
// here ONLY for the batch-scoped exogeneity read, which binds its own ids.
const batchScopeSQL = "AND d.issue_id IN (%s)"

// blockedTableSpec names the one table a batched mark/unmark pass runs over.
// The batched statements are now built per batch (the exogeneity ids are), so
// the runner takes the table rather than a finished template.
type blockedTableSpec struct {
	table, alias, depTable string
}

var (
	issuesBlockedSpec = blockedTableSpec{table: "issues", alias: "i", depTable: "dependencies"}
	wispsBlockedSpec  = blockedTableSpec{table: "wisps", alias: "w", depTable: "wisp_dependencies"}
)

func markBlockedTemplateForIssues(explained subtreeExplainedParents) string {
	return markBlockedTemplate(issuesBlockedSpec, explained)
}

func unmarkBlockedTemplateForIssues(explained subtreeExplainedParents) string {
	return unmarkBlockedTemplate(issuesBlockedSpec, explained)
}

func markBlockedTemplateForWisps(explained subtreeExplainedParents) string {
	return markBlockedTemplate(wispsBlockedSpec, explained)
}

func unmarkBlockedTemplateForWisps(explained subtreeExplainedParents) string {
	return unmarkBlockedTemplate(wispsBlockedSpec, explained)
}

// markBlockedTemplate is the batched mark statement for one table: the
// batch-scoped analog of markAllBlockedSQL. The union is confined to the
// batch's own dependency rows, so `<alias>.id IN (union)` is exactly the
// old correlated disjunction for every id in the batch.
//
//nolint:gosec // G201: the spec's fields are constants from the four callers above.
func markBlockedTemplate(spec blockedTableSpec, explained subtreeExplainedParents) string {
	return fmt.Sprintf(`
		UPDATE %[1]s %[2]s SET %[2]s.is_blocked = 1, %[2]s.updated_at = %[2]s.updated_at
		WHERE %[2]s.id IN (%%s)
		  AND %[2]s.is_blocked = 0
		  AND %[2]s.status <> 'closed' AND %[2]s.status <> 'pinned'
		  AND %[2]s.id IN (%[3]s)
	`, spec.table, spec.alias,
		shouldBeBlockedIDsUnionScopedPrecomputedSQL(spec.depTable, batchScopeSQL, explained))
}

// unmarkBlockedTemplate is the batched unmark statement for one table: the
// batch-scoped analog of unmarkAllBlockedSQL. NOT IN is null-hostile; the
// union's d.issue_id IS NOT NULL guards keep it total.
//
//nolint:gosec // G201: the spec's fields are constants from the four callers above.
func unmarkBlockedTemplate(spec blockedTableSpec, explained subtreeExplainedParents) string {
	return fmt.Sprintf(`
		UPDATE %[1]s %[2]s SET %[2]s.is_blocked = 0, %[2]s.updated_at = %[2]s.updated_at
		WHERE %[2]s.id IN (%%s)
		  AND %[2]s.is_blocked = 1
		  AND ( %[2]s.status = 'closed' OR %[2]s.status = 'pinned'
		        OR %[2]s.id NOT IN (%[3]s) )
	`, spec.table, spec.alias,
		shouldBeBlockedIDsUnionScopedPrecomputedSQL(spec.depTable, batchScopeSQL, explained))
}

// expandBatchTemplate finishes a batched template: every %s takes the batch's
// IN-list placeholders, and the args come out in the order the finished
// statement reads them — the batch ids once per %s, and the already-rendered
// ? placeholders the template carries (the two parent-child legs' exogeneity
// ids, see notInPlaceholders) interleaved where they actually appear.
//
// TEXT ORDER is the whole contract, and it is why this walks the template
// instead of counting: the batch scope and the exogeneity ids alternate inside
// the union (leg 3's scope, leg 3's NOT IN, leg 4's scope, leg 4's NOT IN), so
// no amount of counting says where a bound group goes. A template with one %s
// and no ? degrades to the plain Sprintf it always was.
//
// The count is textual, so a template must contain no percent sign that is not
// one of the %s verbs (no LIKE 'x%' pattern, no %%) and no ? of its own beyond
// the bound group — both pinned by blocked_state_template_test.go. A mismatch
// between the ? count and the bound ids is a programmer error and is returned
// as one rather than sent to the engine, where it would surface as an opaque
// argument-count error.
//
//nolint:gosec // G201: tmpl is a constant template; only IN-clause placeholders are formatted in.
func expandBatchTemplate(
	tmpl, placeholders string, args, bound []interface{},
) (string, []interface{}, error) {
	var fills []interface{}
	out := make([]interface{}, 0, len(args)+len(bound))
	next := 0
	for i := 0; i < len(tmpl); i++ {
		switch {
		case tmpl[i] == '%' && i+1 < len(tmpl) && tmpl[i+1] == 's':
			fills = append(fills, placeholders)
			out = append(out, args...)
			i++
		case tmpl[i] == '?':
			if next < len(bound) {
				out = append(out, bound[next])
			}
			next++
		}
	}
	if next != len(bound) {
		return "", nil, fmt.Errorf(
			"batched template carries %d ? placeholders, want %d bound ids", next, len(bound))
	}
	return fmt.Sprintf(tmpl, fills...), out, nil
}

func markIsBlockedPassForWispsInTx(ctx context.Context, tx DBTX, ids []string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	return runMarkBatchedInTx(ctx, tx, wispsBlockedSpec, ids)
}

// batchExogeneity reads the batch-scoped exogeneity set for one chunk of ids:
// ONE read for the chunk, shared by the chunk's mark and its unmark.
//
// It is read before the mark, so the unmark sees the set as it was at the top
// of the chunk. That is the same staleness the unbatched full repair accepts
// (one read per PASS, see recomputeIsBlockedCounting), and it is safe for the
// same reason: both callers are fixpoint loops that stop only on a pass that
// changes NO rows, and on that pass the set was read against the state the
// pass ends in. So the state the loop converges to satisfies both predicates
// under the set derived from that very state; a stale read can only cost an
// extra pass, never a wrong fixpoint.
func batchExogeneity(
	ctx context.Context, tx DBTX, spec blockedTableSpec, placeholders string, args []interface{},
) (subtreeExplainedParents, error) {
	return scopedExplainedParentsInTx(ctx, tx, spec.depTable,
		fmt.Sprintf(batchScopeSQL, placeholders), args)
}

func runMarkUnmarkBatchedInTx(ctx context.Context, tx DBTX, spec blockedTableSpec, ids []string) (int64, error) {
	var changed int64
	for start := 0; start < len(ids); start += queryBatchSize {
		end := start + queryBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		placeholders, args := buildSQLInClause(ids[start:end])
		explained, err := batchExogeneity(ctx, tx, spec, placeholders, args)
		if err != nil {
			return changed, fmt.Errorf("recompute is_blocked (exogeneity): %w", err)
		}

		stmt, stmtArgs, err := expandBatchTemplate(
			markBlockedTemplate(spec, explained), placeholders, args, explained.args())
		if err != nil {
			return changed, fmt.Errorf("recompute is_blocked (mark): %w", err)
		}
		res, err := tx.ExecContext(ctx, stmt, stmtArgs...)
		if err != nil {
			return changed, fmt.Errorf("recompute is_blocked (mark): %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return changed, fmt.Errorf("recompute is_blocked (mark rows affected): %w", err)
		}
		changed += n

		stmt, stmtArgs, err = expandBatchTemplate(
			unmarkBlockedTemplate(spec, explained), placeholders, args, explained.args())
		if err != nil {
			return changed, fmt.Errorf("recompute is_blocked (unmark): %w", err)
		}
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
	spec   blockedTableSpec
	chunks []recomputeChunk
}

// planRecomputeInTx chunks ids exactly as runMarkUnmarkBatchedInTx would and
// splits each chunk by whether spec.depTable holds a row with that id as its
// issue_id.
//
// Every leg of the should-be-blocked union the mark/unmark templates probe is
// scoped to `d.issue_id IN (batch)` on spec.depTable
// (shouldBeBlockedIDsUnionScopedPrecomputedSQL), so an id with no row of its
// own there is never in that union: mark cannot select it, and unmark reduces
// to "is_blocked = 1 -> 0". Those ids — every freshly created issue without
// edges, which is most of what a large create or apply-batch recomputes —
// need not ride the two union statements, whose cost grows with the table.
//
// The split costs one probe per chunk, which only pays when the chunk holds
// edgeless ids worth skipping. So it runs when the caller asks (splitEdgeless:
// the create paths) or when the chunk is large; the single-issue recomputes of
// close, reopen, update and dependency edits keep exactly the statements they
// ran before the shortcut existed.
func planRecomputeInTx(ctx context.Context, tx DBTX, spec blockedTableSpec, ids []string, splitEdgeless bool) (recomputePlan, error) {
	plan := recomputePlan{spec: spec}
	for start := 0; start < len(ids); start += queryBatchSize {
		end := min(start+queryBatchSize, len(ids))
		chunk := recomputeChunk{withEdges: ids[start:end]}
		if !createFastPathsDisabled.Load() && (splitEdgeless || end-start >= recomputeSplitMinChunk) {
			var err error
			chunk.withEdges, chunk.edgeless, err = splitByOwnDependencyRowsInTx(ctx, tx, spec.depTable, ids[start:end])
			if err != nil {
				return plan, err
			}
		}
		plan.chunks = append(plan.chunks, chunk)
	}
	return plan, nil
}

// runRecomputePassInTx is one fixpoint pass over the plan,
// statement-for-statement equivalent to runMarkUnmarkBatchedInTx over the
// plan's full ids. Per chunk: the mark and unmark templates run over the ids
// with edges — the union, scoped to them, holds exactly what it held scoped to
// the whole chunk, and so does the batch's exogeneity read (an edgeless id
// names no parent) — and then, on the first pass only, a plain unmark clears
// the edgeless ids. It runs after both templates so their unions see the
// edgeless rows' pre-pass is_blocked, as they did when those rows were cleared
// by the chunk's own unmark statement; on later passes it would match no row
// (nothing marks an edgeless id), so it is skipped.
//
// It is a function rather than a method on recomputePlan because the
// structural write guards follow bare-identifier calls only: as a method, the
// mark/unmark DML it reaches would be invisible from the exported recompute
// entry points, and their versionExemptions entries would read as stale
// (TestEveryBeadMutatorMintsOrIsExempt).
func runRecomputePassInTx(ctx context.Context, tx DBTX, p recomputePlan, firstPass bool) (int64, error) {
	var changed int64
	for _, chunk := range p.chunks {
		if len(chunk.withEdges) > 0 {
			n, err := runMarkUnmarkBatchedInTx(ctx, tx, p.spec, chunk.withEdges)
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
		`, p.spec.table, placeholders), args...)
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

func runMarkBatchedInTx(ctx context.Context, tx DBTX, spec blockedTableSpec, ids []string) (int64, error) {
	var changed int64
	for start := 0; start < len(ids); start += queryBatchSize {
		end := start + queryBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		placeholders, args := buildSQLInClause(ids[start:end])
		explained, err := batchExogeneity(ctx, tx, spec, placeholders, args)
		if err != nil {
			return changed, fmt.Errorf("mark is_blocked (exogeneity): %w", err)
		}

		stmt, stmtArgs, err := expandBatchTemplate(
			markBlockedTemplate(spec, explained), placeholders, args, explained.args())
		if err != nil {
			return changed, fmt.Errorf("mark is_blocked: %w", err)
		}
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
			if err := appendSiblingsUnderAncestorsInTx(ctx, tx, target, &issueSeed, issueSeen, &wispSeed, wispSeen); err != nil {
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
			if err := appendSiblingsUnderAncestorsInTx(ctx, tx, target, &issueSeed, issueSeen, &wispSeed, wispSeen); err != nil {
				return nil, nil, err
			}
		}
		return expandByParentChildDescendantsInTx(ctx, tx, issueSeed, wispSeed, issueSeen, wispSeen)
	default:
		return nil, nil, nil
	}
}

// appendSiblingsUnderAncestorsInTx seeds every existing parent-child child of
// parent AND of each of parent's own ancestors (of either kind) for recompute.
//
// A parent-child edge onto a parent that BLOCKS on the new child reclassifies
// that blocking reason from exogenous to subtree-derived, and the cascade legs
// propagate only exogenous blockedness (gastownhall/beads#6506,
// parentsExplainedBySubtreeSQL). So writing or removing one edge can flip the
// answer for the parent's OTHER children, which no other seed in this walk
// reaches: the close-gate epic is built one child at a time, and the second
// edge is what un-darkens the first child.
//
// The walk goes all the way UP because the exogeneity test is over the whole
// SUBTREE, not over direct children. Hanging G under C changes nothing about C
// but everything about any ANCESTOR of C that blocks on G: that reason just
// moved inside its subtree, so the ancestor stops darkening its own children.
// The reviewer's depth-2 lock is exactly this shape (P blocks G; pc C -> P,
// C2 -> P, G -> C), and only P's sibling set — not C's — carries the flip.
// Seeding each ancestor's whole sibling set, which
// expandByParentChildDescendantsInTx then expands to their subtrees, is what
// keeps the incremental write path agreeing with the full repair.
func appendSiblingsUnderAncestorsInTx(
	ctx context.Context, tx DBTX,
	parent string,
	issueSeed *[]string, issueSeen map[string]bool,
	wispSeed *[]string, wispSeen map[string]bool,
) error {
	parents, err := ancestorChainInTx(ctx, tx, parent)
	if err != nil {
		return err
	}
	if len(parents) == 0 {
		return nil
	}
	// The parents' own kinds are not known here, so probe both target columns;
	// the one that does not match a parent simply returns no rows.
	for _, w := range []struct {
		depTable, parentCol string
		seed                *[]string
		seen                map[string]bool
	}{
		{"dependencies", "depends_on_issue_id", issueSeed, issueSeen},
		{"wisp_dependencies", "depends_on_issue_id", wispSeed, wispSeen},
		{"dependencies", "depends_on_wisp_id", issueSeed, issueSeen},
		{"wisp_dependencies", "depends_on_wisp_id", wispSeed, wispSeen},
	} {
		if err := appendChildrenInTx(ctx, tx, w.depTable, w.parentCol, parents, w.seen, w.seed); err != nil {
			return err
		}
	}
	return nil
}

// ancestorChainInTx returns id, plus every parent-child ancestor of id THAT
// CARRIES A BLOCKING REASON OF ITS OWN — a dependency row that is not
// parent-child. Order is not meaningful.
//
// The filter is what keeps the walk from being quadratic. Only an ancestor
// that holds a blocking reason can have that reason reclassified by an edge
// below it, so only such an ancestor's children can flip; an ancestor with
// nothing but hierarchy edges contributes seeds that can never change. Without
// the filter a 70-deep chain reseeds every ancestor's subtree on every edge
// written into it, which is O(depth x size) per write and times out.
//
// It walks with isAncestorInTx's own recursive members (reachabilityMembers in
// dependencies.go): one per dependency table, each an index probe on issue_id
// pinned by join hints, merged by UNION distinct, so a malformed diamond or
// cycle in the hierarchy terminates by unique reachable node rather than
// running away. The members project NULL for a row that is not parent-child,
// which the outer filter drops. Joining the frontier to a derived UNION of the
// two tables instead, this walk's first shape, is the scan #7224 removed from
// isAncestorInTx (minutes per walk at 100k edges), and it runs here on every
// parent-child write and merge replay. TestGraphWalkPlansHonorJoinHints
// (embedded) pins the lookup plan.
//
//nolint:gosec // G201: the members are built from constant table names and type filters.
func ancestorChainInTx(ctx context.Context, tx DBTX, id string) ([]string, error) {
	if id == "" {
		return nil, nil
	}
	query := fmt.Sprintf(`
		WITH RECURSIVE ancestors(node) AS (
			SELECT ?
			UNION
			%s
		)
		SELECT node FROM ancestors
		WHERE node IS NOT NULL
		  AND ( node = ?
		     OR EXISTS (SELECT 1 FROM dependencies x
		                WHERE x.issue_id = ancestors.node AND x.type <> 'parent-child')
		     OR EXISTS (SELECT 1 FROM wisp_dependencies x
		                WHERE x.issue_id = ancestors.node AND x.type <> 'parent-child') )
	`, reachabilityMembers("ancestors", cycleDetectionTables(), "d.type = 'parent-child'"))

	rows, err := tx.QueryContext(ctx, query, id, id)
	if err != nil {
		return nil, fmt.Errorf("load ancestor chain of %s: %w", id, err)
	}
	defer func() { _ = rows.Close() }()

	var out []string
	for rows.Next() {
		var node string
		if err := rows.Scan(&node); err != nil {
			return nil, fmt.Errorf("scan ancestor chain of %s: %w", id, err)
		}
		out = append(out, node)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ancestor chain rows for %s: %w", id, err)
	}
	return out, nil
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
