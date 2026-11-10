package main

import (
	"context"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// purgeClosedFakeReader is a minimal molReader covering only the calls
// selectClosedWispsForPurge makes. The embedded nil interface makes any other
// call panic loudly rather than return a silent zero value.
//
// rows holds every issue the fake knows by id — closed candidates and live
// sources alike. SearchIssues answers the candidate scan when the filter has
// no IDs, and the live-source scan by filtering rows on the filter's ids and
// statuses, so a closed or custom-done source models "not live" exactly the
// way the real seam would.
type purgeClosedFakeReader struct {
	molReader
	rows     map[string]*types.Issue
	closed   []*types.Issue
	incoming map[string][]*types.Dependency
	custom   []types.CustomStatus
	infra    map[types.IssueType]bool

	captured []types.IssueFilter
}

func (r *purgeClosedFakeReader) SearchIssues(_ context.Context, _ string, filter types.IssueFilter) ([]*types.Issue, error) {
	r.captured = append(r.captured, filter)
	if len(filter.IDs) == 0 {
		return r.closed, nil
	}
	return r.filterRows(filter), nil
}

func (r *purgeClosedFakeReader) filterRows(filter types.IssueFilter) []*types.Issue {
	statuses := make(map[types.Status]bool, len(filter.Statuses))
	for _, s := range filter.Statuses {
		statuses[s] = true
	}
	var out []*types.Issue
	for _, id := range filter.IDs {
		issue, ok := r.rows[id]
		if !ok || !statuses[issue.Status] {
			continue
		}
		out = append(out, issue)
	}
	return out
}

func (r *purgeClosedFakeReader) GetDependentRecordsForIssues(_ context.Context, _ []string) (map[string][]*types.Dependency, error) {
	return r.incoming, nil
}

func (r *purgeClosedFakeReader) GetCustomStatusesDetailed(context.Context) ([]types.CustomStatus, error) {
	return r.custom, nil
}

func (r *purgeClosedFakeReader) IsInfraTypeCtx(_ context.Context, t types.IssueType) bool {
	return r.infra[t]
}

func purgeSelectionIDs(issues []*types.Issue) []string {
	out := make([]string, len(issues))
	for i, issue := range issues {
		out[i] = issue.ID
	}
	return out
}

func closedWisp(id string) *types.Issue {
	return &types.Issue{ID: id, Status: types.StatusClosed, IssueType: types.TypeTask}
}

func liveRow(id string, status types.Status) *types.Issue {
	return &types.Issue{ID: id, Status: status, IssueType: types.TypeTask}
}

func dep(source, target string, t types.DependencyType) *types.Dependency {
	return &types.Dependency{IssueID: source, DependsOnID: target, Type: t}
}

// TestSelectClosedWispsForPurge pins the GH#5152 closed-mode selection on a
// fake reader: which incoming edge types hold a candidate back, which sources
// count as live, how protection propagates through candidate ancestors and
// cycles, the standing pinned/infra/exclude-type protections, and that the
// live-source scan reads every explicit source id rather than a page of them.
func TestSelectClosedWispsForPurge(t *testing.T) {
	cases := []struct {
		name     string
		reader   *purgeClosedFakeReader
		exclude  []types.IssueType
		wantKeep []string
		wantSkip int
	}{
		{
			name: "parent_child_edge_holds_back_live_child",
			reader: &purgeClosedFakeReader{
				rows:     map[string]*types.Issue{"src-open": liveRow("src-open", types.StatusOpen)},
				closed:   []*types.Issue{closedWisp("cand-a")},
				incoming: map[string][]*types.Dependency{"cand-a": {dep("src-open", "cand-a", types.DepParentChild)}},
			},
			wantSkip: 1,
		},
		{
			name: "tracks_edge_holds_back_live_child",
			reader: &purgeClosedFakeReader{
				rows:     map[string]*types.Issue{"src-open": liveRow("src-open", types.StatusOpen)},
				closed:   []*types.Issue{closedWisp("cand-a")},
				incoming: map[string][]*types.Dependency{"cand-a": {dep("src-open", "cand-a", types.DepTracks)}},
			},
			wantSkip: 1,
		},
		{
			// #5735: a closed step whose live dependents arrive only through
			// blocks must stay reclaimable — the whole point of not reusing
			// issueops.SweepLiveDependentEdgeTypes here.
			name: "blocks_only_dependent_does_not_hold_back",
			reader: &purgeClosedFakeReader{
				rows:     map[string]*types.Issue{"src-open": liveRow("src-open", types.StatusOpen)},
				closed:   []*types.Issue{closedWisp("cand-a")},
				incoming: map[string][]*types.Dependency{"cand-a": {dep("src-open", "cand-a", types.DepBlocks)}},
			},
			wantKeep: []string{"cand-a"},
		},
		{
			name: "non_attribution_edges_do_not_hold_back",
			reader: &purgeClosedFakeReader{
				rows:   map[string]*types.Issue{"src-open": liveRow("src-open", types.StatusOpen)},
				closed: []*types.Issue{closedWisp("cand-a")},
				incoming: map[string][]*types.Dependency{"cand-a": {
					dep("src-open", "cand-a", types.DepRelated),
					dep("src-open", "cand-a", types.DepConditionalBlocks),
				}},
			},
			wantKeep: []string{"cand-a"},
		},
		{
			name: "closed_dependent_is_not_live",
			reader: &purgeClosedFakeReader{
				rows:     map[string]*types.Issue{"src-closed": closedWisp("src-closed")},
				closed:   []*types.Issue{closedWisp("cand-a")},
				incoming: map[string][]*types.Dependency{"cand-a": {dep("src-closed", "cand-a", types.DepParentChild)}},
			},
			wantKeep: []string{"cand-a"},
		},
		{
			name: "custom_non_done_status_is_live",
			reader: &purgeClosedFakeReader{
				rows:   map[string]*types.Issue{"src-cust": {ID: "src-cust", Status: types.Status("investigating"), IssueType: types.TypeTask}},
				closed: []*types.Issue{closedWisp("cand-a")},
				incoming: map[string][]*types.Dependency{
					"cand-a": {dep("src-cust", "cand-a", types.DepParentChild)},
				},
				custom: []types.CustomStatus{{Name: "investigating", Category: types.CategoryActive}},
			},
			wantSkip: 1,
		},
		{
			name: "custom_done_status_is_not_live",
			reader: &purgeClosedFakeReader{
				rows:   map[string]*types.Issue{"src-done": {ID: "src-done", Status: types.Status("abandoned"), IssueType: types.TypeTask}},
				closed: []*types.Issue{closedWisp("cand-a")},
				incoming: map[string][]*types.Dependency{
					"cand-a": {dep("src-done", "cand-a", types.DepParentChild)},
				},
				custom: []types.CustomStatus{{Name: "abandoned", Category: types.CategoryDone}},
			},
			wantKeep: []string{"cand-a"},
		},
		{
			// The workapi closure propagates protection up the chain: the
			// open grandchild holds back both its closed parent and its
			// closed grandparent when both are candidates.
			name: "open_grandchild_holds_back_closed_ancestors",
			reader: &purgeClosedFakeReader{
				rows: map[string]*types.Issue{"gc-open": liveRow("gc-open", types.StatusOpen)},
				closed: []*types.Issue{
					closedWisp("cand-parent"),
					closedWisp("cand-grandparent"),
					closedWisp("cand-unrelated"),
				},
				incoming: map[string][]*types.Dependency{
					"cand-parent":      {dep("gc-open", "cand-parent", types.DepParentChild)},
					"cand-grandparent": {dep("cand-parent", "cand-grandparent", types.DepParentChild)},
				},
			},
			wantKeep: []string{"cand-unrelated"},
			wantSkip: 2,
		},
		{
			// A cycle among candidates with no live member terminates and
			// deletes every row in it.
			name: "candidate_only_cycle_is_reclaimed",
			reader: &purgeClosedFakeReader{
				closed: []*types.Issue{closedWisp("cyc-a"), closedWisp("cyc-b")},
				incoming: map[string][]*types.Dependency{
					"cyc-a": {dep("cyc-b", "cyc-a", types.DepParentChild)},
					"cyc-b": {dep("cyc-a", "cyc-b", types.DepParentChild)},
				},
			},
			wantKeep: []string{"cyc-a", "cyc-b"},
		},
		{
			// One live member holds the whole cycle back.
			name: "live_cycle_member_holds_back_cycle",
			reader: &purgeClosedFakeReader{
				rows: map[string]*types.Issue{"live-a": liveRow("live-a", types.StatusInProgress)},
				closed: []*types.Issue{
					closedWisp("cyc-a"),
					closedWisp("cyc-b"),
				},
				incoming: map[string][]*types.Dependency{
					"cyc-a": {dep("cyc-b", "cyc-a", types.DepParentChild)},
					"cyc-b": {dep("live-a", "cyc-b", types.DepParentChild)},
				},
			},
			wantSkip: 2,
		},
		{
			name: "pinned_and_infra_candidates_skipped",
			reader: &purgeClosedFakeReader{
				closed: []*types.Issue{
					closedWisp("cand-plain"),
					{ID: "cand-pinned", Status: types.StatusClosed, IssueType: types.TypeTask, Pinned: true},
					{ID: "cand-infra", Status: types.StatusClosed, IssueType: types.IssueType("rig")},
				},
				infra: map[types.IssueType]bool{types.IssueType("rig"): true},
			},
			wantKeep: []string{"cand-plain"},
		},
		{
			name: "candidate_search_keeps_its_shape",
			reader: &purgeClosedFakeReader{
				closed: []*types.Issue{closedWisp("cand-a")},
			},
			exclude:  []types.IssueType{"agent"},
			wantKeep: []string{"cand-a"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sel, err := selectClosedWispsForPurge(context.Background(), tc.reader, tc.exclude)
			if err != nil {
				t.Fatalf("selectClosedWispsForPurge: %v", err)
			}
			if got := purgeSelectionIDs(sel.Deletable); !equalIDSlices(got, tc.wantKeep) {
				t.Errorf("deletable = %v, want %v", got, tc.wantKeep)
			}
			if sel.LiveDependentSkipped != tc.wantSkip {
				t.Errorf("live_dependent_skipped = %d, want %d", sel.LiveDependentSkipped, tc.wantSkip)
			}
			if tc.name == "candidate_search_keeps_its_shape" && len(tc.reader.captured) > 0 {
				cand := tc.reader.captured[0]
				if cand.Status == nil || *cand.Status != types.StatusClosed {
					t.Errorf("candidate search status = %+v, want closed", cand.Status)
				}
				if cand.Ephemeral == nil || !*cand.Ephemeral {
					t.Error("candidate search must filter ephemeral rows")
				}
				if len(cand.ExcludeTypes) != 1 || cand.ExcludeTypes[0] != "agent" {
					t.Errorf("candidate search exclude types = %v, want [agent]", cand.ExcludeTypes)
				}
			}
		})
	}
}

func equalIDSlices(got, want []string) bool {
	if len(got) == 0 && len(want) == 0 {
		return true
	}
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestSelectClosedWispsForPurgeScansEverySource pins completeness of the
// live-source scan (GH#5152): every explicit source id must reach the status
// read, and every protected candidate must come back held. An accidentally
// page-limited scan would silently under-protect at the tail.
func TestSelectClosedWispsForPurgeScansEverySource(t *testing.T) {
	const roots = 150
	closed := make([]*types.Issue, 0, roots)
	rows := map[string]*types.Issue{}
	incoming := map[string][]*types.Dependency{}
	for i := 0; i < roots; i++ {
		candID := fmt.Sprintf("cand-%03d", i)
		srcID := fmt.Sprintf("src-%03d", i)
		closed = append(closed, closedWisp(candID))
		rows[srcID] = liveRow(srcID, types.StatusOpen)
		incoming[candID] = []*types.Dependency{dep(srcID, candID, types.DepParentChild)}
	}
	r := &purgeClosedFakeReader{
		rows:     rows,
		closed:   closed,
		incoming: incoming,
	}

	sel, err := selectClosedWispsForPurge(context.Background(), r, nil)
	if err != nil {
		t.Fatalf("selectClosedWispsForPurge: %v", err)
	}
	if len(sel.Deletable) != 0 {
		t.Errorf("deletable = %v, want none (every root has a live child)", purgeSelectionIDs(sel.Deletable))
	}
	if sel.LiveDependentSkipped != roots {
		t.Errorf("live_dependent_skipped = %d, want %d", sel.LiveDependentSkipped, roots)
	}
	if len(r.captured) < 2 {
		t.Fatalf("selector made %d searches, want a candidate scan and a source scan", len(r.captured))
	}
	src := r.captured[1]
	if len(src.IDs) != roots {
		t.Errorf("source scan carried %d ids, want all %d", len(src.IDs), roots)
	}
	if src.Limit != 0 {
		t.Errorf("source scan limit = %d, want 0 (unlimited: an explicit id list must be read complete)", src.Limit)
	}
}
