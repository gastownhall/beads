//go:build cgo

package tracker

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// AC-6 (bd-uschhs, #5850): the combined outgoing read is a separate method;
// sync, descendant traversal and Relations keep the issue-only readers. On a
// fixture of local issues with edges onto targets this database has no row
// for, each sees only real issue rows: the descendant walk never queues a
// missing target, a parent-filtered push sends only the two real issues, the
// sync writes no dependency, and Relations answers no placeholder.
func TestUnresolvableTargetsStayOutOfSyncTraversalAndRelations(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	for _, issue := range []*types.Issue{
		{ID: "bd-ub6p", Title: "Parent", Status: types.StatusOpen, IssueType: types.TypeTask, Priority: 2},
		{ID: "bd-ub6c", Title: "Child", Status: types.StatusOpen, IssueType: types.TypeTask, Priority: 2},
	} {
		if err := store.CreateIssue(ctx, issue, "test-actor"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", issue.ID, err)
		}
	}
	for _, dep := range []*types.Dependency{
		{IssueID: "bd-ub6c", DependsOnID: "bd-ub6p", Type: types.DepParentChild},
		{IssueID: "bd-ub6p", DependsOnID: "external:other:gone-1", Type: types.DepBlocks},
		{IssueID: "bd-ub6c", DependsOnID: "liveop-gone", Type: types.DepTracks},
	} {
		if err := store.AddDependency(ctx, dep, "test-actor"); err != nil {
			t.Fatalf("AddDependency(%s -> %s): %v", dep.IssueID, dep.DependsOnID, err)
		}
	}

	// The issue-only reader sync uses: real rows only.
	for id, want := range map[string][]string{"bd-ub6p": {}, "bd-ub6c": {"bd-ub6p"}} {
		rows, err := store.GetDependenciesWithMetadata(ctx, id)
		if err != nil {
			t.Fatalf("GetDependenciesWithMetadata(%s): %v", id, err)
		}
		if got := relatedIDs(rows); !reflect.DeepEqual(got, want) {
			t.Errorf("GetDependenciesWithMetadata(%s) = %v, want only real rows %v", id, got, want)
		}
	}
	if dependencyExists(ctx, store, "bd-ub6c", "liveop-gone", types.DepTracks) {
		t.Error("dependencyExists reported a missing target as a real edge")
	}

	tk := newMockTracker("test")
	engine := NewEngine(tk, store, "test-actor")

	set, err := engine.buildDescendantSet(ctx, "bd-ub6p")
	if err != nil {
		t.Fatalf("buildDescendantSet: %v", err)
	}
	if want := map[string]bool{"bd-ub6p": true, "bd-ub6c": true}; !reflect.DeepEqual(set, want) {
		t.Errorf("descendant set = %v, want only the two real issues", set)
	}

	before := [2]int64{}
	for i, id := range []string{"bd-ub6p", "bd-ub6c"} {
		if before[i], err = store.CountDependencies(ctx, id); err != nil {
			t.Fatalf("CountDependencies(%s): %v", id, err)
		}
	}
	if _, err := engine.Sync(ctx, SyncOptions{Push: true, ParentID: "bd-ub6p"}); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	pushed := []string{}
	for _, issue := range tk.created {
		pushed = append(pushed, issue.ID)
	}
	sort.Strings(pushed)
	if want := []string{"bd-ub6c", "bd-ub6p"}; !reflect.DeepEqual(pushed, want) {
		t.Errorf("pushed %v, want only the real issues %v", pushed, want)
	}
	for i, id := range []string{"bd-ub6p", "bd-ub6c"} {
		after, err := store.CountDependencies(ctx, id)
		if err != nil {
			t.Fatalf("CountDependencies(%s): %v", id, err)
		}
		if after != before[i] {
			t.Errorf("sync changed %s's dependency count %d -> %d: it wrote a link", id, before[i], after)
		}
	}

	rel, err := store.IssueRelations()
	if err != nil {
		t.Fatalf("IssueRelations: %v", err)
	}
	for id, want := range map[string][]string{"bd-ub6p": {}, "bd-ub6c": {"bd-ub6p"}} {
		rows, err := rel.Related(ctx, issueops.RelatedRequest{ID: id, Direction: issueops.RelationOut})
		if err != nil {
			t.Fatalf("Related(%s): %v", id, err)
		}
		if got := relatedIDs(rows); !reflect.DeepEqual(got, want) {
			t.Errorf("Related(%s, out) = %v, want only real rows %v", id, got, want)
		}
	}
}

func relatedIDs(rows []*types.IssueWithDependencyMetadata) []string {
	out := []string{}
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
}
