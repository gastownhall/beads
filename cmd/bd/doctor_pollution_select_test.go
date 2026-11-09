package main

// Pure unit tests for the pollution cleanup's link guard (AC-7, GH#5025):
// selectDeletablePollution runs against a fake edge reader — no database
// involved. The behavioral contract (a linked high-confidence hit survives
// --clean with its edges intact) is pinned by the cgo Dolt tests in
// doctor_pollution_clean_test.go.

import (
	"context"
	"errors"
	"testing"

	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/internal/types"
)

// fakePollutionEdgeReader is the seam double: canned edges, or a failing
// lookup.
type fakePollutionEdgeReader struct {
	outbound map[string][]*types.Dependency
	inbound  map[string][]*types.Dependency
	outErr   error
	inErr    error
}

func (f *fakePollutionEdgeReader) GetDependencyRecordsForIssues(ctx context.Context, issueIDs []string) (map[string][]*types.Dependency, error) {
	if f.outErr != nil {
		return nil, f.outErr
	}
	return f.outbound, nil
}

func (f *fakePollutionEdgeReader) GetDependentRecordsForIssues(ctx context.Context, targetIDs []string) (map[string][]*types.Dependency, error) {
	if f.inErr != nil {
		return nil, f.inErr
	}
	return f.inbound, nil
}

// pollutionHit builds one high-confidence-shaped result for the guard tests.
func pollutionHit(id string) doctor.PollutionResult {
	return doctor.PollutionResult{
		Issue:   &types.Issue{ID: id, Title: "Test issue", Status: types.StatusOpen, IssueType: types.TypeTask},
		Score:   1.4,
		Reasons: []string{"Title starts with test prefix"},
	}
}

func pollutionIDs(hits []doctor.PollutionResult) []string {
	ids := make([]string, 0, len(hits))
	for _, p := range hits {
		ids = append(ids, p.Issue.ID)
	}
	return ids
}

// TestSelectDeletablePollution_LinkedHitsSkipped pins the guard: a hit with
// any edge — outbound or inbound — is skipped, not deleted, and the clean
// ones keep their input order.
func TestSelectDeletablePollution_LinkedHitsSkipped(t *testing.T) {
	ctx := context.Background()
	reader := &fakePollutionEdgeReader{
		outbound: map[string][]*types.Dependency{
			"bd-out": {{IssueID: "bd-out", DependsOnID: "bd-victim"}},
		},
		inbound: map[string][]*types.Dependency{
			"bd-in": {{IssueID: "bd-dep", DependsOnID: "bd-in"}},
		},
	}
	hits := []doctor.PollutionResult{pollutionHit("bd-out"), pollutionHit("bd-clean"), pollutionHit("bd-in")}

	deletable, linked, err := selectDeletablePollution(ctx, reader, hits)
	if err != nil {
		t.Fatalf("selectDeletablePollution: %v", err)
	}
	if got, want := pollutionIDs(deletable), []string{"bd-clean"}; len(got) != 1 || got[0] != want[0] {
		t.Errorf("deletable = %v, want %v", got, want)
	}
	if got, want := pollutionIDs(linked), []string{"bd-out", "bd-in"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("linked = %v, want %v (input order)", got, want)
	}
}

// TestSelectDeletablePollution_NoHits pins the empty contract: no hits means
// no lookups, no error, no selection.
func TestSelectDeletablePollution_NoHits(t *testing.T) {
	deletable, linked, err := selectDeletablePollution(context.Background(), &fakePollutionEdgeReader{}, nil)
	if err != nil {
		t.Fatalf("selectDeletablePollution: %v", err)
	}
	if deletable != nil || linked != nil {
		t.Errorf("deletable=%v linked=%v, want both nil", deletable, linked)
	}
}

// TestSelectDeletablePollution_LookupErrorAborts pins the abort contract: a
// failing edge lookup yields no selection at all — the caller must abort
// before any prompt, backup or delete rather than guess which hits are
// linked.
func TestSelectDeletablePollution_LookupErrorAborts(t *testing.T) {
	ctx := context.Background()
	hits := []doctor.PollutionResult{pollutionHit("bd-high")}

	for _, tc := range []struct {
		name   string
		reader *fakePollutionEdgeReader
		want   string
	}{
		{"outbound lookup fails", &fakePollutionEdgeReader{outErr: errors.New("connection refused")}, "outbound"},
		{"inbound lookup fails", &fakePollutionEdgeReader{inErr: errors.New("connection refused")}, "inbound"},
	} {
		deletable, linked, err := selectDeletablePollution(ctx, tc.reader, hits)
		if err == nil {
			t.Fatalf("%s: no error returned", tc.name)
		}
		if !errors.Is(err, tc.reader.outErr) && !errors.Is(err, tc.reader.inErr) {
			t.Errorf("%s: error %v does not wrap the lookup failure", tc.name, err)
		}
		if deletable != nil || linked != nil {
			t.Errorf("%s: returned deletable=%v linked=%v, want no selection", tc.name, deletable, linked)
		}
	}
}
