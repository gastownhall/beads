// Package sqlite implements the storage interface using SQLite.
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
//  1. Acquire dedicated connection from pool
//  2. Begin IMMEDIATE transaction with retry on SQLITE_BUSY
//  3. Execute user function with Transaction interface
//  4. On success: COMMIT
//  5. On error or panic: ROLLBACK
//
// Panic safety: If the callback panics, the transaction is rolled back
// and the panic is re-raised to the caller.
func (s *SQLiteStorage) RunInTransaction(ctx context.Context, fn func(tx storage.Transaction) error) error {
	// Acquire a dedicated connection for the transaction.
	// This ensures all operations in the transaction use the same connection.
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("failed to acquire connection for transaction: %w", err)
	}
	defer func() { _ = conn.Close() }()

	// Start IMMEDIATE transaction to acquire write lock early.
	// Use retry logic with exponential backoff to handle SQLITE_BUSY (bd-ola6)
	if err := beginImmediateWithRetry(ctx, conn, 5, 10*time.Millisecond); err != nil {
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

// CreateIssue creates a new issue within the transaction.
func (t *sqliteTxStorage) CreateIssue(ctx context.Context, issue *types.Issue, actor string) error {
	// Fetch custom statuses for validation (bd-1pj6)
	customStatuses, err := t.GetCustomStatuses(ctx)
	if err != nil {
		return fmt.Errorf("failed to get custom statuses: %w", err)
	}

	// Validate issue before creating (with custom status support)
	if err := issue.ValidateWithCustomStatuses(customStatuses); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	// Set timestamps
	now := time.Now()
	issue.CreatedAt = now
	issue.UpdatedAt = now

	// Compute content hash (bd-95)
	if issue.ContentHash == "" {
		issue.ContentHash = issue.ComputeContentHash()
	}

	// Get prefix from config (needed for both ID generation and validation)
	var prefix string
	err = t.conn.QueryRowContext(ctx, `SELECT value FROM config WHERE key = ?`, "issue_prefix").Scan(&prefix)
	if err == sql.ErrNoRows || prefix == "" {
		// CRITICAL: Reject operation if issue_prefix config is missing (bd-166)
		return fmt.Errorf("database not initialized: issue_prefix config is missing (run 'bd init --prefix <prefix>' first)")
	} else if err != nil {
		return fmt.Errorf("failed to get config: %w", err)
	}

	// Generate or validate ID
	if issue.ID == "" {
		// Generate hash-based ID with adaptive length based on database size (bd-ea2a13)
		generatedID, err := GenerateIssueID(ctx, t.conn, prefix, issue, actor)
		if err != nil {
			return fmt.Errorf("failed to generate issue ID: %w", err)
		}
		issue.ID = generatedID
	} else {
		// Validate that explicitly provided ID matches the configured prefix (bd-177)
		if err := ValidateIssueIDPrefix(issue.ID, prefix); err != nil {
			return fmt.Errorf("failed to validate issue ID prefix: %w", err)
		}

		// For hierarchical IDs (bd-a3f8e9.1), ensure parent exists
		if strings.Contains(issue.ID, ".") {
			// Try to resurrect entire parent chain if any parents are missing
			resurrected, err := t.parent.tryResurrectParentChainWithConn(ctx, t.conn, issue.ID)
			if err != nil {
				return fmt.Errorf("failed to resurrect parent chain for %s: %w", issue.ID, err)
			}
			if !resurrected {
				// Parent(s) not found in JSONL history - cannot proceed
				lastDot := strings.LastIndex(issue.ID, ".")
				parentID := issue.ID[:lastDot]
				return fmt.Errorf("parent issue %s does not exist and could not be resurrected from JSONL history", parentID)
			}
		}
	}

	// Insert issue
	if err := insertIssue(ctx, t.conn, issue); err != nil {
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

	// Fetch custom statuses for validation (bd-1pj6)
	customStatuses, err := t.GetCustomStatuses(ctx)
	if err != nil {
		return fmt.Errorf("failed to get custom statuses: %w", err)
	}

	// Validate and prepare all issues first (with custom status support)
	now := time.Now()
	for _, issue := range issues {
		if err := issue.ValidateWithCustomStatuses(customStatuses); err != nil {
			return fmt.Errorf("validation failed for issue: %w", err)
		}
		issue.CreatedAt = now
		issue.UpdatedAt = now
		if issue.ContentHash == "" {
			issue.ContentHash = issue.ComputeContentHash()
		}
	}

	// Get prefix from config
	var prefix string
	err = t.conn.QueryRowContext(ctx, `SELECT value FROM config WHERE key = ?`, "issue_prefix").Scan(&prefix)
	if err == sql.ErrNoRows || prefix == "" {
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
			if err := ValidateIssueIDPrefix(issue.ID, prefix); err != nil {
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

	// Insert all issues
	if err := insertIssues(ctx, t.conn, issues); err != nil {
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
		       created_at, updated_at, closed_at, external_ref,
		       compaction_level, compacted_at, compacted_at_commit, original_size, source_repo, close_reason,
		       deleted_at, deleted_by, delete_reason, original_type
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

	// Fetch custom statuses for validation (bd-1pj6)
	customStatuses, err := t.GetCustomStatuses(ctx)
	if err != nil {
		return fmt.Errorf("failed to get custom statuses: %w", err)
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
			return fmt.Errorf("failed to validate field update: %w", err)
		}

		setClauses = append(setClauses, fmt.Sprintf("%s = ?", key))
		args = append(args, value)
	}

	// Auto-manage closed_at when status changes
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

	// Invalidate blocked issues cache if status changed (bd-1c4h)
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
func (t *sqliteTxStorage) CloseIssue(ctx context.Context, id string, reason string, actor string) error {
	now := time.Now()

	result, err := t.conn.ExecContext(ctx, `
		UPDATE issues SET status = ?, closed_at = ?, updated_at = ?
		WHERE id = ?
	`, types.StatusClosed, now, now, id)
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

	// Invalidate blocked issues cache since status changed to closed (bd-1c4h)
	// Closed issues don't block others, so this affects blocking calculations
	if err := t.parent.invalidateBlockedCache(ctx, t.conn); err != nil {
		return fmt.Errorf("failed to invalidate blocked cache: %w", err)
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
		return fmt.Errorf("invalid dependency type: %s (must be blocks, related, parent-child, or discovered-from)", dep.Type)
	}

	// Validate that both issues exist
	issueExists, err := t.GetIssue(ctx, dep.IssueID)
	if err != nil {
		return fmt.Errorf("failed to check issue %s: %w", dep.IssueID, err)
	}
	if issueExists == nil {
		return fmt.Errorf("issue %s not found", dep.IssueID)
	}

	dependsOnExists, err := t.GetIssue(ctx, dep.DependsOnID)
	if err != nil {
		return fmt.Errorf("failed to check dependency %s: %w", dep.DependsOnID, err)
	}
	if dependsOnExists == nil {
		return fmt.Errorf("dependency target %s not found", dep.DependsOnID)
	}

	// Prevent self-dependency
	if dep.IssueID == dep.DependsOnID {
		return fmt.Errorf("issue cannot depend on itself")
	}

	// Validate parent-child dependency direction
	if dep.Type == types.DepParentChild {
		if issueExists.IssueType == types.TypeEpic && dependsOnExists.IssueType != types.TypeEpic {
			return fmt.Errorf("invalid parent-child dependency: parent (%s) cannot depend on child (%s). Use: bd dep add %s %s --type parent-child",
				dep.IssueID, dep.DependsOnID, dep.DependsOnID, dep.IssueID)
		}
	}

	if dep.CreatedAt.IsZero() {
		dep.CreatedAt = time.Now()
	}
	if dep.CreatedBy == "" {
		dep.CreatedBy = actor
	}

	// Cycle detection
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

	// Insert dependency
	_, err = t.conn.ExecContext(ctx, `
		INSERT INTO dependencies (issue_id, depends_on_id, type, created_at, created_by)
		VALUES (?, ?, ?, ?, ?)
	`, dep.IssueID, dep.DependsOnID, dep.Type, dep.CreatedAt, dep.CreatedBy)
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

	// Mark both issues as dirty
	if err := markDirty(ctx, t.conn, dep.IssueID); err != nil {
		return fmt.Errorf("failed to mark issue dirty: %w", err)
	}
	if err := markDirty(ctx, t.conn, dep.DependsOnID); err != nil {
		return fmt.Errorf("failed to mark depends-on issue dirty: %w", err)
	}

	// Invalidate blocked cache for blocking dependencies (bd-1c4h)
	if dep.Type == types.DepBlocks || dep.Type == types.DepParentChild {
		if err := t.parent.invalidateBlockedCache(ctx, t.conn); err != nil {
			return fmt.Errorf("failed to invalidate blocked cache: %w", err)
		}
	}

	return nil
}

// RemoveDependency removes a dependency within the transaction.
func (t *sqliteTxStorage) RemoveDependency(ctx context.Context, issueID, dependsOnID string, actor string) error {
	// First, check what type of dependency is being removed (bd-1c4h)
	var depType types.DependencyType
	err := t.conn.QueryRowContext(ctx, `
		SELECT type FROM dependencies WHERE issue_id = ? AND depends_on_id = ?
	`, issueID, dependsOnID).Scan(&depType)

	// Store whether cache needs invalidation before deletion
	needsCacheInvalidation := false
	if err == nil {
		needsCacheInvalidation = (depType == types.DepBlocks || depType == types.DepParentChild)
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

	// Invalidate blocked cache if this was a blocking dependency (bd-1c4h)
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
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("failed to get config: %w", err)
	}
	return value, nil
}

// GetCustomStatuses retrieves the list of custom status states from config within the transaction.
func (t *sqliteTxStorage) GetCustomStatuses(ctx context.Context) ([]string, error) {
	value, err := t.GetConfig(ctx, CustomStatusConfigKey)
	if err != nil {
		return nil, err
	}
	if value == "" {
		return nil, nil
	}
	return parseCustomStatuses(value), nil
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
	if err == sql.ErrNoRows {
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
		// Exclude tombstones by default unless explicitly filtering for them (bd-1bu)
		whereClauses = append(whereClauses, "status != ?")
		args = append(args, types.StatusTombstone)
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
		       created_at, updated_at, closed_at, external_ref,
		       compaction_level, compacted_at, compacted_at_commit, original_size, source_repo, close_reason,
		       deleted_at, deleted_by, delete_reason, original_type
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

	return t.scanIssues(ctx, rows)
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
	var contentHash sql.NullString
	var closedAt sql.NullTime
	var estimatedMinutes sql.NullInt64
	var assignee sql.NullString
	var externalRef sql.NullString
	var compactedAt sql.NullTime
	var originalSize sql.NullInt64
	var sourceRepo sql.NullString
	var compactedAtCommit sql.NullString
	var closeReason sql.NullString
	var deletedAt sql.NullTime
	var deletedBy sql.NullString
	var deleteReason sql.NullString
	var originalType sql.NullString

	err := row.Scan(
		&issue.ID, &contentHash, &issue.Title, &issue.Description, &issue.Design,
		&issue.AcceptanceCriteria, &issue.Notes, &issue.Status,
		&issue.Priority, &issue.IssueType, &assignee, &estimatedMinutes,
		&issue.CreatedAt, &issue.UpdatedAt, &closedAt, &externalRef,
		&issue.CompactionLevel, &compactedAt, &compactedAtCommit, &originalSize, &sourceRepo, &closeReason,
		&deletedAt, &deletedBy, &deleteReason, &originalType,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to scan issue: %w", err)
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
