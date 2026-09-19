//go:build integration && !windows

package dolt

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestSchemaInitRecoversFromPartialNonlocalMigration reproduces, against a real
// shared dolt sql-server, the brick that bricked freshly-init'd rigs: migration
// 0040 partially applied (its CALL DOLT_COMMIT committed a dolt_nonlocal_tables
// row) but a transient ("busy buffer" -> "driver: bad connection") prevented its
// schema_migrations version row from recording, so the init retry loop re-ran
// 0040 from the top forever.
//
// Before the fix the replay died on "duplicate primary key given: [wisps]"
// (0040's bare INSERT) — or "nothing to commit" (its DOLT_COMMIT after a no-op
// INSERT). The fix leaves 0040/0041's shipped, content-hashed bytes untouched
// (editing them would fork already-upgraded clones via schema_migrations
// .content_hash — see scripts/check-migration-hygiene.sh check C). Instead it
// (a) drains every CALL DOLT_* result set so the transient no longer arises
// (schema.DrainCall in internal/storage/schema/schema.go), and (b) registers
// pre-migration repairs keyed to versions 40 and 41 that normalise
// dolt_nonlocal_tables before each frozen body replays
// (internal/storage/schema/migration_repairs.go). Re-init is therefore
// idempotent and recovers to the latest schema version.
//
// Would have caught the bd-1.0.x/1.1.0-rc.1 shared-server init brick
// (ahdr-ovz / hq-oi0db).
func TestSchemaInitRecoversFromPartialNonlocalMigration(t *testing.T) {
	skipIfNoDolt(t)
	acquireTestSlot()
	t.Cleanup(releaseTestSlot)

	if testServerPort == 0 {
		t.Skip("no Dolt test server available")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	rootDSN := doltutil.ServerDSN{Host: "127.0.0.1", Port: testServerPort, User: "root"}.String()
	rootDB, err := sql.Open("mysql", rootDSN)
	if err != nil {
		t.Fatalf("open root connection: %v", err)
	}
	defer rootDB.Close()

	dbName := uniqueTestDBName(t)
	if _, err := rootDB.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS `"+dbName+"`"); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		// Skip DROP — rapid drop cycles can crash the Dolt container.
	})

	dbDSN := doltutil.ServerDSN{Host: "127.0.0.1", Port: testServerPort, User: "root", Database: dbName}.String()
	db, err := sql.Open("mysql", dbDSN)
	if err != nil {
		t.Fatalf("open db connection: %v", err)
	}
	defer db.Close()

	// 1. Fully initialize to the latest schema.
	if _, err := initSchemaOnDB(ctx, db); err != nil {
		t.Fatalf("initial initSchemaOnDB: %v", err)
	}
	latest := schema.LatestVersion()
	assertSchemaVersionAtLeast(ctx, t, db, latest)

	// 2. Forge the brick: rewind the cursor below 0040 and leave a *committed*
	//    dolt_nonlocal_tables row that 0040 inserts. This is exactly the
	//    partially-applied state a transient leaves behind — 0040's CALL
	//    DOLT_COMMIT committed the side effect, but the schema_migrations version
	//    row never recorded. The row must be committed (not just staged): a real
	//    partial 0040 committed it, and an uncommitted row would instead trip the
	//    separate "pending migration alters a dirty table" guard
	//    (gastownhall/beads#4566) before the replay is ever reached.
	if _, err := db.ExecContext(ctx, "DELETE FROM schema_migrations WHERE version >= 40"); err != nil {
		t.Fatalf("rewind schema_migrations: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"INSERT IGNORE INTO dolt_nonlocal_tables (table_name, target_ref, options) VALUES ('wisps', 'main', 'immediate')"); err != nil {
		t.Fatalf("re-add nonlocal row: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"CALL DOLT_COMMIT('-Am', 'forge partial 0040: committed nonlocal row, version row absent')"); err != nil {
		t.Fatalf("commit forged partial state: %v", err)
	}

	// 3. Re-init. Pre-fix this died replaying 0040's non-idempotent INSERT
	//    ("duplicate primary key given: [wisps]"); post-fix the version-40
	//    pre-migration repair clears the committed stray row so the frozen 0040
	//    replays cleanly and the chain reaches the latest version.
	if _, err := initSchemaOnDB(ctx, db); err != nil {
		t.Fatalf("re-init after forged partial migration (this is the brick): %v", err)
	}
	assertSchemaVersionAtLeast(ctx, t, db, latest)
}

// TestSchemaInitRecoversFromPartial0041NonlocalDelete covers the second frozen
// non-idempotent migration. 0041 opens by clearing dolt_nonlocal_tables and
// committing; if a transient interrupts it after that commit but before its
// version row records, the retry re-runs 0041 against an already-empty table and
// the shipped DELETE+COMMIT (no --skip-empty) dies with "nothing to commit". The
// version-41 pre-migration repair restores the four rows 0040 leaves so the
// frozen DELETE has a real diff again, and re-init recovers.
func TestSchemaInitRecoversFromPartial0041NonlocalDelete(t *testing.T) {
	skipIfNoDolt(t)
	acquireTestSlot()
	t.Cleanup(releaseTestSlot)

	if testServerPort == 0 {
		t.Skip("no Dolt test server available")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	rootDSN := doltutil.ServerDSN{Host: "127.0.0.1", Port: testServerPort, User: "root"}.String()
	rootDB, err := sql.Open("mysql", rootDSN)
	if err != nil {
		t.Fatalf("open root connection: %v", err)
	}
	defer rootDB.Close()

	dbName := uniqueTestDBName(t)
	if _, err := rootDB.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS `"+dbName+"`"); err != nil {
		t.Fatalf("create database: %v", err)
	}

	dbDSN := doltutil.ServerDSN{Host: "127.0.0.1", Port: testServerPort, User: "root", Database: dbName}.String()
	db, err := sql.Open("mysql", dbDSN)
	if err != nil {
		t.Fatalf("open db connection: %v", err)
	}
	defer db.Close()

	// 1. Fully initialize. After 0041 runs, dolt_nonlocal_tables is empty
	//    (0040 added its four rows; 0041 deleted them all) — the exact table
	//    state a partial 0041 leaves behind.
	if _, err := initSchemaOnDB(ctx, db); err != nil {
		t.Fatalf("initial initSchemaOnDB: %v", err)
	}
	latest := schema.LatestVersion()
	assertSchemaVersionAtLeast(ctx, t, db, latest)

	// 2. Forge the partial-0041 brick: rewind the cursor to 40 (0040 recorded,
	//    0041 not) with dolt_nonlocal_tables already empty and committed.
	if _, err := db.ExecContext(ctx, "DELETE FROM schema_migrations WHERE version >= 41"); err != nil {
		t.Fatalf("rewind schema_migrations to 40: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"CALL DOLT_COMMIT('-Am', 'forge partial 0041: cursor at 40, nonlocal table empty', '--skip-empty')"); err != nil {
		t.Fatalf("commit forged partial state: %v", err)
	}

	// 3. Re-init. Pre-fix this died replaying 0041's DELETE over an empty table
	//    ("nothing to commit"); post-fix the version-41 repair restores the rows
	//    so the frozen DELETE+COMMIT has a diff and the chain reaches latest.
	if _, err := initSchemaOnDB(ctx, db); err != nil {
		t.Fatalf("re-init after forged partial 0041 (this is the brick): %v", err)
	}
	assertSchemaVersionAtLeast(ctx, t, db, latest)
}

func assertSchemaVersionAtLeast(ctx context.Context, t *testing.T, db *sql.DB, want int) {
	t.Helper()
	var got int
	if err := db.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&got); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if got < want {
		t.Fatalf("schema version = %d, want >= %d", got, want)
	}
}
