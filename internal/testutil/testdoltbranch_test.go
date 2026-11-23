package testutil

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql" // MySQL driver for direct DB connections
	"github.com/steveyegge/beads/internal/storage/doltutil"
)

// TestSetupSharedTestDB_RefusesAmbientPortMismatch guards the same class of
// bug as TestDoltContainerStartSites_SetBeadsServerPortEnv: if something
// upstream in the resolution chain ever passes a port that disagrees with
// the ambient BEADS_DOLT_SERVER_PORT, SetupSharedTestDB must refuse rather
// than silently create a database against whichever server
// BEADS_DOLT_SERVER_PORT happens to point at. This check must not require a
// real Dolt server: it has to fire before any connection is attempted, the
// same way the existing production-port (3307) firewall does.
func TestSetupSharedTestDB_RefusesAmbientPortMismatch(t *testing.T) {
	t.Setenv("BEADS_DOLT_SERVER_PORT", "19191") // decoy ambient shared server

	db, err := SetupSharedTestDB(29292, "irrelevant_db") // disagrees with ambient port above
	if db != nil {
		_ = db.Close()
	}
	if err == nil {
		t.Fatal("SetupSharedTestDB: expected refusal when port disagrees with ambient BEADS_DOLT_SERVER_PORT, got nil error")
	}
	if !strings.Contains(err.Error(), "BEADS_DOLT_SERVER_PORT") {
		t.Errorf("SetupSharedTestDB error = %q, want it to name the ambient BEADS_DOLT_SERVER_PORT disagreement", err.Error())
	}
}

// neverCreatedDBName returns a per-test database name with a random suffix
// that is guaranteed not to collide with any database CREATEd elsewhere in
// the suite — mirrors uniqueSetupSharedDBName in
// internal/storage/dolt/setup_shared_db_test.go (a different package, so not
// directly reusable here).
func neverCreatedDBName(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	return "test_never_created_" + hex.EncodeToString(buf)
}

// TestWaitForDatabaseVisible_ReturnsBoundedErrorWhenNeverCreated pins the
// bounded-failure contract that makes the visibility-probe fix falsifiable
// (bee-ghosttrack R2 item 1): for a database name that is never CREATEd,
// waitForDatabaseVisible must return the "not visible after" error within a
// bound close to its documented ~10s deadline — it must neither hang nor
// silently report success.
//
// This is a falsifiability guard, not an old-vs-new discriminator for the
// bug it replaces: a name that is never created is a *permanent* absence,
// and the old USE-based poll's "unknown database" retry branch already
// handled that correctly by coincidence (measured ~13s, correct error). The
// actual bug only showed up in the *racy* just-created case — USE can
// return success on attempt 1 because Dolt sometimes auto-attaches a
// database to a session before the server catalog (what SHOW DATABASES and
// dolt.New() actually check) has registered it — and R2 found that race too
// flaky to assert on directly (a mutation-probe experiment against the
// fresh-connection test was non-deterministic, not reliably red). What this
// test does guard is a future regression: an implementation that
// unconditionally reports success would fail it, even though the USE-based
// poll it replaces does not.
func TestWaitForDatabaseVisible_ReturnsBoundedErrorWhenNeverCreated(t *testing.T) {
	RequireDoltContainer(t)

	dsn := doltutil.ServerDSN{Host: "127.0.0.1", Port: DoltContainerPortInt(), User: "root", Timeout: 10 * time.Second}.String()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	dbName := neverCreatedDBName(t)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	const hangBound = 15 * time.Second
	start := time.Now()
	done := make(chan error, 1)
	go func() {
		done <- waitForDatabaseVisible(ctx, db, dbName)
	}()

	select {
	case err := <-done:
		elapsed := time.Since(start)
		if err == nil {
			t.Fatalf("waitForDatabaseVisible(%q): got nil error after %s for a database that was never created — visibility check is not strict enough (silent success)", dbName, elapsed)
		}
		if !strings.Contains(err.Error(), "not visible after") {
			t.Errorf("waitForDatabaseVisible(%q) error = %q, want it to contain %q (the bounded-deadline error)", dbName, err.Error(), "not visible after")
		}
		if elapsed > hangBound {
			t.Errorf("waitForDatabaseVisible(%q) took %s to return, want it bounded near the documented ~10s deadline", dbName, elapsed)
		}
	case <-time.After(hangBound):
		t.Fatalf("waitForDatabaseVisible(%q): did not return within %s — appears to hang instead of returning a bounded error", dbName, hangBound)
	}
}
