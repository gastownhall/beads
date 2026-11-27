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

func (r *statementRecorder) QueryContext(ctx context.Context, q string, a ...any) (*sql.Rows, error) {
	r.stmts = append(r.stmts, recordedStatement{q, a})
	return r.DBTX.QueryContext(ctx, q, a...)
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

// assertRecursiveMembersAreLookups checks a walk built on reachabilityMembers:
// one recursive member per dependency table, each a lookup join from the
// frontier into that table's issue_id index.
func assertRecursiveMembersAreLookups(t *testing.T, plan string) {
	t.Helper()
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

// TestGraphWalkPlansHonorJoinHints proves the engine honors the
// JOIN_ORDER/LOOKUP_JOIN hints the per-edge reachability walks, the ancestor
// walk that seeds a parent-child edge's recompute, and the batched
// blocked-state recompute carry — not just that the text is there. Without
// them the embedded planner already plans the cycle walk's (statistics-less)
// wisp_dependencies member as a scan join, and the recompute's legs can be
// driven from an index scan of every open row (the sql-server flip described
// at shouldBeBlockedIDsUnionScopedPrecomputedSQL). A deleted, misspelled, or
// no longer resolvable hint (e.g. a renamed alias) fails here.
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
			assertRecursiveMembersAreLookups(t, explainPlan(t, tx, st))
		}
	})

	// ancestorChainInTx runs on every parent-child write and merge replay; it
	// is reached here the way a write reaches it.
	t.Run("ancestor-chain walk", func(t *testing.T) {
		rec := &statementRecorder{DBTX: tx}
		if _, _, err := issueops.AffectedByDepChangeInTx(ctx, rec, id(n-7), id(n/3), types.DepParentChild); err != nil {
			t.Fatal(err)
		}
		var walks []recordedStatement
		for _, st := range rec.stmts {
			if strings.Contains(st.query, "WITH RECURSIVE ancestors") {
				walks = append(walks, st)
			}
		}
		if len(walks) != 1 {
			t.Fatalf("recorded %d ancestor walks, want 1", len(walks))
		}
		// The same members as the reachability walks; the reason filter is
		// the outer SELECT's, so it adds no join.
		assertRecursiveMembersAreLookups(t, explainPlan(t, tx, walks[0]))
	})

	t.Run("blocked-state recompute", func(t *testing.T) {
		rec := &statementRecorder{DBTX: tx}
		if err := issueops.RecomputeIsBlockedInTx(ctx, rec, []string{id(5), id(77), id(1500)}, nil, issueops.NoRequestActor); err != nil {
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

	// bd-muhnf0: the hold adds reads of its own — the probe, the holder read,
	// the descendant expansion, and the chunk climb the scoped paths add —
	// and every one must stay a lookup probe on wisps and the edge tables,
	// never a scan: they run on every doctor check and every scoped
	// recompute, including on stores whose whole point is being large. One
	// dangling edge in the fixture makes the reads fire at all; with none,
	// the probe answers 0 and none of this machinery runs.
	t.Run("dangling-edge hold reads", func(t *testing.T) {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO dependencies (id, issue_id, depends_on_wisp_id, type, created_by) VALUES ('wp-dangling-hold', ?, 'wp-absent-wisp-1', 'blocks', 'tester')", id(5)); err != nil {
			t.Fatal(err)
		}
		rec := &statementRecorder{DBTX: tx}
		if _, err := issueops.CountIsBlockedInconsistenciesInTx(ctx, rec); err != nil {
			t.Fatal(err)
		}
		var probe, holders []recordedStatement
		var holdReads []recordedStatement
		for _, st := range rec.stmts {
			switch {
			case strings.Contains(st.query, "LEFT JOIN wisps") && strings.Contains(st.query, "LIMIT 1"):
				probe = append(probe, st)
			case strings.Contains(st.query, "DISTINCT d.issue_id") && strings.Contains(st.query, "w.id IS NULL"):
				holders = append(holders, st)
			case strings.Contains(st.query, "d.type = 'parent-child'") && strings.Contains(st.query, "IN (") &&
				!strings.Contains(st.query, "COUNT") && !strings.Contains(st.query, "WITH RECURSIVE") &&
				!strings.Contains(st.query, "UPDATE"):
				holdReads = append(holdReads, st)
			}
		}
		if len(probe) != 1 {
			t.Fatalf("recorded %d probes, want 1", len(probe))
		}
		if len(holders) != 1 {
			t.Fatalf("recorded %d holder reads, want 1", len(holders))
		}
		if len(holdReads) == 0 {
			t.Fatal("recorded no descendant-expansion reads")
		}
		all := append([]recordedStatement{probe[0], holders[0]}, holdReads...)
		for _, st := range all {
			plan := explainPlan(t, tx, st)
			if strings.Contains(plan, "HashJoin") || strings.Contains(plan, "InnerJoin") {
				t.Errorf("hold read is not a lookup join:\n%s", plan)
			}
		}
		if plan := explainPlan(t, tx, probe[0]); !strings.Contains(plan, "keys: d.depends_on_wisp_id") {
			t.Errorf("probe does not reach wisps by the edge's wisp id:\n%s", plan)
		}
		if plan := explainPlan(t, tx, holders[0]); !strings.Contains(plan, "IndexedTableAccess(wisps)") || !strings.Contains(plan, "keys: d.depends_on_wisp_id") {
			t.Errorf("holder read does not probe wisps by the edge's wisp id:\n%s", plan)
		}

		// The chunk climb the scoped paths add: four arms, each probing the
		// edge tables by the frontier node, and the holder probe over the
		// nodes the climb visited — the probe's shape, not a scan.
		rec = &statementRecorder{DBTX: tx}
		if err := issueops.RecomputeIsBlockedInTx(ctx, rec, []string{id(5)}, nil, issueops.NoRequestActor); err != nil {
			t.Fatal(err)
		}
		var climbs, ancestryHolders []recordedStatement
		for _, st := range rec.stmts {
			switch {
			case strings.Contains(st.query, "WITH RECURSIVE climb"):
				climbs = append(climbs, st)
			case strings.Contains(st.query, "DISTINCT d.issue_id") && strings.Contains(st.query, "w.id IS NULL") &&
				strings.Contains(st.query, "IN ("):
				ancestryHolders = append(ancestryHolders, st)
			}
		}
		if len(climbs) != 1 {
			t.Fatalf("recorded %d chunk climbs, want 1", len(climbs))
		}
		if len(ancestryHolders) != 1 {
			t.Fatalf("recorded %d ancestry holder probes, want 1", len(ancestryHolders))
		}
		plan := explainPlan(t, tx, climbs[0])
		if got := strings.Count(plan, "keys: c.node"); got != 5 {
			t.Errorf("climb probes by the frontier node %d times, want 5 (one per arm, plus the outer open-holder probe):\n%s", got, plan)
		}
		if got := strings.Count(plan, "keys: c.seed"); got != 1 {
			t.Errorf("climb probes by the seed %d times, want 1 (the outer seed probe):\n%s", got, plan)
		}
		if strings.Contains(plan, "HashJoin") || strings.Contains(plan, "InnerJoin") {
			t.Errorf("a climb arm is not a lookup join:\n%s", plan)
		}
		plan = explainPlan(t, tx, ancestryHolders[0])
		if !strings.Contains(plan, "IndexedTableAccess(wisps)") || !strings.Contains(plan, "keys: d.depends_on_wisp_id") {
			t.Errorf("ancestry holder probe does not probe wisps by the edge's wisp id:\n%s", plan)
		}
		if strings.Contains(plan, "HashJoin") || strings.Contains(plan, "InnerJoin") {
			t.Errorf("ancestry holder probe is not a lookup join:\n%s", plan)
		}
	})
}
