package issueops

import (
	"context"
	"database/sql"
	"fmt"

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
		  JSON_UNQUOTE(JSON_EXTRACT(d.metadata, '$.gate')) = 'any-children'
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
`

func RecomputeIsBlockedInTx(ctx context.Context, tx DBTX, issueIDs, wispIDs []string) error {
	if len(issueIDs) == 0 && len(wispIDs) == 0 {
		return nil
	}
	for {
		var changed int64

		n, err := recomputeIsBlockedPassForIssuesInTx(ctx, tx, issueIDs)
		if err != nil {
			return err
		}
		changed += n

		n, err = recomputeIsBlockedPassForWispsInTx(ctx, tx, wispIDs)
		if err != nil {
			return err
		}
		changed += n

		if changed == 0 {
			return nil
		}
	}
}

func MarkIsBlockedInTx(ctx context.Context, tx DBTX, issueIDs, wispIDs []string) error {
	if len(issueIDs) == 0 && len(wispIDs) == 0 {
		return nil
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
			return nil
		}
	}
}

func RecomputeIsBlockedForIDsInTx(ctx context.Context, tx DBTX, ids []string) error {
	return RecomputeIsBlockedInTx(ctx, tx, ids, nil)
}

func RecomputeIsBlockedForWispIDsInTx(ctx context.Context, tx DBTX, ids []string) error {
	return RecomputeIsBlockedInTx(ctx, tx, nil, ids)
}

//nolint:gosec // G201: SQL templates are constant; only IN-clause placeholders are formatted in.
func recomputeIsBlockedPassForIssuesInTx(ctx context.Context, tx DBTX, ids []string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}

	return runMarkUnmarkBatchedInTx(ctx, tx, markBlockedTemplateForIssues(), unmarkBlockedTemplateForIssues(), ids)
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
func markBlockedTemplateForIssues() string {
	return fmt.Sprintf(`
		UPDATE issues i SET i.is_blocked = 1, i.updated_at = i.updated_at
		WHERE i.id IN (%%s)
		  AND i.is_blocked = 0
		  AND i.status <> 'closed' AND i.status <> 'pinned'
		  AND (
		    EXISTS (
		      SELECT 1 FROM dependencies d
		      JOIN issues t ON t.id = d.depends_on_issue_id
		      WHERE d.issue_id = i.id
		        AND (d.type = 'blocks' OR d.type = 'conditional-blocks')
		        AND t.status <> 'closed' AND t.status <> 'pinned'
		    )
		    OR EXISTS (
		      SELECT 1 FROM dependencies d
		      JOIN wisps t ON t.id = d.depends_on_wisp_id
		      WHERE d.issue_id = i.id
		        AND (d.type = 'blocks' OR d.type = 'conditional-blocks')
		        AND t.status <> 'closed' AND t.status <> 'pinned'
		    )
		    OR EXISTS (
		      SELECT 1 FROM dependencies d
		      JOIN issues p ON p.id = d.depends_on_issue_id
		      WHERE d.issue_id = i.id
		        AND d.type = 'parent-child'
		        AND p.is_blocked = 1
		    )
		    OR EXISTS (
		      SELECT 1 FROM dependencies d
		      JOIN wisps p ON p.id = d.depends_on_wisp_id
		      WHERE d.issue_id = i.id
		        AND d.type = 'parent-child'
		        AND p.is_blocked = 1
		    )
		    OR EXISTS (
		      SELECT 1 FROM dependencies d
		      WHERE d.issue_id = i.id AND d.type = 'waits-for'
		        AND (%s)
		    )
		  )
	`, waitsForGateBlockedSQL)
}

func unmarkBlockedTemplateForIssues() string {
	return fmt.Sprintf(`
		UPDATE issues i SET i.is_blocked = 0, i.updated_at = i.updated_at
		WHERE i.id IN (%%s)
		  AND i.is_blocked = 1
		  AND (
		    i.status = 'closed' OR i.status = 'pinned'
		    OR (
		      NOT EXISTS (
		        SELECT 1 FROM dependencies d
		        JOIN issues t ON t.id = d.depends_on_issue_id
		        WHERE d.issue_id = i.id
		          AND (d.type = 'blocks' OR d.type = 'conditional-blocks')
		          AND t.status <> 'closed' AND t.status <> 'pinned'
		      )
		      AND NOT EXISTS (
		        SELECT 1 FROM dependencies d
		        JOIN wisps t ON t.id = d.depends_on_wisp_id
		        WHERE d.issue_id = i.id
		          AND (d.type = 'blocks' OR d.type = 'conditional-blocks')
		          AND t.status <> 'closed' AND t.status <> 'pinned'
		      )
		      AND NOT EXISTS (
		        SELECT 1 FROM dependencies d
		        JOIN issues p ON p.id = d.depends_on_issue_id
		        WHERE d.issue_id = i.id
		          AND d.type = 'parent-child'
		          AND p.is_blocked = 1
		      )
		      AND NOT EXISTS (
		        SELECT 1 FROM dependencies d
		        JOIN wisps p ON p.id = d.depends_on_wisp_id
		        WHERE d.issue_id = i.id
		          AND d.type = 'parent-child'
		          AND p.is_blocked = 1
		      )
		      AND NOT EXISTS (
		        SELECT 1 FROM dependencies d
		        WHERE d.issue_id = i.id AND d.type = 'waits-for'
		          AND (%s)
		      )
		    )
		  )
	`, waitsForGateBlockedSQL)
}

//nolint:gosec // G201: SQL templates are constant; only IN-clause placeholders are formatted in.
func recomputeIsBlockedPassForWispsInTx(ctx context.Context, tx DBTX, ids []string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}

	return runMarkUnmarkBatchedInTx(ctx, tx, markBlockedTemplateForWisps(), unmarkBlockedTemplateForWisps(), ids)
}

func markIsBlockedPassForWispsInTx(ctx context.Context, tx DBTX, ids []string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	return runMarkBatchedInTx(ctx, tx, markBlockedTemplateForWisps(), ids)
}

func markBlockedTemplateForWisps() string {
	return fmt.Sprintf(`
		UPDATE wisps w SET w.is_blocked = 1, w.updated_at = w.updated_at
		WHERE w.id IN (%%s)
		  AND w.is_blocked = 0
		  AND w.status <> 'closed' AND w.status <> 'pinned'
		  AND (
		    EXISTS (
		      SELECT 1 FROM wisp_dependencies d
		      JOIN issues t ON t.id = d.depends_on_issue_id
		      WHERE d.issue_id = w.id
		        AND (d.type = 'blocks' OR d.type = 'conditional-blocks')
		        AND t.status <> 'closed' AND t.status <> 'pinned'
		    )
		    OR EXISTS (
		      SELECT 1 FROM wisp_dependencies d
		      JOIN wisps t ON t.id = d.depends_on_wisp_id
		      WHERE d.issue_id = w.id
		        AND (d.type = 'blocks' OR d.type = 'conditional-blocks')
		        AND t.status <> 'closed' AND t.status <> 'pinned'
		    )
		    OR EXISTS (
		      SELECT 1 FROM wisp_dependencies d
		      JOIN issues p ON p.id = d.depends_on_issue_id
		      WHERE d.issue_id = w.id
		        AND d.type = 'parent-child'
		        AND p.is_blocked = 1
		    )
		    OR EXISTS (
		      SELECT 1 FROM wisp_dependencies d
		      JOIN wisps p ON p.id = d.depends_on_wisp_id
		      WHERE d.issue_id = w.id
		        AND d.type = 'parent-child'
		        AND p.is_blocked = 1
		    )
		    OR EXISTS (
		      SELECT 1 FROM wisp_dependencies d
		      WHERE d.issue_id = w.id AND d.type = 'waits-for'
		        AND (%s)
		    )
		  )
	`, waitsForGateBlockedSQL)
}

func unmarkBlockedTemplateForWisps() string {
	return fmt.Sprintf(`
		UPDATE wisps w SET w.is_blocked = 0, w.updated_at = w.updated_at
		WHERE w.id IN (%%s)
		  AND w.is_blocked = 1
		  AND (
		    w.status = 'closed' OR w.status = 'pinned'
		    OR (
		      NOT EXISTS (
		        SELECT 1 FROM wisp_dependencies d
		        JOIN issues t ON t.id = d.depends_on_issue_id
		        WHERE d.issue_id = w.id
		          AND (d.type = 'blocks' OR d.type = 'conditional-blocks')
		          AND t.status <> 'closed' AND t.status <> 'pinned'
		      )
		      AND NOT EXISTS (
		        SELECT 1 FROM wisp_dependencies d
		        JOIN wisps t ON t.id = d.depends_on_wisp_id
		        WHERE d.issue_id = w.id
		          AND (d.type = 'blocks' OR d.type = 'conditional-blocks')
		          AND t.status <> 'closed' AND t.status <> 'pinned'
		      )
		      AND NOT EXISTS (
		        SELECT 1 FROM wisp_dependencies d
		        JOIN issues p ON p.id = d.depends_on_issue_id
		        WHERE d.issue_id = w.id
		          AND d.type = 'parent-child'
		          AND p.is_blocked = 1
		      )
		      AND NOT EXISTS (
		        SELECT 1 FROM wisp_dependencies d
		        JOIN wisps p ON p.id = d.depends_on_wisp_id
		        WHERE d.issue_id = w.id
		          AND d.type = 'parent-child'
		          AND p.is_blocked = 1
		      )
		      AND NOT EXISTS (
		        SELECT 1 FROM wisp_dependencies d
		        WHERE d.issue_id = w.id AND d.type = 'waits-for'
		          AND (%s)
		      )
		    )
		  )
	`, waitsForGateBlockedSQL)
}

// SelfReferentialRecomputeTemplates returns the four is_blocked recompute UPDATE
// statements exactly as issueops emits them to the driver (with a representative
// IN-clause batch substituted for the placeholder). Each one updates issues/wisps
// while its WHERE clause self-references that same target table in EXISTS subqueries,
// which MySQL 8 rejects with error 1093 unless the mysqldialect driver rewrites the
// statement into a materialized derived-table form.
//
// It is exported solely so that rewrite can be regression-tested against these real
// templates instead of a hand-copied fixture: if a template ever drifts out of the
// shape the rewrite recognizes (a predicate before `id IN`, a backticked table, a
// renamed alias, changed whitespace), the mysqldialect rewrite test fails in a normal
// non-gated `go test` run rather than only under the env-gated MySQL conformance suite.
func SelfReferentialRecomputeTemplates() []string {
	placeholders, _ := buildSQLInClause([]string{"a", "b", "c"})
	render := func(tmpl string) string { return fmt.Sprintf(tmpl, placeholders) }
	return []string{
		render(markBlockedTemplateForIssues()),
		render(unmarkBlockedTemplateForIssues()),
		render(markBlockedTemplateForWisps()),
		render(unmarkBlockedTemplateForWisps()),
	}
}

//nolint:gosec // G201: callers pass constant templates; only IN-clause placeholders are formatted in.
func runMarkUnmarkBatchedInTx(ctx context.Context, tx DBTX, markTmpl, unmarkTmpl string, ids []string) (int64, error) {
	var changed int64
	for start := 0; start < len(ids); start += queryBatchSize {
		end := start + queryBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		placeholders, args := buildSQLInClause(ids[start:end])

		res, err := tx.ExecContext(ctx, fmt.Sprintf(markTmpl, placeholders), args...)
		if err != nil {
			return changed, fmt.Errorf("recompute is_blocked (mark): %w", err)
		}
		n, _ := res.RowsAffected()
		changed += n

		res, err = tx.ExecContext(ctx, fmt.Sprintf(unmarkTmpl, placeholders), args...)
		if err != nil {
			return changed, fmt.Errorf("recompute is_blocked (unmark): %w", err)
		}
		n, _ = res.RowsAffected()
		changed += n
	}
	return changed, nil
}

//nolint:gosec // G201: callers pass constant templates; only IN-clause placeholders are formatted in.
func runMarkBatchedInTx(ctx context.Context, tx DBTX, markTmpl string, ids []string) (int64, error) {
	var changed int64
	for start := 0; start < len(ids); start += queryBatchSize {
		end := start + queryBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		placeholders, args := buildSQLInClause(ids[start:end])

		res, err := tx.ExecContext(ctx, fmt.Sprintf(markTmpl, placeholders), args...)
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
	query := fmt.Sprintf(`
		SELECT issue_id FROM %s
		WHERE type = 'parent-child'
		  AND %s = ?
	`, depTable, parentCol)
	for _, parentID := range parentIDs {
		rows, err := tx.QueryContext(ctx, query, parentID)
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
