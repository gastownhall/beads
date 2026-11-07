package dolt

import (
	"context"
	"database/sql"
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
// (GH#7666): CountDependencyEdgesInTx's In direction and
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
