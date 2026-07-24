//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestGetDependentRecordsEmbedded mirrors the dolt-suite target-keyed dependents
// coverage on the embedded backend: direction, two-table span (durable + wisp
// sources), the type filter, row-id keyset paging with no drop/dup, and
// CountDependentRecords totals.
func TestGetDependentRecordsEmbedded(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	te := newTestEnv(t, "dr")
	ctx := t.Context()

	for _, issue := range []*types.Issue{
		{ID: "dr-target", Title: "target", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "dr-s1", Title: "s1", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "dr-s2", Title: "s2", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "dr-w", Title: "wisp", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true},
	} {
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", issue.ID, err)
		}
	}
	for _, dep := range []*types.Dependency{
		{IssueID: "dr-s1", DependsOnID: "dr-target", Type: types.DepBlocks},
		{IssueID: "dr-s2", DependsOnID: "dr-target", Type: types.DepParentChild},
		{IssueID: "dr-w", DependsOnID: "dr-target", Type: types.DepBlocks}, // wisp source -> wisp_dependencies
	} {
		if err := te.store.AddDependency(ctx, dep, "tester"); err != nil {
			t.Fatalf("AddDependency %s->%s: %v", dep.IssueID, dep.DependsOnID, err)
		}
	}

	set := func(deps []*types.Dependency) map[string]bool {
		m := map[string]bool{}
		for _, d := range deps {
			if d.ID == "" {
				t.Fatalf("dependent row missing ID (keyset cursor): %+v", d)
			}
			m[d.IssueID] = true
		}
		return m
	}

	all, err := te.store.GetDependentRecords(ctx, "dr-target", "", 100, "")
	if err != nil {
		t.Fatalf("GetDependentRecords: %v", err)
	}
	got := set(all)
	if len(got) != 3 || !got["dr-s1"] || !got["dr-s2"] || !got["dr-w"] {
		t.Fatalf("dependents = %v, want {dr-s1, dr-s2, dr-w} (spanning both tables)", got)
	}

	// Row-id keyset paging across the two-table boundary.
	seen := map[string]bool{}
	after := ""
	for i := 0; i < 10; i++ {
		page, err := te.store.GetDependentRecords(ctx, "dr-target", "", 1, after)
		if err != nil {
			t.Fatalf("page after %q: %v", after, err)
		}
		if len(page) == 0 {
			break
		}
		if seen[page[0].IssueID] {
			t.Fatalf("duplicate source %q across pages", page[0].IssueID)
		}
		seen[page[0].IssueID] = true
		after = page[0].ID
	}
	if len(seen) != 3 {
		t.Fatalf("paged sources = %v, want 3 distinct", seen)
	}

	// CountDependentRecords totals span both tables and honor the type filter.
	if n, err := te.store.CountDependentRecords(ctx, "dr-target", ""); err != nil {
		t.Fatalf("CountDependentRecords: %v", err)
	} else if n != 3 {
		t.Fatalf("CountDependentRecords(all) = %d, want 3", n)
	}
	if n, err := te.store.CountDependentRecords(ctx, "dr-target", string(types.DepBlocks)); err != nil {
		t.Fatalf("CountDependentRecords(blocks): %v", err)
	} else if n != 2 {
		t.Fatalf("CountDependentRecords(blocks) = %d, want 2 (dr-s1 + dr-w)", n)
	}
}

// TestGetDependentRecordsForIssuesEmbedded mirrors the dolt-suite batched
// target-keyed coverage on the embedded backend: inbound edges keyed by target
// across a SET of targets in one call, spanning both tables (durable + wisp
// sources), with the FULL dep-type set (blocks, waits-for, conditional-blocks,
// parent-child) and real dep_type preserved, and a decoy (source==id) excluded.
func TestGetDependentRecordsForIssuesEmbedded(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	te := newTestEnv(t, "bt")
	ctx := t.Context()

	for _, issue := range []*types.Issue{
		{ID: "bt-x", Title: "x", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "bt-y", Title: "y", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "bt-blk", Title: "blk", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "bt-wait", Title: "wait", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "bt-cond", Title: "cond", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true},
		{ID: "bt-child", Title: "child", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "bt-yblk", Title: "yblk", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "bt-z", Title: "z", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	} {
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", issue.ID, err)
		}
	}
	for _, dep := range []*types.Dependency{
		{IssueID: "bt-blk", DependsOnID: "bt-x", Type: types.DepBlocks},
		{IssueID: "bt-wait", DependsOnID: "bt-x", Type: types.DepWaitsFor},
		{IssueID: "bt-cond", DependsOnID: "bt-x", Type: types.DepConditionalBlocks}, // wisp source
		{IssueID: "bt-child", DependsOnID: "bt-x", Type: types.DepParentChild},
		{IssueID: "bt-yblk", DependsOnID: "bt-y", Type: types.DepBlocks},
		{IssueID: "bt-x", DependsOnID: "bt-z", Type: types.DepBlocks}, // decoy: bt-x is the SOURCE
	} {
		if err := te.store.AddDependency(ctx, dep, "tester"); err != nil {
			t.Fatalf("AddDependency %s->%s: %v", dep.IssueID, dep.DependsOnID, err)
		}
	}

	typesBySrc := func(deps []*types.Dependency, target string) map[string]types.DependencyType {
		out := map[string]types.DependencyType{}
		for _, d := range deps {
			if d.DependsOnID != target {
				t.Fatalf("row keyed under %s has target %s", target, d.DependsOnID)
			}
			if d.ID == "" {
				t.Fatalf("dependent row missing ID: %+v", d)
			}
			out[d.IssueID] = d.Type
		}
		return out
	}

	byTarget, err := te.store.GetDependentRecordsForIssues(ctx, []string{"bt-x", "bt-y"})
	if err != nil {
		t.Fatalf("GetDependentRecordsForIssues: %v", err)
	}
	x := typesBySrc(byTarget["bt-x"], "bt-x")
	if len(x) != 4 || x["bt-blk"] != types.DepBlocks || x["bt-wait"] != types.DepWaitsFor ||
		x["bt-cond"] != types.DepConditionalBlocks || x["bt-child"] != types.DepParentChild {
		t.Fatalf("X dependents = %v, want the full 4-type set with real dep_type", x)
	}
	if _, bad := x["bt-z"]; bad {
		t.Fatalf("decoy edge X->Z surfaced as an inbound edge of X: %v", x)
	}
	y := typesBySrc(byTarget["bt-y"], "bt-y")
	if len(y) != 1 || y["bt-yblk"] != types.DepBlocks {
		t.Fatalf("Y dependents = %v, want {bt-yblk: blocks}", y)
	}
}
