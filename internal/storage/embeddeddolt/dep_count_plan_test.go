//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/batchfixtures"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/storage/sqlcount"
	"github.com/steveyegge/beads/internal/types"
)

// edgeAccess is one read of a dependency table in an EXPLAIN PLAN: the index
// it ranges over and that range, or a full table read (index empty).
type edgeAccess struct {
	table, index, filters string
}

var planTreePrefix = regexp.MustCompile(`^[\s│├└─]*`)

// edgeAccesses lists every read of dependencies or wisp_dependencies in plan.
func edgeAccesses(plan string) []edgeAccess {
	lines := strings.Split(plan, "\n")
	field := func(from int, key string) string {
		for _, l := range lines[from:min(from+4, len(lines))] {
			if v, ok := strings.CutPrefix(planTreePrefix.ReplaceAllString(l, ""), key+": "); ok {
				return v
			}
		}
		return ""
	}
	var out []edgeAccess
	for i, l := range lines {
		node := planTreePrefix.ReplaceAllString(l, "")
		for _, table := range []string{"dependencies", "wisp_dependencies"} {
			switch {
			case node == "IndexedTableAccess("+table+")":
				out = append(out, edgeAccess{table, field(i+1, "index"), field(i+1, "filters")})
			case node == "Table" && field(i+1, "name") == table:
				out = append(out, edgeAccess{table: table})
			}
		}
	}
	return out
}

// explainEdgePlan is explainPlan for a statement that reads the dependency
// tables: a plan that is a full scan, with no indexed access anywhere, is the
// failure under test here rather than an unrecognized format. A plan in which
// no dependency-table read is recognized at all fails rather than skips: a
// skip would silently pass a supported engine's scan plan, the very shape
// these gates exist to catch (gascity#7666).
func explainEdgePlan(t *testing.T, tx *sql.Tx, st recordedStatement) string {
	t.Helper()
	plan := explainPlanText(t, tx, st)
	if len(edgeAccesses(plan)) == 0 {
		t.Fatalf("EXPLAIN plan has no recognized read of a dependency table; plan=\n%s", plan)
	}
	return plan
}

// targetLed reports whether a reads an index a target column leads, alone or
// after type, rather than one that only carries it after issue_id.
func (a edgeAccess) targetLed() bool {
	return strings.HasPrefix(a.index, "["+a.table+".depends_on_") ||
		strings.HasPrefix(a.index, "["+a.table+".type,"+a.table+".depends_on_")
}

// unseekableTargets counts, per dependency table, the target columns no index
// of that table leads with. A branch matching such a column untyped has no
// index to seek: wisp_dependencies created by migration 0021 has none on
// depends_on_external (only a database that went through 0047 does), and
// adding one is a schema change.
func unseekableTargets(t *testing.T, tx *sql.Tx) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, table := range []string{"dependencies", "wisp_dependencies"} {
		for _, col := range []string{"depends_on_issue_id", "depends_on_wisp_id", "depends_on_external"} {
			var n int
			if err := tx.QueryRowContext(context.Background(),
				`SELECT count(*) FROM information_schema.statistics
				 WHERE table_schema = database() AND table_name = ? AND column_name = ? AND seq_in_index = 1`,
				table, col).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n == 0 {
				out[table]++
				t.Logf("%s has no index leading with %s: an untyped match on it reads the table", table, col)
			}
		}
	}
	return out
}

// assertTargetSeeks checks that plan reads the dependency tables exactly want
// times, each an index range bounded to target on a target column (a bare
// target index or the (type, target) one), never a table scan, a scan of the
// type prefix alone, or a range over a precedence guard's NULLs. The one
// exception is the reads unseekable allows per table, for a target column the
// table has no index on at all.
func assertTargetSeeks(t *testing.T, plan, target string, want int, unseekable map[string]int) {
	t.Helper()
	accesses := edgeAccesses(plan)
	if len(accesses) != want {
		t.Errorf("plan reads the dependency tables %d times, want %d:\n%s", len(accesses), want, plan)
	}
	bounded := "[" + target + ", " + target + "]"
	unbounded := map[string]int{}
	for _, a := range accesses {
		if !a.targetLed() || !strings.Contains(a.filters, bounded) {
			unbounded[a.table]++
			if unbounded[a.table] > unseekable[a.table] {
				t.Errorf("%s read is not a target-bounded index range (index %q, filters %q):\n%s", a.table, a.index, a.filters, plan)
			}
		}
	}
}

// TestDependentCountPlansUseTargetIndexes proves the reverse-dependency counts
// (CountDependencyEdgesInTx's In leg, typed and untyped, and
// CountDependentsByStatusInTx) seek the dependency tables by target. Matching
// the COALESCE'd target expression instead left Dolt nothing to seek, so every
// count read every edge in both planes (gascity#7666). The plans are checked
// twice: first with the wisp plane empty and without statistics, the case
// where Dolt is likeliest to range over a precedence guard's NULLs instead,
// then with every target column of both planes populated.
func TestDependentCountPlansUseTargetIndexes(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	fixture := newPristineEmbeddedDoltFixture(t, "depcountplan")
	t.Cleanup(func() { closeEmbeddedDoltStore(t, fixture.store) })
	db, cleanup, err := openCountedConn(t.Context(), fixture.dataDir, fixture.database, &sqlcount.Counts{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	ctx := context.Background()
	const n = 2000
	id := func(i int) string { return batchfixtures.LargeGraphID("dc", i) }
	exec := func(stmts ...batchfixtures.Statement) {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, st := range stmts {
			if _, err := tx.ExecContext(ctx, st.SQL, st.Args...); err != nil {
				_ = tx.Rollback()
				t.Fatal(err)
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	readTx := func(t *testing.T) *sql.Tx {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = tx.Rollback() })
		return tx
	}
	// Durable backdrop: issue i blocks on (i-1)/2, plus a back edge, all
	// targeting depends_on_issue_id and all sources open.
	exec(batchfixtures.LargeGraphInserts("dc", n)...)

	t.Run("empty wisp plane", func(t *testing.T) {
		tx := readTx(t)
		assertDependentCountPlans(t, tx, id(n/3), unseekableTargets(t, tx), nil)
	})

	// Populate every target column of both planes for id(0): dual-home it
	// into wisps so depends_on_wisp_id rows satisfy the foreign key, then 120
	// durable rows on depends_on_wisp_id and 120 on depends_on_external
	// (whose value is the local id itself, the collision case), from disjoint
	// sources, and 120 wisp-plane rows from wisp sources. Types cycle
	// blocks/waits-for/related.
	const handRows = 120
	target := id(0)
	depTypes := []string{string(types.DepBlocks), string(types.DepWaitsFor), string(types.DepRelated)}
	populate := []batchfixtures.Statement{{
		SQL:  `INSERT INTO wisps (id, title, status) VALUES (?, ?, ?)`,
		Args: []any{target, "dual-homed wisp row", string(types.StatusOpen)},
	}}
	for kind, targetCol := range []string{"depends_on_wisp_id", "depends_on_external"} {
		for i := 0; i < handRows; i++ {
			populate = append(populate, batchfixtures.Statement{
				SQL:  `INSERT INTO dependencies (id, issue_id, ` + targetCol + `, type, created_by) VALUES (?, ?, ?, ?, 'tester')`,
				Args: []any{fmt.Sprintf("dc-k%d-%04d", kind, i), id(1000 + kind*handRows + i), target, depTypes[i%3]},
			})
		}
	}
	for i := 0; i < handRows; i++ {
		source := fmt.Sprintf("dcw-%03d", i)
		populate = append(populate,
			batchfixtures.Statement{
				SQL:  `INSERT INTO wisps (id, title, status) VALUES (?, ?, ?)`,
				Args: []any{source, fmt.Sprintf("wisp source %d", i), string(types.StatusOpen)},
			},
			batchfixtures.Statement{
				SQL:  `INSERT INTO wisp_dependencies (id, issue_id, depends_on_issue_id, type, created_by) VALUES (?, ?, ?, ?, 'tester')`,
				Args: []any{fmt.Sprintf("dc-ww-%04d", i), source, target, depTypes[i%3]},
			})
	}
	exec(populate...)

	t.Run("populated target columns", func(t *testing.T) {
		tx := readTx(t)
		// Durable: the two backdrop edges into id(0) (both blocks) plus
		// 120 + 120; wisp: 120. The blocks filter keeps every third hand row.
		assertDependentCountPlans(t, tx, target, unseekableTargets(t, tx), &dependentCounts{edges: 362, blocksEdges: 122, open: 362})
	})
}

// dependentCounts are the counts a populated fixture expects, checked so the
// plans were explained against the rows they were meant to see.
type dependentCounts struct{ edges, blocksEdges, open int64 }

// assertDependentCountPlans explains every statement the changed inbound
// counts issue for target and checks each dependency-table read with
// assertTargetSeeks. Timings are logged as report-only evidence.
func assertDependentCountPlans(t *testing.T, tx *sql.Tx, target string, unseekable map[string]int, want *dependentCounts) {
	t.Helper()
	ctx := context.Background()
	// Every target column has a (type, target) index in both tables, so a
	// typed count has a seek for every branch.
	typed := map[string]int{}

	t.Run("edge counts", func(t *testing.T) {
		for _, filter := range [][]types.DependencyType{nil, {types.DepBlocks}} {
			allowed := unseekable
			if filter != nil {
				allowed = typed
			}
			rec := &statementRecorder{DBTX: tx}
			start := time.Now()
			got, err := issueops.CountDependencyEdgesInTx(ctx, rec, target, domain.DepDirectionIn, filter)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("type filter %v: inbound edge count took %v", filter, time.Since(start))
			if want != nil {
				wantN := want.edges
				if filter != nil {
					wantN = want.blocksEdges
				}
				if got != wantN {
					t.Fatalf("type filter %v: CountDependencyEdgesInTx In(%s) = %d, want %d: fixture drifted", filter, target, got, wantN)
				}
			}
			if len(rec.stmts) != 2 {
				t.Fatalf("type filter %v: recorded %d statements, want 2 (durable, wisp)", filter, len(rec.stmts))
			}
			// The wisp count also reads the durable plane, for the rows it
			// already counted.
			assertTargetSeeks(t, explainEdgePlan(t, tx, rec.stmts[0]), target, 3, allowed)
			assertTargetSeeks(t, explainEdgePlan(t, tx, rec.stmts[1]), target, 6, allowed)
		}
	})

	t.Run("status counts", func(t *testing.T) {
		rec := &statementRecorder{DBTX: tx}
		start := time.Now()
		got, err := issueops.CountDependentsByStatusInTx(ctx, rec, target, types.StatusOpen)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("dependents-by-status count took %v", time.Since(start))
		if want != nil && got != want.open {
			t.Fatalf("CountDependentsByStatusInTx(%s, open) = %d, want %d: fixture drifted", target, got, want.open)
		}
		if len(rec.stmts) != 2 {
			t.Fatalf("recorded %d statements, want 2 (one per plane)", len(rec.stmts))
		}
		for _, st := range rec.stmts {
			plan := explainEdgePlan(t, tx, st)
			assertTargetSeeks(t, plan, target, 3, unseekable)
			// Each matched edge probes its home row by key, rather than the
			// join scanning the home table's status range or merging over
			// the whole edge table.
			if got := strings.Count(plan, "LookupJoin"); got != 3 {
				t.Errorf("plan has %d LookupJoin, want 3 (one per target column):\n%s", got, plan)
			}
			if got := strings.Count(plan, "keys: d.issue_id"); got != 3 {
				t.Errorf("plan probes the home table by d.issue_id %d times, want 3:\n%s", got, plan)
			}
		}
	})

	// The by-IDs search hydration counts each page row's blockers. Its other
	// subqueries read the edge tables by issue_id; the reverse-blocker one
	// must read them by target, one bounded range per column and plane.
	t.Run("search counts by ids", func(t *testing.T) {
		ids := []string{target, batchfixtures.LargeGraphID("dc", 5), batchfixtures.LargeGraphID("dc", 1500)}
		q, args := sqlbuild.SearchCountsSQL(sqlbuild.IssuesFilterTables, ids, "", "", "", true, sqlbuild.CountsHydration{})
		plan := explainEdgePlan(t, tx, recordedStatement{q, args})
		for _, a := range edgeAccesses(plan) {
			if a.index == "" {
				t.Errorf("plan reads %s with a table scan:\n%s", a.table, plan)
			}
		}
		seeks := 0
		for _, a := range edgeAccesses(plan) {
			if !a.targetLed() {
				continue
			}
			for _, id := range ids {
				if !strings.Contains(a.filters, "["+id+", "+id+"]") {
					t.Errorf("%s target read is not bounded to %s (index %q, filters %q):\n%s", a.table, id, a.index, a.filters, plan)
				}
			}
			seeks++
		}
		if seeks != 6 {
			t.Errorf("plan has %d target-column reads, want 6 (three columns, two planes):\n%s", seeks, plan)
		}

		// And the rewritten count is the count: each page row's rdep_count
		// is its 'blocks' dependents across both planes.
		rows, err := tx.QueryContext(ctx, "SELECT id, rdep_count FROM ("+q+") page", args...)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]int64{}
		for rows.Next() {
			var id string
			var n int64
			if err := rows.Scan(&id, &n); err != nil {
				t.Fatal(err)
			}
			got[id] = n
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		for _, id := range ids {
			var want int64
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM (
				SELECT `+sqlbuild.DepTargetExpr+` AS dep_id FROM dependencies WHERE type = 'blocks'
				UNION ALL SELECT `+sqlbuild.DepTargetExpr+` AS dep_id FROM wisp_dependencies WHERE type = 'blocks'
			) b WHERE dep_id = ?`, id).Scan(&want); err != nil {
				t.Fatal(err)
			}
			if got[id] != want {
				t.Errorf("rdep_count(%s) = %d, want %d", id, got[id], want)
			}
		}
	})
}
