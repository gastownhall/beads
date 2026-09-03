package uow

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
)

// EventsJournalCursorSource is the capability accessor a unit-of-work provider
// offers for the READ side of the durable events journal, the sibling of
// MemoriesSource and WorkspaceConfigSource. It is named here so a consumer
// holding a provider by interface asks for the journal the way a consumer
// holding a store asks for it — by accessor, not by reaching for a constructor.
//
// There is deliberately no prune accessor beside it. Retention is a decision
// the workspace makes, and the surfaces that page through the journal
// (GET /v0/beads/events) must not be one line away from a delete; the prune
// path stays on uw.EventsJournalUseCase(), where `bd events prune` reaches it
// inside the ephemeral-commit transaction the dolt_ignored table needs.
type EventsJournalCursorSource interface {
	EventsJournalCursor() (storage.EventsJournalCursor, error)
}

// eventsJournalCursor reads the journal through a unit of work.
type eventsJournalCursor struct {
	provider UnitOfWorkProvider
}

// EventsJournalCursor returns the journal read surface for this provider.
func (p *doltSQLProvider) EventsJournalCursor() (storage.EventsJournalCursor, error) {
	return NewEventsJournalCursor(p)
}

// NewEventsJournalCursor constructs a journal read surface backed by provider.
func NewEventsJournalCursor(provider UnitOfWorkProvider) (storage.EventsJournalCursor, error) {
	if isNilUnitOfWorkProvider(provider) {
		return nil, fmt.Errorf("new events journal cursor: unit-of-work provider must not be nil")
	}
	return &eventsJournalCursor{provider: provider}, nil
}

var _ storage.EventsJournalCursor = (*eventsJournalCursor)(nil)

// ReadEventsJournalPage answers one page inside one unit of work.
//
// RunTxRead, not RunTx: this never commits, which is what keeps a read of a
// dolt_ignored table from minting anything. The rows and the head come from
// the single transaction the use case runs them in, so the head cannot be
// taken from a later instant than the rows it describes.
func (c *eventsJournalCursor) ReadEventsJournalPage(ctx context.Context, since int64, limit int) (storage.EventsJournalPage, error) {
	return RunTxRead(ctx, c.provider, func(ctx context.Context, uw UnitOfWork) (storage.EventsJournalPage, error) {
		return uw.EventsJournalUseCase().ReadPage(ctx, since, limit)
	})
}
