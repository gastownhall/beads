//go:build cgo

package embeddeddolt_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/dberrors"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// TestDependentCountsMatchCoalesceOracle is the embedded twin of dolt's
// oracle test: the guarded per-column UNION ALL rewrite of the inbound
// dependency counts (gascity#7666) must return exactly what the old COALESCE
// queries computed, over durable/wisp edges covering every target column,
// mixed types and source statuses, an external value that collides with a
// local issue id, the permitted cross-plane duplicate, and the missing-wisp-
// plane case CountDependencyEdgesInTx tolerates (and the status count does
// not). Fixture, expectations and oracles mirror dolt's; only the seeding
// and query helpers differ.
func TestDependentCountsMatchCoalesceOracle(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	te := newTestEnv(t, "eco")
	ctx := t.Context()

	newIssue := func(id string, status types.Status, ephemeral bool) *types.Issue {
		return &types.Issue{ID: id, Title: "oracle " + id, Status: status, Priority: 2, IssueType: types.TypeTask, Ephemeral: ephemeral}
	}
	for _, issue := range []*types.Issue{
		newIssue("eco-tgt", types.StatusOpen, false),
		newIssue("eco-empty", types.StatusOpen, false),
		newIssue("eco-src-open", types.StatusOpen, false),
		newIssue("eco-src-closed", types.StatusClosed, false),
		newIssue("eco-dual", types.StatusOpen, false),
		newIssue("eco-wsrc-open", types.StatusOpen, true),
		newIssue("eco-wsrc-closed", types.StatusClosed, true),
		newIssue("eco-wtgt", types.StatusOpen, true),
	} {
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("create %s: %v", issue.ID, err)
		}
	}
	// The dual-homed id: the same id in wisps with a DIFFERENT status, so the
	// two planes disagree about its status and each counts its own copy.
	te.exec(t, ctx, `INSERT INTO wisps (id, title, status) VALUES (?, ?, ?)`,
		"eco-dual", "dual-homed wisp row", string(types.StatusClosed))

	depRows := []struct {
		id, src, issue, wisp, external, depType string
	}{
		{"eco-r1", "eco-src-open", "eco-tgt", "", "", string(types.DepBlocks)},
		{"eco-r2", "eco-src-closed", "eco-tgt", "", "", string(types.DepWaitsFor)},
		{"eco-r3", "eco-src-open", "", "eco-wtgt", "", string(types.DepBlocks)},
		{"eco-r4", "eco-src-open", "", "", "external:gh-9", string(types.DepRelated)},
		{"eco-r5", "eco-empty", "", "", "eco-tgt", string(types.DepBlocks)},
		{"eco-r6", "eco-dual", "eco-tgt", "", "", string(types.DepBlocks)},
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
		{"eco-w1", "eco-wsrc-open", "eco-tgt", "", string(types.DepBlocks)},
		{"eco-w2", "eco-wsrc-closed", "eco-tgt", "", string(types.DepRelated)},
		{"eco-w3", "eco-dual", "eco-tgt", "", string(types.DepBlocks)},
		{"eco-w4", "eco-wsrc-open", "", "external:gh-9", string(types.DepBlocks)},
	}
	for _, r := range wispRows {
		te.exec(t, ctx,
			`INSERT INTO wisp_dependencies (id, issue_id, depends_on_issue_id, depends_on_external, type, created_by)
			 VALUES (?, ?, ?, ?, ?, 'tester')`,
			r.id, r.src, nullDepArg(r.issue), nullDepArg(r.external), r.depType)
	}

	// The old-query oracles, verbatim.
	oracleInboundEdges := func(target string, filter []types.DependencyType) int64 {
		typeClause, typeArgs := "", []any{}
		if len(filter) > 0 {
			ph := make([]string, len(filter))
			for i, ft := range filter {
				ph[i] = "?"
				typeArgs = append(typeArgs, string(ft))
			}
			typeClause = " AND type IN (" + strings.Join(ph, ",") + ")"
		}
		var durable int64
		te.queryScalar(t, ctx,
			`SELECT count(*) FROM dependencies WHERE `+issueops.DepTargetExpr+` = ?`+typeClause,
			append([]any{target}, typeArgs...), &durable)
		var wisp int64
		te.queryScalar(t, ctx,
			`SELECT count(*) FROM wisp_dependencies WHERE `+issueops.DepTargetExpr+` = ?
			 AND issue_id NOT IN (SELECT issue_id FROM dependencies WHERE `+issueops.DepTargetExpr+` = ?)`+typeClause,
			append([]any{target, target}, typeArgs...), &wisp)
		return durable + wisp
	}
	oracleDependentsByStatus := func(target string, status types.Status) int64 {
		var perm, wisp int64
		te.queryScalar(t, ctx,
			`SELECT count(*) FROM dependencies d
			 JOIN issues i ON i.id = d.issue_id
			 WHERE COALESCE(d.depends_on_issue_id, d.depends_on_wisp_id, d.depends_on_external) = ? AND i.status = ?`,
			[]any{target, string(status)}, &perm)
		te.queryScalar(t, ctx,
			`SELECT count(*) FROM wisp_dependencies d
			 JOIN wisps w ON w.id = d.issue_id
			 WHERE COALESCE(d.depends_on_issue_id, d.depends_on_wisp_id, d.depends_on_external) = ? AND w.status = ?`,
			[]any{target, string(status)}, &wisp)
		return perm + wisp
	}

	// issueops-level type-filtered calls run on a short-lived raw connection.
	inboundEdges := func(target string, filter []types.DependencyType) int64 {
		db, cleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
		if err != nil {
			t.Fatalf("OpenSQL: %v", err)
		}
		defer cleanup()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("BeginTx: %v", err)
		}
		defer tx.Rollback() //nolint:errcheck
		n, err := issueops.CountDependencyEdgesInTx(ctx, tx, target, domain.DepDirectionIn, filter)
		if err != nil {
			t.Fatalf("CountDependencyEdgesInTx(%s, %v): %v", target, filter, err)
		}
		return n
	}

	// Inbound edge counts: every target column, both filters, empty result.
	// (Hand-computed against the fixture; mirrors the dolt twin's table.)
	edgeCases := []struct {
		target string
		filter []types.DependencyType
		want   int64
	}{
		{"eco-tgt", nil, 6},
		{"eco-tgt", []types.DependencyType{types.DepBlocks}, 4},
		{"eco-tgt", []types.DependencyType{types.DepWaitsFor, types.DepRelated}, 2},
		{"external:gh-9", nil, 2},
		{"eco-wtgt", nil, 1},
		{"eco-empty", nil, 0},
	}
	for _, tc := range edgeCases {
		if got := oracleInboundEdges(tc.target, tc.filter); got != tc.want {
			t.Errorf("oracle In(%s, %v) = %d, fixture says %d — oracle or fixture is wrong", tc.target, tc.filter, got, tc.want)
		}
		if got := inboundEdges(tc.target, tc.filter); got != tc.want || got != oracleInboundEdges(tc.target, tc.filter) {
			t.Errorf("CountDependencyEdgesInTx In(%s, %v) = %d, want %d (oracle %d)",
				tc.target, tc.filter, got, tc.want, oracleInboundEdges(tc.target, tc.filter))
		}
	}
	if n, err := te.store.CountDependents(ctx, "eco-tgt"); err != nil {
		t.Fatalf("CountDependents: %v", err)
	} else if n != 6 {
		t.Errorf("CountDependents(eco-tgt) = %d, want 6", n)
	}

	// Status counts: the dual-homed row counts OPEN from issues and CLOSED
	// from wisps — one row, two planes, both right.
	statusCases := []struct {
		target string
		status types.Status
		want   int64
	}{
		{"eco-tgt", types.StatusOpen, 4},
		{"eco-tgt", types.StatusClosed, 3},
		{"external:gh-9", types.StatusOpen, 2},
		{"eco-wtgt", types.StatusOpen, 1},
		{"eco-empty", types.StatusOpen, 0},
	}
	for _, tc := range statusCases {
		if got := oracleDependentsByStatus(tc.target, tc.status); got != tc.want {
			t.Errorf("oracle status(%s, %s) = %d, fixture says %d — oracle or fixture is wrong", tc.target, tc.status, got, tc.want)
		}
		n, err := te.store.CountDependentsByStatus(ctx, tc.target, tc.status)
		if err != nil || n != tc.want {
			t.Errorf("CountDependentsByStatus(%s, %s) = %d, %v; want %d (oracle %d)",
				tc.target, tc.status, n, err, tc.want, oracleDependentsByStatus(tc.target, tc.status))
		}
	}

	// Outbound direction untouched; one parity spot check.
	if n, err := te.store.CountDependencies(ctx, "eco-dual"); err != nil {
		t.Fatalf("CountDependencies: %v", err)
	} else if n != 1 {
		t.Errorf("CountDependencies(eco-dual) = %d, want 1 (wisp copy suppressed by durable twin)", n)
	}

	// Missing wisp plane, last: edges tolerate, status surfaces the error.
	te.exec(t, ctx, "DROP TABLE wisp_dependencies")
	if n, err := te.store.CountDependents(ctx, "eco-tgt"); err != nil {
		t.Fatalf("CountDependents without wisp plane: %v", err)
	} else if n != 4 {
		t.Errorf("CountDependents(eco-tgt) without wisp plane = %d, want 4 (durable alone)", n)
	}
	if got := inboundEdges("eco-tgt", []types.DependencyType{types.DepBlocks}); got != 3 {
		t.Errorf("CountDependencyEdgesInTx In(eco-tgt, blocks) without wisp plane = %d, want 3", got)
	}
	if _, err := te.store.CountDependentsByStatus(ctx, "eco-tgt", types.StatusOpen); err == nil || !dberrors.IsMissingTable(err, "wisp_dependencies") {
		t.Errorf("CountDependentsByStatus without wisp plane: err = %v, want a missing-wisp_dependencies error", err)
	}
}

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
