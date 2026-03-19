//go:build embeddeddolt

package embeddeddolt

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// RunInTransaction executes a function within a database transaction.
// EmbeddedDolt auto-commits the SQL transaction; Dolt versioning is deferred
// to CommitPending which is called by the auto-commit flow.
func (s *EmbeddedDoltStore) RunInTransaction(ctx context.Context, commitMsg string, fn func(tx storage.Transaction) error) error {
	return s.withConn(ctx, true, func(sqlTx *sql.Tx) error {
		tx := &embeddedTransaction{tx: sqlTx}
		return fn(tx)
	})
}

// embeddedTransaction implements storage.Transaction for EmbeddedDoltStore.
type embeddedTransaction struct {
	tx *sql.Tx
}

func (t *embeddedTransaction) CreateIssue(ctx context.Context, issue *types.Issue, actor string) error {
	bc, err := issueops.NewBatchContext(ctx, t.tx, storage.BatchCreateOptions{SkipPrefixValidation: true})
	if err != nil {
		return err
	}
	return issueops.CreateIssueInTx(ctx, t.tx, bc, issue, actor)
}

func (t *embeddedTransaction) CreateIssues(ctx context.Context, issues []*types.Issue, actor string) error {
	for _, issue := range issues {
		if err := t.CreateIssue(ctx, issue, actor); err != nil {
			return err
		}
	}
	return nil
}

func (t *embeddedTransaction) UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
	_, err := issueops.UpdateIssueInTx(ctx, t.tx, id, updates, actor)
	return err
}

func (t *embeddedTransaction) CloseIssue(ctx context.Context, id string, reason string, actor string, session string) error {
	_, err := issueops.CloseIssueInTx(ctx, t.tx, id, reason, actor, session)
	return err
}

func (t *embeddedTransaction) DeleteIssue(ctx context.Context, id string) error {
	return issueops.DeleteIssueInTx(ctx, t.tx, id)
}

func (t *embeddedTransaction) GetIssue(ctx context.Context, id string) (*types.Issue, error) {
	return issueops.GetIssueInTx(ctx, t.tx, id)
}

func (t *embeddedTransaction) SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error) {
	return issueops.SearchIssuesInTx(ctx, t.tx, query, filter)
}

func (t *embeddedTransaction) AddDependency(ctx context.Context, dep *types.Dependency, actor string) error {
	return issueops.AddDependencyInTx(ctx, t.tx, dep, actor, issueops.AddDependencyOpts{})
}

func (t *embeddedTransaction) RemoveDependency(ctx context.Context, issueID, dependsOnID string, actor string) error {
	return issueops.RemoveDependencyInTx(ctx, t.tx, issueID, dependsOnID)
}

func (t *embeddedTransaction) GetDependencyRecords(ctx context.Context, issueID string) ([]*types.Dependency, error) {
	m, err := issueops.GetDependencyRecordsForIssuesInTx(ctx, t.tx, []string{issueID})
	if err != nil {
		return nil, err
	}
	return m[issueID], nil
}

func (t *embeddedTransaction) AddLabel(ctx context.Context, issueID, label, actor string) error {
	return issueops.AddLabelInTx(ctx, t.tx, "", "", issueID, label, actor)
}

func (t *embeddedTransaction) RemoveLabel(ctx context.Context, issueID, label, actor string) error {
	return issueops.RemoveLabelInTx(ctx, t.tx, "", "", issueID, label, actor)
}

func (t *embeddedTransaction) GetLabels(ctx context.Context, issueID string) ([]string, error) {
	return issueops.GetLabelsInTx(ctx, t.tx, "", issueID)
}

func (t *embeddedTransaction) SetConfig(ctx context.Context, key, value string) error {
	_, err := t.tx.ExecContext(ctx, "INSERT INTO config (`key`, value) VALUES (?, ?) ON DUPLICATE KEY UPDATE value = ?", key, value, value)
	return err
}

func (t *embeddedTransaction) GetConfig(ctx context.Context, key string) (string, error) {
	var value string
	err := t.tx.QueryRowContext(ctx, "SELECT value FROM config WHERE `key` = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("config key %q not found", key)
	}
	return value, err
}

func (t *embeddedTransaction) SetMetadata(ctx context.Context, key, value string) error {
	_, err := t.tx.ExecContext(ctx, "INSERT INTO metadata (`key`, value) VALUES (?, ?) ON DUPLICATE KEY UPDATE value = ?", key, value, value)
	return err
}

func (t *embeddedTransaction) GetMetadata(ctx context.Context, key string) (string, error) {
	var value string
	err := t.tx.QueryRowContext(ctx, "SELECT value FROM metadata WHERE `key` = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

func (t *embeddedTransaction) AddComment(ctx context.Context, issueID, actor, comment string) error {
	return fmt.Errorf("embeddedTransaction: AddComment not implemented")
}

func (t *embeddedTransaction) ImportIssueComment(ctx context.Context, issueID, author, text string, createdAt time.Time) (*types.Comment, error) {
	return nil, fmt.Errorf("embeddedTransaction: ImportIssueComment not implemented")
}

func (t *embeddedTransaction) GetIssueComments(ctx context.Context, issueID string) ([]*types.Comment, error) {
	return nil, fmt.Errorf("embeddedTransaction: GetIssueComments not implemented")
}

func (t *embeddedTransaction) CreateIssueImport(ctx context.Context, issue *types.Issue, actor string, skipPrefixValidation bool) error {
	bc, err := issueops.NewBatchContext(ctx, t.tx, storage.BatchCreateOptions{SkipPrefixValidation: skipPrefixValidation})
	if err != nil {
		return err
	}
	return issueops.CreateIssueInTx(ctx, t.tx, bc, issue, actor)
}
