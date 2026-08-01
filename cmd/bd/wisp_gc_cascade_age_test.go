package main

import (
	"context"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// cascadeAgeReader is a minimal molReader covering only the calls
// findAbandonedWisps makes. The embedded nil interface makes any other call
// panic loudly rather than return a silent zero value.
type cascadeAgeReader struct {
	molReader
	issues   []*types.Issue
	children map[string]bool
}

func (r *cascadeAgeReader) SearchIssues(context.Context, string, types.IssueFilter) ([]*types.Issue, error) {
	return r.issues, nil
}

func (r *cascadeAgeReader) GetBlockedIssues(context.Context, types.WorkFilter) ([]*types.BlockedIssue, error) {
	return nil, nil
}

func (r *cascadeAgeReader) GetCustomStatusesDetailed(context.Context) ([]types.CustomStatus, error) {
	return nil, nil
}

func (r *cascadeAgeReader) IsInfraTypeCtx(context.Context, types.IssueType) bool { return false }

func (r *cascadeAgeReader) FindWispDependentsRecursive(context.Context, []string) (map[string]bool, error) {
	return r.children, nil
}

func (r *cascadeAgeReader) GetIssuesByIDs(_ context.Context, ids []string) ([]*types.Issue, error) {
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	var out []*types.Issue
	for _, issue := range r.issues {
		if want[issue.ID] {
			out = append(out, issue)
		}
	}
	return out, nil
}

// TestWispGCCascadeRespectsAge is the regression test for hq-k27y.
//
// `bd mol wisp gc --age` selected abandoned parents by age and then pulled in
// their transitive wisp dependents with no age test at all. A step created
// seconds ago was therefore destroyed because something it depended on was old.
//
// The consequence was not a stray bead: a patrol molecule slung moments earlier
// went down with all nine of its steps — 43 beads in one run — and the agent's
// hook emptied instantly. Two rigs of three were hit; the third survived only
// because a stop order reached it first.
//
// It also made the flag misleading in a way that punishes the obvious reaction:
// raising --age does not make the command safer, because the child's own age is
// never consulted. That is asserted separately below.
//
// GH#4430 already protects a step that has been picked up (in_progress/blocked/
// hooked map to CategoryWIP). A freshly created step is still `open`, and open
// maps to CategoryActive, which that guard deliberately does not protect — so
// the fresh-child path stayed open until this check.
func TestWispGCCascadeRespectsAge(t *testing.T) {
	now := time.Now()
	const threshold = time.Hour

	staleParent := &types.Issue{
		ID:        "gc-stale-parent",
		Status:    types.StatusOpen,
		UpdatedAt: now.Add(-24 * time.Hour),
	}
	// The molecule the patrol is working right now: created seconds ago,
	// not yet picked up, so still open.
	freshChild := &types.Issue{
		ID:        "gc-fresh-child",
		Status:    types.StatusOpen,
		UpdatedAt: now.Add(-2 * time.Second),
	}
	// A child that really is abandoned must still be collected — the cascade
	// exists for a reason and the fix must not disable it.
	staleChild := &types.Issue{
		ID:        "gc-stale-child",
		Status:    types.StatusOpen,
		UpdatedAt: now.Add(-48 * time.Hour),
	}

	r := &cascadeAgeReader{
		issues:   []*types.Issue{staleParent, freshChild, staleChild},
		children: map[string]bool{"gc-fresh-child": true, "gc-stale-child": true},
	}

	got, err := findAbandonedWisps(context.Background(), r, false, threshold, nil)
	if err != nil {
		t.Fatalf("findAbandonedWisps: %v", err)
	}

	collected := make(map[string]bool, len(got))
	for _, issue := range got {
		collected[issue.ID] = true
	}

	if collected["gc-fresh-child"] {
		t.Errorf("fresh child %q was collected: a wisp updated %v ago must survive --age %v regardless of its parent's age (hq-k27y); collected=%v",
			freshChild.ID, now.Sub(freshChild.UpdatedAt), threshold, collected)
	}
	if !collected["gc-stale-parent"] {
		t.Errorf("stale parent %q must still be collected; collected=%v", staleParent.ID, collected)
	}
	if !collected["gc-stale-child"] {
		t.Errorf("stale child %q must still be collected — the cascade must keep working for genuinely abandoned children; collected=%v", staleChild.ID, collected)
	}
}

// TestWispGCRaisingAgeDoesNotSaveFreshChild pins the property that made the
// defect worse than it looked: before the fix, no value of --age protected a
// fresh child, so the natural mitigation ("use a bigger threshold") did
// nothing. After the fix a larger threshold is strictly more protective.
func TestWispGCRaisingAgeDoesNotSaveFreshChild(t *testing.T) {
	now := time.Now()

	for _, threshold := range []time.Duration{time.Hour, 24 * time.Hour, 720 * time.Hour} {
		parent := &types.Issue{
			ID:        "gc-ancient-parent",
			Status:    types.StatusOpen,
			UpdatedAt: now.Add(-2 * threshold),
		}
		child := &types.Issue{
			ID:        "gc-newborn",
			Status:    types.StatusOpen,
			UpdatedAt: now.Add(-time.Second),
		}
		r := &cascadeAgeReader{
			issues:   []*types.Issue{parent, child},
			children: map[string]bool{"gc-newborn": true},
		}

		got, err := findAbandonedWisps(context.Background(), r, false, threshold, nil)
		if err != nil {
			t.Fatalf("findAbandonedWisps(--age %v): %v", threshold, err)
		}
		for _, issue := range got {
			if issue.ID == "gc-newborn" {
				t.Errorf("--age %v still collected a wisp created 1s ago; the threshold must apply to cascade children", threshold)
			}
		}
	}
}
