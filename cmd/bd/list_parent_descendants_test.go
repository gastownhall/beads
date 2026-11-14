package main

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// parentTreeStore is an in-memory --parent tree. GetIssue and SearchIssues
// answer from it, SearchIssues answering only the two shapes the --parent path
// sends (an exact ID lookup and a one-level ParentID search), and every
// SearchIssues filter is recorded.
type parentTreeStore struct {
	storage.DoltStorage
	issues   map[string]*types.Issue
	children map[string][]string
	searches []types.IssueFilter
}

func newParentTreeStore() *parentTreeStore {
	s := &parentTreeStore{issues: map[string]*types.Issue{}, children: map[string][]string{}}
	add := func(parent string, ids ...string) {
		for _, id := range ids {
			s.issues[id] = &types.Issue{ID: id, Status: types.StatusOpen}
		}
		if parent != "" {
			s.children[parent] = append(s.children[parent], ids...)
		}
	}
	add("", "t-root")
	add("t-root", "t-a", "t-b")
	add("t-a", "t-a1", "t-a2")
	add("t-a1", "t-a1x")
	// A cycle back to the root and a node reached from two parents.
	s.children["t-b"] = []string{"t-root", "t-a2"}
	return s
}

func (s *parentTreeStore) GetIssue(_ context.Context, id string) (*types.Issue, error) {
	if issue, ok := s.issues[id]; ok {
		return issue, nil
	}
	return nil, storage.ErrNotFound
}

func (s *parentTreeStore) childrenOf(parentID string) []*types.Issue {
	var out []*types.Issue
	for _, id := range s.children[parentID] {
		out = append(out, s.issues[id])
	}
	return out
}

func (s *parentTreeStore) SearchIssues(_ context.Context, _ string, filter types.IssueFilter) ([]*types.Issue, error) {
	s.searches = append(s.searches, filter)
	switch {
	case len(filter.IDs) > 0:
		var out []*types.Issue
		for _, id := range filter.IDs {
			if issue, ok := s.issues[id]; ok {
				out = append(out, issue)
			}
		}
		return out, nil
	case filter.ParentID != nil:
		return s.childrenOf(*filter.ParentID), nil
	}
	return nil, errors.New("parentTreeStore: unexpected search")
}

// descendantCall is one SearchDescendants call.
type descendantCall struct {
	rootID string
	filter types.IssueFilter
}

// descendantTreeStore is parentTreeStore with the SearchDescendants capability.
// It answers from the tree directly, never through SearchIssues, so any
// ParentID search parentTreeStore records came from the caller.
type descendantTreeStore struct {
	*parentTreeStore
	calls []descendantCall
}

func (s *descendantTreeStore) SearchDescendants(_ context.Context, rootID string, filter types.IssueFilter) ([]*types.Issue, error) {
	s.calls = append(s.calls, descendantCall{rootID: rootID, filter: filter})
	seen := map[string]bool{}
	var out []*types.Issue
	var walk func(string)
	walk = func(parentID string) {
		for _, child := range s.childrenOf(parentID) {
			if !seen[child.ID] {
				seen[child.ID] = true
				out = append(out, child)
				walk(child.ID)
			}
		}
	}
	walk(rootID)
	return out, nil
}

// unwrappingStore is a decorator that hides the inner store's optional
// capabilities from a type assertion but hands the inner store to Unwrap.
type unwrappingStore struct {
	storage.DoltStorage
}

func (s unwrappingStore) Unwrap() storage.DoltStorage { return s.DoltStorage }

func sortedIssueIDs(issues []*types.Issue) []string {
	ids := make([]string, 0, len(issues))
	for _, issue := range issues {
		ids = append(ids, issue.ID)
	}
	slices.Sort(ids)
	return ids
}

// TestHierarchicalChildrenUsesStoreDescendantSearch pins that the --parent
// tree (`bd list --parent`, and every `bd list --watch --parent` refresh) is
// one SearchDescendants call when the innermost store, behind any decorators,
// offers it: one read transaction instead of a search, and on the embedded
// backend an engine open, per tree node (GH#3415). The tree must come out the
// same as the node-by-node walk a store without the capability gets.
func TestHierarchicalChildrenUsesStoreDescendantSearch(t *testing.T) {
	ctx := context.Background()
	open := types.StatusOpen
	filter := types.IssueFilter{Status: &open, Labels: []string{"ui"}, Limit: 7}

	plain := newParentTreeStore()
	walked, err := getHierarchicalChildren(ctx, plain, "", "t-root", filter)
	if err != nil {
		t.Fatalf("getHierarchicalChildren without the capability: %v", err)
	}
	wantIDs := sortedIssueIDs(walked)
	if want := []string{"t-a", "t-a1", "t-a1x", "t-a2", "t-b", "t-root"}; !slices.Equal(wantIDs, want) {
		t.Fatalf("node-by-node tree = %v, want %v", wantIDs, want)
	}

	for decorators := range 3 {
		t.Run(fmt.Sprintf("decorators=%d", decorators), func(t *testing.T) {
			inner := &descendantTreeStore{parentTreeStore: newParentTreeStore()}
			var s storage.DoltStorage = inner
			for range decorators {
				s = unwrappingStore{s}
			}

			got, err := getHierarchicalChildren(ctx, s, "", "t-root", filter)
			if err != nil {
				t.Fatalf("getHierarchicalChildren: %v", err)
			}
			if len(inner.calls) != 1 {
				t.Fatalf("SearchDescendants calls = %d, want 1", len(inner.calls))
			}
			if call := inner.calls[0]; call.rootID != "t-root" || !reflect.DeepEqual(call.filter, filter) {
				t.Fatalf("SearchDescendants(%q, %+v), want (%q, %+v)", call.rootID, call.filter, "t-root", filter)
			}
			for _, f := range inner.searches {
				if f.ParentID != nil {
					t.Fatalf("SearchIssues called with ParentID=%q alongside SearchDescendants", *f.ParentID)
				}
			}
			if gotIDs := sortedIssueIDs(got); !slices.Equal(gotIDs, wantIDs) {
				t.Fatalf("tree = %v, want the node-by-node walk's %v", gotIDs, wantIDs)
			}
		})
	}
}
