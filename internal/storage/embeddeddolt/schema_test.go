//go:build cgo

package embeddeddolt_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

func TestSchemaAfterInit(t *testing.T) {
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
	t.Cleanup(func() { store.Close() })

	db, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, "testdb", "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	t.Cleanup(func() { _ = cleanup() })

	var maxVersion int
	if err := db.QueryRowContext(ctx, "SELECT MAX(version) FROM schema_migrations").Scan(&maxVersion); err != nil {
		t.Fatalf("reading max schema_migrations version: %v", err)
	}
	if want := embeddeddolt.LatestVersion(); maxVersion != want {
		t.Errorf("schema_migrations max version: got %d, want %d", maxVersion, want)
	}

	var maxIgnoredVersion int
	if err := db.QueryRowContext(ctx, "SELECT MAX(version) FROM ignored_schema_migrations").Scan(&maxIgnoredVersion); err != nil {
		t.Fatalf("reading max ignored_schema_migrations version: %v", err)
	}
	if want := embeddeddolt.LatestIgnoredVersion(); maxIgnoredVersion != want {
		t.Errorf("ignored_schema_migrations max version: got %d, want %d", maxIgnoredVersion, want)
	}

	// bd-2rd37: migration 0051 (and ignored/0010 for the wisp twins) drops the
	// dormant DEFAULT (UUID()) on the aux-table primary keys, so an insert path
	// that omits id fails loudly instead of silently minting a per-clone-random
	// key (the #4259 failure class). dependencies.id is the original #4259
	// table: its DEFAULT (UUID()) is dropped by 0050's prepared ALTER, which
	// this assertion verifies actually took effect (bd-578h9.17). Scanning
	// COLUMN_DEFAULT (rather than counting) also fails if the table or column
	// is missing entirely.
	for _, table := range []string{
		"dependencies",
		"events", "comments", "issue_snapshots", "compaction_snapshots",
		"wisp_events", "wisp_comments", "wisp_dependencies",
	} {
		var columnDefault sql.NullString
		err := db.QueryRowContext(ctx, `
			SELECT COLUMN_DEFAULT FROM INFORMATION_SCHEMA.COLUMNS
			WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = 'id'
		`, table).Scan(&columnDefault)
		if err != nil {
			t.Fatalf("reading %s.id default: %v", table, err)
		}
		if columnDefault.Valid {
			t.Errorf("%s.id has DEFAULT %q, want none (migrations 0050/0051 / ignored 0010)", table, columnDefault.String)
		}
	}
}
