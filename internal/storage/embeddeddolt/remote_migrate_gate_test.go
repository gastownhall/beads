//go:build cgo

package embeddeddolt_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestEmbeddedRemoteMigrateGate_BlocksReopen verifies that the #4259
// remote-migrate gate also protects embedded mode (the mode the original report
// was filed against): reopening an existing, remote-backed embedded database
// that is behind the binary must refuse to auto-migrate.
func TestEmbeddedRemoteMigrateGate_BlocksReopen(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}
	t.Setenv(schema.AllowRemoteMigrateEnv, "0")

	ctx := t.Context()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	dataDir := filepath.Join(beadsDir, "embeddeddolt")

	// Create and fully migrate the embedded database.
	store, err := embeddeddolt.Open(ctx, beadsDir, "testdb", "main")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Regress one migration and register a remote on a raw SQL connection so the
	// reopen sees a behind, remote-backed database.
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, "testdb", "main")
	if err != nil {
		store.Close()
		t.Fatalf("OpenSQL: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"DELETE FROM schema_migrations WHERE version = ?", schema.LatestVersion()); err != nil {
		t.Fatalf("regress schema_migrations: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"CALL DOLT_REMOTE('add', 'origin', ?)", "file://"+filepath.Join(t.TempDir(), "remote")); err != nil {
		t.Fatalf("add remote: %v", err)
	}
	_ = cleanup()
	store.Close()

	// Reopen must hit the gate.
	reopened, reErr := embeddeddolt.Open(ctx, beadsDir, "testdb", "main")
	if reErr == nil {
		reopened.Close()
		t.Fatal("Open (reopen) = nil, want *schema.RemoteMigrateGateError for a behind, remote-backed DB")
	}
	if !schema.IsRemoteMigrateGateError(reErr) {
		t.Fatalf("error = %T (%v), want error wrapping *schema.RemoteMigrateGateError", reErr, reErr)
	}
}

// TestEmbeddedOpenReadOnly_SkipsGateAndMigrations covers bd-6dnrw.32: the
// read-only embedded open (used for cross-repo hydration, GH#3231) must not
// run the remote-migrate gate, must not write anything into the target's
// history or working set, and must refuse write transactions — while plain
// Open of the same behind, remote-backed database stays gated.
func TestEmbeddedOpenReadOnly_SkipsGateAndMigrations(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}
	t.Setenv(schema.AllowRemoteMigrateEnv, "0")

	ctx := t.Context()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	dataDir := filepath.Join(beadsDir, "embeddeddolt")

	// Create and fully migrate the embedded database, with a marker config row.
	store, err := embeddeddolt.Open(ctx, beadsDir, "testdb", "main")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := store.SetConfig(ctx, "issue_prefix", "ro"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	store.Close()

	snapshot := func() (commits, dirty int) {
		db, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, "testdb", "main")
		if err != nil {
			t.Fatalf("OpenSQL: %v", err)
		}
		defer func() { _ = cleanup() }()
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_log").Scan(&commits); err != nil {
			t.Fatalf("count dolt_log: %v", err)
		}
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_status").Scan(&dirty); err != nil {
			t.Fatalf("count dolt_status: %v", err)
		}
		return commits, dirty
	}
	commitsBefore, dirtyBefore := snapshot()

	// Read-only open of an up-to-date database: reads work, writes are refused.
	ro, err := embeddeddolt.OpenReadOnly(ctx, beadsDir, "testdb", "main")
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	if got, err := ro.GetConfig(ctx, "issue_prefix"); err != nil || got != "ro" {
		t.Fatalf("GetConfig = %q, %v; want %q, nil", got, err, "ro")
	}
	if err := ro.SetConfig(ctx, "issue_prefix", "mutated"); err == nil {
		t.Fatal("SetConfig on read-only store = nil error, want refusal")
	}
	if _, err := ro.ApplySchemaMigrations(ctx); err == nil {
		t.Fatal("ApplySchemaMigrations on read-only store = nil error, want refusal")
	}
	// Version-control mutations bypass withConn's commit guard (they run via
	// withDBConn, outside any SQL transaction) and must be refused too
	// (bd-578h9.12).
	if err := ro.Branch(ctx, "ro-test-branch"); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("Branch on read-only store = %v, want read-only refusal", err)
	}
	if err := ro.Push(ctx); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("Push on read-only store = %v, want read-only refusal", err)
	}
	if _, err := ro.Merge(ctx, "main"); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("Merge on read-only store = %v, want read-only refusal", err)
	}
	ro.Close()

	if commitsAfter, dirtyAfter := snapshot(); commitsAfter != commitsBefore || dirtyAfter != dirtyBefore {
		t.Fatalf("read-only open mutated the database: commits %d -> %d, dirty tables %d -> %d",
			commitsBefore, commitsAfter, dirtyBefore, dirtyAfter)
	}

	// Make the database behind and remote-backed: plain Open must hit the gate,
	// the read-only open must not.
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, "testdb", "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"DELETE FROM schema_migrations WHERE version = ?", schema.LatestVersion()); err != nil {
		t.Fatalf("regress schema_migrations: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"CALL DOLT_REMOTE('add', 'origin', ?)", "file://"+filepath.Join(t.TempDir(), "remote")); err != nil {
		t.Fatalf("add remote: %v", err)
	}
	_ = cleanup()

	if gated, gateErr := embeddeddolt.Open(ctx, beadsDir, "testdb", "main"); gateErr == nil {
		gated.Close()
		t.Fatal("Open of behind, remote-backed DB = nil error, want remote-migrate gate")
	} else if !schema.IsRemoteMigrateGateError(gateErr) {
		t.Fatalf("Open error = %T (%v), want *schema.RemoteMigrateGateError", gateErr, gateErr)
	}

	// The read-only open stays exempt from the remote-migrate gate, but a
	// behind database now fails at open with a clear behind-drift error
	// instead of unknown-column errors at query time (bd-578h9.12)...
	if ro2, roErr := embeddeddolt.OpenReadOnly(ctx, beadsDir, "testdb", "main"); roErr == nil {
		ro2.Close()
		t.Fatal("OpenReadOnly of behind DB = nil error, want *schema.SchemaBehindError")
	} else if schema.IsRemoteMigrateGateError(roErr) {
		t.Fatalf("OpenReadOnly error = remote-migrate gate (%v); read-only opens must stay exempt from it", roErr)
	} else if !schema.IsSchemaBehindError(roErr) {
		t.Fatalf("OpenReadOnly error = %T (%v), want *schema.SchemaBehindError", roErr, roErr)
	}

	// ...and BD_IGNORE_SCHEMA_SKEW=1 keeps the escape hatch open.
	t.Setenv("BD_IGNORE_SCHEMA_SKEW", "1")
	ro2, err := embeddeddolt.OpenReadOnly(ctx, beadsDir, "testdb", "main")
	if err != nil {
		t.Fatalf("OpenReadOnly (behind, skew ignored) = %v, want nil", err)
	}
	if got, err := ro2.GetConfig(ctx, "issue_prefix"); err != nil || got != "ro" {
		t.Fatalf("GetConfig after skew-ignored open = %q, %v; want %q, nil", got, err, "ro")
	}
	ro2.Close()
}

// TestEmbeddedOpenForReadOnlyCommand_LenientGate covers bd-578h9.5: a
// read-only command's open of a behind, remote-backed embedded database must
// not be bricked by the remote-migrate gate. The open succeeds WITHOUT
// migrating (the schema stays behind), while plain Open of the same database
// stays gated.
func TestEmbeddedOpenForReadOnlyCommand_LenientGate(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}
	t.Setenv(schema.AllowRemoteMigrateEnv, "0")

	ctx := t.Context()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	dataDir := filepath.Join(beadsDir, "embeddeddolt")

	// Create and fully migrate the embedded database.
	store, err := embeddeddolt.Open(ctx, beadsDir, "testdb", "main")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Regress one migration and register a remote so the reopen sees a
	// behind, remote-backed database.
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, "testdb", "main")
	if err != nil {
		store.Close()
		t.Fatalf("OpenSQL: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"DELETE FROM schema_migrations WHERE version = ?", schema.LatestVersion()); err != nil {
		t.Fatalf("regress schema_migrations: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"CALL DOLT_REMOTE('add', 'origin', ?)", "file://"+filepath.Join(t.TempDir(), "remote")); err != nil {
		t.Fatalf("add remote: %v", err)
	}
	_ = cleanup()
	store.Close()

	// Plain Open must stay gated.
	if gated, gateErr := embeddeddolt.Open(ctx, beadsDir, "testdb", "main"); gateErr == nil {
		gated.Close()
		t.Fatal("Open (reopen) = nil, want remote-migrate gate refusal")
	} else if !schema.IsRemoteMigrateGateError(gateErr) {
		t.Fatalf("Open error = %T (%v), want *schema.RemoteMigrateGateError", gateErr, gateErr)
	}

	// The read-only-command open must succeed without migrating.
	roStore, roErr := embeddeddolt.OpenForReadOnlyCommand(ctx, beadsDir, "testdb", "main")
	if roErr != nil {
		t.Fatalf("OpenForReadOnlyCommand: %v", roErr)
	}
	defer roStore.Close()

	db2, cleanup2, err := embeddeddolt.OpenSQL(ctx, dataDir, "testdb", "main")
	if err != nil {
		t.Fatalf("OpenSQL (verify): %v", err)
	}
	defer func() { _ = cleanup2() }()
	var count int
	if err := db2.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM schema_migrations WHERE version = ?", schema.LatestVersion()).Scan(&count); err != nil {
		t.Fatalf("read schema_migrations: %v", err)
	}
	if count != 0 {
		t.Fatalf("lenient-gate open applied the pending migration (version %d present); it must not migrate", schema.LatestVersion())
	}
}
