//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage"
	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/issueops"
)

// IssueClaimer returns the guarded atomic-claim surface for this store.
func (s *EmbeddedDoltStore) IssueClaimer() (issueops.Claimer, error) {
	return NewIssueClaimer(s)
}

// NewIssueClaimer returns the guarded atomic claim backed by store.
func NewIssueClaimer(store *EmbeddedDoltStore) (issueops.Claimer, error) {
	if store == nil {
		return nil, &storage.ErrUnsupported{Op: "NewIssueClaimer", Backend: "nil"}
	}
	return &issueClaimer{store: store}, nil
}

type issueClaimer struct{ store *EmbeddedDoltStore }

func (c *issueClaimer) Claim(ctx context.Context, request issueops.ClaimRequest) (issueops.ClaimResult, error) {
	var result issueops.ClaimResult
	err := c.store.runIssueOperationTx(ctx, storageissueops.ClaimCommitMessage(request.IssueID, request.Actor),
		func(tx *sql.Tx) (storageissueops.ChangedTables, error) {
			var err error
			var tables storageissueops.ChangedTables
			result, tables, err = storageissueops.ExecuteClaim(ctx, tx, request)
			return tables, err
		})
	if err != nil {
		return issueops.ClaimResult{}, err
	}
	return result, nil
}

var _ issueops.Claimer = (*issueClaimer)(nil)
