package workapi

import (
	"errors"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

func queryRequest(expression string, limit int) issueops.QueryRequest {
	return issueops.QueryRequest{Expression: expression, Limit: &limit}
}

// TestBuildQueryPlanLeavesAPredicateQueryUNBOUNDED is the unit-level pin on the
// defect this role shipped to fix. Both front doors set the row limit to
// max(3*Limit, 100) and filtered what came back, so an OR query over a workspace
// with more than a hundred candidate rows returned a prefix of its answer and
// reported it as whole.
//
// A predicate query's filter must carry NO row limit. The predicate rejects an
// unknown fraction of the rows the database returns, so any bound on the fetch
// is a bound on the matches with no relationship to the page the caller asked
// for.
func TestBuildQueryPlanLeavesAPredicateQueryUNBOUNDED(t *testing.T) {
	for _, expression := range []string{
		"type=bug OR type=feature",
		"NOT priority>2",
		"(status=open OR status=blocked) AND priority<2",
		"type=bug OR (label=urgent AND assignee=none)",
	} {
		t.Run(expression, func(t *testing.T) {
			plan, err := BuildQueryPlan(queryRequest(expression, 5))
			if err != nil {
				t.Fatalf("BuildQueryPlan(%q): %v", expression, err)
			}
			if !plan.RequiresPredicate() {
				t.Fatalf("%q was planned without a predicate; this case no longer covers the shape it exists for", expression)
			}
			if plan.Filter.Limit != 0 {
				t.Errorf("predicate query's filter carries Limit=%d; a bounded fetch drops matches the predicate never saw",
					plan.Filter.Limit)
			}
			if plan.Limit != 5 {
				t.Errorf("plan.Limit = %d, want the 5 the caller asked to receive", plan.Limit)
			}
		})
	}
}

// TestBuildQueryPlanPushesTheLimitDownWhenTheFilterIsExact is the other half:
// an expression the storage filter expresses exactly is bounded by the
// database, because there is no predicate to hide rows from it.
func TestBuildQueryPlanPushesTheLimitDownWhenTheFilterIsExact(t *testing.T) {
	for _, expression := range []string{
		"status=open",
		"status=open AND priority<=2",
		"NOT status=closed",
		"label=frontend OR label=backend",
	} {
		t.Run(expression, func(t *testing.T) {
			plan, err := BuildQueryPlan(queryRequest(expression, 7))
			if err != nil {
				t.Fatalf("BuildQueryPlan(%q): %v", expression, err)
			}
			if plan.RequiresPredicate() {
				t.Fatalf("%q was planned WITH a predicate; this case no longer covers the shape it exists for", expression)
			}
			if plan.Filter.Limit != 7 {
				t.Errorf("filter.Limit = %d, want the page limit pushed into the query", plan.Filter.Limit)
			}
		})
	}
}

// TestBuildQueryPlanLimitDefaulting pins the pointer's three states, which is
// what lets one constant serve both surfaces.
func TestBuildQueryPlanLimitDefaulting(t *testing.T) {
	plan, err := BuildQueryPlan(issueops.QueryRequest{Expression: "status=open"})
	if err != nil {
		t.Fatalf("BuildQueryPlan: %v", err)
	}
	if plan.Limit != DefaultQueryLimit {
		t.Errorf("unset Limit = %d, want the shared default %d", plan.Limit, DefaultQueryLimit)
	}

	plan, err = BuildQueryPlan(queryRequest("status=open", 0))
	if err != nil {
		t.Fatalf("BuildQueryPlan: %v", err)
	}
	if plan.Limit != 0 || plan.Filter.Limit != 0 {
		t.Errorf("explicit 0 = plan %d / filter %d, want unlimited on both", plan.Limit, plan.Filter.Limit)
	}

	if _, err := BuildQueryPlan(queryRequest("status=open", -1)); !errors.Is(err, issueops.ErrValidation) {
		t.Errorf("negative Limit error = %v, want ErrValidation rather than a second spelling of unlimited", err)
	}
}

// TestBuildQueryPlanExcludesClosedUnlessTheExpressionSaysOtherwise pins the
// conditional default (issueops/querier.go:29-41): an expression with an
// opinion about status keeps it, and only one without gets the exclusion.
func TestBuildQueryPlanExcludesClosedUnlessTheExpressionSaysOtherwise(t *testing.T) {
	for _, test := range []struct {
		expression    string
		includeClosed bool
		wantExcluded  bool
	}{
		{"priority=1", false, true},
		{"priority=1", true, false},
		{"status=closed", false, false},
		{"NOT status=open", false, false},
		{"type=bug OR status=closed", false, false},
		{"type=bug OR type=feature", false, true},
	} {
		t.Run(test.expression, func(t *testing.T) {
			plan, err := BuildQueryPlan(issueops.QueryRequest{
				Expression: test.expression, IncludeClosed: test.includeClosed,
			})
			if err != nil {
				t.Fatalf("BuildQueryPlan(%q): %v", test.expression, err)
			}
			excluded := slices.Contains(plan.Filter.ExcludeStatus, types.StatusClosed)
			if excluded != test.wantExcluded {
				t.Errorf("closed excluded = %v, want %v (ExcludeStatus=%v, Status=%v)",
					excluded, test.wantExcluded, plan.Filter.ExcludeStatus, plan.Filter.Status)
			}
		})
	}
}

// TestBuildQueryPlanRefusals pins every deterministic refusal, each of which a
// front door would otherwise make for itself — which is how the two routes came
// to disagree about what an offset means.
func TestBuildQueryPlanRefusals(t *testing.T) {
	for _, test := range []struct {
		name string
		req  issueops.QueryRequest
	}{
		{"blank expression", issueops.QueryRequest{}},
		{"whitespace expression", issueops.QueryRequest{Expression: "   "}},
		{"unparseable expression", issueops.QueryRequest{Expression: "===invalid==="}},
		{"unknown field", issueops.QueryRequest{Expression: "nosuchfield=1"}},
		{"negative offset", issueops.QueryRequest{Expression: "status=open", Offset: -1}},
		{"offset with a display order", issueops.QueryRequest{Expression: "status=open", Offset: 1, SortBy: "priority"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := BuildQueryPlan(test.req); !errors.Is(err, issueops.ErrValidation) {
				t.Errorf("error = %v, want ErrValidation", err)
			}
		})
	}
}

// TestAQueryExpressionRefusalNamesItselfForTheWire pins the message prefix
// internal/httpapi matches to turn an unparseable expression into a 400 on `q`
// rather than a 500, written down here so a reword fails beside the sentence it
// rewords.
func TestAQueryExpressionRefusalNamesItselfForTheWire(t *testing.T) {
	_, err := BuildQueryPlan(issueops.QueryRequest{Expression: "===invalid==="})
	if err == nil {
		t.Fatal("BuildQueryPlan accepted an unparseable expression")
	}
	const prefix = "invalid query expression"
	if got := err.Error(); len(got) < len(prefix) || got[:len(prefix)] != prefix {
		t.Errorf("error = %q, want it to start with %q (internal/httpapi maps that prefix to param `q`)", got, prefix)
	}
}

// TestApplyQueryPredicateDropsRowsWithNoIssue pins the one thing the shared
// filter does beyond calling the predicate: a row carrying no issue is dropped
// rather than handed to a function that reads issue fields.
func TestApplyQueryPredicateDropsRowsWithNoIssue(t *testing.T) {
	rows := []*types.IssueWithCounts{
		{Issue: &types.Issue{ID: "bd-1"}},
		nil,
		{},
		{Issue: &types.Issue{ID: "bd-2"}},
	}
	got := ApplyQueryPredicate(rows, func(i *types.Issue) bool { return i.ID != "bd-1" })
	if len(got) != 1 || got[0].Issue.ID != "bd-2" {
		t.Fatalf("kept %d rows, want only bd-2", len(got))
	}
	if same := ApplyQueryPredicate(rows, nil); len(same) != len(rows) {
		t.Errorf("a nil predicate filtered %d rows down to %d; it must be the identity", len(rows), len(same))
	}
}

// TestSkipRows pins the Offset half of a predicate query: it skips MATCHES, and
// skipping past the end is an empty page rather than a nil one.
func TestSkipRows(t *testing.T) {
	rows := []*types.IssueWithCounts{
		{Issue: &types.Issue{ID: "bd-1"}},
		{Issue: &types.Issue{ID: "bd-2"}},
		{Issue: &types.Issue{ID: "bd-3"}},
	}
	if got := SkipRows(rows, 0); len(got) != 3 {
		t.Errorf("SkipRows(0) returned %d rows, want all three", len(got))
	}
	if got := SkipRows(rows, 2); len(got) != 1 || got[0].Issue.ID != "bd-3" {
		t.Errorf("SkipRows(2) = %v, want bd-3 alone", got)
	}
	got := SkipRows(rows, 9)
	if got == nil || len(got) != 0 {
		t.Errorf("SkipRows past the end = %v, want an empty non-nil page", got)
	}
}
