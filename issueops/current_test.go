package issueops_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
	"github.com/steveyegge/beads/issueops"
)

type currentQueryResponse struct {
	ids []string
	err error
}

type currentQueryRecorder struct {
	responses []currentQueryResponse
	calls     []issueops.QueryRequest
}

func (r *currentQueryRecorder) Query(_ context.Context, req issueops.QueryRequest) (issueops.IssuePage, error) {
	r.calls = append(r.calls, req)
	response := r.responses[len(r.calls)-1]
	if response.err != nil {
		return issueops.IssuePage{}, response.err
	}
	items := make([]*types.IssueWithCounts, 0, len(response.ids))
	for _, id := range response.ids {
		items = append(items, &types.IssueWithCounts{Issue: &types.Issue{ID: id}})
	}
	return issueops.IssuePage{Items: items}, nil
}

func wantCurrentQueries(quotedActor string, statuses ...types.Status) []issueops.QueryRequest {
	one := 1
	want := make([]issueops.QueryRequest, 0, len(statuses))
	for _, status := range statuses {
		want = append(want, issueops.QueryRequest{
			Expression: "status=" + string(status) + " AND assignee=" + quotedActor,
			Limit:      &one,
		})
	}
	return want
}

func TestFindCurrentIssueInProgressShortCircuits(t *testing.T) {
	q := &currentQueryRecorder{responses: []currentQueryResponse{{ids: []string{"in-progress-first", "in-progress-second"}}}}
	got, err := issueops.FindCurrentIssue(context.Background(), q, "tester")
	if err != nil || got != "in-progress-first" {
		t.Fatalf("FindCurrentIssue = (%q, %v), want (in-progress-first, nil)", got, err)
	}
	if want := wantCurrentQueries(`"tester"`, types.StatusInProgress); !reflect.DeepEqual(q.calls, want) {
		t.Fatalf("Query calls = %#v, want %#v", q.calls, want)
	}
}

func TestFindCurrentIssueFindsHookedAfterInProgressMiss(t *testing.T) {
	q := &currentQueryRecorder{responses: []currentQueryResponse{{}, {ids: []string{"hooked"}}}}
	got, err := issueops.FindCurrentIssue(context.Background(), q, "tester")
	if err != nil || got != "hooked" {
		t.Fatalf("FindCurrentIssue = (%q, %v), want (hooked, nil)", got, err)
	}
	if want := wantCurrentQueries(`"tester"`, types.StatusInProgress, types.StatusHooked); !reflect.DeepEqual(q.calls, want) {
		t.Fatalf("Query calls = %#v, want %#v", q.calls, want)
	}
}

func TestFindCurrentIssueAnswersEmptyAfterTwoMisses(t *testing.T) {
	q := &currentQueryRecorder{responses: []currentQueryResponse{{}, {}}}
	got, err := issueops.FindCurrentIssue(context.Background(), q, "tester")
	if err != nil || got != "" {
		t.Fatalf("FindCurrentIssue = (%q, %v), want (\"\", nil)", got, err)
	}
}

// TestFindCurrentIssueNeverTakesAFailureForAMiss is S6e: a refused or failed
// read used to fall through to the next read and then to the last-touched
// issue, so `bd show --current` printed the WRONG issue, exit 0.
func TestFindCurrentIssueNeverTakesAFailureForAMiss(t *testing.T) {
	refusal := errors.New("SearchIssues: not supported over HTTP")
	for _, tc := range []struct {
		name      string
		responses []currentQueryResponse
		calls     int
	}{
		{"in-progress read fails", []currentQueryResponse{{err: refusal}}, 1},
		{"hooked read fails", []currentQueryResponse{{}, {err: refusal}}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := &currentQueryRecorder{responses: tc.responses}
			got, err := issueops.FindCurrentIssue(context.Background(), q, "tester")
			if !errors.Is(err, refusal) || got != "" {
				t.Fatalf("FindCurrentIssue = (%q, %v), want (\"\", an error wrapping %v)", got, err, refusal)
			}
			if len(q.calls) != tc.calls {
				t.Fatalf("query calls = %d, want %d", len(q.calls), tc.calls)
			}
		})
	}
}

// TestFindCurrentIssueQuotesTheActor pins that an actor is one quoted value,
// whatever it contains, so it can neither break the parse nor splice in a
// clause.
func TestFindCurrentIssueQuotesTheActor(t *testing.T) {
	q := &currentQueryRecorder{responses: []currentQueryResponse{{ids: []string{"x"}}}}
	if _, err := issueops.FindCurrentIssue(context.Background(), q, `gastown/polecats/ru"st\ AND status=closed`); err != nil {
		t.Fatal(err)
	}
	if want := wantCurrentQueries(`"gastown/polecats/ru\"st\\ AND status=closed"`, types.StatusInProgress); !reflect.DeepEqual(q.calls, want) {
		t.Fatalf("Query calls = %#v, want %#v", q.calls, want)
	}
}

// TestFindCurrentIssueSkipsActorsItCannotAskAbout pins the actors the query
// language cannot express: `assignee=none`/`null` mean "no assignee", which
// would name some unassigned issue as this actor's.
func TestFindCurrentIssueSkipsActorsItCannotAskAbout(t *testing.T) {
	for _, actor := range []string{"", "none", "NULL"} {
		q := &currentQueryRecorder{}
		got, err := issueops.FindCurrentIssue(context.Background(), q, actor)
		if err != nil || got != "" || len(q.calls) != 0 {
			t.Fatalf("actor %q: got (%q, %v) after %d queries, want (\"\", nil) after none", actor, got, err, len(q.calls))
		}
	}
}

// TestFindCurrentIssueQueryMeansTheRawFilter pins that each read evaluates to
// exactly `status=<s>`, `assignee=<actor>` — no predicate half, no default
// closed exclusion, no sort — so the store answers it with the same rows in
// the same default order the raw read did.
func TestFindCurrentIssueQueryMeansTheRawFilter(t *testing.T) {
	for _, actor := range []string{"tester", "gastown/polecats/rust", `we"ird\name`, "has space"} {
		q := &currentQueryRecorder{responses: []currentQueryResponse{{}, {}}}
		if _, err := issueops.FindCurrentIssue(context.Background(), q, actor); err != nil {
			t.Fatal(err)
		}
		for i, req := range q.calls {
			plan, err := workapi.BuildQueryPlan(req)
			if err != nil {
				t.Fatalf("actor %q read %d: %v", actor, i, err)
			}
			f := plan.Filter
			status := issueops.CurrentIssueStatuses[i]
			if plan.RequiresPredicate() || f.Status == nil || *f.Status != status || f.Assignee == nil || *f.Assignee != actor ||
				f.NoAssignee || len(f.ExcludeStatus) != 0 || f.Limit != 1 || f.SortBy != "" {
				t.Fatalf("actor %q status %s: plan = %+v", actor, status, plan)
			}
		}
	}
}
