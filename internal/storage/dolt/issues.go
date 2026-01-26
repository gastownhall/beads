package dolt

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

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
		args = append(args, value)
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
			created_at, created_by, owner, updated_at, closed_at, external_ref,
			compaction_level, compacted_at, compacted_at_commit, original_size,
			deleted_at, deleted_by, delete_reason, original_type,
			sender, ephemeral, pinned, is_template, crystallizes,
			mol_type, work_type, quality_score, source_system, source_repo, close_reason,
			event_kind, actor, target, payload,
			await_type, await_id, timeout_ns, waiters,
			hook_bead, role_bead, agent_state, last_activity, role_type, rig,
			due_at, defer_until
		) VALUES (
			?, ?, ?, ?, ?, ?, ?,
			?, ?, ?, ?, ?,
			?, ?, ?, ?, ?, ?,
			?, ?, ?, ?,
			?, ?, ?, ?,
			?, ?, ?, ?, ?,
			?, ?, ?, ?, ?, ?,
			?, ?, ?, ?,
			?, ?, ?, ?,
			?, ?, ?, ?, ?, ?,
			?, ?
		)
	`,
		issue.ID, issue.ContentHash, issue.Title, issue.Description, issue.Design, issue.AcceptanceCriteria, issue.Notes,
		issue.Status, issue.Priority, issue.IssueType, nullString(issue.Assignee), nullInt(issue.EstimatedMinutes),
		issue.CreatedAt, issue.CreatedBy, issue.Owner, issue.UpdatedAt, issue.ClosedAt, nullStringPtr(issue.ExternalRef),
		issue.CompactionLevel, issue.CompactedAt, nullStringPtr(issue.CompactedAtCommit), nullIntVal(issue.OriginalSize),
		issue.DeletedAt, issue.DeletedBy, issue.DeleteReason, issue.OriginalType,
		issue.Sender, issue.Ephemeral, issue.Pinned, issue.IsTemplate, issue.Crystallizes,
		issue.MolType, issue.WorkType, issue.QualityScore, issue.SourceSystem, issue.SourceRepo, issue.CloseReason,
		issue.EventKind, issue.Actor, issue.Target, issue.Payload,
		issue.AwaitType, issue.AwaitID, issue.Timeout.Nanoseconds(), formatJSONStringArray(issue.Waiters),
		issue.HookBead, issue.RoleBead, issue.AgentState, issue.LastActivity, issue.RoleType, issue.Rig,
		issue.DueAt, issue.DeferUntil,
	)
	return err
}

func scanIssue(ctx context.Context, db *sql.DB, id string) (*types.Issue, error) {
	var issue types.Issue
	var createdAtStr, updatedAtStr sql.NullString // TEXT columns - must parse manually
	var closedAt, compactedAt, deletedAt, lastActivity, dueAt, deferUntil sql.NullTime
	var estimatedMinutes, originalSize, timeoutNs sql.NullInt64
	var assignee, externalRef, compactedAtCommit, owner sql.NullString
	var contentHash, sourceRepo, closeReason, deletedBy, deleteReason, originalType sql.NullString
	var workType, sourceSystem sql.NullString
	var sender, molType, eventKind, actor, target, payload sql.NullString
	var awaitType, awaitID, waiters sql.NullString
	var hookBead, roleBead, agentState, roleType, rig sql.NullString
	var ephemeral, pinned, isTemplate, crystallizes sql.NullInt64
	var qualityScore sql.NullFloat64

	err := db.QueryRowContext(ctx, `
		SELECT id, content_hash, title, description, design, acceptance_criteria, notes,
		       status, priority, issue_type, assignee, estimated_minutes,
		       created_at, created_by, owner, updated_at, closed_at, external_ref,
		       compaction_level, compacted_at, compacted_at_commit, original_size, source_repo, close_reason,
		       deleted_at, deleted_by, delete_reason, original_type,
		       sender, ephemeral, pinned, is_template, crystallizes,
		       await_type, await_id, timeout_ns, waiters,
		       hook_bead, role_bead, agent_state, last_activity, role_type, rig, mol_type,
		       event_kind, actor, target, payload,
		       due_at, defer_until,
		       quality_score, work_type, source_system
		FROM issues
		WHERE id = ?
	`, id).Scan(
		&issue.ID, &contentHash, &issue.Title, &issue.Description, &issue.Design,
		&issue.AcceptanceCriteria, &issue.Notes, &issue.Status,
		&issue.Priority, &issue.IssueType, &assignee, &estimatedMinutes,
		&createdAtStr, &issue.CreatedBy, &owner, &updatedAtStr, &closedAt, &externalRef,
		&issue.CompactionLevel, &compactedAt, &compactedAtCommit, &originalSize, &sourceRepo, &closeReason,
		&deletedAt, &deletedBy, &deleteReason, &originalType,
		&sender, &ephemeral, &pinned, &isTemplate, &crystallizes,
		&awaitType, &awaitID, &timeoutNs, &waiters,
		&hookBead, &roleBead, &agentState, &lastActivity, &roleType, &rig, &molType,
		&eventKind, &actor, &target, &payload,
		&dueAt, &deferUntil,
		&qualityScore, &workType, &sourceSystem,
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

// nolint:unparam // error return kept for interface consistency
func generateIssueID(_ context.Context, _ *sql.Tx, prefix string, issue *types.Issue, _ string) (string, error) {
	// Simple hash-based ID generation
	// Use first 6 chars of content hash
	hash := issue.ComputeContentHash()
	if len(hash) > 6 {
		hash = hash[:6]
	}
	return fmt.Sprintf("%s-%s", prefix, hash), nil
}

func isAllowedUpdateField(key string) bool {
	allowed := map[string]bool{
		"status": true, "priority": true, "title": true, "assignee": true,
		"description": true, "design": true, "acceptance_criteria": true, "notes": true,
		"issue_type": true, "estimated_minutes": true, "external_ref": true,
		"closed_at": true, "close_reason": true, "closed_by_session": true,
		"sender": true, "wisp": true, "pinned": true,
		"hook_bead": true, "role_bead": true, "agent_state": true, "last_activity": true,
		"role_type": true, "rig": true, "mol_type": true,
		"event_category": true, "event_actor": true, "event_target": true, "event_payload": true,
		"due_at": true, "defer_until": true, "await_id": true,
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
