package proxiedopen

import (
	"context"
	"time"

	"github.com/steveyegge/beads/internal/eventsjournal"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/versionedhistory"
)

// providerCloseTimeout bounds the close of a provider whose activation failed.
const providerCloseTimeout = 10 * time.Second

// activateEventsJournalProvider applies the workspace's events-journal switch
// to a just-constructed unit-of-work provider (closing it on failure).
// writesBeads is the provider's posture (uow.OpensForBeadWrites).
//
// The name is the one cmd/bd's construction guard
// (TestEveryStoreConstructionActivatesTheEventsJournal) matches; the guard
// scans this package because both provider constructors live here.
func activateEventsJournalProvider(ctx context.Context, beadsDir string, writesBeads bool, p uow.UnitOfWorkProvider, err error) (uow.UnitOfWorkProvider, error) {
	if err != nil || p == nil {
		return p, err
	}
	configurer, _ := p.(storage.EventsJournalConfigurer)
	if cfgErr := eventsjournal.Apply(configurer, eventsjournal.EnabledFor(beadsDir), writesBeads); cfgErr != nil {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), providerCloseTimeout)
		defer cancel()
		_ = p.Close(closeCtx)
		return nil, cfgErr
	}
	return p, nil
}

// activateVersionedHistoryProvider is the same for versioned history, under
// the name cmd/bd's versioned-history construction guard matches.
//
// A provider has no DoltStorage to ask for a settings plane, so its row is read
// with SQL of its own: the provider is handed the read and makes it inside its
// first unit of work (storage.VersionedHistoryDeferrer), one SELECT after that
// unit's START TRANSACTION. The provider constructors have already opened the
// pool and run their schema setup, so the read starts no server and creates
// nothing.
func activateVersionedHistoryProvider(ctx context.Context, p uow.UnitOfWorkProvider, err error) (uow.UnitOfWorkProvider, error) {
	return versionedhistory.ActivateProvider(ctx, p, err)
}
