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

	queries := committedSchemaSnapshotQueries()
	var snapshot []string
	for _, name := range sortedSnapshotQueryNames(queries) {
		for _, row := range queryCSV(t, dir, queries[name]) {
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

	queries := committedSchemaSnapshotQueries()
	var snapshot []string
	for _, name := range sortedSnapshotQueryNames(queries) {
		query := queries[name]
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

// sortedSnapshotQueryNames returns the query names in sorted order so both
// snapshot helpers issue queries in a fixed sequence. The runtime side reads
// through a session whose root only advances once a query has succeeded
// (be-itm5); ranging over the map directly let a different category run
// first on every invocation and made whichever one drew that slot read as
// spuriously empty.
func sortedSnapshotQueryNames(queries map[string]string) []string {
	names := make([]string, 0, len(queries))
	for name := range queries {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func committedSchemaSnapshotQueries() map[string]string {
	// The ignored-migration stream owns objects at two levels: whole tables
	// (wisps, excluded below by name/prefix) and individual columns bolted
	// onto an otherwise main-stream table (leases.granted_node, added by
	// migrations/ignored/0016_add_lease_granted_node.up.sql). Both levels are
	// intentionally excluded from the committed-schema parity oracle, which
	// compares the main migration stream only. CLI substitutions that touch
	// wisps still have focused coverage in internal/storage/schema tests.
	//
	// That exclusion has a real cost, paid once: migration 0065's
	// wisp_comments.text widening drifted for six days and this oracle could
	// not see it (ga-61ruw). Widening the filter is nonetheless not a filter
	// tweak, because schema.AllMigrationsSQL() walks the MAIN series only and
	// there is no ignored-series bundle to pair with it. Measured 2026-08-21
	// by applying bundle-only and bundle-plus-ignored-series through dolt
	// 2.3.1, the ignored plane owns at least: wisps.is_blocked (and
	// idx_wisps_is_blocked, idx_wisps_defer_until), the dropped uuid()
	// defaults on wisp_comments.id / wisp_dependencies.id / wisp_events.id,
	// wisp_events.old_value+new_value as LONGTEXT, leases.granted_node, and
	// ignored_schema_migrations itself. Every one of those would read as a
	// spurious "only in runtime" line. Covering the ephemeral plane properly
	// means giving the CLI side an ignored-series bundle, not deleting these
	// predicates. Until then the masking-proof source-level guard in
	// internal/storage/schema/cli_prepared_ddl.go is what covers the class
	// that got past this oracle.
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
  AND LEFT(t.table_name, 5) <> 'dolt_'
  AND NOT (c.table_name = 'leases' AND c.column_name = 'granted_node')`,
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
