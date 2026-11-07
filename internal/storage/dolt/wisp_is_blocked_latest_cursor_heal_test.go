package dolt

import (
	"path/filepath"
	"testing"
)

// This file covers the wisps.is_blocked restore that ignored migration 0032's
// pre-migration repair (restoreWispIsBlocked) performs on a clone whose
// ignored cursor already sits past ignored/0015, where the ignored/7 and
// ignored/15 repairs can never fire again (gastownhall/beads#4468, #5257).
// Every test drives it through the real server-mode open path -- New, not a
// direct MigrateUp call -- on its own database, because the defect it heals
// is an open-path failure and the fix must be proven on the same path.

// wispHealEnv owns one dedicated database on the test server plus the
// disposable .beads directory stores open under.
type wispHealEnv struct {
	t      *testing.T
	dbName string
	dir    string
}

func newWispHealEnv(t *testing.T) *wispHealEnv {
	t.Helper()
	skipIfNoDolt(t)
	acquireTestSlot()
	t.Cleanup(releaseTestSlot)
	// No DROP DATABASE on cleanup: rapid CREATE/DROP cycles crash the Dolt
	// server; orphan databases are cleaned up when the container terminates.
	dbName := uniqueTestDBName(t)
	return &wispHealEnv{t: t, dbName: dbName, dir: filepath.Join(t.TempDir(), ".beads")}
}

// open opens a store against the env's database under its own timeout budget,
// the way every reopen in these tests must (see openStoreWithOwnBudget).
func (e *wispHealEnv) open() (*DoltStore, error) {
	e.t.Helper()
	return openStoreWithOwnBudget(e.t, &Config{
		Path:            filepath.Join(e.dir, "dolt"),
		BeadsDir:        e.dir,
		ServerHost:      "127.0.0.1",
		ServerPort:      testServerPort,
		Database:        e.dbName,
		MaxOpenConns:    1,
		CreateIfMissing: true,
	})
}

// scalarInt reads one integer over the store's pool.
func (e *wispHealEnv) scalarInt(store *DoltStore, query string, args ...any) int {
	e.t.Helper()
	ctx, cancel := testContext(e.t)
	defer cancel()
	var n int
	if err := store.db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		e.t.Fatalf("%.80q: %v", query, err)
	}
	return n
}

// scalarStrings reads one string column into a slice.
func (e *wispHealEnv) scalarStrings(store *DoltStore, query string, args ...any) []string {
	e.t.Helper()
	ctx, cancel := testContext(e.t)
	defer cancel()
	rows, err := store.db.QueryContext(ctx, query, args...)
	if err != nil {
		e.t.Fatalf("%.80q: %v", query, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			e.t.Fatalf("scan %.80q: %v", query, err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		e.t.Fatalf("%.80q: %v", query, err)
	}
	return out
}

// driftWispIsBlockedForHeal removes the shape ignored/0006 added -- index
// first, then the column it is keyed on -- the way the drifted clones in the
// wild present.
func driftWispIsBlockedForHeal(t *testing.T, store *DoltStore) {
	t.Helper()
	ctx, cancel := testContext(t)
	defer cancel()
	if _, err := store.db.ExecContext(ctx, "DROP INDEX idx_wisps_is_blocked ON wisps"); err != nil {
		t.Fatalf("drop idx_wisps_is_blocked: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, "ALTER TABLE wisps DROP COLUMN is_blocked"); err != nil {
		t.Fatalf("drop wisps.is_blocked: %v", err)
	}
}

// rewindIgnoredCursor deletes every ignored cursor row at version or above,
// re-pending that marker and its repair for the next open.
func rewindIgnoredCursor(t *testing.T, store *DoltStore, version int) {
	t.Helper()
	ctx, cancel := testContext(t)
	defer cancel()
	if _, err := store.db.ExecContext(ctx,
		"DELETE FROM ignored_schema_migrations WHERE version >= ?", version); err != nil {
		t.Fatalf("rewind ignored cursor to %d: %v", version, err)
	}
}

// requireWispIsBlockedShape asserts the column is back with ignored/0006's
// definition and the index is back over (is_blocked, status).
func (e *wispHealEnv) requireWispIsBlockedShape(store *DoltStore) {
	e.t.Helper()
	shape := e.scalarStrings(store, `SELECT CONCAT(COLUMN_TYPE, '|', IS_NULLABLE, '|', COALESCE(COLUMN_DEFAULT, '<null>'))
		FROM INFORMATION_SCHEMA.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps' AND COLUMN_NAME = 'is_blocked'`)
	if len(shape) != 1 || shape[0] != "tinyint(1)|NO|0" {
		e.t.Fatalf("wisps.is_blocked shape = %v, want [tinyint(1)|NO|0]", shape)
	}
	indexCols := e.scalarStrings(store, `SELECT COLUMN_NAME FROM INFORMATION_SCHEMA.STATISTICS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps' AND INDEX_NAME = 'idx_wisps_is_blocked'
		ORDER BY SEQ_IN_INDEX`)
	if len(indexCols) != 2 || indexCols[0] != "is_blocked" || indexCols[1] != "status" {
		e.t.Fatalf("idx_wisps_is_blocked columns = %v, want [is_blocked status]", indexCols)
	}
}

// TestDoltNew_WispIsBlockedSelfHealAtLatestIgnoredCursor_RealDolt covers
// AC-1: a clone at the top of the ignored series whose wisps table lost
// is_blocked heals the column and the index on the next open, before the
// ignored cursor records 0032, and the healed shape matches ignored/0006's
// definition exactly.
func TestDoltNew_WispIsBlockedSelfHealAtLatestIgnoredCursor_RealDolt(t *testing.T) {
	env := newWispHealEnv(t)

	store, err := env.open()
	if err != nil {
		t.Fatalf("New (fresh): %v", err)
	}
	if n := env.scalarInt(store, "SELECT COUNT(*) FROM ignored_schema_migrations WHERE version = 32"); n != 1 {
		store.Close()
		t.Fatalf("fresh open recorded ignored 0032 %d times, want exactly 1", n)
	}
	driftWispIsBlockedForHeal(t, store)
	rewindIgnoredCursor(t, store, 32)
	if err := store.Close(); err != nil {
		t.Fatalf("close drifted store: %v", err)
	}

	healed, err := env.open()
	if err != nil {
		t.Fatalf("New on the drifted clone: %v", err)
	}
	defer func() { _ = healed.Close() }()
	env.requireWispIsBlockedShape(healed)
	if n := env.scalarInt(healed, "SELECT COUNT(*) FROM ignored_schema_migrations WHERE version = 32"); n != 1 {
		t.Fatalf("healed open recorded ignored 0032 %d times, want exactly 1", n)
	}
}
