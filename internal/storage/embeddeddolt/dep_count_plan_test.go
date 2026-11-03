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
	publicops "github.com/steveyegge/beads/issueops"
)

// depTableScan and depIndexAccess match the plan nodes that read a dependency
// table: a full table access ("name: dependencies" under a Table node) or an
// index access, whose index columns and ranges follow on the next lines.
var (
	depTableScan   = regexp.MustCompile(`[├└]─ name: (wisp_)?dependencies$`)
	depIndexAccess = regexp.MustCompile(`IndexedTableAccess\((wisp_)?dependencies\)`)
	planIndexCols  = regexp.MustCompile(`index: \[([^\]]*)\]`)
	planRangeGroup = regexp.MustCompile(`\{([^}]*)\}`)
	planInterval   = regexp.MustCompile(`[\[(][^\])]*[\])]`)
	wispExtFilter  = regexp.MustCompile(`depends_on_external (= |HASH IN)`)
)

// unboundedDepReads returns the plan nodes that read a dependency table
// without a bounded key: a full table scan, or an index range whose leading
// prefix is open (a NULL or open interval before any column but `type` is
// pinned, which reads every row of a type or the whole index). Lookup-join
// probes carry "keys:" rather than ranges and are bounded by the outer row.
//
// wispExternal holds the one such read the schema leaves no bounded path for:
// the untyped depends_on_external arm on wisp_dependencies, whose only indexes
// on that column lead with `type` or issue_id (0021_create_wisp_auxiliary; the
// durable table has idx_dep_external_target). The base reads the whole table
// there too; bounding it needs an index migration, outside this change.
func unboundedDepReads(plan string) (bad, wispExternal []string) {
	lines := strings.Split(plan, "\n")
	for i, line := range lines {
		if depTableScan.MatchString(line) {
			if strings.HasSuffix(line, "wisp_dependencies") && filteredOnWispExternal(lines[max(0, i-3):i]) {
				wispExternal = append(wispExternal, strings.TrimSpace(line))
				continue
			}
			bad = append(bad, strings.TrimSpace(line))
			continue
		}
		if !depIndexAccess.MatchString(line) {
			continue
		}
		var cols []string
		for _, next := range lines[i+1 : min(i+5, len(lines))] {
			if m := planIndexCols.FindStringSubmatch(next); m != nil {
				cols = strings.Split(m[1], ",")
			}
			if !strings.Contains(next, "filters:") {
				continue
			}
			for _, group := range planRangeGroup.FindAllStringSubmatch(next, -1) {
				intervals := planInterval.FindAllString(group[1], -1)
				if rangeGroupBounded(cols, intervals) {
					continue
				}
				read := strings.TrimSpace(line) + " " + strings.Join(cols, ",") + " " + group[0]
				if strings.Join(cols, ",") == "wisp_dependencies.type,wisp_dependencies.depends_on_external" && rangeGroupBounded(cols[1:], intervals[1:]) {
					wispExternal = append(wispExternal, read)
				} else {
					bad = append(bad, read)
				}
				break
			}
			break
		}
	}
	return bad, wispExternal
}

// filteredOnWispExternal reports whether the Filter above a table node keys
// on depends_on_external.
func filteredOnWispExternal(above []string) bool {
	for _, line := range above {
		if wispExtFilter.MatchString(line) {
			return true
		}
	}
	return false
}

// rangeGroupBounded reports whether one index range pins a leading prefix of
// the index, through at least one column other than `type`, to concrete
// values. A later column's bound does not count once an earlier one is open:
// Dolt reads the whole open prefix and filters.
func rangeGroupBounded(cols, intervals []string) bool {
	for k, iv := range intervals {
		if strings.Contains(iv, "NULL") || strings.Contains(iv, "∞") {
			return false
		}
		if k >= len(cols) || !strings.HasSuffix(cols[k], ".type") {
			return true
		}
	}
	return false
}

func explainCountPlan(t *testing.T, tx *sql.Tx, query string, args []any) string {
	t.Helper()
	rows, err := tx.QueryContext(context.Background(), "EXPLAIN PLAN "+query, args...)
	if err != nil {
		t.Fatalf("EXPLAIN PLAN: %v\n%s", err, query)
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
	if !strings.Contains(plan, "IndexedTableAccess(") && !strings.Contains(plan, "Table\n") {
		t.Skipf("EXPLAIN output not in a recognized Dolt plan format, skipping plan assertions; plan=\n%s", plan)
	}
	return plan
}

// TestDependencyCountPlansSeekTargetIndexes pins that every target-keyed read
// the five dependency-count paths issue seeks a target index: a COALESCE over
// the three target columns has no index path, and an OR across them does not
// index-merge on Dolt, so either one scans the whole dependency table per
// count. Each path is driven through its production entry point with a
// statementRecorder, so the plans checked are the statements actually sent.
func TestDependencyCountPlansSeekTargetIndexes(t *testing.T) {
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
	id := func(i int) string { return batchfixtures.LargeGraphID("cp", i) }
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	stmts := batchfixtures.LargeGraphInserts("cp", n)
	stmts = append(stmts, wispPlaneInserts(id, 300)...)
	for _, st := range stmts {
		if _, err := tx.ExecContext(ctx, st.SQL, st.Args...); err != nil {
			_ = tx.Rollback()
			t.Fatalf("%v\n%s", err, st.SQL)
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

	target := id(37)
	batch := []string{id(5), id(37), id(1500), "cp-w00007"}

	// typed says whether the counted statements carry a type filter, which
	// gives the wisp external arm its (type, depends_on_external) seek: then
	// no read may stay unbounded.
	assertBounded := func(t *testing.T, stmts []recordedStatement, typed bool) {
		t.Helper()
		checked := 0
		for _, st := range stmts {
			if !strings.Contains(st.query, "dependencies") {
				continue
			}
			checked++
			plan := explainCountPlan(t, tx, st.query, st.args)
			bad, wispExternal := unboundedDepReads(plan)
			if typed {
				bad = append(bad, wispExternal...)
			} else if len(wispExternal) > 0 {
				t.Logf("schema gap, not a regression: the untyped wisp_dependencies.depends_on_external arm reads unbounded %v", wispExternal)
			}
			if len(bad) > 0 {
				t.Errorf("target-keyed count reads a dependency table unbounded %v:\n%s\nplan:\n%s", bad, st.query, plan)
			}
		}
		if checked == 0 {
			t.Fatal("recorded no dependency-table statement")
		}
	}

	// The check must reject the shapes it exists to keep out.
	t.Run("rejects COALESCE and OR target filters", func(t *testing.T) {
		for _, q := range []string{
			"SELECT count(*) FROM dependencies WHERE " + sqlbuild.DepTargetExpr + " = ?",
			"SELECT count(*) FROM dependencies WHERE (depends_on_issue_id = ? OR depends_on_wisp_id = ? OR depends_on_external = ?)",
		} {
			args := []any{target}
			if strings.Contains(q, " OR ") {
				args = []any{target, target, target}
			}
			if bad, _ := unboundedDepReads(explainCountPlan(t, tx, q, args)); len(bad) == 0 {
				t.Errorf("check accepted an unindexable target filter:\n%s", q)
			}
		}
	})

	t.Run("CountDependencyEdgesInTx", func(t *testing.T) {
		for _, filter := range [][]types.DependencyType{nil, {types.DepBlocks}} {
			rec := &statementRecorder{DBTX: tx}
			if _, err := issueops.CountDependencyEdgesInTx(ctx, rec, target, domain.DepDirectionIn, filter); err != nil {
				t.Fatal(err)
			}
			assertBounded(t, rec.stmts, filter != nil)
		}
	})

	t.Run("SearchCountsSQL by IDs", func(t *testing.T) {
		for _, tables := range []sqlbuild.FilterTables{sqlbuild.IssuesFilterTables, sqlbuild.WispsFilterTables} {
			q, args := sqlbuild.SearchCountsSQL(tables, batch, "", "", "", true, sqlbuild.CountsHydration{})
			assertBounded(t, []recordedStatement{{q, args}}, true)
		}
	})

	t.Run("CountDependentsByStatusInTx", func(t *testing.T) {
		rec := &statementRecorder{DBTX: tx}
		if _, err := issueops.CountDependentsByStatusInTx(ctx, rec, target, types.StatusOpen); err != nil {
			t.Fatal(err)
		}
		assertBounded(t, rec.stmts, false)
	})

	t.Run("GetDependencyCountsInTx", func(t *testing.T) {
		rec := &statementRecorder{DBTX: tx}
		if _, err := issueops.GetDependencyCountsInTx(ctx, rec, batch); err != nil {
			t.Fatal(err)
		}
		var dependents []recordedStatement
		for _, st := range rec.stmts {
			if !strings.Contains(st.query, "WHERE issue_id IN") {
				dependents = append(dependents, st)
			}
		}
		assertBounded(t, dependents, true)
	})

	t.Run("ExecuteEdgeCount inbound", func(t *testing.T) {
		for _, req := range []publicops.EdgeCountRequest{
			{IDs: batch, Direction: publicops.EdgeDirectionIn},
			{IDs: batch, Direction: publicops.EdgeDirectionIn, Types: []types.DependencyType{types.DepBlocks}},
			{IDs: batch, Direction: publicops.EdgeDirectionIn, Status: string(types.StatusOpen)},
		} {
			rec := &statementRecorder{DBTX: tx}
			if _, err := issueops.ExecuteEdgeCount(ctx, rec, req); err != nil {
				t.Fatal(err)
			}
			assertBounded(t, rec.stmts, len(req.Types) > 0)
		}
	})
}

// wispPlaneInserts adds wisps whose wisp_dependencies rows target durable
// issues, other wisps and external references, so the wisp plane's target
// columns are all populated and a lookup has unrelated rows to skip.
func wispPlaneInserts(durableID func(int) string, wisps int) []batchfixtures.Statement {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	wispID := func(i int) string { return fmt.Sprintf("cp-w%05d", i) }
	var values, deps []string
	var args, depArgs []any
	for i := 0; i < wisps; i++ {
		values = append(values, "(?, ?, '', '', '', '', 'open', 2, 'task', ?, ?)")
		args = append(args, wispID(i), fmt.Sprintf("wisp %d", i), at, at)
		var issueTarget, wispTarget, externalTarget any
		switch i % 3 {
		case 0:
			issueTarget = durableID(i)
		case 1:
			wispTarget = wispID(i / 2)
		default:
			externalTarget = fmt.Sprintf("external:cp:%d", i)
		}
		deps = append(deps, "(?, ?, ?, ?, ?, 'blocks', 'fixture', ?, '{}')")
		depArgs = append(depArgs, fmt.Sprintf("wd-%05d", i), wispID(i), issueTarget, wispTarget, externalTarget, at)
	}
	return []batchfixtures.Statement{
		{SQL: "INSERT INTO wisps (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, created_at, updated_at) VALUES " + strings.Join(values, ", "), Args: args},
		{SQL: "INSERT INTO wisp_dependencies (id, issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external, type, created_by, created_at, metadata) VALUES " + strings.Join(deps, ", "), Args: depArgs},
	}
}
