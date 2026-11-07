//go:build cgo

package embeddeddolt_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// nullDepArg binds an unset target column as SQL NULL.
func nullDepArg(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// TestReadyCountsByIDsMatchPredicateForm is the embedded twin of dolt's
// by-IDs/predicate parity test for the guarded reverse-blocker rewrite
// (gascity#7666): a bounded ready page hydrates through SearchCountsSQL's
// by-IDs form — rc subquery a guarded per-column union per plane — and must
// project exactly what the unbounded predicate form projects, over blocker
// edges in every target column, a non-blocks edge rc ignores, and wisp-plane
// blockers. The embedded engine is the pickiest about the SQL (the pure-Go
// GMS analyzer), so parity here carries the most weight.
func TestReadyCountsByIDsMatchPredicateForm(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	te := newTestEnv(t, "rbp")
	ctx := t.Context()

	newIssue := func(id string) *types.Issue {
		return &types.Issue{ID: id, Title: "byids " + id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	}
	for _, issue := range []*types.Issue{
		newIssue("rbp-t1"), newIssue("rbp-s1"), newIssue("rbp-s2"), newIssue("rbp-s3"), newIssue("rbp-s4"), newIssue("rbp-e1"),
	} {
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("create %s: %v", issue.ID, err)
		}
	}
	for _, w := range []string{"rbp-w1", "rbp-w2", "rbp-wt"} {
		te.exec(t, ctx, `INSERT INTO wisps (id, title, status) VALUES (?, ?, ?)`,
			w, "byids wisp "+w, string(types.StatusOpen))
	}
	depRows := []struct {
		id, src, issue, wisp, external, depType string
	}{
		{"rbp-d1", "rbp-s1", "rbp-t1", "", "", string(types.DepBlocks)},
		{"rbp-d2", "rbp-s2", "", "rbp-wt", "", string(types.DepBlocks)},
		{"rbp-d3", "rbp-s3", "", "", "rbp-t1", string(types.DepBlocks)},
		{"rbp-d4", "rbp-s4", "rbp-t1", "", "", string(types.DepRelated)},
	}
	for _, r := range depRows {
		te.exec(t, ctx,
			`INSERT INTO dependencies (id, issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external, type, created_by)
			 VALUES (?, ?, ?, ?, ?, ?, 'tester')`,
			r.id, r.src, nullDepArg(r.issue), nullDepArg(r.wisp), nullDepArg(r.external), r.depType)
	}
	wispRows := []struct {
		id, src, issue, external, depType string
	}{
		{"rbp-w1e", "rbp-w1", "rbp-t1", "", string(types.DepBlocks)},
		{"rbp-w2e", "rbp-w2", "", "rbp-t1", string(types.DepBlocks)},
	}
	for _, r := range wispRows {
		te.exec(t, ctx,
			`INSERT INTO wisp_dependencies (id, issue_id, depends_on_issue_id, depends_on_external, type, created_by)
			 VALUES (?, ?, ?, ?, ?, 'tester')`,
			r.id, r.src, nullDepArg(r.issue), nullDepArg(r.external), r.depType)
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
	bounded, err := te.store.GetReadyWorkWithCounts(ctx, types.WorkFilter{IncludeDeferred: true, Limit: 10})
	if err != nil {
		t.Fatalf("GetReadyWorkWithCounts bounded: %v", err)
	}
	unbounded, err := te.store.GetReadyWorkWithCounts(ctx, types.WorkFilter{IncludeDeferred: true})
	if err != nil {
		t.Fatalf("GetReadyWorkWithCounts unbounded: %v", err)
	}
	b, u := render(bounded), render(unbounded)
	if len(b) != 9 || len(u) != 9 {
		t.Fatalf("ready pages hold %d (bounded) and %d (unbounded) rows, want 9 each — fixture drifted:\n%s\n%s",
			len(b), len(u), strings.Join(b, "\n"), strings.Join(u, "\n"))
	}
	for i := range b {
		if b[i] != u[i] {
			t.Errorf("row %d differs across forms:\nby-IDs:       %s\nby-predicate: %s", i, b[i], u[i])
		}
	}
	for _, row := range b {
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
