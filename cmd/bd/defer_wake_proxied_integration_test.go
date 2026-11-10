//go:build cgo

package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestProxiedServerDeferAutoWake is the proxied-mode parity subtest for the
// defer auto-wake sweep: a DATED defer whose date has passed returns to open
// on the next ready read, a DATELESS defer never does. Same contract, other
// execution mode — the sweep must behave identically in both.
func TestProxiedServerDeferAutoWake(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "dwk")

	showState := func(t *testing.T, id string) (string, interface{}) {
		t.Helper()
		out, err := bdProxiedRun(t, bd, p.dir, "show", id, "--json")
		if err != nil {
			t.Fatalf("show %s: %v\n%s", id, err, out)
		}
		var arr []map[string]interface{}
		if err := json.Unmarshal(out, &arr); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		if len(arr) == 0 {
			t.Fatalf("show returned nothing for %s", id)
		}
		status, _ := arr[0]["status"].(string)
		return status, arr[0]["defer_until"]
	}

	readySet := func(t *testing.T) map[string]bool {
		t.Helper()
		out, err := bdProxiedRun(t, bd, p.dir, "ready", "--json", "--limit", "0")
		if err != nil {
			t.Fatalf("ready: %v\n%s", err, out)
		}
		s := strings.TrimSpace(string(out))
		start := strings.Index(s, "[")
		if start < 0 {
			return map[string]bool{}
		}
		var arr []map[string]interface{}
		if err := json.Unmarshal([]byte(s[start:]), &arr); err != nil {
			t.Fatalf("parse ready JSON: %v\n%s", err, s)
		}
		ids := map[string]bool{}
		for _, row := range arr {
			if id, ok := row["id"].(string); ok {
				ids[id] = true
			}
		}
		return ids
	}

	t.Run("expired_dated_defer_wakes_on_ready", func(t *testing.T) {
		a := bdProxiedCreate(t, bd, p.dir, "Expired snooze proxied", "--type", "task")
		if out, err := bdProxiedRun(t, bd, p.dir, "defer", a.ID, "--until", "2020-01-01"); err != nil {
			t.Fatalf("defer: %v\n%s", err, out)
		}
		status, _ := showState(t, a.ID)
		if status != string(types.StatusDeferred) {
			t.Fatalf("precondition: status = %s, want deferred", status)
		}

		ids := readySet(t)
		if !ids[a.ID] {
			t.Errorf("expected %s in bd ready after its defer date passed", a.ID)
		}
		status, deferUntil := showState(t, a.ID)
		if status != string(types.StatusOpen) {
			t.Errorf("status = %s, want open after wake", status)
		}
		if deferUntil != nil {
			t.Errorf("defer_until should be cleared after wake, got %v", deferUntil)
		}
	})

	t.Run("expired_dated_defer_wakes_wisp", func(t *testing.T) {
		// The uow stack's wisp seam: wisp tables are dolt_ignored, so a
		// wisp-only sweep mints no wake commit and must persist through the
		// ephemeral plain-COMMIT form instead — the path that once rolled
		// wisp wakes back, leaving expired wisp defers deferred forever in
		// proxied/serve mode while embedded mode woke them (parity break).
		id := bdProxiedCreateSilent(t, bd, p.dir, "Wisp snooze proxied", "--type", "task", "--ephemeral", "--wisp-type", "heartbeat")
		if out, err := bdProxiedRun(t, bd, p.dir, "defer", id, "--until", "2020-01-01"); err != nil {
			t.Fatalf("defer wisp: %v\n%s", err, out)
		}
		status, _ := showState(t, id)
		if status != string(types.StatusDeferred) {
			t.Fatalf("precondition: wisp status = %s, want deferred", status)
		}

		if out, err := bdProxiedRun(t, bd, p.dir, "ready", "--json"); err != nil {
			t.Fatalf("ready: %v\n%s", err, out)
		}

		status, deferUntil := showState(t, id)
		if status != string(types.StatusOpen) {
			t.Errorf("wisp status = %s, want open after wake (parity break vs embedded)", status)
		}
		if deferUntil != nil {
			t.Errorf("wisp defer_until should be cleared after wake, got %v", deferUntil)
		}
	})

	t.Run("dateless_defer_never_wakes", func(t *testing.T) {
		a := bdProxiedCreate(t, bd, p.dir, "Indefinite icebox proxied", "--type", "task")
		if out, err := bdProxiedRun(t, bd, p.dir, "defer", a.ID); err != nil {
			t.Fatalf("defer: %v\n%s", err, out)
		}

		ids := readySet(t)
		if ids[a.ID] {
			t.Errorf("dateless defer %s must not appear in bd ready", a.ID)
		}
		status, _ := showState(t, a.ID)
		if status != string(types.StatusDeferred) {
			t.Errorf("dateless defer must stay deferred, got %s", status)
		}
	})

	t.Run("future_dated_defer_stays_hidden", func(t *testing.T) {
		a := bdProxiedCreate(t, bd, p.dir, "Future snooze proxied", "--type", "task")
		if out, err := bdProxiedRun(t, bd, p.dir, "defer", a.ID, "--until", "+8760h"); err != nil {
			t.Fatalf("defer: %v\n%s", err, out)
		}

		ids := readySet(t)
		if ids[a.ID] {
			t.Errorf("future defer %s must not appear in bd ready", a.ID)
		}
		status, _ := showState(t, a.ID)
		if status != string(types.StatusDeferred) {
			t.Errorf("future defer must stay deferred, got %s", status)
		}
	})
}
