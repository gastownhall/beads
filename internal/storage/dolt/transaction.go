package dolt

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// doltTransaction implements storage.Transaction for Dolt
type doltTransaction struct {
	tx    *sql.Tx
	store *DoltStore
}

// CreateIssueImport is the import-friendly issue creation hook.
// Dolt does not enforce prefix validation at the storage layer, so this delegates to CreateIssue.
func (t *doltTransaction) CreateIssueImport(ctx context.Context, issue *types.Issue, actor string, skipPrefixValidation bool) error {
	return t.CreateIssue(ctx, issue, actor)
}

// RunInTransaction executes a function within a database transaction.
// Wisp routing is handled within individual transaction methods based on ID/Ephemeral flag.
func (s *DoltStore) RunInTransaction(ctx context.Context, fn func(tx storage.Transaction) error) error {
	return s.runDoltTransaction(ctx, fn)
}

func (s *DoltStore) runDoltTransaction(ctx context.Context, fn func(tx storage.Transaction) error) error {
	sqlTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}

	tx := &doltTransaction{tx: sqlTx, store: s}

	defer func() {
		if r := recover(); r != nil {
			_ = sqlTx.Rollback() // Best effort rollback on error path
			panic(r)
		}
	}()

	if err := fn(tx); err != nil {
		_ = sqlTx.Rollback() // Best effort rollback on error path
		return err
	}

	return sqlTx.Commit()
}

// CreateIssue creates an issue within the transaction.
// Routes ephemeral issues to the wisps table.
func (t *doltTransaction) CreateIssue(ctx context.Context, issue *types.Issue, actor string) error {
	now := time.Now().UTC()
	if issue.CreatedAt.IsZero() {
		issue.CreatedAt = now
	}
	if issue.UpdatedAt.IsZero() {
		issue.UpdatedAt = now
	}
	if issue.ContentHash == "" {
		issue.ContentHash = issue.ComputeContentHash()
	}

	table := "issues"
	if issue.Ephemeral {
		table = "wisps"
	}

	// Generate ID if not provided
	if issue.ID == "" {
		var configPrefix string
		err := t.tx.QueryRowContext(ctx, "SELECT value FROM config WHERE `key` = ?", "issue_prefix").Scan(&configPrefix)
		if err == sql.ErrNoRows || configPrefix == "" {
			return fmt.Errorf("%w: issue_prefix config is missing", storage.ErrNotInitialized)
		} else if err != nil {
			return fmt.Errorf("failed to get config: %w", err)
		}

		var prefix string
		if issue.Ephemeral {
			prefix = wispPrefix(configPrefix, issue)
		} else {
			prefix = configPrefix
			if issue.PrefixOverride != "" {
				prefix = issue.PrefixOverride
			} else if issue.IDPrefix != "" {
				prefix = configPrefix + "-" + issue.IDPrefix
			}
		}

		generatedID, err := generateIssueIDInTable(ctx, t.tx, table, prefix, issue, actor)
		if err != nil {
			return fmt.Errorf("failed to generate issue ID: %w", err)
		}
		issue.ID = generatedID
	}

	return insertIssueTxIntoTable(ctx, t.tx, table, issue)
}

// CreateIssues creates multiple issues within the transaction
func (t *doltTransaction) CreateIssues(ctx context.Context, issues []*types.Issue, actor string) error {
	for _, issue := range issues {
		if err := t.CreateIssue(ctx, issue, actor); err != nil {
			return err
		}
	}
	return nil
}

// GetIssue retrieves an issue within the transaction.
// Checks wisps table for ephemeral IDs.
func (t *doltTransaction) GetIssue(ctx context.Context, id string) (*types.Issue, error) {
	table := "issues"
	if IsEphemeralID(id) {
		table = "wisps"
	}
	return scanIssueTxFromTable(ctx, t.tx, table, id)
}

// SearchIssues searches for issues within the transaction
func (t *doltTransaction) SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error) {
	table := "issues"
	if filter.Ephemeral != nil && *filter.Ephemeral {
		table = "wisps"
	}

	whereClauses := []string{}
	args := []interface{}{}

	if query != "" {
		whereClauses = append(whereClauses, "(title LIKE ? OR description LIKE ? OR id LIKE ?)")
		pattern := "%" + query + "%"
		args = append(args, pattern, pattern, pattern)
	}

	if filter.ParentID != nil {
		parentID := *filter.ParentID
		depTable := "dependencies"
		if table == "wisps" {
			depTable = "wisp_dependencies"
		}
		whereClauses = append(whereClauses, fmt.Sprintf("(id IN (SELECT issue_id FROM %s WHERE type = 'parent-child' AND depends_on_id = ?) OR id LIKE CONCAT(?, '.%%'))", depTable))
		args = append(args, parentID, parentID)
	}

	if filter.Status != nil {
		whereClauses = append(whereClauses, "status = ?")
		args = append(args, *filter.Status)
	}
	if filter.SpecIDPrefix != "" {
		whereClauses = append(whereClauses, "spec_id LIKE ?")
		args = append(args, filter.SpecIDPrefix+"%")
	}
	if filter.SourceRepo != nil {
		whereClauses = append(whereClauses, "source_repo = ?")
		args = append(args, *filter.SourceRepo)
	}

	whereSQL := ""
	if len(whereClauses) > 0 {
		whereSQL = "WHERE " + strings.Join(whereClauses, " AND ")
	}

	//nolint:gosec // G201: table is hardcoded, whereSQL is parameterized
	rows, err := t.tx.QueryContext(ctx, fmt.Sprintf(`
		SELECT id FROM %s %s ORDER BY priority ASC, created_at DESC
	`, table, whereSQL), args...)
	if err != nil {
		return nil, err
	}

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()

	var issues []*types.Issue
	for _, id := range ids {
		issue, err := t.GetIssue(ctx, id)
		if err != nil {
			return nil, err
		}
		issues = append(issues, issue)
	}
	return issues, nil
}

// UpdateIssue updates an issue within the transaction
func (t *doltTransaction) UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
	table := "issues"
	if IsEphemeralID(id) {
		table = "wisps"
	}

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

	args = append(args, id)
	//nolint:gosec // G201: table is hardcoded, setClauses contains only column names
	querySQL := fmt.Sprintf("UPDATE %s SET %s WHERE id = ?", table, strings.Join(setClauses, ", "))
	_, err := t.tx.ExecContext(ctx, querySQL, args...)
	return err
}

// CloseIssue closes an issue within the transaction
func (t *doltTransaction) CloseIssue(ctx context.Context, id string, reason string, actor string, session string) error {
	table := "issues"
	if IsEphemeralID(id) {
		table = "wisps"
	}

	now := time.Now().UTC()
	//nolint:gosec // G201: table is hardcoded
	_, err := t.tx.ExecContext(ctx, fmt.Sprintf(`
		UPDATE %s SET status = ?, closed_at = ?, updated_at = ?, close_reason = ?, closed_by_session = ?
		WHERE id = ?
	`, table), types.StatusClosed, now, now, reason, session, id)
	return err
}

// DeleteIssue deletes an issue within the transaction
func (t *doltTransaction) DeleteIssue(ctx context.Context, id string) error {
	table := "issues"
	if IsEphemeralID(id) {
		table = "wisps"
	}

	//nolint:gosec // G201: table is hardcoded
	_, err := t.tx.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE id = ?", table), id)
	return err
}

// AddDependency adds a dependency within the transaction
func (t *doltTransaction) AddDependency(ctx context.Context, dep *types.Dependency, actor string) error {
	table := "dependencies"
	if IsEphemeralID(dep.IssueID) {
		table = "wisp_dependencies"
	}

	//nolint:gosec // G201: table is hardcoded
	_, err := t.tx.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (issue_id, depends_on_id, type, created_at, created_by, thread_id)
		VALUES (?, ?, ?, NOW(), ?, ?)
		ON DUPLICATE KEY UPDATE type = VALUES(type)
	`, table), dep.IssueID, dep.DependsOnID, dep.Type, actor, dep.ThreadID)
	return err
}

func (t *doltTransaction) GetDependencyRecords(ctx context.Context, issueID string) ([]*types.Dependency, error) {
	table := "dependencies"
	if IsEphemeralID(issueID) {
		table = "wisp_dependencies"
	}

	//nolint:gosec // G201: table is hardcoded
	rows, err := t.tx.QueryContext(ctx, fmt.Sprintf(`
		SELECT issue_id, depends_on_id, type, created_at, created_by, metadata, thread_id
		FROM %s
		WHERE issue_id = ?
	`, table), issueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var deps []*types.Dependency
	for rows.Next() {
		var d types.Dependency
		var metadata sql.NullString
		var threadID sql.NullString
		if err := rows.Scan(&d.IssueID, &d.DependsOnID, &d.Type, &d.CreatedAt, &d.CreatedBy, &metadata, &threadID); err != nil {
			return nil, err
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

// RemoveDependency removes a dependency within the transaction
func (t *doltTransaction) RemoveDependency(ctx context.Context, issueID, dependsOnID string, actor string) error {
	table := "dependencies"
	if IsEphemeralID(issueID) {
		table = "wisp_dependencies"
	}

	//nolint:gosec // G201: table is hardcoded
	_, err := t.tx.ExecContext(ctx, fmt.Sprintf(`
		DELETE FROM %s WHERE issue_id = ? AND depends_on_id = ?
	`, table), issueID, dependsOnID)
	return err
}

// AddLabel adds a label within the transaction
func (t *doltTransaction) AddLabel(ctx context.Context, issueID, label, actor string) error {
	table := "labels"
	if IsEphemeralID(issueID) {
		table = "wisp_labels"
	}

	//nolint:gosec // G201: table is hardcoded
	_, err := t.tx.ExecContext(ctx, fmt.Sprintf(`
		INSERT IGNORE INTO %s (issue_id, label) VALUES (?, ?)
	`, table), issueID, label)
	return err
}

func (t *doltTransaction) GetLabels(ctx context.Context, issueID string) ([]string, error) {
	table := "labels"
	if IsEphemeralID(issueID) {
		table = "wisp_labels"
	}

	//nolint:gosec // G201: table is hardcoded
	rows, err := t.tx.QueryContext(ctx, fmt.Sprintf(`SELECT label FROM %s WHERE issue_id = ? ORDER BY label`, table), issueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var labels []string
	for rows.Next() {
		var l string
		if err := rows.Scan(&l); err != nil {
			return nil, err
		}
		labels = append(labels, l)
	}
	return labels, rows.Err()
}

// RemoveLabel removes a label within the transaction
func (t *doltTransaction) RemoveLabel(ctx context.Context, issueID, label, actor string) error {
	table := "labels"
	if IsEphemeralID(issueID) {
		table = "wisp_labels"
	}

	//nolint:gosec // G201: table is hardcoded
	_, err := t.tx.ExecContext(ctx, fmt.Sprintf(`
		DELETE FROM %s WHERE issue_id = ? AND label = ?
	`, table), issueID, label)
	return err
}

// SetConfig sets a config value within the transaction
func (t *doltTransaction) SetConfig(ctx context.Context, key, value string) error {
	_, err := t.tx.ExecContext(ctx, `
		INSERT INTO config (`+"`key`"+`, value) VALUES (?, ?)
		ON DUPLICATE KEY UPDATE value = VALUES(value)
	`, key, value)
	return err
}

// GetConfig gets a config value within the transaction
func (t *doltTransaction) GetConfig(ctx context.Context, key string) (string, error) {
	var value string
	err := t.tx.QueryRowContext(ctx, "SELECT value FROM config WHERE `key` = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

// SetMetadata sets a metadata value within the transaction
func (t *doltTransaction) SetMetadata(ctx context.Context, key, value string) error {
	_, err := t.tx.ExecContext(ctx, `
		INSERT INTO metadata (`+"`key`"+`, value) VALUES (?, ?)
		ON DUPLICATE KEY UPDATE value = VALUES(value)
	`, key, value)
	return err
}

// GetMetadata gets a metadata value within the transaction
func (t *doltTransaction) GetMetadata(ctx context.Context, key string) (string, error) {
	var value string
	err := t.tx.QueryRowContext(ctx, "SELECT value FROM metadata WHERE `key` = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

func (t *doltTransaction) ImportIssueComment(ctx context.Context, issueID, author, text string, createdAt time.Time) (*types.Comment, error) {
	_, err := t.GetIssue(ctx, issueID)
	if err != nil {
		return nil, err
	}

	table := "comments"
	if IsEphemeralID(issueID) {
		table = "wisp_comments"
	}

	createdAt = createdAt.UTC()
	//nolint:gosec // G201: table is hardcoded
	res, err := t.tx.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (issue_id, author, text, created_at)
		VALUES (?, ?, ?, ?)
	`, table), issueID, author, text, createdAt)
	if err != nil {
		return nil, fmt.Errorf("failed to add comment: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("failed to get comment id: %w", err)
	}

	return &types.Comment{ID: id, IssueID: issueID, Author: author, Text: text, CreatedAt: createdAt}, nil
}

func (t *doltTransaction) GetIssueComments(ctx context.Context, issueID string) ([]*types.Comment, error) {
	table := "comments"
	if IsEphemeralID(issueID) {
		table = "wisp_comments"
	}

	//nolint:gosec // G201: table is hardcoded
	rows, err := t.tx.QueryContext(ctx, fmt.Sprintf(`
		SELECT id, issue_id, author, text, created_at
		FROM %s
		WHERE issue_id = ?
		ORDER BY created_at ASC
	`, table), issueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var comments []*types.Comment
	for rows.Next() {
		var c types.Comment
		if err := rows.Scan(&c.ID, &c.IssueID, &c.Author, &c.Text, &c.CreatedAt); err != nil {
			return nil, err
		}
		comments = append(comments, &c)
	}
	return comments, rows.Err()
}

// AddComment adds a comment within the transaction
func (t *doltTransaction) AddComment(ctx context.Context, issueID, actor, comment string) error {
	table := "events"
	if IsEphemeralID(issueID) {
		table = "wisp_events"
	}

	//nolint:gosec // G201: table is hardcoded
	_, err := t.tx.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (issue_id, event_type, actor, comment)
		VALUES (?, ?, ?, ?)
	`, table), issueID, types.EventCommented, actor, comment)
	return err
}
