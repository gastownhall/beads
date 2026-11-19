package dolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestDoltNew_SharedStoreMigrateGate_NoRemote is the server-mode half of
// gastownhall/beads#5920: a dolt sql-server database with NO remote at all.
//
// Before this gate, the first upgraded client to open such a database writably
// migrated it silently, promoting the schema cursor for every co-resident bd
// client on the same server — which then refused the database until each was
// upgraded too (~14 minutes of a locked-out team, in the incident). A
// sql-server store serves co-resident clients by construction, so the refusal
// does not depend on a remote existing.
//
// One CREATE/DROP DATABASE cycle covers the whole matrix: consecutive cycles
// destabilize the test dolt server, so the states are walked in order on a
// single database.
func TestDoltNew_SharedStoreMigrateGate_NoRemote(t *testing.T) {
	skipIfNoDolt(t)
	t.Setenv(schema.AllowRemoteMigrateEnv, "0")
	t.Setenv(schema.SmartGateEnv, "0")
	t.Cleanup(func() {
		schema.SetSharedMigrateConsent(false)
		schema.SetForceAllowRemoteMigrate(false)
	})

	ctx, cancel := testContext(t)
	defer cancel()

	tmpDir := t.TempDir()
	dbName := uniqueTestDBName(t)

	store, err := New(ctx, &Config{
		Path:            tmpDir,
		CommitterName:   "test",
		CommitterEmail:  "test@example.com",
		Database:        dbName,
		CreateIfMissing: true,
	})
	if err != nil {
		t.Fatalf("New (create): %v", err)
	}
	defer func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 5*testTimeout)
		defer dropCancel()
		_, _ = store.db.ExecContext(dropCtx, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", dbName))
		store.Close()
	}()

	// Precondition: no remote anywhere. This is the state the ordinary #4259
	// gate allows straight through.
	if remotes, err := store.ListRemotes(ctx); err != nil {
		t.Fatalf("ListRemotes: %v", err)
	} else if len(remotes) != 0 {
		t.Fatalf("precondition failed: %d remote(s) registered; this test covers the no-remote case", len(remotes))
	}

	// Put the database one migration behind the binary by regressing only the
	// recorded cursor, exactly as the #4259 integration test does.
	if _, err := store.db.ExecContext(ctx,
		"DELETE FROM schema_migrations WHERE version = ?", schema.LatestVersion()); err != nil {
		t.Fatalf("regress schema_migrations: %v", err)
	}

	openWith := func(cfg *Config) error {
		cfg.Path = tmpDir
		cfg.CommitterName = "test"
		cfg.CommitterEmail = "test@example.com"
		cfg.Database = dbName
		s, err := New(ctx, cfg)
		if err == nil {
			s.Close()
		}
		return err
	}
	cursor := func() int {
		var v int
		if err := store.db.QueryRowContext(ctx,
			"SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&v); err != nil {
			t.Fatalf("read schema cursor: %v", err)
		}
		return v
	}

	behind := cursor()
	if behind != schema.LatestVersion()-1 {
		t.Fatalf("precondition failed: cursor = %d, want %d", behind, schema.LatestVersion()-1)
	}

	t.Run("writable open refuses without consent", func(t *testing.T) {
		err := openWith(&Config{})
		var gateErr *schema.RemoteMigrateGateError
		if !errors.As(err, &gateErr) {
			t.Fatalf("New (writable, behind, no remote) = %T (%v), want *schema.RemoteMigrateGateError", err, err)
		}
		if gateErr.Decision != "shared-no-remote" {
			t.Errorf("Decision = %q, want %q", gateErr.Decision, "shared-no-remote")
		}
		if got := cursor(); got != behind {
			t.Fatalf("the refused open promoted the cursor to %d (was %d) — this is the #5920 regression", got, behind)
		}
	})

	// The bd-578h9.5 contract: reads must keep working on the old schema while
	// the operator decides. Read-only opens never touch the schema at all.
	t.Run("read-only open still succeeds", func(t *testing.T) {
		if err := openWith(&Config{ReadOnly: true}); err != nil {
			t.Fatalf("New (read-only, behind, no remote) = %v, want success", err)
		}
		if got := cursor(); got != behind {
			t.Fatalf("read-only open promoted the cursor to %d (was %d)", got, behind)
		}
	})

	t.Run("verb consent migrates", func(t *testing.T) {
		schema.SetSharedMigrateConsent(true)
		defer schema.SetSharedMigrateConsent(false)
		if err := openWith(&Config{}); err != nil {
			t.Fatalf("New (writable, consented) = %v, want success", err)
		}
		if got := cursor(); got != schema.LatestVersion() {
			t.Fatalf("cursor = %d after a consented open, want %d", got, schema.LatestVersion())
		}
	})
}

// TestDoltNew_SharedStoreMigrateGate_RefusalWaitsOutInFlightPass is the
// storage half of GH#7122. A migration pass holds the database's migration
// lock and commits each step as it goes, so an open that reads the cursor
// while another session's pass is running sees migrations pending on an
// existing database. On a shared store the gate then refused with #5920's
// migration-consent advice, though nobody needed to consent: the pass (the
// init that just created the database, say) finished seconds later. Once
// ordinary shared-server inits stopped serializing on the shared root, a
// second init of the same database name, and every init's open of the shared
// global database, ran into exactly this.
//
// The cursor is held one step short of latest throughout, the state a pass
// leaves mid-way, and the test session plays the migrating session by holding
// the lock. The refusal now waits for the lock and is read again under it:
//
//   - no pass in flight: the refusal is final, as before;
//   - a pass that dies part-way (the lock is released with the cursor still
//     short): the locked read refuses too, so a stranded database still needs
//     consent;
//   - a pass that finishes: the open waits it out and goes on with nothing
//     left to apply.
func TestDoltNew_SharedStoreMigrateGate_RefusalWaitsOutInFlightPass(t *testing.T) {
	skipIfNoDolt(t)
	// SHOW PROCESSLIST below must see this test's GET_LOCK waiter only.
	acquireAllTestSlots()
	t.Cleanup(releaseAllTestSlots)
	t.Setenv(schema.AllowRemoteMigrateEnv, "0")
	t.Setenv(schema.SmartGateEnv, "0")

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
		t.Fatalf("New (create): %v", err)
	}
	defer func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 5*testTimeout)
		defer dropCancel()
		_, _ = store.db.ExecContext(dropCtx, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", dbName))
		store.Close()
	}()
	// No BeadsDir, so the store is classified shared (sharedServerDatabase):
	// the #5920 gate this is about.
	if !sharedServerDatabase(store.cfg) {
		t.Fatal("precondition failed: the store is not classified as a shared server database")
	}

	// The migrating session. Its pass is mid-way: the last step has not
	// committed, so the cursor is one short of latest.
	migrator, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin the migrating session: %v", err)
	}
	defer migrator.Close()
	var migratorID int64
	if err := migrator.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&migratorID); err != nil {
		t.Fatalf("read the migrating session's connection id: %v", err)
	}
	latest := schema.LatestVersion()
	var lastStepHash sql.NullString
	if err := migrator.QueryRowContext(ctx,
		"SELECT content_hash FROM schema_migrations WHERE version = ?", latest).Scan(&lastStepHash); err != nil {
		t.Fatalf("read the last migration step's cursor row: %v", err)
	}
	if _, err := migrator.ExecContext(ctx, "DELETE FROM schema_migrations WHERE version = ?", latest); err != nil {
		t.Fatalf("hold the cursor short of the last step: %v", err)
	}
	cursor := func() int {
		t.Helper()
		var v int
		if err := migrator.QueryRowContext(ctx,
			"SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&v); err != nil {
			t.Fatalf("read schema cursor: %v", err)
		}
		return v
	}
	if got := cursor(); got != latest-1 {
		t.Fatalf("precondition failed: cursor = %d, want %d", got, latest-1)
	}

	lockName := schema.MigrationLockName(dbName)
	setLock := func(query string) {
		t.Helper()
		var ok sql.NullInt64
		if err := migrator.QueryRowContext(ctx, query, lockName).Scan(&ok); err != nil || !ok.Valid || ok.Int64 != 1 {
			t.Fatalf("%s on %s = %v (err %v), want 1", query, lockName, ok, err)
		}
	}
	observer, err := sql.Open("mysql", doltutil.ServerDSN{Host: "127.0.0.1", Port: testServerPort, User: "root"}.String())
	if err != nil {
		t.Fatalf("open the processlist observer: %v", err)
	}
	defer observer.Close()

	// open runs the schema init of a writable open of the database (the gate
	// and the migration, inside their retry loop) and delivers its result.
	open := func() <-chan error {
		done := make(chan error, 1)
		go func() {
			_, err := store.initSchema(ctx, nil)
			done <- err
		}()
		return done
	}
	result := func(done <-chan error) error {
		t.Helper()
		select {
		case err := <-done:
			return err
		case <-time.After(60 * time.Second):
			t.Fatal("the open did not return after the migrating session released the lock")
			return nil
		}
	}
	wantRefusal := func(err error) {
		t.Helper()
		var gateErr *schema.RemoteMigrateGateError
		if !errors.As(err, &gateErr) || gateErr.Decision != "shared-no-remote" {
			t.Fatalf("open = %T (%v), want the shared-no-remote *schema.RemoteMigrateGateError", err, err)
		}
		if got := cursor(); got != latest-1 {
			t.Fatalf("the refused open moved the cursor to %d (was %d)", got, latest-1)
		}
	}

	t.Run("no pass in flight: the refusal is final", func(t *testing.T) {
		wantRefusal(result(open()))
	})

	t.Run("a pass that dies part-way still refuses once its lock is free", func(t *testing.T) {
		setLock("SELECT GET_LOCK(?, 0)")
		done := open()
		// Before GH#7122 the open refused here, while the pass still ran.
		waitForMigrationLockWaiter(t, ctx, observer, migratorID, done)
		setLock("SELECT RELEASE_LOCK(?)")
		wantRefusal(result(done))
	})

	t.Run("a pass that finishes is waited out", func(t *testing.T) {
		setLock("SELECT GET_LOCK(?, 0)")
		done := open()
		waitForMigrationLockWaiter(t, ctx, observer, migratorID, done)
		// The pass's last step commits, then the pass lets go of the lock.
		if _, err := migrator.ExecContext(ctx,
			"INSERT INTO schema_migrations (version, content_hash) VALUES (?, ?)", latest, lastStepHash); err != nil {
			t.Fatalf("commit the last migration step: %v", err)
		}
		setLock("SELECT RELEASE_LOCK(?)")
		if err := result(done); err != nil {
			t.Fatalf("open after the in-flight pass finished = %v, want success", err)
		}
		if got := cursor(); got != latest {
			t.Fatalf("cursor = %d after the pass, want %d", got, latest)
		}
	})
}
