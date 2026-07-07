package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

var permanentIssueAuxTables = []string{"issues", "labels", "dependencies", "events", "comments"}

// IsEphemeralID returns true if the ID belongs to an ephemeral issue.
func IsEphemeralID(id string) bool {
	return strings.Contains(id, "-wisp-")
}

func DefaultInfraTypes() []string {
	return domain.DefaultInfraTypes()
}

func IsInfraType(t types.IssueType) bool {
	return domain.IsInfraType(t)
}

// IsInfraTypeCtx returns true if the issue type is infrastructure, using the
// configured infra types from DB config / config.yaml / defaults.
func (s *DoltStore) IsInfraTypeCtx(ctx context.Context, t types.IssueType) bool {
	return s.GetInfraTypes(ctx)[string(t)]
}

// isActiveWisp checks if an issue ID exists in the wisps table.
// Returns false if the wisp was promoted/deleted or doesn't exist.
// Used by CRUD methods to decide whether to route to wisp tables or fall through
// to permanent tables (handles promoted wisps correctly).
//
// For IDs matching the -wisp- pattern, does a full row scan (fast path for
// auto-generated wisp IDs). For other IDs, uses a lightweight existence check
// to support ephemeral beads created with explicit IDs (GH#2053).
func (s *DoltStore) isActiveWisp(ctx context.Context, id string) bool {
	if IsEphemeralID(id) {
		wisp, _ := s.getWisp(ctx, id)
		return wisp != nil
	}
	// Fallback: check wisps table for ephemeral beads with explicit IDs.
	// Ephemeral beads created with --id=<custom> don't contain "-wisp-" in
	// their ID, but are still stored in the wisps table. Use a lightweight
	// existence check to avoid full row scan on every non-wisp lookup.
	return s.wispExists(ctx, id)
}

// wispExists checks if an ID exists in the wisps table using a lightweight query.
// Used as a fallback for ephemeral beads with explicit (non-wisp) IDs (GH#2053).
func (s *DoltStore) wispExists(ctx context.Context, id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var exists int
	err := s.db.QueryRowContext(ctx, "SELECT 1 FROM wisps WHERE id = ? LIMIT 1", id).Scan(&exists)
	return err == nil
}

// allEphemeral returns true if all IDs in the slice are ephemeral.
func allEphemeral(ids []string) bool {
	for _, id := range ids {
		if !IsEphemeralID(id) {
			return false
		}
	}
	return len(ids) > 0
}

// partitionIDs separates IDs into ephemeral and dolt groups based on ID pattern only.
// NOTE: This misses explicit-ID ephemerals (GH#2053). For correct routing, use
// partitionByWispStatus which checks the wisps table as source of truth.
func partitionIDs(ids []string) (ephIDs, doltIDs []string) {
	for _, id := range ids {
		if IsEphemeralID(id) {
			ephIDs = append(ephIDs, id)
		} else {
			doltIDs = append(doltIDs, id)
		}
	}
	return
}

// partitionByWispStatus separates IDs into wisp (ephemeral) and permanent groups,
// using the wisps table as source of truth. Unlike partitionIDs (which only checks
// the ID pattern), this correctly handles explicit-ID ephemerals (GH#2053).
func (s *DoltStore) partitionByWispStatus(ctx context.Context, ids []string) (wispIDs, permIDs []string) {
	if len(ids) == 0 {
		return nil, nil
	}

	// Fast partition by ID pattern — handles -wisp- IDs correctly
	var patternWispIDs []string
	patternWispIDs, permIDs = partitionIDs(ids)

	// Verify wisp-pattern IDs actually exist in the wisps table (bd-ftc).
	// Promoted wisps have -wisp- in their ID but live in the issues table,
	// so pattern-based routing alone misroutes them.
	if len(patternWispIDs) > 0 {
		activeSet := s.batchWispExists(ctx, patternWispIDs)
		for _, id := range patternWispIDs {
			if activeSet[id] {
				wispIDs = append(wispIDs, id)
			} else {
				permIDs = append(permIDs, id)
			}
		}
	}

	// Check if any permanent IDs are actually explicit-ID wisps (GH#2053)
	if len(permIDs) == 0 {
		return
	}

	activeSet := s.batchWispExists(ctx, permIDs)
	if len(activeSet) == 0 {
		return
	}

	var realPerm []string
	for _, id := range permIDs {
		if activeSet[id] {
			wispIDs = append(wispIDs, id)
		} else {
			realPerm = append(realPerm, id)
		}
	}
	permIDs = realPerm
	return
}

// batchWispExists returns the set of IDs that exist in the wisps table.
// Used by partitionByWispStatus to detect explicit-ID ephemerals in a single query.
// Uses batched IN clauses (queryBatchSize) to avoid full table scans on Dolt with large ID sets.
func (s *DoltStore) batchWispExists(ctx context.Context, ids []string) map[string]bool {
	if len(ids) == 0 {
		return nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]bool)
	for start := 0; start < len(ids); start += queryBatchSize {
		end := start + queryBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]
		placeholders, args := doltBuildSQLInClause(batch)

		//nolint:gosec // G201: placeholders contains only ? markers
		rows, err := s.db.QueryContext(ctx,
			fmt.Sprintf("SELECT id FROM wisps WHERE id IN (%s)", placeholders),
			args...)
		if err != nil {
			return nil // On error, assume no wisps (safe fallback)
		}

		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err == nil {
				result[id] = true
			}
		}
		_ = rows.Close()
	}
	return result
}

// PromoteFromEphemeral copies an issue from the wisps table to the issues table,
// clearing the Ephemeral flag. Used by bd promote and mol squash to crystallize wisps.
//
// Uses direct SQL inserts to bypass IsEphemeralID routing, which would otherwise
// redirect label/dependency/event writes back to wisp tables.
func (s *DoltStore) PromoteFromEphemeral(ctx context.Context, id string, actor string) error {
	if err := s.withRetryTx(ctx, func(tx *sql.Tx) error {
		if err := issueops.PromoteFromEphemeralInTx(ctx, tx, id, actor); err != nil {
			return err
		}
		return s.doltAddAndCommitInTx(ctx, tx, permanentIssueAuxTables, fmt.Sprintf("bd: promote %s", id))
	}); err != nil {
		return err
	}
	return nil
}

// DemoteToWisp moves an issue from the issues table to the wisps table.
// This is the inverse of PromoteFromEphemeral. It applies any provided updates
// (e.g., setting no_history or ephemeral) without recording an intermediate
// update event, then migrates it atomically: insert into wisps, copy auxiliary
// data, delete from issues.
//
// Called by UpdateIssue when no_history=true or wisp=true is set on a regular issue.
func (s *DoltStore) DemoteToWisp(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
	if err := s.withRetryTx(ctx, func(tx *sql.Tx) error {
		if _, err := issueops.UpdateIssueWithoutEventInTx(ctx, tx, id, updates, actor); err != nil {
			return fmt.Errorf("update issue before demotion: %w", err)
		}

		issue, err := scanIssueTxFromTable(ctx, tx, "issues", id)
		if err != nil {
			return fmt.Errorf("failed to get updated issue for demotion: %w", err)
		}

		if err := insertIssueTxIntoTable(ctx, tx, "wisps", issue); err != nil {
			return fmt.Errorf("failed to insert issue into wisps: %w", err)
		}

		if _, err := tx.ExecContext(ctx, `
		INSERT IGNORE INTO wisp_labels (issue_id, label)
		SELECT issue_id, label FROM labels WHERE issue_id = ?
	`, id); err != nil {
			return fmt.Errorf("copy labels for demoted issue %s: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM labels WHERE issue_id = ?`, id); err != nil {
			return fmt.Errorf("delete copied labels for demoted issue %s: %w", id, err)
		}

		// Demotion is the inverse of promotion: carry id across so the wisp edge
		// keeps the deterministic key its dependency had. Both tables key id on
		// (issue_id, target), and wisp_dependencies.id also has no DEFAULT now, so
		// the copy is both consistent and required (#4259).
		if _, err := tx.ExecContext(ctx, `
		INSERT IGNORE INTO wisp_dependencies (id, issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external, type, created_at, created_by, metadata, thread_id)
		SELECT id, issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external, type, created_at, created_by, metadata, thread_id
		FROM dependencies WHERE issue_id = ?
	`, id); err != nil {
			return fmt.Errorf("copy dependencies for demoted issue %s: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM dependencies WHERE issue_id = ?`, id); err != nil {
			return fmt.Errorf("delete copied dependencies for demoted issue %s: %w", id, err)
		}

		if _, err := tx.ExecContext(ctx, `
		INSERT IGNORE INTO wisp_events (id, issue_id, event_type, actor, old_value, new_value, comment, created_at)
		SELECT id, issue_id, event_type, actor, old_value, new_value, comment, created_at
		FROM events WHERE issue_id = ?
	`, id); err != nil {
			return fmt.Errorf("copy events for demoted issue %s: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM events WHERE issue_id = ?`, id); err != nil {
			return fmt.Errorf("delete copied events for demoted issue %s: %w", id, err)
		}

		if _, err := tx.ExecContext(ctx, `
		INSERT IGNORE INTO wisp_comments (id, issue_id, author, text, created_at)
		SELECT id, issue_id, author, text, created_at
		FROM comments WHERE issue_id = ?
	`, id); err != nil {
			return fmt.Errorf("copy comments for demoted issue %s: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM comments WHERE issue_id = ?`, id); err != nil {
			return fmt.Errorf("delete copied comments for demoted issue %s: %w", id, err)
		}

		if _, err := tx.ExecContext(ctx, `
		INSERT INTO wisp_events (id, issue_id, event_type, actor, old_value, new_value)
		VALUES (?, ?, ?, ?, ?, ?)
	`, issueops.NewEventID(), id, types.EventUpdated, actor, "", "demoted to wisp"); err != nil {
			return fmt.Errorf("record demotion event for demoted issue %s: %w", id, err)
		}

		if err := issueops.RetargetInboundDependenciesToWispInTx(ctx, tx, id); err != nil {
			return err
		}

		if _, err := tx.ExecContext(ctx, "DELETE FROM issues WHERE id = ?", id); err != nil {
			return fmt.Errorf("failed to delete issue from issues: %w", err)
		}

		affectedIssues, affectedWisps, aerr := issueops.AffectedByStatusChangeForWispInTx(ctx, tx, id)
		if aerr != nil {
			return fmt.Errorf("affected by demote for %s: %w", id, aerr)
		}
		if err := issueops.RecomputeIsBlockedInTx(ctx, tx, affectedIssues, affectedWisps); err != nil {
			return fmt.Errorf("recompute is_blocked after demote for %s: %w", id, err)
		}

		return s.doltAddAndCommitInTx(ctx, tx, permanentIssueAuxTables, fmt.Sprintf("bd: demote %s to wisp", id))
	}); err != nil {
		return err
	}
	return nil
}

func (s *DoltStore) doltAddAndCommitInTx(ctx context.Context, tx *sql.Tx, tables []string, commitMsg string) error {
	for _, table := range tables {
		if _, err := tx.ExecContext(ctx, "CALL DOLT_ADD(?)", table); err != nil {
			return fmt.Errorf("dolt add %s: %w", table, err)
		}
	}
	if _, err := tx.ExecContext(ctx, "CALL DOLT_COMMIT('-m', ?, '--author', ?)",
		commitMsg, s.commitAuthorString()); err != nil && !isDoltNothingToCommit(err) {
		return fmt.Errorf("dolt commit: %w", err)
	}
	return nil
}

// getAllWispDependencyRecords returns all wisp dependency records, keyed by issue_id.
// Used by DetectCycles to include wisp dependencies in cross-table cycle detection. (bd-xe27)
func (s *DoltStore) getAllWispDependencyRecords(ctx context.Context) (map[string][]*types.Dependency, error) {
	rows, err := s.queryContext(ctx, fmt.Sprintf(`
		SELECT issue_id, %s AS depends_on_id, type, created_at, created_by, metadata, thread_id
		FROM wisp_dependencies
		ORDER BY issue_id
	`, issueops.DepTargetExpr))
	if err != nil {
		return nil, fmt.Errorf("failed to get all wisp dependency records: %w", err)
	}
	defer rows.Close()

	result := make(map[string][]*types.Dependency)
	for rows.Next() {
		dep, err := scanDependencyRow(rows)
		if err != nil {
			return nil, fmt.Errorf("get all wisp dependency records: %w", err)
		}
		result[dep.IssueID] = append(result[dep.IssueID], dep)
	}
	return result, rows.Err()
}

// getWispDependencyRecords returns raw dependency records for a wisp from wisp_dependencies.
func (s *DoltStore) getWispDependencyRecords(ctx context.Context, issueID string) ([]*types.Dependency, error) {
	rows, err := s.queryContext(ctx, fmt.Sprintf(`
		SELECT issue_id, %s AS depends_on_id, type, created_at, created_by, metadata, thread_id
		FROM wisp_dependencies
		WHERE issue_id = ?
	`, issueops.DepTargetExpr), issueID)
	if err != nil {
		return nil, fmt.Errorf("failed to get wisp dependency records: %w", err)
	}
	defer rows.Close()

	return scanDependencyRows(rows)
}
