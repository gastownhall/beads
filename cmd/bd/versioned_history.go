package main

import (
	"context"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/versionedhistory"
)

// Versioned-history activation, applied in the SAME place events-journal
// activation is (see the note at the top of events_journal.go): the factories
// that construct a store or a unit-of-work provider -- store_factory.go,
// store_factory_nocgo.go, uow_factory.go, the personal-migration planning store,
// and bd doctor's repair handlers through the same shared package. Every
// plumbing bd opens goes through one of those, so a routed write cannot acquire a
// store that silently records no history.
//
// It did not start there. It was first applied in wireStorageDecorators, which
// the root pre-run calls on the ONE store a command opens for its own workspace.
// A routed update or close (routed.go), a routed create (`bd create --repo`) and
// the direct-mode open each build their OWN write-capable store through
// newDoltStoreFromConfig, so with the switch on they recorded nothing while the
// command reported success -- the "configured on, actually off" class this
// feature exists to prevent. TestEveryStoreConstructionActivatesVersionedHistory
// keeps it in the factories.
//
// Each store answers for itself. The switch is a row of THAT store's own config
// table (what `bd config set versioned-history.enabled true` writes), OR'd with
// the process-wide environment, which can only turn recording on -- so a write
// routed into rig B reads rig B's row, never the launching workspace's. Nothing
// here takes a beadsDir for that reason: the store already knows where it lives.
//
// The two defers in a factory are ordered on purpose. Deferred calls run
// last-in-first-out, and this one is registered ABOVE the events-journal
// activation, so the journal runs first. A journal activation that fails closes
// the store and returns (nil, err); this one then passes that through instead of
// reading a row out of a closed store.
//
// The policy itself lives in internal/versionedhistory so bd doctor's fix
// package, which cannot import package main, applies the identical rule.

// activateVersionedHistoryStore is versionedhistory.ActivateStore under the name
// the construction guard matches. Kept as a wrapper rather than called directly so
// cmd/bd has one spelling of the idiom and the guard has one name to look for,
// exactly as activateEventsJournalStore does for its feature.
func activateVersionedHistoryStore(ctx context.Context, s storage.DoltStorage, err error) (storage.DoltStorage, error) {
	return versionedhistory.ActivateStore(ctx, s, err)
}

// activateVersionedHistoryProvider is the same for a unit-of-work provider -- bd's
// second write plumbing, with its own transactions and its own activation switch,
// which proxied-server mode and `bd serve` against a server-mode workspace write
// through instead of a DoltStorage.
//
// A provider has no DoltStorage to ask for a settings plane, so its row is read
// through a unit of work at construction (versionedhistory.ProviderSetting): one
// pooled connection, one SELECT, rolled back. The provider constructors have
// already opened the pool and run their schema setup by then, so the read starts
// no server and creates nothing.
func activateVersionedHistoryProvider(ctx context.Context, p uow.UnitOfWorkProvider, err error) (uow.UnitOfWorkProvider, error) {
	return versionedhistory.ActivateProvider(ctx, p, err)
}
