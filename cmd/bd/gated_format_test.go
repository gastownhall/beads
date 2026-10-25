package main

import (
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

func openGate(id, awaitType, reason, awaitID string) *types.Issue {
	return &types.Issue{
		ID:          id,
		IssueType:   types.TypeGate,
		Status:      types.StatusOpen,
		AwaitType:   awaitType,
		AwaitID:     awaitID,
		Description: types.GateDescription("bd-target", reason),
	}
}

func TestFormatIssueHeaderGatedMarker(t *testing.T) {
	issue := &types.Issue{ID: "bd-target", Title: "Ship it", Status: types.StatusOpen, Priority: 2}

	plain := formatIssueHeaderWithGates(issue, nil)
	if !strings.Contains(plain, "[P2 · OPEN]") {
		t.Errorf("ungated header = %q, want the untouched [P2 · OPEN]", plain)
	}
	if strings.Contains(plain, "GATED") {
		t.Errorf("ungated header claims GATED: %q", plain)
	}
	if plain != formatIssueHeader(issue) {
		t.Errorf("formatIssueHeader disagrees with the nil-gate call:\n%q\n%q", formatIssueHeader(issue), plain)
	}

	gated := formatIssueHeaderWithGates(issue, []*types.Issue{openGate("bd-gate", "human", "", "")})
	if !strings.Contains(gated, "[P2 · OPEN · GATED]") {
		t.Errorf("gated header = %q, want [P2 · OPEN · GATED]", gated)
	}
	if !strings.Contains(gated, "Ship it") {
		t.Errorf("gated header lost the title: %q", gated)
	}
}

func TestFormatIssueMetadataGatedLines(t *testing.T) {
	issue := &types.Issue{ID: "bd-target", Title: "Ship it", Status: types.StatusOpen, Priority: 2, CreatedBy: "bee"}

	if got := formatIssueMetadataWithGates(issue, nil); strings.Contains(got, "Gated by:") {
		t.Errorf("ungated meta block mentions a gate: %q", got)
	}

	gates := []*types.Issue{
		openGate("bd-g1", "human", "Need design review", ""),
		openGate("bd-g2", "gh:pr", "", "42"),
		openGate("bd-g3", "", "", ""),
	}
	out := formatIssueMetadataWithGates(issue, gates)
	for _, want := range []string{
		"Gated by: bd-g1 (human: Need design review)",
		"Gated by: bd-g2 (gh:pr, awaiting 42)",
		"Gated by: bd-g3 (gate)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("meta block missing %q:\n%s", want, out)
		}
	}
	if n := strings.Count(out, "Gated by:"); n != 3 {
		t.Errorf("meta block has %d Gated by lines, want one per gate", n)
	}
}

func TestFormatIssueMetadataGatedKeepsDeferredLine(t *testing.T) {
	deferUntil := time.Date(2099, time.January, 15, 12, 0, 0, 0, time.Local)
	issue := &types.Issue{
		ID: "bd-target", Title: "Later", Status: types.StatusDeferred, Priority: 1,
		DeferUntil: &deferUntil,
	}
	out := formatIssueMetadataWithGates(issue, []*types.Issue{openGate("bd-g1", "human", "", "")})
	if !strings.Contains(out, "Deferred: 2099-01-15") {
		t.Errorf("deferred+gated meta lost the Deferred line:\n%s", out)
	}
	if !strings.Contains(out, "Gated by: bd-g1") {
		t.Errorf("deferred+gated meta lost the Gated by line:\n%s", out)
	}
}

func TestFormatPrettyIssueGatedGlyph(t *testing.T) {
	gates := []string{"bd-gate"}
	open := &types.Issue{ID: "bd-1", Title: "Open", Status: types.StatusOpen, Priority: 2}
	if got := formatPrettyIssueGated(open, nil); !strings.HasPrefix(got, ui.StatusIconOpen) {
		t.Errorf("ungated row = %q, want the open glyph", got)
	}
	if got := formatPrettyIssueGated(open, gates); !strings.HasPrefix(got, ui.StatusIconGated) {
		t.Errorf("gated row = %q, want the gated glyph %q", got, ui.StatusIconGated)
	}

	// The gated glyph outranks the deferred snowflake: one column, and the
	// gate is the fact the row cannot otherwise show.
	deferred := &types.Issue{ID: "bd-2", Title: "Later", Status: types.StatusDeferred, Priority: 2}
	if got := formatPrettyIssueGated(deferred, gates); !strings.HasPrefix(got, ui.StatusIconGated) {
		t.Errorf("deferred+gated row = %q, want the gated glyph", got)
	}
	if got := formatPrettyIssueGated(deferred, nil); !strings.HasPrefix(got, ui.StatusIconDeferred) {
		t.Errorf("deferred row = %q, want the deferred glyph", got)
	}

	// A closed issue is never decorated: `bd ready` withholds it on its own
	// account, so a gate marker would claim a causation that is not there.
	closed := &types.Issue{ID: "bd-3", Title: "Done", Status: types.StatusClosed, Priority: 2}
	if got := formatPrettyIssueGated(closed, gates); strings.Contains(got, ui.StatusIconGated) {
		t.Errorf("closed row carries the gated glyph: %q", got)
	}

	// Nor is a PINNED one — and the pin survives, because the glyph never gets
	// the chance to evict it (types.SubjectCanBeGated says no first).
	pinned := &types.Issue{ID: "bd-4", Title: "Pinned", Status: types.StatusPinned, Priority: 2}
	got := formatPrettyIssueGated(pinned, gates)
	if strings.Contains(got, ui.StatusIconGated) {
		t.Errorf("pinned row carries the gated glyph: %q", got)
	}
	if !strings.HasPrefix(got, ui.StatusIconPinned) {
		t.Errorf("pinned+gated row = %q, want it to keep the pin %q", got, ui.StatusIconPinned)
	}
}

// TestFormatIssueCompactGated pins the compact row: the same glyph rule, plus
// the "gated by" clause that names the gate the glyph only hints at.
func TestFormatIssueCompactGated(t *testing.T) {
	issue := &types.Issue{ID: "bd-1", Title: "Open", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}

	var buf strings.Builder
	formatIssueCompact(&buf, issue, nil, []string{"bd-gate"}, nil, "", []string{"bd-gate"})
	got := buf.String()
	if !strings.Contains(got, ui.StatusIconGated) {
		t.Errorf("gated compact row missing %q: %q", ui.StatusIconGated, got)
	}
	if !strings.Contains(got, "gated by: bd-gate") {
		t.Errorf("gated compact row does not name the gate: %q", got)
	}

	buf.Reset()
	pinned := &types.Issue{ID: "bd-2", Title: "Pinned", Status: types.StatusPinned, Priority: 2, IssueType: types.TypeTask}
	formatIssueCompact(&buf, pinned, nil, nil, nil, "", []string{"bd-gate"})
	if got := buf.String(); strings.Contains(got, ui.StatusIconGated) {
		t.Errorf("pinned compact row carries the gated glyph: %q", got)
	}
}

// TestFormatAgentIssueGated is SHOULD-3: agent mode has no glyph column, and
// ui.IsAgentMode() is true in every CLAUDE_CODE seat — the population that
// cannot otherwise tell a gated row from a startable one.
func TestFormatAgentIssueGated(t *testing.T) {
	issue := &types.Issue{ID: "bd-1", Title: "Gated work", Status: types.StatusOpen, Priority: 2}

	var buf strings.Builder
	formatAgentIssue(&buf, issue, nil, nil, "", nil)
	if got := buf.String(); got != "bd-1: Gated work\n" {
		t.Errorf("ungated agent line = %q, want the untouched one-liner", got)
	}

	buf.Reset()
	formatAgentIssue(&buf, issue, []string{"bd-gate"}, nil, "", []string{"bd-gate"})
	got := buf.String()
	if !strings.Contains(got, "gated by: bd-gate") {
		t.Errorf("agent line does not name the gate: %q", got)
	}
	if !strings.Contains(got, "blocked by: bd-gate") {
		t.Errorf("agent line lost its blocked-by clause: %q", got)
	}
}
