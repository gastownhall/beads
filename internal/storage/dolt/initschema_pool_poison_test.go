package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"

	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestFreshDB_PoolReadsSchemaMigrations reproduces a pooled-connection
// poisoning bug: on a brand-new database, the migration cursor's probe query
// runs against schema_migrations before the table exists, and Dolt pins that
// pooled session's catalog to its pre-statement snapshot on the resulting
// failure. Migrations then create the table on a different connection, but a
// caller who later reuses the poisoned connection still sees "table not
// found: schema_migrations" even though the table is really there.
//
// A single query can pass by luck if it happens to land on a connection that
// never saw the pre-table failure, so this loops enough times to cycle
// through the whole pool rather than trust one draw.
func TestFreshDB_PoolReadsSchemaMigrations(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := testContext(t)
	defer cancel()

	tmpDir, err := os.MkdirTemp("", "dolt-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbName := uniqueTestDBName(t)
	cfg := &Config{
		Path:            tmpDir,
		CommitterName:   "test",
		CommitterEmail:  "test@example.com",
		Database:        dbName,
		CreateIfMissing: true,
	}

	store, err := New(ctx, cfg)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 5*testTimeout)
		defer dropCancel()
		_, _ = store.db.ExecContext(dropCtx, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", dbName))
		store.Close()
	}()

	for i := 0; i < defaultMaxOpenConns*2; i++ {
		var count int
		if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil {
			t.Fatalf("store's own pool could not read schema_migrations on iteration %d (fresh DB immediately after New()): %v", i, err)
		}
		if count == 0 {
			t.Fatalf("schema_migrations has no rows on iteration %d — migrations did not record their version", i)
		}
	}
}

// TestSentinelColumnProbe_PoolSeesLaterTables is the be-bv7x shape for the
// ignored cursor's column sentinel, leases.granted_node. Every write-capable
// open reads that cursor through the sentinel probes, and the column's carrier
// table is exactly what a release-lineage store lacks (no leases before
// ignored/0012) or what an out-of-band copy can drop. A probe that sends
// SHOW COLUMNS to the missing table fails, and on the Dolt releases bd runs
// against (2.1.8 and the 2.2.0 CI pins; 2.4.1 no longer does it) a failing
// statement on the store's multiStatements session pins that pooled session
// to its pre-statement catalog: the leases table the ignored replay then
// creates on another connection stays "table not found" there.
//
// The pool under test holds one connection, so the session the probe ran on
// is the session the follow-up read lands on.
func TestSentinelColumnProbe_PoolSeesLaterTables(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := testContext(t)
	defer cancel()

	dbName := uniqueTestDBName(t)
	store, err := New(ctx, &Config{
		Path:            t.TempDir(),
		CommitterName:   "test",
		CommitterEmail:  "test@example.com",
		Database:        dbName,
		CreateIfMissing: true,
	})
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 5*testTimeout)
		defer dropCancel()
		_, _ = store.db.ExecContext(dropCtx, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", dbName))
		store.Close()
	}()

	// Fully migrated, then the column sentinel's carrier table goes missing:
	// the ignored cursor and every sentinel table are present, leases is not.
	if _, err := store.db.ExecContext(ctx, "DROP TABLE leases"); err != nil {
		t.Fatalf("drop leases: %v", err)
	}

	pool, err := sql.Open("mysql", doltutil.ServerDSN{
		Host: "127.0.0.1", Port: testServerPort, User: "root", Database: dbName,
	}.String())
	if err != nil {
		t.Fatalf("open single-connection pool: %v", err)
	}
	defer pool.Close()
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)

	version, err := schema.CurrentIgnoredVersion(ctx, pool)
	if err != nil {
		t.Fatalf("CurrentIgnoredVersion with leases missing: %v", err)
	}
	if version >= schema.LatestIgnoredVersion() {
		t.Fatalf("ignored cursor read %d with leases missing; the column sentinel did not contradict it", version)
	}

	// The ignored replay re-creates leases on its own connection.
	if _, err := store.db.ExecContext(ctx,
		"CREATE TABLE leases (id VARCHAR(255) PRIMARY KEY, granted_node VARCHAR(255) NOT NULL DEFAULT '')"); err != nil {
		t.Fatalf("re-create leases: %v", err)
	}

	var count int
	if err := pool.QueryRowContext(ctx, "SELECT COUNT(*) FROM leases").Scan(&count); err != nil {
		t.Fatalf("the pooled session that ran the sentinel probe cannot see a table created after it (be-bv7x pin): %v", err)
	}
}

// TestCursorProbe_NoDatabaseSessionStaysUsable pins the cursor-existence probe
// to the be-bv7x rule on a real sql-server: it must succeed on any session. On
// a session with no database selected, SHOW TABLES fails ("no database
// selected"), and a failing statement pins the pooled session to its
// pre-statement catalog. The probe has to read the cursor as absent there,
// exactly as the INFORMATION_SCHEMA.TABLES count it replaced did, and leave the
// session able to see a database and table created afterwards elsewhere.
func TestCursorProbe_NoDatabaseSessionStaysUsable(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := testContext(t)
	defer cancel()

	dsn := doltutil.ServerDSN{Host: "127.0.0.1", Port: testServerPort, User: "root"}.String()
	pool, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open no-database pool: %v", err)
	}
	defer pool.Close()
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)

	version, err := schema.CurrentVersion(ctx, pool)
	if err != nil {
		t.Fatalf("CurrentVersion on a session with no database selected: %v", err)
	}
	if version != 0 {
		t.Fatalf("CurrentVersion on a session with no database selected = %d, want 0", version)
	}

	admin, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	defer admin.Close()
	dbName := uniqueTestDBName(t)
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE `"+dbName+"`"); err != nil {
		t.Fatalf("create database: %v", err)
	}
	defer func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 5*testTimeout)
		defer dropCancel()
		_, _ = admin.ExecContext(dropCtx, "DROP DATABASE IF EXISTS `"+dbName+"`")
	}()
	if _, err := admin.ExecContext(ctx, "CREATE TABLE `"+dbName+"`.probe_after (id INT PRIMARY KEY)"); err != nil {
		t.Fatalf("create table: %v", err)
	}

	conn, err := pool.Conn(ctx)
	if err != nil {
		t.Fatalf("pin the probed session: %v", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "USE `"+dbName+"`"); err != nil {
		t.Fatalf("the session the cursor probe ran on cannot select a database created after it: %v", err)
	}
	var count int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM probe_after").Scan(&count); err != nil {
		t.Fatalf("the session the cursor probe ran on cannot see a table created after it (be-bv7x pin): %v", err)
	}
}

// TestCursorProbe_DroppedDatabaseSessionStaysUsable is the 1049 half of
// TestCursorProbe_NoDatabaseSessionStaysUsable: on a session whose database
// was dropped under it, SHOW TABLES fails with "database not found" rather
// than "no database selected". The probe must read the cursor as absent there
// too, and leave the session able to see a database and table created
// afterwards.
func TestCursorProbe_DroppedDatabaseSessionStaysUsable(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := testContext(t)
	defer cancel()

	dsn := doltutil.ServerDSN{Host: "127.0.0.1", Port: testServerPort, User: "root"}.String()
	admin, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	defer admin.Close()
	dropLater := func(name string) {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 5*testTimeout)
		defer dropCancel()
		_, _ = admin.ExecContext(dropCtx, "DROP DATABASE IF EXISTS `"+name+"`")
	}

	dropped := uniqueTestDBName(t)
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE `"+dropped+"`"); err != nil {
		t.Fatalf("create database: %v", err)
	}
	defer dropLater(dropped)

	pool, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open probe pool: %v", err)
	}
	defer pool.Close()
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)
	conn, err := pool.Conn(ctx)
	if err != nil {
		t.Fatalf("pin the probed session: %v", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "USE `"+dropped+"`"); err != nil {
		t.Fatalf("select the database to drop: %v", err)
	}
	if _, err := admin.ExecContext(ctx, "DROP DATABASE `"+dropped+"`"); err != nil {
		t.Fatalf("drop the probed session's database: %v", err)
	}

	version, err := schema.CurrentVersion(ctx, conn)
	if err != nil {
		t.Fatalf("CurrentVersion on a session whose database was dropped: %v", err)
	}
	if version != 0 {
		t.Fatalf("CurrentVersion on a session whose database was dropped = %d, want 0", version)
	}

	dbName := uniqueTestDBName(t)
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE `"+dbName+"`"); err != nil {
		t.Fatalf("create database: %v", err)
	}
	defer dropLater(dbName)
	if _, err := admin.ExecContext(ctx, "CREATE TABLE `"+dbName+"`.probe_after (id INT PRIMARY KEY)"); err != nil {
		t.Fatalf("create table: %v", err)
	}

	if _, err := conn.ExecContext(ctx, "USE `"+dbName+"`"); err != nil {
		t.Fatalf("the session the cursor probe ran on cannot select a database created after it: %v", err)
	}
	var count int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM probe_after").Scan(&count); err != nil {
		t.Fatalf("the session the cursor probe ran on cannot see a table created after it (be-bv7x pin): %v", err)
	}
}

// TestShowTablesProbes_SeeIgnoredTablesInAnyCase checks the per-open existence
// probes against a real migrated store on a fresh session. Both cursor tables'
// rows must be believed as read: the ignored cursor and every sentinel the
// cursor reality check reads are dolt_ignored, so they live only in the
// working set, and a probe that missed them would clamp the cursor and replay
// the series on every open. Dolt resolves table names case-insensitively, as
// the INFORMATION_SCHEMA.TABLES count these probes replaced does, so a
// sentinel stored in another case is still present — a case-sensitive probe
// (SHOW TABLES LIKE) reads it as absent.
func TestShowTablesProbes_SeeIgnoredTablesInAnyCase(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := testContext(t)
	defer cancel()

	dbName := uniqueTestDBName(t)
	store, err := New(ctx, &Config{
		Path:            t.TempDir(),
		CommitterName:   "test",
		CommitterEmail:  "test@example.com",
		Database:        dbName,
		CreateIfMissing: true,
	})
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 5*testTimeout)
		defer dropCancel()
		_, _ = store.db.ExecContext(dropCtx, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", dbName))
		store.Close()
	}()

	pool, err := sql.Open("mysql", doltutil.ServerDSN{
		Host: "127.0.0.1", Port: testServerPort, User: "root", Database: dbName,
	}.String())
	if err != nil {
		t.Fatalf("open single-connection pool: %v", err)
	}
	defer pool.Close()
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)

	requireCursors := func(state string) {
		t.Helper()
		main, err := schema.CurrentVersion(ctx, pool)
		if err != nil {
			t.Fatalf("%s: CurrentVersion: %v", state, err)
		}
		if main != schema.LatestVersion() {
			t.Fatalf("%s: CurrentVersion = %d, want %d", state, main, schema.LatestVersion())
		}
		ignored, err := schema.CurrentIgnoredVersion(ctx, pool)
		if err != nil {
			t.Fatalf("%s: CurrentIgnoredVersion: %v", state, err)
		}
		if ignored != schema.LatestIgnoredVersion() {
			t.Fatalf("%s: CurrentIgnoredVersion = %d, want %d: a sentinel table read as absent", state, ignored, schema.LatestIgnoredVersion())
		}
	}
	requireCursors("freshly migrated store")

	if _, err := store.db.ExecContext(ctx, "RENAME TABLE bd_events_seq TO bd_events_seq_case"); err != nil {
		t.Fatalf("rename bd_events_seq: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, "RENAME TABLE bd_events_seq_case TO BD_EVENTS_SEQ"); err != nil {
		t.Fatalf("rename to BD_EVENTS_SEQ: %v", err)
	}
	requireCursors("bd_events_seq stored as BD_EVENTS_SEQ")
}

// TestReadMigrationContentHashes_PoolSeesLaterTables is the be-bv7x shape for
// the HEAD read of the migration content hashes (bd-21dc1). `bd doctor` runs
// that read on a database it never migrates, where the cursor table can be
// absent, or present from before #4259 without content_hash. A SELECT that
// fails on either shape pins the pooled session to its pre-statement catalog
// on the Dolt releases bd runs against (see
// TestSentinelColumnProbe_PoolSeesLaterTables), so the read has to report both
// as missing objects without sending a statement that fails.
//
// The pool under test holds one connection, so the session the read ran on is
// the session the follow-up read lands on.
func TestReadMigrationContentHashes_PoolSeesLaterTables(t *testing.T) {
	skipIfNoDolt(t)

	admin, err := sql.Open("mysql", doltutil.ServerDSN{Host: "127.0.0.1", Port: testServerPort, User: "root"}.String())
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	defer admin.Close()

	tests := []struct {
		name   string
		cursor string // DDL for the cursor's shape; empty when the table is absent
	}{
		{name: "cursor table absent"},
		{name: "content_hash column absent", cursor: "CREATE TABLE schema_migrations (version INT PRIMARY KEY)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := testContext(t)
			defer cancel()

			dbName := uniqueTestDBName(t)
			if _, err := admin.ExecContext(ctx, "CREATE DATABASE `"+dbName+"`"); err != nil {
				t.Fatalf("create database: %v", err)
			}
			defer func() {
				dropCtx, dropCancel := context.WithTimeout(context.Background(), 5*testTimeout)
				defer dropCancel()
				_, _ = admin.ExecContext(dropCtx, "DROP DATABASE IF EXISTS `"+dbName+"`")
			}()

			pool, err := sql.Open("mysql", doltutil.ServerDSN{
				Host: "127.0.0.1", Port: testServerPort, User: "root", Database: dbName,
			}.String())
			if err != nil {
				t.Fatalf("open single-connection pool: %v", err)
			}
			defer pool.Close()
			pool.SetMaxOpenConns(1)
			pool.SetMaxIdleConns(1)
			if tc.cursor != "" {
				if _, err := pool.ExecContext(ctx, tc.cursor); err != nil {
					t.Fatalf("create the cursor table: %v", err)
				}
			}

			_, err = schema.ReadMigrationContentHashes(ctx, pool, "")
			if err == nil || !schema.MissingMigrationObjectErr(err) {
				t.Fatalf("ReadMigrationContentHashes(HEAD) error = %v, want a missing-object error", err)
			}

			if _, err := admin.ExecContext(ctx, "CREATE TABLE `"+dbName+"`.later_tbl (id INT PRIMARY KEY)"); err != nil {
				t.Fatalf("create table: %v", err)
			}
			var count int
			if err := pool.QueryRowContext(ctx, "SELECT COUNT(*) FROM later_tbl").Scan(&count); err != nil {
				t.Fatalf("the pooled session that ran the HEAD read cannot see a table created after it (be-bv7x pin): %v", err)
			}
		})
	}
}
