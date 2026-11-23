//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestEmbeddedMigration0075AddsClosedByWithoutRewritingRows pins migration
// 0075 (GH#8617) on a real engine, upgrading a database that sits at 0074 and
// holds open and closed rows on both planes:
//
//   - issues and wisps both gain closed_by;
//   - every existing row reads ” (no backfill), with content_hash and
//     updated_at exactly as they were;
//   - a replay of the frozen up.sql is a no-op that keeps a stamped value.
func TestEmbeddedMigration0075AddsClosedByWithoutRewritingRows(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	dataDir := seedMainSchemaAt(t, ctx, 74)
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()

	for _, table := range []string{"issues", "wisps"} {
		for _, row := range []struct{ id, status, closedAt string }{
			{table + "-open", "open", "NULL"},
			{table + "-closed", "closed", "'2026-03-01 12:00:00'"},
		} {
			execFrozenGuard(t, ctx, conn, `INSERT INTO `+table+` (id, content_hash, title, description, design,
				acceptance_criteria, notes, status, closed_at, updated_at)
				VALUES ('`+row.id+`', 'hash-`+row.id+`', 't', '', '', '', '', '`+row.status+`', `+row.closedAt+`,
				'2026-02-01 08:00:00')`)
		}
	}
	// MigrateUp refuses to alter a table with uncommitted rows (#4566), so the
	// seeded issues rows are committed the way a real 0074 store holds them.
	if _, err := conn.ExecContext(ctx, "CALL DOLT_ADD('issues')"); err != nil {
		t.Fatalf("stage seeded issues: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-m', 'seed 0074 rows')"); err != nil {
		t.Fatalf("commit seeded issues: %v", err)
	}
	before := closedByRowFingerprints(t, ctx, conn)

	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp from 0074: %v", err)
	}

	for _, table := range []string{"issues", "wisps"} {
		var colType string
		var colDefault sql.NullString
		if err := conn.QueryRowContext(ctx, `SELECT COLUMN_TYPE, COLUMN_DEFAULT FROM INFORMATION_SCHEMA.COLUMNS
			WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = 'closed_by'`, table).Scan(&colType, &colDefault); err != nil {
			t.Fatalf("%s.closed_by after MigrateUp: %v", table, err)
		}
		if colType != "varchar(255)" {
			t.Errorf("%s.closed_by type = %q, want varchar(255) (created_by's shape)", table, colType)
		}
		if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM "+table+" WHERE closed_by IS NULL OR closed_by <> ''"); got != 0 {
			t.Errorf("%s: %d existing rows read a closed_by other than '' after 0075 (no backfill)", table, got)
		}
	}
	after := closedByRowFingerprints(t, ctx, conn)
	for id, want := range before {
		if got := after[id]; got != want {
			t.Errorf("row %s content_hash/updated_at = %q after 0075, want %q (the column add rewrites no row)", id, got, want)
		}
	}

	execFrozenGuard(t, ctx, conn, "UPDATE issues SET closed_by = 'closer-a' WHERE id = 'issues-closed'")
	upSQL, err := schema.MigrationSQL("0075_add_closed_by.up.sql")
	if err != nil {
		t.Fatalf("read 0075: %v", err)
	}
	execFrozenGuard(t, ctx, conn, upSQL)
	if got := scalarString(t, ctx, conn, "SELECT closed_by FROM issues WHERE id = 'issues-closed'"); got != "closer-a" {
		t.Errorf("closed_by after a 0075 replay = %q, want the stamped %q (replay is a no-op)", got, "closer-a")
	}
	twinSQL, err := schema.IgnoredMigrationSQL("0032_add_wisps_closed_by.up.sql")
	if err != nil {
		t.Fatalf("read ignored 0032: %v", err)
	}
	execFrozenGuard(t, ctx, conn, twinSQL)
	if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM wisps"); got != 2 {
		t.Errorf("wisps rows after an ignored/0032 replay = %d, want 2", got)
	}
}

// closedByRowFingerprints maps every row id on both planes to its
// content_hash and updated_at.
func closedByRowFingerprints(t *testing.T, ctx context.Context, conn *sql.Conn) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, table := range []string{"issues", "wisps"} {
		rows, err := conn.QueryContext(ctx, "SELECT id, COALESCE(content_hash, ''), CAST(updated_at AS CHAR) FROM "+table)
		if err != nil {
			t.Fatalf("read %s fingerprints: %v", table, err)
		}
		for rows.Next() {
			var id, hash, updated string
			if err := rows.Scan(&id, &hash, &updated); err != nil {
				_ = rows.Close()
				t.Fatalf("scan %s fingerprint: %v", table, err)
			}
			out[id] = hash + "@" + updated
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("iterate %s fingerprints: %v", table, err)
		}
		_ = rows.Close()
	}
	if len(out) != 4 {
		t.Fatalf("fingerprinted %d rows, want 4", len(out))
	}
	return out
}
