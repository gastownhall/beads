package conformance

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// testCombinedOutgoingRead is RunAll's portable case for
// GetDependenciesWithUnresolvable (bd-uschhs, #5850): an issue with one local
// and two missing outgoing targets. The combined read answers the local issue
// as resolved and the two stored edges as records, and the issue-only
// GetDependenciesWithMetadata answers exactly the resolved half.
func testCombinedOutgoingRead(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "cor-a", Title: "A"}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "cor-b", Title: "B"}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "cor-c", Title: "C"}), "a"))
	must(t, s.AddDependency(c, &types.Dependency{IssueID: "cor-a", DependsOnID: "cor-b", Type: types.DepBlocks}, "a"))
	must(t, s.AddDependency(c, &types.Dependency{IssueID: "cor-a", DependsOnID: "external:other:cap-1", Type: types.DepBlocks, Metadata: `{"why":"x"}`}, "a"))
	must(t, s.AddDependency(c, &types.Dependency{IssueID: "cor-a", DependsOnID: "external:other:cap-0", Type: types.DepRelated}, "a"))

	resolved, unresolvable, err := s.GetDependenciesWithUnresolvable(c, "cor-a")
	must(t, err)
	if got := depMetaKeys(resolved); !reflect.DeepEqual(got, []string{"cor-b:blocks"}) {
		t.Errorf("resolved = %v, want [cor-b:blocks]", got)
	}
	if got := depRecordKeys(unresolvable); !reflect.DeepEqual(got, []string{"external:other:cap-0:related", "external:other:cap-1:blocks"}) {
		t.Errorf("unresolvable = %v, want both external edges sorted by target", got)
	}
	for _, d := range unresolvable {
		if d.IssueID != "cor-a" || d.CreatedBy != "a" || d.CreatedAt.IsZero() {
			t.Errorf("unresolvable record %+v lost its stored source/creator/created_at", d)
		}
		if d.DependsOnID == "external:other:cap-1" && !strings.Contains(d.Metadata, `"why"`) {
			t.Errorf("unresolvable record metadata = %q, want the stored metadata", d.Metadata)
		}
	}
	old, err := s.GetDependenciesWithMetadata(c, "cor-a")
	must(t, err)
	if !reflect.DeepEqual(depMetaKeys(old), depMetaKeys(resolved)) {
		t.Errorf("GetDependenciesWithMetadata = %v, want the combined read's resolved half %v", depMetaKeys(old), depMetaKeys(resolved))
	}

	// A fully local issue has nothing unresolvable.
	_, none, err := s.GetDependenciesWithUnresolvable(c, "cor-c")
	must(t, err)
	if len(none) != 0 {
		t.Errorf("unresolvable for an issue with no edges = %v, want none", depRecordKeys(none))
	}
}

// OutgoingReadersRawExec runs stmts on ONE raw connection of the store's
// database, so a session setting such as foreign_key_checks applies to the
// statements after it. Each backend supplies its own.
type OutgoingReadersRawExec func(t *testing.T, stmts ...string)

// RunOutgoingDependencyReadersAgree pins both outgoing readers to one answer
// across backends on a fixture only raw SQL can build (bd-uschhs): an edge
// that sits in BOTH dependency planes with different types (what a pulled
// promotion next to a local wisp copy leaves), an edge only in the other
// plane, and missing targets in each plane. Every backend runs it with the
// same expectations, so the server and embedded stores agree on membership,
// return one result per far-end id, and give the durable row precedence —
// for a durable source and for a wisp source.
func RunOutgoingDependencyReadersAgree(t *testing.T, s storage.DoltStorage, exec OutgoingReadersRawExec) {
	t.Helper()
	c := context.Background()
	for _, id := range []string{"odr-src", "odr-a", "odr-b"} {
		must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: id, Title: id}), "a"))
	}
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "odr-w", Title: "w", Ephemeral: true}), "a"))

	// Durable source: blocks odr-a and an external target, both durable rows.
	must(t, s.AddDependency(c, &types.Dependency{IssueID: "odr-src", DependsOnID: "odr-a", Type: types.DepBlocks}, "a"))
	must(t, s.AddDependency(c, &types.Dependency{IssueID: "odr-src", DependsOnID: "external:x:durable", Type: types.DepTracks}, "a"))
	// Wisp source: blocks odr-a, a wisp-plane row.
	must(t, s.AddDependency(c, &types.Dependency{IssueID: "odr-w", DependsOnID: "odr-a", Type: types.DepBlocks}, "a"))

	const ins = "INSERT INTO %s (id, issue_id, %s, type, created_at, created_by, metadata) VALUES (UUID(), '%s', '%s', '%s', NOW(), 'raw', '{}')"
	exec(t,
		"SET foreign_key_checks = 0",
		// The durable source's duplicate of odr-a in the wisp plane, typed
		// differently: the durable row must win.
		fmt.Sprintf(ins, "wisp_dependencies", "depends_on_issue_id", "odr-src", "odr-a", "related"),
		// An edge only the wisp plane holds, onto a local issue and onto a
		// missing target.
		fmt.Sprintf(ins, "wisp_dependencies", "depends_on_issue_id", "odr-src", "odr-b", "related"),
		fmt.Sprintf(ins, "wisp_dependencies", "depends_on_external", "odr-src", "external:x:wisp", "related"),
		// The wisp source's duplicate of odr-a in the durable plane, typed
		// differently: the durable row wins here too.
		fmt.Sprintf(ins, "dependencies", "depends_on_issue_id", "odr-w", "odr-a", "related"),
		"SET foreign_key_checks = 1",
	)

	for _, tc := range []struct {
		source       string
		resolved     []string
		unresolvable []string
	}{
		{source: "odr-src", resolved: []string{"odr-a:blocks", "odr-b:related"}, unresolvable: []string{"external:x:durable:tracks", "external:x:wisp:related"}},
		{source: "odr-w", resolved: []string{"odr-a:related"}, unresolvable: []string{}},
	} {
		old, err := s.GetDependenciesWithMetadata(c, tc.source)
		must(t, err)
		if got := depMetaKeys(old); !reflect.DeepEqual(got, tc.resolved) {
			t.Errorf("GetDependenciesWithMetadata(%s) = %v, want %v (one per far end, durable first)", tc.source, got, tc.resolved)
		}
		resolved, unresolvable, err := s.GetDependenciesWithUnresolvable(c, tc.source)
		must(t, err)
		if got := depMetaKeys(resolved); !reflect.DeepEqual(got, tc.resolved) {
			t.Errorf("GetDependenciesWithUnresolvable(%s) resolved = %v, want %v", tc.source, got, tc.resolved)
		}
		if got := depRecordKeys(unresolvable); !reflect.DeepEqual(got, tc.unresolvable) {
			t.Errorf("GetDependenciesWithUnresolvable(%s) unresolvable = %v, want %v", tc.source, got, tc.unresolvable)
		}
	}
}

func depMetaKeys(deps []*types.IssueWithDependencyMetadata) []string {
	out := []string{}
	for _, d := range deps {
		out = append(out, d.ID+":"+string(d.DependencyType))
	}
	return out
}

func depRecordKeys(deps []*types.Dependency) []string {
	out := []string{}
	for _, d := range deps {
		out = append(out, d.DependsOnID+":"+string(d.Type))
	}
	return out
}

// testRelationsAndTreeSeeOnlyRealRows pins the GH#5850 AC-6 invariants on
// real databases: the consumers that sit on the existing metadata readers —
// the Relations neighbor query and the recursive dependency-tree walk —
// receive only real rows when an anchor carries an edge whose target has no
// row in this database. The tree is the sharper pin: its recursion fetches
// each neighbor by id, so a missing target that leaked through would fail
// the whole walk with not-found rather than merely drawing a wrong tree —
// this test passing means no fetch of the missing id was ever attempted.
func testRelationsAndTreeSeeOnlyRealRows(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()

	const anchor = "test-tu"
	const child = "test-tv"
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: anchor, Title: "anchor"}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: child, Title: "real child"}), "a"))
	must(t, s.AddDependency(c, &types.Dependency{IssueID: anchor, DependsOnID: child, Type: types.DepBlocks}, "actor"))
	must(t, s.AddDependency(c, &types.Dependency{IssueID: anchor, DependsOnID: "external:liveop:gone", Type: types.DepTracks}, "actor"))

	// Relations: the neighbor list is the anchor's real rows alone — no
	// placeholder for the missing far end, no error naming it.
	rel, err := s.IssueRelations()
	must(t, err)
	neighbors, err := rel.Related(c, issueops.RelatedRequest{ID: anchor, Direction: issueops.RelationOut})
	must(t, err)
	if len(neighbors) != 1 || neighbors[0].ID != child || neighbors[0].DependencyType != types.DepBlocks {
		t.Fatalf("Related(out) = %+v, want exactly the one real neighbor %q under a blocks edge", neighbors, child)
	}

	// The dependency tree: root plus the real child, and the walk did not
	// fail fetching the missing target (its edge type, tracks, is a tree
	// edge, so a leak would have been followed, not filtered).
	tree, err := s.GetDependencyTree(c, anchor, 3, false, false)
	must(t, err)
	if len(tree) != 2 {
		t.Fatalf("tree = %+v, want exactly the root and the real child", tree)
	}
	if tree[0].ID != anchor || tree[0].Depth != 0 {
		t.Errorf("tree[0] = %+v, want the anchor at depth 0", tree[0])
	}
	if tree[1].ID != child || tree[1].Depth != 1 || tree[1].ParentID != anchor || tree[1].EdgeFromParent != types.DepBlocks {
		t.Errorf("tree[1] = %+v, want the real child under a blocks edge", tree[1])
	}
}
