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
