//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
)

// EmbeddedDoltStore reads and prunes the durable events journal through its
// own per-operation connection (withConn), so the `bd events` CLI works in
// embedded mode — where there is no stable *sql.DB to reach via RawDBAccessor.
var (
	_ storage.EventsJournalAccessor    = (*EmbeddedDoltStore)(nil)
	_ storage.EventsJournalCursor      = (*EmbeddedDoltStore)(nil)
	_ issueops.EventsMaintenanceRunner = (*EmbeddedDoltStore)(nil)
)

// ReadEventsJournal returns journal rows with seq greater than since. The
// read runs in a rolled-back transaction (no writes), matching every other
// read on this store.
func (s *EmbeddedDoltStore) ReadEventsJournal(ctx context.Context, since int64, limit int) ([]storage.EventsJournalRow, error) {
	var out []storage.EventsJournalRow
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var readErr error
		out, readErr = issueops.ReadEventsInTx(ctx, tx, since, limit)
		return readErr
	})
	return out, err
}

// ReadEventsJournalPage returns the same rows plus the journal head, on one
// connection so the pair cannot straddle a commit.
func (s *EmbeddedDoltStore) ReadEventsJournalPage(ctx context.Context, since int64, limit int) (storage.EventsJournalPage, error) {
	var page storage.EventsJournalPage
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var readErr error
		page, readErr = issueops.ReadEventsPageInTx(ctx, tx, since, limit)
		return readErr
	})
	return page, err
}

// PruneEventsJournal deletes journal rows below before, honoring the retain
// floors, and returns the number of rows deleted. The delete commits.
func (s *EmbeddedDoltStore) PruneEventsJournal(ctx context.Context, before int64, retainDays, retainRows int) (int64, error) {
	var n int64
	err := s.withConn(ctx, true, func(tx *sql.Tx) error {
		var pruneErr error
		n, pruneErr = issueops.PruneEventsInTx(ctx, tx, before, retainDays, retainRows, time.Now().UTC())
		return pruneErr
	})
	return n, err
}

// RunEventsMaintenanceTx runs one auto-prune step in its own transaction.
//
// Each call opens a connection of its own, which on this backend means booting
// the embedded engine again — the same price every other operation on this
// store pays, and the reason auto-prune resolves "is anything due?" in a single
// query (ReadEventsAutoPruneStateInTx) before opening any further ones. The
// alternative, pinning one connection across the whole pass, would fork the
// transaction lifecycle that withConn owns for every other write; a rare
// maintenance pass is not worth a second copy of that.
//
// A read-only store refuses here (ErrReadOnly, from withConn's commit arm)
// rather than silently doing nothing, and the caller logs it: maintenance never
// fails a user's command.
func (s *EmbeddedDoltStore) RunEventsMaintenanceTx(ctx context.Context, fn func(context.Context, issueops.DBTX) error) error {
	return s.withConn(ctx, true, func(tx *sql.Tx) error {
		return fn(ctx, tx)
	})
}
