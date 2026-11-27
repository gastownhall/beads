package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
)

// The PARTIAL-VIEW hold (bd-muhnf0). `dependencies` is versioned and carries
// depends_on_wisp_id, but `wisps`/`wisp_%` are dolt_ignore'd, so on a clone a
// dependency row can name a wisp this database has no row for. The
// should-be-blocked union INNER-JOINS wisps, so such an edge reads as a
// satisfied blocker: the unmark clears the dependent, the cleared value lands
// in the versioned issues table, the database that has the wisp writes the
// true value back, and the two trade the same rows through every merge.
//
// A dependency row that names a wisp with no row here is UNKNOWN, neither a
// reason nor a satisfied blocker. A row whose value depends on an unknown —
// because it holds such an edge, or because it inherits from a row that does —
// is HELD: the mark/unmark fixpoint leaves it exactly as found, and a closing
// mark (markHeldRowsInTx) decides it only on evidence this database can see.
// `wisp_dependencies` cannot dangle (its foreign keys cascade), so only
// `dependencies` rows do, and their holder is always an issue.
//
// The cost where nothing dangles — every database that mints its own wisps,
// while the wisp-delete cascade holds — is one LIMIT 1 probe per full repair
// or doctor count, and one primary-key lookup per durable→wisp edge in a
// scoped chunk (the batch guard read, blocked_state.go). When the probe is
// clean the statements run byte for byte as they did before the hold.

// heldRows is the set of rows whose is_blocked value is UNKNOWN on this
// database: rows that hold a dangling wisp edge (H0, always issues), and every
// OPEN parent-child descendant of such a row through both planes (the walk
// stops at closed and pinned rows: their value is 0, and the parent-child legs
// read a parent's flag only while it is neither closed nor pinned).
type heldRows struct {
	issues []string
	wisps  []string
}

func (h heldRows) empty() bool { return len(h.issues) == 0 && len(h.wisps) == 0 }

// danglingWispEdgeProbeSQL asks whether any dependencies row names a wisp this
// database has no row for. One row is enough: the probe only decides whether
// the hold machinery runs at all, so it is LIMIT 1 and driven from the
// dependency rows with a primary-key lookup into wisps — the shape the
// existing hinted walks use, pinned by TestGraphWalkPlansHonorJoinHints.
//
//nolint:gosec // a constant; no interpolation.
const danglingWispEdgeProbeSQL = `
	SELECT /*+ JOIN_ORDER(d, w) LOOKUP_JOIN(d, w) */ 1
	FROM dependencies d
	LEFT JOIN wisps w ON w.id = d.depends_on_wisp_id
	WHERE d.depends_on_wisp_id IS NOT NULL AND w.id IS NULL
	LIMIT 1
`

// danglingWispEdgeExistsInTx is the cheap gate in front of every hold path:
// true when at least one dangling wisp edge exists, false when none does (and
// on an engine without the wisp tables, where nothing can dangle).
func danglingWispEdgeExistsInTx(ctx context.Context, tx DBTX) (bool, error) {
	var one int
	err := tx.QueryRowContext(ctx, danglingWispEdgeProbeSQL).Scan(&one)
	if err == nil {
		return true, nil
	}
	if err == sql.ErrNoRows {
		return false, nil
	}
	if isTableNotExistError(err) {
		return false, nil
	}
	return false, fmt.Errorf("probe dangling wisp edges: %w", err)
}

// danglingEdgeHoldersInTx reads H0: the open issues that hold a dangling wisp
// edge. A closed or pinned holder is not held — its value is 0 however the
// edge reads — so it is not seeded and its descendants are not walked.
//
//nolint:gosec // a constant; no interpolation.
func danglingEdgeHoldersInTx(ctx context.Context, tx DBTX) ([]string, error) {
	return queryIDs(ctx, tx, `
		SELECT /*+ JOIN_ORDER(d, i) LOOKUP_JOIN(d, i) */ DISTINCT d.issue_id
		FROM dependencies d
		JOIN issues i ON i.id = d.issue_id
		WHERE d.depends_on_wisp_id IS NOT NULL
		  AND NOT EXISTS (SELECT 1 FROM wisps w WHERE w.id = d.depends_on_wisp_id)
		  AND i.status <> 'closed' AND i.status <> 'pinned'
	`)
}

// heldRowsForAllInTx computes the full hold set: probe first, and only when
// something dangles read H0 and close it under open parent-child descendants.
// It is the hold for the whole-database paths — the full repair and the doctor
// counts — where every held row must be known, because the unbatched
// statements carry `id NOT IN (held)` over the whole table.
func heldRowsForAllInTx(ctx context.Context, tx DBTX) (heldRows, error) {
	var held heldRows
	exists, err := danglingWispEdgeExistsInTx(ctx, tx)
	if err != nil || !exists {
		return held, err
	}
	held.issues, err = danglingEdgeHoldersInTx(ctx, tx)
	if err != nil {
		return held, fmt.Errorf("read dangling wisp edge holders: %w", err)
	}
	issueSeen := make(map[string]bool, len(held.issues))
	for _, id := range held.issues {
		issueSeen[id] = true
	}
	wispSeen := make(map[string]bool)
	issueQueue, wispQueue := held.issues, []string(nil)
	issueHead, wispHead := 0, 0
	// The frontier expansion expandByParentChildDescendantsInTx runs, with the
	// walk's one difference: it stops at closed and pinned children, whose
	// value is decided and whose flag nothing inherits.
	for issueHead < len(issueQueue) || wispHead < len(wispQueue) {
		if issueHead < len(issueQueue) {
			end := min(issueHead+queryBatchSize, len(issueQueue))
			batch := issueQueue[issueHead:end]
			issueHead = end
			if err := appendOpenChildrenInTx(ctx, tx, "dependencies", "depends_on_issue_id", batch, "issues", issueSeen, &issueQueue); err != nil {
				return held, err
			}
			if err := appendOpenChildrenInTx(ctx, tx, "dependencies", "depends_on_wisp_id", batch, "wisps", wispSeen, &wispQueue); err != nil {
				return held, err
			}
		}
		if wispHead < len(wispQueue) {
			end := min(wispHead+queryBatchSize, len(wispQueue))
			batch := wispQueue[wispHead:end]
			wispHead = end
			if err := appendOpenChildrenInTx(ctx, tx, "wisp_dependencies", "depends_on_issue_id", batch, "issues", issueSeen, &issueQueue); err != nil {
				return held, err
			}
			if err := appendOpenChildrenInTx(ctx, tx, "wisp_dependencies", "depends_on_wisp_id", batch, "wisps", wispSeen, &wispQueue); err != nil {
				return held, err
			}
		}
	}
	held.issues, held.wisps = issueQueue, wispQueue
	return held, nil
}

// appendOpenChildrenInTx enqueues the OPEN parent-child children of one
// parent batch: the edge lives in the child's dependency table and names the
// parent in parentCol; childTable is the children's own table (issues rows
// hold their edges in dependencies, wisp rows in wisp_dependencies). A closed
// or pinned child is decided and not entered, and the walk does not continue
// through it.
//
//nolint:gosec // table/column names are constants from the only caller; placeholders carry only ? placeholders.
func appendOpenChildrenInTx(
	ctx context.Context, tx DBTX,
	depTable, parentCol string, parents []string, childTable string,
	seen map[string]bool, queue *[]string,
) error {
	for start := 0; start < len(parents); start += queryBatchSize {
		end := min(start+queryBatchSize, len(parents))
		placeholders, args := buildSQLInClause(parents[start:end])
		query := fmt.Sprintf(`
			SELECT /*+ JOIN_ORDER(d, c) LOOKUP_JOIN(d, c) */ d.issue_id
			FROM %[1]s d JOIN %[2]s c ON c.id = d.issue_id
			WHERE d.type = 'parent-child' AND d.%[3]s IN (%[4]s)
			  AND c.status <> 'closed' AND c.status <> 'pinned'
		`, depTable, childTable, parentCol, placeholders)
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("expand held children from %s on %s: %w", depTable, parentCol, err)
		}
		for rows.Next() {
			var childID string
			if err := rows.Scan(&childID); err != nil {
				_ = rows.Close()
				return fmt.Errorf("expand held children: scan: %w", err)
			}
			if !seen[childID] {
				seen[childID] = true
				*queue = append(*queue, childID)
			}
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("expand held children: rows: %w", err)
		}
	}
	return nil
}

// heldChunkMembersInTx is the scoped paths' hold: the held subset of one
// chunk's ids — the ids that hold a dangling wisp edge themselves plus the
// ids with an open ancestor chain that reaches one. One recursive walk per
// chunk, seeded with the chunk's own ids, in the hinted shape the ancestor
// walk (ancestorChainInTx) already uses: one recursive member per dependency
// table and parent column, each an index probe on the child's issue_id, so
// the cost tracks the chunk's ancestry, not the store.
//
// The walk climbs through OPEN parents only: blockedness does not flow out of
// a closed or pinned row, so a chain that meets one is not a hold chain. The
// holder test — open issue holding a dangling edge — runs once, in the outer
// SELECT, never inside the recursion.
//
//nolint:gosec // spec's fields are constants; placeholders carries only ? placeholders.
func heldChunkMembersInTx(ctx context.Context, tx DBTX, spec blockedTableSpec, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders, args := buildSQLInClause(ids)
	query := fmt.Sprintf(`
		WITH RECURSIVE climb(seed, node) AS (
			SELECT t.id, t.id FROM %[1]s t WHERE t.id IN (%[2]s)
			UNION
			SELECT /*+ JOIN_ORDER(c, d) LOOKUP_JOIN(c, d) */ c.seed, p.id FROM climb c
			JOIN dependencies d ON d.issue_id = c.node AND d.type = 'parent-child'
			JOIN issues p ON p.id = d.depends_on_issue_id
			WHERE p.status <> 'closed' AND p.status <> 'pinned'
			UNION
			SELECT /*+ JOIN_ORDER(c, d) LOOKUP_JOIN(c, d) */ c.seed, w.id FROM climb c
			JOIN dependencies d ON d.issue_id = c.node AND d.type = 'parent-child'
			JOIN wisps w ON w.id = d.depends_on_wisp_id
			WHERE w.status <> 'closed' AND w.status <> 'pinned'
			UNION
			SELECT /*+ JOIN_ORDER(c, wd) LOOKUP_JOIN(c, wd) */ c.seed, p.id FROM climb c
			JOIN wisp_dependencies wd ON wd.issue_id = c.node AND wd.type = 'parent-child'
			JOIN issues p ON p.id = wd.depends_on_issue_id
			WHERE p.status <> 'closed' AND p.status <> 'pinned'
			UNION
			SELECT /*+ JOIN_ORDER(c, wd) LOOKUP_JOIN(c, wd) */ c.seed, w.id FROM climb c
			JOIN wisp_dependencies wd ON wd.issue_id = c.node AND wd.type = 'parent-child'
			JOIN wisps w ON w.id = wd.depends_on_wisp_id
			WHERE w.status <> 'closed' AND w.status <> 'pinned'
		)
		SELECT DISTINCT c.seed
		FROM climb c
		JOIN issues h ON h.id = c.node
		JOIN %[3]s s ON s.id = c.seed
		WHERE h.status <> 'closed' AND h.status <> 'pinned'
		  AND s.status <> 'closed' AND s.status <> 'pinned'
		  AND EXISTS (SELECT 1 FROM dependencies dd
		              WHERE dd.issue_id = c.node AND dd.depends_on_wisp_id IS NOT NULL
		                AND NOT EXISTS (SELECT 1 FROM wisps w2 WHERE w2.id = dd.depends_on_wisp_id))
	`, spec.table, placeholders, spec.table)
	found, err := queryIDs(ctx, tx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("read held chunk members: %w", err)
	}
	return found, nil
}

// heldScopeAncestryInTx returns the OPEN parent-child ancestors of the given
// held rows, through both planes, for the scoped closing mark's parent
// exclusion. A held row's parent is itself held whenever the row is held
// through its ancestry (the chain is the hold), but the chunk climb reports
// only the chunk's own ids, so a parent above the chunk would otherwise read
// as decided. Excluding the whole ancestry over-holds at most — it can only
// leave a flag as found — never marks on a guess.
//
//nolint:gosec // constants and ? placeholders only.
func heldScopeAncestryInTx(ctx context.Context, tx DBTX, held heldRows) (heldRows, error) {
	var out heldRows
	// Each plane's statement seeds from that plane's held ids only: an empty
	// seed set has no ancestry to read, and rendering it would be `IN ()`.
	if len(held.issues) == 0 && len(held.wisps) == 0 {
		return out, nil
	}
	var issueAncestry []string
	if len(held.issues) > 0 {
		issuePlaceholders, issueArgs := buildSQLInClause(held.issues)
		var err error
		issueAncestry, err = queryIDs(ctx, tx, fmt.Sprintf(`
			WITH RECURSIVE ancestors(node) AS (
				SELECT id FROM issues WHERE id IN (%[1]s)
				UNION
				%[2]s
			)
			SELECT DISTINCT a.node FROM ancestors a
			JOIN issues t ON t.id = a.node
			WHERE t.status <> 'closed' AND t.status <> 'pinned'
		`, issuePlaceholders, heldAncestryMembers()), issueArgs...)
		if err != nil {
			return out, fmt.Errorf("read held ancestry: %w", err)
		}
	}
	var wispAncestry []string
	if len(held.wisps) > 0 {
		wispPlaceholders, wispArgs := buildSQLInClause(held.wisps)
		var err error
		wispAncestry, err = queryIDs(ctx, tx, fmt.Sprintf(`
			WITH RECURSIVE ancestors(node) AS (
				SELECT id FROM wisps WHERE id IN (%[1]s)
				UNION
				%[2]s
			)
			SELECT DISTINCT a.node FROM ancestors a
			JOIN wisps t ON t.id = a.node
			WHERE t.status <> 'closed' AND t.status <> 'pinned'
		`, wispPlaceholders, heldAncestryMembers()), wispArgs...)
		if err != nil && !isTableNotExistError(err) {
			return out, fmt.Errorf("read held ancestry: %w", err)
		}
	}
	out.issues, out.wisps = issueAncestry, wispAncestry
	return out, nil
}

// heldAncestryMembers is the recursive member of heldScopeAncestryInTx's
// walk, in the same four-arm shape as the chunk climb: an issue node's
// parent-child edges live in dependencies, a wisp node's in
// wisp_dependencies, and either plane's parent can be an issue or a wisp. The
// walk climbs through OPEN parents only, the same rule every hold chain
// carries.
//
//nolint:gosec // table names are constants.
func heldAncestryMembers() string {
	return `
			SELECT /*+ JOIN_ORDER(r, d) LOOKUP_JOIN(r, d) */ p.id FROM ancestors r
			JOIN dependencies d ON d.issue_id = r.node AND d.type = 'parent-child'
			JOIN issues p ON p.id = d.depends_on_issue_id
			WHERE p.status <> 'closed' AND p.status <> 'pinned'
			UNION
			SELECT /*+ JOIN_ORDER(r, d) LOOKUP_JOIN(r, d) */ w.id FROM ancestors r
			JOIN dependencies d ON d.issue_id = r.node AND d.type = 'parent-child'
			JOIN wisps w ON w.id = d.depends_on_wisp_id
			WHERE w.status <> 'closed' AND w.status <> 'pinned'
			UNION
			SELECT /*+ JOIN_ORDER(r, wd) LOOKUP_JOIN(r, wd) */ p.id FROM ancestors r
			JOIN wisp_dependencies wd ON wd.issue_id = r.node AND wd.type = 'parent-child'
			JOIN issues p ON p.id = wd.depends_on_issue_id
			WHERE p.status <> 'closed' AND p.status <> 'pinned'
			UNION
			SELECT /*+ JOIN_ORDER(r, wd) LOOKUP_JOIN(r, wd) */ w.id FROM ancestors r
			JOIN wisp_dependencies wd ON wd.issue_id = r.node AND wd.type = 'parent-child'
			JOIN wisps w ON w.id = wd.depends_on_wisp_id
			WHERE w.status <> 'closed' AND w.status <> 'pinned'
	`
}

// heldNotInSQL renders the fixpoint statements' held-row exclusion for alias:
// nothing at all for an empty set — `NOT IN ()` is a syntax error, and "no row
// is held" is the right reading of an empty set — so the statement text is
// byte for byte the pre-hold statement wherever nothing dangles.
func heldNotInSQL(alias string, held []string) string {
	if len(held) == 0 {
		return ""
	}
	placeholders := ""
	for i := range held {
		if i > 0 {
			placeholders += ","
		}
		placeholders += "?"
	}
	return fmt.Sprintf(" AND %s.id NOT IN (%s)", alias, placeholders)
}

// heldPredicateConds carries the closing-mark predicate's bound id groups:
// the (c)-leg exclusions (parents this run must not read: the explained set,
// the held set, and — on the scoped path — the held rows' ancestry) and the
// marked-parent arm's ids (held rows this closing mark has already
// established blocked, minus the explained ones, which may not darken
// anything: the #6506 rule holds inside the closing mark too).
type heldPredicateConds struct {
	issueParentExcl, wispParentExcl []string
	markedIssue, markedWisp         []string
}

// notInCond renders "AND p.id NOT IN (…)" and appends the ids to args, or
// nothing for an empty set.
func notInCond(ids []string, args *[]any) string {
	if len(ids) == 0 {
		return ""
	}
	placeholders := ""
	for i := range ids {
		if i > 0 {
			placeholders += ","
		}
		placeholders += "?"
		*args = append(*args, ids[i])
	}
	return "AND p.id NOT IN (" + placeholders + ")"
}

// inCond renders "dp.<col> IN (…)" for the marked-parent arm, or nothing for
// an empty set.
func inCond(ids []string, col string, args *[]any) string {
	if len(ids) == 0 {
		return ""
	}
	placeholders := ""
	for i := range ids {
		if i > 0 {
			placeholders += ","
		}
		placeholders += "?"
		*args = append(*args, ids[i])
	}
	return "dp." + col + " IN (" + placeholders + ")"
}

// sql returns the closing-mark predicate for a row aliased rowAlias whose
// dependency rows live in depTable, plus the predicate's bound ids in text
// order. The three reasons:
//
//	(a) a blocks/conditional-blocks edge onto a present open target, issue or
//	    wisp — this reads the target's status, never a flag;
//	(b) a waits-for edge onto a PRESENT spawner whose gate blocks; a
//	    waits-for onto an absent spawner is unknown even when an open durable
//	    child is visible, because the any-children carve-out is non-monotone,
//	    the spawner's wisp children are invisible here, and also_blocks needs
//	    the spawner's own status;
//	(c) a parent-child edge onto p, where p is neither closed nor pinned,
//	    carries a stored 1 this run may read (p outside the exclusions), and
//	    is not explained; or p is a held row this closing mark has established
//	    blocked and is opaque on visible evidence (not explained).
//
// The (a) and (b) bodies deliberately re-spell union legs 1-2 and the gate
// rather than calling the union builder: the closing mark asks a different
// question (what can this database SEE) than the fixpoint (what is), and the
// spawner-presence conjunct in (b) has no fixpoint counterpart.
//
//nolint:gosec // rowAlias and depTable are constants; conds render ? placeholders only.
func (c heldPredicateConds) sql(rowAlias, depTable string) (string, []any) {
	var args []any
	issueParentCond := notInCond(c.issueParentExcl, &args)
	wispParentCond := notInCond(c.wispParentExcl, &args)
	markedIssue := inCond(c.markedIssue, "depends_on_issue_id", &args)
	markedWisp := inCond(c.markedWisp, "depends_on_wisp_id", &args)
	return fmt.Sprintf(`
			  ( EXISTS (SELECT 1 FROM %[2]s dh
			            WHERE dh.issue_id = %[1]s.id
			              AND (dh.type = 'blocks' OR dh.type = 'conditional-blocks')
			              AND ( EXISTS (SELECT 1 FROM issues bt
			                            WHERE bt.id = dh.depends_on_issue_id
			                              AND bt.status <> 'closed' AND bt.status <> 'pinned')
			                 OR EXISTS (SELECT 1 FROM wisps bt
			                            WHERE bt.id = dh.depends_on_wisp_id
			                              AND bt.status <> 'closed' AND bt.status <> 'pinned') ))
			  OR EXISTS (SELECT 1 FROM %[2]s dg
			            WHERE dg.issue_id = %[1]s.id
			              AND dg.type = 'waits-for'
			              AND ( (dg.depends_on_issue_id IS NOT NULL
			                     AND EXISTS (SELECT 1 FROM issues sp WHERE sp.id = dg.depends_on_issue_id))
			                 OR (dg.depends_on_wisp_id IS NOT NULL
			                     AND EXISTS (SELECT 1 FROM wisps sp WHERE sp.id = dg.depends_on_wisp_id)) )
			              AND (%[3]s) )
			  OR EXISTS (SELECT 1 FROM %[2]s dp
			            JOIN issues p ON p.id = dp.depends_on_issue_id
			            WHERE dp.issue_id = %[1]s.id
			              AND dp.type = 'parent-child'
			              AND p.is_blocked = 1
			              AND p.status <> 'closed' AND p.status <> 'pinned'
			              %[4]s)
			  OR EXISTS (SELECT 1 FROM %[2]s dp
			            JOIN wisps p ON p.id = dp.depends_on_wisp_id
			            WHERE dp.issue_id = %[1]s.id
			              AND dp.type = 'parent-child'
			              AND p.is_blocked = 1
			              AND p.status <> 'closed' AND p.status <> 'pinned'
			              %[5]s)
			  %[6]s
			  %[7]s )
		`, rowAlias, depTable, waitsForGateBlockedSQLFor("dg"),
		issueParentCond, wispParentCond, markedIssue, markedWisp), args
}

// markHeldRowsInTx is the closing mark: after the mark/unmark fixpoint has
// converged over the DECIDED rows, it decides held rows that this database's
// visible evidence settles, and only those. It is mark-only and monotone — it
// never clears a flag — so a held row with no visible reason keeps exactly
// what it carried in: the last merge's value, or one a since-closed local
// blocker set.
//
// It iterates to a fixpoint because a row it establishes can be the visible
// blocker of the next: round r's marked parents arm round r+1's (c) leg. A
// parent counts as established when the predicate settles its value — a row
// it flips 0→1, or a row already at 1 whose blockedness the predicate itself
// establishes, whose stored bit is therefore not being trusted but confirmed.
// Explained parents never darken anything, the #6506 rule, so they are
// subtracted from the marked arm.
//
// extraExcl is the scoped path's supplement to the (c)-leg exclusion (the
// held rows' open ancestry, see heldScopeAncestryInTx); the whole-database
// path passes none, because its held set is already complete.
//
// Flips are returned per kind, so the caller's corrected-row count — and with
// it the `changed > 0` commit gate every repair mode shares — and the callers
// that stage tables for a Dolt commit (GH#6647) both see them.
func markHeldRowsInTx(ctx context.Context, tx DBTX, held, extraExcl heldRows) (heldRows, error) {
	_, flips, err := heldMarkRoundsInTx(ctx, tx, held, extraExcl, true)
	return flips, err
}

// heldMarkRoundsInTx runs the closing-mark rounds. With apply set it writes
// the marks and returns the rows flipped, per kind; either way it returns the
// full set of held rows the predicate establishes (marked), which is what the
// doctor's held count subtracts from the hold set.
func heldMarkRoundsInTx(ctx context.Context, tx DBTX, held, extraExcl heldRows, apply bool) (marked, flips heldRows, err error) {
	if held.empty() {
		return marked, flips, nil
	}
	// Which parents may the (c) leg read? Not the explained ones, not the
	// held ones, and — on a scoped run — not the held rows' ancestry. The
	// explained set is read by id, once, for exactly the parents the held
	// rows name.
	parents, err := heldParentRefsInTx(ctx, tx, held)
	if err != nil {
		return marked, flips, err
	}
	explained, err := explainedAmongPrunedInTx(ctx, tx, parents)
	if err != nil {
		return marked, flips, err
	}
	issueExcl := idSetUnion(explained.issues, held.issues, extraExcl.issues)
	wispExcl := idSetUnion(explained.wisps, held.wisps, extraExcl.wisps)
	issueMarked := make(map[string]bool)
	wispMarked := make(map[string]bool)
	for {
		conds := heldPredicateConds{
			issueParentExcl: issueExcl,
			wispParentExcl:  wispExcl,
			markedIssue:     minusIDs(issueMarked, explained.issues),
			markedWisp:      minusIDs(wispMarked, explained.wisps),
		}
		roundMarked := 0
		for _, side := range []struct {
			table string
			ids   []string
		}{
			{"issues", held.issues},
			{"wisps", held.wisps},
		} {
			rows, err := heldMarkRoundRowsInTx(ctx, tx, side.table, side.ids, conds)
			if err != nil {
				return marked, flips, err
			}
			if len(rows) == 0 {
				continue
			}
			roundMarked += len(rows)
			var toFlip []string
			for _, row := range rows {
				if side.table == "issues" {
					issueMarked[row.id] = true
				} else {
					wispMarked[row.id] = true
				}
				if !row.blocked {
					toFlip = append(toFlip, row.id)
				}
			}
			if !apply || len(toFlip) == 0 {
				continue
			}
			// SELECT-then-UPDATE: RowsAffected cannot name the rows it
			// flipped, and the journal and the flip count both want the ids —
			// toFlip already carries exactly them.
			placeholders, args := buildSQLInClause(toFlip)
			//nolint:gosec // table is one of the two constants above.
			_, err = tx.ExecContext(ctx, fmt.Sprintf(`
				UPDATE %[1]s t SET t.is_blocked = 1, t.updated_at = t.updated_at
				WHERE t.id IN (%[2]s) AND t.is_blocked = 0 AND t.status <> 'closed' AND t.status <> 'pinned'
			`, side.table, placeholders), args...)
			if err != nil {
				return marked, flips, fmt.Errorf("mark held is_blocked (%s): %w", side.table, err)
			}
			if side.table == "issues" {
				flips.issues = append(flips.issues, toFlip...)
			} else {
				flips.wisps = append(flips.wisps, toFlip...)
			}
		}
		if roundMarked == 0 {
			break
		}
	}
	marked.issues, marked.wisps = setIDs(issueMarked), setIDs(wispMarked)
	return marked, flips, nil
}

// heldMarkRow is one held row this round's predicate establishes as blocked,
// with its stored flag — a row already at 1 is an established parent for the
// next round exactly like one this run flips, and only a 0 is a flip.
type heldMarkRow struct {
	id      string
	blocked bool
}

// heldMarkRoundRowsInTx selects the held rows of one table this round's
// predicate establishes as blocked, with their stored flags.
//
//nolint:gosec // table is one of two constants; conds render ? placeholders only.
func heldMarkRoundRowsInTx(ctx context.Context, tx DBTX, table string, ids []string, conds heldPredicateConds) ([]heldMarkRow, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders, args := buildSQLInClause(ids)
	depTable := "dependencies"
	if table == "wisps" {
		depTable = "wisp_dependencies"
	}
	pred, predArgs := conds.sql("t", depTable)
	query := fmt.Sprintf(`
		SELECT t.id, t.is_blocked FROM %[1]s t
		WHERE t.id IN (%[2]s)
		  AND t.status <> 'closed' AND t.status <> 'pinned'
		  AND (%[3]s)
	`, table, placeholders, pred)
	rows, err := tx.QueryContext(ctx, query, append(args, predArgs...)...)
	if err != nil {
		return nil, fmt.Errorf("read markable held rows (%s): %w", table, err)
	}
	defer func() { _ = rows.Close() }()
	var out []heldMarkRow
	for rows.Next() {
		var row heldMarkRow
		if err := rows.Scan(&row.id, &row.blocked); err != nil {
			return nil, fmt.Errorf("read markable held rows (%s): scan: %w", table, err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read markable held rows (%s): rows: %w", table, err)
	}
	return out, nil
}

// countMarkableHeldInTx counts the held rows of one table whose stored 0 the
// closing mark's FIRST round would flip — the round-1 predicate, no marked
// parents yet, over rows the fixpoint statements left as found. It is the
// doctor count's held arm, built from the same predicate builder as the
// closing mark so the two readings cannot drift.
//
//nolint:gosec // table is one of two constants; conds render ? placeholders only.
func countMarkableHeldInTx(ctx context.Context, tx DBTX, table string, ids []string, conds heldPredicateConds) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	placeholders, args := buildSQLInClause(ids)
	depTable := "dependencies"
	if table == "wisps" {
		depTable = "wisp_dependencies"
	}
	pred, predArgs := conds.sql("t", depTable)
	query := fmt.Sprintf(`
		SELECT COUNT(*) FROM %[1]s t
		WHERE t.id IN (%[2]s)
		  AND t.is_blocked = 0
		  AND t.status <> 'closed' AND t.status <> 'pinned'
		  AND (%[3]s)
	`, table, placeholders, pred)
	n, err := countRows(ctx, tx, query, append(args, predArgs...)...)
	if err != nil {
		return 0, fmt.Errorf("count markable held rows (%s): %w", table, err)
	}
	return n, nil
}

// heldParentRefsInTx reads the parent-child parents of the held rows, both
// kinds, for the explained read.
//
//nolint:gosec // depTable is one of two constants; placeholders carry only ? placeholders.
func heldParentRefsInTx(ctx context.Context, tx DBTX, held heldRows) ([]parentRef, error) {
	var out []parentRef
	for _, side := range []struct {
		depTable string
		ids      []string
		wisp     bool
	}{
		{"dependencies", held.issues, false},
		{"wisp_dependencies", held.wisps, true},
	} {
		if len(side.ids) == 0 {
			continue
		}
		placeholders, args := buildSQLInClause(side.ids)
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(`
			SELECT d.depends_on_issue_id, d.depends_on_wisp_id
			FROM %[1]s d
			WHERE d.issue_id IN (%[2]s) AND d.type = 'parent-child'
		`, side.depTable, placeholders), args...)
		if err != nil {
			if optionalBlockedTable(side.depTable) && isTableNotExistError(err) {
				continue
			}
			return nil, fmt.Errorf("read held rows' parents: %w", err)
		}
		for rows.Next() {
			var ip, wp sql.NullString
			if err := rows.Scan(&ip, &wp); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("read held rows' parents: scan: %w", err)
			}
			if ip.Valid {
				out = append(out, parentRef{id: ip.String})
			}
			if wp.Valid {
				out = append(out, parentRef{id: wp.String, wisp: true})
			}
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("read held rows' parents: rows: %w", err)
		}
	}
	return out, nil
}

// explainedAmongPrunedInTx reads, by id, which of the named parents are
// explained by their own subtree (parentsExplainedBySubtreeSQL's set, pruned
// by ancestry exactly as the fixpoint's reads are), split by kind. The
// closing mark uses it so "p is not explained" cannot drift from the
// fixpoint's own exogeneity reading.
func explainedAmongPrunedInTx(ctx context.Context, tx DBTX, parents []parentRef) (heldRows, error) {
	var out heldRows
	var read subtreeExplainedParents
	for _, kind := range []struct {
		table, depTable string
		wisp            bool
		target          *[]string
	}{
		{"issues", "dependencies", false, &read.issueParents},
		{"wisps", "wisp_dependencies", true, &read.wispParents},
	} {
		var ids []string
		for _, p := range parents {
			if p.wisp == kind.wisp {
				ids = append(ids, p.id)
			}
		}
		for start := 0; start < len(ids); start += queryBatchSize {
			end := min(start+queryBatchSize, len(ids))
			placeholders, args := buildSQLInClause(ids[start:end])
			found, err := queryIDs(ctx, tx,
				parentsExplainedBySubtreeCoreSQL(kind.table, kind.depTable, candByIDSQL(kind.table, placeholders)),
				args...)
			if err != nil {
				return out, fmt.Errorf("read explained parents of held rows: %w", err)
			}
			*kind.target = append(*kind.target, found...)
		}
	}
	pruned, err := pruneExplainedByAncestryInTx(ctx, tx, read, false)
	if err != nil {
		return out, err
	}
	out.issues, out.wisps = pruned.issueParents, pruned.wispParents
	return out, nil
}

// CountIsBlockedHeldInTx counts the held rows the closing mark does NOT
// establish — the rows `bd doctor` reports as left as found. It is read-only
// (the rounds run without their UPDATE), informational, and never changes a
// status. Nothing dangling anywhere returns 0 through the probe, without the
// exogeneity read.
func CountIsBlockedHeldInTx(ctx context.Context, tx DBTX) (int64, error) {
	held, err := heldRowsForAllInTx(ctx, tx)
	if err != nil || held.empty() {
		return 0, err
	}
	marked, _, err := heldMarkRoundsInTx(ctx, tx, held, heldRows{}, false)
	if err != nil {
		return 0, err
	}
	return int64(len(held.issues)+len(held.wisps)) - int64(len(marked.issues)+len(marked.wisps)), nil
}

// total returns the number of rows across both kinds.
func (h heldRows) total() int {
	return len(h.issues) + len(h.wisps)
}

// idSetUnion merges id lists into one deduplicated list.
func idSetUnion(lists ...[]string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, list := range lists {
		for _, id := range list {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	return out
}

// minusIDs returns the keys of set that are not in sub.
func minusIDs(set map[string]bool, sub []string) []string {
	excluded := make(map[string]bool, len(sub))
	for _, id := range sub {
		excluded[id] = true
	}
	var out []string
	for id := range set {
		if !excluded[id] {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// setIDs lists a set's keys, sorted for deterministic statement text.
func setIDs(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
