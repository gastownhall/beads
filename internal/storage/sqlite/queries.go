package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// NOTE: createGraphEdgesFromIssueFields and createGraphEdgesFromUpdates removed
// per Decision 004 Phase 4 - Edge Schema Consolidation.
// Graph edges (replies-to, relates-to, duplicates, supersedes) are now managed
// exclusively through the dependency API. Use AddDependency() instead.

// Parsing utilities (parseTimeString, parseNullableTimeString, etc.) moved to parsing.go
// Search operations (SearchIssues, filterByLabelRegex) moved to search.go
// Delete operations (DeleteIssue, DeleteIssues, helpers) moved to delete.go

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
	// Fetch custom statuses and types for validation
	customStatuses, err := s.GetCustomStatuses(ctx)
	if err != nil {
		return fmt.Errorf("failed to get custom statuses: %w", err)
	}
	customTypes, err := s.GetCustomTypes(ctx)
	if err != nil {
		return fmt.Errorf("failed to get custom types: %w", err)
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

	// Validate issue before creating (with custom status and type support)
	if err := issue.ValidateWithCustom(customStatuses, customTypes); err != nil {
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

	// Start IMMEDIATE transaction with retry logic for SQLITE_BUSY.
	// IMMEDIATE acquires a RESERVED lock immediately, preventing other IMMEDIATE or EXCLUSIVE
	// transactions from starting. This serializes ID generation across concurrent writers.
	//
	// We use raw Exec instead of BeginTx because database/sql doesn't support transaction
	// modes in BeginTx, and modernc.org/sqlite's BeginTx always uses DEFERRED mode.
	//
	// Retries with exponential backoff handle cases where busy_timeout alone is insufficient.
	if err := beginImmediateWithRetry(ctx, conn); err != nil {
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
	if errors.Is(err, sql.ErrNoRows) || configPrefix == "" {
		// CRITICAL: Reject operation if issue_prefix config is missing
		// This prevents duplicate issues with wrong prefix
		return fmt.Errorf("database not initialized: issue_prefix config is missing (run 'bd init --prefix <prefix>' first)")
	} else if err != nil {
		return fmt.Errorf("failed to get config: %w", err)
	}

	// Determine prefix for ID generation and validation:
	// 1. PrefixOverride completely replaces config prefix (for cross-rig creation)
	// 2. IDPrefix appends to config prefix (e.g., "bd" + "wisp" → "bd-wisp")
	// 3. Otherwise use config prefix as-is
	prefix := configPrefix
	if issue.PrefixOverride != "" {
		prefix = issue.PrefixOverride
	} else if issue.IDPrefix != "" {
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
		if err := validateIssueIDPrefix(issue.ID, prefix); err != nil {
			return wrapDBError("validate issue ID prefix", err)
		}

		// For hierarchical IDs (bd-a3f8e9.1), ensure parent exists
		// Use isHierarchicalID to correctly handle prefixes with dots (GH#508)
		if isHierarchical, parentID := isHierarchicalID(issue.ID); isHierarchical {
			// Check that parent exists
			var parentCount int
			if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM issues WHERE id = ?`, parentID).Scan(&parentCount); err != nil {
				return wrapDBError("check parent existence", err)
			}
			if parentCount == 0 {
				return fmt.Errorf("parent issue %s does not exist", parentID)
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

	// Insert issue using strict mode (fails on duplicates)
	// GH#956: Use insertIssueStrict instead of insertIssue to prevent FK constraint errors
	// from silent INSERT OR IGNORE failures under concurrent load.
	if err := insertIssueStrict(ctx, conn, issue); err != nil {
		return wrapDBError("insert issue", err)
	}

	// Record creation event
	if err := recordCreatedEvent(ctx, conn, issue, actor); err != nil {
		return wrapDBError("record creation event", err)
	}

	// NOTE: Graph edges (replies-to, relates-to, duplicates, supersedes) are now
	// managed via AddDependency() per Decision 004 Phase 4.

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
	var createdAtStr sql.NullString // TEXT column - must parse manually for cross-driver compatibility
	var updatedAtStr sql.NullString // TEXT column - must parse manually for cross-driver compatibility
	var closedAt sql.NullTime
	var estimatedMinutes sql.NullInt64
	var assignee sql.NullString
	var externalRef sql.NullString
	var specID sql.NullString
	var compactedAt sql.NullTime
	var originalSize sql.NullInt64
	var sourceRepo sql.NullString
	var closeReason sql.NullString
	// Messaging fields
	var sender sql.NullString
	var wisp sql.NullInt64
	var wispType sql.NullString
	// Pinned field
	var pinned sql.NullInt64
	// Template field
	var isTemplate sql.NullInt64
	// Crystallizes field (work economics)
	var crystallizes sql.NullInt64
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
	// Time-based scheduling fields (GH#820)
	var dueAt sql.NullTime
	var deferUntil sql.NullTime
	// Custom metadata field (GH#1406)
	var metadata sql.NullString

	var contentHash sql.NullString
	var compactedAtCommit sql.NullString
	var owner sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT id, content_hash, title, description, design, acceptance_criteria, notes,
		       status, priority, issue_type, assignee, estimated_minutes,
		       created_at, created_by, owner, updated_at, closed_at, external_ref,
		       spec_id, compaction_level, compacted_at, compacted_at_commit, original_size, source_repo, close_reason,
		       sender, ephemeral, wisp_type, pinned, is_template, crystallizes,
		       await_type, await_id, timeout_ns, waiters,
		       hook_bead, role_bead, agent_state, last_activity, role_type, rig, mol_type,
		       event_kind, actor, target, payload,
		       due_at, defer_until, metadata
		FROM issues
		WHERE id = ?
	`, id).Scan(
		&issue.ID, &contentHash, &issue.Title, &issue.Description, &issue.Design,
		&issue.AcceptanceCriteria, &issue.Notes, &issue.Status,
		&issue.Priority, &issue.IssueType, &assignee, &estimatedMinutes,
		&createdAtStr, &issue.CreatedBy, &owner, &updatedAtStr, &closedAt, &externalRef,
		&specID, &issue.CompactionLevel, &compactedAt, &compactedAtCommit, &originalSize, &sourceRepo, &closeReason,
		&sender, &wisp, &wispType, &pinned, &isTemplate, &crystallizes,
		&awaitType, &awaitID, &timeoutNs, &waiters,
		&hookBead, &roleBead, &agentState, &lastActivity, &roleType, &rig, &molType,
		&eventKind, &actor, &target, &payload,
		&dueAt, &deferUntil, &metadata,
	)

	if errors.Is(err, sql.ErrNoRows) {
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
	// Messaging fields
	if sender.Valid {
		issue.Sender = sender.String
	}
	if wisp.Valid && wisp.Int64 != 0 {
		issue.Ephemeral = true
	}
	if wispType.Valid {
		issue.WispType = types.WispType(wispType.String)
	}
	// Pinned field
	if pinned.Valid && pinned.Int64 != 0 {
		issue.Pinned = true
	}
	// Template field
	if isTemplate.Valid && isTemplate.Int64 != 0 {
		issue.IsTemplate = true
	}
	// Crystallizes field (work economics)
	if crystallizes.Valid && crystallizes.Int64 != 0 {
		issue.Crystallizes = true
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
	// Time-based scheduling fields (GH#820)
	if dueAt.Valid {
		issue.DueAt = &dueAt.Time
	}
	if deferUntil.Valid {
		issue.DeferUntil = &deferUntil.Time
	}
	// Custom metadata field (GH#1406)
	if metadata.Valid && metadata.String != "" && metadata.String != "{}" {
		issue.Metadata = []byte(metadata.String)
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

	if errors.Is(err, sql.ErrNoRows) {
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
	var createdAtStr sql.NullString // TEXT column - must parse manually for cross-driver compatibility
	var updatedAtStr sql.NullString // TEXT column - must parse manually for cross-driver compatibility
	var closedAt sql.NullTime
	var estimatedMinutes sql.NullInt64
	var assignee sql.NullString
	var externalRefCol sql.NullString
	var specID sql.NullString
	var compactedAt sql.NullTime
	var originalSize sql.NullInt64
	var contentHash sql.NullString
	var compactedAtCommit sql.NullString
	var sourceRepo sql.NullString
	var closeReason sql.NullString
	// Messaging fields
	var sender sql.NullString
	var wisp sql.NullInt64
	var wispType sql.NullString
	// Pinned field
	var pinned sql.NullInt64
	// Template field
	var isTemplate sql.NullInt64
	// Crystallizes field (work economics)
	var crystallizes sql.NullInt64
	// Gate fields
	var awaitType sql.NullString
	var awaitID sql.NullString
	var timeoutNs sql.NullInt64
	var waiters sql.NullString

	var owner sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT id, content_hash, title, description, design, acceptance_criteria, notes,
		       status, priority, issue_type, assignee, estimated_minutes,
		       created_at, created_by, owner, updated_at, closed_at, external_ref,
		       spec_id, compaction_level, compacted_at, compacted_at_commit, original_size, source_repo, close_reason,
		       sender, ephemeral, wisp_type, pinned, is_template, crystallizes,
		       await_type, await_id, timeout_ns, waiters
		FROM issues
		WHERE external_ref = ?
	`, externalRef).Scan(
		&issue.ID, &contentHash, &issue.Title, &issue.Description, &issue.Design,
		&issue.AcceptanceCriteria, &issue.Notes, &issue.Status,
		&issue.Priority, &issue.IssueType, &assignee, &estimatedMinutes,
		&createdAtStr, &issue.CreatedBy, &owner, &updatedAtStr, &closedAt, &externalRefCol,
		&specID, &issue.CompactionLevel, &compactedAt, &compactedAtCommit, &originalSize, &sourceRepo, &closeReason,
		&sender, &wisp, &wispType, &pinned, &isTemplate, &crystallizes,
		&awaitType, &awaitID, &timeoutNs, &waiters,
	)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get issue by external_ref: %w", err)
	}

	// Parse timestamp strings (TEXT columns require manual parsing)
	if createdAtStr.Valid {
		issue.CreatedAt = parseTimeString(createdAtStr.String)
	}
	if updatedAtStr.Valid {
		issue.UpdatedAt = parseTimeString(updatedAtStr.String)
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
	if owner.Valid {
		issue.Owner = owner.String
	}
	if externalRefCol.Valid {
		issue.ExternalRef = &externalRefCol.String
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
	// Messaging fields
	if sender.Valid {
		issue.Sender = sender.String
	}
	if wisp.Valid && wisp.Int64 != 0 {
		issue.Ephemeral = true
	}
	if wispType.Valid {
		issue.WispType = types.WispType(wispType.String)
	}
	// Pinned field
	if pinned.Valid && pinned.Int64 != 0 {
		issue.Pinned = true
	}
	// Template field
	if isTemplate.Valid && isTemplate.Int64 != 0 {
		issue.IsTemplate = true
	}
	// Crystallizes field (work economics)
	if crystallizes.Valid && crystallizes.Int64 != 0 {
		issue.Crystallizes = true
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
	"spec_id":             true,
	"closed_at":           true,
	"close_reason":        true,
	"closed_by_session":   true,
	// Source repo field (for multi-repo migration)
	"source_repo": true,
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
	// Time-based scheduling fields (GH#820)
	"due_at":      true,
	"defer_until": true,
	// Gate fields (bd-z6kw: support await_id updates for gate discovery)
	"await_id": true,
	"waiters":  true,
	// Custom metadata field (GH#1406)
	"metadata": true,
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

	// Fetch custom statuses and types for validation
	customStatuses, err := s.GetCustomStatuses(ctx)
	if err != nil {
		return wrapDBError("get custom statuses", err)
	}

	customTypes, err := s.GetCustomTypes(ctx)
	if err != nil {
		return wrapDBError("get custom types", err)
	}

	// Build update query with validated field names
	setClauses := []string{"updated_at = ?"}
	args := []interface{}{time.Now()}

	for key, value := range updates {
		// Prevent SQL injection by validating field names
		if !allowedUpdateFields[key] {
			return fmt.Errorf("invalid field for update: %s", key)
		}

		// Validate field values (with custom status and type support)
		if err := validateFieldUpdateWithCustom(key, value, customStatuses, customTypes); err != nil {
			return wrapDBError("validate field update", err)
		}

		// Map API field names to database column names (wisp -> ephemeral)
		columnName := key
		if key == "wisp" {
			columnName = "ephemeral"
		}
		setClauses = append(setClauses, fmt.Sprintf("%s = ?", columnName))

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

	// Auto-manage closed_at when status changes (enforce invariant)
	setClauses, args = manageClosedAt(oldIssue, updates, setClauses, args)

	// Recompute content_hash if any content fields changed
	contentChanged := false
	contentFields := []string{"title", "description", "design", "acceptance_criteria", "notes", "status", "priority", "issue_type", "assignee", "external_ref", "spec_id"}
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
			case "spec_id":
				if value == nil {
					updatedIssue.SpecID = ""
				} else {
					updatedIssue.SpecID = value.(string)
				}
			}
		}
		newHash := updatedIssue.ComputeContentHash()
		setClauses = append(setClauses, "content_hash = ?")
		args = append(args, newHash)
	}

	args = append(args, id)

	// Prepare event data before transaction
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
	statusChanged := false
	if _, ok := updates["status"]; ok {
		statusChanged = true
	}

	// Execute in transaction using BEGIN IMMEDIATE (GH#1272 fix)
	return s.withTx(ctx, func(conn *sql.Conn) error {
		// Update issue
		query := fmt.Sprintf("UPDATE issues SET %s WHERE id = ?", strings.Join(setClauses, ", ")) // #nosec G201 - safe SQL with controlled column names
		_, err := conn.ExecContext(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("failed to update issue: %w", err)
		}

		// Record event
		_, err = conn.ExecContext(ctx, `
			INSERT INTO events (issue_id, event_type, actor, old_value, new_value)
			VALUES (?, ?, ?, ?, ?)
		`, id, eventType, actor, oldDataStr, newDataStr)
		if err != nil {
			return fmt.Errorf("failed to record event: %w", err)
		}

		// NOTE: Graph edges now managed via AddDependency() per Decision 004 Phase 4.

		// Invalidate blocked issues cache if status changed
		// Status changes affect which issues are blocked (blockers must be open/in_progress/blocked)
		if statusChanged {
			if err := s.invalidateBlockedCache(ctx, conn); err != nil {
				return fmt.Errorf("failed to invalidate blocked cache: %w", err)
			}
		}

		return nil
	})
}

// ClaimIssue atomically claims an issue using compare-and-swap semantics.
// It sets the assignee to actor and status to "in_progress" only if the issue
// currently has no assignee (empty string). This is done in a single transaction
// with a conditional UPDATE to prevent race conditions where multiple concurrent
// callers could both successfully claim the same issue.
//
// Returns storage.ErrAlreadyClaimed (wrapped with current assignee) if the issue
// is already claimed. Returns an error if the issue doesn't exist.
func (s *SQLiteStorage) ClaimIssue(ctx context.Context, id string, actor string) error {
	// Get the issue first to check existence and get old data for event
	oldIssue, err := s.GetIssue(ctx, id)
	if err != nil {
		return wrapDBError("get issue for claim", err)
	}
	if oldIssue == nil {
		return fmt.Errorf("issue %s not found", id)
	}

	// Prepare event data
	oldData, err := json.Marshal(oldIssue)
	if err != nil {
		oldData = []byte(fmt.Sprintf(`{"id":"%s"}`, id))
	}
	newUpdates := map[string]interface{}{
		"assignee": actor,
		"status":   "in_progress",
	}
	newData, err := json.Marshal(newUpdates)
	if err != nil {
		newData = []byte(`{}`)
	}

	now := time.Now()

	// Compute new content hash
	updatedIssue := *oldIssue
	updatedIssue.Assignee = actor
	updatedIssue.Status = types.StatusInProgress
	newHash := updatedIssue.ComputeContentHash()

	var alreadyClaimedBy string

	err = s.withTx(ctx, func(conn *sql.Conn) error {
		// Use conditional UPDATE with WHERE clause to ensure atomicity.
		// The UPDATE only succeeds if assignee is currently empty.
		// This is the compare-and-swap: we compare assignee='' and swap to new value.
		result, err := conn.ExecContext(ctx, `
			UPDATE issues
			SET assignee = ?, status = 'in_progress', updated_at = ?, content_hash = ?
			WHERE id = ? AND (assignee = '' OR assignee IS NULL)
		`, actor, now, newHash, id)
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
			err := conn.QueryRowContext(ctx, `SELECT assignee FROM issues WHERE id = ?`, id).Scan(&currentAssignee)
			if err != nil {
				return fmt.Errorf("failed to get current assignee: %w", err)
			}
			alreadyClaimedBy = currentAssignee
			// Return a sentinel error that we'll convert outside the transaction
			return fmt.Errorf("already claimed")
		}

		// Record the claim event
		_, err = conn.ExecContext(ctx, `
			INSERT INTO events (issue_id, event_type, actor, old_value, new_value)
			VALUES (?, ?, ?, ?, ?)
		`, id, "claimed", actor, string(oldData), string(newData))
		if err != nil {
			return fmt.Errorf("failed to record claim event: %w", err)
		}

		// Invalidate blocked issues cache since status changed
		if err := s.invalidateBlockedCache(ctx, conn); err != nil {
			return fmt.Errorf("failed to invalidate blocked cache: %w", err)
		}

		return nil
	})

	if err != nil {
		if strings.Contains(err.Error(), "already claimed") {
			return fmt.Errorf("%w by %s", storage.ErrAlreadyClaimed, alreadyClaimedBy)
		}
		return err
	}

	return nil
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

	_, err = tx.ExecContext(ctx, `UPDATE issue_snapshots SET issue_id = ? WHERE issue_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update issue_snapshots: %w", err)
	}

	_, err = tx.ExecContext(ctx, `UPDATE compaction_snapshots SET issue_id = ? WHERE issue_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update compaction_snapshots: %w", err)
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

	// GH#1016: Rebuild blocked_issues_cache since it stores issue IDs
	// that have now been renamed
	if err := s.invalidateBlockedCache(ctx, nil); err != nil {
		return fmt.Errorf("failed to rebuild blocked cache: %w", err)
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

// CloseIssue closes an issue with a reason.
// The session parameter tracks which Claude Code session closed the issue (can be empty).
func (s *SQLiteStorage) CloseIssue(ctx context.Context, id string, reason string, actor string, session string) error {
	now := time.Now()

	// Execute in transaction using BEGIN IMMEDIATE (GH#1272 fix)
	return s.withTx(ctx, func(conn *sql.Conn) error {
		// NOTE: close_reason is stored in two places:
		// 1. issues.close_reason - for direct queries (bd show --json, exports)
		// 2. events.comment - for audit history (when was it closed, by whom)
		// Keep both in sync. If refactoring, consider deriving one from the other.
		result, err := conn.ExecContext(ctx, `
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

		_, err = conn.ExecContext(ctx, `
			INSERT INTO events (issue_id, event_type, actor, comment)
			VALUES (?, ?, ?, ?)
		`, id, types.EventClosed, actor, reason)
		if err != nil {
			return fmt.Errorf("failed to record event: %w", err)
		}

		// Invalidate blocked issues cache since status changed to closed
		// Closed issues don't block others, so this affects blocking calculations
		if err := s.invalidateBlockedCache(ctx, conn); err != nil {
			return fmt.Errorf("failed to invalidate blocked cache: %w", err)
		}

		// Reactive convoy completion: check if any convoys tracking this issue should auto-close
		// Find convoys that track this issue (convoy.issue_id tracks closed_issue.depends_on_id)
		// Uses gt:convoy label instead of issue_type for Gas Town separation
		convoyRows, err := conn.QueryContext(ctx, `
			SELECT DISTINCT d.issue_id
			FROM dependencies d
			JOIN issues i ON d.issue_id = i.id
			JOIN labels l ON i.id = l.issue_id AND l.label = 'gt:convoy'
			WHERE d.depends_on_id = ?
			  AND d.type = ?
			  AND i.status != ?
		`, id, types.DepTracks, types.StatusClosed)
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
			err := conn.QueryRowContext(ctx, `
				SELECT COUNT(*)
				FROM dependencies d
				JOIN issues i ON d.depends_on_id = i.id
				WHERE d.issue_id = ?
				  AND d.type = ?
				  AND i.status != ?
			`, convoyID, types.DepTracks, types.StatusClosed).Scan(&openCount)
			if err != nil {
				return fmt.Errorf("failed to count open tracked issues for convoy %s: %w", convoyID, err)
			}

			// If all tracked issues are closed, auto-close the convoy
			if openCount == 0 {
				closeReason := "All tracked issues completed"
				_, err := conn.ExecContext(ctx, `
					UPDATE issues SET status = ?, closed_at = ?, updated_at = ?, close_reason = ?
					WHERE id = ?
				`, types.StatusClosed, now, now, closeReason, convoyID)
				if err != nil {
					return fmt.Errorf("failed to auto-close convoy %s: %w", convoyID, err)
				}

				// Record the close event
				_, err = conn.ExecContext(ctx, `
					INSERT INTO events (issue_id, event_type, actor, comment)
					VALUES (?, ?, ?, ?)
				`, convoyID, types.EventClosed, "system:convoy-completion", closeReason)
				if err != nil {
					return fmt.Errorf("failed to record convoy close event: %w", err)
				}

			}
		}

		return nil
	})
}
