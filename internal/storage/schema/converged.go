package schema

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/storage/dberrors"
)

// alreadyConverged reports whether databaseName is already at this binary's
// target schema in every respect MigrateUp checks before its no-work
// short-circuit: no migration work is pending, the canonical dolt_ignore
// patterns are all present, and nobody is holding the migration lock. When it
// says yes, a MigrateUp pass would acquire the migration lock, do nothing,
// and return (0, nil).
//
// Why it exists: the whole schema-init probe ran INSIDE the database-scoped
// GET_LOCK, so every bd invocation against a shared Dolt server serialized on
// it even though the probe is a no-op in the steady state. Measured on an
// 18-seat rig, the lock was held 96.7% of a 14s sampling window, held runs had
// a 673ms median and a 4.2s max, and free gaps had a 0ms median — handed
// straight from holder to holder. That queue was 0.4-2.4s of pure waiting on
// every claim, heartbeat, list and comment. The statements under the lock are
// individually cheap; the SERIALIZATION is the cost, so batching them does not
// help and answering the steady-state question WITHOUT the lock does.
//
// It is deliberately advisory and fails closed onto the locked path. Any
// error, any unreadable state, and anything short of provably converged
// returns false, and the caller then behaves exactly as it did before this
// check existed. A false negative costs one ordinary locked pass; false
// positives are avoided by evaluating the same predicates MigrateUp itself
// evaluates, on the same pinned session, with no writes of its own.
func alreadyConverged(ctx context.Context, db DBConn, databaseName string, selector DatabaseSelector) (bool, error) {
	if databaseName == "" {
		return false, nil
	}

	// Put the session on the target database first. The hot path — the proxied
	// CLI open in uow.openAndInitSchema — pins its schema-init pool with an
	// EMPTY DSN database and only USEs the database after GET_LOCK, so a probe
	// that merely ASKED whether the session was already on databaseName read
	// NULL from DATABASE() and declined on every single invocation: the fast
	// path never fired where it was needed.
	onTarget, qualifier, err := selectTargetDatabase(ctx, db, databaseName, selector)
	if err != nil {
		return false, err
	}
	if !onTarget {
		return false, nil
	}

	// Exactly MigrateUp's own gate, and it runs first: on a fresh or
	// mid-upgrade database it reports work needed from the cursor probe alone,
	// before any statement that could fail against a missing table.
	needed, err := migrationWorkNeeded(ctx, db)
	if err != nil {
		return false, fmt.Errorf("checking schema migration work: %w", err)
	}
	if needed {
		return false, nil
	}

	// MigrateUp re-asserts the canonical dolt_ignore patterns ahead of that
	// gate precisely because an out-of-band-materialized database can arrive
	// with its cursors at-latest and the patterns missing. Read the same
	// question instead of writing it: an under-seeded database is not
	// converged and must take the locked path, which heals and commits it.
	//
	// migrationWorkNeeded has just proved mainSource.atLatest, i.e. the main
	// cursor is at or past mainSource.latest(); every version-gated pattern's
	// flip migration is part of that embedded set, so the gate can be
	// evaluated against latest() with no second cursor read.
	seeded, err := doltIgnoreSeeded(ctx, db, qualifier, mainSource.latest())
	if err != nil {
		return false, err
	}
	if !seeded {
		return false, nil
	}

	// LAST, and deliberately so: everything above is the cheap steady-state
	// question and short-circuits first. This term closes the window where a
	// peer is midway through a real migration pass. migrationWorkNeeded only
	// covers the FRONT half of MigrateUp — once the version cursors and
	// content_hash columns have landed it reports "nothing pending" while the
	// pass is still running backfills, dependency/aux PK rekeys, the ignored
	// series, and the schema commit. A lock-free prober in that window would
	// otherwise conclude "converged" and hand its caller a database whose
	// tables are being rewritten underneath it. If anyone holds the migration
	// lock we are not entitled to that conclusion: fail closed into the locked
	// path and wait our turn, exactly as every caller did before the fast path
	// existed.
	return migrationLockFree(ctx, db, MigrationLockName(databaseName))
}

// selectTargetDatabase puts the pinned session on databaseName, reporting
// whether it is now provably there and — when a selector had to be used to get
// there — the identifier-quoted name to schema-qualify later reads with.
//
// A session already on databaseName needs nothing: it is provably on the
// target because DATABASE() was just read and matched, and later reads may
// therefore run unqualified against it. An empty qualifier means exactly that.
//
// Without a selector the probe may not issue USE at all, so a session on any
// other database (or on none) is declined. That is the whole behavior for
// callers whose pool DSN already names the database (internal/storage/dolt),
// and it keeps this package free of the DDL repository it would otherwise need
// (see DatabaseSelector for why that import cannot exist).
//
// A database that does not exist yet is a fresh bootstrap: report false so the
// caller falls through to the locked path, whose CREATE DATABASE arbitrates
// creation and issues the #5012 fresh-bootstrap heal capability.
//
// Selecting is also what makes skipping a caller's locked bootstrap
// preparation safe: preparation creates the database and USEs it, and both
// have provably happened here — the database existed before we touched it (so
// preparation's bare CREATE DATABASE could only have failed with "database
// exists" and captured no heal authority), and the selector issues the same
// USE preparation would.
//
// The existence probe is not decoration. A Dolt session that issues a FAILING
// statement stays pinned to its pre-statement catalog snapshot, so a USE of a
// not-yet-created database would poison this pooled connection for the rest of
// its life (be-bv7x). Probe with a query that always succeeds, then act.
func selectTargetDatabase(ctx context.Context, db DBConn, databaseName string, selector DatabaseSelector) (bool, string, error) {
	var current sql.NullString
	if err := db.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&current); err != nil {
		return false, "", fmt.Errorf("reading current database: %w", err)
	}
	if current.Valid && current.String == databaseName {
		return true, "", nil
	}
	if selector == nil {
		return false, "", nil
	}

	var exists int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = ?",
		databaseName,
	).Scan(&exists); err != nil {
		return false, "", fmt.Errorf("probing database %q existence: %w", databaseName, err)
	}
	if exists == 0 {
		return false, "", nil
	}

	quoted, err := selector(ctx, db, databaseName)
	if err != nil {
		return false, "", fmt.Errorf("selecting database %q: %w", databaseName, err)
	}
	if quoted == "" {
		return false, "", fmt.Errorf("selecting database %q: selector returned no quoted name", databaseName)
	}
	return true, quoted, nil
}

// migrationLockFree reports whether the database-scoped migration lock is
// currently unheld. IS_FREE_LOCK is a read: it never queues, never acquires,
// and costs one round trip, which is the entire point — the fast path exists
// to stop paying GET_LOCK's queue.
//
// A NULL answer means the server would not tell us, which is not the same as
// "free": fail closed.
func migrationLockFree(ctx context.Context, db DBConn, lockName string) (bool, error) {
	var free sql.NullInt64
	if err := db.QueryRowContext(ctx, "SELECT IS_FREE_LOCK(?)", lockName).Scan(&free); err != nil {
		return false, fmt.Errorf("probing migration lock %q: %w", lockName, err)
	}
	if !free.Valid {
		return false, nil
	}
	return free.Int64 == 1, nil
}

// doltIgnoreSeeded reports whether every canonical dolt_ignore pattern
// seedDoltIgnorePatterns would assert is already present. It is the read-only
// counterpart of that seed and shares its version gate: a pattern whose flip
// migration has not been reached yet is not expected, exactly as the seed
// would not insert it. mainVersionAtLeast is a lower bound on the main cursor
// that the caller has already established, so this read costs one round trip
// rather than three.
//
// Presence is judged on the pattern alone, never on its ignored value, because
// INSERT IGNORE would leave an explicit operator override (a pattern recorded
// with ignored=false) untouched. Reporting such a row as missing would send
// every invocation down the locked path forever.
//
// qualifier is the identifier-quoted database name selectTargetDatabase
// returned, or empty when the session was already on the target database and
// nothing needed selecting. dolt_ignore is a Dolt system table: it is NOT
// listed in information_schema.tables (verified against a live dolt sql-server
// — the cursor-table existence probe pattern from migrationSource.currentVersion
// would report it absent and disable the fast path permanently), so this read
// cannot get the full be-bv7x probe-before-act treatment at table granularity.
// What it has instead: selectTargetDatabase has already proved this session is
// on the target database (by reading DATABASE(), or by probing
// information_schema.schemata and selecting it), Dolt materializes dolt_ignore
// on every real database (live-verified), the name is stated explicitly
// whenever one was quoted for us rather than re-derived here, and
// IsTableNotExist below fails closed onto the locked path should that
// materialization assumption break.
func doltIgnoreSeeded(ctx context.Context, db DBConn, qualifier string, mainVersionAtLeast int) (bool, error) {
	table := "dolt_ignore"
	if qualifier != "" {
		table = qualifier + ".dolt_ignore"
	}
	rows, err := db.QueryContext(ctx, "SELECT pattern FROM "+table)
	if err != nil {
		if dberrors.IsTableNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("reading dolt_ignore: %w", err)
	}
	defer rows.Close()

	present := make(map[string]struct{})
	for rows.Next() {
		var pattern string
		if err := rows.Scan(&pattern); err != nil {
			return false, fmt.Errorf("reading dolt_ignore: %w", err)
		}
		present[pattern] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("reading dolt_ignore: %w", err)
	}

	for _, pattern := range doltIgnorePatterns {
		if _, ok := present[pattern]; !ok {
			return false, nil
		}
	}
	for _, gated := range versionGatedDoltIgnorePatterns {
		if mainVersionAtLeast < gated.minMainVersion {
			continue
		}
		if _, ok := present[gated.pattern]; !ok {
			return false, nil
		}
	}
	return true, nil
}
