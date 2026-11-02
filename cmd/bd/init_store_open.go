package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

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
// interactions uncommitted on a database that has no migration cursor yet.
// bd init refused that database and wrote no workspace configuration, so
// `gt install` could not finish until someone ran `bd dolt commit` from a
// workspace that reaches the database and then ran `bd init` again.
//
// The sequence is fixed, with no loop: a lenient open (Config.LenientOpen,
// the same open `bd dolt commit` uses), one CommitAll of the whole working
// set, a close of that handle, and one more strict open whose result is
// returned unchanged. When the commit committed something and that final
// open succeeds, one note goes to notes (nil, under --quiet, for none). The
// note says only what init committed: the adopted database can still lack a
// project identity, which init's identity check reports on its own.
// CommitAll commits every committable row, user rows included, exactly as
// the manual remedy does; running init against the database is the intent
// to adopt it. Like the manual remedy, it is not atomic with the refusal it
// answers.
func openInitStore(ctx context.Context, cfg *dolt.Config, open initStoreOpener, notes io.Writer) (storage.DoltStorage, error) {
	store, err := open(ctx, cfg)
	if err == nil {
		return store, nil
	}
	dirtyErr := initStoreAdoptable(cfg, err)
	if dirtyErr == nil {
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
	committed, cerr := lenient.CommitAll(ctx, initReconcileCommitMessage)
	_ = lenient.Close()
	if cerr != nil {
		return nil, initReconcileError(err, cerr)
	}

	store, err = open(ctx, cfg)
	if err == nil && committed && notes != nil {
		fmt.Fprintf(notes, "Note: database %q had uncommitted changes in tables the schema migrations alter (%s); bd init committed the working set in its own commit, then applied the migrations (gastownhall/beads#6089)\n",
			cfg.Database, strings.Join(dirtyErr.Tables, ", "))
	}
	return store, err
}

// initStoreNotes is where openInitStore's note goes: stderr, or nowhere
// under --quiet.
func initStoreNotes(quiet bool) io.Writer {
	if quiet {
		return nil
	}
	return os.Stderr
}

// initStoreAdoptable reports whether openInitStore may adopt the database
// behind a refused strict open: only a writable, direct server open (no
// embedded, proxied, gateway, read-only or preview intent) refused by the
// pending-migration dirty guard at main cursor 0. A versioned database keeps
// the #4566 guard's protection of its dirty rows; its remedy stays the
// operator's explicit `bd dolt commit`. It returns the refusal when it is
// adoptable and nil otherwise.
func initStoreAdoptable(cfg *dolt.Config, err error) *schema.DirtyTablesError {
	if !cfg.ServerMode || cfg.ProxiedServer || cfg.Gateway || cfg.ReadOnly || cfg.Preview {
		return nil
	}
	var dirtyErr *schema.DirtyTablesError
	if !errors.As(err, &dirtyErr) || !dirtyErr.Unversioned {
		return nil
	}
	return dirtyErr
}

// initReconcileError reports a failed adoption, keeping both the original
// refusal and the recovery failure reachable through errors.As/Is.
func initReconcileError(refusal, recovery error) error {
	return fmt.Errorf("%w; bd init could not commit the pre-existing working set: %w", refusal, recovery)
}
