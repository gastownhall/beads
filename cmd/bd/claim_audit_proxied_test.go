//go:build cgo

package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestProxiedServerClaimPathsJournalSidecar is
// TestEmbeddedClaimPathsJournalSidecar through --proxied-server (GH#6146):
// the client workspace's sidecar gets the same rows, and a plain explicit status/priority update journals as it does directly.
func TestProxiedServerClaimPathsJournalSidecar(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	run := func(t *testing.T, p proxiedProject, args ...string) string {
		t.Helper()
		stdout, stderr, err := bdProxiedRunEnv(t, bd, p.dir, []string{"BD_AUDIT_ENABLED=1", "BEADS_ACTOR=alice"}, args...)
		if err != nil {
			t.Fatalf("bd %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout, stderr)
		}
		return stdout
	}

	t.Run("update_explicit_status_priority", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pe")
		x := bdProxiedCreate(t, bd, p.dir, "Explicit update", "--priority", "2")

		run(t, p, "update", x.ID, "--status", "blocked", "--priority", "0")
		rows := sidecarEntriesFor(readSidecarEntries(t, p.beadsDir), x.ID)
		if len(rows) != 2 {
			t.Fatalf("proxied explicit update journaled %d rows for %s, want 2: %+v", len(rows), x.ID, rows)
		}
		requireFieldChange(t, rows[0], "alice", "status", "open", "blocked")
		requireFieldChange(t, rows[1], "alice", "priority", "2", "0")
	})

	t.Run("update_claim", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pu")
		x := bdProxiedCreate(t, bd, p.dir, "Claimed issue")

		run(t, p, "update", x.ID, "--claim")
		rows := sidecarEntriesFor(readSidecarEntries(t, p.beadsDir), x.ID)
		if len(rows) != 2 {
			t.Fatalf("proxied bd update --claim journaled %d rows for %s, want 2: %+v", len(rows), x.ID, rows)
		}
		requireFieldChange(t, rows[0], "alice", "status", "open", "in_progress")
		requireFieldChange(t, rows[1], "alice", "assignee", "", "alice")

		run(t, p, "update", x.ID, "--claim")
		if rows := sidecarEntriesFor(readSidecarEntries(t, p.beadsDir), x.ID); len(rows) != 2 {
			t.Fatalf("idempotent proxied re-claim journaled more rows: %+v", rows)
		}
	})

	t.Run("update_claim_with_status", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "ps")
		x := bdProxiedCreate(t, bd, p.dir, "Claimed and blocked")

		run(t, p, "update", x.ID, "--claim", "--status", "blocked")
		rows := sidecarEntriesFor(readSidecarEntries(t, p.beadsDir), x.ID)
		if len(rows) != 2 {
			t.Fatalf("proxied bd update --claim --status blocked journaled %d rows for %s, want 2: %+v", len(rows), x.ID, rows)
		}
		requireFieldChange(t, rows[0], "alice", "status", "open", "blocked")
		requireFieldChange(t, rows[1], "alice", "assignee", "", "alice")
	})

	readyClaim := func(t *testing.T, p proxiedProject) *types.IssueWithCounts {
		t.Helper()
		out := run(t, p, "ready", "--claim", "--json")
		var claimed []*types.IssueWithCounts
		if err := json.Unmarshal([]byte(out[strings.Index(out, "["):]), &claimed); err != nil {
			t.Fatalf("parse bd ready --claim --json: %v\n%s", err, out)
		}
		if len(claimed) != 1 {
			t.Fatalf("bd ready --claim claimed %d issues, want 1: %s", len(claimed), out)
		}
		return claimed[0]
	}

	t.Run("ready_claim_open", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pr")
		x := bdProxiedCreate(t, bd, p.dir, "Ready open issue")

		claimed := readyClaim(t, p)
		if claimed.ID != x.ID {
			t.Fatalf("bd ready --claim claimed %s, want %s", claimed.ID, x.ID)
		}
		rows := sidecarEntriesFor(readSidecarEntries(t, p.beadsDir), x.ID)
		if len(rows) != 1 {
			t.Fatalf("proxied bd ready --claim journaled %d rows for %s, want 1 claimed row: %+v", len(rows), x.ID, rows)
		}
		requireClaimed(t, rows[0], "alice", claimed.Issue)

		if out := run(t, p, "ready", "--claim", "--json"); strings.TrimSpace(out) != "[]" {
			t.Fatalf("second bd ready --claim = %q, want []", out)
		}
		if all := readSidecarEntries(t, p.beadsDir); len(all) != 1 {
			t.Fatalf("empty proxied bd ready --claim journaled more rows: %+v", all)
		}
	})

	t.Run("ready_claim_custom_active", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pa")
		run(t, p, "config", "set", "status.custom", "triaging:active")
		x := bdProxiedCreate(t, bd, p.dir, "Ready triaging issue")
		run(t, p, "update", x.ID, "--status", "triaging")
		before := len(sidecarEntriesFor(readSidecarEntries(t, p.beadsDir), x.ID))

		claimed := readyClaim(t, p)
		if claimed.ID != x.ID {
			t.Fatalf("bd ready --claim claimed %s, want %s", claimed.ID, x.ID)
		}
		rows := sidecarEntriesFor(readSidecarEntries(t, p.beadsDir), x.ID)[before:]
		if len(rows) != 1 {
			t.Fatalf("proxied bd ready --claim journaled %d rows for %s, want 1 claimed row: %+v", len(rows), x.ID, rows)
		}
		requireClaimed(t, rows[0], "alice", claimed.Issue)
	})

	t.Run("close_claim_next", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pc")
		closed := bdProxiedCreate(t, bd, p.dir, "Closed first", "--priority", "0")
		next := bdProxiedCreate(t, bd, p.dir, "Claimed next", "--priority", "1")

		out := run(t, p, "close", closed.ID, "--claim-next", "--json")
		var res struct {
			Claimed *types.Issue `json:"claimed"`
		}
		if err := json.Unmarshal([]byte(out[strings.Index(out, "{"):]), &res); err != nil {
			t.Fatalf("parse bd close --claim-next --json: %v\n%s", err, out)
		}
		if res.Claimed == nil || res.Claimed.ID != next.ID {
			t.Fatalf("bd close --claim-next claimed %+v, want %s", res.Claimed, next.ID)
		}
		rows := readSidecarEntries(t, p.beadsDir)
		if len(rows) != 2 {
			t.Fatalf("proxied bd close --claim-next journaled %d rows, want the close then the claim: %+v", len(rows), rows)
		}
		if rows[0].IssueID != closed.ID || rows[0].Kind != "field_change" || rows[0].Extra["field"] != "status" || rows[0].Extra["new_value"] != "closed" {
			t.Fatalf("first row %+v: want %s's close field_change", rows[0], closed.ID)
		}
		requireClaimed(t, rows[1], "alice", res.Claimed)
	})
}
