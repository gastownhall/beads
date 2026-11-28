//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/batchfixtures"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/sqlcount"
)

// #7666: the incoming dependency count must plan as the guarded UNION ALL, one
// branch per target column, each seeking its index. The per-column OR and the
// COALESCE both plan as a full table read of dependencies.
func TestIncomingDependencyCountPlansAsGuardedUnion(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	fixture := newPristineEmbeddedDoltFixture(t, "countplan")
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
	for _, st := range batchfixtures.LargeGraphInserts("cp", n) {
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

	rec := &statementRecorder{DBTX: tx}
	target := batchfixtures.LargeGraphID("cp", n/3)
	if _, err := issueops.CountDependencyEdgesInTx(ctx, rec, target, domain.DepDirectionIn, nil); err != nil {
		t.Fatal(err)
	}
	if len(rec.stmts) == 0 {
		t.Fatal("CountDependencyEdgesInTx sent no statements to plan")
	}
	for i, st := range rec.stmts {
		plan := explainPlanNoSkip(t, tx, st)
		if got := strings.Count(plan, "IndexedTableAccess("); got < 3 {
			t.Errorf("statement %d: want one index seek per target column (>=3 IndexedTableAccess), got %d:\n%s\nquery:\n%s", i, got, plan, st.query)
		}
		if strings.Contains(plan, "Table(dependencies)") && !strings.Contains(plan, "IndexedTableAccess(dependencies)") {
			t.Errorf("statement %d: plan reads dependencies as a full table:\n%s", i, plan)
		}
	}
}

// explainPlanNoSkip is explainPlan without its skip: a plan that carries no
// index seek must fail this test, not skip it.
func explainPlanNoSkip(t *testing.T, tx *sql.Tx, st recordedStatement) string {
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
	return strings.Join(out, "\n")
}
