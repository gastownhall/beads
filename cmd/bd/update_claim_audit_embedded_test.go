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

// TestEmbeddedUpdateClaimJournalsFieldChanges: GH#6146 — `bd update --claim`
// moves status to in_progress and sets the assignee, so with the audit sidecar
// enabled it must journal the same status/assignee field_change rows that the
// equivalent `bd update --status/--assignee` emits.
func TestEmbeddedUpdateClaimJournalsFieldChanges(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt update tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "ca")
	control := bdCreate(t, bd, dir, "Control status update")
	claimed := bdCreate(t, bd, dir, "Claimed issue")

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(bd, args...)
		cmd.Dir = dir
		cmd.Env = append(bdEnv(dir), "BD_AUDIT_ENABLED=1", "BD_ACTOR=alice")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd %s failed: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	rows := func(issueID string) map[string]string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(beadsDir, "interactions.jsonl"))
		if err != nil && !os.IsNotExist(err) {
			t.Fatalf("read interactions.jsonl: %v", err)
		}
		got := map[string]string{}
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if line == "" {
				continue
			}
			var e struct {
				Kind    string         `json:"kind"`
				IssueID string         `json:"issue_id"`
				Extra   map[string]any `json:"extra"`
			}
			if err := json.Unmarshal([]byte(line), &e); err != nil {
				t.Fatalf("bad journal line %q: %v", line, err)
			}
			if e.Kind != "field_change" || e.IssueID != issueID {
				continue
			}
			field, _ := e.Extra["field"].(string)
			newValue, _ := e.Extra["new_value"].(string)
			got[field] = newValue
		}
		return got
	}

	// Control: a plain status update journals, so the sidecar is live.
	run("update", control.ID, "--status", "in_progress")
	if got := rows(control.ID); got["status"] != "in_progress" {
		t.Fatalf("control: bd update --status journaled %v, want a status→in_progress row", got)
	}

	run("update", claimed.ID, "--claim")
	got := rows(claimed.ID)
	if got["status"] != "in_progress" || got["assignee"] != "alice" {
		t.Fatalf("bd update --claim journaled %v for %s, want status→in_progress and assignee→alice field_change rows", got, claimed.ID)
	}
}
