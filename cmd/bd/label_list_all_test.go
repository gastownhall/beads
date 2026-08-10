package main

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// searchOnlyStore records the SearchIssues calls countLabelsAcrossIssues
// makes and returns issues whose Labels are already hydrated, the way
// issueops.SearchIssuesInTx hydrates them for a real store.
type searchOnlyStore struct {
	issues []*types.Issue
	err    error

	calls   int
	filters []types.IssueFilter
	queries []string
}

func (s *searchOnlyStore) SearchIssues(_ context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error) {
	s.calls++
	s.queries = append(s.queries, query)
	s.filters = append(s.filters, filter)
	if s.err != nil {
		return nil, s.err
	}
	return s.issues, nil
}

func TestCountLabelsAcrossIssues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		issues []*types.Issue
		want   map[string]int
	}{
		{
			name: "counts every hydrated label once per issue",
			issues: []*types.Issue{
				{ID: "be-1", Labels: []string{"backend", "urgent"}},
				{ID: "be-2", Labels: []string{"backend"}},
				{ID: "be-3", Labels: []string{"urgent", "docs"}},
			},
			want: map[string]int{"backend": 2, "urgent": 2, "docs": 1},
		},
		{
			name:   "issues without labels contribute nothing",
			issues: []*types.Issue{{ID: "be-1"}, {ID: "be-2", Labels: []string{}}},
			want:   map[string]int{},
		},
		{
			name:   "no issues",
			issues: nil,
			want:   map[string]int{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := &searchOnlyStore{issues: tt.issues}
			got, err := countLabelsAcrossIssues(context.Background(), store)
			if err != nil {
				t.Fatalf("countLabelsAcrossIssues: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("counts = %v, want %v", got, tt.want)
			}
			if store.calls != 1 {
				t.Errorf("SearchIssues called %d times, want exactly 1", store.calls)
			}
		})
	}
}

// TestCountLabelsAcrossIssues_SearchFilter locks the search contract the
// command depends on: an empty query and a zero IssueFilter, which means
// full-database scope, bulk label hydration (SkipLabels stays false), and
// the CONTRIBUTING.md MaxRows opt-out (this is not a --max-rows-wired
// command, so the cap must never abort it).
func TestCountLabelsAcrossIssues_SearchFilter(t *testing.T) {
	t.Parallel()

	store := &searchOnlyStore{issues: []*types.Issue{{ID: "be-1", Labels: []string{"x"}}}}
	if _, err := countLabelsAcrossIssues(context.Background(), store); err != nil {
		t.Fatalf("countLabelsAcrossIssues: %v", err)
	}
	if len(store.filters) != 1 {
		t.Fatalf("SearchIssues called %d times, want exactly 1", len(store.filters))
	}
	if store.queries[0] != "" {
		t.Errorf("query = %q, want empty (whole database)", store.queries[0])
	}
	if got := store.filters[0]; !reflect.DeepEqual(got, types.IssueFilter{}) {
		t.Errorf("filter = %+v, want the zero IssueFilter", got)
	}
}

func TestCountLabelsAcrossIssues_SearchError(t *testing.T) {
	t.Parallel()

	want := errors.New("boom")
	store := &searchOnlyStore{err: want}
	if _, err := countLabelsAcrossIssues(context.Background(), store); !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

// TestLabelListAllSearcherExposesNoPerIssueLookup is the regression gate for
// GH#5325. bd label list-all used to call store.GetLabels once per issue on
// top of a SearchIssues result that already carried hydrated labels; in
// embedded Dolt mode that is a fresh connector plus transaction per issue.
// Keeping the command's storage surface to SearchIssues alone makes that
// N+1 unreachable at compile time — widening this interface would silently
// re-open it, so the method set is asserted directly.
func TestLabelListAllSearcherExposesNoPerIssueLookup(t *testing.T) {
	t.Parallel()

	iface := reflect.TypeOf((*labelListAllSearcher)(nil)).Elem()
	for i := 0; i < iface.NumMethod(); i++ {
		if name := iface.Method(i).Name; name != "SearchIssues" {
			t.Errorf("labelListAllSearcher exposes %q; bd label list-all must read labels only from the bulk-hydrated SearchIssues result (GH#5325)", name)
		}
	}
}
