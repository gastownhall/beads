//go:build cgo

package main

import (
	"context"
	"database/sql"
	"os"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/migration"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/types"
)

// TestServerModeDeferAutoWake documents the same defer/wake contract as
// TestEmbeddedDeferAutoWake (defer_wake_embedded_test.go) and
// TestProxiedServerDeferAutoWake (defer_wake_proxied_integration_test.go), but
// against a real dolt sql-server (be-vbhpf): a DATED defer is a snooze, and
// once defer_until passes, the next ready-front read on `bd ready` or
// `bd list --ready` must return the issue to open. In server mode this sweep
// (DoltStore.wakeExpiredDefers) is silently skipped today because the store is
// opened read-only purely from command classification (bd ready/bd list are
// on the read-only allowlist for the CURRENT project, GH#804) — indistinguishable,
// before this fix, from a strict --readonly or foreign-project open that must
// never sweep.
//
// The *_under_migration_freeze subtests pin the freeze half (bd-6zf0q5): a
// freeze lets these reads through, but the sweep is a write, so a frozen read
// must answer without waking anything or adding a Dolt commit. The freeze is
// passed to one invocation through BD_MIGRATION_FREEZE_FILE, so every other
// bd call here runs unfrozen.
func TestServerModeDeferAutoWake(t *testing.T) {
	port := requireSharedProxiedServer(t)
	t.Parallel()

	bd := buildEmbeddedBD(t)
	p := newServerModeProject(t, bd, "dws")
	db := openServerModeDB(t, port, p.database)
	// The marker lives outside the project and is reached only through the
	// env override, so no other bd call in this test sees it.
	marker := writeFreezeMarker(t, t.TempDir(), "migrator", "dolt v2 migration")
	frozenEnv := []string{migration.EnvFreezeFile + "=" + marker}

	t.Run("expired_dated_defer_wakes_on_ready", func(t *testing.T) {
		issue := bdCreate(t, bd, p.dir, "Expired snooze (server mode)", "--type", "task")
		bdDefer(t, bd, p.dir, issue.ID, "--until", "2020-01-01")
		status, _ := showDeferState(t, bd, p.dir, issue.ID)
		if status != "deferred" {
			t.Fatalf("precondition: expected status=deferred, got %q", status)
		}

		ids := wakeReadyIDs(t, bd, p.dir)
		if !ids[issue.ID] {
			t.Errorf("expected %s in bd ready after its defer date passed (server mode)", issue.ID)
		}
		status, deferUntil := showDeferState(t, bd, p.dir, issue.ID)
		if status != "open" {
			t.Errorf("expected status=open after wake, got %q", status)
		}
		if deferUntil != nil {
			t.Errorf("expected defer_until cleared after wake (undefer's shape), got %v", deferUntil)
		}
	})

	t.Run("expired_dated_defer_wakes_on_list_ready", func(t *testing.T) {
		issue := bdCreate(t, bd, p.dir, "Expired snooze via list (server mode)", "--type", "task")
		bdDefer(t, bd, p.dir, issue.ID, "--until", "2020-01-01")

		issues := bdListJSON(t, bd, p.dir, "--ready")
		var found *types.IssueWithCounts
		for _, iss := range issues {
			if iss.ID == issue.ID {
				found = iss
				break
			}
		}
		if found == nil {
			t.Fatalf("expected %s in bd list --ready after its defer date passed (server mode)", issue.ID)
		}
		if found.Status != types.StatusOpen {
			t.Errorf("expected status=open in bd list --ready, got %q", found.Status)
		}
		if found.DeferUntil != nil {
			t.Errorf("expected defer_until cleared after wake, got %v", found.DeferUntil)
		}
	})

	t.Run("dateless_defer_never_wakes", func(t *testing.T) {
		issue := bdCreate(t, bd, p.dir, "Indefinite icebox (server mode)", "--type", "task")
		bdDefer(t, bd, p.dir, issue.ID)

		ids := wakeReadyIDs(t, bd, p.dir)
		if ids[issue.ID] {
			t.Errorf("dateless defer %s must not appear in bd ready", issue.ID)
		}
		status, _ := showDeferState(t, bd, p.dir, issue.ID)
		if status != "deferred" {
			t.Errorf("dateless defer must stay deferred, got %q", status)
		}
	})

	t.Run("future_dated_defer_stays_hidden", func(t *testing.T) {
		issue := bdCreate(t, bd, p.dir, "Future snooze (server mode)", "--type", "task")
		bdDefer(t, bd, p.dir, issue.ID, "--until", "+8760h")

		ids := wakeReadyIDs(t, bd, p.dir)
		if ids[issue.ID] {
			t.Errorf("future defer %s must not appear in bd ready", issue.ID)
		}
		status, _ := showDeferState(t, bd, p.dir, issue.ID)
		if status != "deferred" {
			t.Errorf("future defer must stay deferred, got %q", status)
		}
	})

	t.Run("expired_dated_defer_stays_hidden_under_strict_readonly", func(t *testing.T) {
		issue := bdCreate(t, bd, p.dir, "Expired snooze under strict --readonly (server mode)", "--type", "task")
		bdDefer(t, bd, p.dir, issue.ID, "--until", "2020-01-01")
		status, _ := showDeferState(t, bd, p.dir, issue.ID)
		if status != "deferred" {
			t.Fatalf("precondition: expected status=deferred, got %q", status)
		}

		ids := wakeReadyIDs(t, bd, p.dir, "--readonly")
		if ids[issue.ID] {
			t.Errorf("expired defer %s must not appear in bd --readonly ready — strict --readonly must never sweep", issue.ID)
		}
		status, _ = showDeferState(t, bd, p.dir, issue.ID)
		if status != "deferred" {
			t.Errorf("expired defer must stay deferred under strict --readonly, got %q", status)
		}
	})

	t.Run("expired_dated_defer_stays_deferred_on_list_ready_under_migration_freeze", func(t *testing.T) {
		control := bdCreate(t, bd, p.dir, "Open control under a freeze (server mode, list)", "--type", "task")
		issue := bdCreate(t, bd, p.dir, "Expired snooze under a freeze (server mode, list)", "--type", "task")
		bdDefer(t, bd, p.dir, issue.ID, "--until", "2020-01-01")
		status, deferUntil := showDeferState(t, bd, p.dir, issue.ID)
		if status != "deferred" || deferUntil == nil {
			t.Fatalf("precondition: expected a dated deferred issue, got status=%q defer_until=%v", status, deferUntil)
		}

		commitsBefore := doltLogCount(t, db)
		issues := bdListJSONWithEnv(t, bd, p.dir, frozenEnv, "--ready")
		commitsAfter := doltLogCount(t, db)

		listed := map[string]bool{}
		for _, iss := range issues {
			listed[iss.ID] = true
		}
		if !listed[control.ID] {
			t.Errorf("open control %s missing from frozen bd list --ready: the read must still answer", control.ID)
		}
		if listed[issue.ID] {
			t.Errorf("expired defer %s appeared in frozen bd list --ready: the wake sweep wrote past the freeze", issue.ID)
		}
		assertDeferUnchanged(t, bd, p.dir, issue.ID, deferUntil)
		if commitsAfter != commitsBefore {
			t.Errorf("frozen bd list --ready added %d Dolt commit(s); a frozen read must not commit", commitsAfter-commitsBefore)
		}
	})

	// A migration freeze withholds ClassifiedRead (classifiedReadOpen's frozen
	// term at the main.go call site), so the sweep — a write — must not run
	// while the marker is up. The predicate is unit-tested on its own; this
	// pins the wiring: dropping the frozen term from the call site reddens
	// this case.
	t.Run("frozen_ready_does_not_wake", func(t *testing.T) {
		control := bdCreate(t, bd, p.dir, "Open control under migration freeze (server mode, ready)", "--type", "task")
		issue := bdCreate(t, bd, p.dir, "Expired snooze under migration freeze (server mode)", "--type", "task")
		bdDefer(t, bd, p.dir, issue.ID, "--until", "2020-01-01")
		status, deferUntil := showDeferState(t, bd, p.dir, issue.ID)
		if status != "deferred" || deferUntil == nil {
			t.Fatalf("precondition: expected a dated deferred issue, got status=%q defer_until=%v", status, deferUntil)
		}

		commitsBefore := doltLogCount(t, db)
		ids := wakeReadyIDsWithEnv(t, bd, p.dir, frozenEnv)
		commitsAfter := doltLogCount(t, db)

		if !ids[control.ID] {
			t.Errorf("open control %s missing from frozen bd ready: the read must still answer", control.ID)
		}
		if ids[issue.ID] {
			t.Errorf("expired defer %s must not appear in bd ready under a migration freeze — a frozen read must never sweep", issue.ID)
		}
		assertDeferUnchanged(t, bd, p.dir, issue.ID, deferUntil)
		if commitsAfter != commitsBefore {
			t.Errorf("frozen bd ready added %d Dolt commit(s); a frozen read must not commit", commitsAfter-commitsBefore)
		}
		if t.Failed() {
			t.FailNow()
		}

		// Thaw: the same project now wakes the issue, so the frozen
		// assertions above are not vacuous. This removes the shared marker,
		// so every frozen subtest must run before this one.
		if err := os.Remove(marker); err != nil {
			t.Fatalf("removing freeze marker: %v", err)
		}
		if !wakeReadyIDs(t, bd, p.dir)[issue.ID] {
			t.Errorf("expected %s in bd ready once the freeze lifted", issue.ID)
		}
		status, deferUntil = showDeferState(t, bd, p.dir, issue.ID)
		if status != "open" {
			t.Errorf("expected status=open after wake once the freeze lifted, got %q", status)
		}
		if deferUntil != nil {
			t.Errorf("expected defer_until cleared after wake once the freeze lifted, got %v", deferUntil)
		}
	})
}

// openServerModeDB opens a test-only connection to a server-mode project's
// database on the shared test server, closed at test cleanup.
func openServerModeDB(t *testing.T, port int, database string) *sql.DB {
	t.Helper()
	db, err := sql.Open("mysql", doltutil.ServerDSN{Host: "127.0.0.1", Port: port, User: "root", Database: database}.String())
	if err != nil {
		t.Fatalf("connecting to the test server: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// doltLogCount returns the number of commits in the database's history.
func doltLogCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM dolt_log").Scan(&n); err != nil {
		t.Fatalf("counting dolt_log: %v", err)
	}
	return n
}

// assertDeferUnchanged checks that an issue is still deferred with the defer
// date it had before.
func assertDeferUnchanged(t *testing.T, bd, dir, id string, wantDeferUntil interface{}) {
	t.Helper()
	status, deferUntil := showDeferState(t, bd, dir, id)
	if status != "deferred" {
		t.Errorf("expired defer %s must stay deferred under a migration freeze, got status=%q", id, status)
	}
	if !reflect.DeepEqual(deferUntil, wantDeferUntil) {
		t.Errorf("defer_until of %s changed under a migration freeze: was %v, now %v", id, wantDeferUntil, deferUntil)
	}
}
