package main

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// recordingGateQuerier records every Query and answers with fixed rows.
type recordingGateQuerier struct {
	calls []issueops.QueryRequest
	items []*types.IssueWithCounts
	err   error
}

func (q *recordingGateQuerier) Query(_ context.Context, req issueops.QueryRequest) (issueops.IssuePage, error) {
	q.calls = append(q.calls, req)
	if q.err != nil {
		return issueops.IssuePage{}, q.err
	}
	return issueops.IssuePage{Items: q.items}, nil
}

// TestGateCheckListsOpenGatesThroughListGates pins the gate check listing to
// the Querier role (queryIssues on a remote backend) instead of the raw
// SearchIssues filter the http store refused before reading a gate: the gate
// type, closed excluded, templates excluded in the query itself (so a
// persisted proto's gates take no row of --limit), the --limit carried as
// given (0 = unlimited).
func TestGateCheckListsOpenGatesThroughListGates(t *testing.T) {
	for _, limit := range []int{0, 5, 100} {
		q := &recordingGateQuerier{items: []*types.IssueWithCounts{
			{Issue: &types.Issue{ID: "g-timer", IssueType: "gate", AwaitType: "timer"}},
			{Issue: &types.Issue{ID: "g-pr", IssueType: "gate", AwaitType: "gh:pr"}},
		}}
		got, err := listOpenGatesWith(context.Background(), q, limit)
		if err != nil {
			t.Fatalf("limit %d: listOpenGatesWith: %v", limit, err)
		}
		l := limit
		want := issueops.QueryRequest{Expression: "type=gate AND template=false", IncludeClosed: false, Limit: &l}
		if len(q.calls) != 1 || !reflect.DeepEqual(q.calls[0], want) {
			t.Fatalf("limit %d: Query calls = %#v, want one %#v", limit, q.calls, want)
		}
		if len(got) != 2 || got[0].ID != "g-timer" || got[1].ID != "g-pr" {
			t.Fatalf("limit %d: gates = %v, want [g-timer g-pr] in the Querier's order", limit, got)
		}
		// The per-type selection still happens after the listing.
		timers := filterCheckableGates(got, "timer")
		if len(timers) != 1 || timers[0].ID != "g-timer" {
			t.Fatalf("limit %d: timer gates = %v, want [g-timer]", limit, timers)
		}
		gh := filterCheckableGates(got, "gh")
		if len(gh) != 1 || gh[0].ID != "g-pr" {
			t.Fatalf("limit %d: gh gates = %v, want [g-pr]", limit, gh)
		}
	}
}

// TestGateCheckListingFailureIsAnError keeps a refused or failed listing an
// error: gate-sweep must fail loudly, never report "No open gates found."
func TestGateCheckListingFailureIsAnError(t *testing.T) {
	refusal := &issueops.ErrUnsupported{Op: "Querier.Query", Backend: "http"}
	q := &recordingGateQuerier{err: refusal}
	got, err := listOpenGatesWith(context.Background(), q, 0)
	if !errors.Is(err, refusal) || got != nil {
		t.Fatalf("listOpenGatesWith = (%v, %v), want (nil, the refusal)", got, err)
	}
}

// TestGateCheckTimerArmOverListedGates runs the evaluation over the listed
// rows: an expired timer gate resolves, an unexpired one stays pending, and a
// gate with no await type is not evaluated (the corpus seed's case).
func TestGateCheckTimerArmOverListedGates(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	q := &recordingGateQuerier{items: []*types.IssueWithCounts{
		{Issue: &types.Issue{ID: "g-expired", IssueType: "gate", AwaitType: "timer", CreatedAt: now.Add(-2 * time.Hour), Timeout: time.Hour}},
		{Issue: &types.Issue{ID: "g-pending", IssueType: "gate", AwaitType: "timer", CreatedAt: now.Add(-time.Minute), Timeout: time.Hour}},
		{Issue: &types.Issue{ID: "g-untyped", IssueType: "gate"}},
	}}
	gates, err := listOpenGatesWith(context.Background(), q, 0)
	if err != nil {
		t.Fatalf("listOpenGatesWith: %v", err)
	}
	results := evaluateGates(context.Background(), filterCheckableGates(gates, ""), now, nil, nil, nil)
	got := map[string]bool{}
	for _, r := range results {
		if r.err != nil {
			t.Fatalf("%s: unexpected error %v", r.gate.ID, r.err)
		}
		got[r.gate.ID] = r.resolved
	}
	want := map[string]bool{"g-expired": true, "g-pending": false}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("evaluated = %v, want %v (g-untyped skipped)", got, want)
	}
}
