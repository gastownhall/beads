package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/dberrors"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// TestDependentCountsMatchCoalesceOracle is the parity guard for the guarded
// per-column UNION ALL rewrite of the inbound dependency counts
// (gascity#7666): CountDependencyEdgesInTx's In direction and
// CountDependentsByStatus must return exactly what their old COALESCE
// queries computed, over durable/wisp edges covering every target column,
// mixed types and source statuses, an external value that collides with a
// local issue id, the permitted cross-plane duplicate, and the missing-wisp-
// plane case CountDependencyEdgesInTx tolerates (and the status count does
// not). Each API is compared against its OWN old-query oracle run on the same
// fixture, and both against hand-computed expectations, so a rewrite that
// changes semantics fails twice.
//
// The fixture uses raw SQL for the dependency rows so each typed target
// column is populated deliberately (production writes route targets the same
// way, but one row at a time); rows stay within ck_dep_one_target (exactly
// one non-NULL target) and every foreign key, so nothing here depends on
// constraints being relaxed.
func TestDependentCountsMatchCoalesceOracle(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Issues: tgt is the counted target; src-open/src-closed are perm
	// dependents by status; dual lives in issues (open) AND wisps (closed, raw
	// row below) so the cross-plane duplicate is legal against both planes'
	// foreign keys; empty has no edges; wsrc-open/wsrc-closed are wisp
	// dependents; wtgt is a wisp target.
	newIssue := func(id string, status types.Status, ephemeral bool) *types.Issue {
		return &types.Issue{ID: id, Title: "oracle " + id, Status: status, Priority: 2, IssueType: types.TypeTask, Ephemeral: ephemeral}
	}
	for _, issue := range []*types.Issue{
		newIssue("dco-tgt", types.StatusOpen, false),
		newIssue("dco-empty", types.StatusOpen, false),
		newIssue("dco-src-open", types.StatusOpen, false),
		newIssue("dco-src-closed", types.StatusClosed, false),
		newIssue("dco-dual", types.StatusOpen, false),
		newIssue("dco-wsrc-open", types.StatusOpen, true),
		newIssue("dco-wsrc-closed", types.StatusClosed, true),
		newIssue("dco-wtgt", types.StatusOpen, true),
	} {
		if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("create %s: %v", issue.ID, err)
		}
	}
	// The dual-homed id: the same id in wisps with a DIFFERENT status, so the
	// two planes disagree about its status and each counts its own copy.
	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO wisps (id, title, status) VALUES (?, ?, ?)`,
		"dco-dual", "dual-homed wisp row", string(types.StatusClosed)); err != nil {
		t.Fatalf("seed dual wisps row: %v", err)
	}

	// Durable rows (dependencies): every target column represented. r5 puts a
	// LOCAL issue id in depends_on_external — no foreign key, and the one
	// shape where a per-column rewrite could mis-route if a branch's guards
	// were missing. r6 is the durable copy of the cross-plane duplicate.
	depRows := []struct {
		id, src, issue, wisp, external, depType string
	}{
		{"dco-r1", "dco-src-open", "dco-tgt", "", "", string(types.DepBlocks)},
		{"dco-r2", "dco-src-closed", "dco-tgt", "", "", string(types.DepWaitsFor)},
		{"dco-r3", "dco-src-open", "", "dco-wtgt", "", string(types.DepBlocks)},
		{"dco-r4", "dco-src-open", "", "", "external:gh-9", string(types.DepRelated)},
		{"dco-r5", "dco-empty", "", "", "dco-tgt", string(types.DepBlocks)},
		{"dco-r6", "dco-dual", "dco-tgt", "", "", string(types.DepBlocks)},
	}
	for _, r := range depRows {
		if _, err := store.db.ExecContext(ctx,
			`INSERT INTO dependencies (id, issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external, type, created_by)
			 VALUES (?, ?, ?, ?, ?, ?, 'tester')`,
			r.id, r.src, nullDepArg(r.issue), nullDepArg(r.wisp), nullDepArg(r.external), r.depType); err != nil {
			t.Fatalf("seed dependency %s: %v", r.id, err)
		}
	}
	// Wisp rows (wisp_dependencies): w3 is the wisp copy of the durable r6.
	wispRows := []struct {
		id, src, issue, external, depType string
	}{
		{"dco-w1", "dco-wsrc-open", "dco-tgt", "", string(types.DepBlocks)},
		{"dco-w2", "dco-wsrc-closed", "dco-tgt", "", string(types.DepRelated)},
		{"dco-w3", "dco-dual", "dco-tgt", "", string(types.DepBlocks)},
		{"dco-w4", "dco-wsrc-open", "", "external:gh-9", string(types.DepBlocks)},
	}
	for _, r := range wispRows {
		if _, err := store.db.ExecContext(ctx,
			`INSERT INTO wisp_dependencies (id, issue_id, depends_on_issue_id, depends_on_external, type, created_by)
			 VALUES (?, ?, ?, ?, ?, 'tester')`,
			r.id, r.src, nullDepArg(r.issue), nullDepArg(r.external), r.depType); err != nil {
			t.Fatalf("seed wisp dependency %s: %v", r.id, err)
		}
	}

	// The old-query oracles: verbatim what countDistinctEdges(In) and the two
	// stores' CountDependentsByStatus ran before the rewrite.
	oracleInboundEdges := func(target string, filter []types.DependencyType) int64 {
		t.Helper()
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
		if err := store.db.QueryRowContext(ctx,
			`SELECT count(*) FROM dependencies WHERE `+issueops.DepTargetExpr+` = ?`+typeClause,
			append([]any{target}, typeArgs...)...).Scan(&durable); err != nil {
			t.Fatalf("oracle durable count %s: %v", target, err)
		}
		var wisp int64
		if err := store.db.QueryRowContext(ctx,
			`SELECT count(*) FROM wisp_dependencies WHERE `+issueops.DepTargetExpr+` = ?
			 AND issue_id NOT IN (SELECT issue_id FROM dependencies WHERE `+issueops.DepTargetExpr+` = ?)`+typeClause,
			append([]any{target, target}, typeArgs...)...).Scan(&wisp); err != nil {
			t.Fatalf("oracle wisp count %s: %v", target, err)
		}
		return durable + wisp
	}
	oracleDependentsByStatus := func(target string, status types.Status) int64 {
		t.Helper()
		var perm, wisp int64
		if err := store.db.QueryRowContext(ctx,
			`SELECT count(*) FROM dependencies d
			 JOIN issues i ON i.id = d.issue_id
			 WHERE COALESCE(d.depends_on_issue_id, d.depends_on_wisp_id, d.depends_on_external) = ? AND i.status = ?`,
			target, string(status)).Scan(&perm); err != nil {
			t.Fatalf("oracle durable status count %s: %v", target, err)
		}
		if err := store.db.QueryRowContext(ctx,
			`SELECT count(*) FROM wisp_dependencies d
			 JOIN wisps w ON w.id = d.issue_id
			 WHERE COALESCE(d.depends_on_issue_id, d.depends_on_wisp_id, d.depends_on_external) = ? AND w.status = ?`,
			target, string(status)).Scan(&wisp); err != nil {
			t.Fatalf("oracle wisp status count %s: %v", target, err)
		}
		return perm + wisp
	}

	// issueops-level calls (type-filtered In, and the shared status-count
	// body) run on a plain read transaction over the same pool.
	inboundEdges := func(target string, filter []types.DependencyType) int64 {
		t.Helper()
		var n int64
		if err := store.withReadTx(ctx, func(tx *sql.Tx) error {
			var err error
			n, err = issueops.CountDependencyEdgesInTx(ctx, tx, target, domain.DepDirectionIn, filter)
			return err
		}); err != nil {
			t.Fatalf("CountDependencyEdgesInTx(%s, %v): %v", target, filter, err)
		}
		return n
	}

	// Inbound edge counts: every target column, both filters, empty result.
	// Hand-computed against the fixture above (see the row tables).
	edgeCases := []struct {
		target string
		filter []types.DependencyType
		want   int64
	}{
		{"dco-tgt", nil, 6}, // durable r1,r2,r5,r6 + wisp w1,w2 (w3 suppressed by r6)
		{"dco-tgt", []types.DependencyType{types.DepBlocks}, 4},
		{"dco-tgt", []types.DependencyType{types.DepWaitsFor, types.DepRelated}, 2},
		{"external:gh-9", nil, 2}, // durable r4 + wisp w4
		{"dco-wtgt", nil, 1},      // durable r3 (depends_on_wisp_id column)
		{"dco-empty", nil, 0},     // empty result; dco-empty is itself r5's source
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
	// The store-level wrapper (nil filter) must agree too.
	if n, err := store.CountDependents(ctx, "dco-tgt"); err != nil {
		t.Fatalf("CountDependents: %v", err)
	} else if n != 6 {
		t.Errorf("CountDependents(dco-tgt) = %d, want 6", n)
	}

	// Status counts: the dual-homed row counts OPEN from issues and CLOSED
	// from wisps — one row, two planes, both right.
	statusCases := []struct {
		target string
		status types.Status
		want   int64
	}{
		{"dco-tgt", types.StatusOpen, 4},   // durable r1,r5,r6(open) + wisp w1
		{"dco-tgt", types.StatusClosed, 3}, // durable r2 + wisp w2, w3(dual wisps row is closed)
		{"external:gh-9", types.StatusOpen, 2},
		{"dco-wtgt", types.StatusOpen, 1},
		{"dco-empty", types.StatusOpen, 0},
	}
	for _, tc := range statusCases {
		if got := oracleDependentsByStatus(tc.target, tc.status); got != tc.want {
			t.Errorf("oracle status(%s, %s) = %d, fixture says %d — oracle or fixture is wrong", tc.target, tc.status, got, tc.want)
		}
		n, err := store.CountDependentsByStatus(ctx, tc.target, tc.status)
		if err != nil || n != tc.want {
			t.Errorf("CountDependentsByStatus(%s, %s) = %d, %v; want %d (oracle %d)",
				tc.target, tc.status, n, err, tc.want, oracleDependentsByStatus(tc.target, tc.status))
		}
	}

	// The outbound direction is untouched by the rewrite; one parity spot
	// check keeps it honest (dco-dual's two same-target rows count once).
	if n, err := store.CountDependencies(ctx, "dco-dual"); err != nil {
		t.Fatalf("CountDependencies: %v", err)
	} else if n != 1 {
		t.Errorf("CountDependencies(dco-dual) = %d, want 1 (wisp copy suppressed by durable twin)", n)
	}

	// Missing wisp plane, last: CountDependencyEdgesInTx tolerates a dropped
	// wisp_dependencies (a dolt-ignored table a working-set reset can drop)
	// and counts the durable plane alone; the status count has no such
	// tolerance and must surface the error.
	if _, err := store.db.ExecContext(ctx, "DROP TABLE wisp_dependencies"); err != nil {
		t.Fatalf("drop wisp_dependencies: %v", err)
	}
	if n, err := store.CountDependents(ctx, "dco-tgt"); err != nil {
		t.Fatalf("CountDependents without wisp plane: %v", err)
	} else if n != 4 {
		t.Errorf("CountDependents(dco-tgt) without wisp plane = %d, want 4 (durable alone)", n)
	}
	if got := inboundEdges("dco-tgt", []types.DependencyType{types.DepBlocks}); got != 3 {
		t.Errorf("CountDependencyEdgesInTx In(dco-tgt, blocks) without wisp plane = %d, want 3", got)
	}
	_, err := store.CountDependentsByStatus(ctx, "dco-tgt", types.StatusOpen)
	if err == nil || !dberrors.IsMissingTable(err, "wisp_dependencies") {
		t.Errorf("CountDependentsByStatus without wisp plane: err = %v, want a missing-wisp_dependencies error", err)
	}
}

func nullDepArg(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// TestCountDependentsByStatusSQLShape pins that the delegated status count
// issues the guarded per-column unions (gascity#7666), not the old COALESCE
// filters, on the server engine.
func TestCountDependentsByStatusSQLShape(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	err := store.withReadTx(ctx, func(tx *sql.Tx) error {
		rec := &recordingTx{Tx: tx}
		if _, err := issueops.CountDependentsByStatusInTx(ctx, rec, "dcss-tgt", types.StatusOpen); err != nil {
			t.Fatal(err)
		}
		if len(rec.stmts) != 2 {
			t.Fatalf("recorded %d statements, want 2 (durable then wisp): %+v", len(rec.stmts), rec.stmts)
		}
		for _, st := range rec.stmts {
			if strings.Contains(st.query, "COALESCE(") {
				t.Errorf("status count still filters through a COALESCE expression no index can match:\n%s", st.query)
			}
			if got := strings.Count(st.query, "UNION ALL"); got != 2 {
				t.Errorf("status count has %d UNION ALL separators, want 2 (three guarded branches):\n%s", got, st.query)
			}
			if len(st.args) != 6 {
				t.Errorf("status count binds %d args, want 6 (target+status per branch × 3): %v", len(st.args), st.args)
			}
			for i, a := range st.args {
				wantKey, wantStatus := "dcss-tgt", string(types.StatusOpen)
				if a != wantKey && a != wantStatus {
					t.Errorf("status count arg %d = %v, want %q or %q", i, a, wantKey, wantStatus)
				}
				if i%2 == 0 && a != wantKey {
					t.Errorf("status count arg %d = %v, want target %q (branch-leading)", i, a, wantKey)
				}
			}
		}
		// Plane order: durable first, wisp second.
		if !strings.Contains(rec.stmts[0].query, "JOIN issues") || !strings.Contains(rec.stmts[1].query, "JOIN wisps") {
			t.Errorf("status counts are not durable-then-wisp:\n%s\n%s", rec.stmts[0].query, rec.stmts[1].query)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// recordingTx is an issueops.DBTX that records every statement sent through
// it (the server-mode twin of embeddeddolt's statementRecorder).
type recordingTx struct {
	*sql.Tx
	stmts []recordedStmt
}

type recordedStmt struct {
	query string
	args  []any
}

func (r *recordingTx) QueryRowContext(ctx context.Context, q string, a ...any) *sql.Row {
	r.stmts = append(r.stmts, recordedStmt{q, a})
	return r.Tx.QueryRowContext(ctx, q, a...)
}

// TestReadyCountsByIDsMatchPredicateForm is the results-match leg of the
// by-IDs reverse-blocker rewrite (gascity#7666): a bounded ready page — which
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
