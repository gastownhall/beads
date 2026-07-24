//go:build cgo

package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestProxiedServerDeferUndefer(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "dfr")

	showStatus := func(t *testing.T, id string) (string, interface{}) {
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
		return arr[0]["status"].(string), arr[0]["defer_until"]
	}

	t.Run("defer_then_undefer", func(t *testing.T) {
		a := bdProxiedCreate(t, bd, p.dir, "Defer target", "--type", "task")

		out, err := bdProxiedRun(t, bd, p.dir, "defer", a.ID, "--until", "tomorrow", "--reason", "waiting on X", "--json")
		if err != nil {
			t.Fatalf("defer: %v\n%s", err, out)
		}
		var deferred []map[string]interface{}
		if err := json.Unmarshal(out, &deferred); err != nil {
			t.Fatalf("unmarshal defer: %v\n%s", err, out)
		}
		if len(deferred) != 1 || deferred[0]["status"] != string(types.StatusDeferred) {
			t.Fatalf("expected 1 deferred issue, got %v", deferred)
		}
		if deferred[0]["defer_until"] == nil {
			t.Errorf("expected defer_until to be set")
		}

		status, deferUntil := showStatus(t, a.ID)
		if status != string(types.StatusDeferred) {
			t.Errorf("status = %s, want deferred", status)
		}
		if deferUntil == nil {
			t.Errorf("defer_until should be set after defer")
		}

		// Undefer restores to open and clears defer_until.
		if out, err := bdProxiedRun(t, bd, p.dir, "undefer", a.ID); err != nil {
			t.Fatalf("undefer: %v\n%s", err, out)
		}
		status, deferUntil = showStatus(t, a.ID)
		if status != string(types.StatusOpen) {
			t.Errorf("status = %s, want open after undefer", status)
		}
		if deferUntil != nil {
			t.Errorf("defer_until should be cleared after undefer, got %v", deferUntil)
		}
	})

	t.Run("deferred_excluded_from_ready", func(t *testing.T) {
		b := bdProxiedCreate(t, bd, p.dir, "Ready-then-deferred", "--type", "task", "--priority", "1")
		if out, err := bdProxiedRun(t, bd, p.dir, "defer", b.ID); err != nil {
			t.Fatalf("defer: %v\n%s", err, out)
		}
		ready := bdProxiedListJSON(t, bd, p, "--ready")
		for _, iss := range ready {
			if iss.ID == b.ID {
				t.Errorf("deferred issue %s should not appear in ready", b.ID)
			}
		}
	})

	t.Run("undefer_nondeferred_errors", func(t *testing.T) {
		c := bdProxiedCreate(t, bd, p.dir, "Open issue", "--type", "task")
		stdout, stderr, _ := bdProxiedRunBuffers(t, bd, p.dir, "undefer", c.ID)
		if !strings.Contains(stdout+stderr, "is not deferred") {
			t.Errorf("expected 'is not deferred' message, got stdout=%q stderr=%q", stdout, stderr)
		}
	})

	t.Run("defer_is_idempotent", func(t *testing.T) {
		d := bdProxiedCreate(t, bd, p.dir, "Idempotent defer", "--type", "task")
		if out, err := bdProxiedRun(t, bd, p.dir, "defer", d.ID); err != nil {
			t.Fatalf("first defer: %v\n%s", err, out)
		}
		// Second defer on an already-deferred issue must not error (ClientFoundRows fix).
		if out, err := bdProxiedRun(t, bd, p.dir, "defer", d.ID, "--json"); err != nil {
			t.Fatalf("second defer errored (no-op update regression): %v\n%s", err, out)
		}
	})

	t.Run("defer_multiple", func(t *testing.T) {
		e := bdProxiedCreate(t, bd, p.dir, "Defer multi 1", "--type", "task")
		f := bdProxiedCreate(t, bd, p.dir, "Defer multi 2", "--type", "task")

		out, err := bdProxiedRun(t, bd, p.dir, "defer", e.ID, f.ID)
		if err != nil {
			t.Fatalf("defer multiple: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), e.ID) || !strings.Contains(string(out), f.ID) {
			t.Errorf("expected both IDs in defer output, got:\n%s", out)
		}
		for _, id := range []string{e.ID, f.ID} {
			if status, _ := showStatus(t, id); status != string(types.StatusDeferred) {
				t.Errorf("status of %s = %s, want deferred", id, status)
			}
		}
	})

	t.Run("undefer_multiple", func(t *testing.T) {
		g := bdProxiedCreate(t, bd, p.dir, "Undefer multi 1", "--type", "task")
		h := bdProxiedCreate(t, bd, p.dir, "Undefer multi 2", "--type", "task")
		if out, err := bdProxiedRun(t, bd, p.dir, "defer", g.ID, h.ID); err != nil {
			t.Fatalf("defer: %v\n%s", err, out)
		}

		out, err := bdProxiedRun(t, bd, p.dir, "undefer", g.ID, h.ID)
		if err != nil {
			t.Fatalf("undefer multiple: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), g.ID) || !strings.Contains(string(out), h.ID) {
			t.Errorf("expected both IDs in undefer output, got:\n%s", out)
		}
		for _, id := range []string{g.ID, h.ID} {
			if status, _ := showStatus(t, id); status != string(types.StatusOpen) {
				t.Errorf("status of %s = %s, want open after undefer", id, status)
			}
		}
	})

	t.Run("defer_reason_appended_to_notes", func(t *testing.T) {
		i := bdProxiedCreate(t, bd, p.dir, "Defer with reason", "--type", "task")
		if out, err := bdProxiedRun(t, bd, p.dir, "defer", i.ID, "--reason", "waiting on API access"); err != nil {
			t.Fatalf("defer --reason: %v\n%s", err, out)
		}
		iss := bdProxiedShow(t, bd, p.dir, i.ID)
		if !strings.Contains(iss.Notes, "waiting on API access") {
			t.Errorf("expected defer reason appended to notes, got notes=%q", iss.Notes)
		}
	})
}
