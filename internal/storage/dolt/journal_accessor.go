package dolt

import (
	"context"
	"database/sql"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
)

// DoltStore reads and prunes the durable events journal through its own
// transaction helpers so the `bd events` CLI works against a server-mode
// store the same way it does against the embedded store.
var (
	_ storage.EventsJournalAccessor    = (*DoltStore)(nil)
	_ storage.EventsJournalCursor      = (*DoltStore)(nil)
	_ issueops.EventsMaintenanceRunner = (*DoltStore)(nil)
)

// ReadEventsJournal returns journal rows with seq greater than since.
func (s *DoltStore) ReadEventsJournal(ctx context.Context, since int64, limit int) ([]storage.EventsJournalRow, error) {
	var out []storage.EventsJournalRow
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var readErr error
		out, readErr = issueops.ReadEventsInTx(ctx, tx, since, limit)
		return readErr
	})
	return out, err
}

// ReadEventsJournalPage returns the same rows plus the journal head, in ONE
// transaction so the pair cannot straddle a commit.
func (s *DoltStore) ReadEventsJournalPage(ctx context.Context, since int64, limit int) (storage.EventsJournalPage, error) {
	var page storage.EventsJournalPage
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var readErr error
		page, readErr = issueops.ReadEventsPageInTx(ctx, tx, since, limit)
		return readErr
	})
	return page, err
}

// PruneEventsJournal deletes journal rows below before, honoring the retain
// floors, and returns the number of rows deleted.
func (s *DoltStore) PruneEventsJournal(ctx context.Context, before int64, retainDays, retainRows int) (int64, error) {
	var n int64
	err := s.withRetryTx(ctx, func(tx *sql.Tx) error {
		var pruneErr error
		n, pruneErr = issueops.PruneEventsInTx(ctx, tx, before, retainDays, retainRows, time.Now().UTC())
		return pruneErr
	})
	return n, err
}

// RunEventsMaintenanceTx runs one auto-prune step in its own transaction. Like
// the explicit prune it commits without staging anything for version control,
// which is what the dolt_ignored journal tables need.
//
// It uses withWriteTx rather than withRetryTx, and that is the whole point of
// having its own method: maintenance must be HUMBLE. withRetryTx exists to make
// a user's write survive Dolt's optimistic-commit conflicts, and it will spend
// up to fifteen seconds of backoff per transaction doing it — which, across a
// pass, is over a minute of a user's command spent losing races on work nobody
// asked for. A maintenance transaction that loses a race has nothing to
// recover: the prefix delete is idempotent, the watermark is already stamped,
// and the next trigger picks the backlog up. Failing immediately and letting
// the caller log it is strictly better than waiting.
func (s *DoltStore) RunEventsMaintenanceTx(ctx context.Context, fn func(context.Context, issueops.DBTX) error) error {
	return s.withWriteTx(ctx, func(tx *sql.Tx) error {
		return fn(ctx, tx)
	})
}
