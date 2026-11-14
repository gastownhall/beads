package main

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// explainBlockerIDs must hand the explanation every id whose status it
// reads: the blockers of blocked issues AND the blocking-dependency targets of
// ready issues (the ones whose closed-or-pinned status the ready query
// collapsed), once each, and no parent-child or related target.
func TestExplainBlockerIDs(t *testing.T) {
	blocked := []*types.BlockedIssue{
		{Issue: types.Issue{ID: "bd-b1"}, BlockedBy: []string{"bd-x", "bd-y"}},
		{Issue: types.Issue{ID: "bd-b2"}, BlockedBy: []string{"bd-y", ""}},
	}
	ready := []*types.Issue{{ID: "bd-r1"}, {ID: "bd-r2"}}
	allDeps := map[string][]*types.Dependency{
		"bd-r1": {
			{IssueID: "bd-r1", DependsOnID: "bd-pinned", Type: types.DepBlocks},
			{IssueID: "bd-r1", DependsOnID: "bd-x", Type: types.DepConditionalBlocks},
			{IssueID: "bd-r1", DependsOnID: "bd-epic", Type: types.DepParentChild},
		},
		"bd-r2": {
			{IssueID: "bd-r2", DependsOnID: "bd-spawner", Type: types.DepWaitsFor},
			{IssueID: "bd-r2", DependsOnID: "bd-rel", Type: types.DepRelated},
		},
		"bd-not-ready": {
			{IssueID: "bd-not-ready", DependsOnID: "bd-elsewhere", Type: types.DepBlocks},
		},
	}

	got := explainBlockerIDs(blocked, ready, allDeps)
	want := []string{"bd-x", "bd-y", "bd-pinned", "bd-spawner"}
	if len(got) != len(want) {
		t.Fatalf("explainBlockerIDs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("explainBlockerIDs = %v, want %v", got, want)
		}
	}
}

func TestExplainBlockerIDs_Empty(t *testing.T) {
	if got := explainBlockerIDs(nil, nil, nil); len(got) != 0 {
		t.Fatalf("expected no ids, got %v", got)
	}
}
