//go:build cgo

package embeddeddolt_test

import (
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
)

// TestEmbeddedWakeExpiredDefersSkipsDuringMigrationFreeze pins the per-open
// freeze gate (#7634; evolved from PR #7653's TestEmbeddedDeferWakeSweepSwitch,
// with the sweep switch promoted into the classified-read constructor the
// approved design chose): a store opened frozen through
// OpenForReadOnlyCommand leaves a committed expired deferral exactly as it
// stands through EVERY entry point that sweeps — the three ready reads, the
// count read, and the advisory sweep itself — writing no defer-wake commit
// and moving neither HEAD nor the dolt_log row count. The wisp leg matters
// because a wisp wake persists with the SQL commit alone and mints no version
// commit: only its row, not HEAD, would show that write. An independently
// reopened unfrozen store still performs the normal wake, proving the freeze
// postponed the sweep rather than disabling it; the dateless and future
// defers stay deferred through both.
func TestEmbeddedWakeExpiredDefersSkipsDuringMigrationFreeze(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	ctx := t.Context()
	te := newTestEnv(t, "dwf")
	beadsDir := filepath.Dir(te.dataDir)

	seed := func(title string) *types.Issue {
		issue := &types.Issue{Title: title, Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}
		return issue
	}

	// The expired defer under test, committed so a suppressed sweep cannot be
	// confused with uncommitted state.
	expired := seed("frozen snooze")
	te.exec(t, ctx, "UPDATE issues SET status = 'deferred', defer_until = '2020-01-01 00:00:00' WHERE id = ?", expired.ID)
	// A dateless defer (the indefinite icebox) and a future-dated one must
	// stay deferred regardless of the freeze.
	dateless := seed("dateless icebox")
	te.exec(t, ctx, "UPDATE issues SET status = 'deferred', defer_until = NULL WHERE id = ?", dateless.ID)
	future := seed("future snooze")
	te.exec(t, ctx, "UPDATE issues SET status = 'deferred', defer_until = '2099-01-01 00:00:00' WHERE id = ?", future.ID)
	if _, err := te.store.CommitPending(ctx, "seed defers"); err != nil {
		t.Fatalf("CommitPending seed: %v", err)
	}
	// An expired wisp defer: wisp wakes persist with the SQL commit but mint
	// no version commit, so only its row would show the write.
	wisp := &types.Issue{Title: "wisp snooze", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, Ephemeral: true}
	if err := te.store.CreateIssue(ctx, wisp, "tester"); err != nil {
		t.Fatalf("create wisp: %v", err)
	}
	te.exec(t, ctx, "UPDATE wisps SET status = 'deferred', defer_until = '2020-01-01 00:00:00' WHERE id = ?", wisp.ID)

	// Close the bootstrap store before the frozen open: the open cache reuses
	// the first store a directory ever got, so the freeze value only the new
	// open carries must not be shadowed by a live earlier reference (see
	// openCached's invariant note).
	closeEmbeddedDoltStore(t, te.store)

	head := func() string {
		var h string
		te.queryScalar(t, ctx, "SELECT HASHOF('HEAD')", nil, &h)
		return h
	}
	logCount := func() int {
		var n int
		te.queryScalar(t, ctx, "SELECT COUNT(*) FROM dolt_log", nil, &n)
		return n
	}
	wakeCommits := func() int {
		var n int
		te.queryScalar(t, ctx, "SELECT COUNT(*) FROM dolt_log WHERE message LIKE 'bd: wake %'", nil, &n)
		return n
	}
	assertAsleep := func(read string, beforeHead string, beforeLog int) {
		t.Helper()
		for _, row := range []struct{ table, id string }{
			{"issues", expired.ID},
			{"wisps", wisp.ID},
		} {
			var status string
			var deferUntil *string
			te.queryScalar(t, ctx, "SELECT status, defer_until FROM "+row.table+" WHERE id = ?", []any{row.id}, &status, &deferUntil)
			if status != "deferred" || deferUntil == nil {
				t.Errorf("%s woke %s %s: status = %q, defer_until = %v; want deferred with its date", read, row.table, row.id, status, deferUntil)
			}
		}
		if h := head(); h != beforeHead {
			t.Errorf("%s moved HEAD %s -> %s", read, beforeHead, h)
		}
		if n := logCount(); n != beforeLog {
			t.Errorf("%s changed dolt_log %d -> %d rows", read, beforeLog, n)
		}
		if n := wakeCommits(); n != 0 {
			t.Errorf("%s minted %d defer-wake commit(s)", read, n)
		}
	}

	frozen, err := embeddeddolt.OpenForReadOnlyCommand(ctx, beadsDir, te.database, "main", true)
	if err != nil {
		t.Fatalf("OpenForReadOnlyCommand (frozen): %v", err)
	}
	defer closeEmbeddedDoltStore(t, frozen)

	beforeHead, beforeLog := head(), logCount()

	t.Run("frozen_get_ready_work", func(t *testing.T) {
		ready, err := frozen.GetReadyWork(ctx, types.WorkFilter{})
		if err != nil {
			t.Fatalf("GetReadyWork (frozen): %v", err)
		}
		for _, w := range ready {
			if w.ID == expired.ID {
				t.Errorf("frozen GetReadyWork surfaced the expired defer %s", expired.ID)
			}
		}
		assertAsleep("frozen GetReadyWork", beforeHead, beforeLog)
	})

	t.Run("frozen_get_ready_work_with_counts", func(t *testing.T) {
		ready, err := frozen.GetReadyWorkWithCounts(ctx, types.WorkFilter{})
		if err != nil {
			t.Fatalf("GetReadyWorkWithCounts (frozen): %v", err)
		}
		for _, w := range ready {
			if w.ID == expired.ID {
				t.Errorf("frozen GetReadyWorkWithCounts surfaced the expired defer %s", expired.ID)
			}
		}
		assertAsleep("frozen GetReadyWorkWithCounts", beforeHead, beforeLog)
	})

	t.Run("frozen_get_ready_work_with_counts_and_total", func(t *testing.T) {
		ready, total, err := frozen.GetReadyWorkWithCountsAndTotal(ctx, types.WorkFilter{})
		if err != nil {
			t.Fatalf("GetReadyWorkWithCountsAndTotal (frozen): %v", err)
		}
		for _, w := range ready {
			if w.ID == expired.ID {
				t.Errorf("frozen GetReadyWorkWithCountsAndTotal surfaced the expired defer %s", expired.ID)
			}
		}
		if total != 0 {
			t.Errorf("frozen total = %d, want 0 (every issue in the fixture is deferred)", total)
		}
		assertAsleep("frozen GetReadyWorkWithCountsAndTotal", beforeHead, beforeLog)
	})

	t.Run("frozen_count_ready_work", func(t *testing.T) {
		n, err := frozen.CountReadyWork(ctx, types.WorkFilter{})
		if err != nil {
			t.Fatalf("CountReadyWork (frozen): %v", err)
		}
		if n != 0 {
			t.Errorf("frozen CountReadyWork = %d, want 0 (every issue in the fixture is deferred)", n)
		}
		assertAsleep("frozen CountReadyWork", beforeHead, beforeLog)
	})

	t.Run("frozen_advisory_sweep", func(t *testing.T) {
		frozen.WakeExpiredDefersAdvisory(ctx)
		assertAsleep("frozen advisory sweep", beforeHead, beforeLog)
	})

	// The freeze only postpones the sweep: an independently reopened unfrozen
	// store performs the normal wake. The frozen reference above is closed
	// first — the cache would otherwise keep serving it.
	closeEmbeddedDoltStore(t, frozen)
	thawed, err := embeddeddolt.OpenForReadOnlyCommand(ctx, beadsDir, te.database, "main", false)
	if err != nil {
		t.Fatalf("OpenForReadOnlyCommand (unfrozen): %v", err)
	}
	defer closeEmbeddedDoltStore(t, thawed)

	t.Run("reopened_unfrozen_store_wakes", func(t *testing.T) {
		ready, err := thawed.GetReadyWork(ctx, types.WorkFilter{})
		if err != nil {
			t.Fatalf("GetReadyWork (unfrozen): %v", err)
		}
		var listed bool
		for _, w := range ready {
			if w.ID == expired.ID {
				listed = true
			}
		}
		if !listed {
			t.Errorf("unfrozen GetReadyWork did not surface the expired defer %s", expired.ID)
		}
		var status string
		var deferUntil *string
		te.queryScalar(t, ctx, "SELECT status, defer_until FROM issues WHERE id = ?", []any{expired.ID}, &status, &deferUntil)
		if status != "open" || deferUntil != nil {
			t.Errorf("unfrozen wake left %s at status = %q, defer_until = %v; want open with the date cleared", expired.ID, status, deferUntil)
		}
		var wispStatus string
		te.queryScalar(t, ctx, "SELECT status FROM wisps WHERE id = ?", []any{wisp.ID}, &wispStatus)
		if wispStatus != "open" {
			t.Errorf("unfrozen wake left wisp %s at status = %q, want open", wisp.ID, wispStatus)
		}
		for _, row := range []struct{ id, want string }{{dateless.ID, "deferred"}, {future.ID, "deferred"}} {
			var s string
			te.queryScalar(t, ctx, "SELECT status FROM issues WHERE id = ?", []any{row.id}, &s)
			if s != row.want {
				t.Errorf("unfrozen wake changed %s to status = %q, want %s", row.id, s, row.want)
			}
		}
		if n := wakeCommits(); n != 1 {
			t.Errorf("unfrozen sweep minted %d defer-wake commit(s), want exactly 1", n)
		}
		if h := head(); h == beforeHead {
			t.Error("unfrozen sweep did not move HEAD; want the one defer-wake commit")
		}
	})
}
