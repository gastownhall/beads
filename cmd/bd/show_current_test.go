package main

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

type currentIssueSearchCall struct {
	query  string
	filter types.IssueFilter
}

type currentIssueSearchResponse struct {
	issues []*types.Issue
	err    error
}

type currentIssueSearchRecorder struct {
	responses []currentIssueSearchResponse
	calls     []currentIssueSearchCall
}

func (r *currentIssueSearchRecorder) SearchIssues(_ context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error) {
	r.calls = append(r.calls, currentIssueSearchCall{query: query, filter: filter})
	response := r.responses[len(r.calls)-1]
	return response.issues, response.err
}

func TestResolveCurrentIssueIDFromInProgressShortCircuits(t *testing.T) {
	searcher := &currentIssueSearchRecorder{
		responses: []currentIssueSearchResponse{{
			issues: []*types.Issue{{ID: "in-progress-first"}, {ID: "in-progress-second"}},
		}},
	}
	actorCalls := 0

	got := resolveCurrentIssueIDFrom(context.Background(), searcher, func() string {
		actorCalls++
		return "tester"
	}, func() string {
		t.Fatal("fallback called after in-progress result")
		return ""
	})

	if got != "in-progress-first" {
		t.Fatalf("resolveCurrentIssueIDFrom() = %q, want %q", got, "in-progress-first")
	}
	if actorCalls != 1 {
		t.Fatalf("actor calls = %d, want 1", actorCalls)
	}
	assertCurrentIssueSearchCalls(t, searcher.calls, "tester", types.StatusInProgress)
}

func TestResolveCurrentIssueIDFromFindsHookedAfterInProgressMiss(t *testing.T) {
	searcher := &currentIssueSearchRecorder{
		responses: []currentIssueSearchResponse{
			{err: errors.New("in-progress search failed")},
			{issues: []*types.Issue{{ID: "hooked"}}},
		},
	}

	got := resolveCurrentIssueIDFrom(context.Background(), searcher, func() string {
		return "tester"
	}, func() string {
		t.Fatal("fallback called after hooked result")
		return ""
	})

	if got != "hooked" {
		t.Fatalf("resolveCurrentIssueIDFrom() = %q, want %q", got, "hooked")
	}
	assertCurrentIssueSearchCalls(t, searcher.calls, "tester", types.StatusInProgress, types.StatusHooked)
}

func TestResolveCurrentIssueIDFromFallsBackAfterTwoMisses(t *testing.T) {
	searcher := &currentIssueSearchRecorder{
		responses: []currentIssueSearchResponse{{}, {}},
	}
	fallbackCalls := 0

	got := resolveCurrentIssueIDFrom(context.Background(), searcher, func() string {
		return "tester"
	}, func() string {
		fallbackCalls++
		return "last-touched"
	})

	if got != "last-touched" {
		t.Fatalf("resolveCurrentIssueIDFrom() = %q, want %q", got, "last-touched")
	}
	if fallbackCalls != 1 {
		t.Fatalf("fallback calls = %d, want 1", fallbackCalls)
	}
	assertCurrentIssueSearchCalls(t, searcher.calls, "tester", types.StatusInProgress, types.StatusHooked)
}

func TestResolveCurrentIssueIDFromNilSearcherUsesFallback(t *testing.T) {
	actorCalls := 0
	fallbackCalls := 0

	got := resolveCurrentIssueIDFrom(context.Background(), nil, func() string {
		actorCalls++
		return "tester"
	}, func() string {
		fallbackCalls++
		return "last-touched"
	})

	if got != "last-touched" {
		t.Fatalf("resolveCurrentIssueIDFrom() = %q, want %q", got, "last-touched")
	}
	if actorCalls != 0 {
		t.Fatalf("actor calls = %d, want 0", actorCalls)
	}
	if fallbackCalls != 1 {
		t.Fatalf("fallback calls = %d, want 1", fallbackCalls)
	}
}

func assertCurrentIssueSearchCalls(t *testing.T, got []currentIssueSearchCall, actor string, statuses ...types.Status) {
	t.Helper()

	want := make([]currentIssueSearchCall, 0, len(statuses))
	for _, status := range statuses {
		status := status
		want = append(want, currentIssueSearchCall{
			query: "",
			filter: types.IssueFilter{
				Status:   &status,
				Assignee: &actor,
			},
		})
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SearchIssues calls = %#v, want %#v", got, want)
	}
}
