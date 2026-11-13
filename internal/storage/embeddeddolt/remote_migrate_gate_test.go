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
	// Blunt-gate coverage: pin the (default-on) smart router off.
	t.Setenv(schema.SmartGateEnv, "0")

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
	// Blunt-gate coverage: pin the (default-on) smart router off.
	t.Setenv(schema.SmartGateEnv, "0")

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

// TestEmbeddedOpenReadOnly_FreshDatabaseNoWrites covers the fresh-database
// half of the strict --readonly hermeticity contract (gastownhall/beads#4930
// maintainer review, 2026-07-23): OpenReadOnly against a beadsDir that has no
// embeddeddolt/ data directory yet must fail rather than create one — unlike
// Open/OpenForReadOnlyCommand, which create the data directory and initialize
// the schema on first use.
func TestEmbeddedOpenReadOnly_FreshDatabaseNoWrites(t *testing.T) {
	ctx := t.Context()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	dataDir := filepath.Join(beadsDir, "embeddeddolt")

	if _, err := os.Stat(beadsDir); !os.IsNotExist(err) {
		t.Fatalf("beadsDir must not pre-exist: stat error = %v", err)
	}

	ro, err := embeddeddolt.OpenReadOnly(ctx, beadsDir, "testdb", "main")
	if err == nil {
		ro.Close()
		t.Fatal("OpenReadOnly on a fresh (nonexistent) database = nil error, want refusal")
	}

	if _, statErr := os.Stat(dataDir); !os.IsNotExist(statErr) {
		t.Fatalf("OpenReadOnly on a fresh database created %s (stat error: %v)", dataDir, statErr)
	}
	if _, statErr := os.Stat(beadsDir); !os.IsNotExist(statErr) {
		t.Fatalf("OpenReadOnly on a fresh database created %s (stat error: %v)", beadsDir, statErr)
	}
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
	// Blunt-gate coverage: pin the (default-on) smart router off.
	t.Setenv(schema.SmartGateEnv, "0")

	ctx := t.Context()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	dataDir := filepath.Join(beadsDir, "embeddeddolt")

	// Create and fully migrate the embedded database.
	store, err := embeddeddolt.Open(ctx, beadsDir, "testdb", "main")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Regress the cursor to just below 0073 (arrival_seq) and register a
	// remote so the reopen sees a behind, remote-backed database. Not just
	// one migration back: the dirty-table refusal at the end needs 0073,
	// which alters issues, among the pending migrations, and a migration
	// recorded above it would hold the cursor past it.
	const arrivalSeqVersion = 73
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, "testdb", "main")
	if err != nil {
		store.Close()
		t.Fatalf("OpenSQL: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"DELETE FROM schema_migrations WHERE version >= ?", arrivalSeqVersion); err != nil {
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
	roStore, roErr := embeddeddolt.OpenForReadOnlyCommand(ctx, beadsDir, "testdb", "main", false)
	if roErr != nil {
		t.Fatalf("OpenForReadOnlyCommand: %v", roErr)
	}
	roStore.Close()

	db2, cleanup2, err := embeddeddolt.OpenSQL(ctx, dataDir, "testdb", "main")
	if err != nil {
		t.Fatalf("OpenSQL (verify): %v", err)
	}
	var count int
	if err := db2.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM schema_migrations WHERE version = ?", schema.LatestVersion()).Scan(&count); err != nil {
		_ = cleanup2()
		t.Fatalf("read schema_migrations: %v", err)
	}
	if count != 0 {
		_ = cleanup2()
		t.Fatalf("lenient-gate open applied the pending migration (version %d present); it must not migrate", schema.LatestVersion())
	}

	// The read-through ends where the reads would fail: a schema that lacks a
	// column this binary's issue reads select (arrival_seq, the shape a
	// database the previous release migrated has) is refused at open instead
	// of warned about and then failed on every query.
	for _, stmt := range []string{
		"ALTER TABLE issues DROP COLUMN arrival_seq",
		"ALTER TABLE wisps DROP COLUMN arrival_seq",
	} {
		if _, err := db2.ExecContext(ctx, stmt); err != nil {
			_ = cleanup2()
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	_ = cleanup2()
	if unreadable, err := embeddeddolt.OpenForReadOnlyCommand(ctx, beadsDir, "testdb", "main", false); err == nil {
		unreadable.Close()
		t.Fatal("OpenForReadOnlyCommand of a schema without arrival_seq = nil error, want *schema.SchemaBehindError")
	} else if !schema.IsSchemaBehindError(err) {
		t.Fatalf("OpenForReadOnlyCommand error = %T (%v), want *schema.SchemaBehindError", err, err)
	}

	// The same limit holds for the other refusal a read-only command
	// tolerates. With the remote gone and consent given, the gate passes and
	// the migration is refused by the #4566 dirty-table guard instead: the
	// DROP COLUMNs above left issues dirty, and the pending 0073 alters it.
	db3, cleanup3, err := embeddeddolt.OpenSQL(ctx, dataDir, "testdb", "main")
	if err != nil {
		t.Fatalf("OpenSQL (remove remote): %v", err)
	}
	if _, err := db3.ExecContext(ctx, "CALL DOLT_REMOTE('remove', 'origin')"); err != nil {
		_ = cleanup3()
		t.Fatalf("remove remote: %v", err)
	}
	_ = cleanup3()
	t.Setenv(schema.AllowMigrateEnv, "1")
	if unreadable, err := embeddeddolt.OpenForReadOnlyCommand(ctx, beadsDir, "testdb", "main", false); err == nil {
		unreadable.Close()
		t.Fatal("OpenForReadOnlyCommand past a dirty-table refusal of a schema without arrival_seq = nil error, want *schema.SchemaBehindError")
	} else if !schema.IsSchemaBehindError(err) {
		t.Fatalf("OpenForReadOnlyCommand past a dirty-table refusal: error = %T (%v), want *schema.SchemaBehindError", err, err)
	}
}
