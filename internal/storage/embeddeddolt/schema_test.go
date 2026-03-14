//go:build embeddeddolt

package embeddeddolt_test

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/gastownhall/beads/internal/storage/embeddeddolt"
)

func TestSchemaAfterInit(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}

	ctx := t.Context()
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	dataDir := filepath.Join(beadsDir, "embeddeddolt")

	// Initialize store — creates database and runs all migrations.
	store, err := embeddeddolt.New(ctx, beadsDir, "testdb", "main")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Open a verification connection.
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, "testdb", "main")
	if err != nil {
		store.Close()
		t.Fatalf("OpenSQL: %v", err)
	}

	// --- Verify tables ---

	expectedTables := []string{
		"issues",
		"dependencies",
		"labels",
		"comments",
		"events",
		"config",
		"metadata",
		"child_counters",
		"issue_snapshots",
		"compaction_snapshots",
		"repo_mtimes",
		"routes",
		"issue_counter",
		"interactions",
		"federation_peers",
		"wisps",
		"wisp_labels",
		"wisp_dependencies",
		"wisp_events",
		"wisp_comments",
		"schema_migrations",
	}

	rows, err := db.QueryContext(ctx, "SHOW TABLES")
	if err != nil {
		t.Fatalf("SHOW TABLES: %v", err)
	}

	gotTables := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scanning table name: %v", err)
		}
		gotTables[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterating tables: %v", err)
	}
	rows.Close()

	for _, want := range expectedTables {
		if !gotTables[want] {
			t.Errorf("missing table: %s", want)
		}
	}

	// --- Spot-check key columns via SHOW CREATE TABLE ---

	spotChecks := map[string][]string{
		"issues": {
			"defer_until", "due_at", "rig", "role_type", "agent_state",
			"hook_bead", "role_bead", "await_type", "event_kind",
			"idx_issues_status", "idx_issues_external_ref",
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
		t.Fatalf("reading max migration version: %v", err)
	}
	if maxVersion != 22 {
		t.Errorf("max migration version: got %d, want 22", maxVersion)
	}

	// --- Log all tables for debugging ---

	var tableList []string
	for name := range gotTables {
		tableList = append(tableList, name)
	}
	sort.Strings(tableList)
	t.Logf("tables found: %s", strings.Join(tableList, ", "))

	// --- Close first store and verification connection ---

	cleanup()
	store.Close()

	// --- Verify idempotency: New on same dir succeeds ---

	store2, err := embeddeddolt.New(ctx, beadsDir, "testdb", "main")
	if err != nil {
		t.Fatalf("second New (idempotency): %v", err)
	}

	db2, cleanup2, err := embeddeddolt.OpenSQL(ctx, dataDir, "testdb", "main")
	if err != nil {
		store2.Close()
		t.Fatalf("second OpenSQL: %v", err)
	}

	var migrationCount int
	if err := db2.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&migrationCount); err != nil {
		t.Fatalf("counting migrations: %v", err)
	}
	if migrationCount != 22 {
		t.Errorf("migration count after second init: got %d, want 22", migrationCount)
	}

	if err := db2.QueryRowContext(ctx, "SELECT MAX(version) FROM schema_migrations").Scan(&maxVersion); err != nil {
		t.Fatalf("reading max version after second init: %v", err)
	}
	if maxVersion != 22 {
		t.Errorf("max version after second init: got %d, want 22", maxVersion)
	}

	cleanup2()
	store2.Close()
}
