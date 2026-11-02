package schema

import "strings"

// DirtyTablesError is returned by MigrateUp when pending schema migrations
// would alter tables that already have uncommitted changes in the working
// set. The guard protects dirty user data from being entangled with a
// migration: committing a migration's DDL/DML together with unrelated
// uncommitted rows in the same table makes the two impossible to separate
// later.
//
// The documented recovery is "bd dolt commit" (or "bd vc commit"), which
// commits the working set at the current schema so the migration can then
// run cleanly. But those commit commands also open the store, and an open
// runs MigrateUp - hitting this same guard before the commit that would
// clear the dirty state ever gets a chance to run (gastownhall/beads#4566).
// To break that deadlock, working-set-reconcile opens detect this error type
// at open time via errors.As and skip the migration instead of failing the
// open, so the commit can proceed and clear the working set. Both storage
// modes do this — embeddeddolt.OpenForWorkingSetReconcile and server mode's
// dolt.Config.LenientOpen — so the recovery this message names is reachable
// wherever the guard fires.
type DirtyTablesError struct {
	Tables []string

	// Unversioned reports that the main-source migration cursor was 0 —
	// absent or empty — when the guard refused. It means exactly that: the
	// pending main migrations would alter pre-existing dirty tables on a
	// database that has no recorded main migration history. It does NOT
	// mean the database is empty, free of user data, was created by this
	// process, or was never migrated anywhere in its history: a deleted or
	// never-written cursor cannot prove any of that. The legacy-SDK
	// adoption path (bd init on an unversioned server database,
	// gastownhall/beads#6089) uses this to distinguish "a seeder left
	// bootstrap residue on a database no migration ever touched" — whose
	// whole working set an explicit init may commit — from an ordinary
	// versioned dirty database, which every caller must keep refusing.
	//
	// Set only by the main-source pending-migration guard, from the
	// recorded cursor it read before the pass. Future main-source cursor
	// clamping (disbelieving a contradicted cursor, the way readCursor's
	// sentinel floors do) must preserve this field's meaning — it
	// classifies the RECORDED history, so a clamped cursor belongs in a
	// separate signal, not in this one.
	Unversioned bool
}

func (e *DirtyTablesError) Error() string {
	return "pending schema migrations alter pre-existing dirty tables: " + strings.Join(e.Tables, ", ") +
		"; run 'bd dolt commit' to commit the working set at the current schema, then re-run the migration (gastownhall/beads#4566)"
}
