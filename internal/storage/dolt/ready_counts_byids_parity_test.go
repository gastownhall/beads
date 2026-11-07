package dolt

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// nullDepArg binds an unset target column as SQL NULL.
func nullDepArg(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// TestReadyCountsByIDsMatchPredicateForm is the results-match leg of the
// by-IDs reverse-blocker rewrite (GH#7666): a bounded ready page — which
// hydrates through SearchCountsSQL's by-IDs form, whose rc subquery is now
// the guarded per-column union per plane — must project exactly what the
// unbounded page's predicate form projects, over a fixture that puts blocker
// edges in every target column (issue, wisp, external — with a local id in
// the external column), a non-blocks edge the rc must ignore, and wisp-plane
// blockers. Placeholder/arg alignment is pinned by
// TestReadyCountsByIDsPageBindsEveryInjectionPoint; this pins the numbers.
func TestReadyCountsByIDsMatchPredicateForm(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	newIssue := func(id string) *types.Issue {
		return &types.Issue{ID: id, Title: "byids " + id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	}
	for _, issue := range []*types.Issue{
		newIssue("rbp-t1"), newIssue("rbp-s1"), newIssue("rbp-s2"), newIssue("rbp-s3"), newIssue("rbp-s4"), newIssue("rbp-e1"),
	} {
		if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("create %s: %v", issue.ID, err)
		}
	}
	// Wisp rows: w1/w2 are wisp-plane blocker sources; wt is the target of the
	// durable wisp-column edge (its own row is a wisp, so the edge is legal).
	for _, w := range []string{"rbp-w1", "rbp-w2", "rbp-wt"} {
		if _, err := store.db.ExecContext(ctx,
			`INSERT INTO wisps (id, title, status) VALUES (?, ?, ?)`, w, "byids wisp "+w, string(types.StatusOpen)); err != nil {
			t.Fatalf("seed wisp %s: %v", w, err)
		}
	}
	// Durable blockers into t1 through the issue and external columns (the
	// external value is t1's local id — the routing collision), a wisp-column
	// blocker into wt, and a related edge rc must not count.
	depRows := []struct {
		id, src, issue, wisp, external, depType string
	}{
		{"rbp-d1", "rbp-s1", "rbp-t1", "", "", string(types.DepBlocks)},
		{"rbp-d2", "rbp-s2", "", "rbp-wt", "", string(types.DepBlocks)},
		{"rbp-d3", "rbp-s3", "", "", "rbp-t1", string(types.DepBlocks)},
		{"rbp-d4", "rbp-s4", "rbp-t1", "", "", string(types.DepRelated)},
	}
	for _, r := range depRows {
		if _, err := store.db.ExecContext(ctx,
			`INSERT INTO dependencies (id, issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external, type, created_by)
			 VALUES (?, ?, ?, ?, ?, ?, 'tester')`,
			r.id, r.src, nullDepArg(r.issue), nullDepArg(r.wisp), nullDepArg(r.external), r.depType); err != nil {
			t.Fatalf("seed dependency %s: %v", r.id, err)
		}
	}
	// Wisp-plane blockers into t1 through the issue and external columns.
	wispRows := []struct {
		id, src, issue, external, depType string
	}{
		{"rbp-w1e", "rbp-w1", "rbp-t1", "", string(types.DepBlocks)},
		{"rbp-w2e", "rbp-w2", "", "rbp-t1", string(types.DepBlocks)},
	}
	for _, r := range wispRows {
		if _, err := store.db.ExecContext(ctx,
			`INSERT INTO wisp_dependencies (id, issue_id, depends_on_issue_id, depends_on_external, type, created_by)
			 VALUES (?, ?, ?, ?, ?, 'tester')`,
			r.id, r.src, nullDepArg(r.issue), nullDepArg(r.external), r.depType); err != nil {
			t.Fatalf("seed wisp dependency %s: %v", r.id, err)
		}
	}

	render := func(items []*types.IssueWithCounts) []string {
		out := make([]string, len(items))
		for i, it := range items {
			parent := ""
			if it.Parent != nil {
				parent = *it.Parent
			}
			out[i] = fmt.Sprintf("%s dep=%d rdep=%d comments=%d parent=%s labels=%v",
				it.ID, it.DependencyCount, it.DependentCount, it.CommentCount, parent, it.Issue.Labels)
		}
		return out
	}
	byIDs := func(t *testing.T) []string {
		items, err := store.GetReadyWorkWithCounts(ctx, types.WorkFilter{IncludeDeferred: true, Limit: 10})
		if err != nil {
			t.Fatalf("GetReadyWorkWithCounts bounded: %v", err)
		}
		return render(items)
	}
	byPredicate := func(t *testing.T) []string {
		items, err := store.GetReadyWorkWithCounts(ctx, types.WorkFilter{IncludeDeferred: true})
		if err != nil {
			t.Fatalf("GetReadyWorkWithCounts unbounded: %v", err)
		}
		return render(items)
	}

	bounded, unbounded := byIDs(t), byPredicate(t)
	if len(bounded) != 9 || len(unbounded) != 9 {
		t.Fatalf("ready pages hold %d (bounded) and %d (unbounded) rows, want 9 each — fixture drifted:\n%s\n%s",
			len(bounded), len(unbounded), strings.Join(bounded, "\n"), strings.Join(unbounded, "\n"))
	}
	for i := range bounded {
		if bounded[i] != unbounded[i] {
			t.Errorf("row %d differs across forms:\nby-IDs:     %s\nby-predicate: %s", i, bounded[i], unbounded[i])
		}
	}
	// Non-vacuity, on both forms: t1's reverse blockers span the durable issue
	// and external columns plus the wisp plane (4), wt's live in the durable
	// wisp column (1), and s4's related edge is not a reverse blocker.
	for _, row := range bounded {
		switch {
		case strings.HasPrefix(row, "rbp-t1 dep="):
			if !strings.Contains(row, "rdep=4") {
				t.Errorf("t1 rdep = %q, want rdep=4 (issue + external durable, issue + external wisp)", row)
			}
		case strings.HasPrefix(row, "rbp-wt dep="):
			if !strings.Contains(row, "rdep=1") {
				t.Errorf("wt rdep = %q, want rdep=1 (durable wisp column)", row)
			}
		case strings.HasPrefix(row, "rbp-s4 dep="):
			if !strings.Contains(row, "rbp-s4 dep=0") {
				t.Errorf("s4 row = %q, want dep=0 (related is not a blocker)", row)
			}
		}
	}
}
