//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"fmt"
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
	for _, st := range append(batchfixtures.LargeGraphInserts("wp", n), wispEdgeInserts("wp", n)...) {
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

	// A recompute over rows with wisp edges, some onto wisps that are here and
	// some onto wisps that are not, the shape of a database that mints its
	// wisps and of a clone of it. Spelled NOT EXISTS, the dangling-edge test
	// planned as an anti-join over a full scan of wisps and made this recompute
	// about 20x slower (the review of gastownhall/beads#8597); every statement
	// that reads wisps must read it by primary key.
	t.Run("blocked-state recompute over wisp edges", func(t *testing.T) {
		rec := &statementRecorder{DBTX: tx}
		if err := issueops.RecomputeIsBlockedInTx(ctx, rec, wispEdgeHolders("wp"), nil, issueops.NoRequestActor); err != nil {
			t.Fatal(err)
		}
		var guards, undecided, updates int
		for _, st := range rec.stmts {
			switch {
			case strings.Contains(st.query, "LEFT JOIN wisps dw"):
				// The batch guard and the undecided-row read: drive from the
				// batch's dependency rows, look each wisp up by key.
				plan := explainPlan(t, tx, st)
				assertWispsReadByKey(t, plan, "d.depends_on_wisp_id")
				if !strings.Contains(plan, "LeftOuterLookupJoin") {
					t.Errorf("a dangling-edge read is not a lookup join:\n%s", plan)
				}
				if strings.Contains(st.query, "COUNT(") {
					guards++
				} else {
					undecided++
				}
			case strings.Contains(st.query, "LEFT JOIN wisps rw"):
				assertWispsReadByKey(t, explainPlan(t, tx, st), "pr.depends_on_wisp_id")
			case strings.Contains(st.query, "UPDATE issues") && strings.Contains(st.query, "UNION"):
				updates++
				plan := explainPlan(t, tx, st)
				if got := strings.Count(plan, "LookupJoin"); got != 4 {
					t.Errorf("plan has %d LookupJoin, want 4 (one per joined union leg):\n%s", got, plan)
				}
				assertWispsReadByKey(t, plan, "d.depends_on_wisp_id")
			}
		}
		if guards == 0 || undecided == 0 || updates == 0 {
			t.Fatalf("recorded %d guard reads, %d undecided-row reads and %d union updates; want each at least once", guards, undecided, updates)
		}
	})

	// The unscoped forms over the same fixture: the probes for a dangling
	// edge, the full repair's statements, which hold the dangling holders out
	// of the fixpoint, and the doctor counts.
	t.Run("full repair and doctor counts over wisp edges", func(t *testing.T) {
		rec := &statementRecorder{DBTX: tx}
		if _, err := issueops.RecomputeAllIsBlockedInTx(ctx, rec); err != nil {
			t.Fatal(err)
		}
		if _, err := issueops.CountIsBlockedInconsistenciesInTx(ctx, rec); err != nil {
			t.Fatal(err)
		}
		if _, err := issueops.CountIsBlockedHeldInTx(ctx, rec); err != nil {
			t.Fatal(err)
		}
		checked, gated := 0, 0
		for _, st := range rec.stmts {
			switch {
			case strings.Contains(st.query, "FROM wisps psp"):
				// A statement that decides held rows reads a waits-for gate
				// only onto a present spawner, one lookup per edge.
				gated++
			case strings.Contains(st.query, "LEFT JOIN wisps dw"):
				checked++
			default:
				continue
			}
			assertWispsReadByKey(t, explainPlan(t, tx, st), "d.depends_on_wisp_id")
		}
		if checked == 0 {
			t.Fatal("recorded no statement that reads a dangling wisp edge")
		}
		if gated == 0 {
			t.Fatal("recorded no statement that gates a waits-for edge on a present spawner")
		}
	})
}

// assertWispsReadByKey fails a plan that reads wisps any way but a lookup by
// primary key keyed on key: a plain Table node over wisps is the full scan the
// NOT EXISTS spelling of the dangling-edge test planned. It also fails a semi-
// or anti-join, the shape the engine unnests `id IN (set)` into when the set
// is one SELECT: its exists iterator runs the set again for every outer row
// (see danglingBlockerTypesSQL).
func assertWispsReadByKey(t *testing.T, plan, key string) {
	t.Helper()
	if strings.Contains(plan, "SemiJoin") || strings.Contains(plan, "AntiJoin") {
		t.Errorf("plan runs a set per outer row in a semi- or anti-join:\n%s", plan)
	}
	lines := strings.Split(plan, "\n")
	for i, line := range lines {
		if strings.HasSuffix(strings.TrimSpace(line), "Table") && i+1 < len(lines) &&
			strings.Contains(lines[i+1], "name: wisps") {
			t.Errorf("plan scans wisps:\n%s", plan)
			return
		}
	}
	if !strings.Contains(plan, "IndexedTableAccess(wisps)") || !strings.Contains(plan, "keys: "+key) {
		t.Errorf("plan does not look wisps up by %s:\n%s", key, plan)
	}
}

// wispPlanCount is how many wisps wispEdgeInserts adds, and wispEdgeHolderCount
// how many of the graph's issues it gives wisp edges: three blocks edges each
// onto wisps that are here, and, for the first wispEdgeDangling of them, one
// more onto a wisp id with no row, the edge a clone holds without its wisp.
const (
	wispPlanCount       = 2000
	wispEdgeHolderCount = 60
	wispEdgeDangling    = 5
	wispEdgeFirstHolder = 1000
)

func wispPlanID(prefix string, i int) string { return fmt.Sprintf("%s-w-%04d", prefix, i) }

// wispEdgeHolders lists the issues wispEdgeInserts gives wisp edges.
func wispEdgeHolders(prefix string) []string {
	out := make([]string, 0, wispEdgeHolderCount)
	for i := 0; i < wispEdgeHolderCount; i++ {
		out = append(out, batchfixtures.LargeGraphID(prefix, wispEdgeFirstHolder+i))
	}
	return out
}

// wispEdgeInserts adds wispPlanCount open wisps and the wisp edges of
// wispEdgeHolders to a batchfixtures.LargeGraphInserts graph of at least
// wispEdgeFirstHolder+wispEdgeHolderCount issues, as raw rows.
func wispEdgeInserts(prefix string, issues int) []batchfixtures.Statement {
	if issues < wispEdgeFirstHolder+wispEdgeHolderCount {
		panic("wispEdgeInserts: graph too small")
	}
	var out []batchfixtures.Statement
	for start := 0; start < wispPlanCount; start += 500 {
		var values []string
		var args []any
		for i := start; i < min(start+500, wispPlanCount); i++ {
			values = append(values, "(?, ?)")
			args = append(args, wispPlanID(prefix, i), "plan wisp")
		}
		out = append(out, batchfixtures.Statement{
			SQL:  "INSERT INTO wisps (id, title) VALUES " + strings.Join(values, ", "),
			Args: args,
		})
	}
	var values []string
	var args []any
	for k, holder := range wispEdgeHolders(prefix) {
		targets := []string{
			wispPlanID(prefix, (k*37)%wispPlanCount),
			wispPlanID(prefix, (k*37+11)%wispPlanCount),
			wispPlanID(prefix, (k*37+23)%wispPlanCount),
		}
		if k < wispEdgeDangling {
			targets = append(targets, wispPlanID(prefix, wispPlanCount+k))
		}
		for _, target := range targets {
			values = append(values, "(?, ?, ?, 'blocks', 'fixture', '2026-01-01 00:00:00', '{}')")
			args = append(args, "wp-"+holder+"-"+target, holder, target)
		}
	}
	return append(out, batchfixtures.Statement{
		SQL: "INSERT INTO dependencies (id, issue_id, depends_on_wisp_id, type, created_by, created_at, metadata) VALUES " +
			strings.Join(values, ", "),
		Args: args,
	})
}
