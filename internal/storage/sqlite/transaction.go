// Package sqlite implements the storage interface using SQLite.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// Verify sqliteTxStorage implements storage.Transaction at compile time
var _ storage.Transaction = (*sqliteTxStorage)(nil)

// sqliteTxStorage implements the storage.Transaction interface for SQLite.
// It wraps a dedicated database connection with an active transaction.
type sqliteTxStorage struct {
	conn   *sql.Conn      // Dedicated connection for the transaction
	parent *SQLiteStorage // Parent storage for accessing shared state
}

// RunInTransaction executes a function within a database transaction.
//
// The transaction uses BEGIN IMMEDIATE to acquire a write lock early,
// preventing deadlocks when multiple goroutines compete for the same lock.
//
// Transaction lifecycle:
//  1. Increment active transaction counter (blocks reconnect during active transactions)
//  2. Acquire dedicated connection from pool
//  3. Begin IMMEDIATE transaction with retry on SQLITE_BUSY
//  4. Execute user function with Transaction interface
//  5. On success: COMMIT
//  6. On error or panic: ROLLBACK
//  7. Decrement active transaction counter (via defer)
//
// Panic safety: If the callback panics, the transaction is rolled back
// and the panic is re-raised to the caller.
func (s *SQLiteStorage) RunInTransaction(ctx context.Context, fn func(tx storage.Transaction) error) error {
	// Increment active transaction count BEFORE acquiring connection.
	// This blocks reconnect() from closing the connection while we're using it.
	// The counter is decremented via defer to ensure it always happens.
	s.activeTxCount.Add(1)
	defer s.activeTxCount.Add(-1)

	// Acquire a dedicated connection for the transaction.
	// This ensures all operations in the transaction use the same connection.
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("failed to acquire connection for transaction: %w", err)
	}
	defer func() { _ = conn.Close() }()

	// Start IMMEDIATE transaction to acquire write lock early.
	// Use retry logic with exponential backoff to handle SQLITE_BUSY
	if err := beginImmediateWithRetry(ctx, conn); err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}

	// Track commit state for cleanup
	committed := false
	defer func() {
		if !committed {
			// Use background context to ensure rollback completes even if ctx is canceled
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	// Handle panics: rollback and re-raise
	defer func() {
		if r := recover(); r != nil {
			// Rollback will happen via the committed=false check above
			panic(r) // Re-raise the panic
		}
	}()

	// Create transaction wrapper
	txStorage := &sqliteTxStorage{
		conn:   conn,
		parent: s,
	}

	// Execute user function
	if err := fn(txStorage); err != nil {
		return err // Rollback happens in defer
	}

	// Commit the transaction
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}
	committed = true
	return nil
}

// beginImmediateWithRetry starts a transaction with BEGIN IMMEDIATE, retrying
// on SQLITE_BUSY errors with exponential backoff.
//
// This is more robust than relying solely on busy_timeout because:
// 1. Some BUSY errors aren't retryable by the busy handler (SQLITE_BUSY_SNAPSHOT)
// 2. Explicit retries give us control over backoff timing
// 3. We can log retry attempts for debugging
func beginImmediateWithRetry(ctx context.Context, conn *sql.Conn) error {
	const maxRetries = 5
	const initialBackoff = 10 * time.Millisecond
	backoff := initialBackoff
	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		_, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE")
		if err == nil {
			return nil
		}
		lastErr = err

		// Check if this is a retryable error (SQLITE_BUSY)
		errStr := err.Error()
		if !strings.Contains(errStr, "database is locked") &&
			!strings.Contains(errStr, "SQLITE_BUSY") {
			// Not a busy error, don't retry
			return err
		}

		// Check context before sleeping
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// Sleep with exponential backoff
		if attempt < maxRetries {
			time.Sleep(backoff)
			backoff *= 2
		}
	}

	return fmt.Errorf("failed to begin transaction after %d retries: %w", maxRetries, lastErr)
}

// CreateIssue creates a new issue within the transaction.
func (t *sqliteTxStorage) CreateIssue(ctx context.Context, issue *types.Issue, actor string) error {
	// Fetch custom statuses and types for validation
	customStatuses, err := t.GetCustomStatuses(ctx)
	if err != nil {
		return fmt.Errorf("failed to get custom statuses: %w", err)
	}
	customTypes, err := t.GetCustomTypes(ctx)
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

	// Defensive fix for deleted_at invariant: tombstones must have deleted_at
	if issue.Status == types.StatusTombstone && issue.DeletedAt == nil {
		maxTime := issue.CreatedAt
		if issue.UpdatedAt.After(maxTime) {
			maxTime = issue.UpdatedAt
		}
		deletedAt := maxTime.Add(time.Second)
		issue.DeletedAt = &deletedAt
	}

	// Validate issue before creating (with custom status and type support)
	if err := issue.ValidateWithCustom(customStatuses, customTypes); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	// Compute content hash
	if issue.ContentHash == "" {
		issue.ContentHash = issue.ComputeContentHash()
	}

	// Get prefix from config (needed for both ID generation and validation)
	var configPrefix string
	err = t.conn.QueryRowContext(ctx, `SELECT value FROM config WHERE key = ?`, "issue_prefix").Scan(&configPrefix)
	if errors.Is(err, sql.ErrNoRows) || configPrefix == "" {
		// CRITICAL: Reject operation if issue_prefix config is missing
		return fmt.Errorf("database not initialized: issue_prefix config is missing (run 'bd init --prefix <prefix>' first)")
	} else if err != nil {
		return fmt.Errorf("failed to get config: %w", err)
	}

	// Determine prefix for ID generation and validation:
	// 1. PrefixOverride completely replaces config prefix (for cross-rig creation)
	// 2. IDPrefix appends to config prefix (e.g., "bd" + "wisp" → "bd-wisp")
	// 3. Otherwise use config prefix as-is
	prefix := configPrefix
	skipPrefixValidation := false
	if issue.PrefixOverride != "" {
		prefix = issue.PrefixOverride
		skipPrefixValidation = true // Caller explicitly specified prefix, skip validation
	} else if issue.IDPrefix != "" {
		prefix = configPrefix + "-" + issue.IDPrefix
	}

	// Generate or validate ID
	if issue.ID == "" {
		// Generate hash-based ID with adaptive length based on database size
		generatedID, err := GenerateIssueID(ctx, t.conn, prefix, issue, actor)
		if err != nil {
			return fmt.Errorf("failed to generate issue ID: %w", err)
		}
		issue.ID = generatedID
	} else {
		// Validate that explicitly provided ID matches the configured prefix
		// Skip validation when PrefixOverride is set (cross-rig creation)
		if !skipPrefixValidation {
			if err := validateIssueIDPrefix(issue.ID, prefix); err != nil {
				return fmt.Errorf("failed to validate issue ID prefix: %w", err)
			}
		}

		// For hierarchical IDs (bd-a3f8e9.1), ensure parent exists
		// Use isHierarchicalID to correctly handle prefixes with dots (GH#508)
		if isHierarchical, parentID := isHierarchicalID(issue.ID); isHierarchical {
			// Try to resurrect entire parent chain if any parents are missing
			resurrected, err := t.parent.tryResurrectParentChainWithConn(ctx, t.conn, issue.ID)
			if err != nil {
				return fmt.Errorf("failed to resurrect parent chain for %s: %w", issue.ID, err)
			}
			if !resurrected {
				// Parent(s) not found in JSONL history - cannot proceed
				return fmt.Errorf("parent issue %s does not exist and could not be resurrected from JSONL history", parentID)
			}
		}
	}

	// Insert issue using strict mode (fails on duplicates)
	// GH#956: Use insertIssueStrict instead of insertIssue to prevent FK constraint errors
	// from silent INSERT OR IGNORE failures under concurrent load.
	if err := insertIssueStrict(ctx, t.conn, issue); err != nil {
		return fmt.Errorf("failed to insert issue: %w", err)
	}

	// Record creation event
	if err := recordCreatedEvent(ctx, t.conn, issue, actor); err != nil {
		return fmt.Errorf("failed to record creation event: %w", err)
	}

	// Mark issue as dirty for incremental export
	if err := markDirty(ctx, t.conn, issue.ID); err != nil {
		return fmt.Errorf("failed to mark issue dirty: %w", err)
	}

	return nil
}

// CreateIssues creates multiple issues within the transaction.
func (t *sqliteTxStorage) CreateIssues(ctx context.Context, issues []*types.Issue, actor string) error {
	if len(issues) == 0 {
		return nil
	}

	// Fetch custom statuses and types for validation
	customStatuses, err := t.GetCustomStatuses(ctx)
	if err != nil {
		return fmt.Errorf("failed to get custom statuses: %w", err)
	}
	customTypes, err := t.GetCustomTypes(ctx)
	if err != nil {
		return fmt.Errorf("failed to get custom types: %w", err)
	}

	// Validate and prepare all issues first (with custom status and type support)
	now := time.Now()
	for _, issue := range issues {
		// Set timestamps first so defensive fixes can use them
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

		if err := issue.ValidateWithCustom(customStatuses, customTypes); err != nil {
			return fmt.Errorf("validation failed for issue: %w", err)
		}
		if issue.ContentHash == "" {
			issue.ContentHash = issue.ComputeContentHash()
		}
	}

	// Get prefix from config
	var prefix string
	err = t.conn.QueryRowContext(ctx, `SELECT value FROM config WHERE key = ?`, "issue_prefix").Scan(&prefix)
	if errors.Is(err, sql.ErrNoRows) || prefix == "" {
		return fmt.Errorf("database not initialized: issue_prefix config is missing")
	} else if err != nil {
		return fmt.Errorf("failed to get config: %w", err)
	}

	// Generate IDs for issues that don't have them
	for _, issue := range issues {
		if issue.ID == "" {
			generatedID, err := GenerateIssueID(ctx, t.conn, prefix, issue, actor)
			if err != nil {
				return fmt.Errorf("failed to generate issue ID: %w", err)
			}
			issue.ID = generatedID
		} else {
			if err := validateIssueIDPrefix(issue.ID, prefix); err != nil {
				return fmt.Errorf("failed to validate issue ID prefix: %w", err)
			}
		}
	}

	// Check for duplicate IDs within the batch
	seenIDs := make(map[string]bool)
	for _, issue := range issues {
		if seenIDs[issue.ID] {
			return fmt.Errorf("duplicate issue ID within batch: %s", issue.ID)
		}
		seenIDs[issue.ID] = true
	}

	// GH#956: Check for conflicts with existing IDs in database before inserting.
	// This prevents duplicates from causing FK constraint failures when recording events.
	if err := checkForExistingIDs(ctx, t.conn, issues); err != nil {
		return err
	}

	// Insert all issues using strict mode (fails on duplicates)
	// GH#956: Use insertIssuesStrict instead of insertIssues to prevent FK constraint errors
	// from silent INSERT OR IGNORE failures under concurrent load.
	if err := insertIssuesStrict(ctx, t.conn, issues); err != nil {
		return fmt.Errorf("failed to insert issues: %w", err)
	}

	// Record creation events
	if err := recordCreatedEvents(ctx, t.conn, issues, actor); err != nil {
		return fmt.Errorf("failed to record creation events: %w", err)
	}

	// Mark all issues as dirty
	if err := markDirtyBatch(ctx, t.conn, issues); err != nil {
		return fmt.Errorf("failed to mark issues dirty: %w", err)
	}

	return nil
}

// GetIssue retrieves an issue within the transaction.
// This enables read-your-writes semantics within the transaction.
func (t *sqliteTxStorage) GetIssue(ctx context.Context, id string) (*types.Issue, error) {
	row := t.conn.QueryRowContext(ctx, `
		SELECT id, content_hash, title, description, design, acceptance_criteria, notes,
		       status, priority, issue_type, assignee, estimated_minutes,
		       created_at, created_by, owner, updated_at, closed_at, external_ref,
		       spec_id, compaction_level, compacted_at, compacted_at_commit, original_size, source_repo, close_reason,
		       deleted_at, deleted_by, delete_reason, original_type,
		       sender, ephemeral, pinned, is_template, crystallizes,
		       await_type, await_id, timeout_ns, waiters,
		       hook_bead, role_bead, agent_state, last_activity, role_type, rig, mol_type,
		       due_at, defer_until, metadata
		FROM issues
		WHERE id = ?
	`, id)

	issue, err := scanIssueRow(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get issue: %w", err)
	}

	// Fetch labels for this issue using the transaction connection
	labels, err := t.getLabels(ctx, issue.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to get labels: %w", err)
	}
	issue.Labels = labels

	return issue, nil
}

// getLabels retrieves labels using the transaction's connection
func (t *sqliteTxStorage) getLabels(ctx context.Context, issueID string) ([]string, error) {
	rows, err := t.conn.QueryContext(ctx, `
		SELECT label FROM labels WHERE issue_id = ? ORDER BY label
	`, issueID)
	if err != nil {
		return nil, fmt.Errorf("failed to get labels: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var labels []string
	for rows.Next() {
		var label string
		if err := rows.Scan(&label); err != nil {
			return nil, err
		}
		labels = append(labels, label)
	}

	return labels, nil
}

// UpdateIssue updates an issue within the transaction.
func (t *sqliteTxStorage) UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
	// Get old issue for event
	oldIssue, err := t.GetIssue(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to get issue for update: %w", err)
	}
	if oldIssue == nil {
		return fmt.Errorf("issue %s not found", id)
	}

	// Fetch custom statuses and types for validation
	customStatuses, err := t.GetCustomStatuses(ctx)
	if err != nil {
		return fmt.Errorf("failed to get custom statuses: %w", err)
	}

	customTypes, err := t.GetCustomTypes(ctx)
	if err != nil {
		return fmt.Errorf("failed to get custom types: %w", err)
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
			return fmt.Errorf("failed to validate field update: %w", err)
		}

		setClauses = append(setClauses, fmt.Sprintf("%s = ?", key))
		args = append(args, value)
	}

	// Auto-manage closed_at when status changes
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
		updatedIssue := *oldIssue
		applyUpdatesToIssue(&updatedIssue, updates)
		newHash := updatedIssue.ComputeContentHash()
		setClauses = append(setClauses, "content_hash = ?")
		args = append(args, newHash)
	}

	args = append(args, id)

	// Update issue
	query := fmt.Sprintf("UPDATE issues SET %s WHERE id = ?", strings.Join(setClauses, ", ")) // #nosec G201 - safe SQL with controlled column names
	_, err = t.conn.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("failed to update issue: %w", err)
	}

	// Record event
	oldData, err := json.Marshal(oldIssue)
	if err != nil {
		oldData = []byte(fmt.Sprintf(`{"id":"%s"}`, id))
	}
	newData, err := json.Marshal(updates)
	if err != nil {
		newData = []byte(`{}`)
	}

	eventType := determineEventType(oldIssue, updates)

	_, err = t.conn.ExecContext(ctx, `
		INSERT INTO events (issue_id, event_type, actor, old_value, new_value)
		VALUES (?, ?, ?, ?, ?)
	`, id, eventType, actor, string(oldData), string(newData))
	if err != nil {
		return fmt.Errorf("failed to record event: %w", err)
	}

	// Mark issue as dirty
	if err := markDirty(ctx, t.conn, id); err != nil {
		return fmt.Errorf("failed to mark issue dirty: %w", err)
	}

	// Invalidate blocked issues cache if status changed
	// Status changes affect which issues are blocked (blockers must be open/in_progress/blocked)
	if _, statusChanged := updates["status"]; statusChanged {
		if err := t.parent.invalidateBlockedCache(ctx, t.conn); err != nil {
			return fmt.Errorf("failed to invalidate blocked cache: %w", err)
		}
	}

	return nil
}

// applyUpdatesToIssue applies update map to issue for content hash recomputation
func applyUpdatesToIssue(issue *types.Issue, updates map[string]interface{}) {
	for key, value := range updates {
		switch key {
		case "title":
			if s, ok := value.(string); ok {
				issue.Title = s
			}
		case "description":
			if s, ok := value.(string); ok {
				issue.Description = s
			}
		case "design":
			if s, ok := value.(string); ok {
				issue.Design = s
			}
		case "acceptance_criteria":
			if s, ok := value.(string); ok {
				issue.AcceptanceCriteria = s
			}
		case "notes":
			if s, ok := value.(string); ok {
				issue.Notes = s
			}
		case "status":
			if s, ok := value.(types.Status); ok {
				issue.Status = s
			} else if s, ok := value.(string); ok {
				issue.Status = types.Status(s)
			}
		case "priority":
			if p, ok := value.(int); ok {
				issue.Priority = p
			}
		case "issue_type":
			if t, ok := value.(types.IssueType); ok {
				issue.IssueType = t
			} else if s, ok := value.(string); ok {
				issue.IssueType = types.IssueType(s)
			}
		case "assignee":
			if value == nil {
				issue.Assignee = ""
			} else if s, ok := value.(string); ok {
				issue.Assignee = s
			}
		case "external_ref":
			if value == nil {
				issue.ExternalRef = nil
			} else {
				switch v := value.(type) {
				case string:
					issue.ExternalRef = &v
				case *string:
					issue.ExternalRef = v
				}
			}
		}
	}
}

// CloseIssue closes an issue within the transaction.
// NOTE: close_reason is stored in both issues table and events table - see SQLiteStorage.CloseIssue.
// The session parameter tracks which Claude Code session closed the issue (can be empty).
func (t *sqliteTxStorage) CloseIssue(ctx context.Context, id string, reason string, actor string, session string) error {
	now := time.Now()

	result, err := t.conn.ExecContext(ctx, `
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

	_, err = t.conn.ExecContext(ctx, `
		INSERT INTO events (issue_id, event_type, actor, comment)
		VALUES (?, ?, ?, ?)
	`, id, types.EventClosed, actor, reason)
	if err != nil {
		return fmt.Errorf("failed to record event: %w", err)
	}

	// Mark issue as dirty
	if err := markDirty(ctx, t.conn, id); err != nil {
		return fmt.Errorf("failed to mark issue dirty: %w", err)
	}

	// Invalidate blocked issues cache since status changed to closed
	// Closed issues don't block others, so this affects blocking calculations
	if err := t.parent.invalidateBlockedCache(ctx, t.conn); err != nil {
		return fmt.Errorf("failed to invalidate blocked cache: %w", err)
	}

	// Reactive convoy completion: check if any convoys tracking this issue should auto-close
	// Find convoys that track this issue (convoy.issue_id tracks closed_issue.depends_on_id)
	// Uses gt:convoy label instead of issue_type for Gas Town separation
	convoyRows, err := t.conn.QueryContext(ctx, `
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
		err := t.conn.QueryRowContext(ctx, `
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
			_, err := t.conn.ExecContext(ctx, `
				UPDATE issues SET status = ?, closed_at = ?, updated_at = ?, close_reason = ?
				WHERE id = ?
			`, types.StatusClosed, now, now, closeReason, convoyID)
			if err != nil {
				return fmt.Errorf("failed to auto-close convoy %s: %w", convoyID, err)
			}

			// Record the close event
			_, err = t.conn.ExecContext(ctx, `
				INSERT INTO events (issue_id, event_type, actor, comment)
				VALUES (?, ?, ?, ?)
			`, convoyID, types.EventClosed, "system:convoy-completion", closeReason)
			if err != nil {
				return fmt.Errorf("failed to record convoy close event: %w", err)
			}

			// Mark convoy as dirty
			if err := markDirty(ctx, t.conn, convoyID); err != nil {
				return fmt.Errorf("failed to mark convoy dirty: %w", err)
			}
		}
	}

	return nil
}

// DeleteIssue deletes an issue within the transaction.
func (t *sqliteTxStorage) DeleteIssue(ctx context.Context, id string) error {
	// Delete dependencies (both directions)
	_, err := t.conn.ExecContext(ctx, `DELETE FROM dependencies WHERE issue_id = ? OR depends_on_id = ?`, id, id)
	if err != nil {
		return fmt.Errorf("failed to delete dependencies: %w", err)
	}

	// Delete events
	_, err = t.conn.ExecContext(ctx, `DELETE FROM events WHERE issue_id = ?`, id)
	if err != nil {
		return fmt.Errorf("failed to delete events: %w", err)
	}

	// Delete from dirty_issues
	_, err = t.conn.ExecContext(ctx, `DELETE FROM dirty_issues WHERE issue_id = ?`, id)
	if err != nil {
		return fmt.Errorf("failed to delete dirty marker: %w", err)
	}

	// Delete the issue itself
	result, err := t.conn.ExecContext(ctx, `DELETE FROM issues WHERE id = ?`, id)
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

	return nil
}

// AddDependency adds a dependency between issues within the transaction.
func (t *sqliteTxStorage) AddDependency(ctx context.Context, dep *types.Dependency, actor string) error {
	// Validate dependency type
	if !dep.Type.IsValid() {
		return fmt.Errorf("invalid dependency type: %q (must be non-empty string, max 50 chars)", dep.Type)
	}

	// Validate that source issue exists
	issueExists, err := t.GetIssue(ctx, dep.IssueID)
	if err != nil {
		return fmt.Errorf("failed to check issue %s: %w", dep.IssueID, err)
	}
	if issueExists == nil {
		return fmt.Errorf("issue %s not found", dep.IssueID)
	}

	// External refs (external:<project>:<capability>) don't need target validation
	// They are resolved lazily at query time by checkExternalDep
	isExternalRef := strings.HasPrefix(dep.DependsOnID, "external:")

	var dependsOnExists *types.Issue
	if !isExternalRef {
		dependsOnExists, err = t.GetIssue(ctx, dep.DependsOnID)
		if err != nil {
			return fmt.Errorf("failed to check dependency %s: %w", dep.DependsOnID, err)
		}
		if dependsOnExists == nil {
			return fmt.Errorf("dependency target %s not found", dep.DependsOnID)
		}

		// Prevent self-dependency (only for local deps)
		if dep.IssueID == dep.DependsOnID {
			return fmt.Errorf("issue cannot depend on itself")
		}

		// Parent-child direction validation note:
		// The previous type-based check (Epic can't depend on non-Epic) was removed because
		// it incorrectly rejected valid hierarchies involving custom types (e.g., theme → epic).
		// Custom types like "theme" or "shot" are valid parents for built-in types like "epic"
		// or "task". Cycle detection in this function and higher-level relationship constraints
		// already prevent the actual problematic cases (circular dependencies and duplicate
		// hierarchical links).
	}

	if dep.CreatedAt.IsZero() {
		dep.CreatedAt = time.Now()
	}
	if dep.CreatedBy == "" {
		dep.CreatedBy = actor
	}

	// Cycle detection - skip for relates-to (inherently bidirectional)
	// See dependencies.go for full rationale on cycle prevention
	if dep.Type != types.DepRelatesTo {
		var cycleExists bool
		err = t.conn.QueryRowContext(ctx, `
		WITH RECURSIVE paths AS (
			SELECT
				issue_id,
				depends_on_id,
				1 as depth
			FROM dependencies
			WHERE issue_id = ?

			UNION ALL

			SELECT
				d.issue_id,
				d.depends_on_id,
				p.depth + 1
			FROM dependencies d
			JOIN paths p ON d.issue_id = p.depends_on_id
			WHERE p.depth < 100
		)
		SELECT EXISTS(
			SELECT 1 FROM paths
			WHERE depends_on_id = ?
		)
	`, dep.DependsOnID, dep.IssueID).Scan(&cycleExists)

		if err != nil {
			return fmt.Errorf("failed to check for cycles: %w", err)
		}

		if cycleExists {
			return fmt.Errorf("cannot add dependency: would create a cycle (%s → %s → ... → %s)",
				dep.IssueID, dep.DependsOnID, dep.IssueID)
		}
	}

	// Insert dependency (including metadata and thread_id for edge consolidation - Decision 004)
	_, err = t.conn.ExecContext(ctx, `
		INSERT INTO dependencies (issue_id, depends_on_id, type, created_at, created_by, metadata, thread_id)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, dep.IssueID, dep.DependsOnID, dep.Type, dep.CreatedAt, dep.CreatedBy, dep.Metadata, dep.ThreadID)
	if err != nil {
		return fmt.Errorf("failed to add dependency: %w", err)
	}

	// Record event
	_, err = t.conn.ExecContext(ctx, `
		INSERT INTO events (issue_id, event_type, actor, comment)
		VALUES (?, ?, ?, ?)
	`, dep.IssueID, types.EventDependencyAdded, actor,
		fmt.Sprintf("Added dependency: %s %s %s", dep.IssueID, dep.Type, dep.DependsOnID))
	if err != nil {
		return fmt.Errorf("failed to record event: %w", err)
	}

	// Mark issues as dirty - for external refs, only mark the source issue
	if err := markDirty(ctx, t.conn, dep.IssueID); err != nil {
		return fmt.Errorf("failed to mark issue dirty: %w", err)
	}
	if !isExternalRef {
		if err := markDirty(ctx, t.conn, dep.DependsOnID); err != nil {
			return fmt.Errorf("failed to mark depends-on issue dirty: %w", err)
		}
	}

	// Invalidate blocked cache for blocking dependencies
	if dep.Type.AffectsReadyWork() {
		if err := t.parent.invalidateBlockedCache(ctx, t.conn); err != nil {
			return fmt.Errorf("failed to invalidate blocked cache: %w", err)
		}
	}

	return nil
}

// GetDependencyRecords retrieves dependency records for an issue within the transaction.
func (t *sqliteTxStorage) GetDependencyRecords(ctx context.Context, issueID string) ([]*types.Dependency, error) {
	rows, err := t.conn.QueryContext(ctx, `
		SELECT issue_id, depends_on_id, type, created_at, created_by, metadata, thread_id
		FROM dependencies
		WHERE issue_id = ?
	`, issueID)
	if err != nil {
		return nil, fmt.Errorf("failed to query dependencies: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var deps []*types.Dependency
	for rows.Next() {
		var d types.Dependency
		var metadata sql.NullString
		var threadID sql.NullString
		if err := rows.Scan(&d.IssueID, &d.DependsOnID, &d.Type, &d.CreatedAt, &d.CreatedBy, &metadata, &threadID); err != nil {
			return nil, fmt.Errorf("failed to scan dependency: %w", err)
		}
		if metadata.Valid {
			d.Metadata = metadata.String
		}
		if threadID.Valid {
			d.ThreadID = threadID.String
		}
		deps = append(deps, &d)
	}
	return deps, rows.Err()
}

// RemoveDependency removes a dependency within the transaction.
func (t *sqliteTxStorage) RemoveDependency(ctx context.Context, issueID, dependsOnID string, actor string) error {
	// First, check what type of dependency is being removed
	var depType types.DependencyType
	err := t.conn.QueryRowContext(ctx, `
		SELECT type FROM dependencies WHERE issue_id = ? AND depends_on_id = ?
	`, issueID, dependsOnID).Scan(&depType)

	// Store whether cache needs invalidation before deletion
	needsCacheInvalidation := false
	if err == nil {
		needsCacheInvalidation = depType.AffectsReadyWork()
	}

	result, err := t.conn.ExecContext(ctx, `
		DELETE FROM dependencies WHERE issue_id = ? AND depends_on_id = ?
	`, issueID, dependsOnID)
	if err != nil {
		return fmt.Errorf("failed to remove dependency: %w", err)
	}

	// Check if dependency existed
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to check rows affected: %w", err)
	}
	if rowsAffected == 0 {
		return fmt.Errorf("dependency from %s to %s does not exist", issueID, dependsOnID)
	}

	_, err = t.conn.ExecContext(ctx, `
		INSERT INTO events (issue_id, event_type, actor, comment)
		VALUES (?, ?, ?, ?)
	`, issueID, types.EventDependencyRemoved, actor,
		fmt.Sprintf("Removed dependency on %s", dependsOnID))
	if err != nil {
		return fmt.Errorf("failed to record event: %w", err)
	}

	// Mark both issues as dirty
	if err := markDirty(ctx, t.conn, issueID); err != nil {
		return fmt.Errorf("failed to mark issue dirty: %w", err)
	}
	if err := markDirty(ctx, t.conn, dependsOnID); err != nil {
		return fmt.Errorf("failed to mark depends-on issue dirty: %w", err)
	}

	// Invalidate blocked cache if this was a blocking dependency
	if needsCacheInvalidation {
		if err := t.parent.invalidateBlockedCache(ctx, t.conn); err != nil {
			return fmt.Errorf("failed to invalidate blocked cache: %w", err)
		}
	}

	return nil
}

// AddLabel adds a label to an issue within the transaction.
func (t *sqliteTxStorage) AddLabel(ctx context.Context, issueID, label, actor string) error {
	result, err := t.conn.ExecContext(ctx, `
		INSERT OR IGNORE INTO labels (issue_id, label) VALUES (?, ?)
	`, issueID, label)
	if err != nil {
		return fmt.Errorf("failed to add label: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to check rows affected: %w", err)
	}
	if rows == 0 {
		// Label already existed, no change made
		return nil
	}

	// Record event
	_, err = t.conn.ExecContext(ctx, `
		INSERT INTO events (issue_id, event_type, actor, comment)
		VALUES (?, ?, ?, ?)
	`, issueID, types.EventLabelAdded, actor, fmt.Sprintf("Added label: %s", label))
	if err != nil {
		return fmt.Errorf("failed to record event: %w", err)
	}

	// Mark issue as dirty
	if err := markDirty(ctx, t.conn, issueID); err != nil {
		return fmt.Errorf("failed to mark issue dirty: %w", err)
	}

	return nil
}

// GetLabels retrieves labels for an issue within the transaction.
func (t *sqliteTxStorage) GetLabels(ctx context.Context, issueID string) ([]string, error) {
	return t.getLabels(ctx, issueID)
}

// RemoveLabel removes a label from an issue within the transaction.
func (t *sqliteTxStorage) RemoveLabel(ctx context.Context, issueID, label, actor string) error {
	result, err := t.conn.ExecContext(ctx, `
		DELETE FROM labels WHERE issue_id = ? AND label = ?
	`, issueID, label)
	if err != nil {
		return fmt.Errorf("failed to remove label: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to check rows affected: %w", err)
	}
	if rows == 0 {
		// Label didn't exist, no change made
		return nil
	}

	// Record event
	_, err = t.conn.ExecContext(ctx, `
		INSERT INTO events (issue_id, event_type, actor, comment)
		VALUES (?, ?, ?, ?)
	`, issueID, types.EventLabelRemoved, actor, fmt.Sprintf("Removed label: %s", label))
	if err != nil {
		return fmt.Errorf("failed to record event: %w", err)
	}

	// Mark issue as dirty
	if err := markDirty(ctx, t.conn, issueID); err != nil {
		return fmt.Errorf("failed to mark issue dirty: %w", err)
	}

	return nil
}

// SetConfig sets a configuration value within the transaction.
func (t *sqliteTxStorage) SetConfig(ctx context.Context, key, value string) error {
	_, err := t.conn.ExecContext(ctx, `
		INSERT INTO config (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value
	`, key, value)
	if err != nil {
		return fmt.Errorf("failed to set config: %w", err)
	}
	return nil
}

// GetConfig gets a configuration value within the transaction.
// This enables read-your-writes semantics for config values.
func (t *sqliteTxStorage) GetConfig(ctx context.Context, key string) (string, error) {
	var value string
	err := t.conn.QueryRowContext(ctx, `SELECT value FROM config WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("failed to get config: %w", err)
	}
	return value, nil
}

// GetCustomStatuses retrieves the list of custom status states from config within the transaction.
func (t *sqliteTxStorage) GetCustomStatuses(ctx context.Context) ([]string, error) {
	value, err := t.GetConfig(ctx, customStatusConfigKey)
	if err != nil {
		return nil, err
	}
	if value == "" {
		return nil, nil
	}
	return parseCommaSeparatedList(value), nil
}

// GetCustomTypes retrieves the list of custom issue types from config within the transaction.
func (t *sqliteTxStorage) GetCustomTypes(ctx context.Context) ([]string, error) {
	value, err := t.GetConfig(ctx, customTypeConfigKey)
	if err != nil {
		return nil, err
	}
	if value == "" {
		return nil, nil
	}
	return parseCommaSeparatedList(value), nil
}

// SetMetadata sets a metadata value within the transaction.
func (t *sqliteTxStorage) SetMetadata(ctx context.Context, key, value string) error {
	_, err := t.conn.ExecContext(ctx, `
		INSERT INTO metadata (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value
	`, key, value)
	if err != nil {
		return fmt.Errorf("failed to set metadata: %w", err)
	}
	return nil
}

// GetMetadata gets a metadata value within the transaction.
// This enables read-your-writes semantics for metadata values.
func (t *sqliteTxStorage) GetMetadata(ctx context.Context, key string) (string, error) {
	var value string
	err := t.conn.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("failed to get metadata: %w", err)
	}
	return value, nil
}

// AddComment adds a comment to an issue within the transaction.
func (t *sqliteTxStorage) AddComment(ctx context.Context, issueID, actor, comment string) error {
	// Update issue updated_at timestamp first to verify issue exists
	now := time.Now()
	res, err := t.conn.ExecContext(ctx, `
		UPDATE issues SET updated_at = ? WHERE id = ?
	`, now, issueID)
	if err != nil {
		return fmt.Errorf("failed to update timestamp: %w", err)
	}

	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("issue %s not found", issueID)
	}

	// Insert comment event
	_, err = t.conn.ExecContext(ctx, `
		INSERT INTO events (issue_id, event_type, actor, comment)
		VALUES (?, ?, ?, ?)
	`, issueID, types.EventCommented, actor, comment)
	if err != nil {
		return fmt.Errorf("failed to add comment: %w", err)
	}

	// Mark issue as dirty for incremental export
	if err := markDirty(ctx, t.conn, issueID); err != nil {
		return fmt.Errorf("failed to mark issue dirty: %w", err)
	}

	return nil
}

// ImportIssueComment adds a structured comment during import, preserving the original timestamp.
func (t *sqliteTxStorage) ImportIssueComment(ctx context.Context, issueID, author, text string, createdAt time.Time) (*types.Comment, error) {
	// Verify issue exists
	existing, err := t.GetIssue(ctx, issueID)
	if err != nil {
		return nil, fmt.Errorf("failed to check issue existence: %w", err)
	}
	if existing == nil {
		return nil, fmt.Errorf("issue %s not found", issueID)
	}

	createdAtStr := createdAt.UTC().Format(time.RFC3339Nano)
	res, err := t.conn.ExecContext(ctx, `
		INSERT INTO comments (issue_id, author, text, created_at)
		VALUES (?, ?, ?, ?)
	`, issueID, author, text, createdAtStr)
	if err != nil {
		return nil, fmt.Errorf("failed to insert comment: %w", err)
	}
	commentID, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("failed to get comment ID: %w", err)
	}

	// Mark issue dirty
	if err := markDirty(ctx, t.conn, issueID); err != nil {
		return nil, fmt.Errorf("failed to mark issue dirty: %w", err)
	}

	return &types.Comment{
		ID:        commentID,
		IssueID:   issueID,
		Author:    author,
		Text:      text,
		CreatedAt: createdAt.UTC(),
	}, nil
}

// GetIssueComments retrieves structured comments for an issue within the transaction.
func (t *sqliteTxStorage) GetIssueComments(ctx context.Context, issueID string) ([]*types.Comment, error) {
	rows, err := t.conn.QueryContext(ctx, `
		SELECT id, issue_id, author, text, created_at
		FROM comments
		WHERE issue_id = ?
		ORDER BY created_at ASC
	`, issueID)
	if err != nil {
		return nil, fmt.Errorf("failed to query comments: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var comments []*types.Comment
	for rows.Next() {
		var c types.Comment
		if err := rows.Scan(&c.ID, &c.IssueID, &c.Author, &c.Text, &c.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan comment: %w", err)
		}
		comments = append(comments, &c)
	}
	return comments, rows.Err()
}

// SearchIssues finds issues matching query and filters within the transaction.
// This enables read-your-writes semantics for searching within a transaction.
func (t *sqliteTxStorage) SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error) {
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

	// Type exclusion (for hiding internal types like gates, bd-7zka.2)
	if len(filter.ExcludeTypes) > 0 {
		placeholders := make([]string, len(filter.ExcludeTypes))
		for i, t := range filter.ExcludeTypes {
			placeholders[i] = "?"
			args = append(args, string(t))
		}
		whereClauses = append(whereClauses, fmt.Sprintf("issue_type NOT IN (%s)", strings.Join(placeholders, ",")))
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

	// Label pattern filtering (glob): issue must have at least one label matching the pattern
	if filter.LabelPattern != "" {
		whereClauses = append(whereClauses, "id IN (SELECT issue_id FROM labels WHERE label GLOB ?)")
		args = append(args, filter.LabelPattern)
	}

	// Label regex filtering: done at application level after query (see filterByLabelRegex)
	// SQLite doesn't have built-in regex support without extensions

	// ID filtering: match specific issue IDs
	if len(filter.IDs) > 0 {
		placeholders := make([]string, len(filter.IDs))
		for i, id := range filter.IDs {
			placeholders[i] = "?"
			args = append(args, id)
		}
		whereClauses = append(whereClauses, fmt.Sprintf("id IN (%s)", strings.Join(placeholders, ", ")))
	}

	// ID prefix filtering (for shell completion)
	if filter.IDPrefix != "" {
		whereClauses = append(whereClauses, "id LIKE ?")
		args = append(args, filter.IDPrefix+"%")
	}
	if filter.SpecIDPrefix != "" {
		whereClauses = append(whereClauses, "spec_id LIKE ?")
		args = append(args, filter.SpecIDPrefix+"%")
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

	// Parent filtering: filter children by parent issue
	// Also includes dotted-ID children (e.g., "parent.1.2" is child of "parent")
	if filter.ParentID != nil {
		parentID := *filter.ParentID
		whereClauses = append(whereClauses, "(id IN (SELECT issue_id FROM dependencies WHERE type = 'parent-child' AND depends_on_id = ?) OR id LIKE ? || '.%')")
		args = append(args, parentID, parentID)
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
		       created_at, created_by, owner, updated_at, closed_at, external_ref,
		       spec_id, compaction_level, compacted_at, compacted_at_commit, original_size, source_repo, close_reason,
		       deleted_at, deleted_by, delete_reason, original_type,
		       sender, ephemeral, pinned, is_template, crystallizes,
		       await_type, await_id, timeout_ns, waiters,
		       hook_bead, role_bead, agent_state, last_activity, role_type, rig, mol_type,
		       due_at, defer_until, metadata
		FROM issues
		%s
		ORDER BY priority ASC, created_at DESC
		%s
	`, whereSQL, limitSQL)

	rows, err := t.conn.QueryContext(ctx, querySQL, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to search issues: %w", err)
	}
	defer func() { _ = rows.Close() }()

	issues, err := t.scanIssues(ctx, rows)
	if err != nil {
		return nil, err
	}

	// Apply label regex filtering at application level
	// SQLite doesn't have built-in regex support without extensions
	if filter.LabelRegex != "" {
		issues, err = t.filterByLabelRegex(ctx, issues, filter.LabelRegex)
		if err != nil {
			return nil, fmt.Errorf("failed to filter by label regex: %w", err)
		}
	}

	return issues, nil
}

// scanner is an interface that both *sql.Row and *sql.Rows satisfy
type scanner interface {
	Scan(dest ...interface{}) error
}

// scanIssueRow scans a single issue row from the database.
// This is a shared helper used by both GetIssue and SearchIssues to ensure
// consistent scanning of issue rows.
func scanIssueRow(row scanner) (*types.Issue, error) {
	var issue types.Issue
	var createdAtStr sql.NullString // TEXT column - must parse manually for cross-driver compatibility
	var updatedAtStr sql.NullString // TEXT column - must parse manually for cross-driver compatibility
	var contentHash sql.NullString
	var closedAt sql.NullTime
	var estimatedMinutes sql.NullInt64
	var assignee sql.NullString
	var owner sql.NullString
	var externalRef sql.NullString
	var specID sql.NullString
	var compactedAt sql.NullTime
	var originalSize sql.NullInt64
	var sourceRepo sql.NullString
	var compactedAtCommit sql.NullString
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
	var molType sql.NullString
	// Time-based scheduling fields
	var dueAt sql.NullTime
	var deferUntil sql.NullTime
	// Custom metadata field (GH#1406)
	var metadata sql.NullString

	err := row.Scan(
		&issue.ID, &contentHash, &issue.Title, &issue.Description, &issue.Design,
		&issue.AcceptanceCriteria, &issue.Notes, &issue.Status,
		&issue.Priority, &issue.IssueType, &assignee, &estimatedMinutes,
		&createdAtStr, &issue.CreatedBy, &owner, &updatedAtStr, &closedAt, &externalRef,
		&specID, &issue.CompactionLevel, &compactedAt, &compactedAtCommit, &originalSize, &sourceRepo, &closeReason,
		&deletedAt, &deletedBy, &deleteReason, &originalType,
		&sender, &wisp, &pinned, &isTemplate, &crystallizes,
		&awaitType, &awaitID, &timeoutNs, &waiters,
		&hookBead, &roleBead, &agentState, &lastActivity, &roleType, &rig, &molType,
		&dueAt, &deferUntil, &metadata,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to scan issue: %w", err)
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
	if molType.Valid {
		issue.MolType = types.MolType(molType.String)
	}
	// Time-based scheduling fields
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

	return &issue, nil
}

// scanIssues scans issue rows and fetches labels using the transaction connection.
func (t *sqliteTxStorage) scanIssues(ctx context.Context, rows *sql.Rows) ([]*types.Issue, error) {
	var issues []*types.Issue
	var issueIDs []string

	// First pass: scan all issues
	for rows.Next() {
		issue, err := scanIssueRow(rows)
		if err != nil {
			return nil, err
		}
		issues = append(issues, issue)
		issueIDs = append(issueIDs, issue.ID)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating rows: %w", err)
	}

	// Second pass: batch-load labels for all issues using transaction connection
	labelsMap, err := t.getLabelsForIssues(ctx, issueIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to get labels: %w", err)
	}

	// Attach labels to issues
	for _, issue := range issues {
		issue.Labels = labelsMap[issue.ID]
	}

	return issues, nil
}

// getLabelsForIssues retrieves labels for multiple issues using the transaction connection.
func (t *sqliteTxStorage) getLabelsForIssues(ctx context.Context, issueIDs []string) (map[string][]string, error) {
	result := make(map[string][]string)
	if len(issueIDs) == 0 {
		return result, nil
	}

	// Build placeholders for IN clause
	placeholders := make([]string, len(issueIDs))
	args := make([]interface{}, len(issueIDs))
	for i, id := range issueIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	// nolint:gosec // G201: placeholders is only "?" characters, not user input
	query := fmt.Sprintf(`
		SELECT issue_id, label FROM labels
		WHERE issue_id IN (%s)
		ORDER BY issue_id, label
	`, strings.Join(placeholders, ", "))

	rows, err := t.conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get labels: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var issueID, label string
		if err := rows.Scan(&issueID, &label); err != nil {
			return nil, err
		}
		result[issueID] = append(result[issueID], label)
	}

	return result, rows.Err()
}

// filterByLabelRegex filters issues to only include those with at least one label
// matching the given regex pattern. This is done at the application level because
// SQLite doesn't have built-in regex support without extensions.
func (t *sqliteTxStorage) filterByLabelRegex(_ context.Context, issues []*types.Issue, pattern string) ([]*types.Issue, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid regex pattern %q: %w", pattern, err)
	}

	// Filter issues that have at least one label matching the regex
	// Note: Labels were already loaded by scanIssues
	var filtered []*types.Issue
	for _, issue := range issues {
		for _, label := range issue.Labels {
			if re.MatchString(label) {
				filtered = append(filtered, issue)
				break // Only need one match
			}
		}
	}

	return filtered, nil
}
