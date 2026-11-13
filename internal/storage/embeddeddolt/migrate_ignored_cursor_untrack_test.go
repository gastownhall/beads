//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/dberrors"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// These tests cover gastownhall/beads#4356 against the real engine: a database
// whose ignored_schema_migrations table is COMMITTED at HEAD.
//
// dolt_ignore only exempts tables that were never committed, so on such a
// database the ignore pattern bd re-asserts at every open is inert. Every
// staging path anti-joins dolt_ignore, so the table can be dirtied but never
// committed, and Dolt's merge preflight refuses to start while a tracked table
// is dirty — push keeps working and every pull dies with Error 1105, forever.
//
// The fixture recipe is DOLT_ADD('-f') + DOLT_COMMIT, the same one
// internal/storage/dolt/aux_row_id_rekey_test.go uses to force an ignored
// table onto the versioned plane, and the state 0062's header notes that
// branch-per-test databases materialize on their own.

const untrackScratchTable = "__temp__ignored_schema_migrations_untrack"

// legacyTrackedFixture is a store whose cursor table has been committed into
// HEAD, closed and ready to be reopened through the code path under test. The
// store must be closed to reopen it, so the fixture hands back the paths
// rather than a live store.
type legacyTrackedFixture struct {
	beadsDir string
	dataDir  string
	database string
	// cursorRows is the cursor's contents before any heal ran, captured as
	// "version|content_hash" so the assertion is about the rows themselves and
	// not merely their number.
	cursorRows []string
	commits    int
}

// newLegacyTrackedFixture builds a fully migrated database and then commits
// its ignored-lane cursor table into HEAD, which is the shape upgraded
// lineages arrive in.
func newLegacyTrackedFixture(t *testing.T, database string) *legacyTrackedFixture {
	t.Helper()
	ctx := t.Context()

	pristine := newPristineEmbeddedDoltFixture(t, database)
	closeEmbeddedDoltStore(t, pristine.store)

	fixture := &legacyTrackedFixture{
		beadsDir: pristine.beadsDir,
		dataDir:  pristine.dataDir,
		database: database,
	}
	fixture.withRawConn(t, func(conn *sql.Conn) {
		mustExecOn(t, ctx, conn, "CALL DOLT_ADD('-f', 'ignored_schema_migrations')")
		mustExecOn(t, ctx, conn, "CALL DOLT_COMMIT('-m', 'legacy tracked baseline')")
		if !trackedAtHead(t, ctx, conn, "ignored_schema_migrations") {
			t.Fatal("fixture did not commit ignored_schema_migrations into HEAD; the wedge cannot be reproduced")
		}
		fixture.cursorRows = cursorRows(t, ctx, conn)
		fixture.commits = commitCount(t, ctx, conn)
	})
	if len(fixture.cursorRows) == 0 {
		t.Fatal("fixture captured no cursor rows; a heal that dropped them all would pass vacuously")
	}
	return fixture
}

// withRawConn runs fn on a pinned connection to the fixture's database. It
// opens and closes its own engine, so it must not be called while a store is
// open on the same directory: embedded working-set writes are not reliably
// visible across handles until each is closed.
func (f *legacyTrackedFixture) withRawConn(t *testing.T, fn func(conn *sql.Conn)) {
	t.Helper()
	withRawConn(t, f.dataDir, f.database, fn)
}

func withRawConn(t *testing.T, dataDir, database string, fn func(conn *sql.Conn)) {
	t.Helper()
	ctx := t.Context()
	pool, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, database, "")
	if err != nil {
		t.Fatalf("OpenSQL(%s): %v", dataDir, err)
	}
	defer func() { _ = cleanup() }()
	conn, err := pool.Conn(ctx)
	if err != nil {
		t.Fatalf("pin connection: %v", err)
	}
	defer func() { _ = conn.Close() }()
	fn(conn)
}

// reopen runs the code under test: a writable open, which is what calls
// schema.MigrateUp and therefore the reconcile.
func (f *legacyTrackedFixture) reopen(t *testing.T) *embeddeddolt.EmbeddedDoltStore {
	t.Helper()
	store, err := embeddeddolt.Open(t.Context(), f.beadsDir, f.database, "main")
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	return store
}

// reopenAndClose is the whole heal, observed from the outside: open the store
// (which migrates), then close it so the assertions can use a raw connection.
func (f *legacyTrackedFixture) reopenAndClose(t *testing.T) {
	t.Helper()
	closeEmbeddedDoltStore(t, f.reopen(t))
}

func mustExecOn(t *testing.T, ctx context.Context, conn *sql.Conn, stmt string, args ...any) {
	t.Helper()
	if _, err := conn.ExecContext(ctx, stmt, args...); err != nil {
		t.Fatalf("exec %q: %v", stmt, err)
	}
}

// trackedAtHead answers the question the whole fix turns on, using the
// spelling the field diagnosed with: a table that is not committed at HEAD
// cannot be selected AS OF 'HEAD' at all. (Production uses the always-
// succeeding SHOW TABLES listing instead, because a probe that fails on every
// healthy database would pin the pooled Dolt session to a stale catalog
// snapshot — be-bv7x. Both are asserted here, and they must agree.)
func trackedAtHead(t *testing.T, ctx context.Context, conn *sql.Conn, table string) bool {
	t.Helper()

	var n int
	//nolint:gosec // G201: test-local table names.
	selectErr := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" AS OF 'HEAD'").Scan(&n)
	if selectErr != nil && !dberrors.IsTableNotExist(selectErr) {
		t.Fatalf("probing %s AS OF HEAD: %v", table, selectErr)
	}

	rows, err := conn.QueryContext(ctx, "SHOW TABLES AS OF 'HEAD'")
	if err != nil {
		t.Fatalf("listing tables at HEAD: %v", err)
	}
	defer func() { _ = rows.Close() }()
	listed := false
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("listing tables at HEAD: %v", err)
		}
		if strings.EqualFold(name, table) {
			listed = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("listing tables at HEAD: %v", err)
	}

	if listed != (selectErr == nil) {
		t.Fatalf("the two tracked-at-HEAD probes disagree for %s: listing=%v, AS OF error=%v",
			table, listed, selectErr)
	}
	return listed
}

func cursorRows(t *testing.T, ctx context.Context, conn *sql.Conn) []string {
	t.Helper()
	rows, err := conn.QueryContext(ctx,
		"SELECT version, IFNULL(content_hash, '') FROM ignored_schema_migrations ORDER BY version")
	if err != nil {
		t.Fatalf("reading cursor rows: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var version int
		var hash string
		if err := rows.Scan(&version, &hash); err != nil {
			t.Fatalf("reading cursor rows: %v", err)
		}
		out = append(out, strconv.Itoa(version)+"|"+hash)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading cursor rows: %v", err)
	}
	return out
}

func commitCount(t *testing.T, ctx context.Context, conn *sql.Conn) int {
	t.Helper()
	var n int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_log").Scan(&n); err != nil {
		t.Fatalf("counting commits: %v", err)
	}
	return n
}

func untrackCommitCount(t *testing.T, ctx context.Context, conn *sql.Conn) int {
	t.Helper()
	var n int
	if err := conn.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dolt_log WHERE message LIKE '%4356%'").Scan(&n); err != nil {
		t.Fatalf("counting untrack commits: %v", err)
	}
	return n
}

func tablePresent(t *testing.T, ctx context.Context, conn *sql.Conn, table string) bool {
	t.Helper()
	var n int
	if err := conn.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?",
		table).Scan(&n); err != nil {
		t.Fatalf("probing %s: %v", table, err)
	}
	return n > 0
}

// assertHealed is the post-condition every path in this file shares: the
// cursor table is back to being an untracked ADD delta with all its rows, the
// scratch table is gone, and the working set is clean — which is exactly what
// makes a pull possible again.
func assertHealed(t *testing.T, f *legacyTrackedFixture, wantRows []string) {
	t.Helper()
	ctx := t.Context()
	f.withRawConn(t, func(conn *sql.Conn) {
		if trackedAtHead(t, ctx, conn, "ignored_schema_migrations") {
			t.Error("ignored_schema_migrations is still committed at HEAD; dolt_ignore stays inert and pulls stay wedged")
		}
		if !tablePresent(t, ctx, conn, "ignored_schema_migrations") {
			t.Fatal("ignored_schema_migrations is gone from the working set; the ignored migration series would replay from zero")
		}
		got := cursorRows(t, ctx, conn)
		if len(got) != len(wantRows) {
			t.Fatalf("cursor rows after heal = %d, want %d (rows must survive the untrack)", len(got), len(wantRows))
		}
		for i := range got {
			if got[i] != wantRows[i] {
				t.Fatalf("cursor row %d after heal = %q, want %q", i, got[i], wantRows[i])
			}
		}
		if tablePresent(t, ctx, conn, untrackScratchTable) {
			t.Errorf("%s survived the heal", untrackScratchTable)
		}
		if dirty := dirtyTableNames(t, ctx, conn); len(dirty) > 0 {
			t.Errorf("working set is dirty after the heal: %v — a tracked dirty table is the wedge itself", dirty)
		}
		if n := untrackCommitCount(t, ctx, conn); n != 1 {
			t.Errorf("commits referencing #4356 = %d, want exactly 1", n)
		}
	})
}

// TestLegacyTrackedIgnoredCursorHealsOnOpen is the core claim: a database in
// the legacy shape repairs itself on the next writable open, losing nothing.
func TestLegacyTrackedIgnoredCursorHealsOnOpen(t *testing.T) {
	f := newLegacyTrackedFixture(t, "healopen")

	f.reopenAndClose(t)

	assertHealed(t, f, f.cursorRows)
	f.withRawConn(t, func(conn *sql.Conn) {
		if got := commitCount(t, t.Context(), conn); got != f.commits+1 {
			t.Errorf("commits after heal = %d, want %d (the untrack must be one scoped commit)", got, f.commits+1)
		}
	})
}

// The reconcile runs at EVERY open, which is the property a one-shot migration
// could not have — so it has to cost nothing once the database is healthy. A
// second open must not commit again, and '--skip-empty' plus the gate are what
// keep it from churning empty commits into everyone's history.
func TestIgnoredCursorHealIsIdempotent(t *testing.T) {
	f := newLegacyTrackedFixture(t, "healidem")

	f.reopenAndClose(t)
	var afterFirst int
	f.withRawConn(t, func(conn *sql.Conn) {
		afterFirst = commitCount(t, t.Context(), conn)
	})

	f.reopenAndClose(t)
	f.reopenAndClose(t)

	assertHealed(t, f, f.cursorRows)
	f.withRawConn(t, func(conn *sql.Conn) {
		if got := commitCount(t, t.Context(), conn); got != afterFirst {
			t.Errorf("commits after two more opens = %d, want %d (a healthy database must produce no commit at all)", got, afterFirst)
		}
	})
}

// An operator who put the cursor table back on the versioned plane on purpose
// (a dolt_ignore row with ignored=0) has made a choice the reconcile must not
// overrule, exactly as seedDoltIgnorePatterns leaves that override alone.
func TestIgnoredCursorHealRespectsOperatorOverride(t *testing.T) {
	f := newLegacyTrackedFixture(t, "healoverride")
	ctx := t.Context()

	f.withRawConn(t, func(conn *sql.Conn) {
		mustExecOn(t, ctx, conn, "REPLACE INTO dolt_ignore VALUES ('ignored_schema_migrations', false)")
		mustExecOn(t, ctx, conn, "CALL DOLT_COMMIT('-Am', 'operator: version the cursor table on purpose')")
	})

	f.reopenAndClose(t)

	f.withRawConn(t, func(conn *sql.Conn) {
		if !trackedAtHead(t, ctx, conn, "ignored_schema_migrations") {
			t.Error("the reconcile untracked a table the operator had explicitly un-ignored")
		}
		if n := untrackCommitCount(t, ctx, conn); n != 0 {
			t.Errorf("commits referencing #4356 = %d, want 0 under an operator override", n)
		}
	})
}

// TestLegacyTrackedIgnoredCursorUnwedgesPull is the reported bug, end to end,
// over a real file:// remote: two clones of one legacy lineage, the wedge
// reproduced as the exact Error 1105 the issue reports, and both clones
// converging after each heals. The peer's untrack and this clone's untrack
// delete the same table, so the merge is a delete/delete — clean by
// construction, which is what makes the fix safe to roll out one machine at a
// time.
func TestLegacyTrackedIgnoredCursorUnwedgesPull(t *testing.T) {
	ctx := t.Context()
	f := newLegacyTrackedFixture(t, "healpull")

	remoteDir := filepath.Join(t.TempDir(), "remote")
	if err := os.MkdirAll(remoteDir, 0o700); err != nil {
		t.Fatalf("create remote dir: %v", err)
	}
	remoteURL := "file://" + remoteDir

	f.withRawConn(t, func(conn *sql.Conn) {
		mustExecOn(t, ctx, conn, "CALL DOLT_REMOTE('add', 'origin', ?)", remoteURL)
		// Push works on a wedged database — that asymmetry ("push works, pull
		// never does") is how the field recognizes this bug.
		mustExecOn(t, ctx, conn, "CALL DOLT_PUSH('origin', 'main')")
	})

	// The peer clone inherits the tracked cursor table, heals on its own first
	// writable open, and publishes both the untrack and a data change.
	peerBeads := filepath.Join(t.TempDir(), "peer")
	peerData := filepath.Join(peerBeads, "embeddeddolt")
	if err := os.MkdirAll(peerData, 0o700); err != nil {
		t.Fatalf("create peer data dir: %v", err)
	}
	withRawConn(t, peerData, "", func(conn *sql.Conn) {
		mustExecOn(t, ctx, conn, "CALL DOLT_CLONE(?, ?)", remoteURL, f.database)
	})
	peer, err := embeddeddolt.Open(ctx, peerBeads, f.database, "main")
	if err != nil {
		t.Fatalf("open peer clone: %v", err)
	}
	if err := peer.SetConfig(ctx, "peer.marker", "from the peer"); err != nil {
		t.Fatalf("peer SetConfig: %v", err)
	}
	if err := peer.Commit(ctx, "peer: add a marker"); err != nil {
		t.Fatalf("peer commit: %v", err)
	}
	if err := peer.Push(ctx); err != nil {
		t.Fatalf("peer push: %v", err)
	}
	closeEmbeddedDoltStore(t, peer)
	withRawConn(t, peerData, f.database, func(conn *sql.Conn) {
		if trackedAtHead(t, ctx, conn, "ignored_schema_migrations") {
			t.Fatal("the peer clone did not heal on open; it would keep re-tracking the table for the whole fleet")
		}
	})

	// This clone is still in the legacy shape. Dirty the cursor table the way
	// any ignored-lane migration pass does, and the pull is dead.
	f.withRawConn(t, func(conn *sql.Conn) {
		mustExecOn(t, ctx, conn,
			"INSERT IGNORE INTO ignored_schema_migrations (version, content_hash) VALUES (99999, NULL)")
		if dirty := dirtyTableNames(t, ctx, conn); len(dirty) != 1 || dirty[0] != "ignored_schema_migrations" {
			t.Fatalf("dolt_status = %v, want exactly [ignored_schema_migrations]: the wedge precondition is a DIRTY TRACKED cursor table", dirty)
		}
		_, err := conn.ExecContext(ctx, "CALL DOLT_PULL('origin', 'main')")
		if err == nil {
			t.Fatal("pull succeeded on a tracked+dirty cursor table; the wedge is not reproduced and this test proves nothing")
		}
		if !strings.Contains(err.Error(), "cannot merge with uncommitted changes") {
			t.Fatalf("pre-heal pull error = %v, want Dolt's uncommitted-changes refusal (gastownhall/beads#4356)", err)
		}
	})

	// Heal, and the same pull goes through.
	store := f.reopen(t)
	if err := store.Pull(ctx); err != nil {
		closeEmbeddedDoltStore(t, store)
		t.Fatalf("post-heal pull failed: %v", err)
	}
	closeEmbeddedDoltStore(t, store)

	f.withRawConn(t, func(conn *sql.Conn) {
		if trackedAtHead(t, ctx, conn, "ignored_schema_migrations") {
			t.Error("the cursor table is tracked again after the pull; the fleet has not converged")
		}
		var marker string
		if err := conn.QueryRowContext(ctx,
			"SELECT value FROM config WHERE `key` = 'peer.marker'").Scan(&marker); err != nil {
			t.Fatalf("reading the merged peer row: %v", err)
		}
		if marker != "from the peer" {
			t.Errorf("merged peer value = %q, want %q", marker, "from the peer")
		}
		var kept int
		if err := conn.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM ignored_schema_migrations WHERE version = 99999").Scan(&kept); err != nil {
			t.Fatalf("reading the cursor row written before the heal: %v", err)
		}
		if kept != 1 {
			t.Error("the cursor row written before the heal did not survive it")
		}
		if got := len(cursorRows(t, ctx, conn)); got != len(f.cursorRows)+1 {
			t.Errorf("cursor rows after heal+pull = %d, want %d", got, len(f.cursorRows)+1)
		}
	})
}

// TestIgnoredCursorHealConvergesFromEveryCrashState walks the states that can
// persist if the process dies mid-repair. Each is manufactured with the same
// raw SQL the repair itself issues, and each must converge in ONE reopen —
// that is what lets the repair be fatal past the drop instead of having to be
// transactional, which Dolt cannot give it.
func TestIgnoredCursorHealConvergesFromEveryCrashState(t *testing.T) {
	for i, tt := range []struct {
		name string
		// crash runs against a legacy-tracked database and leaves it in the
		// state its name describes.
		crash func(t *testing.T, ctx context.Context, conn *sql.Conn)
	}{
		{
			// Died after the backup, before the drop. HEAD still carries the
			// table and so does the working set; the scratch table is a
			// duplicate. Phase A simply re-runs.
			name: "backed up, not yet dropped",
			crash: func(t *testing.T, ctx context.Context, conn *sql.Conn) {
				backupCursorRows(t, ctx, conn)
			},
		},
		{
			// Died between the drop and its commit. HEAD still carries the
			// table, the working set does not — so the re-run's copy has no
			// source and must treat that as "already saved" rather than as a
			// failure, or the pre-mutation policy swallows it and the database
			// never converges.
			name: "dropped, drop not committed",
			crash: func(t *testing.T, ctx context.Context, conn *sql.Conn) {
				backupCursorRows(t, ctx, conn)
				mustExecOn(t, ctx, conn, "DROP TABLE IF EXISTS ignored_schema_migrations")
			},
		},
		{
			// Died after the drop was committed, before the recreate. The
			// table now reads as untracked — i.e. healthy — so only the
			// surviving scratch table can tell the reconcile there is a cursor
			// to put back.
			name: "drop committed, rows not restored",
			crash: func(t *testing.T, ctx context.Context, conn *sql.Conn) {
				backupCursorRows(t, ctx, conn)
				mustExecOn(t, ctx, conn, "DROP TABLE IF EXISTS ignored_schema_migrations")
				mustExecOn(t, ctx, conn, "CALL DOLT_ADD('-f', 'ignored_schema_migrations')")
				mustExecOn(t, ctx, conn, "CALL DOLT_COMMIT('--skip-empty', '-m', 'schema: untrack legacy ignored_schema_migrations so dolt_ignore can apply (gastownhall/beads#4356)')")
			},
		},
		{
			// Died after the rows were restored, before the scratch table was
			// dropped. Everything is already correct; the reconcile must clean
			// up without touching the rows.
			name: "restored, scratch table left behind",
			crash: func(t *testing.T, ctx context.Context, conn *sql.Conn) {
				backupCursorRows(t, ctx, conn)
				mustExecOn(t, ctx, conn, "DROP TABLE IF EXISTS ignored_schema_migrations")
				mustExecOn(t, ctx, conn, "CALL DOLT_ADD('-f', 'ignored_schema_migrations')")
				mustExecOn(t, ctx, conn, "CALL DOLT_COMMIT('--skip-empty', '-m', 'schema: untrack legacy ignored_schema_migrations so dolt_ignore can apply (gastownhall/beads#4356)')")
				restoreCursorFromScratch(t, ctx, conn)
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			f := newLegacyTrackedFixture(t, "crash"+strconv.Itoa(i))

			f.withRawConn(t, func(conn *sql.Conn) {
				tt.crash(t, ctx, conn)
			})

			f.reopenAndClose(t)

			assertHealed(t, f, f.cursorRows)
		})
	}
}

// backupCursorRows manufactures the repair's scratch table. It derives both
// the shape and the column list from the live cursor table (CREATE ... LIKE,
// SELECT *) rather than restating them: a hand-copied DDL here is a fourth
// place the cursor shape would have to be kept in sync, and the point of the
// crash matrix is to exercise the real shape, whatever it currently is.
func backupCursorRows(t *testing.T, ctx context.Context, conn *sql.Conn) {
	t.Helper()
	mustExecOn(t, ctx, conn, "CREATE TABLE IF NOT EXISTS "+untrackScratchTable+" LIKE ignored_schema_migrations")
	mustExecOn(t, ctx, conn, "INSERT IGNORE INTO "+untrackScratchTable+" SELECT * FROM ignored_schema_migrations")
}

// restoreCursorFromScratch recreates the cursor table from the scratch copy,
// the way the repair's own restore phase does.
func restoreCursorFromScratch(t *testing.T, ctx context.Context, conn *sql.Conn) {
	t.Helper()
	mustExecOn(t, ctx, conn, "CREATE TABLE IF NOT EXISTS ignored_schema_migrations LIKE "+untrackScratchTable)
	mustExecOn(t, ctx, conn, "INSERT IGNORE INTO ignored_schema_migrations SELECT * FROM "+untrackScratchTable)
}

// untrackCursorInPlace performs the repair by hand, leaving the scratch table
// behind — the "restored, scratch left behind" crash state.
func untrackCursorInPlace(t *testing.T, ctx context.Context, conn *sql.Conn) {
	t.Helper()
	backupCursorRows(t, ctx, conn)
	mustExecOn(t, ctx, conn, "DROP TABLE IF EXISTS ignored_schema_migrations")
	mustExecOn(t, ctx, conn, "CALL DOLT_ADD('-f', 'ignored_schema_migrations')")
	mustExecOn(t, ctx, conn, "CALL DOLT_COMMIT('--skip-empty', '-m', 'schema: untrack legacy ignored_schema_migrations so dolt_ignore can apply (gastownhall/beads#4356)')")
	restoreCursorFromScratch(t, ctx, conn)
}

// TestIgnoredCursorHealNeverResurrectsRolledBackVersions is the guard on the
// resume path's blast radius.
//
// The sanctioned recovery from a bad release is a cursor ROLLBACK: delete the
// affected rows from ignored_schema_migrations so the corrected migration
// series re-applies on the next open. A scratch table left behind by an
// interrupted repair holds a snapshot from BEFORE that rollback, so a resume
// that unioned it back into a live cursor table would restore exactly the
// versions the operator deleted — with their original applied_at — and the
// corrected migrations would be silently skipped forever. The reconcile must
// treat a live cursor table as the truth and the scratch as junk to remove.
func TestIgnoredCursorHealNeverResurrectsRolledBackVersions(t *testing.T) {
	ctx := t.Context()
	f := newLegacyTrackedFixture(t, "rollback")

	// A timestamp no migration pass could ever mint, so the row's provenance
	// after the reopen is unambiguous: this exact value means it came from the
	// stale backup, anything else means the migration genuinely re-applied.
	const staleApplied = "2000-01-01 00:00:00"

	var rolledBack int
	f.withRawConn(t, func(conn *sql.Conn) {
		// An interrupted repair: untracked and restored, scratch still there.
		untrackCursorInPlace(t, ctx, conn)
		// The operator then rolls the cursor back to re-apply a corrected
		// ignored migration. The scratch still remembers the deleted version.
		if err := conn.QueryRowContext(ctx,
			"SELECT MAX(version) FROM ignored_schema_migrations").Scan(&rolledBack); err != nil {
			t.Fatalf("reading the cursor high-water mark: %v", err)
		}
		mustExecOn(t, ctx, conn, "DELETE FROM ignored_schema_migrations WHERE version = ?", rolledBack)
		mustExecOn(t, ctx, conn,
			"UPDATE "+untrackScratchTable+" SET applied_at = ? WHERE version = ?", staleApplied, rolledBack)
		var stillInScratch int
		if err := conn.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM "+untrackScratchTable+" WHERE version = ?", rolledBack).Scan(&stillInScratch); err != nil {
			t.Fatalf("reading the scratch table: %v", err)
		}
		if stillInScratch != 1 {
			t.Fatalf("scratch table does not hold the rolled-back version %d; the hazard is not reproduced", rolledBack)
		}
	})

	f.reopenAndClose(t)

	f.withRawConn(t, func(conn *sql.Conn) {
		// The version is expected BACK — that is the whole point of a cursor
		// rollback. What must not happen is it coming back from the backup:
		// then the migration never re-ran, and the corrected body that the
		// rollback existed to apply is skipped forever while the cursor claims
		// it succeeded.
		var appliedAt string
		if err := conn.QueryRowContext(ctx,
			"SELECT applied_at FROM ignored_schema_migrations WHERE version = ?", rolledBack).Scan(&appliedAt); err != nil {
			t.Fatalf("reading the rolled-back cursor row after the reconcile: %v", err)
		}
		if strings.HasPrefix(appliedAt, "2000-01-01") {
			t.Errorf("cursor version %d carries the stale backup's applied_at (%s): the reconcile resurrected it instead of letting the corrected migration re-apply",
				rolledBack, appliedAt)
		}
		if tablePresent(t, ctx, conn, untrackScratchTable) {
			t.Errorf("%s survived; it is dolt_ignore'd under __temp__%%, so a straggler sits in the working set invisibly to dolt_status until something commits it", untrackScratchTable)
		}
		if dirty := dirtyTableNames(t, ctx, conn); len(dirty) > 0 {
			t.Errorf("working set is dirty after the reconcile: %v", dirty)
		}
	})
}

// The operator veto is re-read on the resume path, not inherited from whatever
// open crashed: someone who resolved a crashed repair by deliberately
// versioning the cursor table must not have stale rows pushed back into it.
// The scratch is still cleaned up — it is bd's own junk either way.
//
// This is also the file's only arm that puts the scratch table at HEAD, so it
// is the only one that reaches the sweep's forced-staging branch. The fixture
// has to force its own staging to get there: the '-Am' below used to sweep the
// scratch in, but "__temp__%" is seeded by the fixture's real MigrateUp, and a
// blanket commit's ignore filter is hardcoded on (no '-f' form of '-Am'
// exists). Without the explicit forced add the scratch would never reach HEAD,
// the branch would go unexercised, and the weaker "it is gone from the working
// set" assertion below would pass on the local DROP alone.
func TestIgnoredCursorHealResumeRespectsOperatorOverride(t *testing.T) {
	ctx := t.Context()
	f := newLegacyTrackedFixture(t, "resumeoverride")

	f.withRawConn(t, func(conn *sql.Conn) {
		untrackCursorInPlace(t, ctx, conn)
		mustExecOn(t, ctx, conn, "REPLACE INTO dolt_ignore VALUES ('ignored_schema_migrations', false)")
		mustExecOn(t, ctx, conn, "CALL DOLT_ADD('-f', ?)", untrackScratchTable)
		mustExecOn(t, ctx, conn, "CALL DOLT_COMMIT('-Am', 'operator: version the cursor table on purpose')")
		// The precondition the rest of this test rests on. Assert it, or a
		// future change to the staging above silently turns everything below
		// into a test of the ordinary untracked-scratch path.
		if !trackedAtHead(t, ctx, conn, untrackScratchTable) {
			t.Fatalf("%s is not committed at HEAD after the operator's blanket commit; the tracked-scratch sweep would not be exercised", untrackScratchTable)
		}
	})

	f.reopenAndClose(t)

	f.withRawConn(t, func(conn *sql.Conn) {
		if tablePresent(t, ctx, conn, untrackScratchTable) {
			t.Errorf("%s survived an override-suppressed resume; the cleanup is always correct", untrackScratchTable)
		}
		// Positive proof the sweep COMMIT landed, not just the local DROP:
		// nothing but a commit can take the table back out of HEAD. An
		// unforced stage makes the sweep a silent no-op that --skip-empty
		// reports as success, leaving the table at HEAD and a permanent
		// delete delta that wedges every later pull.
		if trackedAtHead(t, ctx, conn, untrackScratchTable) {
			t.Errorf("%s is still committed at HEAD; the sweep staged nothing and --skip-empty committed nothing", untrackScratchTable)
		}
		if dirty := dirtyTableNames(t, ctx, conn); len(dirty) > 0 {
			t.Errorf("working set is dirty after the override-suppressed resume: %v", dirty)
		}
		if got := len(cursorRows(t, ctx, conn)); got != len(f.cursorRows) {
			t.Errorf("cursor rows = %d, want %d", got, len(f.cursorRows))
		}
	})
}

// A broad house pattern next to the exact override is the shape that made the
// naive "any matching ignored=1 row" predicate destructive: Dolt resolves
// dolt_ignore most-specific-first, so it keeps the table TRACKED here, and a
// heal would drop and commit away a lineage the operator chose to keep.
func TestIgnoredCursorHealRespectsOverrideUnderBroaderPattern(t *testing.T) {
	ctx := t.Context()
	f := newLegacyTrackedFixture(t, "broadoverride")

	f.withRawConn(t, func(conn *sql.Conn) {
		mustExecOn(t, ctx, conn, `REPLACE INTO dolt_ignore VALUES ('ignored\_%', true)`)
		mustExecOn(t, ctx, conn, "REPLACE INTO dolt_ignore VALUES ('ignored_schema_migrations', false)")
		mustExecOn(t, ctx, conn, "CALL DOLT_COMMIT('-Am', 'operator: version the cursor table under a broad house pattern')")
	})

	f.reopenAndClose(t)

	f.withRawConn(t, func(conn *sql.Conn) {
		if !trackedAtHead(t, ctx, conn, "ignored_schema_migrations") {
			t.Error("the reconcile untracked a table Dolt keeps tracked for the operator; the committed lineage is gone")
		}
		if n := untrackCommitCount(t, ctx, conn); n != 0 {
			t.Errorf("commits referencing #4356 = %d, want 0 under an operator override", n)
		}
	})
}

// ignoredCursorTempSweepCommitMessage is the message of the scoped commit that
// sweeps a scratch table committed at HEAD.
const ignoredCursorTempSweepCommitMessage = "schema: drop stray ignored_schema_migrations untrack scratch table (gastownhall/beads#4356)"

// newScratchPendingDeletionFixture builds the state the reconcile gate used to
// miss: the cursor already untracked, the untrack scratch committed at HEAD
// (an older peer, or replication from one), and the scratch then dropped from
// the working set without that deletion ever being committed — the window
// between dropIgnoredCursorScratch's publish and its scoped commit. "__temp__%"
// hides the scratch from every ignore-filtered dirty guard, so nothing but a
// HEAD probe can tell that the deletion is still owed.
func newScratchPendingDeletionFixture(t *testing.T, database string) *legacyTrackedFixture {
	t.Helper()
	ctx := t.Context()
	f := newLegacyTrackedFixture(t, database)

	f.withRawConn(t, func(conn *sql.Conn) {
		untrackCursorInPlace(t, ctx, conn)
		mustExecOn(t, ctx, conn, "CALL DOLT_ADD('-f', ?)", untrackScratchTable)
		mustExecOn(t, ctx, conn, "CALL DOLT_COMMIT('-m', 'peer: scratch table committed by an older binary')")
		mustExecOn(t, ctx, conn, "DROP TABLE "+untrackScratchTable)

		// The preconditions everything below rests on. Without them the test
		// silently covers the working-set scratch path the gate already sees,
		// or passes because the scratch name is not ignored at all.
		requireTempPatternSeeded(t, conn)
		if !trackedAtHead(t, ctx, conn, untrackScratchTable) {
			t.Fatalf("fixture did not commit %s into HEAD", untrackScratchTable)
		}
		if tablePresent(t, ctx, conn, untrackScratchTable) {
			t.Fatalf("fixture left %s in the working set", untrackScratchTable)
		}
		if dirty := dirtyTableNames(t, ctx, conn); !contains(dirty, untrackScratchTable) {
			t.Fatalf("dolt_status does not list the pending %s deletion: %v", untrackScratchTable, dirty)
		}
		if trackedAtHead(t, ctx, conn, "ignored_schema_migrations") {
			t.Fatal("fixture left ignored_schema_migrations tracked at HEAD; the legacy-shape branch would run instead")
		}
		f.cursorRows = cursorRows(t, ctx, conn)
		f.commits = commitCount(t, ctx, conn)
	})
	return f
}

// sweepCommits returns the hashes of the commits the scratch sweep minted.
func sweepCommits(t *testing.T, ctx context.Context, conn *sql.Conn) []string {
	t.Helper()
	rows, err := conn.QueryContext(ctx,
		"SELECT commit_hash FROM dolt_log WHERE message = ?", ignoredCursorTempSweepCommitMessage)
	if err != nil {
		t.Fatalf("reading sweep commits: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			t.Fatalf("reading sweep commits: %v", err)
		}
		out = append(out, hash)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading sweep commits: %v", err)
	}
	return out
}

// commitTables lists the tables a commit changed.
func commitTables(t *testing.T, ctx context.Context, conn *sql.Conn, hash string) []string {
	t.Helper()
	rows, err := conn.QueryContext(ctx,
		"SELECT table_name FROM dolt_diff WHERE commit_hash = ? ORDER BY table_name", hash)
	if err != nil {
		t.Fatalf("reading tables changed by %s: %v", hash, err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("reading tables changed by %s: %v", hash, err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading tables changed by %s: %v", hash, err)
	}
	return out
}

func configValue(t *testing.T, ctx context.Context, conn *sql.Conn, asOf, key string) (string, bool) {
	t.Helper()
	var value string
	//nolint:gosec // G201: asOf is a test-local constant.
	err := conn.QueryRowContext(ctx, "SELECT value FROM config"+asOf+" WHERE `key` = ?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false
	}
	if err != nil {
		t.Fatalf("reading config %q%s: %v", key, asOf, err)
	}
	return value, true
}

// TestIgnoredCursorScratchPendingDeletionIsSweptOnOpen is bd-fgdx42: with the
// scratch committed at HEAD and already gone from the working set, the open
// must commit that pending deletion — and only it — or `bd dolt pull` refuses
// the delta forever. The live cursor is the truth and must come through
// unchanged; an unrelated staged change must neither ride into the sweep
// commit nor lose its working-set value.
func TestIgnoredCursorScratchPendingDeletionIsSweptOnOpen(t *testing.T) {
	ctx := t.Context()
	f := newScratchPendingDeletionFixture(t, "scratchheadonly")

	const unrelatedKey = "test.scratch-sweep-unrelated"
	f.withRawConn(t, func(conn *sql.Conn) {
		mustExecOn(t, ctx, conn, "INSERT INTO config (`key`, value) VALUES (?, 'v1')", unrelatedKey)
		mustExecOn(t, ctx, conn, "CALL DOLT_ADD('config')")
	})

	f.reopenAndClose(t)

	var swept []string
	var afterSweep int
	f.withRawConn(t, func(conn *sql.Conn) {
		if trackedAtHead(t, ctx, conn, untrackScratchTable) {
			t.Errorf("%s is still committed at HEAD; its pending deletion was never committed", untrackScratchTable)
		}
		if dirty := dirtyTableNames(t, ctx, conn); contains(dirty, untrackScratchTable) {
			t.Errorf("dolt_status still lists %s after the open: %v", untrackScratchTable, dirty)
		}
		if got := cursorRows(t, ctx, conn); !slices.Equal(got, f.cursorRows) {
			t.Errorf("cursor rows changed across the sweep:\n got: %v\nwant: %v", got, f.cursorRows)
		}
		afterSweep = commitCount(t, ctx, conn)
		if afterSweep != f.commits+1 {
			t.Errorf("commits after the sweep = %d, want %d (exactly the scoped sweep)", afterSweep, f.commits+1)
		}
		swept = sweepCommits(t, ctx, conn)
		if len(swept) != 1 {
			t.Fatalf("sweep commits = %d, want exactly 1", len(swept))
		}
		if tables := commitTables(t, ctx, conn, swept[0]); len(tables) != 1 || tables[0] != untrackScratchTable {
			t.Errorf("sweep commit changed %v, want only %s", tables, untrackScratchTable)
		}
		if _, atHead := configValue(t, ctx, conn, " AS OF 'HEAD'", unrelatedKey); atHead {
			t.Errorf("the unrelated staged config row %q rode into HEAD", unrelatedKey)
		}
		if v, ok := configValue(t, ctx, conn, "", unrelatedKey); !ok || v != "v1" {
			t.Errorf("working-set config %q = %q (present %v), want v1 kept", unrelatedKey, v, ok)
		}
	})

	// Idempotent: a second open finds nothing owed and commits nothing.
	f.reopenAndClose(t)
	f.withRawConn(t, func(conn *sql.Conn) {
		if again := sweepCommits(t, ctx, conn); len(again) != 1 {
			t.Errorf("sweep commits after a second open = %d, want still 1", len(again))
		}
		if got := commitCount(t, ctx, conn); got != afterSweep {
			t.Errorf("commits after a second open = %d, want %d (a healthy database must produce no commit)", got, afterSweep)
		}
		if trackedAtHead(t, ctx, conn, untrackScratchTable) || tablePresent(t, ctx, conn, untrackScratchTable) {
			t.Errorf("%s reappeared after a second open", untrackScratchTable)
		}
	})
}

// TestIgnoredCursorScratchPendingDeletionWithCursorMissingSweepsOnly is the
// restore guard. With the cursor table also gone, the resume must not treat a
// scratch that exists only at HEAD as its row source — the working-set column
// read finds no such table and every writable open would fail. The sweep
// commits the deletion, and the ordinary migration pass recreates the cursor.
func TestIgnoredCursorScratchPendingDeletionWithCursorMissingSweepsOnly(t *testing.T) {
	ctx := t.Context()
	f := newScratchPendingDeletionFixture(t, "scratchheadnocursor")

	f.withRawConn(t, func(conn *sql.Conn) {
		mustExecOn(t, ctx, conn, "DROP TABLE ignored_schema_migrations")
		if tablePresent(t, ctx, conn, "ignored_schema_migrations") {
			t.Fatal("fixture left ignored_schema_migrations in the working set")
		}
	})

	f.reopenAndClose(t)

	var commits int
	f.withRawConn(t, func(conn *sql.Conn) {
		if trackedAtHead(t, ctx, conn, untrackScratchTable) {
			t.Errorf("%s is still committed at HEAD", untrackScratchTable)
		}
		if tablePresent(t, ctx, conn, untrackScratchTable) {
			t.Errorf("%s is back in the working set", untrackScratchTable)
		}
		if !tablePresent(t, ctx, conn, "ignored_schema_migrations") {
			t.Fatal("ignored_schema_migrations was not recreated by the migration pass")
		}
		if got := cursorRows(t, ctx, conn); len(got) == 0 {
			t.Error("the recreated cursor has no rows; the ignored series did not re-run")
		}
		var version int
		if err := conn.QueryRowContext(ctx,
			"SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations").Scan(&version); err != nil {
			t.Fatalf("reading the recreated cursor: %v", err)
		}
		if version != schema.LatestIgnoredVersion() {
			t.Errorf("recreated cursor high-water mark = %d, want %d (normal migration, not a restore)", version, schema.LatestIgnoredVersion())
		}
		if trackedAtHead(t, ctx, conn, "ignored_schema_migrations") {
			t.Error("the recreated ignored_schema_migrations was committed into HEAD")
		}
		if dirty := dirtyTableNames(t, ctx, conn); len(dirty) > 0 {
			t.Errorf("working set is dirty after the reopen: %v", dirty)
		}
		if n := len(sweepCommits(t, ctx, conn)); n != 1 {
			t.Errorf("sweep commits = %d, want exactly 1", n)
		}
		commits = commitCount(t, ctx, conn)
	})

	f.reopenAndClose(t)
	f.withRawConn(t, func(conn *sql.Conn) {
		if got := commitCount(t, ctx, conn); got != commits {
			t.Errorf("commits after a second open = %d, want %d (nothing left to do)", got, commits)
		}
		if dirty := dirtyTableNames(t, ctx, conn); len(dirty) > 0 {
			t.Errorf("working set is dirty after the second open: %v", dirty)
		}
	})
}
