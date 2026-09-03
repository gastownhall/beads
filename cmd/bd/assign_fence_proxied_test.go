//go:build cgo

package main

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestProxiedServerReassignFence proves the bd-98s5c live-claim reassign
// fence holds on the proxied-server path — the topology where cross-actor
// collisions actually happen, since every shared-dolt-server clone writes
// through it. A silent steamroll refuses with exit 1 (policy refusal, never
// ExitGuardMismatch), --force overrides, and the idempotent re-assert stays a
// success for retry/replay safety.
func TestProxiedServerReassignFence(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	t.Run("update_refuses_then_force_overrides", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rfa")
		issue := bdProxiedCreate(t, bd, p.dir, "Held via proxy")
		bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--actor", "holder", "--assignee", "holder", "--status", "in_progress")

		out, code := bdProxiedUpdateFailCode(t, bd, p.dir, issue.ID, "--actor", "thief", "--assignee", "thief")
		if code != 1 {
			t.Errorf("fence refusal exit code = %d, want 1 (policy refusal, not %d/guard-mismatch)\n%s", code, ExitGuardMismatch, out)
		}
		for _, frag := range []string{"held by", "holder", "--force"} {
			if !strings.Contains(out, frag) {
				t.Errorf("refusal should contain %q, got:\n%s", frag, out)
			}
		}
		got := bdProxiedShow(t, bd, p.dir, issue.ID)
		if got.Assignee != "holder" || got.Status != types.StatusInProgress {
			t.Errorf("refused reassign clobbered the row: assignee=%q status=%s, want holder/in_progress", got.Assignee, got.Status)
		}

		// Idempotent re-assert of the current holder is a success (replay
		// safety on the retrying proxied path).
		bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--actor", "replayer", "--assignee", "holder")

		bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--actor", "thief", "--assignee", "thief", "--force")
		if got := bdProxiedShow(t, bd, p.dir, issue.ID); got.Assignee != "thief" {
			t.Errorf("--force reassign did not apply: assignee=%q, want thief", got.Assignee)
		}
	})

	t.Run("assign_refuses_then_force_overrides", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rfb")
		issue := bdProxiedCreate(t, bd, p.dir, "Assigned via proxy")
		bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--actor", "holder", "--assignee", "holder", "--status", "in_progress")

		out, err := bdProxiedRun(t, bd, p.dir, "assign", issue.ID, "thief", "--actor", "thief")
		if err == nil {
			t.Fatalf("proxied assign over a live foreign claim should fail, got:\n%s", out)
		}
		if !strings.Contains(string(out), "holder") {
			t.Errorf("assign refusal should name the holder, got:\n%s", out)
		}
		if got := bdProxiedShow(t, bd, p.dir, issue.ID); got.Assignee != "holder" {
			t.Errorf("refused assign clobbered the row: assignee=%q, want holder", got.Assignee)
		}

		if out, err := bdProxiedRun(t, bd, p.dir, "assign", issue.ID, "thief", "--actor", "thief", "--force"); err != nil {
			t.Fatalf("assign --force failed: %v\n%s", err, out)
		}
		if got := bdProxiedShow(t, bd, p.dir, issue.ID); got.Assignee != "thief" {
			t.Errorf("assign --force did not apply: assignee=%q, want thief", got.Assignee)
		}
	})

	t.Run("open_bead_dispatch_stays_frictionless", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rfc")
		issue := bdProxiedCreate(t, bd, p.dir, "Open dispatch via proxy", "--assignee", "alice")
		bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--actor", "dispatcher", "--assignee", "bob")
		if got := bdProxiedShow(t, bd, p.dir, issue.ID); got.Assignee != "bob" {
			t.Errorf("open-bead reassign should not be fenced: assignee=%q, want bob", got.Assignee)
		}
	})
}
