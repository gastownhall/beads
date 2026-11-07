package dolt

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// The AC-7 bounds for the complete pass: both migration sources from the
// committed main-v58/ignored-v14 fixture state to the binary's latest
// versions, with the same failing-control shape as the per-step bounds.
const (
	planFullChainBound     = 120 * time.Second
	planFullChainKillGrace = 165 * time.Second
)

// planStampBase is the fixed historical timestamp base the full-chain
// fixture stamps every entity with (offset per id): months-old, second
// precision, distinct per row, so a migration that stamps updated_at cannot
// pass as preservation.
var planStampBase = time.Date(2025, time.March, 1, 9, 0, 0, 0, time.UTC)

// planStamp returns the historical updated_at stamped on entity number n.
func planStamp(n int) time.Time { return planStampBase.Add(time.Duration(n) * time.Second) }

// TestMigrationBodyPlanFullChainFromV58 is the full designated-migrator
// rehearsal of gastownhall/beads#6520 (bd-f1s6sq, AC-7): a clean, committed
// main-v58/ignored-v14 database carrying the scaled fixture graph with
// historical updated_at stamps runs the complete migration pass, and both
// cursors must reach the binary's latest versions within the bound, with the
// two planned steps retaining their frozen hashes, both entity kinds at the
// frozen predicate's blocked state, and the historical timestamps preserved.
func TestMigrationBodyPlanFullChainFromV58(t *testing.T) {
	skipIfNoDolt(t)
	acquireTestSlot()
	t.Cleanup(releaseTestSlot)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)

	fixture := buildPlanFixture()
	fixture.checkCounts(t)
	model := fixture.model()
	fixture.assertStructuralExpectations(t, model)

	admin, conn := planScaledDB(t, ctx, fixture)

	exec := func(query string) {
		t.Helper()
		if _, err := conn.ExecContext(ctx, query); err != nil {
			t.Fatalf("exec %.80q: %v", query, err)
		}
	}
	// Historical timestamps, distinct per row, committed: the restored-
	// database shape the report upgraded.
	exec("UPDATE issues SET updated_at = DATE_ADD('2025-03-01 09:00:00', INTERVAL CAST(SUBSTRING(id, 3) AS UNSIGNED) SECOND)")
	exec("UPDATE wisps SET updated_at = DATE_ADD('2025-03-01 09:00:00', INTERVAL CAST(SUBSTRING(id, 3) AS UNSIGNED) SECOND)")
	exec("CALL DOLT_ADD('-A')")
	exec("CALL DOLT_COMMIT('-m', 'test: stamp historical updated_at')")

	var connID int64
	if err := conn.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&connID); err != nil {
		t.Fatalf("read pinned connection id: %v", err)
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-time.After(planFullChainKillGrace):
			_, _ = admin.ExecContext(context.Background(), fmt.Sprintf("KILL QUERY %d", connID))
		case <-done:
		}
	}()

	start := time.Now()
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp failed after %s (the failing control kills the pass at %s; the AC bound is %s): %v",
			time.Since(start).Round(time.Millisecond), planFullChainKillGrace, planFullChainBound, err)
	}
	elapsed := time.Since(start)
	t.Logf("full pass from main v58 / ignored v14 to v%d/ignored v%d: %s",
		schema.LatestVersion(), schema.LatestIgnoredVersion(), elapsed.Round(time.Millisecond))
	if elapsed > planFullChainBound {
		t.Fatalf("full migration pass took %s, want <= %s (#6520)", elapsed.Round(time.Millisecond), planFullChainBound)
	}

	// Both cursors at the binary's latest versions.
	var mainMax, ignoredMax int
	if err := conn.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&mainMax); err != nil {
		t.Fatalf("read main cursor max: %v", err)
	}
	if err := conn.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations").Scan(&ignoredMax); err != nil {
		t.Fatalf("read ignored cursor max: %v", err)
	}
	if mainMax != schema.LatestVersion() {
		t.Errorf("main cursor at v%d, want v%d", mainMax, schema.LatestVersion())
	}
	if ignoredMax != schema.LatestIgnoredVersion() {
		t.Errorf("ignored cursor at v%d, want v%d", ignoredMax, schema.LatestIgnoredVersion())
	}

	// The planned steps retain their frozen hashes.
	for _, cursor := range []struct {
		table, hash string
		version     int
	}{
		{"schema_migrations", planFrozenMain59Hash, 59},
		{"ignored_schema_migrations", planFrozenIgnored15, 15},
	} {
		var got string
		if err := conn.QueryRowContext(ctx,
			fmt.Sprintf("SELECT content_hash FROM %s WHERE version = %d", cursor.table, cursor.version),
		).Scan(&got); err != nil {
			t.Fatalf("read %s row %d: %v", cursor.table, cursor.version, err)
		}
		if got != cursor.hash {
			t.Errorf("%s row %d hash %s, want the frozen file's %s", cursor.table, cursor.version, got, cursor.hash)
		}
	}

	// Both entity kinds: the frozen predicate's blocked state with the
	// historical timestamps preserved (per-row stamps, second precision).
	for _, plane := range []struct {
		table   string
		blocked map[string]bool
		stamp   func(n int) time.Time
	}{
		{"issues", model.issueBlocked, planStamp},
		{"wisps", model.wispBlocked, planStamp},
	} {
		rows, err := conn.QueryContext(ctx, fmt.Sprintf("SELECT id, is_blocked, updated_at FROM %s", plane.table))
		if err != nil {
			t.Fatalf("read %s: %v", plane.table, err)
		}
		blockedWrong, stampedWrong := 0, 0
		for rows.Next() {
			var id string
			var blocked bool
			var updated time.Time
			if err := rows.Scan(&id, &blocked, &updated); err != nil {
				rows.Close()
				t.Fatalf("scan %s row: %v", plane.table, err)
			}
			var n int
			if _, err := fmt.Sscanf(id[2:], "%d", &n); err != nil {
				rows.Close()
				t.Fatalf("parse fixture id %q: %v", id, err)
			}
			if blocked != plane.blocked[id] {
				blockedWrong++
				if blockedWrong <= 5 {
					t.Errorf("%s %s is_blocked=%v, want %v (frozen-predicate model)", plane.table, id, blocked, plane.blocked[id])
				}
			}
			if want := plane.stamp(n).UTC(); !updated.Equal(want) {
				stampedWrong++
				if stampedWrong <= 5 {
					t.Errorf("%s %s updated_at=%s, want its historical %s (the pass overwrote it)",
						plane.table, id, updated.UTC().Format(time.RFC3339), want.Format(time.RFC3339))
				}
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatalf("iterate %s: %v", plane.table, err)
		}
		rows.Close()
		if blockedWrong > 0 || stampedWrong > 0 {
			t.Errorf("%s: %d blocked-state and %d updated_at mismatches across the full pass", plane.table, blockedWrong, stampedWrong)
		}
	}
}
