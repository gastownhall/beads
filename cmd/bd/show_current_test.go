package main

import (
	"context"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

type fixedQuerier struct {
	ids []string
	err error
	n   int
}

func (q *fixedQuerier) Query(context.Context, issueops.QueryRequest) (issueops.IssuePage, error) {
	q.n++
	if q.err != nil {
		return issueops.IssuePage{}, q.err
	}
	items := make([]*types.IssueWithCounts, 0, len(q.ids))
	for _, id := range q.ids {
		items = append(items, &types.IssueWithCounts{Issue: &types.Issue{ID: id}})
	}
	return issueops.IssuePage{Items: items}, nil
}

// The lookup itself is issueops.FindCurrentIssue's (issueops/current_test.go).
// What the CLI owns is the fallback: last-touched answers only a lookup that
// answered and found nothing, never a failed one.

func TestResolveCurrentIssueIDFromUsesTheLookupBeforeTheFallback(t *testing.T) {
	got, err := resolveCurrentIssueIDFrom(context.Background(), &fixedQuerier{ids: []string{"in-progress"}},
		func() string { return "tester" },
		func() string { t.Fatal("fallback called after a found issue"); return "" })
	if err != nil || got != "in-progress" {
		t.Fatalf("got (%q, %v), want (in-progress, nil)", got, err)
	}
}

func TestResolveCurrentIssueIDFromFallsBackOnlyOnAMiss(t *testing.T) {
	got, err := resolveCurrentIssueIDFrom(context.Background(), &fixedQuerier{},
		func() string { return "tester" }, func() string { return "last-touched" })
	if err != nil || got != "last-touched" {
		t.Fatalf("got (%q, %v), want (last-touched, nil)", got, err)
	}
}

func TestResolveCurrentIssueIDFromNeverFallsBackOnAFailure(t *testing.T) {
	refusal := errors.New("not supported over HTTP")
	got, err := resolveCurrentIssueIDFrom(context.Background(), &fixedQuerier{err: refusal},
		func() string { return "tester" },
		func() string { t.Fatal("fallback called after a failed read"); return "" })
	if !errors.Is(err, refusal) || got != "" {
		t.Fatalf("got (%q, %v), want (\"\", an error wrapping the refusal)", got, err)
	}
}

func TestResolveCurrentIssueIDFromNilQuerierUsesFallback(t *testing.T) {
	got, err := resolveCurrentIssueIDFrom(context.Background(), nil,
		func() string { t.Fatal("actor resolved without a querier"); return "" },
		func() string { return "last-touched" })
	if err != nil || got != "last-touched" {
		t.Fatalf("got (%q, %v), want (last-touched, nil)", got, err)
	}
}
