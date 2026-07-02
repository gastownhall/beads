package dolt

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/depid"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestSchemaMigrationsPopulatedAfterInit verifies that initSchemaOnDB populates
// the schema_migrations table after successful initialization.
func TestSchemaMigrationsPopulatedAfterInit(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	var maxVersion int
	err := store.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&maxVersion)
	if err != nil {
		t.Fatalf("schema_migrations query failed: %v", err)
	}
	if maxVersion != schema.LatestVersion() {
		t.Errorf("max migration version = %d, want %d", maxVersion, schema.LatestVersion())
	}
}

// TestSchemaSkipsReinit verifies that initSchemaOnDB returns early
// when all migrations are already applied, skipping all DDL.
func TestSchemaSkipsReinit(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Drop a table to detect whether init actually runs DDL
	_, err := store.db.ExecContext(ctx, "DROP TABLE IF EXISTS export_hashes")
	if err != nil {
		t.Fatalf("failed to drop export_hashes: %v", err)
	}

	// Run initSchemaOnDB again — should skip because migrations are current
	if _, err := initSchemaOnDB(ctx, store.db); err != nil {
		t.Fatalf("initSchemaOnDB failed: %v", err)
	}

	// export_hashes should still be missing (init was skipped)
	var count int
	err = store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_name = 'export_hashes' AND table_schema = DATABASE()").Scan(&count)
	if err != nil {
		t.Fatalf("failed to check for export_hashes: %v", err)
	}
	if count != 0 {
		t.Error("export_hashes was recreated — initSchemaOnDB should have skipped when migrations are current")
	}
}

// TestSchemaRunsInitWhenStale verifies that initSchemaOnDB runs
// migrations when the schema_migrations table is behind.
func TestSchemaRunsInitWhenStale(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Remove the latest migration record to simulate a stale schema
	_, err := store.db.ExecContext(ctx,
		"DELETE FROM schema_migrations WHERE version = ?", schema.LatestVersion())
	if err != nil {
		t.Fatalf("failed to delete latest migration: %v", err)
	}

	// Run initSchemaOnDB — should detect stale and re-apply
	if _, err := initSchemaOnDB(ctx, store.db); err != nil {
		t.Fatalf("initSchemaOnDB failed: %v", err)
	}

	// Latest version should be back
	var maxVersion int
	err = store.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&maxVersion)
	if err != nil {
		t.Fatalf("reading max version: %v", err)
	}
	if maxVersion != schema.LatestVersion() {
		t.Errorf("max migration version = %d after re-init, want %d", maxVersion, schema.LatestVersion())
	}
}

func TestMigration0053PromotesRigWisps(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	const rigID = "schema-rig-wisp"
	const targetID = "schema-rig-target"
	const sourceID = "schema-rig-source"

	for _, id := range []string{targetID, sourceID} {
		if _, err := store.db.ExecContext(ctx, `
			INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type)
			VALUES (?, ?, '', '', '', '', 'open', 2, 'task')
		`, id, id); err != nil {
			t.Fatalf("seed issue %s: %v", id, err)
		}
	}
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO wisps (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, ephemeral)
		VALUES (?, 'Rig identity', '', '', '', '', 'open', 1, 'rig', 1)
	`, rigID); err != nil {
		t.Fatalf("seed rig wisp: %v", err)
	}
	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO wisp_labels (issue_id, label) VALUES (?, 'gt:rig')`, rigID); err != nil {
		t.Fatalf("seed wisp label: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO wisp_dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by, metadata)
		VALUES (?, ?, ?, 'blocks', NOW(), 'tester', JSON_OBJECT())
	`, depid.New(rigID, targetID), rigID, targetID); err != nil {
		t.Fatalf("seed outgoing wisp dependency: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO dependencies (id, issue_id, depends_on_wisp_id, type, created_at, created_by, metadata)
		VALUES (?, ?, ?, 'blocks', NOW(), 'tester', JSON_OBJECT())
	`, depid.New(sourceID, rigID), sourceID, rigID); err != nil {
		t.Fatalf("seed inbound dependency: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO wisp_events (id, issue_id, event_type, actor, created_at)
		VALUES ('schema-rig-event', ?, 'created', 'tester', NOW())
	`, rigID); err != nil {
		t.Fatalf("seed wisp event: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO wisp_comments (id, issue_id, author, text, created_at)
		VALUES ('schema-rig-comment', ?, 'tester', 'durable identity', NOW())
	`, rigID); err != nil {
		t.Fatalf("seed wisp comment: %v", err)
	}
	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO wisp_child_counters (parent_id, last_child) VALUES (?, 7)`, rigID); err != nil {
		t.Fatalf("seed wisp child counter: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, "CALL DOLT_ADD('issues')"); err != nil {
		t.Fatalf("stage seeded issues: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, "CALL DOLT_ADD('dependencies')"); err != nil {
		t.Fatalf("stage seeded dependencies: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, "CALL DOLT_COMMIT('-m', 'seed rig repair fixture')"); err != nil &&
		!strings.Contains(strings.ToLower(err.Error()), "nothing to commit") {
		t.Fatalf("commit seed fixture: %v", err)
	}

	if _, err := store.db.ExecContext(ctx, "DELETE FROM schema_migrations WHERE version = ?", schema.LatestVersion()); err != nil {
		t.Fatalf("mark 0053 pending: %v", err)
	}
	if _, err := schema.MigrateUp(ctx, store.db); err != nil {
		t.Fatalf("MigrateUp: %v", err)
	}

	var issueRows, wispRows int
	if err := store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM issues WHERE id = ? AND issue_type = 'rig' AND ephemeral = 0`, rigID).Scan(&issueRows); err != nil {
		t.Fatalf("count promoted rig issue: %v", err)
	}
	if issueRows != 1 {
		t.Fatalf("promoted rig issue rows = %d, want 1", issueRows)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM wisps WHERE id = ?`, rigID).Scan(&wispRows); err != nil {
		t.Fatalf("count remaining rig wisp: %v", err)
	}
	if wispRows != 0 {
		t.Fatalf("remaining rig wisp rows = %d, want 0", wispRows)
	}

	assertCount := func(name, query string, want int, args ...any) {
		t.Helper()
		var got int
		if err := store.db.QueryRowContext(ctx, query, args...).Scan(&got); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got != want {
			t.Fatalf("%s = %d, want %d", name, got, want)
		}
	}
	assertCount("promoted labels",
		`SELECT COUNT(*) FROM labels WHERE issue_id = ? AND label = 'gt:rig'`, 1, rigID)
	assertCount("promoted outgoing dependencies",
		`SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND depends_on_issue_id = ?`, 1, rigID, targetID)
	assertCount("retargeted inbound dependencies",
		`SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND depends_on_issue_id = ? AND depends_on_wisp_id IS NULL`, 1, sourceID, rigID)
	assertCount("promoted events",
		`SELECT COUNT(*) FROM events WHERE issue_id = ?`, 1, rigID)
	assertCount("promoted comments",
		`SELECT COUNT(*) FROM comments WHERE issue_id = ?`, 1, rigID)
	assertCount("promoted child counter",
		`SELECT COUNT(*) FROM child_counters WHERE parent_id = ? AND last_child = 7`, 1, rigID)
	assertCount("remaining wisp child counter",
		`SELECT COUNT(*) FROM wisp_child_counters WHERE parent_id = ?`, 0, rigID)
	assertCount("remaining source wisp dependencies",
		`SELECT COUNT(*) FROM wisp_dependencies WHERE issue_id = ?`, 0, rigID)
}

// TestSchemaRunsInitWhenMissing verifies that initSchemaOnDB runs
// full initialization when schema_migrations doesn't exist (fresh db).
func TestSchemaRunsInitWhenMissing(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := testContext(t)
	defer cancel()

	tmpDir, err := os.MkdirTemp("", "dolt-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbName := uniqueTestDBName(t)
	cfg := &Config{
		Path:            tmpDir,
		CommitterName:   "test",
		CommitterEmail:  "test@example.com",
		Database:        dbName,
		CreateIfMissing: true,
	}

	store, err := New(ctx, cfg)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	var maxVersion int
	err = store.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&maxVersion)
	if err != nil {
		t.Fatalf("schema_migrations query failed: %v", err)
	}
	if maxVersion != schema.LatestVersion() {
		t.Errorf("max migration version = %d, want %d", maxVersion, schema.LatestVersion())
	}

	var legacyTargetColumns int
	err = store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema = DATABASE()
		  AND table_name = 'wisp_dependencies'
		  AND column_name = 'depends_on_id'`).Scan(&legacyTargetColumns)
	if err != nil {
		t.Fatalf("query wisp_dependencies legacy target column: %v", err)
	}
	if legacyTargetColumns != 0 {
		t.Fatalf("wisp_dependencies.depends_on_id exists after fresh migration")
	}

	var splitTargetColumns int
	err = store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema = DATABASE()
		  AND table_name = 'wisp_dependencies'
		  AND column_name IN ('depends_on_issue_id', 'depends_on_wisp_id', 'depends_on_external')`).Scan(&splitTargetColumns)
	if err != nil {
		t.Fatalf("query wisp_dependencies split target columns: %v", err)
	}
	if splitTargetColumns != 3 {
		t.Fatalf("wisp_dependencies split target column count = %d, want 3", splitTargetColumns)
	}

	dropCtx, dropCancel := context.WithTimeout(context.Background(), 5*testTimeout)
	defer dropCancel()
	_, _ = store.db.ExecContext(dropCtx, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", dbName))
	store.Close()
}
