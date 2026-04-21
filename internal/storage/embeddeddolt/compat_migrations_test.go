//go:build cgo

package embeddeddolt_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// TestEmbeddedOpenRunsCompatMigrations verifies that opening an existing
// embedded Dolt database with a newer bd binary runs the idempotent compat
// migrations that repair column-shape drift. Regression test for GH#3412.
//
// Setup:
//  1. Initialize a fresh embedded store (creates all tables + runs SQL
//     migrations, including the one that adds issues.started_at).
//  2. Simulate a pre-existing DB whose SQL migration for started_at was
//     recorded as applied but never actually produced the column — drop the
//     column while leaving schema_migrations intact.
//  3. Close and reopen the store.
//
// If compat migrations are NOT wired into the embedded open path, the column
// stays missing and any subsequent query referencing started_at fails with
// "column started_at could not be found". With the wiring in place, compat
// migration 017 (MigrateAddStartedAtColumn) adds it back.
func TestEmbeddedOpenRunsCompatMigrations(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}

	ctx := t.Context()
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	dataDir := filepath.Join(beadsDir, "embeddeddolt")

	// Step 1: fresh init — schema.MigrateUp adds all columns including started_at.
	store, err := embeddeddolt.New(ctx, beadsDir, "testdb", "main")
	if err != nil {
		t.Fatalf("initial New: %v", err)
	}

	// Step 2: drop issues.started_at to simulate a DB that predates (or was
	// stranded missing) the started_at SQL migration, WITHOUT rolling back
	// schema_migrations. This is the exact shape of the bug: schema_migrations
	// says the migration ran, but the column is absent, and the compat runner
	// is the only path that repairs it.
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, "testdb", "main")
	if err != nil {
		store.Close()
		t.Fatalf("OpenSQL for setup: %v", err)
	}
	if _, err := db.ExecContext(ctx, "ALTER TABLE `issues` DROP COLUMN started_at"); err != nil {
		cleanup()
		store.Close()
		t.Fatalf("dropping started_at for test setup: %v", err)
	}
	cleanup()
	store.Close()

	// Step 3: reopen. If compat migrations are wired in, this repairs the
	// column via MigrateAddStartedAtColumn.
	store2, err := embeddeddolt.New(ctx, beadsDir, "testdb", "main")
	if err != nil {
		t.Fatalf("reopen New: %v", err)
	}
	defer store2.Close()

	db2, cleanup2, err := embeddeddolt.OpenSQL(ctx, dataDir, "testdb", "main")
	if err != nil {
		t.Fatalf("OpenSQL for verification: %v", err)
	}
	defer cleanup2()

	// The column must now exist. SHOW COLUMNS LIKE returns a row on hit,
	// no rows on miss — mirrors the shape of migrations.columnExists.
	rows, err := db2.QueryContext(ctx, "SHOW COLUMNS FROM `issues` LIKE 'started_at'")
	if err != nil {
		t.Fatalf("SHOW COLUMNS: %v", err)
	}
	found := rows.Next()
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatalf("iterating SHOW COLUMNS: %v", err)
	}
	rows.Close()
	if !found {
		t.Fatal("issues.started_at missing after reopen — compat migration runner did not execute on embedded open path")
	}

	// Sanity check: a SELECT that references the column must succeed, proving
	// the exact original failure mode ("column … could not be found in any
	// table in scope") is repaired.
	if _, err := db2.ExecContext(ctx, "SELECT started_at FROM `issues` LIMIT 0"); err != nil {
		t.Fatalf("SELECT started_at after compat migration: %v", err)
	}
}
