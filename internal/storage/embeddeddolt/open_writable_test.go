//go:build cgo

package embeddeddolt_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestEmbeddedOpenWritable_SkipsGateAndMigrations verifies that OpenWritable
// skips the remote-migrate gate (bd-6dnrw.32 / GH#4259) and does not run
// MigrateUp, matching OpenReadOnly's gate exemption.
func TestEmbeddedOpenWritable_SkipsGateAndMigrations(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}
	t.Setenv(schema.AllowRemoteMigrateEnv, "0")

	ctx := t.Context()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	dataDir := filepath.Join(beadsDir, "embeddeddolt")

	store, err := embeddeddolt.Open(ctx, beadsDir, "testdb", "main")
	if err != nil {
		t.Fatalf("Open: %v", err)
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

	// Make the DB behind and add a remote so a plain Open hits the gate.
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, "testdb", "main")
	if err != nil {
		t.Fatalf("OpenSQL setup: %v", err)
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

	commitsBefore, dirtyBefore := snapshot()

	// Sanity check: plain Open is gated.
	if gated, gateErr := embeddeddolt.Open(ctx, beadsDir, "testdb", "main"); gateErr == nil {
		gated.Close()
		t.Fatal("Open of behind, remote-backed DB = nil, want RemoteMigrateGateError")
	} else if !schema.IsRemoteMigrateGateError(gateErr) {
		t.Fatalf("Open error = %T (%v), want RemoteMigrateGateError", gateErr, gateErr)
	}

	// OpenWritable is gate-exempt: error is RoutedSchemaBehindError, not the gate.
	_, wErr := embeddeddolt.OpenWritable(ctx, beadsDir, "testdb", "main")
	if schema.IsRemoteMigrateGateError(wErr) {
		t.Fatalf("OpenWritable returned gate error, want RoutedSchemaBehindError: %v", wErr)
	}
	if !embeddeddolt.IsRoutedSchemaBehindError(wErr) {
		t.Fatalf("OpenWritable error = %T (%v), want RoutedSchemaBehindError", wErr, wErr)
	}

	// No Dolt commits or new dirty tables — migrations did not run.
	if after, dirtyAfter := snapshot(); after != commitsBefore || dirtyAfter != dirtyBefore {
		t.Fatalf("Dolt state changed: commits %d→%d, dirty %d→%d; OpenWritable must not run migrations",
			commitsBefore, after, dirtyBefore, dirtyAfter)
	}
}

// TestEmbeddedOpenWritable_AllowsWriteTransactions verifies that a store
// opened via OpenWritable accepts write transactions (SetConfig succeeds).
func TestEmbeddedOpenWritable_AllowsWriteTransactions(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}

	ctx := t.Context()
	beadsDir := filepath.Join(t.TempDir(), ".beads")

	store, err := embeddeddolt.Open(ctx, beadsDir, "testdb", "main")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	store.Close()

	wo, err := embeddeddolt.OpenWritable(ctx, beadsDir, "testdb", "main")
	if err != nil {
		t.Fatalf("OpenWritable: %v", err)
	}
	defer wo.Close()

	if err := wo.SetConfig(ctx, "routed_write_key", "routed_write_val"); err != nil {
		t.Fatalf("SetConfig on writable store: %v", err)
	}
	got, err := wo.GetConfig(ctx, "routed_write_key")
	if err != nil || got != "routed_write_val" {
		t.Fatalf("GetConfig = %q, %v; want %q, nil", got, err, "routed_write_val")
	}
}

// TestEmbeddedOpenWritable_RejectsSchemaAheadDB verifies that OpenWritable
// returns a SchemaSkewError when the target database schema is ahead of the binary.
func TestEmbeddedOpenWritable_RejectsSchemaAheadDB(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}

	ctx := t.Context()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	dataDir := filepath.Join(beadsDir, "embeddeddolt")

	store, err := embeddeddolt.Open(ctx, beadsDir, "testdb", "main")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	store.Close()

	// Advance schema_migrations beyond the binary's expected version.
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, "testdb", "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"INSERT INTO schema_migrations (version) VALUES (?)", schema.LatestVersion()+1); err != nil {
		t.Fatalf("advance schema_migrations: %v", err)
	}
	_ = cleanup()

	_, wErr := embeddeddolt.OpenWritable(ctx, beadsDir, "testdb", "main")
	if !schema.IsSchemaSkewError(wErr) {
		t.Fatalf("OpenWritable error = %T (%v), want SchemaSkewError", wErr, wErr)
	}
}

// TestEmbeddedOpenWritable_RejectsSchemaBehindNonZeroDB verifies that
// OpenWritable returns a RoutedSchemaBehindError when the target schema is
// behind the binary and the stored version is non-zero (initialized DB).
func TestEmbeddedOpenWritable_RejectsSchemaBehindNonZeroDB(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}

	ctx := t.Context()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	dataDir := filepath.Join(beadsDir, "embeddeddolt")

	store, err := embeddeddolt.Open(ctx, beadsDir, "testdb", "main")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	store.Close()

	// Roll back the latest migration (non-zero version, but behind latest).
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, "testdb", "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"DELETE FROM schema_migrations WHERE version = ?", schema.LatestVersion()); err != nil {
		t.Fatalf("regress schema_migrations: %v", err)
	}
	_ = cleanup()

	_, wErr := embeddeddolt.OpenWritable(ctx, beadsDir, "testdb", "main")
	if !embeddeddolt.IsRoutedSchemaBehindError(wErr) {
		t.Fatalf("OpenWritable error = %T (%v), want RoutedSchemaBehindError", wErr, wErr)
	}
}

// TestEmbeddedOpenWritable_FreshDBSkipsBackwardDriftCheck verifies that
// OpenWritable succeeds when schema_migrations is empty (version=0), treating
// the target as uninitialized and skipping the backward-drift rejection.
func TestEmbeddedOpenWritable_FreshDBSkipsBackwardDriftCheck(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}

	ctx := t.Context()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	dataDir := filepath.Join(beadsDir, "embeddeddolt")

	store, err := embeddeddolt.Open(ctx, beadsDir, "testdb", "main")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	store.Close()

	// Simulate a pre-initSchema state: clear all migration rows and insert
	// version=0. Dolt returns sql.ErrNoRows for COALESCE(MAX()) on an empty
	// table (unlike standard MySQL), so a bare DELETE would cause
	// CheckForwardDrift to fail before checkBackwardDrift runs. Version=0
	// is the sentinel both checks treat as "fresh DB — skip".
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, "testdb", "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM schema_migrations"); err != nil {
		t.Fatalf("clear schema_migrations: %v", err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO schema_migrations (version) VALUES (0)"); err != nil {
		t.Fatalf("insert v0 sentinel: %v", err)
	}
	_ = cleanup()

	wo, err := embeddeddolt.OpenWritable(ctx, beadsDir, "testdb", "main")
	if err != nil {
		t.Fatalf("OpenWritable on v=0 DB = %v; want nil (fresh DB skips backward-drift check)", err)
	}
	wo.Close()
}
