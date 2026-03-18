package dolt

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// IsEphemeralID returns true if the ID belongs to an ephemeral issue.
func IsEphemeralID(id string) bool {
	return strings.Contains(id, "-wisp-")
}

// DefaultInfraTypes returns a copy of the built-in infrastructure types.
// Delegates to storage.DefaultInfraTypes.
func DefaultInfraTypes() []string {
	return storage.DefaultInfraTypes()
}

// IsInfraType returns true if the issue type is infrastructure.
// Delegates to storage.IsInfraType.
func IsInfraType(t types.IssueType) bool {
	return storage.IsInfraType(t)
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
	issue, err := s.getWisp(ctx, id)
	if errors.Is(err, storage.ErrNotFound) {
		return fmt.Errorf("wisp %s not found", id)
	}
	if err != nil {
		return wrapDBError("get wisp for promote", err)
	}
	if issue == nil {
		return fmt.Errorf("wisp %s not found", id)
	}

	// Clear ephemeral flag for persistent storage
	issue.Ephemeral = false

	// Create in issues table (bypasses ephemeral routing since Ephemeral=false)
	if err := s.CreateIssue(ctx, issue, actor); err != nil {
		return fmt.Errorf("failed to promote wisp to issues: %w", err)
	}

	// Copy labels directly to permanent labels table (bypass IsEphemeralID routing)
	labels, err := s.getWispLabels(ctx, id)
	if err != nil {
		return wrapQueryError("get wisp labels for promote", err)
	}
	for _, label := range labels {
		if _, err := s.execContext(ctx,
			`INSERT IGNORE INTO labels (issue_id, label) VALUES (?, ?)`,
			id, label); err != nil {
			return fmt.Errorf("failed to copy label %q: %w", label, err)
		}
	}

	// Copy dependencies directly to permanent dependencies table
	deps, err := s.getWispDependencyRecords(ctx, id)
	if err != nil {
		return wrapQueryError("get wisp dependencies for promote", err)
	}
	for _, dep := range deps {
		metadata := dep.Metadata
		if metadata == "" {
			metadata = "{}"
		}
		if _, err := s.execContext(ctx, `
			INSERT IGNORE INTO dependencies (issue_id, depends_on_id, type, created_at, created_by, metadata, thread_id)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`, dep.IssueID, dep.DependsOnID, dep.Type, dep.CreatedAt, dep.CreatedBy, metadata, dep.ThreadID); err != nil {
			// Skip if target doesn't exist (external ref to other wisp)
			if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "foreign key") {
				continue
			}
			return fmt.Errorf("failed to copy dependency: %w", err)
		}
	}

	// Copy events via INSERT...SELECT (best-effort: log but don't fail promotion)
	if _, err := s.execContext(ctx, `
		INSERT IGNORE INTO events (issue_id, event_type, actor, old_value, new_value, comment, created_at)
		SELECT issue_id, event_type, actor, old_value, new_value, comment, created_at
		FROM wisp_events WHERE issue_id = ?
	`, id); err != nil {
		log.Printf("promote %s: failed to copy events (data may be lost): %v", id, err)
	}

	// Copy comments via INSERT...SELECT (best-effort: log but don't fail promotion)
	if _, err := s.execContext(ctx, `
		INSERT IGNORE INTO comments (issue_id, author, text, created_at)
		SELECT issue_id, author, text, created_at
		FROM wisp_comments WHERE issue_id = ?
	`, id); err != nil {
		log.Printf("promote %s: failed to copy comments (data may be lost): %v", id, err)
	}

	// Delete from wisps table (and all wisp_* auxiliary tables)
	return s.deleteWisp(ctx, id)
}

// DemoteToWisp moves an issue from the issues table to the wisps table.
// This is the inverse of PromoteFromEphemeral. It applies any provided updates
// (e.g., setting no_history or ephemeral) to the issue in-memory, then migrates
// it atomically: insert into wisps, copy auxiliary data, delete from issues.
//
// Called by UpdateIssue when no_history=true or wisp=true is set on a regular issue.
func (s *DoltStore) DemoteToWisp(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
	// Read the current issue from the issues table.
	issue, err := scanIssueFromTable(ctx, s.db, "issues", id)
	if err != nil {
		return fmt.Errorf("failed to get issue for demotion: %w", err)
	}

	// Apply in-memory updates so the wisps row reflects all requested changes.
	applyUpdatesToIssueStruct(issue, updates)

	// Begin a single transaction for the insert + delete + dolt commit.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Insert into wisps table.
	if err := insertIssueTxIntoTable(ctx, tx, "wisps", issue); err != nil {
		return fmt.Errorf("failed to insert issue into wisps: %w", err)
	}

	// Copy labels: labels → wisp_labels.
	if _, err := tx.ExecContext(ctx, `
		INSERT IGNORE INTO wisp_labels (issue_id, label)
		SELECT issue_id, label FROM labels WHERE issue_id = ?
	`, id); err != nil {
		log.Printf("demote %s: failed to copy labels (data may be lost): %v", id, err)
	}

	// Copy dependencies: dependencies → wisp_dependencies.
	if _, err := tx.ExecContext(ctx, `
		INSERT IGNORE INTO wisp_dependencies (issue_id, depends_on_id, type, created_at, created_by, metadata, thread_id)
		SELECT issue_id, depends_on_id, type, created_at, created_by, metadata, thread_id
		FROM dependencies WHERE issue_id = ?
	`, id); err != nil {
		log.Printf("demote %s: failed to copy dependencies (data may be lost): %v", id, err)
	}

	// Copy events: events → wisp_events.
	if _, err := tx.ExecContext(ctx, `
		INSERT IGNORE INTO wisp_events (issue_id, event_type, actor, old_value, new_value, comment, created_at)
		SELECT issue_id, event_type, actor, old_value, new_value, comment, created_at
		FROM events WHERE issue_id = ?
	`, id); err != nil {
		log.Printf("demote %s: failed to copy events (data may be lost): %v", id, err)
	}

	// Copy comments: comments → wisp_comments.
	if _, err := tx.ExecContext(ctx, `
		INSERT IGNORE INTO wisp_comments (issue_id, author, text, created_at)
		SELECT issue_id, author, text, created_at
		FROM comments WHERE issue_id = ?
	`, id); err != nil {
		log.Printf("demote %s: failed to copy comments (data may be lost): %v", id, err)
	}

	// Record a demotion event in wisp_events.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO wisp_events (issue_id, event_type, actor, old_value, new_value)
		VALUES (?, ?, ?, ?, ?)
	`, id, types.EventUpdated, actor, "", "demoted to wisp"); err != nil {
		log.Printf("demote %s: failed to record demotion event: %v", id, err)
	}

	// Delete from permanent auxiliary tables.
	for _, table := range []string{"dependencies", "events", "comments", "labels"} {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE issue_id = ?", table), id); //nolint:gosec // G201: table is hardcoded
		err != nil {
			return fmt.Errorf("failed to delete from %s: %w", table, err)
		}
	}

	// Delete from issues table.
	if _, err := tx.ExecContext(ctx, "DELETE FROM issues WHERE id = ?", id); err != nil {
		return fmt.Errorf("failed to delete issue from issues: %w", err)
	}

	// Dolt commit to record the removal from versioned tables.
	for _, table := range []string{"issues", "labels", "dependencies", "events", "comments"} {
		_, _ = tx.ExecContext(ctx, "CALL DOLT_ADD(?)", table)
	}
	commitMsg := fmt.Sprintf("bd: demote %s to wisp", id)
	if _, err := tx.ExecContext(ctx, "CALL DOLT_COMMIT('-m', ?, '--author', ?)",
		commitMsg, s.commitAuthorString()); err != nil && !isDoltNothingToCommit(err) {
		return fmt.Errorf("dolt commit after demotion: %w", err)
	}

	return wrapTransactionError("commit demote to wisp", tx.Commit())
}

// applyUpdatesToIssueStruct applies an updates map (as used by UpdateIssue) to
// an Issue struct in memory. Used by DemoteToWisp so the wisps row reflects all
// requested field changes alongside the routing-flag change.
func applyUpdatesToIssueStruct(issue *types.Issue, updates map[string]interface{}) {
	now := time.Now().UTC()
	issue.UpdatedAt = now

	for key, value := range updates {
		switch key {
		case "status":
			if v, ok := value.(string); ok {
				issue.Status = types.Status(v)
			}
		case "title":
			if v, ok := value.(string); ok {
				issue.Title = v
			}
		case "description":
			if v, ok := value.(string); ok {
				issue.Description = v
			}
		case "design":
			if v, ok := value.(string); ok {
				issue.Design = v
			}
		case "notes":
			if v, ok := value.(string); ok {
				issue.Notes = v
			}
		case "assignee":
			if v, ok := value.(string); ok {
				issue.Assignee = v
			}
		case "priority":
			if v, ok := value.(int); ok {
				issue.Priority = v
			}
		case "issue_type":
			if v, ok := value.(string); ok {
				issue.IssueType = types.IssueType(v)
			}
		case "wisp":
			if v, ok := value.(bool); ok {
				issue.Ephemeral = v
			}
		case "no_history":
			if v, ok := value.(bool); ok {
				issue.NoHistory = v
			}
		case "acceptance_criteria":
			if v, ok := value.(string); ok {
				issue.AcceptanceCriteria = v
			}
		case "external_ref":
			if v, ok := value.(string); ok {
				issue.ExternalRef = &v
			}
		case "spec_id":
			if v, ok := value.(string); ok {
				issue.SpecID = v
			}
		case "estimated_minutes":
			if v, ok := value.(int); ok {
				issue.EstimatedMinutes = &v
			}
		}
		// closed_at is managed by manageClosedAt; skip here.
		// Labels, metadata, and other complex fields are handled outside this path.
	}
}

// getAllWispDependencyRecords returns all wisp dependency records, keyed by issue_id.
// Used by DetectCycles to include wisp dependencies in cross-table cycle detection. (bd-xe27)
func (s *DoltStore) getAllWispDependencyRecords(ctx context.Context) (map[string][]*types.Dependency, error) {
	rows, err := s.queryContext(ctx, `
		SELECT issue_id, depends_on_id, type, created_at, created_by, metadata, thread_id
		FROM wisp_dependencies
		ORDER BY issue_id
	`)
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
	rows, err := s.queryContext(ctx, `
		SELECT issue_id, depends_on_id, type, created_at, created_by, metadata, thread_id
		FROM wisp_dependencies
		WHERE issue_id = ?
	`, issueID)
	if err != nil {
		return nil, fmt.Errorf("failed to get wisp dependency records: %w", err)
	}
	defer rows.Close()

	return scanDependencyRows(rows)
}
