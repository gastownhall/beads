//go:build cgo

package main

import (
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
	requireSharedProxiedServer(t)
	t.Parallel()

	bd := buildEmbeddedBD(t)
	p := newServerModeProject(t, bd, "dws")

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
}
