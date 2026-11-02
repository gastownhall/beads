package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// initUnversionedRecoveryCommitMessage is the fixed message on the one
// commit bd init makes to adopt an unversioned dirty server database
// (gastownhall/beads#6089). It is deliberately distinct from every
// migration-pass commit label so the adoption is identifiable in history —
// the reporter's recovery flow and the E2E tests both look for it.
const initUnversionedRecoveryCommitMessage = "bd init: commit pre-existing working set before schema migration (gastownhall/beads#6089)"

// initStoreOpener opens a store for a config: the signature of newDoltStore,
// injected so tests can observe the open/commit/close sequence without a
// Dolt server.
type initStoreOpener func(ctx context.Context, cfg *dolt.Config) (storage.DoltStorage, error)

// openInitStore opens bd init's store, adopting an unversioned dirty server
// database when the strict open refuses it (gastownhall/beads#6089).
//
// A first-time `bd init` can target a server database an older SDK (Gas
// Town v1.2.1 on beads v1.0.0) already wrote bootstrap residue into —
// modified config, new routes/interactions — without any main migration
// ever having run. The strict open refuses that database at the #4566
// dirty-table guard, and the refusal's own documented recovery (`bd dolt
// commit`) cannot run because every bd command opens the store first: a
// deadlock with no in-band escape. An explicit init is an adoption
// decision, so when the refusal is typed Unversioned the helper clears it
// the same way the documented manual recovery would: one lenient open
// (the store's existing working-set-reconcile mode, which tolerates its
// own refusal), one CommitAll of the entire committable working set, close
// that handle, and one final strict open that runs the migrations on the
// now-clean database.
//
// This is user-level policy composed from existing storage interfaces —
// no reset, no lock, no storage retry machinery. The reconciliation
// commit is not atomic with the eligibility observation, so a concurrent
// writer is not fenced; like the manual recovery, it commits whatever is
// in the working set, user rows included. Ineligible refusals — a
// versioned dirty database, an embedded/gateway/read-only/preview/proxied
// configuration, any other error — are returned unchanged.
func openInitStore(ctx context.Context, cfg *dolt.Config, open initStoreOpener, note func(string)) (storage.DoltStorage, error) {
	store, err := open(ctx, cfg)
	if err == nil || !initStoreAdoptionEligible(cfg, err) {
		return store, err
	}
	var refusal *schema.DirtyTablesError
	if !errors.As(err, &refusal) {
		// Unreachable: initStoreAdoptionEligible checked the same chain.
		return nil, err
	}

	lenientCfg := *cfg
	lenientCfg.LenientOpen = true
	lenient, lenientErr := open(ctx, &lenientCfg)
	if lenientErr != nil {
		return nil, fmt.Errorf("%w; bd init could not commit the pre-existing working set: %w",
			refusal, lenientErr)
	}
	committed, commitErr := lenient.CommitAll(ctx, initUnversionedRecoveryCommitMessage)
	// The lenient handle is closed before any further open or return, as
	// every init store close is: the close error is advisory and ignored.
	_ = lenient.Close()
	if commitErr != nil {
		return nil, fmt.Errorf("%w; bd init could not commit the pre-existing working set: %w",
			refusal, commitErr)
	}

	// The final open is the original strict intent, unmutated: it re-runs
	// the migration, and a refusal here is still fatal (and unchanged).
	final, finalErr := open(ctx, cfg)
	if finalErr != nil {
		return nil, finalErr
	}
	// A no-op reconciliation ((false, nil): nothing was committable) is not
	// an adoption, so it earns no note either.
	if committed && note != nil {
		note(fmt.Sprintf(
			"Note: database %q had uncommitted changes in tables the schema migrations alter (%s); "+
				"bd init committed the working set in its own commit, then applied the migrations; "+
				"no further action is needed (gastownhall/beads#6089)",
			cfg.Database, strings.Join(refusal.Tables, ", ")))
	}
	return final, nil
}

// initStoreAdoptionEligible reports whether err, the strict open's failure,
// may be recovered by committing the working set: a server-mode writable
// configuration and a refusal chain carrying an UNVERSIONED main-source
// DirtyTablesError. Embedded, gateway, read-only, preview and proxied
// configurations never gain adoption authority — an explicit init is
// consent for a database it can name on a server, not for whatever an
// embedded open would find — and a versioned dirty database (Unversioned
// false) keeps the ordinary refusal: its uncommitted rows may be a
// concurrent writer's half-applied work, not bootstrap residue.
func initStoreAdoptionEligible(cfg *dolt.Config, err error) bool {
	if err == nil {
		return false
	}
	if !cfg.ServerMode || cfg.Gateway || cfg.ReadOnly || cfg.Preview || cfg.ProxiedServer {
		return false
	}
	var refusal *schema.DirtyTablesError
	return errors.As(err, &refusal) && refusal.Unversioned
}
