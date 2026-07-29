//go:build cgo

package main

import (
	"testing"

	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

// TestSyncIsScoped covers the gate that decides whether to skip the
// post-sync parent reconcile pass. bd-9w3 root cause: ExcludeTypes is
// almost always non-empty on real rigs (via persistent
// linear.exclude_types config like "molecule,event"), so treating it as
// a scoping signal silently disabled the reconcile pass entirely on
// every full sync — orphans never got repaired.
//
// Contract: only flags the user EXPLICITLY passed this invocation count
// as scoping. Persistent config exclusions don't disable reconcile.
func TestSyncIsScoped(t *testing.T) {
	cases := []struct {
		name string
		opts *tracker.SyncOptions
		want bool
	}{
		{"nil opts", nil, false},
		{"empty opts", &tracker.SyncOptions{}, false},
		{"parent id set", &tracker.SyncOptions{ParentID: "bd-foo"}, true},
		{"issue ids set", &tracker.SyncOptions{IssueIDs: []string{"bd-1", "bd-2"}}, true},
		{"type filter set", &tracker.SyncOptions{TypeFilter: []types.IssueType{types.TypeTask}}, true},

		// bd-9w3: this is the case that was broken. A rig with persistent
		// linear.exclude_types = "molecule,event" populates opts.ExcludeTypes
		// on every sync — used to flip scoped=true and disable reconcile.
		{"exclude types only (persistent config)",
			&tracker.SyncOptions{ExcludeTypes: []types.IssueType{"molecule", "event"}}, false},

		// Combined: exclude_types config + explicit --parent → still scoped
		// (the --parent is the actual scoping signal).
		{"exclude types + parent id",
			&tracker.SyncOptions{
				ParentID:     "bd-foo",
				ExcludeTypes: []types.IssueType{"molecule"},
			}, true},

		// Combined: exclude_types config + explicit --type → scoped
		// (the --type is the scoping signal).
		{"exclude types + type filter",
			&tracker.SyncOptions{
				TypeFilter:   []types.IssueType{types.TypeFeature},
				ExcludeTypes: []types.IssueType{"molecule"},
			}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := syncIsScoped(tc.opts)
			if got != tc.want {
				t.Errorf("syncIsScoped(%+v) = %v, want %v", tc.opts, got, tc.want)
			}
		})
	}
}
