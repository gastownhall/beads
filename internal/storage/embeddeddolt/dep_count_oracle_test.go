//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/storage/sqlcount"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// oracleTarget is the COALESCE target expression the dependency counts
// filtered on before they matched one target column at a time; the oracles
// below are those statements, so each rewritten count must answer exactly what
// they answer.
const oracleTarget = "COALESCE(d.depends_on_issue_id, d.depends_on_wisp_id, d.depends_on_external)"

// TestDependencyCountsMatchCoalesceOracle pins that matching the target one
// column at a time (sqlbuild.DepTargetArms) counts the same rows as the
// COALESCE it replaced, for every rewritten count path. The fixture holds edges
// to durable, wisp and external targets on both planes, mixed types and
// statuses, a target nothing depends on, an id nothing holds, and a source id
// present on both planes whose wisp edge duplicates its durable one under
// another type (the durable row is the edge). A row naming two targets is not
// seeded: ck_dep_one_target and ck_wisp_dep_one_target refuse it.
func TestDependencyCountsMatchCoalesceOracle(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	fixture := newPristineEmbeddedDoltFixture(t, "countoracle")
	t.Cleanup(func() { closeEmbeddedDoltStore(t, fixture.store) })
	db, cleanup, err := openCountedConn(t.Context(), fixture.dataDir, fixture.database, &sqlcount.Counts{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	seedOracleGraph(t, tx)

	const ext = "external:co:x"
	ids := []string{"co-1", "co-2", "co-3", "co-4", "co-5", "co-w1", "co-w2", "co-w3", "co-dup", ext, "co-missing"}
	filters := [][]types.DependencyType{nil, {types.DepBlocks}, {types.DepRelated, types.DepParentChild}}
	nonZero := 0
	check := func(t *testing.T, what string, got, want int64) {
		t.Helper()
		if got != want {
			t.Errorf("%s = %d, COALESCE oracle %d", what, got, want)
		}
		if want != 0 {
			nonZero++
		}
	}

	t.Run("CountDependencyEdgesInTx", func(t *testing.T) {
		for _, id := range ids {
			for _, dir := range []domain.DepDirection{domain.DepDirectionIn, domain.DepDirectionOut, domain.DepDirectionBoth} {
				for _, filter := range filters {
					got, err := issueops.CountDependencyEdgesInTx(ctx, tx, id, dir, filter)
					if err != nil {
						t.Fatal(err)
					}
					check(t, fmt.Sprintf("CountDependencyEdgesInTx(%s, %v, %v)", id, dir, filter), got, oracleEdgeCount(t, tx, id, dir, filter))
				}
			}
		}
	})

	t.Run("CountDependentsByStatusInTx", func(t *testing.T) {
		for _, id := range ids {
			for _, status := range []types.Status{types.StatusOpen, types.StatusClosed, types.StatusInProgress} {
				got, err := issueops.CountDependentsByStatusInTx(ctx, tx, id, status)
				if err != nil {
					t.Fatal(err)
				}
				var want int64
				for _, plane := range [][2]string{{"dependencies", "issues"}, {"wisp_dependencies", "wisps"}} {
					want += oracleCount(t, tx, fmt.Sprintf(
						"SELECT count(*) FROM %s d JOIN %s s ON s.id = d.issue_id WHERE %s = ? AND s.status = ?",
						plane[0], plane[1], oracleTarget), id, string(status))
				}
				check(t, fmt.Sprintf("CountDependentsByStatusInTx(%s, %s)", id, status), got, want)
			}
		}
	})

	t.Run("GetDependencyCountsInTx", func(t *testing.T) {
		got, err := issueops.GetDependencyCountsInTx(ctx, tx, ids)
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range ids {
			var want int64
			for _, table := range []string{"dependencies", "wisp_dependencies"} {
				want += oracleCount(t, tx, fmt.Sprintf(
					"SELECT count(*) FROM %s d WHERE %s = ? AND d.type = 'blocks'", table, oracleTarget), id)
			}
			check(t, "GetDependencyCountsInTx["+id+"].DependentCount", int64(got[id].DependentCount), want)
		}
	})

	// The predicate form still reads the reverse blockers through the
	// COALESCE, so its rdep_count is the oracle for the by-IDs form's.
	t.Run("SearchCountsSQL by IDs", func(t *testing.T) {
		for _, tables := range []sqlbuild.FilterTables{sqlbuild.IssuesFilterTables, sqlbuild.WispsFilterTables} {
			for _, withWisps := range []bool{false, true} {
				q, args := sqlbuild.SearchCountsSQL(tables, ids, "", "", "", withWisps, sqlbuild.CountsHydration{})
				got := rdepCounts(t, tx, q, args)
				q, args = sqlbuild.SearchCountsSQL(tables, nil, "", "", "", withWisps, sqlbuild.CountsHydration{})
				want := rdepCounts(t, tx, q, args)
				for id, n := range got {
					check(t, fmt.Sprintf("%s rdep_count[%s] (wisps %v)", tables.Main, id, withWisps), n, want[id])
				}
				if len(got) == 0 {
					t.Errorf("%s by-IDs search returned no rows", tables.Main)
				}
			}
		}
	})

	t.Run("ExecuteEdgeCount inbound", func(t *testing.T) {
		var requests []publicops.EdgeCountRequest
		for _, filter := range filters {
			for _, status := range []string{"", string(types.StatusOpen), string(types.StatusClosed)} {
				requests = append(requests, publicops.EdgeCountRequest{IDs: ids, Direction: publicops.EdgeDirectionIn, Types: filter, Status: status})
			}
		}
		for _, req := range requests {
			res, err := issueops.ExecuteEdgeCount(ctx, tx, req)
			if err != nil {
				t.Fatal(err)
			}
			want := oracleInboundEdgeCounts(t, tx, req)
			for _, a := range res.Anchors {
				// A missing anchor (an external reference holds no row)
				// counts nothing by contract (FinishEdgeCount), on either side
				// of the rewrite.
				if a.Missing {
					check(t, fmt.Sprintf("ExecuteEdgeCount(missing %s)", a.ID), a.Count, 0)
					continue
				}
				check(t, fmt.Sprintf("ExecuteEdgeCount(%s, types %v, status %q)", a.ID, req.Types, req.Status), a.Count, want[a.ID])
			}
		}
	})

	if nonZero == 0 {
		t.Fatal("every oracle answered zero: the fixture exercises nothing")
	}
}

// seedOracleGraph inserts the hand-made graph the oracle test compares on.
func seedOracleGraph(t *testing.T, tx *sql.Tx) {
	t.Helper()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rowsOf := func(table string, rows [][2]string) {
		for _, r := range rows {
			if _, err := tx.Exec("INSERT INTO "+table+" (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, created_at, updated_at) VALUES (?, ?, '', '', '', '', ?, 2, 'task', ?, ?)",
				r[0], r[0], r[1], at, at); err != nil {
				t.Fatalf("seed %s %s: %v", table, r[0], err)
			}
		}
	}
	rowsOf("issues", [][2]string{{"co-1", "open"}, {"co-2", "open"}, {"co-3", "closed"}, {"co-4", "in_progress"}, {"co-5", "open"}, {"co-dup", "open"}})
	rowsOf("wisps", [][2]string{{"co-w1", "open"}, {"co-w2", "closed"}, {"co-w3", "open"}, {"co-dup", "closed"}})

	// Each edge names its one target in the column its kind owns.
	type edge struct{ id, from, issue, wisp, external, typ string }
	edgesOf := func(table string, edges []edge) {
		for _, e := range edges {
			target := func(v string) any {
				if v == "" {
					return nil
				}
				return v
			}
			if _, err := tx.Exec("INSERT INTO "+table+" (id, issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external, type, created_by, created_at, metadata) VALUES (?, ?, ?, ?, ?, ?, 'fixture', ?, '{}')",
				e.id, e.from, target(e.issue), target(e.wisp), target(e.external), e.typ, at); err != nil {
				t.Fatalf("seed %s %s: %v", table, e.id, err)
			}
		}
	}
	edgesOf("dependencies", []edge{
		{id: "cd-1", from: "co-2", issue: "co-1", typ: "blocks"},
		{id: "cd-2", from: "co-3", issue: "co-1", typ: "blocks"},
		{id: "cd-3", from: "co-4", issue: "co-1", typ: "related"},
		{id: "cd-4", from: "co-1", wisp: "co-w1", typ: "blocks"},
		{id: "cd-5", from: "co-2", wisp: "co-w1", typ: "parent-child"},
		{id: "cd-6", from: "co-3", external: "external:co:x", typ: "blocks"},
		{id: "cd-7", from: "co-4", external: "external:co:x", typ: "related"},
		{id: "cd-8", from: "co-2", issue: "co-4", typ: "parent-child"},
		{id: "cd-9", from: "co-dup", issue: "co-1", typ: "blocks"},
	})
	edgesOf("wisp_dependencies", []edge{
		{id: "cw-1", from: "co-w1", issue: "co-1", typ: "blocks"},
		{id: "cw-2", from: "co-w2", issue: "co-1", typ: "related"},
		{id: "cw-3", from: "co-w3", wisp: "co-w1", typ: "blocks"},
		{id: "cw-4", from: "co-w2", external: "external:co:x", typ: "blocks"},
		{id: "cw-5", from: "co-w3", issue: "co-4", typ: "blocks"},
		// co-dup's durable edge to co-1 is a blocks row: this one is the same
		// edge, so a distinct count counts it once, under any type filter.
		{id: "cw-6", from: "co-dup", issue: "co-1", typ: "related"},
	})
}

func oracleCount(t *testing.T, tx *sql.Tx, q string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := tx.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("oracle: %v\n%s", err, q)
	}
	return n
}

// oracleTypeClause is buildDepTypeClause's " AND d.type IN (...)" form.
func oracleTypeClause(filter []types.DependencyType) (string, []any) {
	if len(filter) == 0 {
		return "", nil
	}
	args := make([]any, len(filter))
	for i, f := range filter {
		args[i] = string(f)
	}
	return " AND d.type IN (" + strings.TrimSuffix(strings.Repeat("?,", len(filter)), ",") + ")", args
}

// oracleEdgeCount is CountDependencyEdgesInTx as it read before the rewrite:
// the durable rows on the anchor, plus the wisp rows on it whose far end the
// durable plane does not already hold, with the anchor matched by COALESCE.
func oracleEdgeCount(t *testing.T, tx *sql.Tx, id string, dir domain.DepDirection, filter []types.DependencyType) int64 {
	t.Helper()
	typeClause, typeArgs := oracleTypeClause(filter)
	distinct := func(anchor, far, farGuard string) int64 {
		args := append([]any{id}, typeArgs...)
		n := oracleCount(t, tx, "SELECT count(*) FROM dependencies d WHERE "+anchor+typeClause, args...)
		args = append([]any{id, id}, typeArgs...)
		return n + oracleCount(t, tx, fmt.Sprintf(
			"SELECT count(*) FROM wisp_dependencies d WHERE %s AND %s NOT IN (SELECT %s FROM dependencies d WHERE %s%s)%s",
			anchor, far, far, anchor, farGuard, typeClause), args...)
	}
	var total int64
	if dir == domain.DepDirectionOut || dir == domain.DepDirectionBoth {
		total += distinct("d.issue_id = ?", oracleTarget, " AND "+oracleTarget+" IS NOT NULL")
	}
	if dir == domain.DepDirectionIn || dir == domain.DepDirectionBoth {
		total += distinct(oracleTarget+" = ?", "d.issue_id", "")
	}
	return total
}

// oracleInboundEdgeCounts is ExecuteEdgeCount's inbound tally as it read
// before the rewrite: per plane, grouped by the COALESCE target, summed.
func oracleInboundEdgeCounts(t *testing.T, tx *sql.Tx, req publicops.EdgeCountRequest) map[string]int64 {
	t.Helper()
	typeClause, typeArgs := oracleTypeClause(req.Types)
	in := strings.TrimSuffix(strings.Repeat("?,", len(req.IDs)), ",")
	args := make([]any, 0, len(req.IDs)+len(typeArgs)+1)
	for _, id := range req.IDs {
		args = append(args, id)
	}
	args = append(args, typeArgs...)
	counts := map[string]int64{}
	for _, plane := range [][2]string{{"dependencies", "issues"}, {"wisp_dependencies", "wisps"}} {
		join, status := "", ""
		planeArgs := args
		if req.Status != "" {
			join, status = " JOIN "+plane[1]+" s ON s.id = d.issue_id", " AND s.status = ?"
			planeArgs = append(append([]any{}, args...), req.Status)
		}
		rows, err := tx.Query(fmt.Sprintf("SELECT %s AS anchor, COUNT(*) FROM %s d%s WHERE %s IN (%s)%s%s GROUP BY anchor",
			oracleTarget, plane[0], join, oracleTarget, in, typeClause, status), planeArgs...)
		if err != nil {
			t.Fatalf("oracle: %v", err)
		}
		for rows.Next() {
			var id string
			var n int64
			if err := rows.Scan(&id, &n); err != nil {
				t.Fatal(err)
			}
			counts[id] += n
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return counts
}

// rdepCounts runs a SearchCountsSQL statement and returns its rdep_count per
// issue id.
func rdepCounts(t *testing.T, tx *sql.Tx, q string, args []any) map[string]int64 {
	t.Helper()
	rows, err := tx.Query(q, args...)
	if err != nil {
		t.Fatalf("search counts: %v\n%s", err, q)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	idCol, rdepCol := -1, -1
	for i, c := range cols {
		switch c {
		case "id":
			if idCol < 0 {
				idCol = i
			}
		case "rdep_count":
			rdepCol = i
		}
	}
	if idCol < 0 || rdepCol < 0 {
		t.Fatalf("search counts projects no id or rdep_count column: %v", cols)
	}
	out := map[string]int64{}
	for rows.Next() {
		vals := make([]any, len(cols))
		for i := range vals {
			vals[i] = new(sql.RawBytes)
		}
		if err := rows.Scan(vals...); err != nil {
			t.Fatal(err)
		}
		var n int64
		if _, err := fmt.Sscan(string(*vals[rdepCol].(*sql.RawBytes)), &n); err != nil {
			t.Fatalf("rdep_count: %v", err)
		}
		out[string(*vals[idCol].(*sql.RawBytes))] = n
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
