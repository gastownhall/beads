package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/ephemeral"
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
// When an ephemeral store is attached, uses a routing transaction that
// lazily determines the target store (Dolt or SQLite) based on whether
// the first CreateIssue call has Ephemeral=true.
func (s *DoltStore) RunInTransaction(ctx context.Context, fn func(tx storage.Transaction) error) error {
	if s.ephemeralStore != nil {
		return s.runRoutingTransaction(ctx, fn)
	}
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

func (s *DoltStore) runRoutingTransaction(ctx context.Context, fn func(tx storage.Transaction) error) error {
	rt := &routingTransaction{store: s, ctx: ctx}

	defer func() {
		if r := recover(); r != nil {
			rt.rollback()
			panic(r)
		}
	}()

	if err := fn(rt); err != nil {
		rt.rollback()
		return err
	}

	return rt.commit()
}

// routingTransaction implements storage.Transaction by lazily routing to either
// the Dolt or ephemeral store based on the first CreateIssue call.
// Since cloneSubgraph always sets the same Ephemeral flag for all issues in a
// subgraph, all operations within one transaction go to the same store.
type routingTransaction struct {
	store    *DoltStore
	ctx      context.Context
	doltTx   *doltTransaction           // non-nil when routing to Dolt
	ephTx    *ephemeral.Tx              // non-nil when routing to ephemeral
	sqlTx    *sql.Tx                    // underlying Dolt tx (if started)
	resolved bool                       // true once the target store is determined
}

// ensureDolt lazily starts a Dolt transaction.
func (rt *routingTransaction) ensureDolt() error {
	if rt.doltTx != nil {
		return nil
	}
	sqlTx, err := rt.store.db.BeginTx(rt.ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin dolt transaction: %w", err)
	}
	rt.sqlTx = sqlTx
	rt.doltTx = &doltTransaction{tx: sqlTx, store: rt.store}
	rt.resolved = true
	return nil
}

// ensureEphemeral lazily starts an ephemeral transaction.
func (rt *routingTransaction) ensureEphemeral() error {
	if rt.ephTx != nil {
		return nil
	}
	tx, err := rt.store.ephemeralStore.BeginTx(rt.ctx)
	if err != nil {
		return fmt.Errorf("failed to begin ephemeral transaction: %w", err)
	}
	rt.ephTx = tx
	rt.resolved = true
	return nil
}

func (rt *routingTransaction) commit() error {
	if rt.doltTx != nil {
		return rt.sqlTx.Commit()
	}
	if rt.ephTx != nil {
		return rt.ephTx.Commit()
	}
	return nil // no ops happened
}

func (rt *routingTransaction) rollback() {
	if rt.sqlTx != nil {
		_ = rt.sqlTx.Rollback()
	}
	if rt.ephTx != nil {
		_ = rt.ephTx.Rollback()
	}
}

func (rt *routingTransaction) CreateIssue(ctx context.Context, issue *types.Issue, actor string) error {
	if issue.Ephemeral {
		if err := rt.ensureEphemeral(); err != nil {
			return err
		}
		return rt.ephTx.Transaction().CreateIssue(ctx, issue, actor)
	}
	if err := rt.ensureDolt(); err != nil {
		return err
	}
	return rt.doltTx.CreateIssue(ctx, issue, actor)
}

func (rt *routingTransaction) CreateIssues(ctx context.Context, issues []*types.Issue, actor string) error {
	for _, issue := range issues {
		if err := rt.CreateIssue(ctx, issue, actor); err != nil {
			return err
		}
	}
	return nil
}

func (rt *routingTransaction) UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
	if IsEphemeralID(id) {
		if err := rt.ensureEphemeral(); err != nil {
			return err
		}
		return rt.ephTx.Transaction().UpdateIssue(ctx, id, updates, actor)
	}
	if err := rt.ensureDolt(); err != nil {
		return err
	}
	return rt.doltTx.UpdateIssue(ctx, id, updates, actor)
}

func (rt *routingTransaction) CloseIssue(ctx context.Context, id string, reason string, actor string, session string) error {
	if IsEphemeralID(id) {
		if err := rt.ensureEphemeral(); err != nil {
			return err
		}
		return rt.ephTx.Transaction().CloseIssue(ctx, id, reason, actor, session)
	}
	if err := rt.ensureDolt(); err != nil {
		return err
	}
	return rt.doltTx.CloseIssue(ctx, id, reason, actor, session)
}

func (rt *routingTransaction) DeleteIssue(ctx context.Context, id string) error {
	if IsEphemeralID(id) {
		if err := rt.ensureEphemeral(); err != nil {
			return err
		}
		return rt.ephTx.Transaction().DeleteIssue(ctx, id)
	}
	if err := rt.ensureDolt(); err != nil {
		return err
	}
	return rt.doltTx.DeleteIssue(ctx, id)
}

func (rt *routingTransaction) GetIssue(ctx context.Context, id string) (*types.Issue, error) {
	if IsEphemeralID(id) && rt.ephTx != nil {
		return rt.ephTx.Transaction().GetIssue(ctx, id)
	}
	if rt.doltTx != nil {
		return rt.doltTx.GetIssue(ctx, id)
	}
	// Fall back to non-transactional read
	return rt.store.GetIssue(ctx, id)
}

func (rt *routingTransaction) SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error) {
	if filter.Ephemeral != nil && *filter.Ephemeral && rt.ephTx != nil {
		return rt.ephTx.Transaction().SearchIssues(ctx, query, filter)
	}
	if rt.doltTx != nil {
		return rt.doltTx.SearchIssues(ctx, query, filter)
	}
	return rt.store.SearchIssues(ctx, query, filter)
}

func (rt *routingTransaction) AddDependency(ctx context.Context, dep *types.Dependency, actor string) error {
	if IsEphemeralID(dep.IssueID) {
		if err := rt.ensureEphemeral(); err != nil {
			return err
		}
		return rt.ephTx.Transaction().AddDependency(ctx, dep, actor)
	}
	if err := rt.ensureDolt(); err != nil {
		return err
	}
	return rt.doltTx.AddDependency(ctx, dep, actor)
}

func (rt *routingTransaction) RemoveDependency(ctx context.Context, issueID, dependsOnID string, actor string) error {
	if IsEphemeralID(issueID) {
		if err := rt.ensureEphemeral(); err != nil {
			return err
		}
		return rt.ephTx.Transaction().RemoveDependency(ctx, issueID, dependsOnID, actor)
	}
	if err := rt.ensureDolt(); err != nil {
		return err
	}
	return rt.doltTx.RemoveDependency(ctx, issueID, dependsOnID, actor)
}

func (rt *routingTransaction) GetDependencyRecords(ctx context.Context, issueID string) ([]*types.Dependency, error) {
	if IsEphemeralID(issueID) && rt.ephTx != nil {
		return rt.ephTx.Transaction().GetDependencyRecords(ctx, issueID)
	}
	if rt.doltTx != nil {
		return rt.doltTx.GetDependencyRecords(ctx, issueID)
	}
	return rt.store.GetDependencyRecords(ctx, issueID)
}

func (rt *routingTransaction) AddLabel(ctx context.Context, issueID, label, actor string) error {
	if IsEphemeralID(issueID) {
		if err := rt.ensureEphemeral(); err != nil {
			return err
		}
		return rt.ephTx.Transaction().AddLabel(ctx, issueID, label, actor)
	}
	if err := rt.ensureDolt(); err != nil {
		return err
	}
	return rt.doltTx.AddLabel(ctx, issueID, label, actor)
}

func (rt *routingTransaction) RemoveLabel(ctx context.Context, issueID, label, actor string) error {
	if IsEphemeralID(issueID) {
		if err := rt.ensureEphemeral(); err != nil {
			return err
		}
		return rt.ephTx.Transaction().RemoveLabel(ctx, issueID, label, actor)
	}
	if err := rt.ensureDolt(); err != nil {
		return err
	}
	return rt.doltTx.RemoveLabel(ctx, issueID, label, actor)
}

func (rt *routingTransaction) GetLabels(ctx context.Context, issueID string) ([]string, error) {
	if IsEphemeralID(issueID) && rt.ephTx != nil {
		return rt.ephTx.Transaction().GetLabels(ctx, issueID)
	}
	if rt.doltTx != nil {
		return rt.doltTx.GetLabels(ctx, issueID)
	}
	return rt.store.GetLabels(ctx, issueID)
}

func (rt *routingTransaction) SetConfig(ctx context.Context, key, value string) error {
	// Config always goes to Dolt (the persistent store)
	if err := rt.ensureDolt(); err != nil {
		return err
	}
	return rt.doltTx.SetConfig(ctx, key, value)
}

func (rt *routingTransaction) GetConfig(ctx context.Context, key string) (string, error) {
	if rt.doltTx != nil {
		return rt.doltTx.GetConfig(ctx, key)
	}
	return rt.store.GetConfig(ctx, key)
}

func (rt *routingTransaction) SetMetadata(ctx context.Context, key, value string) error {
	if err := rt.ensureDolt(); err != nil {
		return err
	}
	return rt.doltTx.SetMetadata(ctx, key, value)
}

func (rt *routingTransaction) GetMetadata(ctx context.Context, key string) (string, error) {
	if rt.doltTx != nil {
		return rt.doltTx.GetMetadata(ctx, key)
	}
	// Fall back to non-tx read
	return "", nil
}

func (rt *routingTransaction) AddComment(ctx context.Context, issueID, actor, comment string) error {
	if IsEphemeralID(issueID) {
		if err := rt.ensureEphemeral(); err != nil {
			return err
		}
		return rt.ephTx.Transaction().AddComment(ctx, issueID, actor, comment)
	}
	if err := rt.ensureDolt(); err != nil {
		return err
	}
	return rt.doltTx.AddComment(ctx, issueID, actor, comment)
}

func (rt *routingTransaction) ImportIssueComment(ctx context.Context, issueID, author, text string, createdAt time.Time) (*types.Comment, error) {
	if IsEphemeralID(issueID) {
		if err := rt.ensureEphemeral(); err != nil {
			return nil, err
		}
		return rt.ephTx.Transaction().ImportIssueComment(ctx, issueID, author, text, createdAt)
	}
	if err := rt.ensureDolt(); err != nil {
		return nil, err
	}
	return rt.doltTx.ImportIssueComment(ctx, issueID, author, text, createdAt)
}

func (rt *routingTransaction) GetIssueComments(ctx context.Context, issueID string) ([]*types.Comment, error) {
	if IsEphemeralID(issueID) && rt.ephTx != nil {
		return rt.ephTx.Transaction().GetIssueComments(ctx, issueID)
	}
	if rt.doltTx != nil {
		return rt.doltTx.GetIssueComments(ctx, issueID)
	}
	return rt.store.GetIssueComments(ctx, issueID)
}

// CreateIssue creates an issue within the transaction
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

	// Generate ID if not provided (critical for wisp creation)
	if issue.ID == "" {
		// Get prefix from config
		var configPrefix string
		err := t.tx.QueryRowContext(ctx, "SELECT value FROM config WHERE `key` = ?", "issue_prefix").Scan(&configPrefix)
		if err == sql.ErrNoRows || configPrefix == "" {
			return fmt.Errorf("database not initialized: issue_prefix config is missing")
		} else if err != nil {
			return fmt.Errorf("failed to get config: %w", err)
		}

		// Determine effective prefix
		prefix := configPrefix
		if issue.PrefixOverride != "" {
			prefix = issue.PrefixOverride
		} else if issue.IDPrefix != "" {
			prefix = configPrefix + "-" + issue.IDPrefix
		}

		// Generate ID
		generatedID, err := generateIssueID(ctx, t.tx, prefix, issue, actor)
		if err != nil {
			return fmt.Errorf("failed to generate issue ID: %w", err)
		}
		issue.ID = generatedID
	}

	return insertIssueTx(ctx, t.tx, issue)
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

// GetIssue retrieves an issue within the transaction
func (t *doltTransaction) GetIssue(ctx context.Context, id string) (*types.Issue, error) {
	return scanIssueTx(ctx, t.tx, id)
}

// SearchIssues searches for issues within the transaction
func (t *doltTransaction) SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error) {
	// Simplified search for transaction context
	whereClauses := []string{}
	args := []interface{}{}

	if query != "" {
		whereClauses = append(whereClauses, "(title LIKE ? OR description LIKE ? OR id LIKE ?)")
		pattern := "%" + query + "%"
		args = append(args, pattern, pattern, pattern)
	}

	// Parent filtering: filter children by parent issue
	// Also includes dotted-ID children (e.g., "parent.1.2" is child of "parent")
	if filter.ParentID != nil {
		parentID := *filter.ParentID
		whereClauses = append(whereClauses, "(id IN (SELECT issue_id FROM dependencies WHERE type = 'parent-child' AND depends_on_id = ?) OR id LIKE CONCAT(?, '.%'))")
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

	rows, err := t.tx.QueryContext(ctx, fmt.Sprintf(`
		SELECT id FROM issues %s ORDER BY priority ASC, created_at DESC
	`, whereSQL), args...)
	if err != nil {
		return nil, err
	}

	// Collect all IDs first, then close rows before fetching full issues.
	// MySQL server mode can't handle multiple active result sets on one connection.
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close() // Best effort cleanup on error path
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close() // Best effort cleanup on error path
		return nil, err
	}
	_ = rows.Close() // Redundant close for safety (rows already iterated)

	// Now fetch each issue (safe since rows is closed)
	var issues []*types.Issue
	for _, id := range ids {
		issue, err := t.GetIssue(ctx, id)
		if err != nil {
			return nil, err
		}
		if issue != nil {
			issues = append(issues, issue)
		}
	}
	return issues, nil
}

// UpdateIssue updates an issue within the transaction
func (t *doltTransaction) UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
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

	args = append(args, id)
	// nolint:gosec // G201: setClauses contains only column names (e.g. "status = ?"), actual values passed via args
	query := fmt.Sprintf("UPDATE issues SET %s WHERE id = ?", strings.Join(setClauses, ", "))
	_, err := t.tx.ExecContext(ctx, query, args...)
	return err
}

// CloseIssue closes an issue within the transaction
func (t *doltTransaction) CloseIssue(ctx context.Context, id string, reason string, actor string, session string) error {
	now := time.Now().UTC()
	_, err := t.tx.ExecContext(ctx, `
		UPDATE issues SET status = ?, closed_at = ?, updated_at = ?, close_reason = ?, closed_by_session = ?
		WHERE id = ?
	`, types.StatusClosed, now, now, reason, session, id)
	return err
}

// DeleteIssue deletes an issue within the transaction
func (t *doltTransaction) DeleteIssue(ctx context.Context, id string) error {
	_, err := t.tx.ExecContext(ctx, "DELETE FROM issues WHERE id = ?", id)
	return err
}

// AddDependency adds a dependency within the transaction
func (t *doltTransaction) AddDependency(ctx context.Context, dep *types.Dependency, actor string) error {
	_, err := t.tx.ExecContext(ctx, `
		INSERT INTO dependencies (issue_id, depends_on_id, type, created_at, created_by, thread_id)
		VALUES (?, ?, ?, NOW(), ?, ?)
		ON DUPLICATE KEY UPDATE type = VALUES(type)
	`, dep.IssueID, dep.DependsOnID, dep.Type, actor, dep.ThreadID)
	return err
}

func (t *doltTransaction) GetDependencyRecords(ctx context.Context, issueID string) ([]*types.Dependency, error) {
	rows, err := t.tx.QueryContext(ctx, `
		SELECT issue_id, depends_on_id, type, created_at, created_by, metadata, thread_id
		FROM dependencies
		WHERE issue_id = ?
	`, issueID)
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
	_, err := t.tx.ExecContext(ctx, `
		DELETE FROM dependencies WHERE issue_id = ? AND depends_on_id = ?
	`, issueID, dependsOnID)
	return err
}

// AddLabel adds a label within the transaction
func (t *doltTransaction) AddLabel(ctx context.Context, issueID, label, actor string) error {
	_, err := t.tx.ExecContext(ctx, `
		INSERT IGNORE INTO labels (issue_id, label) VALUES (?, ?)
	`, issueID, label)
	return err
}

func (t *doltTransaction) GetLabels(ctx context.Context, issueID string) ([]string, error) {
	rows, err := t.tx.QueryContext(ctx, `SELECT label FROM labels WHERE issue_id = ? ORDER BY label`, issueID)
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
	_, err := t.tx.ExecContext(ctx, `
		DELETE FROM labels WHERE issue_id = ? AND label = ?
	`, issueID, label)
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
	// Verify issue exists in tx
	iss, err := t.GetIssue(ctx, issueID)
	if err != nil {
		return nil, err
	}
	if iss == nil {
		return nil, fmt.Errorf("issue %s not found", issueID)
	}

	createdAt = createdAt.UTC()
	res, err := t.tx.ExecContext(ctx, `
		INSERT INTO comments (issue_id, author, text, created_at)
		VALUES (?, ?, ?, ?)
	`, issueID, author, text, createdAt)
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
	rows, err := t.tx.QueryContext(ctx, `
		SELECT id, issue_id, author, text, created_at
		FROM comments
		WHERE issue_id = ?
		ORDER BY created_at ASC
	`, issueID)
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
	_, err := t.tx.ExecContext(ctx, `
		INSERT INTO events (issue_id, event_type, actor, comment)
		VALUES (?, ?, ?, ?)
	`, issueID, types.EventCommented, actor, comment)
	return err
}

// Helper functions for transaction context

func insertIssueTx(ctx context.Context, tx *sql.Tx, issue *types.Issue) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO issues (
			id, content_hash, title, description, design, acceptance_criteria, notes,
			status, priority, issue_type, assignee, estimated_minutes,
			created_at, created_by, owner, updated_at, closed_at,
			sender, ephemeral, wisp_type, pinned, is_template, crystallizes
		) VALUES (
			?, ?, ?, ?, ?, ?, ?,
			?, ?, ?, ?, ?,
			?, ?, ?, ?, ?,
			?, ?, ?, ?, ?, ?
		)
	`,
		issue.ID, issue.ContentHash, issue.Title, issue.Description, issue.Design, issue.AcceptanceCriteria, issue.Notes,
		issue.Status, issue.Priority, issue.IssueType, nullString(issue.Assignee), nullInt(issue.EstimatedMinutes),
		issue.CreatedAt, issue.CreatedBy, issue.Owner, issue.UpdatedAt, issue.ClosedAt,
		issue.Sender, issue.Ephemeral, string(issue.WispType), issue.Pinned, issue.IsTemplate, issue.Crystallizes,
	)
	return err
}

// scanIssueTx is an intentionally minimal projection for transaction-scoped reads.
// It selects only the ~21 columns needed within transactions (e.g., import, batch ops)
// rather than the full ~50 columns in issueSelectColumns / scanIssueFrom.
// This is a deliberate trade-off: transactions need core fields for logic decisions,
// not the full hydration needed by export/display paths.
func scanIssueTx(ctx context.Context, tx *sql.Tx, id string) (*types.Issue, error) {
	var issue types.Issue
	var createdAtStr, updatedAtStr sql.NullString // TEXT columns - must parse manually
	var closedAt sql.NullTime
	var estimatedMinutes sql.NullInt64
	var assignee, owner, contentHash sql.NullString
	var ephemeral, pinned, isTemplate, crystallizes sql.NullInt64

	err := tx.QueryRowContext(ctx, `
		SELECT id, content_hash, title, description, design, acceptance_criteria, notes,
		       status, priority, issue_type, assignee, estimated_minutes,
		       created_at, created_by, owner, updated_at, closed_at,
		       ephemeral, pinned, is_template, crystallizes
		FROM issues
		WHERE id = ?
	`, id).Scan(
		&issue.ID, &contentHash, &issue.Title, &issue.Description, &issue.Design,
		&issue.AcceptanceCriteria, &issue.Notes, &issue.Status,
		&issue.Priority, &issue.IssueType, &assignee, &estimatedMinutes,
		&createdAtStr, &issue.CreatedBy, &owner, &updatedAtStr, &closedAt,
		&ephemeral, &pinned, &isTemplate, &crystallizes,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
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

	return &issue, nil
}
