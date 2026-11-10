//go:build cgo

package main

import (
	"database/sql"
	"fmt"
	"testing"

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
func TestServerModeDeferAutoWake(t *testing.T) {
	port := requireSharedProxiedServer(t)
	t.Parallel()

	bd := buildEmbeddedBD(t)
	p := newServerModeProject(t, bd, "dws")
	markerDir := t.TempDir()

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

	// A MIGRATION-FREEZE holds the sweep off on a direct server too: the
	// classified read is no longer one that may write (#7634). The first
	// ready read after the thaw wakes what the frozen one left asleep.
	t.Run("frozen_ready_leaves_expired_defer_asleep_until_thaw", func(t *testing.T) {
		db, err := sql.Open("mysql", fmt.Sprintf("root:@tcp(127.0.0.1:%d)/%s?parseTime=true", port, p.database))
		if err != nil {
			t.Fatalf("open server db: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })

		issue := bdCreate(t, bd, p.dir, "Frozen snooze (server mode)", "--type", "task")
		bdDefer(t, bd, p.dir, issue.ID, "--until", "2020-01-01")
		before := proxiedDoltHead(t, db)

		ids := bdReadIDsWithEnv(t, bd, p.dir, deferWakeFreezeEnv(t, markerDir, true), "ready", "--json")
		if ids[issue.ID] {
			t.Errorf("frozen bd ready listed %s: the sweep woke it during a freeze (server mode)", issue.ID)
		}
		if after := proxiedDoltHead(t, db); after != before {
			t.Errorf("frozen bd ready moved HEAD %s -> %s (server mode)", before, after)
		}
		status, deferUntil := showDeferState(t, bd, p.dir, issue.ID)
		if status != "deferred" || deferUntil == nil {
			t.Fatalf("frozen bd ready changed %s to status=%q defer_until=%v, want deferred with its date", issue.ID, status, deferUntil)
		}

		ids = bdReadIDsWithEnv(t, bd, p.dir, deferWakeFreezeEnv(t, markerDir, false), "ready", "--json")
		if !ids[issue.ID] {
			t.Errorf("expected %s in bd ready after the thaw (server mode)", issue.ID)
		}
		status, deferUntil = showDeferState(t, bd, p.dir, issue.ID)
		if status != "open" || deferUntil != nil {
			t.Errorf("thawed bd ready left %s at status=%q defer_until=%v, want open with no date", issue.ID, status, deferUntil)
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
}
