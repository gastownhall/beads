package main

import (
	"context"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

// descendantFakeStore answers GetDependentsWithMetadata from an in-memory
// parent -> children map. Every other storage.Storage method is promoted from
// the nil embedded interface, so a reach for one panics rather than silently
// widening what this test covers.
type descendantFakeStore struct {
	storage.Storage
	children map[string][]string
	// related are non-parent-child dependents, returned alongside the real
	// children so the traversal's edge-type filter is exercised.
	related map[string][]string
}

func (s *descendantFakeStore) GetDependentsWithMetadata(_ context.Context, issueID string) ([]*types.IssueWithDependencyMetadata, error) {
	var out []*types.IssueWithDependencyMetadata
	for _, childID := range s.children[issueID] {
		out = append(out, &types.IssueWithDependencyMetadata{
			Issue:          types.Issue{ID: childID},
			DependencyType: types.DepParentChild,
		})
	}
	for _, otherID := range s.related[issueID] {
		out = append(out, &types.IssueWithDependencyMetadata{
			Issue:          types.Issue{ID: otherID},
			DependencyType: types.DepBlocks,
		})
	}
	return out, nil
}

// TestFilterGitHubLinkScopedIssues pins one row per tracker.SyncOptions
// selector the GitHub commands can set. The --parent row is the one that
// matters most: before the relationship pass honoured opts.ParentID it kept
// every issue in the workspace, so `bd github sync --push --parent X` created
// GitHub links on issues the content push had excluded.
func TestFilterGitHubLinkScopedIssues(t *testing.T) {
	issues := []*types.Issue{
		{ID: "bd-epic", IssueType: types.TypeEpic},
		{ID: "bd-child", IssueType: types.TypeTask},
		{ID: "bd-grandchild", IssueType: types.TypeBug},
		{ID: "bd-outside", IssueType: types.TypeTask},
		{ID: "bd-wisp", IssueType: types.TypeTask, Ephemeral: true},
	}
	store := &descendantFakeStore{
		children: map[string][]string{
			"bd-epic":  {"bd-child"},
			"bd-child": {"bd-grandchild"},
		},
		// bd-outside blocks bd-epic: a dependent, but not a descendant.
		related: map[string][]string{"bd-epic": {"bd-outside"}},
	}

	tests := []struct {
		name string
		opts tracker.SyncOptions
		want []string
	}{
		{
			name: "no selectors keeps everything",
			opts: tracker.SyncOptions{},
			want: []string{"bd-epic", "bd-child", "bd-grandchild", "bd-outside", "bd-wisp"},
		},
		{
			name: "IssueIDs keeps only the listed beads",
			opts: tracker.SyncOptions{IssueIDs: []string{"bd-child", "bd-wisp"}},
			want: []string{"bd-child", "bd-wisp"},
		},
		{
			name: "ParentID keeps the subtree and drops the rest",
			opts: tracker.SyncOptions{ParentID: "bd-epic"},
			want: []string{"bd-epic", "bd-child", "bd-grandchild"},
		},
		{
			name: "ParentID on a leaf keeps only that leaf",
			opts: tracker.SyncOptions{ParentID: "bd-grandchild"},
			want: []string{"bd-grandchild"},
		},
		{
			name: "TypeFilter keeps only the named types",
			opts: tracker.SyncOptions{TypeFilter: []types.IssueType{types.TypeEpic, types.TypeBug}},
			want: []string{"bd-epic", "bd-grandchild"},
		},
		{
			name: "ExcludeTypes drops the named types",
			opts: tracker.SyncOptions{ExcludeTypes: []types.IssueType{types.TypeTask}},
			want: []string{"bd-epic", "bd-grandchild"},
		},
		{
			name: "ExcludeEphemeral drops wisps",
			opts: tracker.SyncOptions{ExcludeEphemeral: true},
			want: []string{"bd-epic", "bd-child", "bd-grandchild", "bd-outside"},
		},
		{
			name: "ParentID composes with the other selectors",
			opts: tracker.SyncOptions{ParentID: "bd-epic", ExcludeTypes: []types.IssueType{types.TypeBug}},
			want: []string{"bd-epic", "bd-child"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Resolve the subtree exactly the way collectGitHubLinkSyncData
			// does, so the helper is under test too.
			var descendantSet map[string]bool
			if tt.opts.ParentID != "" {
				var err error
				descendantSet, err = buildSyncDescendantSet(context.Background(), store, tt.opts.ParentID)
				if err != nil {
					t.Fatalf("buildSyncDescendantSet(%q) error = %v", tt.opts.ParentID, err)
				}
			}

			got := filterGitHubLinkScopedIssues(issues, tt.opts, descendantSet)
			ids := make([]string, 0, len(got))
			for _, issue := range got {
				ids = append(ids, issue.ID)
			}
			if !slices.Equal(ids, tt.want) {
				t.Errorf("filterGitHubLinkScopedIssues() = %v, want %v", ids, tt.want)
			}
		})
	}
}

// TestBuildSyncDescendantSetIgnoresNonParentChildEdges pins the traversal's
// edge-type filter directly: a blocks dependent is not a descendant, so
// --parent must not drag it into scope.
func TestBuildSyncDescendantSetIgnoresNonParentChildEdges(t *testing.T) {
	store := &descendantFakeStore{
		children: map[string][]string{"bd-epic": {"bd-child"}},
		related:  map[string][]string{"bd-epic": {"bd-blocker"}},
	}

	got, err := buildSyncDescendantSet(context.Background(), store, "bd-epic")
	if err != nil {
		t.Fatalf("buildSyncDescendantSet error = %v", err)
	}
	if !got["bd-epic"] || !got["bd-child"] {
		t.Errorf("descendant set = %v, want it to contain bd-epic and bd-child", got)
	}
	if got["bd-blocker"] {
		t.Errorf("descendant set = %v, want it to exclude the blocks-edge dependent bd-blocker", got)
	}
}
