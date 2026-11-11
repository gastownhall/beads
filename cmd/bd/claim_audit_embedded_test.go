//go:build cgo

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// sidecarEntry is one parsed .beads/interactions.jsonl row, every kind kept.
type sidecarEntry struct {
	Kind    string         `json:"kind"`
	IssueID string         `json:"issue_id"`
	Actor   string         `json:"actor"`
	Reason  string         `json:"reason"`
	Extra   map[string]any `json:"extra"`
}

// readSidecarEntries parses every row of beadsDir's interactions.jsonl in
// file order; a missing file reads as no rows.
func readSidecarEntries(t *testing.T, beadsDir string) []sidecarEntry {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(beadsDir, "interactions.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read interactions.jsonl: %v", err)
	}
	var entries []sidecarEntry
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e sidecarEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("bad interactions.jsonl line %q: %v", line, err)
		}
		entries = append(entries, e)
	}
	return entries
}

// sidecarEntriesFor keeps issueID's rows, in file order.
func sidecarEntriesFor(entries []sidecarEntry, issueID string) []sidecarEntry {
	var out []sidecarEntry
	for _, e := range entries {
		if e.IssueID == issueID {
			out = append(out, e)
		}
	}
	return out
}

// requireFieldChange fails unless e is a reason-less field_change row by actor
// moving field from oldValue to newValue.
func requireFieldChange(t *testing.T, e sidecarEntry, actor, field, oldValue, newValue string) {
	t.Helper()
	if e.Kind != "field_change" || e.Actor != actor || e.Reason != "" {
		t.Fatalf("row %+v: want a reason-less field_change by %q", e, actor)
	}
	if e.Extra["field"] != field || e.Extra["old_value"] != oldValue || e.Extra["new_value"] != newValue {
		t.Fatalf("row %+v: want %s %q -> %q", e, field, oldValue, newValue)
	}
	if _, ok := e.Extra["reason"]; ok {
		t.Fatalf("row %+v: want no reason", e)
	}
}

// requireClaimed fails unless e is a claimed row by actor carrying exactly
// the returned status and assignee, and no prior values.
func requireClaimed(t *testing.T, e sidecarEntry, actor string, claimed *types.Issue) {
	t.Helper()
	if e.Kind != "claimed" || e.IssueID != claimed.ID || e.Actor != actor || e.Reason != "" {
		t.Fatalf("row %+v: want a claimed row for %s by %q", e, claimed.ID, actor)
	}
	want := map[string]any{"status": string(claimed.Status), "assignee": claimed.Assignee}
	if !reflect.DeepEqual(e.Extra, want) {
		t.Fatalf("claimed row extra = %v, want exactly %v", e.Extra, want)
	}
}

// bdAuditRun runs bd in dir as actor alice with the sidecar enabled and
// returns stdout.
func bdAuditRun(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bd, args...)
	cmd.Dir = dir
	cmd.Env = append(bdEnv(dir), "BD_AUDIT_ENABLED=1", "BEADS_ACTOR=alice")
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

// TestEmbeddedClaimPathsJournalSidecar: GH#6146 — the CLI claim paths write
// the opt-in interactions.jsonl sidecar. `bd update --claim` journals the
// status and assignee field_change rows the equivalent explicit update would;
// `bd ready --claim` and `bd close --claim-next`, which have no pre-image,
// journal one post-state-only claimed row.
func TestEmbeddedClaimPathsJournalSidecar(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)

	t.Run("update_claim", func(t *testing.T) {
		t.Parallel()
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "uc")
		x := bdCreate(t, bd, dir, "Claimed issue")

		bdAuditRun(t, bd, dir, "update", x.ID, "--claim")
		rows := sidecarEntriesFor(readSidecarEntries(t, beadsDir), x.ID)
		if len(rows) != 2 {
			t.Fatalf("bd update --claim journaled %d rows for %s, want 2: %+v", len(rows), x.ID, rows)
		}
		requireFieldChange(t, rows[0], "alice", "status", "open", "in_progress")
		requireFieldChange(t, rows[1], "alice", "assignee", "", "alice")

		// Re-claiming an issue alice already holds changes nothing, so it
		// journals nothing.
		bdAuditRun(t, bd, dir, "update", x.ID, "--claim")
		if rows := sidecarEntriesFor(readSidecarEntries(t, beadsDir), x.ID); len(rows) != 2 {
			t.Fatalf("idempotent re-claim journaled more rows: %+v", rows)
		}
	})

	t.Run("update_claim_with_status", func(t *testing.T) {
		t.Parallel()
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "us")
		x := bdCreate(t, bd, dir, "Claimed and blocked")

		bdAuditRun(t, bd, dir, "update", x.ID, "--claim", "--status", "blocked")
		rows := sidecarEntriesFor(readSidecarEntries(t, beadsDir), x.ID)
		if len(rows) != 2 {
			t.Fatalf("bd update --claim --status blocked journaled %d rows for %s, want 2: %+v", len(rows), x.ID, rows)
		}
		requireFieldChange(t, rows[0], "alice", "status", "open", "blocked")
		requireFieldChange(t, rows[1], "alice", "assignee", "", "alice")
	})

	// readyClaim runs `bd ready --claim --json` and returns the one issue it
	// claimed.
	readyClaim := func(t *testing.T, dir string) *types.IssueWithCounts {
		t.Helper()
		out := bdAuditRun(t, bd, dir, "ready", "--claim", "--json")
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
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "ro")
		x := bdCreate(t, bd, dir, "Ready open issue")

		claimed := readyClaim(t, dir)
		if claimed.ID != x.ID {
			t.Fatalf("bd ready --claim claimed %s, want %s", claimed.ID, x.ID)
		}
		rows := sidecarEntriesFor(readSidecarEntries(t, beadsDir), x.ID)
		if len(rows) != 1 {
			t.Fatalf("bd ready --claim journaled %d rows for %s, want 1 claimed row: %+v", len(rows), x.ID, rows)
		}
		requireClaimed(t, rows[0], "alice", claimed.Issue)

		// An empty ready front claims nothing and journals nothing.
		out := bdAuditRun(t, bd, dir, "ready", "--claim", "--json")
		if strings.TrimSpace(out) != "[]" {
			t.Fatalf("second bd ready --claim = %q, want []", out)
		}
		if all := readSidecarEntries(t, beadsDir); len(all) != 1 {
			t.Fatalf("empty bd ready --claim journaled more rows: %+v", all)
		}
	})

	t.Run("ready_claim_custom_active", func(t *testing.T) {
		t.Parallel()
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "ra")
		bdConfig(t, bd, dir, "set", "status.custom", "triaging:active")
		x := bdCreate(t, bd, dir, "Ready triaging issue")
		bdAuditRun(t, bd, dir, "update", x.ID, "--status", "triaging")
		before := len(sidecarEntriesFor(readSidecarEntries(t, beadsDir), x.ID))

		claimed := readyClaim(t, dir)
		if claimed.ID != x.ID {
			t.Fatalf("bd ready --claim claimed %s, want %s", claimed.ID, x.ID)
		}
		rows := sidecarEntriesFor(readSidecarEntries(t, beadsDir), x.ID)[before:]
		if len(rows) != 1 {
			t.Fatalf("bd ready --claim journaled %d rows for %s, want 1 claimed row: %+v", len(rows), x.ID, rows)
		}
		requireClaimed(t, rows[0], "alice", claimed.Issue)
	})

	t.Run("close_claim_next", func(t *testing.T) {
		t.Parallel()
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "cn")
		p := bdCreate(t, bd, dir, "Closed first", "--priority", "0")
		q := bdCreate(t, bd, dir, "Claimed next", "--priority", "1")

		out := bdAuditRun(t, bd, dir, "close", p.ID, "--claim-next", "--json")
		var res struct {
			Claimed *types.Issue `json:"claimed"`
		}
		if err := json.Unmarshal([]byte(out[strings.Index(out, "{"):]), &res); err != nil {
			t.Fatalf("parse bd close --claim-next --json: %v\n%s", err, out)
		}
		if res.Claimed == nil || res.Claimed.ID != q.ID {
			t.Fatalf("bd close --claim-next claimed %+v, want %s", res.Claimed, q.ID)
		}
		rows := readSidecarEntries(t, beadsDir)
		if len(rows) != 2 {
			t.Fatalf("bd close --claim-next journaled %d rows, want P's close then Q's claim: %+v", len(rows), rows)
		}
		if rows[0].IssueID != p.ID || rows[0].Kind != "field_change" || rows[0].Extra["field"] != "status" || rows[0].Extra["new_value"] != "closed" {
			t.Fatalf("first row %+v: want %s's close field_change", rows[0], p.ID)
		}
		requireClaimed(t, rows[1], "alice", res.Claimed)
	})

	t.Run("close_without_claim_next", func(t *testing.T) {
		t.Parallel()
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "co")
		p := bdCreate(t, bd, dir, "Closed alone")
		other := bdCreate(t, bd, dir, "Ready issue left alone")

		// A close that claims nothing journals only its own close record:
		// no claimed row, and nothing for the untouched ready issue.
		bdAuditRun(t, bd, dir, "close", p.ID)
		rows := readSidecarEntries(t, beadsDir)
		if len(rows) != 1 {
			t.Fatalf("plain bd close journaled %d rows, want only %s's close: %+v", len(rows), p.ID, rows)
		}
		if rows[0].IssueID != p.ID || rows[0].Kind != "field_change" || rows[0].Extra["field"] != "status" ||
			rows[0].Extra["old_value"] != "open" || rows[0].Extra["new_value"] != "closed" {
			t.Fatalf("row %+v: want %s's open -> closed field_change", rows[0], p.ID)
		}
		if got := sidecarEntriesFor(rows, other.ID); len(got) != 0 {
			t.Fatalf("plain bd close journaled rows for untouched %s: %+v", other.ID, got)
		}
	})
}
