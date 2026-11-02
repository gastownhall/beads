package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// initStoreOpener is newDoltStore's signature; openInitStore takes it as a
// parameter so its tests can count opens, commits and closes.
type initStoreOpener func(context.Context, *dolt.Config) (storage.DoltStorage, error)

// initReconcileCommitMessage labels the one commit openInitStore makes.
const initReconcileCommitMessage = "bd init: commit pre-existing working set before schema migration (gastownhall/beads#6089)"

// openInitStore opens bd init's store. When the strict open is refused by the
// pending-migration dirty-table guard (gastownhall/beads#4566) on a database
// that guard read as unversioned, it adopts the database the way the
// refusal's own remedy does (gastownhall/beads#6089): an older beads SDK —
// Gas Town's EnsureRigIssuePrefix, for one — can leave config, routes and
// interactions uncommitted on a database that has no migration cursor yet,
// and no later bd command could open the store to run `bd dolt commit`.
//
// The sequence is fixed, with no loop: a lenient open (Config.LenientOpen,
// the same open `bd dolt commit` uses), one CommitAll of the whole working
// set, a close of that handle, and one more strict open whose result is
// returned unchanged. CommitAll commits every committable row, user rows
// included, exactly as the manual remedy does; running init against the
// database is the intent to adopt it. Like the manual remedy, it is not
// atomic with the refusal it answers.
func openInitStore(ctx context.Context, cfg *dolt.Config, open initStoreOpener) (storage.DoltStorage, error) {
	store, err := open(ctx, cfg)
	if err == nil {
		return store, nil
	}
	var dirtyErr *schema.DirtyTablesError
	if !errors.As(err, &dirtyErr) {
		return store, err
	}

	// A copy, so the caller's strict intent survives for the final open.
	lenientCfg := *cfg
	lenientCfg.LenientOpen = true
	lenient, lerr := open(ctx, &lenientCfg)
	if lerr != nil {
		return nil, initReconcileError(err, lerr)
	}
	// CommitAll already reports an empty working set as (false, nil), so any
	// error stops the recovery. The close error is ignored, as at init's
	// other store closes.
	_, cerr := lenient.CommitAll(ctx, initReconcileCommitMessage)
	_ = lenient.Close()
	if cerr != nil {
		return nil, initReconcileError(err, cerr)
	}

	return open(ctx, cfg)
}

// initReconcileError reports a failed adoption, keeping both the original
// refusal and the recovery failure reachable through errors.As/Is.
func initReconcileError(refusal, recovery error) error {
	return fmt.Errorf("%w; bd init could not commit the pre-existing working set: %w", refusal, recovery)
}
