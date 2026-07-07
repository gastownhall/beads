package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"

	"github.com/steveyegge/beads/internal/types"
)

// doltCommitHashRE matches a Dolt commit hash (32 base32 characters). The
// dolt_diff table function requires literal arguments, so the from-commit is
// inlined into the query and must be validated, never interpolated raw.
var doltCommitHashRE = regexp.MustCompile(`^[0-9a-v]{32}$`)

// mergeRecomputeSeedCap bounds the per-row diff expansion. A pull that merged
// more rows than this (bootstrap-scale, or a long-offline clone catching up)
// recomputes the whole graph in one batched pass instead of running a BFS per
// changed row — the full pass is bounded by table size, the per-row expansion
// is not.
const mergeRecomputeSeedCap = 1000

// isBlockedRecomputePendingKey marks a post-merge is_blocked recompute that
// failed AFTER its merge was already committed (bd-578h9.11). Without it the
// retry is unreachable: the next pull's fromCommit is the new HEAD, so the
// head==fromCommit skip reads the failed window as "nothing merged" and stale
// is_blocked persists silently. The marker's value is the failed attempt's
// fromCommit (or "full" when unknown), so the retry can widen its diff window
// to cover the lost merge instead of always paying the full-graph pass.
const isBlockedRecomputePendingKey = "is_blocked_recompute_pending"

// MarkIsBlockedRecomputePendingInTx records a failed post-merge recompute so
// the next RecomputeIsBlockedAfterMergeInTx retries with a widened window. It
// must run in its OWN transaction — the failed recompute's tx is rolling back.
// INSERT IGNORE on purpose: when a marker from an earlier failure exists, its
// older fromCommit covers a superset of this one's window and must win.
func MarkIsBlockedRecomputePendingInTx(ctx context.Context, tx *sql.Tx, fromCommit string) error {
	value := fromCommit
	if !doltCommitHashRE.MatchString(value) {
		value = "full"
	}
	_, err := tx.ExecContext(ctx,
		"INSERT IGNORE INTO metadata (`key`, value) VALUES (?, ?)",
		isBlockedRecomputePendingKey, value)
	return err
}

// pendingIsBlockedRecompute reads the failure marker; "" means none. Read
// errors degrade to "no marker" — the marker is a self-heal fast path, and the
// caller's recompute proceeds either way.
func pendingIsBlockedRecompute(ctx context.Context, tx *sql.Tx) string {
	var value string
	if err := tx.QueryRowContext(ctx,
		"SELECT value FROM metadata WHERE `key` = ?", isBlockedRecomputePendingKey).Scan(&value); err != nil {
		return ""
	}
	return value
}

// RecomputeIsBlockedAfterMergeInTx recomputes the denormalized is_blocked
// column for every issue and wisp whose blocked state may have changed between
// fromCommit and the current working set — typically the HEAD before a
// `bd dolt pull` and the merged result it produced (bd-6dnrw.3, PR 4107
// follow-up). The diff targets WORKING, not HEAD: an auto-resolved or
// cascade-repaired merge sits in the working set without a merge commit until
// a later DOLT_COMMIT, so HEAD alone misses it (bd-6dnrw.39).
//
// is_blocked is maintained only by the local write paths (create/close/
// delete/dep/promote/bulk ops), so a merge that brings in another clone's
// writes bypasses every recompute hook: clone A closes blocker X while clone B
// adds an edge W->X, and the merged result silently carries W.is_blocked=1
// with a closed blocker — `bd ready` then trusts the stale column. This is the
// missing merge-side hook.
//
// The recompute is scoped by dolt_diff: rows of issues/dependencies changed
// between the two commits seed the same affected-set expansion the local write
// paths use, and only that closure is recomputed (a full-graph pass timed out
// on large databases when migration 0047 tried it). Above
// mergeRecomputeSeedCap changed rows — or if the diff itself fails, e.g.
// because the merge also changed those tables' schemas — it falls back to
// recomputing every issue and wisp. A fromCommit equal to HEAD with a clean
// working set (nothing merged) is a no-op.
//
// The is_blocked updates land in the working set; committing them is the
// caller's responsibility (they are derived state, so committing them on every
// clone converges — both sides compute the same values from the same merged
// graph).
func RecomputeIsBlockedAfterMergeInTx(ctx context.Context, tx *sql.Tx, fromCommit string) error {
	// bd-578h9.11: a marker from an earlier recompute that failed after its
	// merge committed means this call must also cover THAT merge's window —
	// the head==fromCommit skip below would otherwise read it as "nothing
	// merged" forever. Widen fromCommit to the failed attempt's (older)
	// pre-merge HEAD, or to a full pass when it is unknown. The marker is
	// cleared in this same transaction, so it survives if this attempt fails
	// too and disappears atomically with a successful recompute.
	if pending := pendingIsBlockedRecompute(ctx, tx); pending != "" {
		if doltCommitHashRE.MatchString(pending) && fromCommit != "" {
			fromCommit = pending
		} else {
			fromCommit = ""
		}
		if err := recomputeIsBlockedAfterMergeScoped(ctx, tx, fromCommit); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			"DELETE FROM metadata WHERE `key` = ?", isBlockedRecomputePendingKey)
		return err
	}
	return recomputeIsBlockedAfterMergeScoped(ctx, tx, fromCommit)
}

// recomputeIsBlockedAfterMergeScoped is RecomputeIsBlockedAfterMergeInTx
// without the failure-marker handling.
func recomputeIsBlockedAfterMergeScoped(ctx context.Context, tx *sql.Tx, fromCommit string) error {
	if fromCommit == "" {
		// The caller could not read the pre-merge HEAD; recompute everything
		// rather than skip the hook.
		return recomputeIsBlockedForAll(ctx, tx)
	}
	if !doltCommitHashRE.MatchString(fromCommit) {
		return fmt.Errorf("recompute is_blocked after merge: invalid from-commit %q", fromCommit)
	}
	var head string
	if err := tx.QueryRowContext(ctx, "SELECT DOLT_HASHOF('HEAD')").Scan(&head); err != nil {
		// Older engines (and embedded Dolt) expose only the HASHOF alias.
		if err := tx.QueryRowContext(ctx, "SELECT HASHOF('HEAD')").Scan(&head); err != nil {
			return fmt.Errorf("recompute is_blocked after merge: read HEAD: %w", err)
		}
	}
	if head == fromCommit {
		// HEAD did not advance — but a merge whose conflicts or constraint
		// violations bd auto-resolved lands in the WORKING SET without a merge
		// commit (bd-6dnrw.39), so an unchanged HEAD alone does not mean
		// nothing merged. Skip only when issues/dependencies are clean too;
		// on a status read error fall through to the diff, which answers the
		// same question row-by-row.
		var dirty int
		if err := tx.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM dolt_status WHERE table_name IN ('issues', 'dependencies')").Scan(&dirty); err == nil && dirty == 0 {
			return nil // nothing was merged
		}
	}

	issueIDs, wispIDs, ok, err := mergeAffectedSets(ctx, tx, fromCommit)
	if err != nil {
		return err
	}
	if !ok {
		return recomputeIsBlockedForAll(ctx, tx)
	}
	return RecomputeIsBlockedInTx(ctx, tx, issueIDs, wispIDs)
}

// mergeAffectedSets expands the issues/dependencies rows changed since
// fromCommit into the closed affected set, exactly as the local write paths
// would have done for each change. ok=false means the caller should fall back
// to a full recompute (diff unavailable or seed volume above the cap).
func mergeAffectedSets(ctx context.Context, tx *sql.Tx, fromCommit string) (issueIDs, wispIDs []string, ok bool, err error) {
	changedIssues, err := changedIssueIDs(ctx, tx, fromCommit)
	if err != nil {
		// dolt_diff fails when the merge also reshaped the table (schema change
		// between the refs); the safe answer is the full pass. A canceled or
		// timed-out context is not a diff failure, though — falling back would
		// only run a doomed full recompute on a dead context.
		if ctx.Err() != nil {
			return nil, nil, false, ctx.Err()
		}
		return nil, nil, false, nil
	}
	changedDeps, err := changedDependencyEdges(ctx, tx, fromCommit)
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, false, ctx.Err()
		}
		return nil, nil, false, nil
	}
	if len(changedIssues)+len(changedDeps) > mergeRecomputeSeedCap {
		return nil, nil, false, nil
	}

	issueSet := make(map[string]bool)
	wispSet := make(map[string]bool)
	add := func(issues, wisps []string) {
		for _, id := range issues {
			if !issueSet[id] {
				issueSet[id] = true
				issueIDs = append(issueIDs, id)
			}
		}
		for _, id := range wisps {
			if !wispSet[id] {
				wispSet[id] = true
				wispIDs = append(wispIDs, id)
			}
		}
	}

	for _, id := range changedIssues {
		issues, wisps, err := AffectedByStatusChangeInTx(ctx, tx, id)
		if err != nil {
			return nil, nil, false, fmt.Errorf("expand merged issue %s: %w", id, err)
		}
		add(issues, wisps)
	}
	for _, e := range changedDeps {
		issues, wisps, err := AffectedByDepChangeInTx(ctx, tx, e.issueID, e.target, e.depType)
		if err != nil {
			return nil, nil, false, fmt.Errorf("expand merged edge %s: %w", e.issueID, err)
		}
		add(issues, wisps)
	}
	return issueIDs, wispIDs, true, nil
}

// changedIssueIDs returns the id of every issues row added, removed, or
// modified between fromCommit and the working set. Removed ids are included on purpose:
// they no longer match any UPDATE, but their dependers (reached through the
// dependencies diff and the expansion) may unblock.
func changedIssueIDs(ctx context.Context, tx *sql.Tx, fromCommit string) ([]string, error) {
	//nolint:gosec // fromCommit is validated against doltCommitHashRE; dolt_diff requires literal args.
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(
		"SELECT COALESCE(to_id, from_id) FROM dolt_diff('%s', 'WORKING', 'issues')", fromCommit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id sql.NullString
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if id.Valid {
			ids = append(ids, id.String)
		}
	}
	return ids, rows.Err()
}

type changedDepEdge struct {
	issueID string
	target  string
	depType types.DependencyType
}

// changedDependencyEdges returns one entry per side (pre- and post-merge) of
// every dependencies row changed between fromCommit and the working set. Both sides
// matter: the to-side seeds newly-added or retargeted edges, the from-side
// seeds the dependers of deleted edges (whose rows no longer exist to be found
// by the expansion).
func changedDependencyEdges(ctx context.Context, tx *sql.Tx, fromCommit string) ([]changedDepEdge, error) {
	//nolint:gosec // fromCommit is validated against doltCommitHashRE; dolt_diff requires literal args.
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(`
		SELECT from_issue_id, COALESCE(from_depends_on_issue_id, from_depends_on_wisp_id, from_depends_on_external), from_type,
		       to_issue_id, COALESCE(to_depends_on_issue_id, to_depends_on_wisp_id, to_depends_on_external), to_type
		FROM dolt_diff('%s', 'WORKING', 'dependencies')`, fromCommit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var edges []changedDepEdge
	for rows.Next() {
		var fromIssue, fromTarget, fromType, toIssue, toTarget, toType sql.NullString
		if err := rows.Scan(&fromIssue, &fromTarget, &fromType, &toIssue, &toTarget, &toType); err != nil {
			return nil, err
		}
		if fromIssue.Valid {
			edges = append(edges, changedDepEdge{fromIssue.String, fromTarget.String, types.DependencyType(fromType.String)})
		}
		if toIssue.Valid {
			edges = append(edges, changedDepEdge{toIssue.String, toTarget.String, types.DependencyType(toType.String)})
		}
	}
	return edges, rows.Err()
}

// recomputeIsBlockedForAll recomputes the whole graph in one batched
// mark/unmark fixpoint pass — the fallback when the merge is too large or too
// reshaped to scope.
func recomputeIsBlockedForAll(ctx context.Context, tx *sql.Tx) error {
	issueIDs, err := allIDs(ctx, tx, "issues")
	if err != nil {
		return fmt.Errorf("recompute is_blocked after merge: list issues: %w", err)
	}
	wispIDs, err := allIDs(ctx, tx, "wisps")
	if err != nil {
		if isTableNotExistError(err) {
			wispIDs = nil
		} else {
			return fmt.Errorf("recompute is_blocked after merge: list wisps: %w", err)
		}
	}
	return RecomputeIsBlockedInTx(ctx, tx, issueIDs, wispIDs)
}

// allIDs lists every id in table. table must be a hardcoded constant.
func allIDs(ctx context.Context, tx DBTX, table string) ([]string, error) {
	//nolint:gosec // G201: table is a hardcoded constant, never user input.
	rows, err := tx.QueryContext(ctx, fmt.Sprintf("SELECT id FROM %s", table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
