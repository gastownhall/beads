package dolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage"
	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// IssueRelations returns the guarded neighbor-query surface for this store.
func (s *DoltStore) IssueRelations() (issueops.Relations, error) {
	return NewIssueRelations(s)
}

// NewIssueRelations returns guarded neighbor queries backed by store.
func NewIssueRelations(store *DoltStore) (issueops.Relations, error) {
	if store == nil {
		return nil, &storage.ErrUnsupported{Op: "NewIssueRelations", Backend: "nil"}
	}
	return &issueRelations{store: store}, nil
}

type issueRelations struct{ store *DoltStore }

// Related runs the anchor probe and the neighbor read in ONE read
// transaction, so an anchor cannot be reported missing by a probe that raced a
// create the neighbor read then saw.
func (r *issueRelations) Related(ctx context.Context, request issueops.RelatedRequest) ([]*issueops.RelatedIssue, error) {
	if err := storageissueops.ValidateRelatedRequest(request); err != nil {
		return nil, err
	}
	var items []*types.IssueWithDependencyMetadata
	err := r.store.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		items, err = storageissueops.ExecuteRelated(ctx, tx, request)
		return err
	})
	if err != nil {
		return nil, err
	}
	return items, nil
}

var _ issueops.Relations = (*issueRelations)(nil)
