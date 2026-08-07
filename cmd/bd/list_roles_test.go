package main

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The two projections `bd list` performs between its roles and its formatters.
// Both routes call them, so a regression here shows up on both at once.

// TestListPageIssuesKeepsThePageOrder pins the one thing the projection must not
// do: reorder. The page's order is issueops.Reader.List's decision, and a
// projection that rebuilt the slice from a map would replace it with
// map-iteration order.
func TestListPageIssuesKeepsThePageOrder(t *testing.T) {
	page := issueops.IssuePage{
		Items: []*types.IssueWithCounts{
			{Issue: &types.Issue{ID: "bd-z"}},
			{Issue: &types.Issue{ID: "bd-a"}},
			{Issue: &types.Issue{ID: "bd-m"}},
		},
		HasMore: true,
	}

	issues, hasMore := listPageIssues(page)
	if !hasMore {
		t.Error("hasMore = false; the page's verdict must survive the projection")
	}
	want := []string{"bd-z", "bd-a", "bd-m"}
	if len(issues) != len(want) {
		t.Fatalf("issues = %d rows, want %d", len(issues), len(want))
	}
	for i, id := range want {
		if issues[i].ID != id {
			t.Fatalf("issues[%d].ID = %q, want %q — the page's order is the role's, not this projection's", i, issues[i].ID, id)
		}
	}
}

// TestListPageIssuesDropsANilRowRatherThanPanicking pins the defensive half: the
// role promises no nil row, and a listing is not where a broken implementation
// of it should become a panic in front of a user.
func TestListPageIssuesDropsANilRowRatherThanPanicking(t *testing.T) {
	page := issueops.IssuePage{Items: []*types.IssueWithCounts{
		{Issue: &types.Issue{ID: "bd-1"}},
		nil,
		{Issue: nil},
		{Issue: &types.Issue{ID: "bd-2"}},
	}}

	issues, _ := listPageIssues(page)
	if len(issues) != 2 || issues[0].ID != "bd-1" || issues[1].ID != "bd-2" {
		t.Fatalf("issues = %v, want just the two real rows", issues)
	}
}

// TestNewListBlockingKeysTheDecorationByID pins the other projection: the role
// answers with a slice in request order, and the formatters index by id.
//
// The empty entry is the case worth having: formatDependencyInfo decides
// through `len(blockedBy) == 0 && len(blocks) == 0 && parent == ""` on the
// values these maps hand back, so an absent key and an empty slice have to read
// the same.
func TestNewListBlockingKeysTheDecorationByID(t *testing.T) {
	blocking := newListBlocking(issueops.BlockingResult{Items: []issueops.IssueBlocking{
		{ID: "bd-1", BlockedBy: []string{"bd-2", "bd-3"}, Blocks: []string{}, Parent: "bd-9"},
		{ID: "bd-4", BlockedBy: []string{}, Blocks: []string{"bd-1"}},
		{ID: "bd-5", BlockedBy: []string{}, Blocks: []string{}},
	}})

	if got := blocking.blockedBy["bd-1"]; len(got) != 2 || got[0] != "bd-2" || got[1] != "bd-3" {
		t.Errorf("blockedBy[bd-1] = %v, want the role's list in the role's order", got)
	}
	if got := blocking.parent["bd-1"]; got != "bd-9" {
		t.Errorf("parent[bd-1] = %q, want bd-9", got)
	}
	if got := blocking.blocks["bd-4"]; len(got) != 1 || got[0] != "bd-1" {
		t.Errorf("blocks[bd-4] = %v, want [bd-1]", got)
	}
	if got := formatDependencyInfo(blocking.blockedBy["bd-5"], blocking.blocks["bd-5"], blocking.parent["bd-5"]); got != "" {
		t.Errorf("a bare entry rendered %q, want no decoration at all", got)
	}
	if got := formatDependencyInfo(blocking.blockedBy["bd-absent"], blocking.blocks["bd-absent"], blocking.parent["bd-absent"]); got != "" {
		t.Errorf("an id the role did not mention rendered %q, want no decoration at all", got)
	}
}
