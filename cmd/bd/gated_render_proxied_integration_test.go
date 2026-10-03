//go:build cgo

package main

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/ui"
)

// TestProxiedServerGatedRendering is the SHOULD-4 half of wy-j2upyy: the
// proxied-server TEXT routes must render the same derived GATED decoration the
// direct routes do.
//
// The proxied --json routes already carried it (uow issue_reader →
// workapi.BuildIssueDetails), so without this the SAME bead read two ways from
// the SAME server disagrees: gated_by in the JSON, a plain OPEN row in the
// terminal. Whole constellations run proxied — it is the default for a shared
// dolt server — so "upstream ships it" and "the seat that filed the bug sees
// it" are not the same statement until this passes.
func TestProxiedServerGatedRendering(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	p := newSharedProxiedProject(t, bd, "pgd")
	target := bdProxiedCreate(t, bd, p.dir, "Proxied gated target", "-p", "2")

	out, stderr, err := bdProxiedRunBuffers(t, bd, p.dir,
		"gate", "create", "--type=human", "--blocks", target.ID, "--reason", "Need design review")
	if err != nil {
		t.Fatalf("gate create failed: %v\nstderr:\n%s", err, stderr)
	}
	gateID := parseCreatedGateID(t, out)

	t.Run("show_text_says_gated", func(t *testing.T) {
		showOut := bdProxiedShowRaw(t, bd, p.dir, target.ID)
		t.Logf("bd show %s:\n%s", target.ID, showOut)
		if !strings.Contains(showOut, "· GATED]") {
			t.Errorf("proxied show header missing the GATED marker:\n%s", showOut)
		}
		wantLine := "Gated by: " + gateID + " (human: Need design review)"
		if !strings.Contains(showOut, wantLine) {
			t.Errorf("proxied show meta missing %q:\n%s", wantLine, showOut)
		}
	})

	// The route that was already right, kept beside the one that was not: the
	// text and the JSON are two readings of one bead and must agree.
	t.Run("show_json_still_carries_gated_by", func(t *testing.T) {
		details := bdProxiedShowDetailsFirst(t, bd, p.dir, target.ID)
		raw, ok := details["gated_by"]
		if !ok {
			t.Fatalf("proxied show --json has no gated_by")
		}
		entries, ok := raw.([]any)
		if !ok || len(entries) != 1 {
			t.Fatalf("gated_by = %#v, want one entry", raw)
		}
		entry, _ := entries[0].(map[string]any)
		if entry["id"] != gateID {
			t.Errorf("gated_by[0].id = %v, want %s", entry["id"], gateID)
		}
	})

	t.Run("pretty_list_row_carries_the_glyph", func(t *testing.T) {
		listOut := bdProxiedList(t, bd, p)
		t.Logf("bd list:\n%s", listOut)
		row := listRowFor(t, listOut, target.ID)
		if !strings.HasPrefix(strings.TrimSpace(row), ui.StatusIconGated) {
			t.Errorf("proxied pretty row does not lead with %q:\n%s", ui.StatusIconGated, row)
		}
	})

	t.Run("compact_list_row_carries_the_glyph_and_names_the_gate", func(t *testing.T) {
		listOut := bdProxiedList(t, bd, p, "--flat")
		t.Logf("bd list --flat:\n%s", listOut)
		row := listRowFor(t, listOut, target.ID)
		if !strings.Contains(row, ui.StatusIconGated) {
			t.Errorf("proxied compact row missing %q:\n%s", ui.StatusIconGated, row)
		}
		if !strings.Contains(row, "gated by: "+gateID) {
			t.Errorf("proxied compact row does not name the gate:\n%s", row)
		}
	})

	t.Run("agent_mode_line_names_the_gate", func(t *testing.T) {
		stdout, stderr, err := bdProxiedRunBuffersWithEnv(t, bd, p.dir, []string{"CLAUDE_CODE=1"}, "list", "--flat")
		if err != nil {
			t.Fatalf("bd list --flat failed: %v\nstderr:\n%s", err, stderr)
		}
		t.Logf("CLAUDE_CODE=1 bd list --flat:\n%s", stdout)
		row := listRowFor(t, stdout, target.ID)
		if !strings.Contains(row, "gated by: "+gateID) {
			t.Errorf("proxied agent row does not name the gate %s:\n%s", gateID, row)
		}
	})

	// The subject clause reaches the proxied routes through the same helper,
	// so a closed bead is undecorated here too.
	t.Run("closed_subject_renders_no_gate", func(t *testing.T) {
		closedTarget := bdProxiedCreate(t, bd, p.dir, "Proxied closed under a gate", "-p", "2")
		if _, stderr, err := bdProxiedRunBuffers(t, bd, p.dir,
			"gate", "create", "--type=human", "--blocks", closedTarget.ID, "--reason", "hold"); err != nil {
			t.Fatalf("gate create failed: %v\nstderr:\n%s", err, stderr)
		}
		if _, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "close", closedTarget.ID, "--force", "-r", "done anyway"); err != nil {
			t.Fatalf("bd close --force failed: %v\nstderr:\n%s", err, stderr)
		}

		showOut := bdProxiedShowRaw(t, bd, p.dir, closedTarget.ID)
		t.Logf("bd show %s:\n%s", closedTarget.ID, showOut)
		if strings.Contains(showOut, "GATED") || strings.Contains(showOut, "Gated by:") {
			t.Errorf("proxied show decorated a closed subject:\n%s", showOut)
		}
		row := listRowFor(t, bdProxiedList(t, bd, p, "--flat", "--status", "closed"), closedTarget.ID)
		if strings.Contains(row, ui.StatusIconGated) {
			t.Errorf("proxied closed row carries the gated glyph:\n%s", row)
		}
	})
}
