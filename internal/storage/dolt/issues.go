package dolt

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/idgen"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// CreateIssue creates a new issue
func (s *DoltStore) CreateIssue(ctx context.Context, issue *types.Issue, actor string) error {
	// Route ephemeral issues to wisps table
	if issue.Ephemeral {
		return s.createWisp(ctx, issue, actor)
	}

	// Fetch custom statuses and types for validation
	customStatuses, err := s.GetCustomStatuses(ctx)
	if err != nil {
		return fmt.Errorf("failed to get custom statuses: %w", err)
	}
	customTypes, err := s.GetCustomTypes(ctx)
	if err != nil {
		return fmt.Errorf("failed to get custom types: %w", err)
	}

	// Set timestamps (always normalize to UTC since DATETIME columns lose timezone info)
	now := time.Now().UTC()
	if issue.CreatedAt.IsZero() {
		issue.CreatedAt = now
	} else {
		issue.CreatedAt = issue.CreatedAt.UTC()
	}
	if issue.UpdatedAt.IsZero() {
		issue.UpdatedAt = now
	} else {
		issue.UpdatedAt = issue.UpdatedAt.UTC()
	}

	// Defensive fix for closed_at invariant
	if issue.Status == types.StatusClosed && issue.ClosedAt == nil {
		maxTime := issue.CreatedAt
		if issue.UpdatedAt.After(maxTime) {
			maxTime = issue.UpdatedAt
		}
		closedAt := maxTime.Add(time.Second)
		issue.ClosedAt = &closedAt
	}

	// Validate issue
	if err := issue.ValidateWithCustom(customStatuses, customTypes); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	// Compute content hash
	if issue.ContentHash == "" {
		issue.ContentHash = issue.ComputeContentHash()
	}

	// Start transaction
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // No-op after successful commit

	// Get prefix from config
	var configPrefix string
	err = tx.QueryRowContext(ctx, "SELECT value FROM config WHERE `key` = ?", "issue_prefix").Scan(&configPrefix)
	if err == sql.ErrNoRows || configPrefix == "" {
		return fmt.Errorf("%w: issue_prefix config is missing (run 'bd init --prefix <prefix>' first)", storage.ErrNotInitialized)
	} else if err != nil {
		return fmt.Errorf("failed to get config: %w", err)
	}

	// Determine prefix for ID generation
	prefix := configPrefix
	if issue.PrefixOverride != "" {
		prefix = issue.PrefixOverride
	} else if issue.IDPrefix != "" {
		prefix = configPrefix + "-" + issue.IDPrefix
	}

	// Generate or validate ID
	if issue.ID == "" {
		generatedID, err := generateIssueID(ctx, tx, prefix, issue, actor)
		if err != nil {
			return fmt.Errorf("failed to generate issue ID: %w", err)
		}
		issue.ID = generatedID
	}

	// Insert issue
	if err := insertIssue(ctx, tx, issue); err != nil {
		return fmt.Errorf("failed to insert issue: %w", err)
	}

	// Record creation event
	if err := recordEvent(ctx, tx, issue.ID, types.EventCreated, actor, "", ""); err != nil {
		return fmt.Errorf("failed to record creation event: %w", err)
	}

	return tx.Commit()
}

// CreateIssues creates multiple issues in a single transaction
func (s *DoltStore) CreateIssues(ctx context.Context, issues []*types.Issue, actor string) error {
	return s.CreateIssuesWithFullOptions(ctx, issues, actor, storage.BatchCreateOptions{
		OrphanHandling:       storage.OrphanAllow,
		SkipPrefixValidation: false,
	})
}

// CreateIssuesWithFullOptions creates multiple issues with full options control.
// This is the backend-agnostic batch creation method that supports orphan handling
// and prefix validation options.
func (s *DoltStore) CreateIssuesWithFullOptions(ctx context.Context, issues []*types.Issue, actor string, opts storage.BatchCreateOptions) error {
	if len(issues) == 0 {
		return nil
	}

	// Route all-ephemeral batches to wisps table
	allEph := true
	for _, issue := range issues {
		if !issue.Ephemeral {
			allEph = false
			break
		}
	}
	if allEph {
		for _, issue := range issues {
			if err := s.createWisp(ctx, issue, actor); err != nil {
				return err
			}
		}
		return nil
	}

	// Fetch custom statuses and types for validation
	customStatuses, err := s.GetCustomStatuses(ctx)
	if err != nil {
		return fmt.Errorf("failed to get custom statuses: %w", err)
	}
	customTypes, err := s.GetCustomTypes(ctx)
	if err != nil {
		return fmt.Errorf("failed to get custom types: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // No-op after successful commit

	// Get prefix from config for validation
	var configPrefix string
	err = tx.QueryRowContext(ctx, "SELECT value FROM config WHERE `key` = ?", "issue_prefix").Scan(&configPrefix)
	if err == sql.ErrNoRows || configPrefix == "" {
		return fmt.Errorf("%w: issue_prefix config is missing (run 'bd init --prefix <prefix>' first)", storage.ErrNotInitialized)
	} else if err != nil {
		return fmt.Errorf("failed to get config: %w", err)
	}

	for _, issue := range issues {
		now := time.Now().UTC()
		if issue.CreatedAt.IsZero() {
			issue.CreatedAt = now
		}
		if issue.UpdatedAt.IsZero() {
			issue.UpdatedAt = now
		}

		// Defensive fix for closed_at invariant
		if issue.Status == types.StatusClosed && issue.ClosedAt == nil {
			maxTime := issue.CreatedAt
			if issue.UpdatedAt.After(maxTime) {
				maxTime = issue.UpdatedAt
			}
			closedAt := maxTime.Add(time.Second)
			issue.ClosedAt = &closedAt
		}

		// Validate issue
		if err := issue.ValidateWithCustom(customStatuses, customTypes); err != nil {
			return fmt.Errorf("validation failed for issue %s: %w", issue.ID, err)
		}

		if issue.ContentHash == "" {
			issue.ContentHash = issue.ComputeContentHash()
		}

		// Validate prefix if not skipped (for imports with different prefixes)
		if !opts.SkipPrefixValidation && issue.ID != "" {
			if err := validateIssueIDPrefix(issue.ID, configPrefix); err != nil {
				return fmt.Errorf("prefix validation failed for %s: %w", issue.ID, err)
			}
		}

		// Handle orphan checking for hierarchical IDs
		if issue.ID != "" {
			if parentID, _, ok := parseHierarchicalID(issue.ID); ok {
				var parentCount int
				err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM issues WHERE id = ?`, parentID).Scan(&parentCount)
				if err != nil {
					return fmt.Errorf("failed to check parent existence: %w", err)
				}
				if parentCount == 0 {
					switch opts.OrphanHandling {
					case storage.OrphanStrict:
						return fmt.Errorf("parent issue %s does not exist (strict mode)", parentID)
					case storage.OrphanSkip:
						// Skip this issue
						continue
					case storage.OrphanResurrect, storage.OrphanAllow:
						// Allow orphan - continue with insert
					}
				}
			}
		}

		if err := insertIssue(ctx, tx, issue); err != nil {
			return fmt.Errorf("failed to insert issue %s: %w", issue.ID, err)
		}
		if err := recordEvent(ctx, tx, issue.ID, types.EventCreated, actor, "", ""); err != nil {
			return fmt.Errorf("failed to record event for %s: %w", issue.ID, err)
		}
	}

	return tx.Commit()
}

// validateIssueIDPrefix validates that the issue ID has the correct prefix
func validateIssueIDPrefix(id, prefix string) error {
	if !strings.HasPrefix(id, prefix+"-") {
		return fmt.Errorf("%w: issue ID %s does not match configured prefix %s", storage.ErrPrefixMismatch, id, prefix)
	}
	return nil
}

// parseHierarchicalID checks if an ID is hierarchical (e.g., "bd-abc.1") and returns the parent ID and child number
func parseHierarchicalID(id string) (parentID string, childNum int, ok bool) {
	// Find the last dot that separates parent from child number
	lastDot := strings.LastIndex(id, ".")
	if lastDot == -1 {
		return "", 0, false
	}

	parentID = id[:lastDot]
	suffix := id[lastDot+1:]

	// Parse child number
	var num int
	_, err := fmt.Sscanf(suffix, "%d", &num)
	if err != nil {
		return "", 0, false
	}

	return parentID, num, true
}

// GetIssue retrieves an issue by ID.
// Returns storage.ErrNotFound (wrapped) if the issue does not exist.
func (s *DoltStore) GetIssue(ctx context.Context, id string) (*types.Issue, error) {
	// Route ephemeral IDs to wisps table (falls through for promoted wisps)
	if s.isActiveWisp(ctx, id) {
		return s.getWisp(ctx, id)
	}

	s.mu.RLock()
	issue, err := scanIssue(ctx, s.db, id)
	if err != nil {
		s.mu.RUnlock()
		return nil, err
	}
	// Fetch labels
	labels, err := s.GetLabels(ctx, issue.ID)
	s.mu.RUnlock()
	if err != nil {
		return nil, fmt.Errorf("failed to get labels: %w", err)
	}
	issue.Labels = labels
	return issue, nil
}

// GetIssueByExternalRef retrieves an issue by external reference.
// Returns storage.ErrNotFound (wrapped) if no issue with the given external reference exists.
func (s *DoltStore) GetIssueByExternalRef(ctx context.Context, externalRef string) (*types.Issue, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var id string
	err := s.db.QueryRowContext(ctx, "SELECT id FROM issues WHERE external_ref = ?", externalRef).Scan(&id)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("%w: external_ref %s", storage.ErrNotFound, externalRef)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get issue by external_ref: %w", err)
	}

	return s.GetIssue(ctx, id)
}

// UpdateIssue updates fields on an issue
func (s *DoltStore) UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
	// Route ephemeral IDs to wisps table (falls through for promoted wisps)
	if s.isActiveWisp(ctx, id) {
		return s.updateWisp(ctx, id, updates, actor)
	}

	oldIssue, err := s.GetIssue(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to get issue for update: %w", err)
	}

	// Build update query
	setClauses := []string{"updated_at = ?"}
	args := []interface{}{time.Now().UTC()}

	for key, value := range updates {
		if !isAllowedUpdateField(key) {
			return fmt.Errorf("invalid field for update: %s", key)
		}

		columnName := key
		if key == "wisp" {
			columnName = "ephemeral"
		}
		setClauses = append(setClauses, fmt.Sprintf("`%s` = ?", columnName))

		// Handle JSON serialization for array fields stored as TEXT
		if key == "waiters" {
			waitersJSON, _ := json.Marshal(value)
			args = append(args, string(waitersJSON))
		} else if key == "metadata" {
			// GH#1417: Normalize metadata to string, accepting string/[]byte/json.RawMessage
			metadataStr, err := storage.NormalizeMetadataValue(value)
			if err != nil {
				return fmt.Errorf("invalid metadata: %w", err)
			}
			args = append(args, metadataStr)
		} else {
			args = append(args, value)
		}
	}

	// Auto-manage closed_at
	setClauses, args = manageClosedAt(oldIssue, updates, setClauses, args)

	args = append(args, id)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // No-op after successful commit

	// nolint:gosec // G201: setClauses contains only column names (e.g. "status = ?"), actual values passed via args
	query := fmt.Sprintf("UPDATE issues SET %s WHERE id = ?", strings.Join(setClauses, ", "))
	if _, err := tx.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("failed to update issue: %w", err)
	}

	// Record event
	oldData, _ := json.Marshal(oldIssue)
	newData, _ := json.Marshal(updates)
	eventType := determineEventType(oldIssue, updates)

	if err := recordEvent(ctx, tx, id, eventType, actor, string(oldData), string(newData)); err != nil {
		return fmt.Errorf("failed to record event: %w", err)
	}

	return tx.Commit()
}

// ClaimIssue atomically claims an issue using compare-and-swap semantics.
// It sets the assignee to actor and status to "in_progress" only if the issue
// currently has no assignee. Returns storage.ErrAlreadyClaimed if already claimed.
func (s *DoltStore) ClaimIssue(ctx context.Context, id string, actor string) error {
	// Route ephemeral IDs to wisps table (falls through for promoted wisps)
	if s.isActiveWisp(ctx, id) {
		return s.claimWisp(ctx, id, actor)
	}

	oldIssue, err := s.GetIssue(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to get issue for claim: %w", err)
	}

	now := time.Now().UTC()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // No-op after successful commit

	// Use conditional UPDATE with WHERE clause to ensure atomicity.
	// The UPDATE only succeeds if assignee is currently empty.
	result, err := tx.ExecContext(ctx, `
		UPDATE issues
		SET assignee = ?, status = 'in_progress', updated_at = ?
		WHERE id = ? AND (assignee = '' OR assignee IS NULL)
	`, actor, now, id)
	if err != nil {
		return fmt.Errorf("failed to claim issue: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		// The UPDATE didn't affect any rows, which means the assignee was not empty.
		// Query to find out who has it claimed.
		var currentAssignee string
		err := s.db.QueryRowContext(ctx, `SELECT assignee FROM issues WHERE id = ?`, id).Scan(&currentAssignee)
		if err != nil {
			return fmt.Errorf("failed to get current assignee: %w", err)
		}
		return fmt.Errorf("%w by %s", storage.ErrAlreadyClaimed, currentAssignee)
	}

	// Record the claim event
	oldData, _ := json.Marshal(oldIssue)
	newUpdates := map[string]interface{}{
		"assignee": actor,
		"status":   "in_progress",
	}
	newData, _ := json.Marshal(newUpdates)

	if err := recordEvent(ctx, tx, id, "claimed", actor, string(oldData), string(newData)); err != nil {
		return fmt.Errorf("failed to record claim event: %w", err)
	}

	return tx.Commit()
}

// CloseIssue closes an issue with a reason
func (s *DoltStore) CloseIssue(ctx context.Context, id string, reason string, actor string, session string) error {
	// Route ephemeral IDs to wisps table (falls through for promoted wisps)
	if s.isActiveWisp(ctx, id) {
		return s.closeWisp(ctx, id, reason, actor, session)
	}

	now := time.Now().UTC()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // No-op after successful commit

	result, err := tx.ExecContext(ctx, `
		UPDATE issues SET status = ?, closed_at = ?, updated_at = ?, close_reason = ?, closed_by_session = ?
		WHERE id = ?
	`, types.StatusClosed, now, now, reason, session, id)
	if err != nil {
		return fmt.Errorf("failed to close issue: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("issue not found: %s", id)
	}

	if err := recordEvent(ctx, tx, id, types.EventClosed, actor, "", reason); err != nil {
		return fmt.Errorf("failed to record event: %w", err)
	}

	return tx.Commit()
}

// DeleteIssue permanently removes an issue
func (s *DoltStore) DeleteIssue(ctx context.Context, id string) error {
	// Route ephemeral IDs to wisps table (falls through for promoted wisps)
	if s.isActiveWisp(ctx, id) {
		return s.deleteWisp(ctx, id)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // No-op after successful commit

	// Delete related data (foreign keys will cascade, but be explicit)
	tables := []string{"dependencies", "events", "comments", "labels"}
	for _, table := range tables {
		// Validate table name to prevent SQL injection (tables are hardcoded above,
		// but validate defensively in case the list is ever modified)
		if err := validateTableName(table); err != nil {
			return fmt.Errorf("invalid table name %q: %w", table, err)
		}
		if table == "dependencies" {
			_, err = tx.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE issue_id = ? OR depends_on_id = ?", table), id, id) //nolint:gosec // G201: table validated by validateTableName above
		} else {
			_, err = tx.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE issue_id = ?", table), id) //nolint:gosec // G201: table validated by validateTableName above
		}
		if err != nil {
			return fmt.Errorf("failed to delete from %s: %w", table, err)
		}
	}

	result, err := tx.ExecContext(ctx, "DELETE FROM issues WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("failed to delete issue: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("issue not found: %s", id)
	}

	return tx.Commit()
}

// DeleteIssues deletes multiple issues in a single transaction.
// If cascade is true, recursively deletes dependents.
// If cascade is false but force is true, deletes issues and orphans dependents.
// If both are false, returns an error if any issue has dependents.
// If dryRun is true, only computes statistics without deleting.
// deleteBatchSize controls the maximum number of IDs per IN-clause query.
// Kept small to avoid large IN-clause queries. See steveyegge/beads#1692.
const deleteBatchSize = 50

func (s *DoltStore) DeleteIssues(ctx context.Context, ids []string, cascade bool, force bool, dryRun bool) (*types.DeleteIssuesResult, error) {
	if len(ids) == 0 {
		return &types.DeleteIssuesResult{}, nil
	}

	idSet := make(map[string]bool, len(ids))
	for _, id := range ids {
		idSet[id] = true
	}

	result := &types.DeleteIssuesResult{}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // No-op after successful commit

	// Resolve the full set of IDs to delete
	expandedIDs := ids
	if cascade {
		allToDelete, err := s.findAllDependentsRecursiveTx(ctx, tx, ids)
		if err != nil {
			return nil, fmt.Errorf("failed to find dependents: %w", err)
		}
		expandedIDs = make([]string, 0, len(allToDelete))
		for id := range allToDelete {
			expandedIDs = append(expandedIDs, id)
		}
	} else if !force {
		// Check for external dependents using batched queries.
		// We need to identify which specific issue has external deps for the error message.
		for i := 0; i < len(ids); i += deleteBatchSize {
			end := i + deleteBatchSize
			if end > len(ids) {
				end = len(ids)
			}
			batch := ids[i:end]
			inClause, args := doltBuildSQLInClause(batch)

			rows, err := tx.QueryContext(ctx,
				fmt.Sprintf(`SELECT depends_on_id, issue_id FROM dependencies WHERE depends_on_id IN (%s)`, inClause),
				args...)
			if err != nil {
				return nil, fmt.Errorf("failed to check dependents: %w", err)
			}

			externalBySource := make(map[string][]string) // depends_on_id -> external issue_ids
			for rows.Next() {
				var depOnID, issueID string
				if err := rows.Scan(&depOnID, &issueID); err != nil {
					_ = rows.Close()
					return nil, fmt.Errorf("failed to scan dependent: %w", err)
				}
				if !idSet[issueID] {
					externalBySource[depOnID] = append(externalBySource[depOnID], issueID)
				}
			}
			_ = rows.Close()
			if err := rows.Err(); err != nil {
				return nil, fmt.Errorf("failed to iterate dependents: %w", err)
			}

			// Return error for the first issue in this batch that has external dependents.
			// Return result (not nil) so the caller can inspect OrphanedIssues even on error.
			for _, id := range batch {
				if deps, ok := externalBySource[id]; ok {
					result.OrphanedIssues = deps
					return result, fmt.Errorf("issue %s has dependents not in deletion set; use --cascade to delete them or --force to orphan them", id)
				}
			}
		}
	} else {
		// Force mode: track orphaned issues using batched queries
		orphans, err := s.findExternalDependentsBatched(ctx, tx, ids, idSet)
		if err != nil {
			return nil, fmt.Errorf("failed to get dependents: %w", err)
		}
		result.OrphanedIssues = orphans
	}

	// Populate stats using batched queries. Dependency counting is split into two
	// non-overlapping passes to prevent double-counting: a row where both issue_id
	// and depends_on_id are in expandedIDs would be counted twice with a single
	// OR query per batch.
	//   Pass 1: COUNT WHERE issue_id IN (batch)       — deps FROM deleted issues
	//   Pass 2: COUNT WHERE depends_on_id IN (batch)   — deps TO deleted issues
	//           AND issue_id NOT in expandedIDSet       — excluding already-counted rows
	// The second pass filters in Go since the full set may exceed one IN clause.
	expandedIDSet := make(map[string]bool, len(expandedIDs))
	for _, id := range expandedIDs {
		expandedIDSet[id] = true
	}

	var depsCount, labelsCount, eventsCount int
	// Pass 1: deps originating from deleted issues (no cross-batch overlap possible)
	for i := 0; i < len(expandedIDs); i += deleteBatchSize {
		end := i + deleteBatchSize
		if end > len(expandedIDs) {
			end = len(expandedIDs)
		}
		batch := expandedIDs[i:end]
		batchInClause, batchArgs := doltBuildSQLInClause(batch)

		var batchDeps int
		err = tx.QueryRowContext(ctx,
			fmt.Sprintf(`SELECT COUNT(*) FROM dependencies WHERE issue_id IN (%s)`, batchInClause),
			batchArgs...).Scan(&batchDeps)
		if err != nil {
			return nil, fmt.Errorf("failed to count dependencies: %w", err)
		}
		depsCount += batchDeps

		var batchLabels int
		err = tx.QueryRowContext(ctx,
			fmt.Sprintf(`SELECT COUNT(*) FROM labels WHERE issue_id IN (%s)`, batchInClause),
			batchArgs...).Scan(&batchLabels)
		if err != nil {
			return nil, fmt.Errorf("failed to count labels: %w", err)
		}
		labelsCount += batchLabels

		var batchEvents int
		err = tx.QueryRowContext(ctx,
			fmt.Sprintf(`SELECT COUNT(*) FROM events WHERE issue_id IN (%s)`, batchInClause),
			batchArgs...).Scan(&batchEvents)
		if err != nil {
			return nil, fmt.Errorf("failed to count events: %w", err)
		}
		eventsCount += batchEvents
	}
	// Pass 2: inbound deps from outside the deletion set (pointing TO deleted issues)
	for i := 0; i < len(expandedIDs); i += deleteBatchSize {
		end := i + deleteBatchSize
		if end > len(expandedIDs) {
			end = len(expandedIDs)
		}
		batch := expandedIDs[i:end]
		batchInClause, batchArgs := doltBuildSQLInClause(batch)

		rows, err := tx.QueryContext(ctx,
			fmt.Sprintf(`SELECT issue_id FROM dependencies WHERE depends_on_id IN (%s)`, batchInClause),
			batchArgs...)
		if err != nil {
			return nil, fmt.Errorf("failed to count inbound dependencies: %w", err)
		}
		for rows.Next() {
			var issID string
			if err := rows.Scan(&issID); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("failed to scan inbound dependency: %w", err)
			}
			if !expandedIDSet[issID] {
				depsCount++
			}
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("failed to iterate inbound dependencies: %w", err)
		}
	}
	result.DependenciesCount = depsCount
	result.LabelsCount = labelsCount
	result.EventsCount = eventsCount
	result.DeletedCount = len(expandedIDs)

	if dryRun {
		return result, nil
	}

	// Delete in batches. The schema uses ON DELETE CASCADE for labels, comments,
	// events, child_counters, issue_snapshots, and compaction_snapshots — as well
	// as dependencies.issue_id — so only the inbound dependency edge
	// (depends_on_id, which has no FK) needs explicit cleanup before issuing the
	// DELETE FROM issues.
	totalDeleted := 0
	for i := 0; i < len(expandedIDs); i += deleteBatchSize {
		end := i + deleteBatchSize
		if end > len(expandedIDs) {
			end = len(expandedIDs)
		}
		batch := expandedIDs[i:end]
		batchInClause, batchArgs := doltBuildSQLInClause(batch)

		// 1. Delete inbound dependency edges (depends_on_id has no FK CASCADE)
		_, err = tx.ExecContext(ctx,
			fmt.Sprintf(`DELETE FROM dependencies WHERE depends_on_id IN (%s)`, batchInClause),
			batchArgs...)
		if err != nil {
			return nil, fmt.Errorf("failed to delete inbound dependencies: %w", err)
		}

		// 2. Delete the issues — CASCADE handles labels, comments, events,
		//    child_counters, issue_snapshots, compaction_snapshots, and
		//    dependencies (issue_id side via fk_dep_issue).
		deleteResult, err := tx.ExecContext(ctx,
			fmt.Sprintf(`DELETE FROM issues WHERE id IN (%s)`, batchInClause),
			batchArgs...)
		if err != nil {
			return nil, fmt.Errorf("failed to delete issues: %w", err)
		}
		rowsAffected, _ := deleteResult.RowsAffected()
		totalDeleted += int(rowsAffected)
	}
	result.DeletedCount = totalDeleted

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %w", err)
	}

	return result, nil
}

// maxRecursiveResults is the safety limit for the total number of issues discovered
// during recursive dependent traversal. Prevents pathological dependency graphs
// from causing unbounded memory/time consumption.
const maxRecursiveResults = 10000

// findAllDependentsRecursiveTx finds all issues that depend on the given issues, recursively (within a transaction).
// Uses batched IN-clause queries instead of per-ID queries to avoid N+1 performance problems
// that hang on embedded Dolt with large ID sets (see steveyegge/beads#1692).
// Traversal is capped at maxRecursiveResults total discovered IDs.
func (s *DoltStore) findAllDependentsRecursiveTx(ctx context.Context, tx *sql.Tx, ids []string) (map[string]bool, error) {
	result := make(map[string]bool)
	for _, id := range ids {
		result[id] = true
	}

	toProcess := make([]string, len(ids))
	copy(toProcess, ids)

	for len(toProcess) > 0 {
		if len(result) > maxRecursiveResults {
			return nil, fmt.Errorf("cascade traversal discovered over %d issues; aborting to prevent runaway deletion", maxRecursiveResults)
		}
		// Take a batch of IDs to process
		batchEnd := deleteBatchSize
		if batchEnd > len(toProcess) {
			batchEnd = len(toProcess)
		}
		batch := toProcess[:batchEnd]
		toProcess = toProcess[batchEnd:]

		inClause, args := doltBuildSQLInClause(batch)
		rows, err := tx.QueryContext(ctx,
			fmt.Sprintf(`SELECT issue_id FROM dependencies WHERE depends_on_id IN (%s)`, inClause),
			args...)
		if err != nil {
			return nil, fmt.Errorf("failed to query dependents for batch: %w", err)
		}

		for rows.Next() {
			var depID string
			if err := rows.Scan(&depID); err != nil {
				_ = rows.Close() // Best effort cleanup on error path
				return nil, fmt.Errorf("failed to scan dependent: %w", err)
			}
			if !result[depID] {
				result[depID] = true
				toProcess = append(toProcess, depID)
			}
		}
		_ = rows.Close() // Redundant close for safety (rows already iterated)
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("failed to iterate dependents for batch: %w", err)
		}
	}

	return result, nil
}

// findExternalDependentsBatched finds all dependents of the given IDs that are NOT in the idSet.
// Uses batched IN-clause queries instead of per-ID queries.
func (s *DoltStore) findExternalDependentsBatched(ctx context.Context, tx *sql.Tx, ids []string, idSet map[string]bool) ([]string, error) {
	orphanSet := make(map[string]bool)
	for i := 0; i < len(ids); i += deleteBatchSize {
		end := i + deleteBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[i:end]
		inClause, args := doltBuildSQLInClause(batch)

		rows, err := tx.QueryContext(ctx,
			fmt.Sprintf(`SELECT issue_id FROM dependencies WHERE depends_on_id IN (%s)`, inClause),
			args...)
		if err != nil {
			return nil, fmt.Errorf("failed to query dependents: %w", err)
		}
		for rows.Next() {
			var depID string
			if err := rows.Scan(&depID); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("failed to scan dependent: %w", err)
			}
			if !idSet[depID] {
				orphanSet[depID] = true
			}
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("failed to iterate dependents: %w", err)
		}
	}

	result := make([]string, 0, len(orphanSet))
	for id := range orphanSet {
		result = append(result, id)
	}
	return result, nil
}

// doltBuildSQLInClause builds a parameterized IN clause for SQL queries
func doltBuildSQLInClause(ids []string) (string, []interface{}) {
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	return strings.Join(placeholders, ","), args
}

// =============================================================================
// Helper functions
// =============================================================================

func insertIssue(ctx context.Context, tx *sql.Tx, issue *types.Issue) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO issues (
			id, content_hash, title, description, design, acceptance_criteria, notes,
			status, priority, issue_type, assignee, estimated_minutes,
			created_at, created_by, owner, updated_at, closed_at, external_ref, spec_id,
			compaction_level, compacted_at, compacted_at_commit, original_size,
			sender, ephemeral, wisp_type, pinned, is_template, crystallizes,
			mol_type, work_type, quality_score, source_system, source_repo, close_reason,
			event_kind, actor, target, payload,
			await_type, await_id, timeout_ns, waiters,
			hook_bead, role_bead, agent_state, last_activity, role_type, rig,
			due_at, defer_until, metadata
		) VALUES (
			?, ?, ?, ?, ?, ?, ?,
			?, ?, ?, ?, ?,
			?, ?, ?, ?, ?, ?, ?,
			?, ?, ?, ?,
			?, ?, ?, ?, ?, ?,
			?, ?, ?, ?, ?, ?,
			?, ?, ?, ?,
			?, ?, ?, ?,
			?, ?, ?, ?, ?, ?,
			?, ?, ?
		)
	`,
		issue.ID, issue.ContentHash, issue.Title, issue.Description, issue.Design, issue.AcceptanceCriteria, issue.Notes,
		issue.Status, issue.Priority, issue.IssueType, nullString(issue.Assignee), nullInt(issue.EstimatedMinutes),
		issue.CreatedAt, issue.CreatedBy, issue.Owner, issue.UpdatedAt, issue.ClosedAt, nullStringPtr(issue.ExternalRef), issue.SpecID,
		issue.CompactionLevel, issue.CompactedAt, nullStringPtr(issue.CompactedAtCommit), nullIntVal(issue.OriginalSize),
		issue.Sender, issue.Ephemeral, issue.WispType, issue.Pinned, issue.IsTemplate, issue.Crystallizes,
		issue.MolType, issue.WorkType, issue.QualityScore, issue.SourceSystem, issue.SourceRepo, issue.CloseReason,
		issue.EventKind, issue.Actor, issue.Target, issue.Payload,
		issue.AwaitType, issue.AwaitID, issue.Timeout.Nanoseconds(), formatJSONStringArray(issue.Waiters),
		issue.HookBead, issue.RoleBead, issue.AgentState, issue.LastActivity, issue.RoleType, issue.Rig,
		issue.DueAt, issue.DeferUntil, jsonMetadata(issue.Metadata),
	)
	return err
}

func scanIssue(ctx context.Context, db *sql.DB, id string) (*types.Issue, error) {
	row := db.QueryRowContext(ctx, `
		SELECT `+issueSelectColumns+`
		FROM issues
		WHERE id = ?
	`, id)

	issue, err := scanIssueFrom(row)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("%w: issue %s", storage.ErrNotFound, id)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get issue: %w", err)
	}
	return issue, nil
}

func recordEvent(ctx context.Context, tx *sql.Tx, issueID string, eventType types.EventType, actor, oldValue, newValue string) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO events (issue_id, event_type, actor, old_value, new_value)
		VALUES (?, ?, ?, ?, ?)
	`, issueID, eventType, actor, oldValue, newValue)
	return err
}

// generateIssueID generates a unique hash-based ID for an issue
// Uses adaptive length based on database size and tries multiple nonces on collision
func generateIssueID(ctx context.Context, tx *sql.Tx, prefix string, issue *types.Issue, actor string) (string, error) {
	// Get adaptive base length based on current database size
	baseLength, err := GetAdaptiveIDLengthTx(ctx, tx, prefix)
	if err != nil {
		// Fallback to 6 on error
		baseLength = 6
	}

	// Try baseLength, baseLength+1, baseLength+2, up to max of 8
	maxLength := 8
	if baseLength > maxLength {
		baseLength = maxLength
	}

	for length := baseLength; length <= maxLength; length++ {
		// Try up to 10 nonces at each length
		for nonce := 0; nonce < 10; nonce++ {
			candidate := generateHashID(prefix, issue.Title, issue.Description, actor, issue.CreatedAt, length, nonce)

			// Check if this ID already exists
			var count int
			err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM issues WHERE id = ?`, candidate).Scan(&count)
			if err != nil {
				return "", fmt.Errorf("failed to check for ID collision: %w", err)
			}

			if count == 0 {
				return candidate, nil
			}
		}
	}

	return "", fmt.Errorf("failed to generate unique ID after trying lengths %d-%d with 10 nonces each", baseLength, maxLength)
}

// generateHashID creates a hash-based ID for a top-level issue.
// Uses base36 encoding (0-9, a-z) for better information density than hex.
func generateHashID(prefix, title, description, creator string, timestamp time.Time, length, nonce int) string {
	return idgen.GenerateHashID(prefix, title, description, creator, timestamp, length, nonce)
}

func isAllowedUpdateField(key string) bool {
	allowed := map[string]bool{
		"status": true, "priority": true, "title": true, "assignee": true,
		"description": true, "design": true, "acceptance_criteria": true, "notes": true,
		"issue_type": true, "estimated_minutes": true, "external_ref": true, "spec_id": true,
		"closed_at": true, "close_reason": true, "closed_by_session": true,
		"source_repo": true,
		"sender":      true, "wisp": true, "wisp_type": true, "pinned": true,
		"hook_bead": true, "role_bead": true, "agent_state": true, "last_activity": true,
		"role_type": true, "rig": true, "mol_type": true,
		"event_category": true, "event_actor": true, "event_target": true, "event_payload": true,
		"due_at": true, "defer_until": true, "await_id": true, "waiters": true,
		"metadata": true,
	}
	return allowed[key]
}

func manageClosedAt(oldIssue *types.Issue, updates map[string]interface{}, setClauses []string, args []interface{}) ([]string, []interface{}) {
	statusVal, hasStatus := updates["status"]
	_, hasExplicitClosedAt := updates["closed_at"]
	if hasExplicitClosedAt || !hasStatus {
		return setClauses, args
	}

	var newStatus string
	switch v := statusVal.(type) {
	case string:
		newStatus = v
	case types.Status:
		newStatus = string(v)
	default:
		return setClauses, args
	}

	if newStatus == string(types.StatusClosed) {
		now := time.Now().UTC()
		setClauses = append(setClauses, "closed_at = ?")
		args = append(args, now)
	} else if oldIssue.Status == types.StatusClosed {
		setClauses = append(setClauses, "closed_at = ?", "close_reason = ?")
		args = append(args, nil, "")
	}

	return setClauses, args
}

func determineEventType(oldIssue *types.Issue, updates map[string]interface{}) types.EventType {
	statusVal, hasStatus := updates["status"]
	if !hasStatus {
		return types.EventUpdated
	}

	newStatus, ok := statusVal.(string)
	if !ok {
		return types.EventUpdated
	}

	if newStatus == string(types.StatusClosed) {
		return types.EventClosed
	}
	if oldIssue.Status == types.StatusClosed {
		return types.EventReopened
	}
	return types.EventStatusChanged
}

// Helper functions for nullable values
func nullString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func nullStringPtr(s *string) interface{} {
	if s == nil {
		return nil
	}
	return *s
}

func nullInt(i *int) interface{} {
	if i == nil {
		return nil
	}
	return *i
}

func nullIntVal(i int) interface{} {
	if i == 0 {
		return nil
	}
	return i
}

// jsonMetadata returns the metadata as a string, or "{}" if empty.
// Dolt's JSON column type requires valid JSON, so we can't insert empty strings.
func jsonMetadata(m []byte) string {
	if len(m) == 0 {
		return "{}"
	}
	return string(m)
}

func parseJSONStringArray(s string) []string {
	if s == "" {
		return nil
	}
	var result []string
	if err := json.Unmarshal([]byte(s), &result); err != nil {
		return nil
	}
	return result
}

// DeleteIssuesBySourceRepo permanently removes all issues from a specific source repository.
// This is used when a repo is removed from the multi-repo configuration.
// It also cleans up related data: dependencies, labels, comments, and events.
// Returns the number of issues deleted.
func (s *DoltStore) DeleteIssuesBySourceRepo(ctx context.Context, sourceRepo string) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // No-op after successful commit

	// Get the list of issue IDs to delete
	rows, err := tx.QueryContext(ctx, `SELECT id FROM issues WHERE source_repo = ?`, sourceRepo)
	if err != nil {
		return 0, fmt.Errorf("failed to query issues: %w", err)
	}
	var issueIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close() // Best effort cleanup on error path
			return 0, fmt.Errorf("failed to scan issue ID: %w", err)
		}
		issueIDs = append(issueIDs, id)
	}
	_ = rows.Close() // Redundant close for safety (rows already iterated)
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("failed to iterate issues: %w", err)
	}

	if len(issueIDs) == 0 {
		if err := tx.Commit(); err != nil {
			return 0, fmt.Errorf("failed to commit empty transaction: %w", err)
		}
		return 0, nil
	}

	// Delete related data for all affected issues
	tables := []string{"dependencies", "events", "comments", "labels"}
	for _, table := range tables {
		if err := validateTableName(table); err != nil {
			return 0, fmt.Errorf("invalid table name %q: %w", table, err)
		}
		for _, id := range issueIDs {
			if table == "dependencies" {
				_, err = tx.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE issue_id = ? OR depends_on_id = ?", table), id, id) //nolint:gosec // G201: table validated by validateTableName above
			} else {
				_, err = tx.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE issue_id = ?", table), id) //nolint:gosec // G201: table validated by validateTableName above
			}
			if err != nil {
				return 0, fmt.Errorf("failed to delete from %s for %s: %w", table, id, err)
			}
		}
	}

	// Delete the issues themselves
	result, err := tx.ExecContext(ctx, `DELETE FROM issues WHERE source_repo = ?`, sourceRepo)
	if err != nil {
		return 0, fmt.Errorf("failed to delete issues: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("failed to check rows affected: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("failed to commit transaction: %w", err)
	}

	return int(rowsAffected), nil
}

// ClearRepoMtime removes the mtime cache entry for a repository.
// This is used when a repo is removed from the multi-repo configuration.
func (s *DoltStore) ClearRepoMtime(ctx context.Context, repoPath string) error {
	// Expand tilde in path to match how it's stored
	expandedPath := repoPath
	if strings.HasPrefix(repoPath, "~") {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("failed to get home directory: %w", err)
		}
		if repoPath == "~" {
			expandedPath = homeDir
		} else {
			expandedPath = filepath.Join(homeDir, repoPath[1:])
		}
	}

	// Get absolute path to match how it's stored in repo_mtimes
	absRepoPath, err := filepath.Abs(expandedPath)
	if err != nil {
		return fmt.Errorf("failed to get absolute path: %w", err)
	}

	_, err = s.execContext(ctx, `DELETE FROM repo_mtimes WHERE repo_path = ?`, absRepoPath)
	if err != nil {
		return fmt.Errorf("failed to delete mtime cache: %w", err)
	}

	return nil
}

func formatJSONStringArray(arr []string) string {
	if len(arr) == 0 {
		return ""
	}
	data, err := json.Marshal(arr)
	if err != nil {
		return ""
	}
	return string(data)
}
