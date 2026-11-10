package issueops_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

func gateQuery(all bool, limit int) issueops.QueryRequest {
	return issueops.QueryRequest{Expression: "type=gate", IncludeClosed: all, Limit: &limit}
}

// TestListGatesAsksTheQuerierTheRawQuestion is S20: `bd gate list` used to
// build a raw SearchIssues filter (gate type, closed excluded unless --all),
// which the http backend refuses. The entry asks the Querier role the same
// question, so every route answers it through a role every server serves.
func TestListGatesAsksTheQuerierTheRawQuestion(t *testing.T) {
	cases := []struct {
		name string
		req  issueops.GateListRequest
		want issueops.QueryRequest
	}{
		{"open gates, default limit", issueops.GateListRequest{Limit: 50}, gateQuery(false, 50)},
		{"unlimited", issueops.GateListRequest{Limit: 0}, gateQuery(false, 0)},
		{"all gates", issueops.GateListRequest{All: true, Limit: 7}, gateQuery(true, 7)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := &currentQueryRecorder{responses: []currentQueryResponse{{ids: []string{"g-1", "g-2"}}}}
			got, err := issueops.ListGates(context.Background(), q, tc.req)
			if err != nil {
				t.Fatalf("ListGates: %v", err)
			}
			if len(q.calls) != 1 || !reflect.DeepEqual(q.calls[0], tc.want) {
				t.Fatalf("Query calls = %#v, want one %#v", q.calls, tc.want)
			}
			ids := make([]string, 0, len(got))
			for _, g := range got {
				ids = append(ids, g.ID)
			}
			if !reflect.DeepEqual(ids, []string{"g-1", "g-2"}) {
				t.Fatalf("gates = %v, want [g-1 g-2] in the Querier's order", ids)
			}
		})
	}
}

// TestListGatesExcludesTemplatesInTheQuery: ExcludeTemplates (bd gate check's
// listing) puts the template exclusion in the expression the Querier answers,
// so the cut at Limit counts only the gates that remain. Dropping templates
// from the returned page instead let a page of a persisted proto's gates fill
// the limit and hide every live gate behind it. Without it the listing names
// no template field: `bd gate list` lists templates.
func TestListGatesExcludesTemplatesInTheQuery(t *testing.T) {
	nonTemplate := func(all bool, limit int) issueops.QueryRequest {
		return issueops.QueryRequest{Expression: "type=gate AND template=false", IncludeClosed: all, Limit: &limit}
	}
	cases := []struct {
		name string
		req  issueops.GateListRequest
		want issueops.QueryRequest
	}{
		{"open gates, limit 1", issueops.GateListRequest{ExcludeTemplates: true, Limit: 1}, nonTemplate(false, 1)},
		{"unlimited", issueops.GateListRequest{ExcludeTemplates: true}, nonTemplate(false, 0)},
		{"all gates", issueops.GateListRequest{All: true, ExcludeTemplates: true, Limit: 7}, nonTemplate(true, 7)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := &currentQueryRecorder{responses: []currentQueryResponse{{ids: []string{"g-1"}}}}
			if _, err := issueops.ListGates(context.Background(), q, tc.req); err != nil {
				t.Fatalf("ListGates: %v", err)
			}
			if len(q.calls) != 1 || !reflect.DeepEqual(q.calls[0], tc.want) {
				t.Fatalf("Query calls = %#v, want one %#v", q.calls, tc.want)
			}
		})
	}
}

func TestListGatesNeverTakesAFailureForAnEmptyList(t *testing.T) {
	refusal := &issueops.ErrUnsupported{Op: "Querier.Query", Backend: "http"}
	q := &currentQueryRecorder{responses: []currentQueryResponse{{err: refusal}}}
	got, err := issueops.ListGates(context.Background(), q, issueops.GateListRequest{Limit: 50})
	if !errors.Is(err, refusal) || got != nil {
		t.Fatalf("ListGates = (%v, %v), want (nil, the refusal)", got, err)
	}
}

func TestListGatesDropsRowsWithoutAnIssue(t *testing.T) {
	q := gateRows{items: []*types.IssueWithCounts{nil, {}, {Issue: &types.Issue{ID: "g-1"}}}}
	got, err := issueops.ListGates(context.Background(), q, issueops.GateListRequest{})
	if err != nil || len(got) != 1 || got[0].ID != "g-1" {
		t.Fatalf("ListGates = (%v, %v), want ([g-1], nil)", got, err)
	}
}

func TestListGatesRefusesANilQuerier(t *testing.T) {
	var unsupported *issueops.ErrUnsupported
	if _, err := issueops.ListGates(context.Background(), nil, issueops.GateListRequest{}); !errors.As(err, &unsupported) {
		t.Fatalf("ListGates(nil) error = %v, want ErrUnsupported", err)
	}
}

type gateRows struct{ items []*types.IssueWithCounts }

func (g gateRows) Query(context.Context, issueops.QueryRequest) (issueops.IssuePage, error) {
	return issueops.IssuePage{Items: g.items}, nil
}
