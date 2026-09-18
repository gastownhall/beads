//go:build cgo

package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// bdGateCreateIssue runs "bd gate create --json" and returns the gate issue.
func bdGateCreateIssue(t *testing.T, bd, dir string, args ...string) *types.Issue {
	t.Helper()
	fullArgs := append([]string{"gate", "create", "--json"}, args...)
	out, err := bdRunWithFlockRetry(t, bd, dir, fullArgs...)
	if err != nil {
		t.Fatalf("bd gate create %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return parseIssueJSON(t, out)
}

// bdListEnv is bdList with extra environment, for the agent-mode arm.
func bdListEnv(t *testing.T, bd, dir string, extraEnv []string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bd, append([]string{"list"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(bdEnv(dir), extraEnv...)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd list %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

// listRowFor returns the `bd list` row that names id, in any of the three row
// shapes: pretty ("○ gd-1 P2 Title"), compact ("○ gd-1 [P2] ...") and agent
// mode ("gd-1: Title (...)").
func listRowFor(t *testing.T, out, id string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, id+" ") || strings.Contains(line, id+"[") || strings.Contains(line, id+":") {
			return line
		}
	}
	t.Fatalf("no row for %s in bd list output:\n%s", id, out)
	return ""
}

// TestEmbeddedGatedRendering pins the derived GATED decoration on the human
// surfaces: an issue an OPEN gate blocks is excluded from `bd ready`, so
// `bd show` and `bd list` must say so rather than rendering a plain OPEN row.
func TestEmbeddedGatedRendering(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "gd")

	target := bdCreate(t, bd, dir, "Gated target", "-p", "2")
	gate := bdGateCreateIssue(t, bd, dir, "--blocks", target.ID, "--reason", "Need design review")

	t.Run("ready_excludes_the_gated_issue", func(t *testing.T) {
		out, err := bdRunWithFlockRetry(t, bd, dir, "ready")
		if err != nil {
			t.Fatalf("bd ready failed: %v\n%s", err, out)
		}
		if strings.Contains(string(out), target.ID) {
			t.Fatalf("bd ready listed gated issue %s (the premise of this test):\n%s", target.ID, out)
		}
	})

	t.Run("show_header_and_meta_say_gated", func(t *testing.T) {
		out := bdShowRaw(t, bd, dir, target.ID)
		t.Logf("bd show %s:\n%s", target.ID, out)
		if !strings.Contains(out, "· GATED]") {
			t.Errorf("show header missing GATED marker:\n%s", out)
		}
		wantLine := "Gated by: " + gate.ID + " (human: Need design review)"
		if !strings.Contains(out, wantLine) {
			t.Errorf("show meta missing %q:\n%s", wantLine, out)
		}
	})

	t.Run("show_json_carries_gated_by", func(t *testing.T) {
		details := bdShowDetails(t, bd, dir, target.ID)
		raw, ok := details["gated_by"]
		if !ok {
			t.Fatalf("show --json has no gated_by field: %v", keysOf(details))
		}
		entries, ok := raw.([]any)
		if !ok || len(entries) != 1 {
			t.Fatalf("gated_by = %#v, want one entry", raw)
		}
		entry, ok := entries[0].(map[string]any)
		if !ok {
			t.Fatalf("gated_by[0] = %#v, want an object", entries[0])
		}
		if entry["id"] != gate.ID {
			t.Errorf("gated_by[0].id = %v, want %s", entry["id"], gate.ID)
		}
		if entry["type"] != "human" {
			t.Errorf("gated_by[0].type = %v, want human", entry["type"])
		}
		if entry["reason"] != "Need design review" {
			t.Errorf("gated_by[0].reason = %v, want the gate's reason", entry["reason"])
		}
		// Additive only: the fields bd show --json already published are unchanged.
		for _, key := range []string{"id", "title", "status", "priority", "issue_type"} {
			if _, ok := details[key]; !ok {
				t.Errorf("show --json lost pre-existing field %q", key)
			}
		}
		if details["status"] != "open" {
			t.Errorf("stored status = %v, want open (the decoration is derived, not stored)", details["status"])
		}
	})

	t.Run("list_row_carries_the_gated_glyph", func(t *testing.T) {
		out := bdList(t, bd, dir)
		t.Logf("bd list:\n%s", out)
		row := listRowFor(t, out, target.ID)
		if !strings.HasPrefix(strings.TrimSpace(row), ui.StatusIconGated) {
			t.Errorf("list row for %s does not lead with the gated glyph %q:\n%s",
				target.ID, ui.StatusIconGated, row)
		}
	})

	// --flat leaves the tree view, and with it the whole-rig edge map the tree
	// already had: this arm is what exercises gatedIssueIDs' own batched
	// dependency read.
	t.Run("flat_list_row_carries_the_gated_glyph", func(t *testing.T) {
		out := bdList(t, bd, dir, "--flat")
		t.Logf("bd list --flat:\n%s", out)
		row := listRowFor(t, out, target.ID)
		if !strings.HasPrefix(strings.TrimSpace(row), ui.StatusIconGated) {
			t.Errorf("flat list row for %s does not lead with the gated glyph %q:\n%s",
				target.ID, ui.StatusIconGated, row)
		}
	})

	t.Run("deferred_and_gated_show_both_markers", func(t *testing.T) {
		deferred := bdCreate(t, bd, dir, "Deferred and gated", "-p", "1")
		bdUpdate(t, bd, dir, deferred.ID, "--defer", "2099-01-15")
		dgate := bdGateCreateIssue(t, bd, dir, "--blocks", deferred.ID, "--type", "gh:pr", "--await-id", "42")

		out := bdShowRaw(t, bd, dir, deferred.ID)
		t.Logf("bd show %s:\n%s", deferred.ID, out)
		if !strings.Contains(out, "Deferred: 2099-01-15") {
			t.Errorf("show lost the Deferred line:\n%s", out)
		}
		wantLine := "Gated by: " + dgate.ID + " (gh:pr, awaiting 42)"
		if !strings.Contains(out, wantLine) {
			t.Errorf("show meta missing %q:\n%s", wantLine, out)
		}
		if !strings.Contains(out, "· GATED]") {
			t.Errorf("show header missing GATED marker on a deferred issue:\n%s", out)
		}

		listOut := bdList(t, bd, dir, "--status", "deferred")
		row := listRowFor(t, listOut, deferred.ID)
		if !strings.HasPrefix(strings.TrimSpace(row), ui.StatusIconGated) {
			t.Errorf("deferred+gated list row does not lead with %q:\n%s", ui.StatusIconGated, row)
		}
	})

	// BLOCKER 1 (opus-adv): waits-for is NOT the status rule. The is_blocked
	// recompute sends that leg through waitsForGateBlockedSQL, which blocks
	// only on an OPEN parent-child child of the target (or an also_blocks
	// spawner), so a plain waits-for onto an open childless gate leaves the
	// dependent IN `bd ready` — and a decoration here would contradict it.
	t.Run("waits_for_onto_a_gate_is_not_decorated", func(t *testing.T) {
		subject := bdCreate(t, bd, dir, "Waits-for subject", "-p", "2")
		wgate := bdCreate(t, bd, dir, "Bare gate", "-p", "2", "-t", "gate")
		if out, err := bdRunWithFlockRetry(t, bd, dir, "dep", "add", subject.ID, wgate.ID, "-t", "waits-for"); err != nil {
			t.Fatalf("bd dep add waits-for failed: %v\n%s", err, out)
		}

		out, err := bdRunWithFlockRetry(t, bd, dir, "ready")
		if err != nil {
			t.Fatalf("bd ready failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), subject.ID) {
			t.Fatalf("bd ready dropped %s over a waits-for edge — the premise of this case is that it does not:\n%s", subject.ID, out)
		}

		showOut := bdShowRaw(t, bd, dir, subject.ID)
		if strings.Contains(showOut, "GATED") || strings.Contains(showOut, "Gated by:") {
			t.Errorf("waits-for edge rendered a gate on bd show, while bd ready lists the bead:\n%s", showOut)
		}
		if details := bdShowDetails(t, bd, dir, subject.ID); details["gated_by"] != nil {
			t.Errorf("waits-for edge put gated_by on the detail view: %#v", details["gated_by"])
		}
		for _, listArgs := range [][]string{nil, {"--flat"}} {
			row := listRowFor(t, bdList(t, bd, dir, listArgs...), subject.ID)
			if strings.Contains(row, ui.StatusIconGated) {
				t.Errorf("list %v decorated a waits-for edge:\n%s", listArgs, row)
			}
		}
	})

	// BLOCKER 2 (opus-adv), subject half: unmarkAllBlockedSQL forces
	// is_blocked=0 for a CLOSED subject, so `bd ready` never withholds one on
	// a gate's account and no surface may print [CLOSED · GATED].
	t.Run("closed_subject_renders_no_gate", func(t *testing.T) {
		subject := bdCreate(t, bd, dir, "Closed under a gate", "-p", "2")
		cgate := bdGateCreateIssue(t, bd, dir, "--blocks", subject.ID, "--reason", "hold")
		if out, err := bdRunWithFlockRetry(t, bd, dir, "close", subject.ID, "--force", "-r", "done anyway"); err != nil {
			t.Fatalf("bd close --force failed: %v\n%s", err, out)
		}

		showOut := bdShowRaw(t, bd, dir, subject.ID)
		t.Logf("bd show %s:\n%s", subject.ID, showOut)
		if strings.Contains(showOut, "GATED") {
			t.Errorf("closed subject rendered a GATED marker (gate %s still open):\n%s", cgate.ID, showOut)
		}
		if strings.Contains(showOut, "Gated by:") {
			t.Errorf("closed subject rendered a Gated by line:\n%s", showOut)
		}
		if details := bdShowDetails(t, bd, dir, subject.ID); details["gated_by"] != nil {
			t.Errorf("closed subject published gated_by: %#v", details["gated_by"])
		}
		row := listRowFor(t, bdList(t, bd, dir, "--status", "closed"), subject.ID)
		if strings.Contains(row, ui.StatusIconGated) {
			t.Errorf("closed list row carries the gated glyph:\n%s", row)
		}
	})

	// Same clause, pinned half — and NOTE 5: the pin survives, because a
	// pinned subject is never gated in the first place.
	t.Run("pinned_subject_renders_no_gate_and_keeps_the_pin", func(t *testing.T) {
		subject := bdCreate(t, bd, dir, "Pinned under a gate", "-p", "2")
		bdGateCreateIssue(t, bd, dir, "--blocks", subject.ID, "--reason", "hold")
		if out, err := bdRunWithFlockRetry(t, bd, dir, "update", subject.ID, "--status", "pinned"); err != nil {
			t.Fatalf("bd update --status pinned failed: %v\n%s", err, out)
		}

		showOut := bdShowRaw(t, bd, dir, subject.ID)
		t.Logf("bd show %s:\n%s", subject.ID, showOut)
		if strings.Contains(showOut, "GATED") || strings.Contains(showOut, "Gated by:") {
			t.Errorf("pinned subject rendered a gate:\n%s", showOut)
		}
		if details := bdShowDetails(t, bd, dir, subject.ID); details["gated_by"] != nil {
			t.Errorf("pinned subject published gated_by: %#v", details["gated_by"])
		}
		row := listRowFor(t, bdList(t, bd, dir, "--status", "pinned"), subject.ID)
		if strings.Contains(row, ui.StatusIconGated) {
			t.Errorf("pinned row carries the gated glyph instead of the pin:\n%s", row)
		}
		if !strings.Contains(row, ui.StatusIconPinned) {
			t.Errorf("pinned row lost the pin %q:\n%s", ui.StatusIconPinned, row)
		}
	})

	// SHOULD 3 (opus-adv): agent mode has no glyph column, and every
	// CLAUDE_CODE seat is in it. The gate rides the parenthetical instead.
	t.Run("agent_mode_line_names_the_gate", func(t *testing.T) {
		out := bdListEnv(t, bd, dir, []string{"CLAUDE_CODE=1"}, "--flat")
		t.Logf("CLAUDE_CODE=1 bd list --flat:\n%s", out)
		row := listRowFor(t, out, target.ID)
		if !strings.Contains(row, "gated by: "+gate.ID) {
			t.Errorf("agent-mode row does not name the gate %s:\n%s", gate.ID, row)
		}
	})

	t.Run("resolved_gate_renders_nothing", func(t *testing.T) {
		if out, err := bdRunWithFlockRetry(t, bd, dir, "gate", "resolve", gate.ID); err != nil {
			t.Fatalf("bd gate resolve failed: %v\n%s", err, out)
		}

		out := bdShowRaw(t, bd, dir, target.ID)
		if strings.Contains(out, "GATED") || strings.Contains(out, "Gated by:") {
			t.Errorf("a resolved gate still renders on bd show:\n%s", out)
		}

		details := bdShowDetails(t, bd, dir, target.ID)
		if raw, ok := details["gated_by"]; ok {
			t.Errorf("gated_by present after the gate closed: %#v", raw)
		}

		for _, listArgs := range [][]string{nil, {"--flat"}} {
			listOut := bdList(t, bd, dir, listArgs...)
			row := listRowFor(t, listOut, target.ID)
			if strings.Contains(row, ui.StatusIconGated) {
				t.Errorf("list %v row still carries the gated glyph after resolve:\n%s", listArgs, row)
			}
		}

		listOut := bdList(t, bd, dir)
		row := listRowFor(t, listOut, target.ID)
		if strings.Contains(row, ui.StatusIconGated) {
			t.Errorf("list row still carries the gated glyph after resolve:\n%s", row)
		}
		if !strings.HasPrefix(strings.TrimSpace(row), ui.StatusIconOpen) {
			t.Errorf("ungated row should lead with %q:\n%s", ui.StatusIconOpen, row)
		}
	})
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
