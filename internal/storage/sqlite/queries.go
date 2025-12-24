package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// NOTE: createGraphEdgesFromIssueFields and createGraphEdgesFromUpdates removed
// per Decision 004 Phase 4 - Edge Schema Consolidation.
// Graph edges (replies-to, relates-to, duplicates, supersedes) are now managed
// exclusively through the dependency API. Use AddDependency() instead.

// REMOVED (bd-8e05): getNextIDForPrefix and AllocateNextID - sequential ID generation
// no longer needed with hash-based IDs
// Migration functions moved to migrations.go (bd-fc2d, bd-b245)

// getNextChildNumber atomically generates the next child number for a parent ID
// Uses the child_counters table for atomic, cross-process child ID generation
// Hash ID generation functions moved to hash_ids.go (bd-90a5)

// REMOVED (bd-c7af): SyncAllCounters - no longer needed with hash IDs

// REMOVED (bd-166): derivePrefixFromPath was causing duplicate issues with wrong prefix
// The database should ALWAYS have issue_prefix config set explicitly (by 'bd init' or auto-import)
// Never derive prefix from filename - it leads to silent data corruption

// CreateIssue creates a new issue
func (s *SQLiteStorage) CreateIssue(ctx context.Context, issue *types.Issue, actor string) error {
	// Fetch custom statuses for validation (bd-1pj6)
	customStatuses, err := s.GetCustomStatuses(ctx)
	if err != nil {
		return fmt.Errorf("failed to get custom statuses: %w", err)
	}

	// Set timestamps first so defensive fixes can use them
	now := time.Now()
	if issue.CreatedAt.IsZero() {
		issue.CreatedAt = now
	}
	if issue.UpdatedAt.IsZero() {
		issue.UpdatedAt = now
	}

	// Defensive fix for closed_at invariant (GH#523): older versions of bd could
	// close issues without setting closed_at. Fix by using max(created_at, updated_at) + 1s.
	if issue.Status == types.StatusClosed && issue.ClosedAt == nil {
		maxTime := issue.CreatedAt
		if issue.UpdatedAt.After(maxTime) {
			maxTime = issue.UpdatedAt
		}
		closedAt := maxTime.Add(time.Second)
		issue.ClosedAt = &closedAt
	}

	// Defensive fix for deleted_at invariant: tombstones must have deleted_at
	if issue.Status == types.StatusTombstone && issue.DeletedAt == nil {
		maxTime := issue.CreatedAt
		if issue.UpdatedAt.After(maxTime) {
			maxTime = issue.UpdatedAt
		}
		deletedAt := maxTime.Add(time.Second)
		issue.DeletedAt = &deletedAt
	}

	// Validate issue before creating (with custom status support)
	if err := issue.ValidateWithCustomStatuses(customStatuses); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	// Compute content hash (bd-95)
	if issue.ContentHash == "" {
		issue.ContentHash = issue.ComputeContentHash()
	}

	// Acquire a dedicated connection for the transaction.
	// This is necessary because we need to execute raw SQL ("BEGIN IMMEDIATE", "COMMIT")
	// on the same connection, and database/sql's connection pool would otherwise
	// use different connections for different queries.
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("failed to acquire connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	// Start IMMEDIATE transaction to acquire write lock early and prevent race conditions.
	// IMMEDIATE acquires a RESERVED lock immediately, preventing other IMMEDIATE or EXCLUSIVE
	// transactions from starting. This serializes ID generation across concurrent writers.
	//
	// We use raw Exec instead of BeginTx because database/sql doesn't support transaction
	// modes in BeginTx, and modernc.org/sqlite's BeginTx always uses DEFERRED mode.
	//
	// Use retry logic with exponential backoff to handle SQLITE_BUSY under concurrent load (bd-ola6)
	if err := beginImmediateWithRetry(ctx, conn, 5, 10*time.Millisecond); err != nil {
		return fmt.Errorf("failed to begin immediate transaction: %w", err)
	}

	// Track commit state for defer cleanup
	// Use context.Background() for ROLLBACK to ensure cleanup happens even if ctx is canceled
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	// Get prefix from config (needed for both ID generation and validation)
	var prefix string
	err = conn.QueryRowContext(ctx, `SELECT value FROM config WHERE key = ?`, "issue_prefix").Scan(&prefix)
	if err == sql.ErrNoRows || prefix == "" {
		// CRITICAL: Reject operation if issue_prefix config is missing (bd-166)
		// This prevents duplicate issues with wrong prefix
		return fmt.Errorf("database not initialized: issue_prefix config is missing (run 'bd init --prefix <prefix>' first)")
	} else if err != nil {
		return fmt.Errorf("failed to get config: %w", err)
	}

	// Generate or validate ID
	if issue.ID == "" {
		// Generate hash-based ID with adaptive length based on database size (bd-ea2a13)
		generatedID, err := GenerateIssueID(ctx, conn, prefix, issue, actor)
		if err != nil {
			return wrapDBError("generate issue ID", err)
		}
		issue.ID = generatedID
	} else {
		// Validate that explicitly provided ID matches the configured prefix (bd-177)
		if err := ValidateIssueIDPrefix(issue.ID, prefix); err != nil {
			return wrapDBError("validate issue ID prefix", err)
		}

		// For hierarchical IDs (bd-a3f8e9.1), ensure parent exists
		// Use IsHierarchicalID to correctly handle prefixes with dots (GH#508)
		if isHierarchical, parentID := IsHierarchicalID(issue.ID); isHierarchical {
			// Try to resurrect entire parent chain if any parents are missing
			// Use the conn-based version to participate in the same transaction
			resurrected, err := s.tryResurrectParentChainWithConn(ctx, conn, issue.ID)
			if err != nil {
				return fmt.Errorf("failed to resurrect parent chain for %s: %w", issue.ID, err)
			}
			if !resurrected {
				// Parent(s) not found in JSONL history - cannot proceed
				return fmt.Errorf("parent issue %s does not exist and could not be resurrected from JSONL history", parentID)
			}
		}
	}

	// Insert issue
	if err := insertIssue(ctx, conn, issue); err != nil {
		return wrapDBError("insert issue", err)
	}

	// Record creation event
	if err := recordCreatedEvent(ctx, conn, issue, actor); err != nil {
		return wrapDBError("record creation event", err)
	}

	// NOTE: Graph edges (replies-to, relates-to, duplicates, supersedes) are now
	// managed via AddDependency() per Decision 004 Phase 4.

	// Mark issue as dirty for incremental export
	if err := markDirty(ctx, conn, issue.ID); err != nil {
		return wrapDBError("mark issue dirty", err)
	}

	// Commit the transaction
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}
	committed = true
	return nil
}

// validateBatchIssues validates all issues in a batch and sets timestamps
// Batch operation functions moved to batch_ops.go (bd-c796)

// GetIssue retrieves an issue by ID
func (s *SQLiteStorage) GetIssue(ctx context.Context, id string) (*types.Issue, error) {
	// Check for external database file modifications (daemon mode)
	s.checkFreshness()

	// Hold read lock during database operations to prevent reconnect() from
	// closing the connection mid-query (GH#607 race condition fix)
	s.reconnectMu.RLock()
	defer s.reconnectMu.RUnlock()

	var issue types.Issue
	var closedAt sql.NullTime
	var estimatedMinutes sql.NullInt64
	var assignee sql.NullString
	var externalRef sql.NullString
	var compactedAt sql.NullTime
	var originalSize sql.NullInt64
	var sourceRepo sql.NullString
	var closeReason sql.NullString
	var deletedAt sql.NullString // TEXT column, not DATETIME - must parse manually
	var deletedBy sql.NullString
	var deleteReason sql.NullString
	var originalType sql.NullString
	// Messaging fields (bd-kwro)
	var sender sql.NullString
	var wisp sql.NullInt64
	// Pinned field (bd-7h5)
	var pinned sql.NullInt64
	// Template field (beads-1ra)
	var isTemplate sql.NullInt64
	// Gate fields (bd-udsi)
	var awaitType sql.NullString
	var awaitID sql.NullString
	var timeoutNs sql.NullInt64
	var waiters sql.NullString

	var contentHash sql.NullString
	var compactedAtCommit sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT id, content_hash, title, description, design, acceptance_criteria, notes,
		       status, priority, issue_type, assignee, estimated_minutes,
		       created_at, updated_at, closed_at, external_ref,
		       compaction_level, compacted_at, compacted_at_commit, original_size, source_repo, close_reason,
		       deleted_at, deleted_by, delete_reason, original_type,
		       sender, ephemeral, pinned, is_template,
		       await_type, await_id, timeout_ns, waiters
		FROM issues
		WHERE id = ?
	`, id).Scan(
		&issue.ID, &contentHash, &issue.Title, &issue.Description, &issue.Design,
		&issue.AcceptanceCriteria, &issue.Notes, &issue.Status,
		&issue.Priority, &issue.IssueType, &assignee, &estimatedMinutes,
		&issue.CreatedAt, &issue.UpdatedAt, &closedAt, &externalRef,
		&issue.CompactionLevel, &compactedAt, &compactedAtCommit, &originalSize, &sourceRepo, &closeReason,
		&deletedAt, &deletedBy, &deleteReason, &originalType,
		&sender, &wisp, &pinned, &isTemplate,
		&awaitType, &awaitID, &timeoutNs, &waiters,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get issue: %w", err)
	}

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
	issue.DeletedAt = parseNullableTimeString(deletedAt)
	if deletedBy.Valid {
		issue.DeletedBy = deletedBy.String
	}
	if deleteReason.Valid {
		issue.DeleteReason = deleteReason.String
	}
	if originalType.Valid {
		issue.OriginalType = originalType.String
	}
	// Messaging fields (bd-kwro)
	if sender.Valid {
		issue.Sender = sender.String
	}
	if wisp.Valid && wisp.Int64 != 0 {
		issue.Wisp = true
	}
	// Pinned field (bd-7h5)
	if pinned.Valid && pinned.Int64 != 0 {
		issue.Pinned = true
	}
	// Template field (beads-1ra)
	if isTemplate.Valid && isTemplate.Int64 != 0 {
		issue.IsTemplate = true
	}
	// Gate fields (bd-udsi)
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

	// Fetch labels for this issue
	labels, err := s.GetLabels(ctx, issue.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to get labels: %w", err)
	}
	issue.Labels = labels

	return &issue, nil
}

// Allowed fields for update to prevent SQL injection
var allowedUpdateFields = map[string]bool{
	"status":              true,
	"priority":            true,
	"title":               true,
	"assignee":            true,
	"description":         true,
	"design":              true,
	"acceptance_criteria": true,
	"notes":               true,
	"issue_type":          true,
	"estimated_minutes":   true,
	"external_ref":        true,
	"closed_at":           true,
	// Messaging fields (bd-kwro)
	"sender": true,
	"wisp":   true, // Database column is 'ephemeral', mapped in UpdateIssue
	// Pinned field (bd-7h5)
	"pinned": true,
	// NOTE: replies_to, relates_to, duplicate_of, superseded_by removed per Decision 004
	// Use AddDependency() to create graph edges instead
}

// validatePriority validates a priority value
// Validation functions moved to validators.go (bd-d9e0)

// determineEventType determines the event type for an update based on old and new status
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

// manageClosedAt automatically manages the closed_at field based on status changes
func manageClosedAt(oldIssue *types.Issue, updates map[string]interface{}, setClauses []string, args []interface{}) ([]string, []interface{}) {
	statusVal, hasStatus := updates["status"]

	// If closed_at is explicitly provided in updates, it's already in setClauses/args
	// and we should not override it (important for import operations that preserve timestamps)
	_, hasExplicitClosedAt := updates["closed_at"]
	if hasExplicitClosedAt {
		return setClauses, args
	}

	if !hasStatus {
		return setClauses, args
	}

	// Handle both string and types.Status
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
		// Changing to closed: ensure closed_at is set
		now := time.Now()
		updates["closed_at"] = now
		setClauses = append(setClauses, "closed_at = ?")
		args = append(args, now)
	} else if oldIssue.Status == types.StatusClosed {
		// Changing from closed to something else: clear closed_at and close_reason
		updates["closed_at"] = nil
		setClauses = append(setClauses, "closed_at = ?")
		args = append(args, nil)
		updates["close_reason"] = ""
		setClauses = append(setClauses, "close_reason = ?")
		args = append(args, "")
	}

	return setClauses, args
}

// UpdateIssue updates fields on an issue
func (s *SQLiteStorage) UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
	// Get old issue for event
	oldIssue, err := s.GetIssue(ctx, id)
	if err != nil {
		return wrapDBError("get issue for update", err)
	}
	if oldIssue == nil {
		return fmt.Errorf("issue %s not found", id)
	}

	// Fetch custom statuses for validation (bd-1pj6)
	customStatuses, err := s.GetCustomStatuses(ctx)
	if err != nil {
		return wrapDBError("get custom statuses", err)
	}

	// Build update query with validated field names
	setClauses := []string{"updated_at = ?"}
	args := []interface{}{time.Now()}

	for key, value := range updates {
		// Prevent SQL injection by validating field names
		if !allowedUpdateFields[key] {
			return fmt.Errorf("invalid field for update: %s", key)
		}

		// Validate field values (with custom status support)
		if err := validateFieldUpdateWithCustomStatuses(key, value, customStatuses); err != nil {
			return wrapDBError("validate field update", err)
		}

		// Map API field names to database column names (wisp -> ephemeral)
		columnName := key
		if key == "wisp" {
			columnName = "ephemeral"
		}
		setClauses = append(setClauses, fmt.Sprintf("%s = ?", columnName))
		args = append(args, value)
	}

	// Auto-manage closed_at when status changes (enforce invariant)
	setClauses, args = manageClosedAt(oldIssue, updates, setClauses, args)

	// Recompute content_hash if any content fields changed (bd-95)
	contentChanged := false
	contentFields := []string{"title", "description", "design", "acceptance_criteria", "notes", "status", "priority", "issue_type", "assignee", "external_ref"}
	for _, field := range contentFields {
		if _, exists := updates[field]; exists {
			contentChanged = true
			break
		}
	}
	if contentChanged {
		// Get updated issue to compute hash
		updatedIssue := *oldIssue
		for key, value := range updates {
			switch key {
			case "title":
				updatedIssue.Title = value.(string)
			case "description":
				updatedIssue.Description = value.(string)
			case "design":
				updatedIssue.Design = value.(string)
			case "acceptance_criteria":
				updatedIssue.AcceptanceCriteria = value.(string)
			case "notes":
				updatedIssue.Notes = value.(string)
			case "status":
				// Handle both string and types.Status
				if s, ok := value.(types.Status); ok {
					updatedIssue.Status = s
				} else {
					updatedIssue.Status = types.Status(value.(string))
				}
			case "priority":
				updatedIssue.Priority = value.(int)
			case "issue_type":
				// Handle both string and types.IssueType
				if t, ok := value.(types.IssueType); ok {
					updatedIssue.IssueType = t
				} else {
					updatedIssue.IssueType = types.IssueType(value.(string))
				}
			case "assignee":
				if value == nil {
					updatedIssue.Assignee = ""
				} else {
					updatedIssue.Assignee = value.(string)
				}
			case "external_ref":
				if value == nil {
					updatedIssue.ExternalRef = nil
				} else {
					// Handle both string and *string
					switch v := value.(type) {
					case string:
						updatedIssue.ExternalRef = &v
					case *string:
						updatedIssue.ExternalRef = v
					default:
						return fmt.Errorf("external_ref must be string or *string, got %T", value)
					}
				}
			}
		}
		newHash := updatedIssue.ComputeContentHash()
		setClauses = append(setClauses, "content_hash = ?")
		args = append(args, newHash)
	}

	args = append(args, id)

	// Start transaction
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Update issue
	query := fmt.Sprintf("UPDATE issues SET %s WHERE id = ?", strings.Join(setClauses, ", ")) // #nosec G201 - safe SQL with controlled column names
	_, err = tx.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("failed to update issue: %w", err)
	}

	// Record event
	oldData, err := json.Marshal(oldIssue)
	if err != nil {
		// Fall back to minimal description if marshaling fails
		oldData = []byte(fmt.Sprintf(`{"id":"%s"}`, id))
	}
	newData, err := json.Marshal(updates)
	if err != nil {
		// Fall back to minimal description if marshaling fails
		newData = []byte(`{}`)
	}
	oldDataStr := string(oldData)
	newDataStr := string(newData)

	eventType := determineEventType(oldIssue, updates)

	_, err = tx.ExecContext(ctx, `
		INSERT INTO events (issue_id, event_type, actor, old_value, new_value)
		VALUES (?, ?, ?, ?, ?)
	`, id, eventType, actor, oldDataStr, newDataStr)
	if err != nil {
		return fmt.Errorf("failed to record event: %w", err)
	}

	// NOTE: Graph edges now managed via AddDependency() per Decision 004 Phase 4.

	// Mark issue as dirty for incremental export
	_, err = tx.ExecContext(ctx, `
		INSERT INTO dirty_issues (issue_id, marked_at)
		VALUES (?, ?)
		ON CONFLICT (issue_id) DO UPDATE SET marked_at = excluded.marked_at
	`, id, time.Now())
	if err != nil {
		return fmt.Errorf("failed to mark issue dirty: %w", err)
	}

	// Invalidate blocked issues cache if status changed (bd-5qim)
	// Status changes affect which issues are blocked (blockers must be open/in_progress/blocked)
	if _, statusChanged := updates["status"]; statusChanged {
		if err := s.invalidateBlockedCache(ctx, tx); err != nil {
			return fmt.Errorf("failed to invalidate blocked cache: %w", err)
		}
	}

	return tx.Commit()
}

// CloseIssue closes an issue with a reason
func (s *SQLiteStorage) CloseIssue(ctx context.Context, id string, reason string, actor string) error {
	now := time.Now()

	// Update with special event handling
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// NOTE: close_reason is stored in two places:
	// 1. issues.close_reason - for direct queries (bd show --json, exports)
	// 2. events.comment - for audit history (when was it closed, by whom)
	// Keep both in sync. If refactoring, consider deriving one from the other.
	result, err := tx.ExecContext(ctx, `
		UPDATE issues SET status = ?, closed_at = ?, updated_at = ?, close_reason = ?
		WHERE id = ?
	`, types.StatusClosed, now, now, reason, id)
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

	_, err = tx.ExecContext(ctx, `
		INSERT INTO events (issue_id, event_type, actor, comment)
		VALUES (?, ?, ?, ?)
	`, id, types.EventClosed, actor, reason)
	if err != nil {
		return fmt.Errorf("failed to record event: %w", err)
	}

	// Mark issue as dirty for incremental export
	_, err = tx.ExecContext(ctx, `
		INSERT INTO dirty_issues (issue_id, marked_at)
		VALUES (?, ?)
		ON CONFLICT (issue_id) DO UPDATE SET marked_at = excluded.marked_at
	`, id, time.Now())
	if err != nil {
		return fmt.Errorf("failed to mark issue dirty: %w", err)
	}

	// Invalidate blocked issues cache since status changed to closed (bd-5qim)
	// Closed issues don't block others, so this affects blocking calculations
	if err := s.invalidateBlockedCache(ctx, tx); err != nil {
		return fmt.Errorf("failed to invalidate blocked cache: %w", err)
	}

	return tx.Commit()
}
