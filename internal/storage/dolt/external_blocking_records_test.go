package dolt

import (
	"sort"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestGetExternalBlockingDependencyRecordsIncludesCrossPrefixBlockers (GH#4647):
// the record set the external-dependency policy evaluates carries bare
// cross-prefix blocks / conditional-blocks targets beside external: refs, from
// permanent and wisp sources alike and whatever the source's status, and
// leaves out cross-prefix edges of every other type.
func TestGetExternalBlockingDependencyRecordsIncludesCrossPrefixBlockers(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	for _, issue := range []*types.Issue{
		{ID: "test-open", Title: "open source", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "test-closed", Title: "closed source", Status: types.StatusClosed, Priority: 2, IssueType: types.TypeTask},
		{ID: "test-wisp", Title: "wisp source", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true},
	} {
		if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("create %s: %v", issue.ID, err)
		}
	}
	for _, dep := range []*types.Dependency{
		{IssueID: "test-open", DependsOnID: "other-blocker", Type: types.DepBlocks},
		{IssueID: "test-open", DependsOnID: "other-cond", Type: types.DepConditionalBlocks},
		{IssueID: "test-open", DependsOnID: "other-related", Type: types.DepRelated},
		{IssueID: "test-open", DependsOnID: "other-relates", Type: types.DepRelatesTo},
		{IssueID: "test-open", DependsOnID: "other-waits", Type: types.DepWaitsFor},
		{IssueID: "test-open", DependsOnID: "external:proj:cap", Type: types.DepBlocks},
		{IssueID: "test-open", DependsOnID: "external:proj:waitcap", Type: types.DepWaitsFor},
		{IssueID: "test-closed", DependsOnID: "other-blocker", Type: types.DepBlocks},
		{IssueID: "test-wisp", DependsOnID: "other-wisp-blocker", Type: types.DepBlocks},
	} {
		if err := store.AddDependency(ctx, dep, "tester"); err != nil {
			t.Fatalf("add %s -> %s (%s): %v", dep.IssueID, dep.DependsOnID, dep.Type, err)
		}
	}

	got, err := store.GetExternalBlockingDependencyRecords(ctx)
	if err != nil {
		t.Fatalf("GetExternalBlockingDependencyRecords: %v", err)
	}
	targets := func(issueID string) []string {
		var out []string
		for _, dep := range got[issueID] {
			out = append(out, dep.DependsOnID+"/"+string(dep.Type))
		}
		sort.Strings(out)
		return out
	}
	want := map[string][]string{
		"test-open": {
			"external:proj:cap/blocks",
			"external:proj:waitcap/waits-for",
			"other-blocker/blocks",
			"other-cond/conditional-blocks",
		},
		"test-closed": {"other-blocker/blocks"},
		"test-wisp":   {"other-wisp-blocker/blocks"},
	}
	for issueID, wantTargets := range want {
		gotTargets := targets(issueID)
		if len(gotTargets) != len(wantTargets) {
			t.Errorf("%s records = %v, want %v", issueID, gotTargets, wantTargets)
			continue
		}
		for i := range wantTargets {
			if gotTargets[i] != wantTargets[i] {
				t.Errorf("%s records = %v, want %v", issueID, gotTargets, wantTargets)
				break
			}
		}
	}
	if len(got) != len(want) {
		t.Errorf("record sources = %d (%v), want %d", len(got), got, len(want))
	}
}
