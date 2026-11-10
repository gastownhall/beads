//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// claimAuditActor is the actor the claim-path sidecar tests run bd as.
const claimAuditActor = "alice"

// claimAuditRun runs bd with the interactions sidecar enabled and a fixed
// actor — the way a user who set audit.enabled=true works. Returns combined
// output for the caller to assert on when it cares.
func claimAuditRun(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bd, args...)
	cmd.Dir = dir
	cmd.Env = append(bdEnv(dir), "BD_AUDIT_ENABLED=1", "BD_ACTOR="+claimAuditActor)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// claimAuditEntry is one parsed interactions.jsonl row, shaped for the
// assertions the sidecar tests make. Extra decodes loosely so an unexpected
// key fails an exact-shape assertion instead of the decode.
type claimAuditEntry struct {
	Kind    string         `json:"kind"`
	IssueID string         `json:"issue_id"`
	Actor   string         `json:"actor"`
	Extra   map[string]any `json:"extra"`
}

// claimAuditEntries reads every row the sidecar has appended so far. An
// absent file reads as no entries: a disabled sidecar and a fresh workspace
// are both "nothing was journaled".
func claimAuditEntries(t *testing.T, beadsDir string) []claimAuditEntry {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(beadsDir, "interactions.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read interactions.jsonl: %v", err)
	}
	var entries []claimAuditEntry
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var e claimAuditEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("bad journal line %q: %v", line, err)
		}
		entries = append(entries, e)
	}
	return entries
}

// claimAuditFieldChanges returns the field_change rows for one issue, in
// file order, with the extra keys the kind documents.
type claimAuditFieldChange struct {
	Field    string
	OldValue string
	NewValue string
}

func claimAuditFieldChanges(t *testing.T, entries []claimAuditEntry, issueID string) []claimAuditFieldChange {
	t.Helper()
	var out []claimAuditFieldChange
	for _, e := range entries {
		if e.Kind != "field_change" || e.IssueID != issueID {
			continue
		}
		field, _ := e.Extra["field"].(string)
		if field == "" {
			t.Fatalf("field_change row for %s has no field: %v", issueID, e.Extra)
		}
		oldValue, _ := e.Extra["old_value"].(string)
		newValue, _ := e.Extra["new_value"].(string)
		out = append(out, claimAuditFieldChange{Field: field, OldValue: oldValue, NewValue: newValue})
	}
	return out
}

// assertClaimAuditFieldChanges asserts the issue's field_change rows are
// exactly want, in order, and carry the expected actor.
func assertClaimAuditFieldChanges(t *testing.T, beadsDir, issueID string, want []claimAuditFieldChange) {
	t.Helper()
	entries := claimAuditEntries(t, beadsDir)
	got := claimAuditFieldChanges(t, entries, issueID)
	if len(got) != len(want) {
		t.Fatalf("field_change rows for %s = %+v, want exactly %+v", issueID, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("field_change row %d for %s = %+v, want %+v", i, issueID, got[i], want[i])
		}
	}
	for _, e := range entries {
		if e.Kind == "field_change" && e.IssueID == issueID && e.Actor != claimAuditActor {
			t.Errorf("field_change row for %s has actor %q, want %q", issueID, e.Actor, claimAuditActor)
		}
	}
}

// assertClaimAuditClaimed asserts the sidecar holds exactly one claimed
// record for the issue, carrying the returned post-state, the calling actor,
// and no invented prior values (no field/old_value/new_value keys).
func assertClaimAuditClaimed(t *testing.T, beadsDir, issueID, wantStatus string) {
	t.Helper()
	var claimed []claimAuditEntry
	for _, e := range claimAuditEntries(t, beadsDir) {
		if e.Kind == "claimed" && e.IssueID == issueID {
			claimed = append(claimed, e)
		}
	}
	if len(claimed) != 1 {
		t.Fatalf("claimed records for %s = %d, want exactly 1:\n%+v", issueID, len(claimed), claimed)
	}
	e := claimed[0]
	if e.Actor != claimAuditActor {
		t.Errorf("claimed record for %s has actor %q, want %q", issueID, e.Actor, claimAuditActor)
	}
	status, _ := e.Extra["status"].(string)
	assignee, _ := e.Extra["assignee"].(string)
	if status != wantStatus || assignee != claimAuditActor || len(e.Extra) != 2 {
		t.Errorf("claimed record for %s has extra %v, want exactly {status: %q, assignee: %q} with no invented prior values",
			issueID, e.Extra, wantStatus, claimAuditActor)
	}
}
