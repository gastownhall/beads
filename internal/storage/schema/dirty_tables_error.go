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
	// Unversioned reports that the refusing guard was the pending-migration
	// guard and that it read main migration cursor 0 (no schema_migrations
	// table, or an empty one) in this database. It says only what the cursor
	// read: not that the database is empty, holds no user data, was created
	// by this process, or was never migrated by any binary. bd init uses it
	// to decide whether a database predates the migration cursor and so may
	// be adopted by committing its working set (gastownhall/beads#6089).
	// Every other refusal, the dependency re-key's included, leaves it false.
	// A future clamp of the main cursor must keep this the recorded cursor.
	Unversioned bool
}

func (e *DirtyTablesError) Error() string {
	return "pending schema migrations alter pre-existing dirty tables: " + strings.Join(e.Tables, ", ") +
		"; run 'bd dolt commit' to commit the working set at the current schema, then re-run the migration (gastownhall/beads#4566)"
}
