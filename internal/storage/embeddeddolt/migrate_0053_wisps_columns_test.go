//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestEmbeddedMigrate0053AddsMissingWispsColumns_6080 is the embedded-engine
// regression for gastownhall/beads#6080 on a store whose main schema is
// genuinely at v52 (not a latest store with its cursor lowered).
//
// 0023 and 0027 add wisps.no_history and wisps.started_at only if wisps
// exists when they run, and wisps is clone-local (0019), so a clone that
// created wisps later from an older shape reaches v52 without them. 0053
// copies both columns from wisps into issues with a fixed INSERT ... SELECT,
// so it failed at PREPARE ("column started_at could not be found in any table
// in scope") and stranded the ledger at v52. The pre-0053 repair must add the
// missing columns with their original definitions so the pass converges, the
// rig wisp is promoted with its label, the task wisp stays put, and both rows
// keep their timestamps and existing values.
func TestEmbeddedMigrate0053AddsMissingWispsColumns_6080(t *testing.T) {
	requireEmbedded(t)

	cases := []struct {
		name    string
		dropped []string
	}{
		// The reported 2026-04 shape: no_history present, started_at missing.
		{name: "started_at_only", dropped: []string{"started_at"}},
		{name: "both_missing", dropped: []string{"no_history", "started_at"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			dataDir := seedMainSchemaAt(t, ctx, 52)

			conn, closeConn := openPinnedConn(t, ctx, dataDir)
			defer closeConn()

			if v := currentMainVersion(t, ctx, conn); v != 52 {
				t.Fatalf("seeded main version = %d, want 52", v)
			}
			if !wispsColumnExists(t, ctx, conn, "started_at") {
				t.Fatalf("seed at v52 has no wisps.started_at; fixture cannot model #6080")
			}
			// The seeded v52 shape is the reference the repaired columns
			// must match: 0023/0027 created it.
			seedShape := map[string]string{}
			for _, col := range tc.dropped {
				seedShape[col] = wispsColumnShape(t, ctx, conn, col)
			}
			for _, col := range tc.dropped {
				if _, err := conn.ExecContext(ctx, "ALTER TABLE wisps DROP COLUMN "+col); err != nil {
					t.Fatalf("drop wisps.%s: %v", col, err)
				}
			}
			noHistoryKept := !contains(tc.dropped, "no_history")

			const (
				rigID     = "w6080-rig"
				taskID    = "w6080-task"
				rigLabel  = "rig-label-6080"
				createdAt = "2026-04-10 08:00:00"
				rigUpd    = "2026-04-11 09:30:00"
				taskUpd   = "2026-04-12 10:45:00"
			)
			insertWisp := func(id, issueType, updatedAt string) {
				t.Helper()
				cols := "id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, ephemeral, rig, created_at, updated_at"
				vals := "?, ?, '', '', '', '', 'open', 1, ?, 1, 'rig-6080', ?, ?"
				args := []any{id, "title " + id, issueType, createdAt, updatedAt}
				if noHistoryKept {
					// An existing non-default value must survive the repair.
					cols += ", no_history"
					vals += ", 1"
				}
				if _, err := conn.ExecContext(ctx,
					"INSERT INTO wisps ("+cols+") VALUES ("+vals+")", args...); err != nil {
					t.Fatalf("seed wisp %s: %v", id, err)
				}
			}
			insertWisp(rigID, "rig", rigUpd)
			insertWisp(taskID, "task", taskUpd)
			if _, err := conn.ExecContext(ctx,
				"INSERT INTO wisp_labels (issue_id, label) VALUES (?, ?)", rigID, rigLabel); err != nil {
				t.Fatalf("seed rig wisp label: %v", err)
			}

			if _, err := schema.MigrateUp(ctx, conn); err != nil {
				t.Fatalf("MigrateUp from v52 with wisps missing %v: %v", tc.dropped, err)
			}
			if v := currentMainVersion(t, ctx, conn); v != schema.LatestVersion() {
				t.Fatalf("main version = %d, want latest %d", v, schema.LatestVersion())
			}

			for _, col := range []string{"no_history", "started_at"} {
				if !wispsColumnExists(t, ctx, conn, col) {
					t.Fatalf("wisps.%s missing after MigrateUp", col)
				}
			}
			for _, col := range tc.dropped {
				if got, want := wispsColumnShape(t, ctx, conn, col), seedShape[col]; got != want {
					t.Errorf("repaired wisps.%s shape = %q, want original %q", col, got, want)
				}
			}

			wantNoHistory := 0
			if noHistoryKept {
				wantNoHistory = 1
			}
			assertRows := func(pass string) {
				t.Helper()
				// The rig wisp is promoted to issues, durable, with its
				// timestamp and values intact.
				var (
					ephemeral, noHistory int
					updatedAt            string
					startedAt            sql.NullString
					rig                  string
				)
				if err := conn.QueryRowContext(ctx, `
					SELECT ephemeral, no_history, started_at, CAST(updated_at AS CHAR), rig
					FROM issues WHERE id = ? AND issue_type = 'rig'`, rigID).
					Scan(&ephemeral, &noHistory, &startedAt, &updatedAt, &rig); err != nil {
					t.Fatalf("%s: read promoted rig issue: %v", pass, err)
				}
				if ephemeral != 0 || noHistory != wantNoHistory || startedAt.Valid ||
					!strings.HasPrefix(updatedAt, rigUpd) || rig != "rig-6080" {
					t.Fatalf("%s: promoted rig = ephemeral=%d no_history=%d started_at=%v updated_at=%q rig=%q, want 0/%d/NULL/%s/rig-6080",
						pass, ephemeral, noHistory, startedAt, updatedAt, rig, wantNoHistory, rigUpd)
				}
				if n := countRows(t, ctx, conn, "SELECT COUNT(*) FROM issues WHERE id = ?", rigID); n != 1 {
					t.Fatalf("%s: issues rows for rig = %d, want 1", pass, n)
				}
				if n := countRows(t, ctx, conn,
					"SELECT COUNT(*) FROM labels WHERE issue_id = ? AND label = ?", rigID, rigLabel); n != 1 {
					t.Fatalf("%s: promoted rig label rows = %d, want 1", pass, n)
				}
				if n := countRows(t, ctx, conn, "SELECT COUNT(*) FROM wisps WHERE id = ?", rigID); n != 0 {
					t.Fatalf("%s: rig rows left in wisps = %d, want 0", pass, n)
				}

				// The task wisp stays in wisps with its timestamp and values.
				if err := conn.QueryRowContext(ctx, `
					SELECT ephemeral, no_history, started_at, CAST(updated_at AS CHAR)
					FROM wisps WHERE id = ?`, taskID).
					Scan(&ephemeral, &noHistory, &startedAt, &updatedAt); err != nil {
					t.Fatalf("%s: read task wisp: %v", pass, err)
				}
				if ephemeral != 1 || noHistory != wantNoHistory || startedAt.Valid ||
					!strings.HasPrefix(updatedAt, taskUpd) {
					t.Fatalf("%s: task wisp = ephemeral=%d no_history=%d started_at=%v updated_at=%q, want 1/%d/NULL/%s",
						pass, ephemeral, noHistory, startedAt, updatedAt, wantNoHistory, taskUpd)
				}
				if n := countRows(t, ctx, conn, "SELECT COUNT(*) FROM wisps WHERE id = ?", taskID); n != 1 {
					t.Fatalf("%s: task wisp rows = %d, want 1", pass, n)
				}
				if n := countRows(t, ctx, conn, "SELECT COUNT(*) FROM issues WHERE id = ?", taskID); n != 0 {
					t.Fatalf("%s: task wisp copied into issues (%d rows), want 0", pass, n)
				}
			}
			assertRows("first pass")

			// Re-entry on the converged store is a no-op: no duplicates.
			if _, err := schema.MigrateUp(ctx, conn); err != nil {
				t.Fatalf("second MigrateUp: %v", err)
			}
			assertRows("second pass")
		})
	}
}

func wispsColumnExists(t *testing.T, ctx context.Context, conn *sql.Conn, col string) bool {
	t.Helper()
	return countRows(t, ctx, conn, `
		SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps' AND COLUMN_NAME = ?`, col) == 1
}

func wispsColumnShape(t *testing.T, ctx context.Context, conn *sql.Conn, col string) string {
	t.Helper()
	var colType, nullable, def string
	if err := conn.QueryRowContext(ctx, `
		SELECT COLUMN_TYPE, IS_NULLABLE, COALESCE(COLUMN_DEFAULT, '<NULL>')
		FROM INFORMATION_SCHEMA.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps' AND COLUMN_NAME = ?`, col).
		Scan(&colType, &nullable, &def); err != nil {
		t.Fatalf("read wisps.%s shape: %v", col, err)
	}
	return fmt.Sprintf("type=%s nullable=%s default=%s", colType, nullable, def)
}

func countRows(t *testing.T, ctx context.Context, conn *sql.Conn, query string, args ...any) int {
	t.Helper()
	var n int
	if err := conn.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		t.Fatalf("count (%s): %v", query, err)
	}
	return n
}
