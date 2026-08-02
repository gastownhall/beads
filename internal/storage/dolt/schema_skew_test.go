package dolt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestCheckForwardDrift_ForwardDrift_ReturnsSchemaSkewError verifies that
// CheckForwardDrift returns a *schema.SchemaSkewError when the DB is one
// migration ahead of the binary.
func TestCheckForwardDrift_ForwardDrift_ReturnsSchemaSkewError(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	if _, err := store.db.ExecContext(ctx,
		"INSERT INTO schema_migrations (version) VALUES (?)",
		schema.LatestVersion()+1,
	); err != nil {
		t.Fatalf("insert future migration: %v", err)
	}

	err := schema.CheckForwardDrift(ctx, store.db)
	if err == nil {
		t.Fatal("CheckForwardDrift = nil, want error when DB is ahead of binary")
	}
	var skewErr *schema.SchemaSkewError
	if !errors.As(err, &skewErr) {
		t.Fatalf("error = %T (%v), want *schema.SchemaSkewError", err, err)
	}
	if skewErr.DBVersion != schema.LatestVersion()+1 {
		t.Errorf("DBVersion = %d, want %d", skewErr.DBVersion, schema.LatestVersion()+1)
	}
	if skewErr.BinaryVersion != schema.LatestVersion() {
		t.Errorf("BinaryVersion = %d, want %d", skewErr.BinaryVersion, schema.LatestVersion())
	}
}

// TestCheckForwardDrift_CurrentVersion_NoError verifies that CheckForwardDrift
// returns nil when the DB schema matches the binary.
func TestCheckForwardDrift_CurrentVersion_NoError(t *testing.T) {
	if os.Getenv("BD_IGNORE_SCHEMA_SKEW") != "" {
		t.Skip("BD_IGNORE_SCHEMA_SKEW is set — skipping to keep the non-escape-hatch path clean")
	}

	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	if err := schema.CheckForwardDrift(ctx, store.db); err != nil {
		t.Fatalf("CheckForwardDrift = %v, want nil when schema is current", err)
	}
}

// TestCheckForwardDrift_EscapeHatch_ReturnsNil verifies that
// BD_IGNORE_SCHEMA_SKEW=1 suppresses the forward-drift error.
//
// Uses a dedicated database rather than setupTestStore: this test calls
// t.Setenv, which Go forbids in a parallel test, and setupTestStore marks the
// test parallel. (setupTestStore + t.Setenv panics the dolt test binary whenever
// the test Dolt server is running.)
func TestCheckForwardDrift_EscapeHatch_ReturnsNil(t *testing.T) {
	skipIfNoDolt(t)
	t.Setenv("BD_IGNORE_SCHEMA_SKEW", "1")

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
	// Do not DROP DATABASE here: rapid CREATE/DROP cycles can crash the Dolt
	// testcontainer. The random test database is discarded with the container.
	defer store.Close()

	if _, err := store.db.ExecContext(ctx,
		"INSERT INTO schema_migrations (version) VALUES (?)",
		schema.LatestVersion()+1,
	); err != nil {
		t.Fatalf("insert future migration: %v", err)
	}

	if err := schema.CheckForwardDrift(ctx, store.db); err != nil {
		t.Fatalf("CheckForwardDrift = %v, want nil when BD_IGNORE_SCHEMA_SKEW=1", err)
	}
}

// TestDoltNew_ReadOnly_ForwardDrift_ReturnsSchemaSkewError is a full-chain
// integration test: opening a read-only store against a DB that is one
// migration ahead of the binary must fail with a *schema.SchemaSkewError.
func TestDoltNew_ReadOnly_ForwardDrift_ReturnsSchemaSkewError(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := testContext(t)
	defer cancel()

	tmpDir := t.TempDir()
	dbName := uniqueTestDBName(t)

	// Open a writable store to create the database and initialize the schema.
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
	// Keep store open for cleanup; DROP must happen before Close.
	defer func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 5*testTimeout)
		defer dropCancel()
		_, _ = store.db.ExecContext(dropCtx, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", dbName))
		store.Close()
	}()

	// Advance schema_migrations by one to simulate a DB from a newer binary.
	if _, err := store.db.ExecContext(ctx,
		"INSERT INTO schema_migrations (version) VALUES (?)",
		schema.LatestVersion()+1,
	); err != nil {
		t.Fatalf("insert future migration: %v", err)
	}

	// Opening in read-only mode must detect the drift and return a SchemaSkewError.
	roStore, roErr := New(ctx, &Config{
		Path:     tmpDir,
		Database: dbName,
		ReadOnly: true,
	})
	if roErr == nil {
		roStore.Close()
		t.Fatal("New (read-only) = nil, want error for forward schema drift")
	}
	if !schema.IsSchemaSkewError(roErr) {
		t.Fatalf("error = %T (%v), want error wrapping *schema.SchemaSkewError", roErr, roErr)
	}
}

// TestDoltNew_ReadOnly_ForwardDrift_EscapeHatch_Succeeds verifies that
// BD_IGNORE_SCHEMA_SKEW=1 allows a read-only store to open despite forward drift.
func TestDoltNew_ReadOnly_ForwardDrift_EscapeHatch_Succeeds(t *testing.T) {
	skipIfNoDolt(t)
	t.Setenv("BD_IGNORE_SCHEMA_SKEW", "1")

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

	if _, err := store.db.ExecContext(ctx,
		"INSERT INTO schema_migrations (version) VALUES (?)",
		schema.LatestVersion()+1,
	); err != nil {
		t.Fatalf("insert future migration: %v", err)
	}

	roStore, roErr := New(ctx, &Config{
		Path:     tmpDir,
		Database: dbName,
		ReadOnly: true,
	})
	if roErr != nil {
		t.Fatalf("New (read-only with escape hatch) = %v, want nil", roErr)
	}
	defer roStore.Close()
}

// TestDoltNew_CreateIfMissing_NoSchemaSkewError is the bd-init no-op guard:
// creating a brand-new database must not trip the schema skew guard.
// A fresh DB has schema_migrations version=0, which checkSchemaSkew treats as no-op.
func TestDoltNew_CreateIfMissing_NoSchemaSkewError(t *testing.T) {
	skipIfNoDolt(t)

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
		if schema.IsSchemaSkewError(err) {
			t.Fatalf("New (CreateIfMissing) tripped schema skew guard — guard must be no-op for fresh DB: %v", err)
		}
		t.Fatalf("New (CreateIfMissing) = %v, want nil", err)
	}
	defer func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 5*testTimeout)
		defer dropCancel()
		_, _ = store.db.ExecContext(dropCtx, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", dbName))
		store.Close()
	}()

	// A fully-migrated fresh DB should also pass CheckForwardDrift (version == binary).
	if err := schema.CheckForwardDrift(ctx, store.db); err != nil {
		t.Fatalf("CheckForwardDrift on fresh DB = %v, want nil", err)
	}
}
