package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The defect these tests pin: `bd dep list <id>` rendered every row as
// "<id>: <title> [P1] (open) via blocks" — a line that is BYTE-IDENTICAL for
// --direction=down and --direction=up. Direction is the whole meaning of a
// dependency edge, so a reader who takes the natural reading of the default
// (down) form reads the graph backwards, with nothing in the output to catch
// it. The KNOWN-BAD CONTROL is explicit in each test: assert that the two
// directions no longer render the same bytes, which is exactly what the
// pre-fix renderer did.

func depListTestNeighbor(id, title string, depType types.DependencyType) *issueops.RelatedIssue {
	iss := &types.IssueWithDependencyMetadata{DependencyType: depType}
	iss.ID = id
	iss.Title = title
	iss.Priority = 1
	iss.Status = types.StatusOpen
	return iss
}

func TestDepEdgeDirectionLabelDistinguishesDirections(t *testing.T) {
	cases := []struct {
		depType  types.DependencyType
		down, up string
	}{
		// Blocking edges get the sharper wording, the same split dep tree
		// uses for its [BLOCKED] badge.
		{types.DepBlocks, "BLOCKED BY", "BLOCKS"},
		{types.DepConditionalBlocks, "BLOCKED BY", "BLOCKS"},
		{types.DepWaitsFor, "BLOCKED BY", "BLOCKS"},
		// Everything else — including parent-child, which is structural and
		// not a blocker, and a workspace's own custom type — gets the
		// structural wording, which claims nothing about blocking.
		{types.DepParentChild, "DEPENDS ON", "DEPENDED ON BY"},
		{types.DepTracks, "DEPENDS ON", "DEPENDED ON BY"},
		{types.DepRelated, "DEPENDS ON", "DEPENDED ON BY"},
		{types.DependencyType("workspace-custom"), "DEPENDS ON", "DEPENDED ON BY"},
	}
	for _, tc := range cases {
		gotDown := depEdgeDirectionLabel("down", tc.depType)
		gotUp := depEdgeDirectionLabel("up", tc.depType)
		if gotDown != tc.down {
			t.Errorf("%s down label = %q, want %q", tc.depType, gotDown, tc.down)
		}
		if gotUp != tc.up {
			t.Errorf("%s up label = %q, want %q", tc.depType, gotUp, tc.up)
		}
		// Known-bad control: pre-fix these were the same (absent) string.
		if gotDown == gotUp {
			t.Errorf("%s renders identically in both directions (%q) — the ambiguity is back", tc.depType, gotDown)
		}
	}
	// An empty direction is the flag's own default, and must read as "down"
	// rather than as an unlabeled row.
	if got := depEdgeDirectionLabel("", types.DepBlocks); got != "BLOCKED BY" {
		t.Errorf("empty direction label = %q, want BLOCKED BY", got)
	}
}

func TestDepListTextRowStatesDirection(t *testing.T) {
	groups := []depListNeighbors{{
		anchorID:  "bd-anchor",
		neighbors: []*issueops.RelatedIssue{depListTestNeighbor("bd-other", "Other issue", types.DepBlocks)},
	}}

	down := captureStdout(t, func() error { return printDepListNeighbors(groups, "down") })
	up := captureStdout(t, func() error { return printDepListNeighbors(groups, "up") })

	if !strings.Contains(down, "BLOCKED BY") || !strings.Contains(down, "bd-other") {
		t.Errorf("down output does not say bd-other BLOCKS the anchor:\n%s", down)
	}
	if !strings.Contains(up, "BLOCKS") || strings.Contains(up, "BLOCKED BY") {
		t.Errorf("up output does not say the anchor blocks bd-other:\n%s", up)
	}
	// Known-bad control: the pre-fix renderer produced the same bytes here.
	if down == up {
		t.Errorf("both directions render identically:\n%s", down)
	}
	// The parts that carried the row before are still there.
	for _, want := range []string{"Other issue", "[P1]", "(open)", "via blocks"} {
		if !strings.Contains(down, want) {
			t.Errorf("down output lost %q:\n%s", want, down)
		}
	}
}

func TestDepListMultiAnchorTextNamesEachAnchor(t *testing.T) {
	groups := []depListNeighbors{
		{anchorID: "bd-a", neighbors: []*issueops.RelatedIssue{depListTestNeighbor("bd-x", "X", types.DepBlocks)}},
		{anchorID: "bd-b", neighbors: []*issueops.RelatedIssue{depListTestNeighbor("bd-y", "Y", types.DepBlocks)}},
	}
	out := captureStdout(t, func() error { return printDepListNeighbors(groups, "up") })
	// With several anchors in one listing, "BLOCKS bd-x" alone cannot say
	// WHICH anchor it blocks — the flat pre-fix list dropped that entirely.
	for _, want := range []string{"Dependents of bd-a", "Dependents of bd-b", "bd-x", "bd-y"} {
		if !strings.Contains(out, want) {
			t.Errorf("multi-anchor output missing %q:\n%s", want, out)
		}
	}

	// A single anchor keeps its historical shape: no heading, just rows.
	single := captureStdout(t, func() error { return printDepListNeighbors(groups[:1], "up") })
	if strings.Contains(single, "Dependents of") {
		t.Errorf("single-anchor output grew a heading it never had:\n%s", single)
	}
}

func TestDepListJSONCarriesDirection(t *testing.T) {
	groups := []depListNeighbors{{
		anchorID:  "bd-anchor",
		neighbors: []*issueops.RelatedIssue{depListTestNeighbor("bd-other", "Other issue", types.DepBlocks)},
	}}

	prev := jsonOutput
	jsonOutput = true
	defer func() { jsonOutput = prev }()

	decode := func(direction string) []map[string]interface{} {
		t.Helper()
		out := captureStdout(t, func() error { return printDepListNeighbors(groups, direction) })
		var rows []map[string]interface{}
		if err := json.Unmarshal([]byte(out), &rows); err != nil {
			t.Fatalf("dep list --json (%s) is not a flat array: %v\n%s", direction, err, out)
		}
		if len(rows) != 1 {
			t.Fatalf("dep list --json (%s) returned %d rows, want 1", direction, len(rows))
		}
		return rows
	}

	down := decode("down")[0]
	up := decode("up")[0]

	if down["dependency_direction"] != "depends-on" {
		t.Errorf("down dependency_direction = %v, want depends-on", down["dependency_direction"])
	}
	if up["dependency_direction"] != "depended-on-by" {
		t.Errorf("up dependency_direction = %v, want depended-on-by", up["dependency_direction"])
	}
	// Known-bad control: pre-fix the payload was the far-end issue's own row
	// and nothing else, so these two were indistinguishable.
	if down["dependency_direction"] == up["dependency_direction"] {
		t.Error("both directions carry the same dependency_direction — the ambiguity is back")
	}
	for _, row := range []map[string]interface{}{down, up} {
		if row["anchor_id"] != "bd-anchor" {
			t.Errorf("anchor_id = %v, want bd-anchor", row["anchor_id"])
		}
		// The addition is additive: the keys the payload always carried are
		// still present, unmoved and unrenamed.
		for _, key := range []string{"id", "title", "status", "priority", "dependency_type"} {
			if _, ok := row[key]; !ok {
				t.Errorf("row lost pre-existing key %q: %v", key, row)
			}
		}
	}
}
