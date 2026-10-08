//go:build cgo

package embeddeddolt_test

import (
	"sort"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/query"
	"github.com/steveyegge/beads/internal/types"
)

// TestSearchNoHistoryReadsOnlyNoHistoryRows: `bd query "no_history=true AND
// status=closed"` answers with the closed no-history rows and nothing from
// the issues table, so a caller looking for wisp-plane rows no longer has to
// list every closed issue and keep the few it wants (gascity's wisp gc read
// the whole closed set of a 61,000-row store to find 19 rows).
func TestSearchNoHistoryReadsOnlyNoHistoryRows(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	te := newTestEnv(t, "snh")

	for _, iss := range []*types.Issue{
		{ID: "snh-plain", Title: "ordinary closed issue", IssueType: types.TypeTask},
		{ID: "snh-open", Title: "ordinary open issue", IssueType: types.TypeTask},
		{ID: "snh-nh", Title: "closed no-history task", IssueType: types.TypeTask, NoHistory: true},
		{ID: "snh-nh-open", Title: "open no-history task", IssueType: types.TypeTask, NoHistory: true},
		{ID: "snh-eph", Title: "closed ephemeral wisp", IssueType: types.TypeTask, Ephemeral: true},
	} {
		iss.Status = types.StatusOpen
		iss.Priority = 2
		if err := te.store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("create %s: %v", iss.ID, err)
		}
	}
	for _, id := range []string{"snh-plain", "snh-nh", "snh-eph"} {
		if err := te.store.CloseIssue(ctx, id, "done", "tester", ""); err != nil {
			t.Fatalf("close %s: %v", id, err)
		}
	}
	te.assertRowExists(t, ctx, "wisps", "snh-nh")
	te.assertRowExists(t, ctx, "issues", "snh-plain")

	search := func(expr string) []string {
		t.Helper()
		node, err := query.Parse(expr)
		if err != nil {
			t.Fatalf("parse %q: %v", expr, err)
		}
		res, err := query.NewEvaluator(time.Now()).Evaluate(node)
		if err != nil {
			t.Fatalf("evaluate %q: %v", expr, err)
		}
		if res.RequiresPredicate {
			t.Fatalf("%q needs a predicate; want a database filter", expr)
		}
		got, err := te.store.SearchIssues(ctx, "", res.Filter)
		if err != nil {
			t.Fatalf("search %q: %v", expr, err)
		}
		ids := make([]string, 0, len(got))
		for _, iss := range got {
			ids = append(ids, iss.ID)
		}
		sort.Strings(ids)
		return ids
	}
	assertIDs := func(expr string, want ...string) {
		t.Helper()
		got := search(expr)
		if len(got) != len(want) {
			t.Fatalf("%q = %v, want %v", expr, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%q = %v, want %v", expr, got, want)
			}
		}
	}

	assertIDs("no_history=true AND status=closed", "snh-nh")
	assertIDs("no_history=true", "snh-nh", "snh-nh-open")
	assertIDs("no_history=false AND status=closed", "snh-eph", "snh-plain")
	// The wisp plane is the two reads together: ephemeral and no-history.
	assertIDs("ephemeral=true AND status=closed", "snh-eph")
}
