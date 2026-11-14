//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestEmbeddedMigrate0053AddsMissingWispsColumns_6080 pins
// gastownhall/beads#6080 on the engine the reporter ran (embeddeddolt).
// Migrations 0023 and 0027 add wisps.no_history and wisps.started_at only
// when the clone-local wisps table already exists when they run, and wisps
// never syncs between clones, so a v32-lineage store that built wisps later
// from an older shape reaches ledger v52 without them. Migration 0053's
// frozen INSERT ... SELECT names both columns, so the upgrade died at
// PREPARE with Error 1105 ("column \"started_at\" could not be found in any
// table in scope") after the ledger had already advanced 32 -> 52, leaving
// the store readable but unwritable with both documented recovery paths
// dead-ended.
//
// The fix (already merged as #8389 when this regression was written) is the
// pre-0053 repair ensureWispsLateColumns: probe the table, probe each late
// column independently, and add exactly what is missing with its original
// 0023/0027 definition. Every supported drift shape runs against the real
// migration runner on a store whose main schema is genuinely at v52 — not a
// lowered cursor — modeling the drift by dropping the columns the drifted
// clones never got. The seeded cases also pin the promotion itself: the rig
// wisp lands in issues with ephemeral=0, its label and its historical
// updated_at, its source row is deleted, the unrelated task wisp keeps its
// values and timestamp, surviving late columns keep their seeded values and
// re-added ones read their defaults.
func TestEmbeddedMigrate0053AddsMissingWispsColumns_6080(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	cases := []wispsLateColumnsCase6080{
		// The reported shape: no_history present (carrying a live 1),
		// started_at absent.
		{name: "started_at_only", drop: []string{"started_at"}, seedRows: true, noHistory: 1},
		// The mirror shape: a live started_at value, no_history absent.
		{name: "no_history_only", drop: []string{"no_history"}, seedRows: true, startedAt: true},
		// A wisps table predating both late columns.
		{name: "both_missing", drop: []string{"no_history", "started_at"}, seedRows: true},
		// The reported drift with no wisp rows at all.
		{name: "started_at_only_empty_wisps", drop: []string{"started_at"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dataDir := seedMainSchemaAt(t, ctx, 52)

			setup, closeSetup := openPinnedConn(t, ctx, dataDir)
			// The freshly migrated shape is the reference the repaired
			// columns must come back with: their original 0023/0027
			// definitions, not a lookalike, captured before the drift is
			// modeled.
			freshShape := map[string]string{}
			for _, col := range tc.drop {
				freshShape[col] = wispsColumnShape6080(t, ctx, setup, col)
			}
			for _, col := range tc.drop {
				if _, err := setup.ExecContext(ctx, "ALTER TABLE wisps DROP COLUMN "+col); err != nil {
					t.Fatalf("model drift: drop wisps.%s: %v", col, err)
				}
			}
			if tc.seedRows {
				seedWispsFixture6080(t, ctx, setup, tc)
			}
			closeSetup()

			// The reporter's recovery command on a fresh pinned session:
			// this is the MigrateUp that stranded their store at v52.
			conn, closeConn := openPinnedConn(t, ctx, dataDir)
			defer closeConn()
			if _, err := schema.MigrateUp(ctx, conn); err != nil {
				t.Fatalf("MigrateUp from v52 with wisps lacking %v: %v", tc.drop, err)
			}
			if v := currentMainVersion(t, ctx, conn); v != schema.LatestVersion() {
				t.Fatalf("main schema version = %d, want latest %d", v, schema.LatestVersion())
			}
			for _, col := range tc.drop {
				if got, want := wispsColumnShape6080(t, ctx, conn, col), freshShape[col]; got != want {
					t.Errorf("wisps.%s shape after repair = %q, want the fresh-store %q", col, got, want)
				}
			}
			if tc.seedRows {
				assertRigPromotion6080(t, ctx, conn, tc)
			}

			// Normal re-entry: a second pass finds nothing to apply and
			// duplicates nothing the first one wrote.
			applied, err := schema.MigrateUp(ctx, conn)
			if err != nil {
				t.Fatalf("re-run MigrateUp: %v", err)
			}
			if applied != 0 {
				t.Errorf("re-run MigrateUp applied = %d, want 0", applied)
			}
			if tc.seedRows {
				assertRigPromotion6080(t, ctx, conn, tc)
			}
		})
	}
}

// wispsLateColumnsCase6080 is one drift shape of
// TestEmbeddedMigrate0053AddsMissingWispsColumns_6080.
type wispsLateColumnsCase6080 struct {
	name string
	drop []string
	// seedRows seeds a rig wisp (with its label) and a task wisp; the
	// empty shape proves 0053 failed at PREPARE even with nothing to
	// promote, so convergence alone is the assertion that matters.
	seedRows bool
	// noHistory, startedAt seed values for the late columns that
	// survive the modeled drift (the dropped ones cannot be named).
	noHistory int
	startedAt bool
}

// Fixture ids and timestamps for TestEmbeddedMigrate0053AddsMissingWispsColumns_6080.
// The stamps are months-old wall-clock values, so no migration-time
// CURRENT_TIMESTAMP can collide with them.
const (
	rigID6080   = "w6080-rig"
	taskID6080  = "w6080-task"
	rigLabel608 = "rig-keep-6080"
)

var (
	rigStamp6080  = time.Date(2026, time.April, 7, 12, 34, 56, 0, time.UTC)
	taskStamp6080 = time.Date(2026, time.April, 6, 10, 5, 0, 0, time.UTC)
	liveStart6080 = time.Date(2026, time.April, 8, 8, 30, 0, 0, time.UTC)
)

// seedWispsFixture6080 seeds the rows 0053 moves and leaves alone. Only
// columns that survive the modeled drift may be named in the rig INSERT, so
// the late columns are appended per case.
func seedWispsFixture6080(t *testing.T, ctx context.Context, conn *sql.Conn, tc wispsLateColumnsCase6080) {
	t.Helper()
	cols := []string{"id", "title", "status", "priority", "issue_type", "ephemeral", "rig", "created_by", "created_at", "updated_at"}
	args := []any{rigID6080, "Rig identity 6080", "open", 1, "rig", true, "test-rig", "seed-6080", rigStamp6080, rigStamp6080}
	if !dropsColumn6080(tc.drop, "no_history") {
		cols = append(cols, "no_history")
		args = append(args, tc.noHistory)
	}
	if !dropsColumn6080(tc.drop, "started_at") {
		cols = append(cols, "started_at")
		if tc.startedAt {
			args = append(args, liveStart6080)
		} else {
			args = append(args, nil)
		}
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(cols)), ", ")
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO wisps ("+strings.Join(cols, ", ")+") VALUES ("+placeholders+")", args...); err != nil {
		t.Fatalf("seed rig wisp %s: %v", rigID6080, err)
	}
	// The rig's label: 0053 must carry it from the clone-local wisp_labels
	// to the shared labels table.
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO wisp_labels (issue_id, label) VALUES (?, ?)", rigID6080, rigLabel608); err != nil {
		t.Fatalf("seed wisp label for %s: %v", rigID6080, err)
	}
	// An unrelated ephemeral task wisp: promoted nowhere, must survive
	// verbatim. When no_history survives the drift it carries a
	// non-default 1, so the repair and the promotion must both leave an
	// existing value alone on a row 0053 does not move.
	taskCols := []string{"id", "title", "status", "priority", "issue_type", "ephemeral", "created_by", "created_at", "updated_at"}
	taskArgs := []any{taskID6080, "Unrelated task wisp", "open", 2, "task", true, "seed-6080", taskStamp6080, taskStamp6080}
	if !dropsColumn6080(tc.drop, "no_history") {
		taskCols = append(taskCols, "no_history")
		taskArgs = append(taskArgs, 1)
	}
	taskPlaceholders := strings.TrimSuffix(strings.Repeat("?, ", len(taskCols)), ", ")
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO wisps ("+strings.Join(taskCols, ", ")+") VALUES ("+taskPlaceholders+")", taskArgs...); err != nil {
		t.Fatalf("seed task wisp %s: %v", taskID6080, err)
	}
}

// assertRigPromotion6080 pins what migration 0053 owes the seeded fixture:
// the rig row lives in issues with ephemeral flipped to 0, its label, its
// rig identity and its historical updated_at; the source wisp row is gone;
// the task wisp is untouched in wisps; late columns that survived the drift
// keep their seeded values through the promotion, and ones the repair
// re-added read their DEFAULT (0 / NULL), not a copy of anything.
func assertRigPromotion6080(t *testing.T, ctx context.Context, conn *sql.Conn, tc wispsLateColumnsCase6080) {
	t.Helper()
	var (
		issueType string
		ephemeral bool
		rig       sql.NullString
		title     string
		updated   time.Time
		noHistory sql.NullBool
		startedAt sql.NullTime
	)
	err := conn.QueryRowContext(ctx, `
		SELECT issue_type, ephemeral, rig, title, updated_at, no_history, started_at
		FROM issues WHERE id = ?
	`, rigID6080).Scan(&issueType, &ephemeral, &rig, &title, &updated, &noHistory, &startedAt)
	if err != nil {
		t.Fatalf("read promoted rig issue %s: %v", rigID6080, err)
	}
	if issueType != "rig" {
		t.Errorf("issues.%s issue_type = %q, want rig", rigID6080, issueType)
	}
	if ephemeral {
		t.Errorf("issues.%s ephemeral = true, want false after 0053's flip", rigID6080)
	}
	if !rig.Valid || rig.String != "test-rig" {
		t.Errorf("issues.%s rig = %+v, want the seeded test-rig", rigID6080, rig)
	}
	if title != "Rig identity 6080" {
		t.Errorf("issues.%s title = %q, want the seeded title", rigID6080, title)
	}
	requireStamp(t, ctx, conn, "issues", rigID6080, rigStamp6080)

	if dropsColumn6080(tc.drop, "no_history") {
		if !noHistory.Valid || noHistory.Bool {
			t.Errorf("issues.%s no_history = %+v, want the DEFAULT 0 the repair's added column carries", rigID6080, noHistory)
		}
	} else if want := tc.noHistory != 0; !noHistory.Valid || noHistory.Bool != want {
		t.Errorf("issues.%s no_history = %+v, want the seeded %d preserved", rigID6080, noHistory, tc.noHistory)
	}
	if dropsColumn6080(tc.drop, "started_at") {
		if startedAt.Valid {
			t.Errorf("issues.%s started_at = %v, want NULL on a column the repair re-added empty", rigID6080, startedAt.Time)
		}
	} else if !startedAt.Valid || !startedAt.Time.Equal(liveStart6080) {
		t.Errorf("issues.%s started_at = %+v, want the seeded %s preserved", rigID6080, startedAt, liveStart6080)
	}

	var labels int
	if err := conn.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM labels WHERE issue_id = ? AND label = ?", rigID6080, rigLabel608).Scan(&labels); err != nil {
		t.Fatalf("count promoted labels for %s: %v", rigID6080, err)
	}
	if labels != 1 {
		t.Errorf("labels rows for %s/%s = %d, want 1", rigID6080, rigLabel608, labels)
	}
	requireGone(t, ctx, conn, "wisps", rigID6080)
	// Exactly one promoted rig row: a re-entry pass must not duplicate it.
	var rigIssues int
	if err := conn.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM issues WHERE id = ?", rigID6080).Scan(&rigIssues); err != nil {
		t.Fatalf("count issues rows for %s: %v", rigID6080, err)
	}
	if rigIssues != 1 {
		t.Errorf("issues rows for %s = %d, want 1", rigID6080, rigIssues)
	}

	var (
		taskTitle     string
		taskPriority  int
		taskEphemeral bool
		taskNoHistory sql.NullBool
		taskStartedAt sql.NullTime
	)
	if err := conn.QueryRowContext(ctx,
		"SELECT title, priority, ephemeral, no_history, started_at FROM wisps WHERE id = ?", taskID6080).
		Scan(&taskTitle, &taskPriority, &taskEphemeral, &taskNoHistory, &taskStartedAt); err != nil {
		t.Fatalf("read task wisp %s: %v", taskID6080, err)
	}
	if taskTitle != "Unrelated task wisp" || taskPriority != 2 {
		t.Errorf("task wisp %s = (%q, %d), want its seeded values", taskID6080, taskTitle, taskPriority)
	}
	if !taskEphemeral {
		t.Errorf("task wisp %s ephemeral = false, want the seeded true (0053 flips only promoted rigs)", taskID6080)
	}
	// A surviving no_history keeps its seeded 1; a re-added one reads 0.
	wantTaskNoHistory := !dropsColumn6080(tc.drop, "no_history")
	if !taskNoHistory.Valid || taskNoHistory.Bool != wantTaskNoHistory {
		t.Errorf("task wisp %s no_history = %+v, want %t", taskID6080, taskNoHistory, wantTaskNoHistory)
	}
	if taskStartedAt.Valid {
		t.Errorf("task wisp %s started_at = %v, want NULL (never seeded)", taskID6080, taskStartedAt.Time)
	}
	requireStamp(t, ctx, conn, "wisps", taskID6080, taskStamp6080)
	requireGone(t, ctx, conn, "issues", taskID6080)
}

func dropsColumn6080(dropped []string, col string) bool {
	for _, d := range dropped {
		if d == col {
			return true
		}
	}
	return false
}

// wispsColumnShape6080 describes wisps.col as the freshly migrated chain
// defines it, so a repaired column can be compared against its original
// definition without the test restating the DDL.
func wispsColumnShape6080(t *testing.T, ctx context.Context, conn *sql.Conn, col string) string {
	t.Helper()
	var colType, nullable, def string
	if err := conn.QueryRowContext(ctx, `
		SELECT COLUMN_TYPE, IS_NULLABLE, COALESCE(COLUMN_DEFAULT, '<NULL>')
		FROM INFORMATION_SCHEMA.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps' AND COLUMN_NAME = ?
	`, col).Scan(&colType, &nullable, &def); err != nil {
		t.Fatalf("read wisps.%s shape: %v", col, err)
	}
	return fmt.Sprintf("type=%s nullable=%s default=%s", colType, nullable, def)
}
