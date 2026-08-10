//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage"
	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/issueops"
)

// Commenter returns the guarded add-comment surface for this store.
func (s *EmbeddedDoltStore) Commenter() (issueops.Commenter, error) {
	return NewCommenter(s)
}

// NewCommenter returns a guarded commenter backed by store.
func NewCommenter(store *EmbeddedDoltStore) (issueops.Commenter, error) {
	if store == nil {
		return nil, &storage.ErrUnsupported{Op: "NewCommenter", Backend: "nil"}
	}
	return &commenter{store: store}, nil
}

type commenter struct{ store *EmbeddedDoltStore }

// AddComment resolves the plane and appends the row in ONE transaction, so a
// comment cannot land on an issue that was promoted or demoted between the
// resolve and the insert.
func (c *commenter) AddComment(ctx context.Context, request issueops.AddCommentRequest) (issueops.AddCommentResult, error) {
	if err := storageissueops.ValidateAddCommentRequest(request); err != nil {
		return issueops.AddCommentResult{}, err
	}
	message := storageissueops.AddCommentCommitMessage(request.IssueID)

	var result issueops.AddCommentResult
	err := c.store.runIssueOperationTx(ctx, message, func(tx *sql.Tx) (storageissueops.ChangedTables, error) {
		var err error
		var tables storageissueops.ChangedTables
		result, tables, err = storageissueops.ExecuteAddComment(ctx, tx, request)
		return tables, err
	})
	if err != nil {
		return issueops.AddCommentResult{}, err
	}
	return result, nil
}

var _ issueops.Commenter = (*commenter)(nil)
