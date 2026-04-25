//go:build cgo

package embeddeddolt_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
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

	// Verify D4v2 indexes exist on the issues table.
	var ignoredName, createStmt string
	if err := db.QueryRowContext(ctx, "SHOW CREATE TABLE `issues`").Scan(&ignoredName, &createStmt); err != nil {
		t.Fatalf("SHOW CREATE TABLE issues: %v", err)
	}
	for _, idx := range []string{"idx_issues_status_updated_at", "idx_issues_defer_until"} {
		if !strings.Contains(createStmt, idx) {
			t.Errorf("issues table missing index %q", idx)
		}
	}

	// --- Spot-check key columns via SHOW CREATE TABLE ---

	spotChecks := map[string][]string{
		"issues": {
			"defer_until", "due_at", "rig", "role_type", "agent_state",
			"hook_bead", "role_bead", "await_type", "event_kind",
			"idx_issues_status_updated_at", "idx_issues_defer_until",
			"idx_issues_external_ref",
		},
		"dependencies": {
			"thread_id", "metadata", "idx_dependencies_thread",
			"idx_dependencies_depends_on_type", "fk_dep_issue",
		},
		"wisps": {
			"defer_until", "due_at", "rig", "idx_wisps_status",
		},
		"wisp_dependencies": {
			"thread_id", "metadata", "idx_wisp_dep_depends",
			"idx_wisp_dep_type", "idx_wisp_dep_type_depends",
		},
	}

	for table, checks := range spotChecks {
		var createStmt string
		row := db.QueryRowContext(ctx, "SHOW CREATE TABLE `"+table+"`")
		var ignoredName string
		if err := row.Scan(&ignoredName, &createStmt); err != nil {
			t.Errorf("SHOW CREATE TABLE %s: %v", table, err)
			continue
		}
		for _, check := range checks {
			if !strings.Contains(createStmt, check) {
				t.Errorf("table %s: expected %q in CREATE statement, not found", table, check)
			}
		}
	}

	// --- Verify views ---

	for _, view := range []string{"ready_issues", "blocked_issues"} {
		if _, err := db.ExecContext(ctx, "SELECT 1 FROM `"+view+"` LIMIT 0"); err != nil {
			t.Errorf("view %s not queryable: %v", view, err)
		}
	}

	// --- Verify default config ---

	var configCount int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM config").Scan(&configCount); err != nil {
		t.Fatalf("counting config rows: %v", err)
	}
	if configCount != 9 {
		t.Errorf("config rows: got %d, want 9", configCount)
	}

	// --- Verify schema_migrations max version ---

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
