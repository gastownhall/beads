//go:build cgo

package main

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/doltutil"
)

// TestPurgeDroppedDatabasesReclaimsDisk is the regression gate for be-pq5:
// `bd dolt clean-databases` must not just DROP stale databases but also
// PURGE them, or their directories linger under .dolt_dropped_databases/
// until something else triggers a purge.
//
// Dolt exposes no SQL view listing dropped-but-not-purged databases (the
// only on-disk signal is a directory inside the server's opaque data dir,
// which the shared testcontainer-based test server does not expose to the
// test process). Instead this uses Dolt's own `dolt_undrop()` stored
// procedure as the observable: a DROP DATABASE without a following PURGE
// leaves the database restorable via `CALL DOLT_UNDROP(name)`; once
// DOLT_PURGE_DROPPED_DATABASES() has run, the same call fails because
// there is nothing left to restore.
func TestPurgeDroppedDatabasesReclaimsDisk(t *testing.T) {
	if testDoltServerPort == 0 {
		t.Skip("no test Dolt server running")
	}

	dsn := doltutil.ServerDSN{Host: "127.0.0.1", Port: testDoltServerPort, User: "root"}.String()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("connect to test dolt server: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	ctx := context.Background()

	t.Run("undrop fails after purge", func(t *testing.T) {
		name := fmt.Sprintf("benchdb_purgetest_%d", time.Now().UnixNano())
		mustExec(t, ctx, db, fmt.Sprintf("CREATE DATABASE `%s`", name))
		mustExec(t, ctx, db, fmt.Sprintf("DROP DATABASE `%s`", name))

		if err := purgeDroppedDatabases(ctx, db); err != nil {
			t.Fatalf("purgeDroppedDatabases: %v", err)
		}

		undropCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		_, err := db.ExecContext(undropCtx, fmt.Sprintf("CALL DOLT_UNDROP('%s')", name))
		if err == nil {
			// Undrop succeeded, meaning the database directory was still
			// present — the purge did not actually reclaim it. Clean up
			// the restored database before failing.
			mustExec(t, ctx, db, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", name))
			t.Fatalf("DOLT_UNDROP(%q) succeeded after purgeDroppedDatabases; database was not actually purged", name)
		}
	})

	// Control case: without a purge, the same DROP is restorable. This
	// pins the assertion methodology above — if Dolt ever changes so that
	// DOLT_UNDROP no longer distinguishes purged from merely-dropped
	// databases, this control catches it as a false negative in the
	// positive case above rather than a silently-vacuous test.
	t.Run("undrop succeeds without purge", func(t *testing.T) {
		name := fmt.Sprintf("benchdb_purgetest_%d", time.Now().UnixNano())
		mustExec(t, ctx, db, fmt.Sprintf("CREATE DATABASE `%s`", name))
		mustExec(t, ctx, db, fmt.Sprintf("DROP DATABASE `%s`", name))

		undropCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if _, err := db.ExecContext(undropCtx, fmt.Sprintf("CALL DOLT_UNDROP('%s')", name)); err != nil {
			t.Fatalf("DOLT_UNDROP(%q) failed without a purge in between: %v (assertion methodology in the purge test above may be invalid)", name, err)
		}

		// The undrop recreated the database; drop and purge it for real so
		// the test doesn't leak a benchdb_* database into the shared server.
		mustExec(t, ctx, db, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", name))
		if err := purgeDroppedDatabases(ctx, db); err != nil {
			t.Logf("cleanup purge after control case: %v", err)
		}
	})
}

func mustExec(t *testing.T, ctx context.Context, db *sql.DB, query string) {
	t.Helper()
	execCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := db.ExecContext(execCtx, query); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}
