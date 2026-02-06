//go:build cgo
package dolt

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/idgen"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// CreateIssue creates a new issue
func (s *DoltStore) CreateIssue(ctx context.Context, issue *types.Issue, actor string) error {
	// Fetch custom statuses and types for validation
	customStatuses, err := s.GetCustomStatuses(ctx)
	if err != nil {
		return fmt.Errorf("failed to get custom statuses: %w", err)
	}
	customTypes, err := s.GetCustomTypes(ctx)
	if err != nil {
		return fmt.Errorf("failed to get custom types: %w", err)
	}

	// Set timestamps
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

	// Defensive fix for deleted_at invariant
	if issue.Status == types.StatusTombstone && issue.DeletedAt == nil {
		maxTime := issue.CreatedAt
		if issue.UpdatedAt.After(maxTime) {
			maxTime = issue.UpdatedAt
		}
		deletedAt := maxTime.Add(time.Second)
		issue.DeletedAt = &deletedAt
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
	defer func() { _ = tx.Rollback() }()

	// Get prefix from config
	var configPrefix string
	err = tx.QueryRowContext(ctx, "SELECT value FROM config WHERE `key` = ?", "issue_prefix").Scan(&configPrefix)
	if err == sql.ErrNoRows || configPrefix == "" {
		return fmt.Errorf("database not initialized: issue_prefix config is missing (run 'bd init --prefix <prefix>' first)")
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

	// Mark issue as dirty
	if err := markDirty(ctx, tx, issue.ID); err != nil {
		return fmt.Errorf("failed to mark issue dirty: %w", err)
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
	defer func() { _ = tx.Rollback() }()

	// Get prefix from config for validation
	var configPrefix string
	err = tx.QueryRowContext(ctx, "SELECT value FROM config WHERE `key` = ?", "issue_prefix").Scan(&configPrefix)
	if err == sql.ErrNoRows || configPrefix == "" {
		return fmt.Errorf("database not initialized: issue_prefix config is missing (run 'bd init --prefix <prefix>' first)")
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

		// Defensive fix for deleted_at invariant
		if issue.Status == types.StatusTombstone && issue.DeletedAt == nil {
			maxTime := issue.CreatedAt
			if issue.UpdatedAt.After(maxTime) {
				maxTime = issue.UpdatedAt
			}
			deletedAt := maxTime.Add(time.Second)
			issue.DeletedAt = &deletedAt
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
		if err := markDirty(ctx, tx, issue.ID); err != nil {
			return fmt.Errorf("failed to mark dirty %s: %w", issue.ID, err)
		}
	}

	return tx.Commit()
}

// validateIssueIDPrefix validates that the issue ID has the correct prefix
func validateIssueIDPrefix(id, prefix string) error {
	if !strings.HasPrefix(id, prefix+"-") {
		return fmt.Errorf("issue ID %s does not match configured prefix %s", id, prefix)
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

// GetIssue retrieves an issue by ID
func (s *DoltStore) GetIssue(ctx context.Context, id string) (*types.Issue, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	issue, err := scanIssue(ctx, s.db, id)
	if err != nil {
		return nil, err
	}
	if issue == nil {
		return nil, nil
	}

	// Fetch labels
	labels, err := s.GetLabels(ctx, issue.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to get labels: %w", err)
	}
	issue.Labels = labels

	return issue, nil
}

// GetIssueByExternalRef retrieves an issue by external reference
func (s *DoltStore) GetIssueByExternalRef(ctx context.Context, externalRef string) (*types.Issue, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var id string
	err := s.db.QueryRowContext(ctx, "SELECT id FROM issues WHERE external_ref = ?", externalRef).Scan(&id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get issue by external_ref: %w", err)
	}

	return s.GetIssue(ctx, id)
}

// UpdateIssue updates fields on an issue
func (s *DoltStore) UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
	oldIssue, err := s.GetIssue(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to get issue for update: %w", err)
	}
	if oldIssue == nil {
		return fmt.Errorf("issue %s not found", id)
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
	defer func() { _ = tx.Rollback() }()

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

	if err := markDirty(ctx, tx, id); err != nil {
		return fmt.Errorf("failed to mark dirty: %w", err)
	}

	return tx.Commit()
}

// ClaimIssue atomically claims an issue using compare-and-swap semantics.
// It sets the assignee to actor and status to "in_progress" only if the issue
// currently has no assignee. Returns storage.ErrAlreadyClaimed if already claimed.
func (s *DoltStore) ClaimIssue(ctx context.Context, id string, actor string) error {
	oldIssue, err := s.GetIssue(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to get issue for claim: %w", err)
	}
	if oldIssue == nil {
		return fmt.Errorf("issue %s not found", id)
	}

	now := time.Now().UTC()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

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

	if err := markDirty(ctx, tx, id); err != nil {
		return fmt.Errorf("failed to mark dirty: %w", err)
	}

	return tx.Commit()
}

// CloseIssue closes an issue with a reason
func (s *DoltStore) CloseIssue(ctx context.Context, id string, reason string, actor string, session string) error {
	now := time.Now().UTC()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

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

	if err := markDirty(ctx, tx, id); err != nil {
		return fmt.Errorf("failed to mark dirty: %w", err)
	}

	return tx.Commit()
}

// DeleteIssue permanently removes an issue
func (s *DoltStore) DeleteIssue(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Delete related data (foreign keys will cascade, but be explicit)
	tables := []string{"dependencies", "events", "comments", "labels", "dirty_issues"}
	for _, table := range tables {
		if table == "dependencies" {
			_, err = tx.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE issue_id = ? OR depends_on_id = ?", table), id, id)
		} else {
			_, err = tx.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE issue_id = ?", table), id)
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
			deleted_at, deleted_by, delete_reason, original_type,
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
		issue.DeletedAt, issue.DeletedBy, issue.DeleteReason, issue.OriginalType,
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
	var issue types.Issue
	var createdAtStr, updatedAtStr sql.NullString // TEXT columns - must parse manually
	var closedAt, compactedAt, deletedAt, lastActivity, dueAt, deferUntil sql.NullTime
	var estimatedMinutes, originalSize, timeoutNs sql.NullInt64
	var assignee, externalRef, specID, compactedAtCommit, owner sql.NullString
	var contentHash, sourceRepo, closeReason, deletedBy, deleteReason, originalType sql.NullString
	var workType, sourceSystem sql.NullString
	var sender, wispType, molType, eventKind, actor, target, payload sql.NullString
	var awaitType, awaitID, waiters sql.NullString
	var hookBead, roleBead, agentState, roleType, rig sql.NullString
	var ephemeral, pinned, isTemplate, crystallizes sql.NullInt64
	var qualityScore sql.NullFloat64
	var metadata sql.NullString

	err := db.QueryRowContext(ctx, `
		SELECT id, content_hash, title, description, design, acceptance_criteria, notes,
		       status, priority, issue_type, assignee, estimated_minutes,
		       created_at, created_by, owner, updated_at, closed_at, external_ref, spec_id,
		       compaction_level, compacted_at, compacted_at_commit, original_size, source_repo, close_reason,
		       deleted_at, deleted_by, delete_reason, original_type,
		       sender, ephemeral, wisp_type, pinned, is_template, crystallizes,
		       await_type, await_id, timeout_ns, waiters,
		       hook_bead, role_bead, agent_state, last_activity, role_type, rig, mol_type,
		       event_kind, actor, target, payload,
		       due_at, defer_until,
		       quality_score, work_type, source_system, metadata
		FROM issues
		WHERE id = ?
	`, id).Scan(
		&issue.ID, &contentHash, &issue.Title, &issue.Description, &issue.Design,
		&issue.AcceptanceCriteria, &issue.Notes, &issue.Status,
		&issue.Priority, &issue.IssueType, &assignee, &estimatedMinutes,
		&createdAtStr, &issue.CreatedBy, &owner, &updatedAtStr, &closedAt, &externalRef, &specID,
		&issue.CompactionLevel, &compactedAt, &compactedAtCommit, &originalSize, &sourceRepo, &closeReason,
		&deletedAt, &deletedBy, &deleteReason, &originalType,
		&sender, &ephemeral, &wispType, &pinned, &isTemplate, &crystallizes,
		&awaitType, &awaitID, &timeoutNs, &waiters,
		&hookBead, &roleBead, &agentState, &lastActivity, &roleType, &rig, &molType,
		&eventKind, &actor, &target, &payload,
		&dueAt, &deferUntil,
		&qualityScore, &workType, &sourceSystem, &metadata,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get issue: %w", err)
	}

	// Parse timestamp strings (TEXT columns require manual parsing)
	if createdAtStr.Valid {
		issue.CreatedAt = parseTimeString(createdAtStr.String)
	}
	if updatedAtStr.Valid {
		issue.UpdatedAt = parseTimeString(updatedAtStr.String)
	}

	// Map nullable fields
	if contentHash.Valid {
		issue.ContentHash = contentHash.String
	}
	if closedAt.Valid {
		issue.ClosedAt = &closedAt.Time
	}
	if estimatedMinutes.Valid {
		mins := int(estimatedMinutes.Int64)
		issue.EstimatedMinutes = &mins
	}
	if assignee.Valid {
		issue.Assignee = assignee.String
	}
	if owner.Valid {
		issue.Owner = owner.String
	}
	if externalRef.Valid {
		issue.ExternalRef = &externalRef.String
	}
	if specID.Valid {
		issue.SpecID = specID.String
	}
	if compactedAt.Valid {
		issue.CompactedAt = &compactedAt.Time
	}
	if compactedAtCommit.Valid {
		issue.CompactedAtCommit = &compactedAtCommit.String
	}
	if originalSize.Valid {
		issue.OriginalSize = int(originalSize.Int64)
	}
	if sourceRepo.Valid {
		issue.SourceRepo = sourceRepo.String
	}
	if closeReason.Valid {
		issue.CloseReason = closeReason.String
	}
	if deletedAt.Valid {
		issue.DeletedAt = &deletedAt.Time
	}
	if deletedBy.Valid {
		issue.DeletedBy = deletedBy.String
	}
	if deleteReason.Valid {
		issue.DeleteReason = deleteReason.String
	}
	if originalType.Valid {
		issue.OriginalType = originalType.String
	}
	if sender.Valid {
		issue.Sender = sender.String
	}
	if ephemeral.Valid && ephemeral.Int64 != 0 {
		issue.Ephemeral = true
	}
	if wispType.Valid {
		issue.WispType = types.WispType(wispType.String)
	}
	if pinned.Valid && pinned.Int64 != 0 {
		issue.Pinned = true
	}
	if isTemplate.Valid && isTemplate.Int64 != 0 {
		issue.IsTemplate = true
	}
	if crystallizes.Valid && crystallizes.Int64 != 0 {
		issue.Crystallizes = true
	}
	if awaitType.Valid {
		issue.AwaitType = awaitType.String
	}
	if awaitID.Valid {
		issue.AwaitID = awaitID.String
	}
	if timeoutNs.Valid {
		issue.Timeout = time.Duration(timeoutNs.Int64)
	}
	if waiters.Valid && waiters.String != "" {
		issue.Waiters = parseJSONStringArray(waiters.String)
	}
	if hookBead.Valid {
		issue.HookBead = hookBead.String
	}
	if roleBead.Valid {
		issue.RoleBead = roleBead.String
	}
	if agentState.Valid {
		issue.AgentState = types.AgentState(agentState.String)
	}
	if lastActivity.Valid {
		issue.LastActivity = &lastActivity.Time
	}
	if roleType.Valid {
		issue.RoleType = roleType.String
	}
	if rig.Valid {
		issue.Rig = rig.String
	}
	if molType.Valid {
		issue.MolType = types.MolType(molType.String)
	}
	if eventKind.Valid {
		issue.EventKind = eventKind.String
	}
	if actor.Valid {
		issue.Actor = actor.String
	}
	if target.Valid {
		issue.Target = target.String
	}
	if payload.Valid {
		issue.Payload = payload.String
	}
	if dueAt.Valid {
		issue.DueAt = &dueAt.Time
	}
	if deferUntil.Valid {
		issue.DeferUntil = &deferUntil.Time
	}
	if qualityScore.Valid {
		qs := float32(qualityScore.Float64)
		issue.QualityScore = &qs
	}
	if workType.Valid {
		issue.WorkType = types.WorkType(workType.String)
	}
	if sourceSystem.Valid {
		issue.SourceSystem = sourceSystem.String
	}
	// Custom metadata field (GH#1406)
	if metadata.Valid && metadata.String != "" && metadata.String != "{}" {
		issue.Metadata = []byte(metadata.String)
	}

	return &issue, nil
}

func recordEvent(ctx context.Context, tx *sql.Tx, issueID string, eventType types.EventType, actor, oldValue, newValue string) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO events (issue_id, event_type, actor, old_value, new_value)
		VALUES (?, ?, ?, ?, ?)
	`, issueID, eventType, actor, oldValue, newValue)
	return err
}

func markDirty(ctx context.Context, tx *sql.Tx, issueID string) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO dirty_issues (issue_id, marked_at)
		VALUES (?, ?)
		ON DUPLICATE KEY UPDATE marked_at = VALUES(marked_at)
	`, issueID, time.Now().UTC())
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
		"sender": true, "wisp": true, "wisp_type": true, "pinned": true,
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


