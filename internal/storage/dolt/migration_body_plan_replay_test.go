package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestMigrationBodyPlanReplayConverges proves the interruption story of
// gastownhall/beads#6520's execution plans (bd-f1s6sq): a partially executed
// 0059 — stand-ins created and filled, issues zeroed, no version-59 cursor
// row — is replayed by the bounded runner when the pass resumes, and must
// converge to the uninterrupted result: the frozen predicate's blocked
// state, the frozen file's cursor hash, no stand-ins left behind, and no
// index changes on the real tables.
//
// The old-body prefix is the state a pre-plan binary leaves behind (the
// frozen body ran until interruption); the planned-body prefix (added with
// the plan's own SQL export) is the state a planned run interrupted the same
// way leaves behind. Both replay through the same runner seam.
func TestMigrationBodyPlanReplayConverges(t *testing.T) {
	skipIfNoDolt(t)
	fixture := buildPlanFixture()
	fixture.checkCounts(t)
	model := fixture.model()

	t.Run("old_body_prefix", func(t *testing.T) {
		skipIfNoDolt(t)
		acquireTestSlot()
		t.Cleanup(releaseTestSlot)

		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		t.Cleanup(cancel)

		admin, conn := planScaledDB(t, ctx, fixture)
		before := planRealTableIndexes(t, ctx, conn)

		// The interrupted old-body run: the frozen prefix through the
		// zeroing UPDATE executes (stand-ins dropped/recreated and filled by
		// the guarded copies, every issue's is_blocked zeroed), and the
		// recompute never starts.
		frozen, err := schema.MigrationSQL("0059_recompute_null_gate_is_blocked.up.sql")
		if err != nil {
			t.Fatalf("MigrationSQL(0059): %v", err)
		}
		head := strings.Index(frozen, "\nWITH RECURSIVE\n")
		if head < 0 {
			t.Fatalf("0059 body no longer carries the WITH RECURSIVE head this test splits on")
		}
		if _, err := conn.ExecContext(ctx, frozen[:head]); err != nil {
			t.Fatalf("apply the interrupted old-body prefix: %v", err)
		}
		var cursorRows int
		if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE version = 59").Scan(&cursorRows); err != nil {
			t.Fatalf("read cursor rows: %v", err)
		}
		if cursorRows != 0 {
			t.Fatalf("fixture bug: version 59 already recorded before the replay")
		}

		// The bounded failing control, as in the scaled test: the pinned
		// connection's id up front, a separate connection kills that exact
		// query once the grace deadline passes.
		var connID int64
		if err := conn.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&connID); err != nil {
			t.Fatalf("read pinned connection id: %v", err)
		}
		done := make(chan struct{})
		defer close(done)
		go func() {
			select {
			case <-time.After(planKillGrace):
				_, _ = admin.ExecContext(context.Background(), fmt.Sprintf("KILL QUERY %d", connID))
			case <-done:
			}
		}()

		start := time.Now()
		_, err = schema.MigrateUpTo(ctx, conn, 59)
		elapsed := time.Since(start)
		t.Logf("0059 replay over the interrupted old-body prefix: %s", elapsed.Round(time.Millisecond))
		if err != nil {
			t.Fatalf("replaying 0059 over the interrupted old-body prefix failed after %s (the failing control kills the step at %s; the AC bound is %s): %v",
				elapsed.Round(time.Millisecond), planKillGrace, planStepBound, err)
		}
		if elapsed > planStepBound {
			t.Fatalf("0059 replay took %s, want <= %s (#6520)", elapsed.Round(time.Millisecond), planStepBound)
		}

		planAssertConverged(t, ctx, conn, model, before)
	})

	t.Run("planned_body_prefix", func(t *testing.T) {
		skipIfNoDolt(t)
		acquireTestSlot()
		t.Cleanup(releaseTestSlot)

		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		t.Cleanup(cancel)

		admin, conn := planScaledDB(t, ctx, fixture)
		before := planRealTableIndexes(t, ctx, conn)

		// The interrupted planned-body run: everything through the plan's
		// stand-in indexes executes (stand-ins filled, plan indexes
		// created), the recompute never starts. The indexes' presence is
		// the wedge a naive replay would hit: re-executing the body must
		// not fail on them, because the frozen prefix's DROP TABLE IF
		// EXISTS recreates the stand-in fresh and its indexes die with the
		// dropped table.
		planned, err := schema.PlannedMigrationSQL(59)
		if err != nil {
			t.Fatalf("PlannedMigrationSQL(59): %v", err)
		}
		head := strings.Index(planned, "\nWITH RECURSIVE\n")
		if head < 0 {
			t.Fatalf("planned 0059 body carries no WITH RECURSIVE head to split on")
		}
		if _, err := conn.ExecContext(ctx, planned[:head]); err != nil {
			t.Fatalf("apply the interrupted planned-body prefix: %v", err)
		}
		var planIndexes int
		if err := conn.QueryRowContext(ctx,
			"SELECT COUNT(DISTINCT INDEX_NAME) FROM information_schema.statistics WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = '__bd_0059_recompute_wisp_deps' AND INDEX_NAME LIKE 'idx_bd0059%'",
		).Scan(&planIndexes); err != nil {
			t.Fatalf("read plan indexes: %v", err)
		}
		if planIndexes != 3 {
			t.Fatalf("interrupted-state bug: %d plan stand-in indexes exist, want 3 (the prefix must actually create them)", planIndexes)
		}
		var cursorRows int
		if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE version = 59").Scan(&cursorRows); err != nil {
			t.Fatalf("read cursor rows: %v", err)
		}
		if cursorRows != 0 {
			t.Fatalf("fixture bug: version 59 already recorded before the replay")
		}

		var connID int64
		if err := conn.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&connID); err != nil {
			t.Fatalf("read pinned connection id: %v", err)
		}
		done := make(chan struct{})
		defer close(done)
		go func() {
			select {
			case <-time.After(planKillGrace):
				_, _ = admin.ExecContext(context.Background(), fmt.Sprintf("KILL QUERY %d", connID))
			case <-done:
			}
		}()

		start := time.Now()
		_, err = schema.MigrateUpTo(ctx, conn, 59)
		elapsed := time.Since(start)
		t.Logf("0059 replay over the interrupted planned-body prefix: %s", elapsed.Round(time.Millisecond))
		if err != nil {
			t.Fatalf("replaying 0059 over the interrupted planned-body prefix failed after %s (the failing control kills the step at %s; the AC bound is %s): %v",
				elapsed.Round(time.Millisecond), planKillGrace, planStepBound, err)
		}
		if elapsed > planStepBound {
			t.Fatalf("0059 replay took %s, want <= %s (#6520)", elapsed.Round(time.Millisecond), planStepBound)
		}

		planAssertConverged(t, ctx, conn, model, before)
	})
}

// planRealTableIndexes snapshots the real tables' indexes as a comparable
// string: the replay must leave them alone (the plan's DDL touches only the
// 0059 stand-ins, which the frozen suffix drops).
func planRealTableIndexes(t *testing.T, ctx context.Context, conn *sql.Conn) string {
	t.Helper()
	rows, err := conn.QueryContext(ctx, `
		SELECT TABLE_NAME, INDEX_NAME, SEQ_IN_INDEX, COLUMN_NAME, NON_UNIQUE
		FROM information_schema.statistics
		WHERE TABLE_SCHEMA = DATABASE()
		  AND TABLE_NAME IN ('issues', 'dependencies', 'wisps', 'wisp_dependencies')
		ORDER BY TABLE_NAME, INDEX_NAME, SEQ_IN_INDEX`)
	if err != nil {
		t.Fatalf("read real-table indexes: %v", err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var table, name, column string
		var seq, nonUnique int
		if err := rows.Scan(&table, &name, &seq, &column, &nonUnique); err != nil {
			t.Fatalf("scan index row: %v", err)
		}
		fmt.Fprintf(&b, "%s/%s/%d/%s/%d\n", table, name, seq, column, nonUnique)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate index rows: %v", err)
	}
	return b.String()
}

// planAssertConverged asserts the post-replay converged state: the frozen
// predicate's issue blocked state, the frozen cursor hash, no 0059 stand-ins
// left behind, and the real tables' indexes unchanged.
func planAssertConverged(t *testing.T, ctx context.Context, conn *sql.Conn, model planModel, before string) {
	t.Helper()

	rows, err := conn.QueryContext(ctx, "SELECT id, is_blocked FROM issues")
	if err != nil {
		t.Fatalf("read issues: %v", err)
	}
	defer rows.Close()
	mismatches := 0
	for rows.Next() {
		var id string
		var blocked bool
		if err := rows.Scan(&id, &blocked); err != nil {
			t.Fatalf("scan issue: %v", err)
		}
		if blocked != model.issueBlocked[id] {
			mismatches++
			if mismatches <= 5 {
				t.Errorf("issue %s is_blocked=%v, want %v (frozen-predicate model)", id, blocked, model.issueBlocked[id])
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate issues: %v", err)
	}
	if mismatches > 0 {
		t.Fatalf("%d of %d issues disagree with the frozen-predicate model after replay", mismatches, planFixtureIssues)
	}

	var hash string
	if err := conn.QueryRowContext(ctx, "SELECT content_hash FROM schema_migrations WHERE version = 59").Scan(&hash); err != nil {
		t.Fatalf("read main 59 cursor row: %v", err)
	}
	if hash != planFrozenMain59Hash {
		t.Errorf("main 59 cursor hash %s, want the frozen file's %s", hash, planFrozenMain59Hash)
	}

	var standIns int
	if err := conn.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM information_schema.tables WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME LIKE '__bd_0059%'",
	).Scan(&standIns); err != nil {
		t.Fatalf("read stand-in tables: %v", err)
	}
	if standIns != 0 {
		t.Errorf("%d 0059 stand-in table(s) remain after the step committed", standIns)
	}

	if after := planRealTableIndexes(t, ctx, conn); after != before {
		t.Errorf("real-table indexes changed across the interrupted run and replay:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}
