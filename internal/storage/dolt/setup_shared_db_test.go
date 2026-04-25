package dolt

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/testutil"
)

// Tests for the SetupSharedTestDB / initSharedSchema interaction surfaced by
// be-s9d. The bug observed in the BEADS_TEST_EXTERNAL_DOLT_PORT path is that
// after testutil.SetupSharedTestDB runs CREATE DATABASE, a freshly-opened
// connection from dolt.New() — which dials the same Dolt server with the new
// database in the DSN — fails with "Error 1049 (HY000): database not found".
// The diagnosis (be-nx7 reviewer notes): the CREATE DATABASE is left in the
// working set without an explicit DOLT_COMMIT, so a fresh connection that
// opens its own session does not see it.
//
// These tests pin the behavioral contract: after SetupSharedTestDB returns,
// the new database MUST be visible to a brand-new *sql.DB connection — both a
// raw mysql driver connection (matching the dolt.New() shape) and a full
// dolt.New() store. Both code paths exercise the same race; if the fix
// regresses, these tests fail.

// uniqueSetupSharedDBName returns a per-test database name with a random
// suffix so each test gets a fresh CREATE DATABASE without colliding with
// the package-shared dolt_pkg_shared or with concurrent subtests.
func uniqueSetupSharedDBName(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	return "test_setupshared_" + hex.EncodeToString(buf)
}

// TestSetupSharedTestDB_FreshRawConnSeesDatabase verifies that a brand-new
// *sql.DB connection sees the new database in SHOW DATABASES immediately
// after SetupSharedTestDB returns.
//
// SHOW DATABASES (not USE) is the canonical visibility check: dolt.New()
// guards CREATE-vs-open with a SHOW DATABASES probe (databaseExistsOnServer
// in store.go), and the be-s9d failure mode is that SHOW DATABASES on a
// fresh connection does NOT list the just-created database, so dolt.New()
// returns "database not found".
//
// USE <db> on a fresh connection appears to succeed via the mysql driver
// (Dolt may auto-attach the database in a session even when the catalog
// hasn't registered it), so a Ping-based test is not strict enough — we
// hit SHOW DATABASES directly.
func TestSetupSharedTestDB_FreshRawConnSeesDatabase(t *testing.T) {
	skipIfNoServer(t)

	dbName := uniqueSetupSharedDBName(t)

	setupConn, err := testutil.SetupSharedTestDB(testServerPort, dbName)
	if err != nil {
		t.Fatalf("SetupSharedTestDB(%q): %v", dbName, err)
	}
	t.Cleanup(func() { _ = setupConn.Close() })
	t.Cleanup(func() { dropTestDatabase(t, testServerPort, dbName) })

	// Open a fresh *sql.DB WITHOUT the database in the DSN — this is the
	// shape of dolt.New()'s initDB connection that runs SHOW DATABASES.
	dsn := doltutil.ServerDSN{
		Host:    "127.0.0.1",
		Port:    testServerPort,
		User:    "root",
		Timeout: 5 * time.Second,
	}.String()
	freshDB, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = freshDB.Close() })
	freshDB.SetMaxOpenConns(1)

	rows, err := freshDB.QueryContext(context.Background(), "SHOW DATABASES")
	if err != nil {
		t.Fatalf("SHOW DATABASES on fresh connection: %v", err)
	}
	defer rows.Close()

	var found bool
	var seen []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		seen = append(seen, n)
		if n == dbName {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}

	if !found {
		t.Fatalf("SHOW DATABASES on fresh connection did not list %q\n  databases visible: %v\n  SetupSharedTestDB created the database but it is not in a fresh session's catalog — this is the be-s9d bug",
			dbName, seen)
	}
}

// TestSetupSharedTestDB_NewStoreOpens verifies that the canonical
// initSharedSchema flow — call SetupSharedTestDB, then dolt.New() against
// the freshly-created database — succeeds. This is exactly the sequence
// testmain_test.go runs in the BEADS_TEST_EXTERNAL_DOLT_PORT branch where
// the bug surfaces.
func TestSetupSharedTestDB_NewStoreOpens(t *testing.T) {
	skipIfNoServer(t)

	dbName := uniqueSetupSharedDBName(t)

	setupConn, err := testutil.SetupSharedTestDB(testServerPort, dbName)
	if err != nil {
		t.Fatalf("SetupSharedTestDB(%q): %v", dbName, err)
	}
	t.Cleanup(func() { _ = setupConn.Close() })
	t.Cleanup(func() { dropTestDatabase(t, testServerPort, dbName) })

	ctx, cancel := testContext(t)
	defer cancel()

	cfg := &Config{
		Path:         t.TempDir(),
		ServerHost:   "127.0.0.1",
		ServerPort:   testServerPort,
		Database:     dbName,
		MaxOpenConns: 1,
		// CreateIfMissing intentionally false: SetupSharedTestDB already
		// created the database. If New errors with "database not found"
		// here, the be-s9d race reproduced.
	}

	store, err := New(ctx, cfg)
	if err != nil {
		t.Fatalf("New on fresh database %q: %v\n  SetupSharedTestDB created the database but dolt.New cannot see it (be-s9d)",
			dbName, err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// SetConfig exercises an actual write through the store, which forces
	// the new connection to USE <db> and execute. If the database is
	// "found" but the working set is malformed, this will fail.
	if err := store.SetConfig(ctx, "issue_prefix", "setupshared_test"); err != nil {
		t.Fatalf("SetConfig on fresh store: %v", err)
	}
}

// TestSetupSharedTestDB_FreshConnAfterSecondSetupCall verifies that a second
// SetupSharedTestDB call against the SAME database name (idempotent path)
// does not break visibility for fresh connections. SetupSharedTestDB already
// uses CREATE DATABASE IF NOT EXISTS and tolerates Dolt's error 1007, but if
// the fix introduces side effects (e.g., DOLT_COMMIT on every call), they
// must not regress the second-call shape.
func TestSetupSharedTestDB_FreshConnAfterSecondSetupCall(t *testing.T) {
	skipIfNoServer(t)

	dbName := uniqueSetupSharedDBName(t)

	first, err := testutil.SetupSharedTestDB(testServerPort, dbName)
	if err != nil {
		t.Fatalf("first SetupSharedTestDB: %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })
	t.Cleanup(func() { dropTestDatabase(t, testServerPort, dbName) })

	second, err := testutil.SetupSharedTestDB(testServerPort, dbName)
	if err != nil {
		t.Fatalf("second SetupSharedTestDB on existing %q: %v", dbName, err)
	}
	t.Cleanup(func() { _ = second.Close() })

	dsn := doltutil.ServerDSN{
		Host:    "127.0.0.1",
		Port:    testServerPort,
		User:    "root",
		Timeout: 5 * time.Second,
	}.String()
	freshDB, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = freshDB.Close() })
	freshDB.SetMaxOpenConns(1)

	rows, err := freshDB.QueryContext(context.Background(), "SHOW DATABASES")
	if err != nil {
		t.Fatalf("SHOW DATABASES after second SetupSharedTestDB: %v", err)
	}
	defer rows.Close()

	var found bool
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if n == dbName {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	if !found {
		t.Fatalf("SHOW DATABASES did not list %q after second SetupSharedTestDB call; idempotency must not regress visibility",
			dbName)
	}
}

// TestSetupSharedTestDB_RefusesProductionPort verifies the FIREWALL guard
// in SetupSharedTestDB: passing the production port (DefaultSQLPort) must
// fail with a clear refusal message and must not open a connection. This is
// a defensive contract — the fix in be-s9d touches SetupSharedTestDB and
// the firewall must remain intact through any refactor.
func TestSetupSharedTestDB_RefusesProductionPort(t *testing.T) {
	conn, err := testutil.SetupSharedTestDB(DefaultSQLPort, "should_never_be_created")
	if err == nil {
		if conn != nil {
			_ = conn.Close()
		}
		t.Fatal("SetupSharedTestDB on production port returned no error; the firewall is broken")
	}
	if conn != nil {
		_ = conn.Close()
		t.Errorf("SetupSharedTestDB on production port returned a non-nil *sql.DB; firewall must close any handle before returning the error")
	}
	if !containsAny(err.Error(), "refused", "production", "3307") {
		t.Errorf("error message should clearly refuse the production port, got: %v", err)
	}
}

// TestInitSharedSchema_AfterFreshDatabase verifies the initSharedSchema flow
// against a freshly-created database. This is the higher-level integration
// test: it exercises the same TestMain-time call sequence that fails in the
// BEADS_TEST_EXTERNAL_DOLT_PORT branch, but driven from a regular test so
// the failure is captured by `go test` rather than a fatal-out-of-TestMain
// stderr message.
//
// Note: the production initSharedSchema closes over the package-level
// testSharedDB. This test temporarily redirects testSharedDB to a unique
// per-test name so it does not collide with the suite-wide shared DB.
func TestInitSharedSchema_AfterFreshDatabase(t *testing.T) {
	skipIfNoServer(t)

	dbName := uniqueSetupSharedDBName(t)

	// SetupSharedTestDB does the CREATE DATABASE.
	setupConn, err := testutil.SetupSharedTestDB(testServerPort, dbName)
	if err != nil {
		t.Fatalf("SetupSharedTestDB: %v", err)
	}
	t.Cleanup(func() { _ = setupConn.Close() })
	t.Cleanup(func() { dropTestDatabase(t, testServerPort, dbName) })

	// initSharedSchema reads the package-level testSharedDB, so we must
	// redirect it for the duration of this test. No goroutines from
	// previous tests should still be calling initSharedSchema, but we
	// restore the original value to be safe.
	prevDB := testSharedDB
	testSharedDB = dbName
	t.Cleanup(func() { testSharedDB = prevDB })

	if err := initSharedSchema(testServerPort); err != nil {
		t.Fatalf("initSharedSchema on fresh database %q: %v\n  this is the literal failure mode reported in be-s9d",
			dbName, err)
	}

	// After initSharedSchema succeeds, the schema must be visible from
	// yet another fresh connection — captures the case where DOLT_COMMIT
	// in initSharedSchema fixed up visibility for that connection only,
	// not for subsequent ones. Use a quick SHOW TABLES probe.
	dsn := doltutil.ServerDSN{
		Host:     "127.0.0.1",
		Port:     testServerPort,
		User:     "root",
		Database: dbName,
		Timeout:  5 * time.Second,
	}.String()
	freshDB, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = freshDB.Close() })

	rows, err := freshDB.QueryContext(context.Background(), "SHOW TABLES")
	if err != nil {
		t.Fatalf("SHOW TABLES on fresh connection after initSharedSchema: %v", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		names = append(names, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	if len(names) == 0 {
		t.Fatalf("SHOW TABLES returned no tables on fresh connection; initSharedSchema did not commit the schema visibly\n  (got %d rows; expected at least the issues table)",
			len(names))
	}

	// issues is the canonical table the schema migrations create; its
	// presence proves the schema commit landed for fresh sessions.
	hasIssues := false
	for _, n := range names {
		if n == "issues" {
			hasIssues = true
			break
		}
	}
	if !hasIssues {
		t.Fatalf("SHOW TABLES did not include 'issues': %v", names)
	}
}
