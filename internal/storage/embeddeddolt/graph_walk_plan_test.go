//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/batchfixtures"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/sqlcount"
	"github.com/steveyegge/beads/internal/types"
)

// statementRecorder is an issueops.DBTX that records every statement the
// production entry points send, so the test can EXPLAIN exactly those (the
// batch-template expansion included) without exporting the builders.
type statementRecorder struct {
	issueops.DBTX
	stmts []recordedStatement
}

type recordedStatement struct {
	query string
	args  []any
}

func (r *statementRecorder) QueryRowContext(ctx context.Context, q string, a ...any) *sql.Row {
	r.stmts = append(r.stmts, recordedStatement{q, a})
	return r.DBTX.QueryRowContext(ctx, q, a...)
}

func (r *statementRecorder) ExecContext(ctx context.Context, q string, a ...any) (sql.Result, error) {
	r.stmts = append(r.stmts, recordedStatement{q, a})
	return r.DBTX.ExecContext(ctx, q, a...)
}

func explainPlan(t *testing.T, tx *sql.Tx, st recordedStatement) string {
	t.Helper()
	rows, err := tx.QueryContext(context.Background(), "EXPLAIN PLAN "+st.query, st.args...)
	if err != nil {
		t.Fatalf("EXPLAIN PLAN: %v\n%s", err, st.query)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var line sql.NullString
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		out = append(out, line.String)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	plan := strings.Join(out, "\n")
	if !strings.Contains(plan, "IndexedTableAccess(") {
		t.Skipf("EXPLAIN output not in a recognized Dolt plan format, skipping plan assertions; plan=\n%s", plan)
	}
	return plan
}

// TestGraphWalkPlansHonorJoinHints proves the engine honors the
// JOIN_ORDER/LOOKUP_JOIN hints the per-edge reachability walks and the batched
// blocked-state recompute carry — not just that the text is there. Without
// them the embedded planner already plans the cycle walk's (statistics-less)
// wisp_dependencies member as a scan join, and the recompute's legs can be
// driven from an index scan of every open row (the sql-server flip described
// at shouldBeBlockedIDsUnionScopedSQL). A deleted, misspelled, or no longer
// resolvable hint (e.g. a renamed alias) fails here.
func TestGraphWalkPlansHonorJoinHints(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	fixture := newPristineEmbeddedDoltFixture(t, "walkplan")
	t.Cleanup(func() { closeEmbeddedDoltStore(t, fixture.store) })
	db, cleanup, err := openCountedConn(t.Context(), fixture.dataDir, fixture.database, &sqlcount.Counts{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	ctx := context.Background()
	const n = 2000
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range batchfixtures.LargeGraphInserts("wp", n) {
		if _, err := tx.ExecContext(ctx, st.SQL, st.Args...); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	id := func(i int) string { return batchfixtures.LargeGraphID("wp", i) }

	t.Run("reachability walks", func(t *testing.T) {
		rec := &statementRecorder{DBTX: tx}
		if _, err := issueops.WouldCreateSchedulingCycleInTx(ctx, rec, id(n-7), id(n/3), nil); err != nil {
			t.Fatal(err)
		}
		dep := &types.Dependency{IssueID: id(n - 7), DependsOnID: id(n / 3), Type: types.DepBlocks}
		if err := issueops.CheckBlockingHierarchyInTx(ctx, rec, dep, nil); err != nil {
			t.Fatal(err)
		}
		if len(rec.stmts) != 3 {
			t.Fatalf("recorded %d statements, want 3 (one cycle walk, two ancestor walks)", len(rec.stmts))
		}
		for _, st := range rec.stmts {
			plan := explainPlan(t, tx, st)
			// One recursive member per dependency table, each a lookup join
			// from the frontier into that table's issue_id index.
			for _, want := range []string{"IndexedTableAccess(dependencies)", "IndexedTableAccess(wisp_dependencies)"} {
				if !strings.Contains(plan, want) {
					t.Errorf("plan has no %s:\n%s", want, plan)
				}
			}
			if got := strings.Count(plan, "LookupJoin"); got != 2 {
				t.Errorf("plan has %d LookupJoin, want 2 (one per recursive member):\n%s", got, plan)
			}
			if got := strings.Count(plan, "keys: r.node"); got != 2 {
				t.Errorf("plan probes the edge tables by the frontier node %d times, want 2:\n%s", got, plan)
			}
			if strings.Contains(plan, "InnerJoin") || strings.Contains(plan, "HashJoin") || strings.Contains(plan, "MergeJoin") {
				t.Errorf("a recursive member is not a lookup join:\n%s", plan)
			}
		}
	})

	t.Run("blocked-state recompute", func(t *testing.T) {
		rec := &statementRecorder{DBTX: tx}
		if err := issueops.RecomputeIsBlockedInTx(ctx, rec, []string{id(5), id(77), id(1500)}, nil); err != nil {
			t.Fatal(err)
		}
		checked := 0
		for _, st := range rec.stmts {
			if !strings.Contains(st.query, "UPDATE issues") || !strings.Contains(st.query, "UNION") {
				continue
			}
			checked++
			plan := explainPlan(t, tx, st)
			if got := strings.Count(plan, "LookupJoin"); got != 4 {
				t.Errorf("plan has %d LookupJoin, want 4 (one per joined union leg):\n%s", got, plan)
			}
			for _, want := range []string{"keys: d.depends_on_issue_id", "keys: d.depends_on_wisp_id"} {
				if got := strings.Count(plan, want); got != 2 {
					t.Errorf("plan has %q %d times, want 2:\n%s", want, got, plan)
				}
			}
			if strings.Contains(plan, "is_blocked,issues.status") || strings.Contains(plan, "is_blocked,wisps.status") {
				t.Errorf("a union leg is driven from an is_blocked/status index scan:\n%s", plan)
			}
		}
		if checked == 0 || checked%2 != 0 {
			t.Fatalf("checked %d mark/unmark statements, want a mark and an unmark per fixpoint pass", checked)
		}
	})
}
