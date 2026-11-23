//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestEmbeddedShowJSONReportsClosedBy reproduces GH#8617: bd records the actor
// that closed an issue (bd history --events prints "closed by <actor>"), but the
// issue JSON from `bd show --json` carries only closed_at and close_reason, so a
// consumer of the issue record cannot tell who closed it.
func TestEmbeddedShowJSONReportsClosedBy(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "cb")

	issue := bdCreate(t, bd, dir, "Closed by actor", "--type", "task", "--actor", "alice")
	bdClose(t, bd, dir, issue.ID, "--actor", "alice", "--reason", "done")

	cmd := exec.Command(bd, "show", issue.ID, "--json")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd show --json failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	s := stdout.String()
	start := strings.IndexAny(s, "[{")
	if start < 0 {
		t.Fatalf("no JSON in bd show output:\n%s", s)
	}
	var obj map[string]any
	dec := json.NewDecoder(strings.NewReader(s[start:]))
	if s[start] == '[' {
		var arr []map[string]any
		if err := dec.Decode(&arr); err != nil || len(arr) == 0 {
			t.Fatalf("decode bd show --json array: %v\n%s", err, s[start:])
		}
		obj = arr[0]
	} else if err := dec.Decode(&obj); err != nil {
		t.Fatalf("decode bd show --json: %v\n%s", err, s[start:])
	}

	if obj["status"] != "closed" || obj["closed_at"] == nil {
		t.Fatalf("issue not closed in show JSON: status=%v closed_at=%v", obj["status"], obj["closed_at"])
	}
	if got, ok := obj["closed_by"]; !ok || got != "alice" {
		t.Errorf("bd show --json closed_by = %v (present=%v), want %q (the --actor that closed it)\nJSON: %s",
			got, ok, "alice", s[start:])
	}

	// The text view reports the closer too (GH#8617's "who closed this" half
	// for human readers), beside the close metadata it already carries.
	showOut, err := bdRunWithFlockRetry(t, bd, dir, "show", issue.ID)
	if err != nil {
		t.Fatalf("bd show %s failed: %v\n%s", issue.ID, err, showOut)
	}
	if !strings.Contains(string(showOut), "Closed by: alice") {
		t.Errorf("bd show text output missing %q:\n%s", "Closed by: alice", showOut)
	}

	// bd list --json reads the lite projection, so closed_by must ride
	// IssueSelectColumnsLite as well or the field vanishes on listing paths.
	listOut, err := bdRunWithFlockRetry(t, bd, dir, "list", "--status", "closed", "--json")
	if err != nil {
		t.Fatalf("bd list --status closed --json failed: %v\n%s", err, listOut)
	}
	ls := string(listOut)
	lstart := strings.IndexAny(ls, "[{")
	if lstart < 0 {
		t.Fatalf("no JSON in bd list output:\n%s", ls)
	}
	var listed []map[string]any
	if err := json.NewDecoder(strings.NewReader(ls[lstart:])).Decode(&listed); err != nil {
		var one map[string]any
		if e2 := json.NewDecoder(strings.NewReader(ls[lstart:])).Decode(&one); e2 != nil {
			t.Fatalf("decode bd list --json: %v\n%s", err, ls[lstart:])
		}
		listed = []map[string]any{one}
	}
	var found bool
	for _, row := range listed {
		if row["id"] != issue.ID {
			continue
		}
		found = true
		if got, ok := row["closed_by"]; !ok || got != "alice" {
			t.Errorf("bd list --json closed_by = %v (present=%v), want %q\nrow: %s",
				got, ok, "alice", ls[lstart:])
		}
	}
	if !found {
		t.Errorf("bd list --status closed --json does not list %s:\n%s", issue.ID, ls)
	}
}
