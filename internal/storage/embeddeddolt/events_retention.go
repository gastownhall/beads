//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
)

var _ storage.EventsRetention = (*EmbeddedDoltStore)(nil)

// CountEventsBefore reports how many history events a prune at this cutoff
// would delete. Read-only, so the connection rolls back like every other read.
func (s *EmbeddedDoltStore) CountEventsBefore(ctx context.Context, before time.Time) (int64, error) {
	var n int64
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var countErr error
		n, countErr = issueops.CountEventsBeforeInTx(ctx, tx, before)
		return countErr
	})
	return n, err
}

// PruneEventsBefore deletes the history events created before the cutoff. The
// delete commits the way every other write on this store does.
func (s *EmbeddedDoltStore) PruneEventsBefore(ctx context.Context, before time.Time) (int64, error) {
	var n int64
	err := s.withConn(ctx, true, func(tx *sql.Tx) error {
		var pruneErr error
		n, pruneErr = issueops.PruneEventsBeforeInTx(ctx, tx, before)
		return pruneErr
	})
	return n, err
}
