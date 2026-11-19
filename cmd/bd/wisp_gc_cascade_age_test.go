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
	// exists for a reason and the fix must not disable it. It is closed so
	// that only the cascade can reach it: without --all the parent scan passes
	// over closed wisps, so an open stale child would be collected even with
	// the cascade switched off.
	staleChild := &types.Issue{
		ID:        "gc-stale-child",
		Status:    types.StatusClosed,
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

// closedGCSelectorReader records the filter the closed-GC selector sends and
// answers with fixture rows regardless of what that filter says, so a test
// can see both halves of the age floor: the storage-side bound in the filter
// and the Go-side guard on the rows that come back. The embedded nil
// molReader makes any other call panic loudly.
type closedGCSelectorReader struct {
	molReader
	rows   []*types.Issue
	infra  map[types.IssueType]bool
	filter types.IssueFilter
}

func (r *closedGCSelectorReader) SearchIssues(_ context.Context, _ string, filter types.IssueFilter) ([]*types.Issue, error) {
	r.filter = filter
	return r.rows, nil
}

func (r *closedGCSelectorReader) IsInfraTypeCtx(_ context.Context, t types.IssueType) bool {
	return r.infra[t]
}

// TestWispGCClosedSelectorHonorsAge pins the closed-GC selection the GH#8202
// fix put behind --age (GH#8202): the age bound rides in the storage query —
// before the 5000-row scan limit, so recent rows cannot fill the window and
// starve older purgeable rows — with wispIsAbandoned as the Go-side guard for
// readers that do not enforce UpdatedBefore. A row exactly at the cutoff
// stays (the strict comparison the abandoned-wisp scan already makes), and
// zero or negative --age omits the bound entirely: the purge-all escape hatch.
// Pinned and configured infra wisps keep their protection; excluded types
// keep riding in the filter.
func TestWispGCClosedSelectorHonorsAge(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	old := wispGCNow
	t.Cleanup(func() { wispGCNow = old })
	wispGCNow = func() time.Time { return now }
	const threshold = time.Hour

	rows := []*types.Issue{
		{ID: "gc-closed-recent", Status: types.StatusClosed, Ephemeral: true, UpdatedAt: now.Add(-10 * time.Second)},
		{ID: "gc-closed-at-cutoff", Status: types.StatusClosed, Ephemeral: true, UpdatedAt: now.Add(-threshold)},
		{ID: "gc-closed-older", Status: types.StatusClosed, Ephemeral: true, UpdatedAt: now.Add(-2 * threshold)},
		{ID: "gc-closed-pinned", Status: types.StatusClosed, Ephemeral: true, UpdatedAt: now.Add(-2 * threshold), Pinned: true},
		{ID: "gc-closed-infra", Status: types.StatusClosed, Ephemeral: true, UpdatedAt: now.Add(-2 * threshold), IssueType: "agent"},
	}
	excludeTypes := []types.IssueType{"patrol"}
	collect := func(sel closedWispPurgeSelection) map[string]bool {
		out := map[string]bool{}
		for _, issue := range sel.candidates {
			out[issue.ID] = true
		}
		return out
	}

	r := &closedGCSelectorReader{
		rows:  rows,
		infra: map[types.IssueType]bool{"agent": true},
	}
	sel, err := selectClosedWispPurgeCandidates(context.Background(), r, threshold, excludeTypes)
	if err != nil {
		t.Fatalf("selectClosedWispPurgeCandidates: %v", err)
	}

	// The age bound rides in the storage query, beside the scan limit.
	if r.filter.Limit != wispPurgeScanLimit {
		t.Errorf("filter.Limit = %d, want the %d-row scan window", r.filter.Limit, wispPurgeScanLimit)
	}
	if r.filter.UpdatedBefore == nil {
		t.Fatal("filter.UpdatedBefore is nil: a positive --age must bound the scan in storage, not post-filter it")
	}
	if want := now.Add(-threshold); !r.filter.UpdatedBefore.Equal(want) {
		t.Errorf("filter.UpdatedBefore = %v, want now-age %v", r.filter.UpdatedBefore, want)
	}
	if r.filter.Status == nil || *r.filter.Status != types.StatusClosed {
		t.Errorf("filter.Status = %v, want closed", r.filter.Status)
	}
	if r.filter.Ephemeral == nil || !*r.filter.Ephemeral {
		t.Errorf("filter.Ephemeral = %v, want true", r.filter.Ephemeral)
	}
	if len(r.filter.ExcludeTypes) != 1 || r.filter.ExcludeTypes[0] != "patrol" {
		t.Errorf("filter.ExcludeTypes = %v, want the caller's excluded types to ride in the filter", r.filter.ExcludeTypes)
	}

	deleted := collect(sel)
	if deleted["gc-closed-recent"] {
		t.Errorf("recent closed wisp selected under --age %v; the Go-side guard must spare rows the reader returns past UpdatedBefore", threshold)
	}
	if deleted["gc-closed-at-cutoff"] {
		t.Errorf("wisp exactly at the cutoff selected: the strict comparison keeps it, as the abandoned-wisp scan does")
	}
	if !deleted["gc-closed-older"] {
		t.Errorf("closed wisp older than the threshold not selected")
	}
	if deleted["gc-closed-pinned"] {
		t.Errorf("pinned closed wisp selected; pinned wisps stay protected")
	}
	if deleted["gc-closed-infra"] {
		t.Errorf("configured infra wisp selected; infra types stay protected")
	}
	if sel.pinned != 1 || sel.infra != 1 {
		t.Errorf("protection counts = pinned %d, infra %d; want 1 and 1 for the route's reporting", sel.pinned, sel.infra)
	}

	// Zero and negative --age omit the bound: every otherwise eligible closed
	// wisp is selected regardless of age (the documented escape hatch).
	for _, tc := range []struct {
		name string
		age  time.Duration
	}{
		{"zero age", 0},
		{"negative age", -5 * time.Minute},
	} {
		r := &closedGCSelectorReader{rows: rows, infra: map[types.IssueType]bool{"agent": true}}
		sel, err := selectClosedWispPurgeCandidates(context.Background(), r, tc.age, nil)
		if err != nil {
			t.Fatalf("selectClosedWispPurgeCandidates(%s): %v", tc.name, err)
		}
		if r.filter.UpdatedBefore != nil {
			t.Errorf("%s: filter.UpdatedBefore = %v, want nil so every closed wisp stays eligible", tc.name, r.filter.UpdatedBefore)
		}
		deleted := collect(sel)
		if !deleted["gc-closed-recent"] || !deleted["gc-closed-at-cutoff"] || !deleted["gc-closed-older"] {
			t.Errorf("%s: selection %v; the escape hatch must select recent, at-cutoff and older rows alike", tc.name, deleted)
		}
		if deleted["gc-closed-pinned"] || deleted["gc-closed-infra"] {
			t.Errorf("%s: pinned and infra wisps must stay protected even with the age bound off", tc.name)
		}
	}
}

// TestWispGCCascadeChildAgesAtStoragePrecision holds cascade children to the
// parents' age predicate, wispIsAbandoned, at Dolt's DATETIME(0) precision
// (#7236). A step closed at 12:00:00.7 is stored as 12:00:01, so a gc at
// 12:00:00.9 reads it 100ms in the future. --age 0s must still collect it with
// its stale parent; the raw now.Sub(updated_at) <= threshold skip that #7236
// replaced for parents reads -100ms and misses it, orphaning the step once its
// parent is deleted. --age 1h must spare it at that instant and collect it two
// hours later.
//
// The step is closed because under --age 0s the parent scan already collects
// every wisp it sees that is not closed, protected or an infra type, so only a
// wisp the scan passes over, such as a closed step without --all, reaches the
// cascade's own age check.
func TestWispGCCascadeChildAgesAtStoragePrecision(t *testing.T) {
	base := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	parent := &types.Issue{
		ID:        "gc-stale-parent",
		Status:    types.StatusOpen,
		UpdatedAt: base.Add(-24 * time.Hour),
	}
	step := &types.Issue{
		ID:        "gc-closed-step",
		Status:    types.StatusClosed,
		UpdatedAt: base.Add(700 * time.Millisecond).Round(time.Second), // 12:00:01
	}
	old := wispGCNow
	t.Cleanup(func() { wispGCNow = old })

	for _, tc := range []struct {
		now       time.Time
		threshold time.Duration
		wantStep  bool
	}{
		{base.Add(900 * time.Millisecond), 0, true},
		{base.Add(900 * time.Millisecond), time.Hour, false},
		{base.Add(2 * time.Hour), time.Hour, true},
	} {
		wispGCNow = func() time.Time { return tc.now }
		r := &cascadeAgeReader{
			issues:   []*types.Issue{parent, step},
			children: map[string]bool{step.ID: true},
		}
		got, err := findAbandonedWisps(context.Background(), r, false, tc.threshold, nil)
		if err != nil {
			t.Fatalf("findAbandonedWisps(--age %s): %v", tc.threshold, err)
		}
		collected := make(map[string]bool, len(got))
		for _, issue := range got {
			collected[issue.ID] = true
		}
		if !collected[parent.ID] {
			t.Errorf("findAbandonedWisps(--age %s) at %s: stale parent %q not collected; collected=%v",
				tc.threshold, tc.now.Format("15:04:05.000"), parent.ID, collected)
		}
		if collected[step.ID] != tc.wantStep {
			t.Errorf("findAbandonedWisps(--age %s) at %s, closed step updated_at 12:00:01: collected=%v, want %v",
				tc.threshold, tc.now.Format("15:04:05.000"), collected[step.ID], tc.wantStep)
		}
	}
}
