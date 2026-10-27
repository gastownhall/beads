package main

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// The --parent tree walk routes through storage.SearchDescendants (GH#3415):
// one call on the innermost store's DescendantSearcher capability when there
// is one, the per-node SearchIssues walk otherwise. The spies here pin the
// routing with a static fixture and no backend.

// descendantFixture is a static three-level tree:
//
//	bd-cap0
//	├── bd-cap1
//	│   └── bd-cap3
//	└── bd-cap2
type descendantFixture struct {
	byID     map[string]*types.Issue
	children map[string][]*types.Issue
}

func newDescendantFixture() *descendantFixture {
	f := &descendantFixture{
		byID:     map[string]*types.Issue{},
		children: map[string][]*types.Issue{},
	}
	add := func(id, parent string) {
		f.byID[id] = &types.Issue{ID: id, Title: "issue " + id, Status: types.StatusOpen}
		if parent != "" {
			f.children[parent] = append(f.children[parent], f.byID[id])
		}
	}
	add("bd-cap0", "")
	add("bd-cap1", "bd-cap0")
	add("bd-cap2", "bd-cap0")
	add("bd-cap3", "bd-cap1")
	return f
}

// walk returns the fixture's depth-first descendant order (bd-cap1, bd-cap3,
// bd-cap2), the order WalkDescendants produces, without consulting the spies.
func (f *descendantFixture) walk(rootID string) []*types.Issue {
	var out []*types.Issue
	var visit func(string)
	visit = func(node string) {
		for _, child := range f.children[node] {
			out = append(out, child)
			visit(child.ID)
		}
	}
	visit(rootID)
	return out
}

// descendantStoreBase carries the shared fake: GetIssue answers the parent
// existence check, SearchIssues answers ResolvePartialID's ID lookup (prefix
// match standing in for its resolution) and, on capability-less stores, the
// per-node walk. Everything else on the embedded nil interface panics, so an
// unexpected store call fails the test loudly instead of passing vacuously.
type descendantStoreBase struct {
	storage.DoltStorage
	fixture     *descendantFixture
	searchCalls []types.IssueFilter
}

func (b *descendantStoreBase) GetIssue(ctx context.Context, id string) (*types.Issue, error) {
	if issue, ok := b.fixture.byID[id]; ok {
		return issue, nil
	}
	return nil, storage.ErrNotFound
}

// GetConfig answers the no-prefix default so a resolution that misses the
// ID lookup stays on its prefix-default path instead of panicking on the
// nil embedded interface.
func (b *descendantStoreBase) GetConfig(ctx context.Context, key string) (string, error) {
	return "", nil
}

func (b *descendantStoreBase) SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error) {
	b.searchCalls = append(b.searchCalls, filter)
	if len(filter.IDs) > 0 {
		var out []*types.Issue
		for id, issue := range b.fixture.byID {
			for _, want := range filter.IDs {
				// Containment stands in for ResolvePartialID's resolution:
				// the lookup with a short id answers the full-id row.
				if strings.Contains(id, want) {
					out = append(out, issue)
				}
			}
		}
		return out, nil
	}
	if filter.ParentID != nil {
		return slices.Clone(b.fixture.children[*filter.ParentID]), nil
	}
	return nil, nil
}

// descendantCapabilityCall is one SearchDescendants invocation as the spy saw it.
type descendantCapabilityCall struct {
	rootID string
	filter types.IssueFilter
}

// descendantCapabilitySpy is a store WITH the DescendantSearcher capability.
type descendantCapabilitySpy struct {
	descendantStoreBase
	capabilityCalls []descendantCapabilityCall
}

func (s *descendantCapabilitySpy) SearchDescendants(ctx context.Context, rootID string, filter types.IssueFilter) ([]*types.Issue, error) {
	s.capabilityCalls = append(s.capabilityCalls, descendantCapabilityCall{rootID: rootID, filter: filter})
	return s.fixture.walk(rootID), nil
}

// perNodeSearchSpy is a store WITHOUT the capability, so the walk must fall
// back to per-node SearchIssues calls.
type perNodeSearchSpy struct {
	descendantStoreBase
}

// unwrappingStoreDecorator wraps a store and exposes Unwrap, like the real
// decorators UnwrapStore peels. It defines SearchDescendants of its own so it
// would satisfy the capability WITHOUT the unwrap — the test fails if that is
// the method that ran, pinning that this read bypasses decorators rather than
// requiring them to forward it.
type unwrappingStoreDecorator struct {
	storage.DoltStorage
	capabilityCalls int
}

func (d *unwrappingStoreDecorator) Unwrap() storage.DoltStorage { return d.DoltStorage }

func (d *unwrappingStoreDecorator) SearchDescendants(ctx context.Context, rootID string, filter types.IssueFilter) ([]*types.Issue, error) {
	d.capabilityCalls++
	return nil, nil
}

func hierarchicalResultIDs(issues []*types.Issue) []string {
	ids := make([]string, 0, len(issues))
	for _, issue := range issues {
		ids = append(ids, issue.ID)
	}
	slices.Sort(ids)
	return ids
}

// TestGetHierarchicalChildrenUsesDescendantSearcherCapability pins the
// capability half of the routing: exactly one SearchDescendants call on the
// store INSIDE the decorator, with the resolved root ID and the caller's
// filter unchanged, no per-node SearchIssues with a ParentID, and the same
// resulting ID set as a capability-less store's per-node walk.
func TestGetHierarchicalChildrenUsesDescendantSearcherCapability(t *testing.T) {
	fixture := newDescendantFixture()
	baseFilter := types.IssueFilter{TitleSearch: "needle", Limit: 25}
	ctx := context.Background()

	capSpy := &descendantCapabilitySpy{descendantStoreBase: descendantStoreBase{fixture: fixture}}
	decorated := &unwrappingStoreDecorator{DoltStorage: capSpy}

	// "cap0" is partial on purpose: the capability must see the RESOLVED id.
	capResult, err := getHierarchicalChildren(ctx, decorated, "", "cap0", baseFilter)
	if err != nil {
		t.Fatalf("getHierarchicalChildren with a capability store: %v", err)
	}
	if decorated.capabilityCalls != 0 {
		t.Fatalf("the decorator's own SearchDescendants ran %d time(s); the read must bypass decorators via UnwrapStore", decorated.capabilityCalls)
	}
	if len(capSpy.capabilityCalls) != 1 {
		t.Fatalf("SearchDescendants called %d time(s) on the inner store, want exactly 1", len(capSpy.capabilityCalls))
	}
	call := capSpy.capabilityCalls[0]
	if call.rootID != "bd-cap0" {
		t.Fatalf("SearchDescendants root ID = %q, want the resolved %q", call.rootID, "bd-cap0")
	}
	if !reflect.DeepEqual(call.filter, baseFilter) {
		t.Fatalf("SearchDescendants filter = %+v, want the caller's filter unchanged: %+v", call.filter, baseFilter)
	}
	for _, f := range capSpy.searchCalls {
		if f.ParentID != nil {
			t.Fatalf("capability path still walked per node: SearchIssues with ParentID=%q (all filters: %+v)", *f.ParentID, capSpy.searchCalls)
		}
	}

	nodeSpy := &perNodeSearchSpy{descendantStoreBase: descendantStoreBase{fixture: fixture}}
	nodeResult, err := getHierarchicalChildren(ctx, nodeSpy, "", "cap0", baseFilter)
	if err != nil {
		t.Fatalf("getHierarchicalChildren with a capability-less store: %v", err)
	}
	// Sanity: the fallback really is the per-node walk, not an accidental
	// empty result that would make the ID comparison below vacuous.
	sawParentSearch := false
	for _, f := range nodeSpy.searchCalls {
		if f.ParentID != nil {
			sawParentSearch = true
			break
		}
	}
	if !sawParentSearch {
		t.Fatalf("capability-less store never ran a per-node SearchIssues walk: %+v", nodeSpy.searchCalls)
	}

	// The parent itself is included as the tree root (GH#3349).
	want := []string{"bd-cap0", "bd-cap1", "bd-cap2", "bd-cap3"}
	if got := hierarchicalResultIDs(capResult); !slices.Equal(got, want) {
		t.Fatalf("capability result IDs = %v, want %v", got, want)
	}
	if got := hierarchicalResultIDs(nodeResult); !slices.Equal(got, want) {
		t.Fatalf("per-node result IDs = %v, want %v", got, want)
	}
}
