package dolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage"
	storeops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/issueops"
)

// BlockedLister returns the blocked-listing surface for this store.
func (s *DoltStore) BlockedLister() (issueops.BlockedLister, error) {
	if s == nil {
		return nil, &storage.ErrUnsupported{Op: "BlockedLister", Backend: "nil"}
	}
	return &blockedLister{store: s}, nil
}

// blockedLister answers a ListBlocked from one read transaction around the
// shared body, storeops.ExecuteListBlockedInTx, the same body GetBlockedIssues
// runs.
type blockedLister struct{ store *DoltStore }

var _ issueops.BlockedLister = (*blockedLister)(nil)

func (l *blockedLister) ListBlocked(ctx context.Context, req issueops.BlockedRequest) (issueops.BlockedResult, error) {
	var result issueops.BlockedResult
	err := l.store.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = storeops.ExecuteListBlockedInTx(ctx, tx, req)
		return err
	})
	if err != nil {
		return issueops.BlockedResult{}, err
	}
	return result, nil
}
