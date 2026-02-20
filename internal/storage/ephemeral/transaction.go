package ephemeral

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// ephemeralTransaction implements storage.Transaction for the ephemeral store.
type ephemeralTransaction struct {
	tx    *sql.Tx
	store *Store
}

// RunInTransaction executes fn within a database transaction.
func (s *Store) RunInTransaction(ctx context.Context, fn func(tx storage.Transaction) error) error {
	sqlTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin ephemeral tx: %w", err)
	}

	tx := &ephemeralTransaction{tx: sqlTx, store: s}
	defer func() {
		if r := recover(); r != nil {
			_ = sqlTx.Rollback()
			panic(r)
		}
	}()

	if err := fn(tx); err != nil {
		_ = sqlTx.Rollback()
		return err
	}

	return sqlTx.Commit()
}

// BeginTx starts a transaction and returns it for external management.
// The caller is responsible for calling Commit() or Rollback() on the returned Tx.
// Used by the DoltStore routing transaction to manage ephemeral transactions externally.
func (s *Store) BeginTx(ctx context.Context) (*Tx, error) {
	sqlTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin ephemeral tx: %w", err)
	}
	return &Tx{inner: &ephemeralTransaction{tx: sqlTx, store: s}, sqlTx: sqlTx}, nil
}

// Tx is an exported transaction handle for the ephemeral store.
// It wraps ephemeralTransaction and exposes Commit/Rollback for external lifecycle management.
type Tx struct {
	inner *ephemeralTransaction
	sqlTx *sql.Tx
}

// Transaction returns the storage.Transaction interface for use in routing transactions.
func (t *Tx) Transaction() storage.Transaction { return t.inner }

// Commit commits the transaction.
func (t *Tx) Commit() error { return t.sqlTx.Commit() }

// Rollback rolls back the transaction.
func (t *Tx) Rollback() error { return t.sqlTx.Rollback() }

func (t *ephemeralTransaction) CreateIssue(ctx context.Context, issue *types.Issue, actor string) error {
	now := time.Now().UTC()
	if issue.CreatedAt.IsZero() {
		issue.CreatedAt = now
	}
	if issue.UpdatedAt.IsZero() {
		issue.UpdatedAt = now
	}
	issue.Ephemeral = true
	return t.store.insertIssue(ctx, t.tx, issue, actor)
}

func (t *ephemeralTransaction) CreateIssues(ctx context.Context, issues []*types.Issue, actor string) error {
	for _, issue := range issues {
		if err := t.CreateIssue(ctx, issue, actor); err != nil {
			return err
		}
	}
	return nil
}

func (t *ephemeralTransaction) UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
	// Build update query within transaction
	if len(updates) == 0 {
		return nil
	}

	var setClauses []string
	var args []any

	for key, val := range updates {
		col := mapFieldToColumn(key)
		if col == "" {
			continue
		}
		setClauses = append(setClauses, col+" = ?")
		args = append(args, normalizeUpdateValue(val))
	}
	if len(setClauses) == 0 {
		return nil
	}

	setClauses = append(setClauses, "updated_at = ?")
	args = append(args, formatTime(time.Now().UTC()))
	args = append(args, id)

	_, err := t.tx.ExecContext(ctx, "UPDATE issues SET "+strings.Join(setClauses, ", ")+" WHERE id = ?", args...)
	return err
}

func (t *ephemeralTransaction) CloseIssue(ctx context.Context, id string, reason string, actor string, session string) error {
	now := formatTime(time.Now().UTC())
	_, err := t.tx.ExecContext(ctx,
		`UPDATE issues SET status = 'closed', closed_at = ?, close_reason = ?, closed_by_session = ?, updated_at = ? WHERE id = ?`,
		now, reason, session, now, id)
	return err
}

func (t *ephemeralTransaction) DeleteIssue(ctx context.Context, id string) error {
	_, err := t.tx.ExecContext(ctx, "DELETE FROM issues WHERE id = ?", id)
	return err
}

func (t *ephemeralTransaction) GetIssue(ctx context.Context, id string) (*types.Issue, error) {
	row := t.tx.QueryRowContext(ctx, "SELECT "+issueSelectColumns+" FROM issues WHERE id = ?", id)
	issue, err := scanIssue(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return issue, err
}

func (t *ephemeralTransaction) SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error) {
	// SQLite with MaxOpenConns(1) ensures the tx and store share the same connection,
	// so read-your-writes works even when delegating to the store.
	return t.store.SearchIssues(ctx, query, filter)
}

func (t *ephemeralTransaction) AddDependency(ctx context.Context, dep *types.Dependency, actor string) error {
	_, err := t.tx.ExecContext(ctx,
		`INSERT OR REPLACE INTO dependencies (issue_id, depends_on_id, type, created_by, metadata, thread_id)
		 VALUES (?, ?, ?, ?, '{}', ?)`,
		dep.IssueID, dep.DependsOnID, dep.Type, actor, dep.ThreadID)
	return err
}

func (t *ephemeralTransaction) RemoveDependency(ctx context.Context, issueID, dependsOnID string, actor string) error {
	_, err := t.tx.ExecContext(ctx,
		`DELETE FROM dependencies WHERE issue_id = ? AND depends_on_id = ?`,
		issueID, dependsOnID)
	return err
}

func (t *ephemeralTransaction) GetDependencyRecords(ctx context.Context, issueID string) ([]*types.Dependency, error) {
	return t.store.GetDependencyRecords(ctx, issueID)
}

func (t *ephemeralTransaction) AddLabel(ctx context.Context, issueID, label, actor string) error {
	_, err := t.tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO labels (issue_id, label) VALUES (?, ?)`,
		issueID, label)
	return err
}

func (t *ephemeralTransaction) RemoveLabel(ctx context.Context, issueID, label, actor string) error {
	_, err := t.tx.ExecContext(ctx,
		`DELETE FROM labels WHERE issue_id = ? AND label = ?`,
		issueID, label)
	return err
}

func (t *ephemeralTransaction) GetLabels(ctx context.Context, issueID string) ([]string, error) {
	return t.store.getLabels(ctx, t.tx, issueID)
}

func (t *ephemeralTransaction) SetConfig(ctx context.Context, key, value string) error {
	_, err := t.tx.ExecContext(ctx,
		`INSERT OR REPLACE INTO config (key, value) VALUES (?, ?)`,
		key, value)
	return err
}

func (t *ephemeralTransaction) GetConfig(ctx context.Context, key string) (string, error) {
	var value string
	err := t.tx.QueryRowContext(ctx, "SELECT value FROM config WHERE key = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

func (t *ephemeralTransaction) SetMetadata(ctx context.Context, key, value string) error {
	return t.SetConfig(ctx, "meta:"+key, value)
}

func (t *ephemeralTransaction) GetMetadata(ctx context.Context, key string) (string, error) {
	return t.GetConfig(ctx, "meta:"+key)
}

func (t *ephemeralTransaction) AddComment(ctx context.Context, issueID, actor, comment string) error {
	_, err := t.tx.ExecContext(ctx,
		`INSERT INTO comments (issue_id, author, text) VALUES (?, ?, ?)`,
		issueID, actor, comment)
	return err
}

func (t *ephemeralTransaction) ImportIssueComment(ctx context.Context, issueID, author, text string, createdAt time.Time) (*types.Comment, error) {
	result, err := t.tx.ExecContext(ctx,
		`INSERT INTO comments (issue_id, author, text, created_at) VALUES (?, ?, ?, ?)`,
		issueID, author, text, formatTime(createdAt))
	if err != nil {
		return nil, err
	}
	id, _ := result.LastInsertId()
	return &types.Comment{
		ID:        id,
		IssueID:   issueID,
		Author:    author,
		Text:      text,
		CreatedAt: createdAt,
	}, nil
}

func (t *ephemeralTransaction) GetIssueComments(ctx context.Context, issueID string) ([]*types.Comment, error) {
	return t.store.GetIssueComments(ctx, issueID)
}

