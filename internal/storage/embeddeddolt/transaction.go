//go:build embeddeddolt

package embeddeddolt

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
	"github.com/steveyegge/beads/internal/types"
)

// RunInTransaction executes a function within a database transaction.
// After the SQL transaction commits, dirty tables are selectively staged
// and a Dolt version commit is created with the given message.
func (s *EmbeddedDoltStore) RunInTransaction(ctx context.Context, commitMsg string, fn func(tx storage.Transaction) error) error {
	var tracker versioncontrolops.DirtyTableTracker

	if err := s.withConn(ctx, true, func(sqlTx *sql.Tx) error {
		tx := &embeddedTransaction{tx: sqlTx, dirty: &tracker}
		return fn(tx)
	}); err != nil {
		return err
	}

	// Create a Dolt version commit from the working set changes.
	if commitMsg != "" && len(tracker.DirtyTables()) > 0 {
		return s.withDBConn(ctx, func(db versioncontrolops.DBConn) error {
			return versioncontrolops.StageAndCommit(ctx, db, tracker.DirtyTables(), commitMsg, commitAuthor)
		})
	}
	return nil
}

// embeddedTransaction implements storage.Transaction for EmbeddedDoltStore.
type embeddedTransaction struct {
	tx    *sql.Tx
	dirty *versioncontrolops.DirtyTableTracker
}

func (t *embeddedTransaction) CreateIssue(ctx context.Context, issue *types.Issue, actor string) error {
	bc, err := issueops.NewBatchContext(ctx, t.tx, storage.BatchCreateOptions{SkipPrefixValidation: true})
	if err != nil {
		return err
	}
	t.dirty.MarkDirty("issues")
	t.dirty.MarkDirty("events")
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
	t.dirty.MarkDirty("issues")
	t.dirty.MarkDirty("events")
	_, err := issueops.UpdateIssueInTx(ctx, t.tx, id, updates, actor)
	return err
}

func (t *embeddedTransaction) CloseIssue(ctx context.Context, id string, reason string, actor string, session string) error {
	t.dirty.MarkDirty("issues")
	t.dirty.MarkDirty("events")
	_, err := issueops.CloseIssueInTx(ctx, t.tx, id, reason, actor, session)
	return err
}

func (t *embeddedTransaction) DeleteIssue(ctx context.Context, id string) error {
	t.dirty.MarkDirty("issues")
	t.dirty.MarkDirty("dependencies")
	t.dirty.MarkDirty("labels")
	t.dirty.MarkDirty("comments")
	t.dirty.MarkDirty("events")
	return issueops.DeleteIssueInTx(ctx, t.tx, id)
}

func (t *embeddedTransaction) GetIssue(ctx context.Context, id string) (*types.Issue, error) {
	return issueops.GetIssueInTx(ctx, t.tx, id)
}

func (t *embeddedTransaction) SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error) {
	return issueops.SearchIssuesInTx(ctx, t.tx, query, filter)
}

func (t *embeddedTransaction) AddDependency(ctx context.Context, dep *types.Dependency, actor string) error {
	t.dirty.MarkDirty("dependencies")
	return issueops.AddDependencyInTx(ctx, t.tx, dep, actor, issueops.AddDependencyOpts{})
}

func (t *embeddedTransaction) RemoveDependency(ctx context.Context, issueID, dependsOnID string, actor string) error {
	t.dirty.MarkDirty("dependencies")
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
	t.dirty.MarkDirty("labels")
	return issueops.AddLabelInTx(ctx, t.tx, "", "", issueID, label, actor)
}

func (t *embeddedTransaction) RemoveLabel(ctx context.Context, issueID, label, actor string) error {
	t.dirty.MarkDirty("labels")
	return issueops.RemoveLabelInTx(ctx, t.tx, "", "", issueID, label, actor)
}

func (t *embeddedTransaction) GetLabels(ctx context.Context, issueID string) ([]string, error) {
	return issueops.GetLabelsInTx(ctx, t.tx, "", issueID)
}

func (t *embeddedTransaction) SetConfig(ctx context.Context, key, value string) error {
	t.dirty.MarkDirty("config")
	return issueops.SetConfigInTx(ctx, t.tx, key, value)
}

func (t *embeddedTransaction) GetConfig(ctx context.Context, key string) (string, error) {
	return issueops.GetConfigInTx(ctx, t.tx, key)
}

func (t *embeddedTransaction) SetMetadata(ctx context.Context, key, value string) error {
	t.dirty.MarkDirty("metadata")
	return issueops.SetMetadataInTx(ctx, t.tx, key, value)
}

func (t *embeddedTransaction) GetMetadata(ctx context.Context, key string) (string, error) {
	return issueops.GetMetadataInTx(ctx, t.tx, key)
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
	t.dirty.MarkDirty("issues")
	t.dirty.MarkDirty("events")
	return issueops.CreateIssueInTx(ctx, t.tx, bc, issue, actor)
}
