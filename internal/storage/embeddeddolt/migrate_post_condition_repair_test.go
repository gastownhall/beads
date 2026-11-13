//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// GH#5269, the recorded half: a store whose cursor already claims a migration
// that drops a column default, while that default is still in place. Only an
// explicit `bd migrate schema` repairs it (schema.MigrateUpAndRepairPostConditions
// is the embedded entry it calls).

// seedCurrentSchema creates an embedded database at the current main and
// ignored schema, committed clean.
func seedCurrentSchema(t *testing.T, ctx context.Context) string {
	t.Helper()
	dataDir := seedMainSchemaAt(t, ctx, 49)
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp to current: %v", err)
	}
	if dirty := dirtyTableNames(t, ctx, conn); len(dirty) != 0 {
		t.Fatalf("precondition: current schema not clean, dirty %v", dirty)
	}
	return dataDir
}

func execAll(t *testing.T, ctx context.Context, conn *sql.Conn, stmts ...string) {
	t.Helper()
	for _, q := range stmts {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func columnDefault(t *testing.T, ctx context.Context, conn *sql.Conn, table, column string) sql.NullString {
	t.Helper()
	var def sql.NullString
	if err := conn.QueryRowContext(ctx, `SELECT COLUMN_DEFAULT FROM INFORMATION_SCHEMA.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ?`, table, column).Scan(&def); err != nil {
		t.Fatalf("read %s.%s default: %v", table, column, err)
	}
	return def
}

func headCommit(t *testing.T, ctx context.Context, conn *sql.Conn) (hash, message string) {
	t.Helper()
	if err := conn.QueryRowContext(ctx, "SELECT commit_hash, message FROM dolt_log ORDER BY commit_order DESC LIMIT 1").Scan(&hash, &message); err != nil {
		t.Fatalf("read HEAD: %v", err)
	}
	return hash, message
}

func stringColumn(t *testing.T, ctx context.Context, conn *sql.Conn, query string, args ...any) []string {
	t.Helper()
	rows, err := conn.QueryContext(ctx, query, args...)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

func allCursorRows(t *testing.T, ctx context.Context, conn *sql.Conn) []string {
	t.Helper()
	return append(
		stringColumn(t, ctx, conn, "SELECT CONCAT('main ', version) FROM schema_migrations ORDER BY version"),
		stringColumn(t, ctx, conn, "SELECT CONCAT('ignored ', version) FROM ignored_schema_migrations ORDER BY version")...)
}

const repairIssueInsert = "INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) " +
	"VALUES ('rp-1', 'repair fixture', '', '', '', '', 'open', 2, 'task')"

// AC-3: drift on clean tracked tables at the current schema. The explicit
// repair drops exactly the recorded defaults, commits only those tables in its
// own commit (unrelated staged work stays out of it, still in the working
// set), keeps rows, ids and cursor rows, and a repeat creates no commit.
func TestEmbeddedMigrateSchemaRepairsRecordedDefaultDriftOnTrackedTables_5269(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	dataDir := seedCurrentSchema(t, ctx)
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()

	execAll(t, ctx, conn,
		repairIssueInsert,
		"ALTER TABLE dependencies ALTER COLUMN id SET DEFAULT (UUID())",
		"ALTER TABLE comments ALTER COLUMN id SET DEFAULT (UUID())",
		// The reporter's symptom: an insert that omits id gets a random one.
		"INSERT INTO comments (issue_id, author, text) VALUES ('rp-1', 'a', 'minted under drift')",
		"CALL DOLT_COMMIT('-Am', 'test: drifted defaults under recorded marks')",
		// Unrelated staged work the repair commit must not sweep up.
		"UPDATE issues SET title = 'staged, not the repair' WHERE id = 'rp-1'",
		"CALL DOLT_ADD('issues')",
	)
	commentIDs := stringColumn(t, ctx, conn, "SELECT id FROM comments ORDER BY id")
	cursorsBefore := allCursorRows(t, ctx, conn)
	if len(commentIDs) != 1 {
		t.Fatalf("precondition: comments = %v, want one row", commentIDs)
	}

	applied, report, err := schema.MigrateUpAndRepairPostConditions(ctx, conn)
	if err != nil {
		t.Fatalf("MigrateUpAndRepairPostConditions: %v", err)
	}
	if applied != 0 {
		t.Fatalf("applied = %d, want 0 on a current schema", applied)
	}
	if got, want := report.Names(), []string{"comments.id", "dependencies.id"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("repaired %v, want %v", got, want)
	}
	if !report.Committed {
		t.Fatalf("report.Committed = false, want the tracked repair committed")
	}
	for _, tc := range [][2]string{{"dependencies", "id"}, {"comments", "id"}} {
		if def := columnDefault(t, ctx, conn, tc[0], tc[1]); def.Valid {
			t.Fatalf("%s.%s still has DEFAULT %q after the repair", tc[0], tc[1], def.String)
		}
	}

	repairHash, message := headCommit(t, ctx, conn)
	if message != "schema: repair recorded migration post-conditions (GH#5269)" {
		t.Fatalf("HEAD message %q, want the repair's own commit", message)
	}
	changed := stringColumn(t, ctx, conn, "SELECT to_table_name FROM dolt_diff_summary(?, ?) ORDER BY to_table_name", repairHash+"~", repairHash)
	if want := []string{"comments", "dependencies"}; !reflect.DeepEqual(changed, want) {
		t.Fatalf("repair commit changed %v, want only %v", changed, want)
	}
	if dirty := dirtyTableNames(t, ctx, conn); !reflect.DeepEqual(dirty, []string{"issues"}) {
		t.Fatalf("working set after repair = %v, want only the unrelated issues change", dirty)
	}
	var title string
	if err := conn.QueryRowContext(ctx, "SELECT title FROM issues WHERE id = 'rp-1'").Scan(&title); err != nil || title != "staged, not the repair" {
		t.Fatalf("unrelated working-set change lost: title %q, err %v", title, err)
	}
	if got := stringColumn(t, ctx, conn, "SELECT id FROM comments ORDER BY id"); !reflect.DeepEqual(got, commentIDs) {
		t.Fatalf("comment ids %v after repair, want %v unchanged", got, commentIDs)
	}
	if got := allCursorRows(t, ctx, conn); !reflect.DeepEqual(got, cursorsBefore) {
		t.Fatalf("cursor rows changed by the repair")
	}

	_, again, err := schema.MigrateUpAndRepairPostConditions(ctx, conn)
	if err != nil {
		t.Fatalf("repeat: %v", err)
	}
	if len(again.Repaired) != 0 || again.Committed {
		t.Fatalf("repeat repaired %v (committed %v), want a no-op", again.Names(), again.Committed)
	}
	if hash, _ := headCommit(t, ctx, conn); hash != repairHash {
		t.Fatalf("repeat moved HEAD from %s to %s, want no new commit", repairHash, hash)
	}
}
