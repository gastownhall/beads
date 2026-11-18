//go:build cgo && integration

package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestCLI_ReadyExcludeParents is the end-to-end regression for GH#6817's
// CLI half: `bd ready --exclude-parents` runs the ordinary ready listing
// minus the parents the close gate counts open children for (AC-2's exact
// command, --limit 0 --json).
//
// The seed is the AC's own fixture in miniature: a parent whose only child
// is open (so the parent is unblocked and ready on its own merits, but
// excluded once the flag runs), a childless task, and an epic whose only
// child is CLOSED — the parent and the epic differ ONLY in their child's
// status, so a leak either way is visible: a definition that counted closed
// children would drop the epic; one that excluded nothing would keep the
// parent. Closing the parent's child mid-test walks the parent back in, the
// transition the conformance case can only stand in for (its fixture has no
// close).
//
// The child itself stays expected until it closes: being someone's child is
// not a readiness defect, and the flag must not vanish it — only the PARENT
// side of an open edge is excluded.
func TestCLI_ReadyExcludeParents(t *testing.T) {
	tmpDir := setupCLITestDB(t)

	create := func(title string, extra ...string) string {
		args := append([]string{"create", title, "--json"}, extra...)
		out := runBDInProcess(t, tmpDir, args...)
		jsonStart := strings.Index(out, "{")
		if jsonStart == -1 {
			t.Fatalf("create %q: no JSON in output: %s", title, out)
		}
		var row struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(out[jsonStart:]), &row); err != nil {
			t.Fatalf("create %q: parse %q: %v", title, out[jsonStart:], err)
		}
		if row.ID == "" {
			t.Fatalf("create %q: no id in %s", title, out[jsonStart:])
		}
		return row.ID
	}

	parent := create("Exclude parents parent")
	child := create("Exclude parents child")
	leaf := create("Exclude parents leaf")
	epic := create("Exclude parents epic", "--type", "epic")
	epicChild := create("Exclude parents epic child")

	runBDInProcess(t, tmpDir, "dep", "add", child, parent, "--type", "parent-child")
	runBDInProcess(t, tmpDir, "dep", "add", epicChild, epic, "--type", "parent-child")
	// The epic's only child closes here; the parent's stays open until the
	// walk-back step below.
	runBDInProcess(t, tmpDir, "close", epicChild)

	readyIDs := func() map[string]bool {
		out := runBDInProcess(t, tmpDir, "ready", "--exclude-parents", "--limit", "0", "--json")
		jsonStart := strings.Index(out, "[")
		if jsonStart == -1 {
			t.Fatalf("ready --json: no JSON array in output: %s", out)
		}
		var rows []struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(out[jsonStart:]), &rows); err != nil {
			t.Fatalf("ready --json: parse %q: %v", out[jsonStart:], err)
		}
		ids := make(map[string]bool, len(rows))
		for _, row := range rows {
			ids[row.ID] = true
		}
		return ids
	}
	assertSet := func(what string, got map[string]bool, want []string) {
		// The fresh template DB holds only the rows this test created, so the
		// flagged answer is asserted exactly, both directions.
		if len(got) != len(want) {
			t.Errorf("%s: ready set = %v, want exactly %v", what, got, want)
			return
		}
		for _, id := range want {
			if !got[id] {
				t.Errorf("%s: %s missing from the flagged ready listing (got %v)", what, id, got)
			}
		}
	}

	got := readyIDs()
	assertSet("open child excludes the parent", got, []string{child, leaf, epic})
	if got[parent] {
		t.Errorf("open child excludes the parent: %s must not be ready under --exclude-parents", parent)
	}

	// Close the child, and the same command walks the parent back in: the
	// exclusion follows the open-children state, not a snapshot of it.
	runBDInProcess(t, tmpDir, "close", child)
	got = readyIDs()
	assertSet("closed child returns the parent", got, []string{leaf, epic, parent})
	if got[child] {
		t.Errorf("closed child returns the parent: %s is closed and must not be ready", child)
	}
}
