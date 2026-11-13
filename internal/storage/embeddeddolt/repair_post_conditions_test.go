//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// seedFullyCurrentSchema leaves a database current on BOTH migration series —
// seedMainSchemaAt migrates the main series only, so the ignored plane (its
// cursor table, the dolt_ignore patterns, the wisp-default drops of ignored
// 0010) never materializes without the full pass. Returns the dataDir.
func seedFullyCurrentSchema(t *testing.T, ctx context.Context) string {
	t.Helper()
	dataDir := seedMainSchemaAt(t, ctx, 49)
	withPinnedConn(t, dataDir, func(ctx context.Context, conn *sql.Conn) {
		if _, err := schema.MigrateUp(ctx, conn); err != nil {
			t.Fatalf("full MigrateUp to current: %v", err)
		}
	})
	return dataDir
}

// withPinnedConn opens the database for a fixture or verification phase and
// closes the engine before returning: two doltembed engines on one data
// directory serialize forever (the second backs off with no deadline), so a
// phase's connection must be gone before the store phase opens its own.
func withPinnedConn(t *testing.T, dataDir string, phase func(ctx context.Context, conn *sql.Conn)) {
	t.Helper()
	ctx := t.Context()
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	phase(ctx, conn)
	closeConn()
}

// GH#5269, AC-3: on a database whose recorded marks outran its DDL — here a
// fully-current store with committed SET DEFAULT drift on two tracked tables —
// the explicit `bd migrate schema` pass (the store's RepairSchemaPostConditions)
// repairs exactly the drifted shapes in one repair commit, preserves rows and
// cursor records untouched, and a repeat run creates no new commit.
func TestEmbeddedRepairSchemaPostConditionsRepairsCommittedDrift_5269(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	dataDir := seedFullyCurrentSchema(t, ctx)
	beadsDir := filepath.Dir(dataDir)

	// Marker rows plus committed drift on two tracked tables: the reporter's
	// state (marks current, defaults back) reproduced deliberately.
	var mainCursorBefore, ignoredCursorBefore string
	withPinnedConn(t, dataDir, func(ctx context.Context, conn *sql.Conn) {
		for _, q := range []string{
			"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES ('repair-marker-1', 'repair marker', '', '', '', '', 'open', 2, 'task')",
			"INSERT INTO comments (id, issue_id, author, text) VALUES ('repair-marker-comment-1', 'repair-marker-1', 'marker', 'survives the repair')",
			"ALTER TABLE dependencies ALTER COLUMN id SET DEFAULT (UUID())",
			"ALTER TABLE comments ALTER COLUMN id SET DEFAULT (UUID())",
			"CALL DOLT_COMMIT('-Am', 'test: marker rows plus drifted defaults')",
		} {
			if _, err := conn.ExecContext(ctx, q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
		if def := tableColumnDefault(t, ctx, conn, "dependencies", "id"); !def.Valid {
			t.Fatalf("precondition: dependencies.id carries no DEFAULT before the repair")
		}
		if def := tableColumnDefault(t, ctx, conn, "comments", "id"); !def.Valid {
			t.Fatalf("precondition: comments.id carries no DEFAULT before the repair")
		}
		mainCursorBefore = cursorDump(t, ctx, conn, "schema_migrations")
		ignoredCursorBefore = cursorDump(t, ctx, conn, "ignored_schema_migrations")
	})

	store, err := embeddeddolt.Open(ctx, beadsDir, "testdb", "main")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	applied, repairs, err := store.RepairSchemaPostConditions(ctx)
	if err != nil {
		t.Fatalf("RepairSchemaPostConditions: %v", err)
	}
	if applied != 0 {
		t.Fatalf("applied = %d on a current store, want 0 (the repair is not a re-migration)", applied)
	}
	if strings.Join(repairs, ",") != "comments.id,dependencies.id" {
		t.Fatalf("repairs = %v, want [comments.id dependencies.id]", repairs)
	}

	withPinnedConn(t, dataDir, func(ctx context.Context, conn *sql.Conn) {
		// The drifted defaults are gone; the marker rows survived.
		if def := tableColumnDefault(t, ctx, conn, "dependencies", "id"); def.Valid {
			t.Fatalf("dependencies.id still carries DEFAULT %q after the repair", def.String)
		}
		if def := tableColumnDefault(t, ctx, conn, "comments", "id"); def.Valid {
			t.Fatalf("comments.id still carries DEFAULT %q after the repair", def.String)
		}
		var issueTitle, commentText string
		if err := conn.QueryRowContext(ctx, "SELECT title FROM issues WHERE id = 'repair-marker-1'").Scan(&issueTitle); err != nil {
			t.Fatalf("marker issue lost by the repair: %v", err)
		}
		if err := conn.QueryRowContext(ctx, "SELECT text FROM comments WHERE id = 'repair-marker-comment-1'").Scan(&commentText); err != nil {
			t.Fatalf("marker comment lost by the repair: %v", err)
		}
		// Cursor records are preserved verbatim: versions and content hashes.
		if after := cursorDump(t, ctx, conn, "schema_migrations"); after != mainCursorBefore {
			t.Fatalf("schema_migrations records changed:\nbefore:\n%s\nafter:\n%s", mainCursorBefore, after)
		}
		if after := cursorDump(t, ctx, conn, "ignored_schema_migrations"); after != ignoredCursorBefore {
			t.Fatalf("ignored_schema_migrations records changed:\nbefore:\n%s\nafter:\n%s", ignoredCursorBefore, after)
		}
		// Exactly one repair commit, named for what it is, and a clean working set.
		var repairCommits int
		if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_log WHERE message = 'schema: repair dropped-default post-conditions'").Scan(&repairCommits); err != nil {
			t.Fatalf("read dolt_log: %v", err)
		}
		if repairCommits != 1 {
			t.Fatalf("repair commits = %d, want 1", repairCommits)
		}
		if dirty := dirtyTableNames(t, ctx, conn); len(dirty) != 0 {
			t.Fatalf("working set dirty after the repair commit (%v)", dirty)
		}
	})

	// A repeat run reports no repairs and creates no new commit.
	if applied, repairs, err := store.RepairSchemaPostConditions(ctx); err != nil || applied != 0 || len(repairs) != 0 {
		t.Fatalf("repeat RepairSchemaPostConditions = (%d, %v, %v), want no repairs and no error", applied, repairs, err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	withPinnedConn(t, dataDir, func(ctx context.Context, conn *sql.Conn) {
		var repairCommits int
		if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_log WHERE message = 'schema: repair dropped-default post-conditions'").Scan(&repairCommits); err != nil {
			t.Fatalf("re-read dolt_log: %v", err)
		}
		if repairCommits != 1 {
			t.Fatalf("repeat run created another repair commit (%d)", repairCommits)
		}
	})
}

// tableColumnDefault reads a column's default from INFORMATION_SCHEMA: NULL
// (invalid) means no default, the same view SHOW COLUMNS gives the repair.
func tableColumnDefault(t *testing.T, ctx context.Context, conn *sql.Conn, table, column string) sql.NullString {
	t.Helper()
	var def sql.NullString
	if err := conn.QueryRowContext(ctx, "SELECT COLUMN_DEFAULT FROM INFORMATION_SCHEMA.COLUMNS"+
		" WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ?", table, column).Scan(&def); err != nil {
		t.Fatalf("read %s.%s default: %v", table, column, err)
	}
	return def
}

// cursorDump renders a cursor table's (version, content_hash) records for
// before/after comparison: the repair must never rewrite them.
func cursorDump(t *testing.T, ctx context.Context, conn *sql.Conn, cursorTable string) string {
	t.Helper()
	rows, err := conn.QueryContext(ctx, "SELECT version, COALESCE(content_hash, '') FROM "+cursorTable+" ORDER BY version")
	if err != nil {
		t.Fatalf("read %s: %v", cursorTable, err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var version int
		var hash string
		if err := rows.Scan(&version, &hash); err != nil {
			t.Fatalf("scan %s row: %v", cursorTable, err)
		}
		fmt.Fprintf(&b, "%d:%s\n", version, hash)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate %s: %v", cursorTable, err)
	}
	return b.String()
}

// The repair reaches the ignored plane too (AC-5): wisp tables and, since main
// 0062, events are under dolt_ignore, so their repair lands in place — the
// default is dropped, nothing is staged, and no commit enters history.
func TestEmbeddedRepairSchemaPostConditionsRepairsIgnoredTablesInPlace_5269(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	dataDir := seedFullyCurrentSchema(t, ctx)
	beadsDir := filepath.Dir(dataDir)

	var logCountBefore int
	withPinnedConn(t, dataDir, func(ctx context.Context, conn *sql.Conn) {
		for _, q := range []string{
			// A marker row on the ignored plane: it must survive the repair.
			// wisp_events.issue_id carries a foreign key into wisps, so the
			// parent wisp is seeded first (title is its only defaultless
			// column).
			"INSERT INTO wisps (id, title) VALUES ('repair-marker-wisp', 'repair marker')",
			"INSERT INTO wisp_events (id, issue_id, event_type, actor) VALUES ('repair-wisp-marker-1', 'repair-marker-wisp', 'noted', 'marker')",
			"ALTER TABLE wisp_events ALTER COLUMN id SET DEFAULT (UUID())",
			"ALTER TABLE wisp_comments ALTER COLUMN id SET DEFAULT (UUID())",
			"ALTER TABLE wisp_dependencies ALTER COLUMN id SET DEFAULT (UUID())",
			"ALTER TABLE events ALTER COLUMN id SET DEFAULT (UUID())",
		} {
			if _, err := conn.ExecContext(ctx, q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
		if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_log").Scan(&logCountBefore); err != nil {
			t.Fatalf("count dolt_log: %v", err)
		}
	})

	store, err := embeddeddolt.Open(ctx, beadsDir, "testdb", "main")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	_, repairs, err := store.RepairSchemaPostConditions(ctx)
	if err != nil {
		t.Fatalf("RepairSchemaPostConditions: %v", err)
	}
	if strings.Join(repairs, ",") != "events.id,wisp_comments.id,wisp_dependencies.id,wisp_events.id" {
		t.Fatalf("repairs = %v, want [events.id wisp_comments.id wisp_dependencies.id wisp_events.id]", repairs)
	}

	withPinnedConn(t, dataDir, func(ctx context.Context, conn *sql.Conn) {
		// Defaults dropped in place; rows untouched; history untouched — no
		// commit at all, not even a repair-shaped one.
		for _, tc := range [][2]string{{"events", "id"}, {"wisp_events", "id"}, {"wisp_comments", "id"}, {"wisp_dependencies", "id"}} {
			if def := tableColumnDefault(t, ctx, conn, tc[0], tc[1]); def.Valid {
				t.Fatalf("%s.%s still carries DEFAULT %q after the repair", tc[0], tc[1], def.String)
			}
		}
		var wispMarkers int
		if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM wisp_events WHERE id = 'repair-wisp-marker-1'").Scan(&wispMarkers); err != nil {
			t.Fatalf("read wisp_events markers: %v", err)
		}
		if wispMarkers != 1 {
			t.Fatalf("wisp_events marker rows = %d after the repair, want 1", wispMarkers)
		}
		var logCountAfter int
		if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_log").Scan(&logCountAfter); err != nil {
			t.Fatalf("count dolt_log: %v", err)
		}
		if logCountAfter != logCountBefore {
			t.Fatalf("dolt_log grew from %d to %d commits: ignored-plane repair must not touch history", logCountBefore, logCountAfter)
		}
		var repairCommits int
		if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_log WHERE message = 'schema: repair dropped-default post-conditions'").Scan(&repairCommits); err != nil {
			t.Fatalf("read dolt_log: %v", err)
		}
		if repairCommits != 0 {
			t.Fatalf("ignored-plane-only repair created %d commit(s)", repairCommits)
		}
	})
}

// GH#5269, AC-4: a tracked repair target holding uncommitted changes is
// refused before anything is altered — durably: the default survives, no
// repair commit exists, and the table's working-set change is still there.
func TestEmbeddedRepairRefusesDirtyTrackedTargetBeforeAltering_5269(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	dataDir := seedFullyCurrentSchema(t, ctx)
	beadsDir := filepath.Dir(dataDir)

	// Uncommitted drift on a tracked table: dependencies carries a default
	// AND an uncommitted schema change.
	withPinnedConn(t, dataDir, func(ctx context.Context, conn *sql.Conn) {
		if _, err := conn.ExecContext(ctx, "ALTER TABLE dependencies ALTER COLUMN id SET DEFAULT (UUID())"); err != nil {
			t.Fatalf("seed uncommitted drift: %v", err)
		}
		if def := tableColumnDefault(t, ctx, conn, "dependencies", "id"); !def.Valid {
			t.Fatalf("precondition: dependencies.id carries no DEFAULT after seeding")
		}
		if !contains(dirtyTableNames(t, ctx, conn), "dependencies") {
			t.Fatalf("precondition: dependencies is not dirty in dolt_status")
		}
	})

	store, err := embeddeddolt.Open(ctx, beadsDir, "testdb", "main")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	_, repairs, repairErr := store.RepairSchemaPostConditions(ctx)
	var dirty *schema.PostConditionDirtyWorkError
	if !errors.As(repairErr, &dirty) {
		t.Fatalf("err = %T %v, want *schema.PostConditionDirtyWorkError", repairErr, repairErr)
	}
	if strings.Join(dirty.Tables, ",") != "dependencies" {
		t.Fatalf("dirty tables = %v, want [dependencies]", dirty.Tables)
	}
	if repairs != nil {
		t.Fatalf("repairs = %v alongside a refusal, want none", repairs)
	}

	withPinnedConn(t, dataDir, func(ctx context.Context, conn *sql.Conn) {
		if def := tableColumnDefault(t, ctx, conn, "dependencies", "id"); !def.Valid {
			t.Fatalf("dependencies.id was altered despite the refusal (DEFAULT %q gone)", def.String)
		}
		if !contains(dirtyTableNames(t, ctx, conn), "dependencies") {
			t.Fatalf("dependencies lost its uncommitted change to the refused repair")
		}
		var repairCommits int
		if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_log WHERE message = 'schema: repair dropped-default post-conditions'").Scan(&repairCommits); err != nil {
			t.Fatalf("read dolt_log: %v", err)
		}
		if repairCommits != 0 {
			t.Fatalf("refused repair created %d commit(s)", repairCommits)
		}
	})
}

// Compile-time pin: the store implements the capability interface the CLI's
// `bd migrate schema` looks for.
var _ storage.SchemaPostConditionRepairer = (*embeddeddolt.EmbeddedDoltStore)(nil)
