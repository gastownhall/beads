//go:build cgo

package issueops

import (
	"context"
	"database/sql"
	"testing"

	sqlite3 "github.com/mattn/go-sqlite3"
)

// The DoltLite backend runs beads SQL through SQLite's grammar while providing
// a MySQL-compatible scalar-function library (JSON_UNQUOTE, ...). A stock
// SQLite engine reproduces that grammar exactly, so it is a faithful proxy for
// the DoltLite parse path; we only have to register the MySQL-only functions
// the recompute templates reference so the full statement can prepare. The bug
// under test — a target-table alias in UPDATE (UPDATE issues i SET i.col = ...),
// which SQLite rejects with `near "i": syntax error` — is a grammar-level
// defect and is unaffected by these function registrations.
func init() {
	sql.Register("sqlite3_beads_blockedstate", &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			// The waits-for gate fragment calls JSON_EXTRACT/JSON_UNQUOTE, which
			// DoltLite provides but a stock mattn SQLite build does not. They only
			// have to *register* so the full statement can prepare; the recompute
			// never evaluates them here (no waits-for gate rows are seeded), so
			// trivial stand-ins are sufficient and faithful to the grammar bug.
			if err := conn.RegisterFunc("json_extract", func(doc, path string) string { return doc }, true); err != nil {
				return err
			}
			return conn.RegisterFunc("json_unquote", func(s string) string { return s }, true)
		},
	})
}

func openBlockedStateSQLite(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3_beads_blockedstate", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// mattn :memory: databases are per-connection; pin to a single connection
	// so DDL, seed, the recompute tx, and the assertions all see one store.
	db.SetMaxOpenConns(1)

	for _, ddl := range []string{
		`CREATE TABLE issues (id TEXT PRIMARY KEY, is_blocked INTEGER NOT NULL DEFAULT 0, updated_at TEXT, status TEXT NOT NULL)`,
		`CREATE TABLE wisps  (id TEXT PRIMARY KEY, is_blocked INTEGER NOT NULL DEFAULT 0, updated_at TEXT, status TEXT NOT NULL)`,
		`CREATE TABLE dependencies      (issue_id TEXT, depends_on_issue_id TEXT, depends_on_wisp_id TEXT, type TEXT, metadata TEXT)`,
		`CREATE TABLE wisp_dependencies (issue_id TEXT, depends_on_issue_id TEXT, depends_on_wisp_id TEXT, type TEXT, metadata TEXT)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("schema %q: %v", ddl, err)
		}
	}
	return db
}

// TestRecomputeIsBlockedInTx_SQLiteGrammar is a regression test for the
// DoltLite `recompute is_blocked (mark): near "i": syntax error` failure: the
// mark/unmark templates emitted a MySQL-style target-table alias
// (`UPDATE issues i SET i.is_blocked = ...`) that SQLite's grammar rejects.
// It exercises all four templates (issue mark+unmark, wisp mark+unmark) against
// a real SQLite engine — the first real-engine coverage of the recompute, which
// the go-sqlmock-only suite could never catch.
func TestRecomputeIsBlockedInTx_SQLiteGrammar(t *testing.T) {
	db := openBlockedStateSQLite(t)
	ctx := context.Background()

	for _, seed := range []string{
		// blocked-i is blocked by an open blocker -> mark should set is_blocked=1.
		`INSERT INTO issues (id,is_blocked,updated_at,status) VALUES ('blocked-i',0,'t','open'),('blocker-i',0,'t','open')`,
		`INSERT INTO dependencies (issue_id,depends_on_issue_id,type) VALUES ('blocked-i','blocker-i','blocks')`,
		// a wisp blocked by the same open issue -> exercises the wisp templates.
		`INSERT INTO wisps (id,is_blocked,updated_at,status) VALUES ('blocked-w',0,'t','open')`,
		`INSERT INTO wisp_dependencies (issue_id,depends_on_issue_id,type) VALUES ('blocked-w','blocker-i','blocks')`,
	} {
		if _, err := db.Exec(seed); err != nil {
			t.Fatalf("seed %q: %v", seed, err)
		}
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Pre-fix, the very first template execution fails with
	// `recompute is_blocked (mark): near "i": syntax error`.
	if err := RecomputeIsBlockedInTx(ctx, tx, []string{"blocked-i", "blocker-i"}, []string{"blocked-w"}); err != nil {
		t.Fatalf("RecomputeIsBlockedInTx (regression: DoltLite parse error): %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	assertBlocked := func(table, id string, want int) {
		t.Helper()
		var got int
		if err := db.QueryRow("SELECT is_blocked FROM "+table+" WHERE id = ?", id).Scan(&got); err != nil {
			t.Fatalf("query %s.%s: %v", table, id, err)
		}
		if got != want {
			t.Errorf("%s %s: is_blocked = %d, want %d", table, id, got, want)
		}
	}
	assertBlocked("issues", "blocked-i", 1) // blocked by an open blocker
	assertBlocked("issues", "blocker-i", 0) // blocks others but is not itself blocked
	assertBlocked("wisps", "blocked-w", 1)  // blocked by the same open blocker
}

// TestRecomputeIsBlockedInTx_SQLiteUnmark covers the unmark path: a bead flagged
// is_blocked whose blocker has since closed must be cleared. This drives the
// unmark templates (also target-table aliased) to a real SQLite engine.
func TestRecomputeIsBlockedInTx_SQLiteUnmark(t *testing.T) {
	db := openBlockedStateSQLite(t)
	ctx := context.Background()

	for _, seed := range []string{
		// was-blocked is flagged, but its only blocker is already closed.
		`INSERT INTO issues (id,is_blocked,updated_at,status) VALUES ('was-blocked',1,'t','open'),('done-blocker',0,'t','closed')`,
		`INSERT INTO dependencies (issue_id,depends_on_issue_id,type) VALUES ('was-blocked','done-blocker','blocks')`,
	} {
		if _, err := db.Exec(seed); err != nil {
			t.Fatalf("seed %q: %v", seed, err)
		}
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := RecomputeIsBlockedInTx(ctx, tx, []string{"was-blocked"}, nil); err != nil {
		t.Fatalf("RecomputeIsBlockedInTx (unmark): %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	var got int
	if err := db.QueryRow("SELECT is_blocked FROM issues WHERE id = 'was-blocked'").Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != 0 {
		t.Errorf("was-blocked: is_blocked = %d, want 0 (blocker is closed)", got)
	}
}
