//go:build cgo

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
// status and assignee field_change rows the equivalent explicit update would.
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
}
