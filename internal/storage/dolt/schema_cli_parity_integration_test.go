//go:build integration

package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
)

func TestCLIBundleMatchesRuntimeCommittedSchema(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := testContext(t)
	defer cancel()

	baseDir := t.TempDir()
	cliDir := filepath.Join(baseDir, "cli")
	if err := os.MkdirAll(cliDir, 0o755); err != nil {
		t.Fatalf("create CLI schema dir: %v", err)
	}
	runCmd(t, cliDir, "dolt", "init")
	runDoltSQL(t, cliDir, schema.AllMigrationsSQL())

	dbName := uniqueTestDBName(t)
	runtimeDir := filepath.Join(baseDir, "runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		t.Fatalf("create runtime schema dir: %v", err)
	}
	store, err := New(ctx, &Config{
		Path:            runtimeDir,
		CommitterName:   "test",
		CommitterEmail:  "test@example.com",
		ServerHost:      "127.0.0.1",
		ServerPort:      testServerPort,
		Database:        dbName,
		CreateIfMissing: true,
	})
	if err != nil {
		t.Fatalf("create runtime store: %v", err)
	}
	defer store.Close()
	defer func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), testTimeout)
		defer dropCancel()
		_, _ = store.db.ExecContext(dropCtx, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", dbName))
	}()

	cliSnapshot := cliCommittedSchemaSnapshot(t, cliDir)
	runtimeSnapshot := runtimeCommittedSchemaSnapshot(t, store.db)
	if diff := firstSchemaSnapshotDiff(cliSnapshot, runtimeSnapshot); diff != "" {
		t.Fatalf("CLI bundle schema does not match runtime committed schema:\n%s", diff)
	}
}

func cliCommittedSchemaSnapshot(t *testing.T, dir string) []string {
	t.Helper()

	var snapshot []string
	for name, query := range committedSchemaSnapshotQueries() {
		for _, row := range queryCSV(t, dir, query) {
			snapshot = append(snapshot, name+"|"+row["line"])
		}
	}
	sort.Strings(snapshot)
	return snapshot
}

func runtimeCommittedSchemaSnapshot(t *testing.T, db *sql.DB) []string {
	t.Helper()

	ctx, cancel := testContext(t)
	defer cancel()

	var snapshot []string
	for name, query := range committedSchemaSnapshotQueries() {
		rows, err := db.QueryContext(ctx, query)
		if err != nil {
			t.Fatalf("query runtime %s snapshot: %v", name, err)
		}
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				rows.Close()
				t.Fatalf("scan runtime %s snapshot: %v", name, err)
			}
			snapshot = append(snapshot, name+"|"+line)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatalf("iterate runtime %s snapshot: %v", name, err)
		}
		rows.Close()
	}
	sort.Strings(snapshot)
	return snapshot
}

func committedSchemaSnapshotQueries() map[string]string {
	// Wisp tables are owned by the ignored-migration stream and are intentionally
	// excluded from the committed-schema parity oracle. CLI substitutions that
	// touch wisps still have focused coverage in internal/storage/schema tests.
	return map[string]string{
		"tables": `
SELECT CONCAT('table|', t.table_name, '|', t.table_type) AS line
FROM information_schema.tables t
WHERE t.table_schema = DATABASE()
  AND t.table_name NOT IN ('ignored_schema_migrations', 'local_metadata', 'repo_mtimes', 'wisps')
  AND LEFT(t.table_name, 5) <> 'wisp_'
  AND LEFT(t.table_name, 5) <> 'dolt_'`,
		"columns": `
SELECT CONCAT('column|', c.table_name, '|', LPAD(c.ordinal_position, 3, '0'), '|',
  c.column_name, '|', c.column_type, '|', c.is_nullable, '|',
  COALESCE(c.column_default, '<NULL>'), '|', c.extra, '|',
  COALESCE(c.generation_expression, '')) AS line
FROM information_schema.columns c
JOIN information_schema.tables t
  ON t.table_schema = c.table_schema AND t.table_name = c.table_name
WHERE c.table_schema = DATABASE()
  AND t.table_name NOT IN ('ignored_schema_migrations', 'local_metadata', 'repo_mtimes', 'wisps')
  AND LEFT(t.table_name, 5) <> 'wisp_'
  AND LEFT(t.table_name, 5) <> 'dolt_'`,
		"indexes": `
SELECT CONCAT('index|', s.table_name, '|', s.index_name, '|', LPAD(s.seq_in_index, 3, '0'), '|',
  s.column_name, '|', s.non_unique, '|', COALESCE(s.sub_part, ''), '|',
  COALESCE(s.nullable, ''), '|', s.index_type) AS line
FROM information_schema.statistics s
JOIN information_schema.tables t
  ON t.table_schema = s.table_schema AND t.table_name = s.table_name
WHERE s.table_schema = DATABASE()
  AND t.table_name NOT IN ('ignored_schema_migrations', 'local_metadata', 'repo_mtimes', 'wisps')
  AND LEFT(t.table_name, 5) <> 'wisp_'
  AND LEFT(t.table_name, 5) <> 'dolt_'`,
		"constraints": `
SELECT CONCAT('constraint|', tc.table_name, '|', tc.constraint_name, '|', tc.constraint_type, '|',
  LPAD(COALESCE(kcu.ordinal_position, 0), 3, '0'), '|',
  COALESCE(kcu.column_name, ''), '|', COALESCE(kcu.referenced_table_name, ''), '|',
  COALESCE(kcu.referenced_column_name, ''), '|', COALESCE(rc.update_rule, ''), '|',
  COALESCE(rc.delete_rule, '')) AS line
FROM information_schema.table_constraints tc
JOIN information_schema.tables t
  ON t.table_schema = tc.table_schema AND t.table_name = tc.table_name
LEFT JOIN information_schema.key_column_usage kcu
  ON kcu.constraint_schema = tc.constraint_schema
 AND kcu.table_name = tc.table_name
 AND kcu.constraint_name = tc.constraint_name
LEFT JOIN information_schema.referential_constraints rc
  ON rc.constraint_schema = tc.constraint_schema
 AND rc.constraint_name = tc.constraint_name
WHERE tc.constraint_schema = DATABASE()
  AND t.table_name NOT IN ('ignored_schema_migrations', 'local_metadata', 'repo_mtimes', 'wisps')
  AND LEFT(t.table_name, 5) <> 'wisp_'
  AND LEFT(t.table_name, 5) <> 'dolt_'`,
		"version": `
SELECT CONCAT('version|', COALESCE(MAX(version), 0)) AS line
FROM schema_migrations`,
	}
}
