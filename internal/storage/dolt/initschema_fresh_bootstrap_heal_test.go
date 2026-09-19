package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestFreshBootstrapHealSelfHealsAfterMidPassFailure is the server-mode
// (internal/storage/dolt) regression test for gastownhall/beads#5012,
// porting the proxied/uow path's
// TestNewExternalDoltServerUOWProvider_FreshInitSelfHealsAfterMidPassFailure
// (merged #5042) to initSchemaOnDBWithRetryAndGateOwnership.
//
// The shape of the failure: New's openServerConnection proves this open
// created the fresh target database (bare CREATE DATABASE succeeds), but the
// first migration attempt dies mid-pass — simulated here by a per-step fault
// hook that dirties a table right after migration 0001 commits and then
// fails with a retryable error, the same shape as a session dying between a
// step's SQL and its per-step Dolt commit. The outer retry
// (initSchemaOnDBWithRetryAndGateOwnership) re-runs MigrateUpWithLock; its
// #4566 dirty-table guard would normally treat that leftover debris as
// pre-existing dirty user data and fail the open permanently — unless the
// fresh-bootstrap ownership signal threaded from openServerConnection arms
// schema.WithFreshBootstrapHeal, in which case the guard discards the debris
// and the pass converges.
func TestFreshBootstrapHealSelfHealsAfterMidPassFailure(t *testing.T) {
	skipIfNoDolt(t)
	acquireTestSlot()
	t.Cleanup(releaseTestSlot)

	tmpDir, err := os.MkdirTemp("", "dolt-fresh-heal-*")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	dbName := uniqueTestDBName(t)

	fired := false
	restore := schema.SetMigrateStepFaultHookForTest(func(ctx context.Context, db schema.DBConn, version int) error {
		if version != 1 || fired {
			return nil
		}
		fired = true
		if _, err := db.ExecContext(ctx, "ALTER TABLE issues ADD COLUMN bd_freshheal_debris INT"); err != nil {
			return fmt.Errorf("injecting mid-step debris: %w", err)
		}
		// Message text must land in isRetryableError's substring
		// classification (internal/storage/dolt/store.go matches on
		// err.Error() text, unlike the uow path's mysql-error-code-based
		// isSerializationError) so the outer retry loop re-attempts instead
		// of failing this open permanently on the very first attempt.
		return fmt.Errorf("test-injected session death: lost connection to MySQL server during query")
	})
	t.Cleanup(restore)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := &Config{
		Path:            tmpDir,
		CommitterName:   "test",
		CommitterEmail:  "test@example.com",
		Database:        dbName,
		MaxOpenConns:    1,
		CreateIfMissing: true, // fresh database: this open must win the create race
	}

	store, err := New(ctx, cfg)
	if err != nil {
		t.Fatalf("New must converge after a mid-pass transient failure on a database it created, "+
			"not trip the #4566 guard on its own bootstrap debris: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	if !fired {
		t.Fatal("fault hook never fired; test no longer exercises the mid-pass failure path")
	}

	var debrisCols int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM information_schema.columns WHERE table_name = 'issues' AND column_name = 'bd_freshheal_debris'",
	).Scan(&debrisCols); err != nil {
		t.Fatalf("count debris column: %v", err)
	}
	if debrisCols != 0 {
		t.Fatalf("bootstrap debris column survived the self-heal, count = %d", debrisCols)
	}

	var version int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(version), 0) FROM schema_migrations",
	).Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version != schema.LatestVersion() {
		t.Fatalf("schema version = %d, want %d (latest)", version, schema.LatestVersion())
	}

	var dirtyIssues int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dolt_status WHERE table_name = 'issues'",
	).Scan(&dirtyIssues); err != nil {
		t.Fatalf("count dirty issues rows: %v", err)
	}
	if dirtyIssues != 0 {
		t.Fatalf("issues still dirty in dolt_status after init, count = %d", dirtyIssues)
	}
}

// TestFreshBootstrapHealNotArmedWhenDatabasePreexists is the
// ownership-negative counterpart: an open that did NOT create the database
// (it already existed, so openServerConnection's bare CREATE DATABASE loses
// the race with "database exists") must keep the #4566 guard's refusal and
// must not DOLT_RESET the working set — the dirt it sees could be another
// actor's legitimate uncommitted state, which only the proven creator may
// discard.
func TestFreshBootstrapHealNotArmedWhenDatabasePreexists(t *testing.T) {
	skipIfNoDolt(t)
	acquireTestSlot()
	t.Cleanup(releaseTestSlot)

	if testServerPort == 0 {
		t.Skip("no Dolt test server available")
	}

	dbName := uniqueTestDBName(t)

	// Pre-create the database directly, outside of New(), so this test's own
	// open loses openServerConnection's bare-CREATE ownership race:
	// createdDatabase must come back false, and WithFreshBootstrapHeal must
	// not be armed.
	initDSN := doltutil.ServerDSN{Host: "127.0.0.1", Port: testServerPort, User: "root"}.String()
	initDB, err := sql.Open("mysql", initDSN)
	if err != nil {
		t.Fatalf("open init connection: %v", err)
	}
	defer initDB.Close()
	if _, err := initDB.ExecContext(context.Background(), "CREATE DATABASE IF NOT EXISTS `"+dbName+"`"); err != nil {
		t.Fatalf("pre-create database: %v", err)
	}

	tmpDir, err := os.MkdirTemp("", "dolt-fresh-heal-negative-*")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	fired := false
	restore := schema.SetMigrateStepFaultHookForTest(func(ctx context.Context, db schema.DBConn, version int) error {
		if version != 1 || fired {
			return nil
		}
		fired = true
		if _, err := db.ExecContext(ctx, "ALTER TABLE issues ADD COLUMN bd_freshheal_negative_debris INT"); err != nil {
			return fmt.Errorf("injecting mid-step debris: %w", err)
		}
		return fmt.Errorf("test-injected session death: lost connection to MySQL server during query")
	})
	t.Cleanup(restore)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg := &Config{
		Path:            tmpDir,
		CommitterName:   "test",
		CommitterEmail:  "test@example.com",
		Database:        dbName,
		MaxOpenConns:    1,
		CreateIfMissing: true,
	}

	store, err := New(ctx, cfg)
	if store != nil {
		store.Close()
	}
	if err == nil {
		t.Fatal("New must not silently heal a database it did not create; expected the #4566 guard's refusal")
	}
	if !strings.Contains(err.Error(), "dirty tables") {
		t.Fatalf("New error = %v, want the #4566 dirty-table guard refusal", err)
	}
	if !fired {
		t.Fatal("fault hook never fired; test no longer exercises the mid-pass failure path")
	}
}
