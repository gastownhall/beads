//go:build cgo

package embeddeddolt_test

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestEmbeddedTempRebuildIntermediatesNeverReachHistory pins a shared-hub
// contract: the ignored-series table rebuilds stage their work
// through __temp__<table> intermediates (ignored/0001, ignored/0002). The
// final names are dolt_ignored clone-local tables, but the intermediates
// carried no ignore pattern, so against a @@dolt_transaction_commit=1 server
// every CREATE __temp__X auto-committed a REAL table at HEAD; the later
// RENAME to an ignored name then left rename halves in dolt_status that no
// working-set shuffle could stage (content-inferred rename classification),
// wedging every older bd behind the dirty-table guard until a manual repair.
//
// The store under test is shaped like the field case: a clone whose
// dolt_ignore was seeded by binaries that predate the __temp__% pattern. The
// fresh-box MigrateUp must assert the pattern itself, in the same pass,
// before the ignored series runs — so no __temp__ intermediate ever lands in
// committed history and the pass ends with a clean working set.
func TestEmbeddedTempRebuildIntermediatesNeverReachHistory(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	dir := seedMainSchemaAt(t, ctx, schema.LatestVersion())
	conn, closeConn := openPinnedConn(t, ctx, dir)
	defer closeConn()
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("seed MigrateUp: %v", err)
	}

	// Shape the store like a real clone (the Door B dance from
	// TestEmbeddedIgnoredSeriesConvergesWithFreshInitShape): committed history
	// carries the main tables and the at-latest main cursor; the clone-local
	// tables and the ignored cursor are working-set state a clone never
	// receives.
	for _, table := range []string{
		"wisp_dependencies", "wisp_events", "wisp_comments", "wisp_labels",
		"wisp_child_counters", "wisps", "events", "leases", "repo_mtimes",
		"local_metadata", "bd_events_journal", "bd_events_seq",
		"ignored_schema_migrations",
	} {
		execFrozenGuard(t, ctx, conn, "DROP TABLE IF EXISTS "+table)
	}
	execFrozenGuard(t, ctx, conn,
		"DELETE FROM dolt_ignore WHERE pattern IN ('leases', 'bd_events_journal', 'bd_events_seq')")
	// The field stores were seeded by binaries that predate the __temp__%
	// pattern; strip it from the baseline so the pass under test has to
	// assert it itself, before the ignored series runs.
	execFrozenGuard(t, ctx, conn, "DELETE FROM dolt_ignore WHERE pattern = '__temp__%'")
	mustDrain(t, ctx, conn, "CALL DOLT_ADD('-A')")
	mustDrain(t, ctx, conn, "CALL DOLT_COMMIT('-m', 'test: clone-shaped baseline', '--skip-empty')")

	// The hub that hit this twice runs @@dolt_transaction_commit=1: every
	// transaction commit becomes a dolt commit of every non-ignored delta,
	// which is the mechanism that materialized the intermediates.
	if _, err := conn.ExecContext(ctx, "SET @@dolt_transaction_commit = 1"); err != nil {
		t.Fatalf("enable dolt_transaction_commit: %v", err)
	}

	// The fresh-box open.
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("fresh-box MigrateUp: %v", err)
	}

	// No rebuild intermediate may exist in ANY commit.
	rows, err := conn.QueryContext(ctx, "SELECT DISTINCT table_name FROM dolt_diff")
	if err != nil {
		t.Fatalf("read dolt_diff table names: %v", err)
	}
	defer rows.Close()
	var leaked []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan dolt_diff table name: %v", err)
		}
		if strings.HasPrefix(name, "__temp__") {
			leaked = append(leaked, name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate dolt_diff table names: %v", err)
	}
	if len(leaked) > 0 {
		t.Errorf("rebuild intermediates reached committed history: %v", leaked)
	}

	// And the pass must end clean — the incident's other half was the
	// permanently-dirty status that wedged older binaries' opens.
	dirty, err := statusTables(ctx, conn)
	if err != nil {
		t.Fatalf("read dolt_status: %v", err)
	}
	if len(dirty) > 0 {
		t.Errorf("working set dirty after the fresh-box pass: %v", dirty)
	}
}
