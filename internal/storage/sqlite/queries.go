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

// parseNullableTimeString parses a nullable time string from database TEXT columns.
// The ncruces/go-sqlite3 driver only auto-converts TEXT→time.Time for columns declared
// as DATETIME/DATE/TIME/TIMESTAMP. For TEXT columns (like deleted_at), we must parse manually.
// Supports RFC3339, RFC3339Nano, and SQLite's native format.
func parseNullableTimeString(ns sql.NullString) *time.Time {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	// Try RFC3339Nano first (more precise), then RFC3339, then SQLite format
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, ns.String); err == nil {
			return &t
		}
	}
	return nil // Unparseable - shouldn't happen with valid data
}

// parseJSONStringArray parses a JSON string array from database TEXT column.
// Returns empty slice if the string is empty or invalid JSON.
func parseJSONStringArray(s string) []string {
	if s == "" {
		return nil
	}
	var result []string
	if err := json.Unmarshal([]byte(s), &result); err != nil {
		return nil // Invalid JSON - shouldn't happen with valid data
	}
	return result
}

// formatJSONStringArray formats a string slice as JSON for database storage.
// Returns empty string if the slice is nil or empty.
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

// REMOVED: getNextIDForPrefix and AllocateNextID - sequential ID generation
// no longer needed with hash-based IDs
// Migration functions moved to migrations.go

// getNextChildNumber atomically generates the next child number for a parent ID
// Uses the child_counters table for atomic, cross-process child ID generation
// Hash ID generation functions moved to hash_ids.go

// REMOVED: SyncAllCounters - no longer needed with hash IDs

// REMOVED: derivePrefixFromPath was causing duplicate issues with wrong prefix
// The database should ALWAYS have issue_prefix config set explicitly (by 'bd init' or auto-import)
// Never derive prefix from filename - it leads to silent data corruption

// CreateIssue creates a new issue
func (s *SQLiteStorage) CreateIssue(ctx context.Context, issue *types.Issue, actor string) error {
	// Fetch custom statuses for validation
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

	// Compute content hash
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
	// Use retry logic with exponential backoff to handle SQLITE_BUSY under concurrent load
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
	var configPrefix string
	err = conn.QueryRowContext(ctx, `SELECT value FROM config WHERE key = ?`, "issue_prefix").Scan(&configPrefix)
	if err == sql.ErrNoRows || configPrefix == "" {
		// CRITICAL: Reject operation if issue_prefix config is missing
		// This prevents duplicate issues with wrong prefix
		return fmt.Errorf("database not initialized: issue_prefix config is missing (run 'bd init --prefix <prefix>' first)")
	} else if err != nil {
		return fmt.Errorf("failed to get config: %w", err)
	}

	// Use IDPrefix override if set, combined with config prefix
	// e.g., configPrefix="bd" + IDPrefix="wisp" → "bd-wisp"
	prefix := configPrefix
	if issue.IDPrefix != "" {
		prefix = configPrefix + "-" + issue.IDPrefix
	}

	// Generate or validate ID
	if issue.ID == "" {
		// Generate hash-based ID with adaptive length based on database size
		generatedID, err := GenerateIssueID(ctx, conn, prefix, issue, actor)
		if err != nil {
			return wrapDBError("generate issue ID", err)
		}
		issue.ID = generatedID
	} else {
		// Validate that explicitly provided ID matches the configured prefix
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

			// Update child_counters to prevent future ID collisions (GH#728 fix)
			// When explicit child IDs are used, the counter must be at least the child number
			if _, childNum, ok := ParseHierarchicalID(issue.ID); ok {
				if err := ensureChildCounterUpdatedWithConn(ctx, conn, parentID, childNum); err != nil {
					return fmt.Errorf("failed to update child counter: %w", err)
				}
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
// Batch operation functions moved to batch_ops.go

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
	// Messaging fields
	var sender sql.NullString
	var wisp sql.NullInt64
	// Pinned field
	var pinned sql.NullInt64
	// Template field
	var isTemplate sql.NullInt64
	// Gate fields
	var awaitType sql.NullString
	var awaitID sql.NullString
	var timeoutNs sql.NullInt64
	var waiters sql.NullString
	// Agent fields
	var hookBead sql.NullString
	var roleBead sql.NullString
	var agentState sql.NullString
	var lastActivity sql.NullTime
	var roleType sql.NullString
	var rig sql.NullString
	// Molecule type field
	var molType sql.NullString
	// Event fields
	var eventKind sql.NullString
	var actor sql.NullString
	var target sql.NullString
	var payload sql.NullString

	var contentHash sql.NullString
	var compactedAtCommit sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT id, content_hash, title, description, design, acceptance_criteria, notes,
		       status, priority, issue_type, assignee, estimated_minutes,
		       created_at, created_by, updated_at, closed_at, external_ref,
		       compaction_level, compacted_at, compacted_at_commit, original_size, source_repo, close_reason,
		       deleted_at, deleted_by, delete_reason, original_type,
		       sender, ephemeral, pinned, is_template,
		       await_type, await_id, timeout_ns, waiters,
		       hook_bead, role_bead, agent_state, last_activity, role_type, rig, mol_type,
		       event_kind, actor, target, payload
		FROM issues
		WHERE id = ?
	`, id).Scan(
		&issue.ID, &contentHash, &issue.Title, &issue.Description, &issue.Design,
		&issue.AcceptanceCriteria, &issue.Notes, &issue.Status,
		&issue.Priority, &issue.IssueType, &assignee, &estimatedMinutes,
		&issue.CreatedAt, &issue.CreatedBy, &issue.UpdatedAt, &closedAt, &externalRef,
		&issue.CompactionLevel, &compactedAt, &compactedAtCommit, &originalSize, &sourceRepo, &closeReason,
		&deletedAt, &deletedBy, &deleteReason, &originalType,
		&sender, &wisp, &pinned, &isTemplate,
		&awaitType, &awaitID, &timeoutNs, &waiters,
		&hookBead, &roleBead, &agentState, &lastActivity, &roleType, &rig, &molType,
		&eventKind, &actor, &target, &payload,
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
	// Messaging fields
	if sender.Valid {
		issue.Sender = sender.String
	}
	if wisp.Valid && wisp.Int64 != 0 {
		issue.Ephemeral = true
	}
	// Pinned field
	if pinned.Valid && pinned.Int64 != 0 {
		issue.Pinned = true
	}
	// Template field
	if isTemplate.Valid && isTemplate.Int64 != 0 {
		issue.IsTemplate = true
	}
	// Gate fields
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
	// Agent fields
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
	// Molecule type field
	if molType.Valid {
		issue.MolType = types.MolType(molType.String)
	}
	// Event fields
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

	// Fetch labels for this issue
	labels, err := s.GetLabels(ctx, issue.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to get labels: %w", err)
	}
	issue.Labels = labels

	return &issue, nil
}

// GetCloseReason retrieves the close reason from the most recent closed event for an issue
func (s *SQLiteStorage) GetCloseReason(ctx context.Context, issueID string) (string, error) {
	var comment sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT comment FROM events
		WHERE issue_id = ? AND event_type = ?
		ORDER BY created_at DESC
		LIMIT 1
	`, issueID, types.EventClosed).Scan(&comment)

	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("failed to get close reason: %w", err)
	}
	if comment.Valid {
		return comment.String, nil
	}
	return "", nil
}

// GetCloseReasonsForIssues retrieves close reasons for multiple issues in a single query
func (s *SQLiteStorage) GetCloseReasonsForIssues(ctx context.Context, issueIDs []string) (map[string]string, error) {
	result := make(map[string]string)
	if len(issueIDs) == 0 {
		return result, nil
	}

	// Build placeholders for IN clause
	placeholders := make([]string, len(issueIDs))
	args := make([]interface{}, len(issueIDs)+1)
	args[0] = types.EventClosed
	for i, id := range issueIDs {
		placeholders[i] = "?"
		args[i+1] = id
	}

	// Use a subquery to get the most recent closed event for each issue
	// #nosec G201 - safe SQL with controlled formatting
	query := fmt.Sprintf(`
		SELECT e.issue_id, e.comment
		FROM events e
		INNER JOIN (
			SELECT issue_id, MAX(created_at) as max_created_at
			FROM events
			WHERE event_type = ? AND issue_id IN (%s)
			GROUP BY issue_id
		) latest ON e.issue_id = latest.issue_id AND e.created_at = latest.max_created_at
		WHERE e.event_type = ?
	`, strings.Join(placeholders, ", "))

	// Append event_type again for the outer WHERE clause
	args = append(args, types.EventClosed)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get close reasons: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var issueID string
		var comment sql.NullString
		if err := rows.Scan(&issueID, &comment); err != nil {
			return nil, fmt.Errorf("failed to scan close reason: %w", err)
		}
		if comment.Valid && comment.String != "" {
			result[issueID] = comment.String
		}
	}

	return result, nil
}

// GetIssueByExternalRef retrieves an issue by external reference
func (s *SQLiteStorage) GetIssueByExternalRef(ctx context.Context, externalRef string) (*types.Issue, error) {
	var issue types.Issue
	var closedAt sql.NullTime
	var estimatedMinutes sql.NullInt64
	var assignee sql.NullString
	var externalRefCol sql.NullString
	var compactedAt sql.NullTime
	var originalSize sql.NullInt64
	var contentHash sql.NullString
	var compactedAtCommit sql.NullString
	var sourceRepo sql.NullString
	var closeReason sql.NullString
	var deletedAt sql.NullString // TEXT column, not DATETIME - must parse manually
	var deletedBy sql.NullString
	var deleteReason sql.NullString
	var originalType sql.NullString
	// Messaging fields
	var sender sql.NullString
	var wisp sql.NullInt64
	// Pinned field
	var pinned sql.NullInt64
	// Template field
	var isTemplate sql.NullInt64
	// Gate fields
	var awaitType sql.NullString
	var awaitID sql.NullString
	var timeoutNs sql.NullInt64
	var waiters sql.NullString

	err := s.db.QueryRowContext(ctx, `
		SELECT id, content_hash, title, description, design, acceptance_criteria, notes,
		       status, priority, issue_type, assignee, estimated_minutes,
		       created_at, created_by, updated_at, closed_at, external_ref,
		       compaction_level, compacted_at, compacted_at_commit, original_size, source_repo, close_reason,
		       deleted_at, deleted_by, delete_reason, original_type,
		       sender, ephemeral, pinned, is_template,
		       await_type, await_id, timeout_ns, waiters
		FROM issues
		WHERE external_ref = ?
	`, externalRef).Scan(
		&issue.ID, &contentHash, &issue.Title, &issue.Description, &issue.Design,
		&issue.AcceptanceCriteria, &issue.Notes, &issue.Status,
		&issue.Priority, &issue.IssueType, &assignee, &estimatedMinutes,
		&issue.CreatedAt, &issue.CreatedBy, &issue.UpdatedAt, &closedAt, &externalRefCol,
		&issue.CompactionLevel, &compactedAt, &compactedAtCommit, &originalSize, &sourceRepo, &closeReason,
		&deletedAt, &deletedBy, &deleteReason, &originalType,
		&sender, &wisp, &pinned, &isTemplate,
		&awaitType, &awaitID, &timeoutNs, &waiters,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get issue by external_ref: %w", err)
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
	if externalRefCol.Valid {
		issue.ExternalRef = &externalRefCol.String
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
	// Messaging fields
	if sender.Valid {
		issue.Sender = sender.String
	}
	if wisp.Valid && wisp.Int64 != 0 {
		issue.Ephemeral = true
	}
	// Pinned field
	if pinned.Valid && pinned.Int64 != 0 {
		issue.Pinned = true
	}
	// Template field
	if isTemplate.Valid && isTemplate.Int64 != 0 {
		issue.IsTemplate = true
	}
	// Gate fields
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
	// Messaging fields
	"sender": true,
	"wisp":   true, // Database column is 'ephemeral', mapped in UpdateIssue
	// Pinned field
	"pinned": true,
	// NOTE: replies_to, relates_to, duplicate_of, superseded_by removed per Decision 004
	// Use AddDependency() to create graph edges instead
	// Agent slot fields
	"hook_bead":     true,
	"role_bead":     true,
	"agent_state":   true,
	"last_activity": true,
	"role_type":     true,
	"rig":           true,
	// Molecule type field
	"mol_type": true,
	// Event fields
	"event_category": true,
	"event_actor":    true,
	"event_target":   true,
	"event_payload":  true,
}

// validatePriority validates a priority value
// Validation functions moved to validators.go

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

	// Fetch custom statuses for validation
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

	// Recompute content_hash if any content fields changed
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

	// Invalidate blocked issues cache if status changed
	// Status changes affect which issues are blocked (blockers must be open/in_progress/blocked)
	if _, statusChanged := updates["status"]; statusChanged {
		if err := s.invalidateBlockedCache(ctx, tx); err != nil {
			return fmt.Errorf("failed to invalidate blocked cache: %w", err)
		}
	}

	return tx.Commit()
}

// UpdateIssueID updates an issue ID and all its text fields in a single transaction
func (s *SQLiteStorage) UpdateIssueID(ctx context.Context, oldID, newID string, issue *types.Issue, actor string) error {
	// Get exclusive connection to ensure PRAGMA applies
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("failed to get connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	// Disable foreign keys on this specific connection
	_, err = conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`)
	if err != nil {
		return fmt.Errorf("failed to disable foreign keys: %w", err)
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(ctx, `
		UPDATE issues
		SET id = ?, title = ?, description = ?, design = ?, acceptance_criteria = ?, notes = ?, updated_at = ?
		WHERE id = ?
	`, newID, issue.Title, issue.Description, issue.Design, issue.AcceptanceCriteria, issue.Notes, time.Now(), oldID)
	if err != nil {
		return fmt.Errorf("failed to update issue ID: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("issue not found: %s", oldID)
	}

	_, err = tx.ExecContext(ctx, `UPDATE dependencies SET issue_id = ? WHERE issue_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update issue_id in dependencies: %w", err)
	}

	_, err = tx.ExecContext(ctx, `UPDATE dependencies SET depends_on_id = ? WHERE depends_on_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update depends_on_id in dependencies: %w", err)
	}

	_, err = tx.ExecContext(ctx, `UPDATE events SET issue_id = ? WHERE issue_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update events: %w", err)
	}

	_, err = tx.ExecContext(ctx, `UPDATE labels SET issue_id = ? WHERE issue_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update labels: %w", err)
	}

	_, err = tx.ExecContext(ctx, `UPDATE comments SET issue_id = ? WHERE issue_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update comments: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
		UPDATE dirty_issues SET issue_id = ? WHERE issue_id = ?
	`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update dirty_issues: %w", err)
	}

	_, err = tx.ExecContext(ctx, `UPDATE issue_snapshots SET issue_id = ? WHERE issue_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update issue_snapshots: %w", err)
	}

	_, err = tx.ExecContext(ctx, `UPDATE compaction_snapshots SET issue_id = ? WHERE issue_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update compaction_snapshots: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO dirty_issues (issue_id, marked_at)
		VALUES (?, ?)
		ON CONFLICT (issue_id) DO UPDATE SET marked_at = excluded.marked_at
	`, newID, time.Now())
	if err != nil {
		return fmt.Errorf("failed to mark issue dirty: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO events (issue_id, event_type, actor, old_value, new_value)
		VALUES (?, 'renamed', ?, ?, ?)
	`, newID, actor, oldID, newID)
	if err != nil {
		return fmt.Errorf("failed to record rename event: %w", err)
	}

	return tx.Commit()
}

// RenameDependencyPrefix updates the prefix in all dependency records
// GH#630: This was previously a no-op, causing dependencies to break after rename-prefix
func (s *SQLiteStorage) RenameDependencyPrefix(ctx context.Context, oldPrefix, newPrefix string) error {
	// Update issue_id column
	_, err := s.db.ExecContext(ctx, `
		UPDATE dependencies 
		SET issue_id = ? || substr(issue_id, length(?) + 1)
		WHERE issue_id LIKE ? || '%'
	`, newPrefix, oldPrefix, oldPrefix)
	if err != nil {
		return fmt.Errorf("failed to update issue_id in dependencies: %w", err)
	}

	// Update depends_on_id column
	_, err = s.db.ExecContext(ctx, `
		UPDATE dependencies 
		SET depends_on_id = ? || substr(depends_on_id, length(?) + 1)
		WHERE depends_on_id LIKE ? || '%'
	`, newPrefix, oldPrefix, oldPrefix)
	if err != nil {
		return fmt.Errorf("failed to update depends_on_id in dependencies: %w", err)
	}

	return nil
}

// RenameCounterPrefix is a no-op with hash-based IDs
// Kept for backward compatibility with rename-prefix command
func (s *SQLiteStorage) RenameCounterPrefix(ctx context.Context, oldPrefix, newPrefix string) error {
	// Hash-based IDs don't use counters, so nothing to update
	return nil
}

// ResetCounter is a no-op with hash-based IDs
// Kept for backward compatibility
func (s *SQLiteStorage) ResetCounter(ctx context.Context, prefix string) error {
	// Hash-based IDs don't use counters, so nothing to reset
	return nil
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

	// Invalidate blocked issues cache since status changed to closed
	// Closed issues don't block others, so this affects blocking calculations
	if err := s.invalidateBlockedCache(ctx, tx); err != nil {
		return fmt.Errorf("failed to invalidate blocked cache: %w", err)
	}

	// Reactive convoy completion: check if any convoys tracking this issue should auto-close
	// Find convoys that track this issue (convoy.issue_id tracks closed_issue.depends_on_id)
	convoyRows, err := tx.QueryContext(ctx, `
		SELECT DISTINCT d.issue_id
		FROM dependencies d
		JOIN issues i ON d.issue_id = i.id
		WHERE d.depends_on_id = ?
		  AND d.type = ?
		  AND i.issue_type = ?
		  AND i.status != ?
	`, id, types.DepTracks, types.TypeConvoy, types.StatusClosed)
	if err != nil {
		return fmt.Errorf("failed to find tracking convoys: %w", err)
	}
	defer func() { _ = convoyRows.Close() }()

	var convoyIDs []string
	for convoyRows.Next() {
		var convoyID string
		if err := convoyRows.Scan(&convoyID); err != nil {
			return fmt.Errorf("failed to scan convoy ID: %w", err)
		}
		convoyIDs = append(convoyIDs, convoyID)
	}
	if err := convoyRows.Err(); err != nil {
		return fmt.Errorf("convoy rows iteration error: %w", err)
	}

	// For each convoy, check if all tracked issues are now closed
	for _, convoyID := range convoyIDs {
		// Count non-closed tracked issues for this convoy
		var openCount int
		err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*)
			FROM dependencies d
			JOIN issues i ON d.depends_on_id = i.id
			WHERE d.issue_id = ?
			  AND d.type = ?
			  AND i.status != ?
			  AND i.status != ?
		`, convoyID, types.DepTracks, types.StatusClosed, types.StatusTombstone).Scan(&openCount)
		if err != nil {
			return fmt.Errorf("failed to count open tracked issues for convoy %s: %w", convoyID, err)
		}

		// If all tracked issues are closed, auto-close the convoy
		if openCount == 0 {
			closeReason := "All tracked issues completed"
			_, err := tx.ExecContext(ctx, `
				UPDATE issues SET status = ?, closed_at = ?, updated_at = ?, close_reason = ?
				WHERE id = ?
			`, types.StatusClosed, now, now, closeReason, convoyID)
			if err != nil {
				return fmt.Errorf("failed to auto-close convoy %s: %w", convoyID, err)
			}

			// Record the close event
			_, err = tx.ExecContext(ctx, `
				INSERT INTO events (issue_id, event_type, actor, comment)
				VALUES (?, ?, ?, ?)
			`, convoyID, types.EventClosed, "system:convoy-completion", closeReason)
			if err != nil {
				return fmt.Errorf("failed to record convoy close event: %w", err)
			}

			// Mark convoy as dirty
			_, err = tx.ExecContext(ctx, `
				INSERT INTO dirty_issues (issue_id, marked_at)
				VALUES (?, ?)
				ON CONFLICT (issue_id) DO UPDATE SET marked_at = excluded.marked_at
			`, convoyID, now)
			if err != nil {
				return fmt.Errorf("failed to mark convoy dirty: %w", err)
			}
		}
	}

	return tx.Commit()
}

// CreateTombstone converts an existing issue to a tombstone record.
// This is a soft-delete that preserves the issue in the database with status="tombstone".
// The issue will still appear in exports but be excluded from normal queries.
// Dependencies must be removed separately before calling this method.
func (s *SQLiteStorage) CreateTombstone(ctx context.Context, id string, actor string, reason string) error {
	// Get the issue to preserve its original type
	issue, err := s.GetIssue(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to get issue: %w", err)
	}
	if issue == nil {
		return fmt.Errorf("issue not found: %s", id)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now()
	originalType := string(issue.IssueType)

	// Convert issue to tombstone
	// Note: closed_at must be set to NULL because of CHECK constraint:
	// (status = 'closed') = (closed_at IS NOT NULL)
	_, err = tx.ExecContext(ctx, `
		UPDATE issues
		SET status = ?,
		    closed_at = NULL,
		    deleted_at = ?,
		    deleted_by = ?,
		    delete_reason = ?,
		    original_type = ?,
		    updated_at = ?
		WHERE id = ?
	`, types.StatusTombstone, now, actor, reason, originalType, now, id)
	if err != nil {
		return fmt.Errorf("failed to create tombstone: %w", err)
	}

	// Record tombstone creation event
	_, err = tx.ExecContext(ctx, `
		INSERT INTO events (issue_id, event_type, actor, comment)
		VALUES (?, ?, ?, ?)
	`, id, "deleted", actor, reason)
	if err != nil {
		return fmt.Errorf("failed to record tombstone event: %w", err)
	}

	// Mark issue as dirty for incremental export
	_, err = tx.ExecContext(ctx, `
		INSERT INTO dirty_issues (issue_id, marked_at)
		VALUES (?, ?)
		ON CONFLICT (issue_id) DO UPDATE SET marked_at = excluded.marked_at
	`, id, now)
	if err != nil {
		return fmt.Errorf("failed to mark issue dirty: %w", err)
	}

	// Invalidate blocked issues cache since status changed
	// Tombstone issues don't block others, so this affects blocking calculations
	if err := s.invalidateBlockedCache(ctx, tx); err != nil {
		return fmt.Errorf("failed to invalidate blocked cache: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return wrapDBError("commit tombstone transaction", err)
	}

	return nil
}

// DeleteIssue permanently removes an issue from the database
func (s *SQLiteStorage) DeleteIssue(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Delete dependencies (both directions)
	_, err = tx.ExecContext(ctx, `DELETE FROM dependencies WHERE issue_id = ? OR depends_on_id = ?`, id, id)
	if err != nil {
		return fmt.Errorf("failed to delete dependencies: %w", err)
	}

	// Delete events
	_, err = tx.ExecContext(ctx, `DELETE FROM events WHERE issue_id = ?`, id)
	if err != nil {
		return fmt.Errorf("failed to delete events: %w", err)
	}

	// Delete comments (no FK cascade on this table)
	_, err = tx.ExecContext(ctx, `DELETE FROM comments WHERE issue_id = ?`, id)
	if err != nil {
		return fmt.Errorf("failed to delete comments: %w", err)
	}

	// Delete from dirty_issues
	_, err = tx.ExecContext(ctx, `DELETE FROM dirty_issues WHERE issue_id = ?`, id)
	if err != nil {
		return fmt.Errorf("failed to delete dirty marker: %w", err)
	}

	// Delete the issue itself
	result, err := tx.ExecContext(ctx, `DELETE FROM issues WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("failed to delete issue: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to check rows affected: %w", err)
	}
	if rowsAffected == 0 {
		return fmt.Errorf("issue not found: %s", id)
	}

	if err := tx.Commit(); err != nil {
		return wrapDBError("commit delete transaction", err)
	}

	// REMOVED: Counter sync after deletion - no longer needed with hash IDs
	return nil
}

// DeleteIssuesResult contains statistics about a batch deletion operation
type DeleteIssuesResult struct {
	DeletedCount      int
	DependenciesCount int
	LabelsCount       int
	EventsCount       int
	OrphanedIssues    []string
}

// DeleteIssues deletes multiple issues in a single transaction
// If cascade is true, recursively deletes dependents
// If cascade is false but force is true, deletes issues and orphans their dependents
// If cascade and force are both false, returns an error if any issue has dependents
// If dryRun is true, only computes statistics without deleting
func (s *SQLiteStorage) DeleteIssues(ctx context.Context, ids []string, cascade bool, force bool, dryRun bool) (*DeleteIssuesResult, error) {
	if len(ids) == 0 {
		return &DeleteIssuesResult{}, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	idSet := buildIDSet(ids)
	result := &DeleteIssuesResult{}

	expandedIDs, err := s.resolveDeleteSet(ctx, tx, ids, idSet, cascade, force, result)
	if err != nil {
		return nil, wrapDBError("resolve delete set", err)
	}

	inClause, args := buildSQLInClause(expandedIDs)
	if err := s.populateDeleteStats(ctx, tx, inClause, args, result); err != nil {
		return nil, err
	}

	if dryRun {
		return result, nil
	}

	if err := s.executeDelete(ctx, tx, inClause, args, result); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %w", err)
	}

	// REMOVED: Counter sync after deletion - no longer needed with hash IDs

	return result, nil
}

func buildIDSet(ids []string) map[string]bool {
	idSet := make(map[string]bool, len(ids))
	for _, id := range ids {
		idSet[id] = true
	}
	return idSet
}

func (s *SQLiteStorage) resolveDeleteSet(ctx context.Context, tx *sql.Tx, ids []string, idSet map[string]bool, cascade bool, force bool, result *DeleteIssuesResult) ([]string, error) {
	if cascade {
		return s.expandWithDependents(ctx, tx, ids, idSet)
	}
	if !force {
		return ids, s.validateNoDependents(ctx, tx, ids, idSet, result)
	}
	return ids, s.trackOrphanedIssues(ctx, tx, ids, idSet, result)
}

func (s *SQLiteStorage) expandWithDependents(ctx context.Context, tx *sql.Tx, ids []string, _ map[string]bool) ([]string, error) {
	allToDelete, err := s.findAllDependentsRecursive(ctx, tx, ids)
	if err != nil {
		return nil, fmt.Errorf("failed to find dependents: %w", err)
	}
	expandedIDs := make([]string, 0, len(allToDelete))
	for id := range allToDelete {
		expandedIDs = append(expandedIDs, id)
	}
	return expandedIDs, nil
}

func (s *SQLiteStorage) validateNoDependents(ctx context.Context, tx *sql.Tx, ids []string, idSet map[string]bool, result *DeleteIssuesResult) error {
	for _, id := range ids {
		if err := s.checkSingleIssueValidation(ctx, tx, id, idSet, result); err != nil {
			return wrapDBError("check dependents", err)
		}
	}
	return nil
}

func (s *SQLiteStorage) checkSingleIssueValidation(ctx context.Context, tx *sql.Tx, id string, idSet map[string]bool, result *DeleteIssuesResult) error {
	var depCount int
	err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM dependencies WHERE depends_on_id = ?`, id).Scan(&depCount)
	if err != nil {
		return fmt.Errorf("failed to check dependents for %s: %w", id, err)
	}
	if depCount == 0 {
		return nil
	}

	rows, err := tx.QueryContext(ctx,
		`SELECT issue_id FROM dependencies WHERE depends_on_id = ?`, id)
	if err != nil {
		return fmt.Errorf("failed to get dependents for %s: %w", id, err)
	}
	defer func() { _ = rows.Close() }()

	hasExternal := false
	for rows.Next() {
		var depID string
		if err := rows.Scan(&depID); err != nil {
			return fmt.Errorf("failed to scan dependent: %w", err)
		}
		if !idSet[depID] {
			hasExternal = true
			result.OrphanedIssues = append(result.OrphanedIssues, depID)
		}
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("failed to iterate dependents for %s: %w", id, err)
	}

	if hasExternal {
		return fmt.Errorf("issue %s has dependents not in deletion set; use --cascade to delete them or --force to orphan them", id)
	}
	return nil
}

func (s *SQLiteStorage) trackOrphanedIssues(ctx context.Context, tx *sql.Tx, ids []string, idSet map[string]bool, result *DeleteIssuesResult) error {
	orphanSet := make(map[string]bool)
	for _, id := range ids {
		if err := s.collectOrphansForID(ctx, tx, id, idSet, orphanSet); err != nil {
			return wrapDBError("collect orphans", err)
		}
	}
	for orphanID := range orphanSet {
		result.OrphanedIssues = append(result.OrphanedIssues, orphanID)
	}
	return nil
}

func (s *SQLiteStorage) collectOrphansForID(ctx context.Context, tx *sql.Tx, id string, idSet map[string]bool, orphanSet map[string]bool) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT issue_id FROM dependencies WHERE depends_on_id = ?`, id)
	if err != nil {
		return fmt.Errorf("failed to get dependents for %s: %w", id, err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var depID string
		if err := rows.Scan(&depID); err != nil {
			return fmt.Errorf("failed to scan dependent: %w", err)
		}
		if !idSet[depID] {
			orphanSet[depID] = true
		}
	}
	return rows.Err()
}

func buildSQLInClause(ids []string) (string, []interface{}) {
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	return strings.Join(placeholders, ","), args
}

func (s *SQLiteStorage) populateDeleteStats(ctx context.Context, tx *sql.Tx, inClause string, args []interface{}, result *DeleteIssuesResult) error {
	counts := []struct {
		query string
		dest  *int
	}{
		{fmt.Sprintf(`SELECT COUNT(*) FROM dependencies WHERE issue_id IN (%s) OR depends_on_id IN (%s)`, inClause, inClause), &result.DependenciesCount},
		{fmt.Sprintf(`SELECT COUNT(*) FROM labels WHERE issue_id IN (%s)`, inClause), &result.LabelsCount},
		{fmt.Sprintf(`SELECT COUNT(*) FROM events WHERE issue_id IN (%s)`, inClause), &result.EventsCount},
	}

	for _, c := range counts {
		queryArgs := args
		if c.dest == &result.DependenciesCount {
			queryArgs = append(args, args...)
		}
		if err := tx.QueryRowContext(ctx, c.query, queryArgs...).Scan(c.dest); err != nil {
			return fmt.Errorf("failed to count: %w", err)
		}
	}

	result.DeletedCount = len(args)
	return nil
}

func (s *SQLiteStorage) executeDelete(ctx context.Context, tx *sql.Tx, inClause string, args []interface{}, result *DeleteIssuesResult) error {
	// Note: This method now creates tombstones instead of hard-deleting
	// Only dependencies are deleted - issues are converted to tombstones

	// 1. Delete dependencies - tombstones don't block other issues
	_, err := tx.ExecContext(ctx,
		fmt.Sprintf(`DELETE FROM dependencies WHERE issue_id IN (%s) OR depends_on_id IN (%s)`, inClause, inClause),
		append(args, args...)...)
	if err != nil {
		return fmt.Errorf("failed to delete dependencies: %w", err)
	}

	// 2. Get issue types before converting to tombstones (need for original_type)
	issueTypes := make(map[string]string)
	rows, err := tx.QueryContext(ctx,
		fmt.Sprintf(`SELECT id, issue_type FROM issues WHERE id IN (%s)`, inClause),
		args...)
	if err != nil {
		return fmt.Errorf("failed to get issue types: %w", err)
	}
	for rows.Next() {
		var id, issueType string
		if err := rows.Scan(&id, &issueType); err != nil {
			_ = rows.Close() // #nosec G104 - error handling not critical in error path
			return fmt.Errorf("failed to scan issue type: %w", err)
		}
		issueTypes[id] = issueType
	}
	_ = rows.Close()

	// 3. Convert issues to tombstones (only for issues that exist)
	// Note: closed_at must be set to NULL because of CHECK constraint:
	// (status = 'closed') = (closed_at IS NOT NULL)
	now := time.Now()
	deletedCount := 0
	for id, originalType := range issueTypes {
		execResult, err := tx.ExecContext(ctx, `
			UPDATE issues
			SET status = ?,
			    closed_at = NULL,
			    deleted_at = ?,
			    deleted_by = ?,
			    delete_reason = ?,
			    original_type = ?,
			    updated_at = ?
			WHERE id = ?
		`, types.StatusTombstone, now, "batch delete", "batch delete", originalType, now, id)
		if err != nil {
			return fmt.Errorf("failed to create tombstone for %s: %w", id, err)
		}

		rowsAffected, _ := execResult.RowsAffected()
		if rowsAffected == 0 {
			continue // Issue doesn't exist, skip
		}
		deletedCount++

		// Record tombstone creation event
		_, err = tx.ExecContext(ctx, `
			INSERT INTO events (issue_id, event_type, actor, comment)
			VALUES (?, ?, ?, ?)
		`, id, "deleted", "batch delete", "batch delete")
		if err != nil {
			return fmt.Errorf("failed to record tombstone event for %s: %w", id, err)
		}

		// Mark issue as dirty for incremental export
		_, err = tx.ExecContext(ctx, `
			INSERT INTO dirty_issues (issue_id, marked_at)
			VALUES (?, ?)
			ON CONFLICT (issue_id) DO UPDATE SET marked_at = excluded.marked_at
		`, id, now)
		if err != nil {
			return fmt.Errorf("failed to mark issue dirty for %s: %w", id, err)
		}
	}

	// 4. Invalidate blocked issues cache since statuses changed
	if err := s.invalidateBlockedCache(ctx, tx); err != nil {
		return fmt.Errorf("failed to invalidate blocked cache: %w", err)
	}

	result.DeletedCount = deletedCount
	return nil
}

// findAllDependentsRecursive finds all issues that depend on the given issues, recursively
func (s *SQLiteStorage) findAllDependentsRecursive(ctx context.Context, tx *sql.Tx, ids []string) (map[string]bool, error) {
	result := make(map[string]bool)
	for _, id := range ids {
		result[id] = true
	}

	toProcess := make([]string, len(ids))
	copy(toProcess, ids)

	for len(toProcess) > 0 {
		current := toProcess[0]
		toProcess = toProcess[1:]

		rows, err := tx.QueryContext(ctx,
			`SELECT issue_id FROM dependencies WHERE depends_on_id = ?`, current)
		if err != nil {
			return nil, err
		}
		defer rows.Close()

		for rows.Next() {
			var depID string
			if err := rows.Scan(&depID); err != nil {
				return nil, err
			}
			if !result[depID] {
				result[depID] = true
				toProcess = append(toProcess, depID)
			}
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}

	return result, nil
}

// SearchIssues finds issues matching query and filters
func (s *SQLiteStorage) SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error) {
	// Check for external database file modifications (daemon mode)
	s.checkFreshness()

	// Hold read lock during database operations to prevent reconnect() from
	// closing the connection mid-query (GH#607 race condition fix)
	s.reconnectMu.RLock()
	defer s.reconnectMu.RUnlock()

	whereClauses := []string{}
	args := []interface{}{}

	if query != "" {
		whereClauses = append(whereClauses, "(title LIKE ? OR description LIKE ? OR id LIKE ?)")
		pattern := "%" + query + "%"
		args = append(args, pattern, pattern, pattern)
	}

	if filter.TitleSearch != "" {
		whereClauses = append(whereClauses, "title LIKE ?")
		pattern := "%" + filter.TitleSearch + "%"
		args = append(args, pattern)
	}

	// Pattern matching
	if filter.TitleContains != "" {
		whereClauses = append(whereClauses, "title LIKE ?")
		args = append(args, "%"+filter.TitleContains+"%")
	}
	if filter.DescriptionContains != "" {
		whereClauses = append(whereClauses, "description LIKE ?")
		args = append(args, "%"+filter.DescriptionContains+"%")
	}
	if filter.NotesContains != "" {
		whereClauses = append(whereClauses, "notes LIKE ?")
		args = append(args, "%"+filter.NotesContains+"%")
	}

	if filter.Status != nil {
		whereClauses = append(whereClauses, "status = ?")
		args = append(args, *filter.Status)
	} else if !filter.IncludeTombstones {
		// Exclude tombstones by default unless explicitly filtering for them
		whereClauses = append(whereClauses, "status != ?")
		args = append(args, types.StatusTombstone)
	}

	// Status exclusion (for default non-closed behavior, GH#788)
	if len(filter.ExcludeStatus) > 0 {
		placeholders := make([]string, len(filter.ExcludeStatus))
		for i, s := range filter.ExcludeStatus {
			placeholders[i] = "?"
			args = append(args, string(s))
		}
		whereClauses = append(whereClauses, fmt.Sprintf("status NOT IN (%s)", strings.Join(placeholders, ",")))
	}

	if filter.Priority != nil {
		whereClauses = append(whereClauses, "priority = ?")
		args = append(args, *filter.Priority)
	}

	// Priority ranges
	if filter.PriorityMin != nil {
		whereClauses = append(whereClauses, "priority >= ?")
		args = append(args, *filter.PriorityMin)
	}
	if filter.PriorityMax != nil {
		whereClauses = append(whereClauses, "priority <= ?")
		args = append(args, *filter.PriorityMax)
	}

	if filter.IssueType != nil {
		whereClauses = append(whereClauses, "issue_type = ?")
		args = append(args, *filter.IssueType)
	}

	if filter.Assignee != nil {
		whereClauses = append(whereClauses, "assignee = ?")
		args = append(args, *filter.Assignee)
	}

	// Date ranges
	if filter.CreatedAfter != nil {
		whereClauses = append(whereClauses, "created_at > ?")
		args = append(args, filter.CreatedAfter.Format(time.RFC3339))
	}
	if filter.CreatedBefore != nil {
		whereClauses = append(whereClauses, "created_at < ?")
		args = append(args, filter.CreatedBefore.Format(time.RFC3339))
	}
	if filter.UpdatedAfter != nil {
		whereClauses = append(whereClauses, "updated_at > ?")
		args = append(args, filter.UpdatedAfter.Format(time.RFC3339))
	}
	if filter.UpdatedBefore != nil {
		whereClauses = append(whereClauses, "updated_at < ?")
		args = append(args, filter.UpdatedBefore.Format(time.RFC3339))
	}
	if filter.ClosedAfter != nil {
		whereClauses = append(whereClauses, "closed_at > ?")
		args = append(args, filter.ClosedAfter.Format(time.RFC3339))
	}
	if filter.ClosedBefore != nil {
		whereClauses = append(whereClauses, "closed_at < ?")
		args = append(args, filter.ClosedBefore.Format(time.RFC3339))
	}

	// Empty/null checks
	if filter.EmptyDescription {
		whereClauses = append(whereClauses, "(description IS NULL OR description = '')")
	}
	if filter.NoAssignee {
		whereClauses = append(whereClauses, "(assignee IS NULL OR assignee = '')")
	}
	if filter.NoLabels {
		whereClauses = append(whereClauses, "id NOT IN (SELECT DISTINCT issue_id FROM labels)")
	}

	// Label filtering: issue must have ALL specified labels
	if len(filter.Labels) > 0 {
		for _, label := range filter.Labels {
			whereClauses = append(whereClauses, "id IN (SELECT issue_id FROM labels WHERE label = ?)")
			args = append(args, label)
		}
	}

	// Label filtering (OR): issue must have AT LEAST ONE of these labels
	if len(filter.LabelsAny) > 0 {
		placeholders := make([]string, len(filter.LabelsAny))
		for i, label := range filter.LabelsAny {
			placeholders[i] = "?"
			args = append(args, label)
		}
		whereClauses = append(whereClauses, fmt.Sprintf("id IN (SELECT issue_id FROM labels WHERE label IN (%s))", strings.Join(placeholders, ", ")))
	}

	// ID filtering: match specific issue IDs
	if len(filter.IDs) > 0 {
		placeholders := make([]string, len(filter.IDs))
		for i, id := range filter.IDs {
			placeholders[i] = "?"
			args = append(args, id)
		}
		whereClauses = append(whereClauses, fmt.Sprintf("id IN (%s)", strings.Join(placeholders, ", ")))
	}

	// Wisp filtering
	if filter.Ephemeral != nil {
		if *filter.Ephemeral {
			whereClauses = append(whereClauses, "ephemeral = 1") // SQL column is still 'ephemeral'
		} else {
			whereClauses = append(whereClauses, "(ephemeral = 0 OR ephemeral IS NULL)")
		}
	}

	// Pinned filtering
	if filter.Pinned != nil {
		if *filter.Pinned {
			whereClauses = append(whereClauses, "pinned = 1")
		} else {
			whereClauses = append(whereClauses, "(pinned = 0 OR pinned IS NULL)")
		}
	}

	// Template filtering
	if filter.IsTemplate != nil {
		if *filter.IsTemplate {
			whereClauses = append(whereClauses, "is_template = 1")
		} else {
			whereClauses = append(whereClauses, "(is_template = 0 OR is_template IS NULL)")
		}
	}

	// Parent filtering: filter children by parent issue
	if filter.ParentID != nil {
		whereClauses = append(whereClauses, "id IN (SELECT issue_id FROM dependencies WHERE type = 'parent-child' AND depends_on_id = ?)")
		args = append(args, *filter.ParentID)
	}

	// Molecule type filtering
	if filter.MolType != nil {
		whereClauses = append(whereClauses, "mol_type = ?")
		args = append(args, string(*filter.MolType))
	}

	whereSQL := ""
	if len(whereClauses) > 0 {
		whereSQL = "WHERE " + strings.Join(whereClauses, " AND ")
	}

	limitSQL := ""
	if filter.Limit > 0 {
		limitSQL = " LIMIT ?"
		args = append(args, filter.Limit)
	}

	// #nosec G201 - safe SQL with controlled formatting
	querySQL := fmt.Sprintf(`
		SELECT id, content_hash, title, description, design, acceptance_criteria, notes,
		       status, priority, issue_type, assignee, estimated_minutes,
		       created_at, created_by, updated_at, closed_at, external_ref, source_repo, close_reason,
		       deleted_at, deleted_by, delete_reason, original_type,
		       sender, ephemeral, pinned, is_template,
		       await_type, await_id, timeout_ns, waiters
		FROM issues
		%s
		ORDER BY priority ASC, created_at DESC
		%s
	`, whereSQL, limitSQL)

	rows, err := s.db.QueryContext(ctx, querySQL, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to search issues: %w", err)
	}
	defer func() { _ = rows.Close() }()

	return s.scanIssues(ctx, rows)
}
