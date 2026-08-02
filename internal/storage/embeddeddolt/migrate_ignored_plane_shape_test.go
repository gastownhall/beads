//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestEmbeddedIgnoredSeriesConvergesWithFreshInitShape pins the bd-hs7fa
// contract: the fresh-clone door and the fresh-init door must produce the
// same schema for every clone-local (dolt_ignored) table.
//
// The two doors build those tables from different sources. A fresh init runs
// the main migration series, whose CREATEs and later ALTERs (0049's LONGTEXT
// widening, 0060's storage_class) all execute. A fresh clone receives only
// committed history — no clone-local tables, no ignored_schema_migrations
// cursor — with the MAIN cursor already at-latest, so none of the main
// series re-runs; the ignored series alone materializes the clone-local
// tables. Any main-plane ALTER against one of these tables that never got an
// ignored-series twin therefore silently never reaches fresh clones
// (observed in prod as wy-98eh5's re-clone missing wisps.storage_class, and
// before that as wy-pt82l missing wisps.row_lock — healed by ignored/0013).
//
// This test simulates both doors against the same binary and diffs every
// clone-local table's column shape (name, type, nullability, default). It is
// the engine-level backstop behind scripts/check-migration-hygiene.sh check
// D, which enforces the twin rule at source-review time.
func TestEmbeddedIgnoredSeriesConvergesWithFreshInitShape(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	// Door A: fresh init. seedMainSchemaAt runs the main series (whose
	// pre-0047 repair creates wisps mid-pass, so the later wisps ALTERs
	// fire); MigrateUp then runs the ignored series over the same tables.
	initDir := seedMainSchemaAt(t, ctx, schema.LatestVersion())
	initConn, closeInit := openPinnedConn(t, ctx, initDir)
	defer closeInit()
	if _, err := schema.MigrateUp(ctx, initConn); err != nil {
		t.Fatalf("fresh-init MigrateUp: %v", err)
	}

	// Door B: fresh clone. Committed history carries the main tables and the
	// at-latest main cursor; the clone-local tables and the ignored cursor
	// are working-set state a clone never receives. Drop them (children
	// before FK parents) and let MigrateUp materialize everything through
	// the ignored series alone.
	cloneDir := seedMainSchemaAt(t, ctx, schema.LatestVersion())
	cloneConn, closeClone := openPinnedConn(t, ctx, cloneDir)
	defer closeClone()
	if _, err := schema.MigrateUp(ctx, cloneConn); err != nil {
		t.Fatalf("clone-door seed MigrateUp: %v", err)
	}
	for _, table := range []string{
		"wisp_dependencies", "wisp_events", "wisp_comments", "wisp_labels",
		"wisp_child_counters", "wisps", "events", "leases", "repo_mtimes",
		"local_metadata", "ignored_schema_migrations",
	} {
		execFrozenGuard(t, ctx, cloneConn, "DROP TABLE IF EXISTS "+table)
	}
	// Harness artifact, not part of the simulation: seedMainSchemaAt's
	// DOLT_ADD('-A') baseline commit runs before MigrateUp seeds the static
	// dolt_ignore pattern for leases, so on THIS path leases lands in
	// committed history and its drop above shows up as a tracked deletion,
	// which the ignored-source dirty-table guard would refuse. A real clone
	// never has leases at HEAD. The pattern row must go before the deletion
	// can be staged (DOLT_ADD skips ignored tables even for deletions);
	// MigrateUp's seedDoltIgnorePatterns re-asserts it immediately after.
	execFrozenGuard(t, ctx, cloneConn, "DELETE FROM dolt_ignore WHERE pattern = 'leases'")
	mustDrain(t, ctx, cloneConn, "CALL DOLT_ADD('-A')")
	mustDrain(t, ctx, cloneConn, "CALL DOLT_COMMIT('-m', 'test: remove clone-local state from history', '--skip-empty')")
	if _, err := schema.MigrateUp(ctx, cloneConn); err != nil {
		t.Fatalf("clone-door MigrateUp: %v", err)
	}

	for _, table := range []string{
		"wisps", "wisp_labels", "wisp_dependencies", "wisp_events",
		"wisp_comments", "wisp_child_counters", "events", "leases",
		"repo_mtimes", "local_metadata",
	} {
		initShape := clonePlaneTableShape(t, ctx, initConn, table)
		cloneShape := clonePlaneTableShape(t, ctx, cloneConn, table)
		diffClonePlaneShapes(t, table, initShape, cloneShape)
	}
}

// clonePlaneColumn is the column identity compared across the two doors.
// Ordinal position is deliberately excluded: an ALTER ADD on the init door
// appends where the ignored series' CREATE may inline, and column order is
// not load-bearing for any bd query.
type clonePlaneColumn struct {
	ColumnType string
	Nullable   string
	Default    sql.NullString
}

func clonePlaneTableShape(t *testing.T, ctx context.Context, conn *sql.Conn, table string) map[string]clonePlaneColumn {
	t.Helper()
	rows, err := conn.QueryContext(ctx, `
		SELECT COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, COLUMN_DEFAULT
		FROM INFORMATION_SCHEMA.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?`, table)
	if err != nil {
		t.Fatalf("read %s columns: %v", table, err)
	}
	defer rows.Close()
	shape := map[string]clonePlaneColumn{}
	for rows.Next() {
		var name string
		var col clonePlaneColumn
		if err := rows.Scan(&name, &col.ColumnType, &col.Nullable, &col.Default); err != nil {
			t.Fatalf("scan %s column: %v", table, err)
		}
		shape[name] = col
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate %s columns: %v", table, err)
	}
	if len(shape) == 0 {
		t.Fatalf("table %s has no columns (missing?)", table)
	}
	return shape
}

func diffClonePlaneShapes(t *testing.T, table string, initShape, cloneShape map[string]clonePlaneColumn) {
	t.Helper()
	names := map[string]bool{}
	for name := range initShape {
		names[name] = true
	}
	for name := range cloneShape {
		names[name] = true
	}
	sorted := make([]string, 0, len(names))
	for name := range names {
		sorted = append(sorted, name)
	}
	sort.Strings(sorted)
	for _, name := range sorted {
		initCol, inInit := initShape[name]
		cloneCol, inClone := cloneShape[name]
		switch {
		case !inClone:
			t.Errorf("%s.%s: present on the fresh-init door but missing on the fresh-clone door — a main-plane migration touched this clone-local table without an ignored-series twin", table, name)
		case !inInit:
			t.Errorf("%s.%s: present on the fresh-clone door but missing on the fresh-init door — the ignored series adds something the main series never did", table, name)
		case initCol != cloneCol:
			t.Errorf("%s.%s: shape differs between doors: fresh-init %s, fresh-clone %s",
				table, name, formatClonePlaneColumn(initCol), formatClonePlaneColumn(cloneCol))
		}
	}
}

func formatClonePlaneColumn(c clonePlaneColumn) string {
	def := "NULL"
	if c.Default.Valid {
		def = fmt.Sprintf("%q", c.Default.String)
	}
	return fmt.Sprintf("{type=%s nullable=%s default=%s}", c.ColumnType, c.Nullable, def)
}
