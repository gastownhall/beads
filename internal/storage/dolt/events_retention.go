package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
)

var _ storage.EventsRetention = (*DoltStore)(nil)

// CountEventsBefore reports how many history events a prune at this cutoff
// would delete.
func (s *DoltStore) CountEventsBefore(ctx context.Context, before time.Time) (int64, error) {
	var n int64
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var countErr error
		n, countErr = issueops.CountEventsBeforeInTx(ctx, tx, before)
		return countErr
	})
	return n, err
}

// PruneEventsBefore deletes the history events created before the cutoff and
// commits the delete to Dolt (one commit, staging only the events table). A
// prune that removes nothing commits nothing.
func (s *DoltStore) PruneEventsBefore(ctx context.Context, before time.Time) (int64, error) {
	var n int64
	err := s.withCircuitWrite(ctx, func(ctx context.Context) error {
		if err := s.withWriteTx(ctx, func(tx *sql.Tx) error {
			var pruneErr error
			n, pruneErr = issueops.PruneEventsBeforeInTx(ctx, tx, before)
			if pruneErr != nil || n == 0 {
				return pruneErr
			}
			return s.doltAddAndCommitInTx(ctx, tx, []string{"events"},
				fmt.Sprintf("bd: gc prune %d history event(s) before %s", n, before.UTC().Format(time.RFC3339)))
		}); err != nil {
			return s.recordDoltPublicationFailure(ctx, err)
		}
		return nil
	})
	return n, err
}
