package query

import (
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// no_history is queryable like ephemeral: an AND query becomes a filter the
// database answers, and an OR query becomes a predicate over the row flag.
func TestNoHistoryField(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	eval := NewEvaluator(now)

	for _, tc := range []struct {
		query string
		want  bool
	}{
		{"no_history=true AND status=closed", true},
		{"no_history=false", false},
	} {
		node, err := Parse(tc.query)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tc.query, err)
		}
		result, err := eval.Evaluate(node)
		if err != nil {
			t.Fatalf("Evaluate(%q): %v", tc.query, err)
		}
		if result.RequiresPredicate {
			t.Fatalf("%q needs a predicate; want a database filter", tc.query)
		}
		if result.Filter.NoHistory == nil || *result.Filter.NoHistory != tc.want {
			t.Fatalf("%q: Filter.NoHistory = %v, want %v", tc.query, result.Filter.NoHistory, tc.want)
		}
	}

	node, err := Parse("no_history=true OR ephemeral=true")
	if err != nil {
		t.Fatal(err)
	}
	result, err := eval.Evaluate(node)
	if err != nil {
		t.Fatal(err)
	}
	if !result.RequiresPredicate {
		t.Fatal("an OR query should need a predicate")
	}
	for _, tc := range []struct {
		issue types.Issue
		want  bool
	}{
		{types.Issue{ID: "x-nh", NoHistory: true}, true},
		{types.Issue{ID: "x-eph", Ephemeral: true}, true},
		{types.Issue{ID: "x-plain"}, false},
	} {
		if got := result.Predicate(&tc.issue); got != tc.want {
			t.Errorf("predicate(%s) = %v, want %v", tc.issue.ID, got, tc.want)
		}
	}

	if _, err := eval.Evaluate(mustParse(t, "no_history=maybe")); err == nil {
		t.Fatal("no_history=maybe should be refused")
	}
}

func mustParse(t *testing.T, q string) Node {
	t.Helper()
	node, err := Parse(q)
	if err != nil {
		t.Fatalf("Parse(%q): %v", q, err)
	}
	return node
}
