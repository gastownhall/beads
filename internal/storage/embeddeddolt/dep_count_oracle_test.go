//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/storage/sqlcount"
	"github.com/steveyegge/beads/internal/types"
)

// The pre-union reverse-dependency counts, verbatim but for layout: each
// matched the COALESCE'd target. The per-column unions that replaced them
// (sqlbuild.DepTargetUnion) must count exactly what these did.
const (
	oracleTarget = "COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external)"

	oracleStatusDurable = `SELECT count(*) FROM dependencies d
		JOIN issues i ON i.id = d.issue_id
		WHERE COALESCE(d.depends_on_issue_id, d.depends_on_wisp_id, d.depends_on_external) = ? AND i.status = ?`
	oracleStatusWisp = `SELECT count(*) FROM wisp_dependencies d
		JOIN wisps w ON w.id = d.issue_id
		WHERE COALESCE(d.depends_on_issue_id, d.depends_on_wisp_id, d.depends_on_external) = ? AND w.status = ?`
	oracleBlockers = `SELECT count(*) FROM (
		SELECT ` + oracleTarget + ` AS dep_id FROM dependencies WHERE type = 'blocks'
		UNION ALL
		SELECT ` + oracleTarget + ` AS dep_id FROM wisp_dependencies WHERE type = 'blocks'
	) b WHERE dep_id = ?`
)

// oracleInEdges is the pre-union CountDependencyEdgesInTx In leg: the durable
// rows matching target, plus the wisp rows matching it whose issue_id the
// durable plane does not hold for target under any type.
func oracleInEdges(t *testing.T, conn *sql.Conn, target string, filter []types.DependencyType, withWisp bool) int64 {
	t.Helper()
	typeClause, typeArgs := "", []any{}
	if len(filter) > 0 {
		typeClause = " AND type IN (?" + strings.Repeat(",?", len(filter)-1) + ")"
		for _, ft := range filter {
			typeArgs = append(typeArgs, string(ft))
		}
	}
	ctx := context.Background()
	var durable, wisp int64
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM dependencies WHERE "+oracleTarget+" = ?"+typeClause,
		append([]any{target}, typeArgs...)...).Scan(&durable); err != nil {
		t.Fatal(err)
	}
	if !withWisp {
		return durable
	}
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM wisp_dependencies WHERE "+oracleTarget+" = ? AND issue_id NOT IN (SELECT issue_id FROM dependencies WHERE "+oracleTarget+" = ?)"+typeClause,
		append([]any{target, target}, typeArgs...)...).Scan(&wisp); err != nil {
		t.Fatal(err)
	}
	return durable + wisp
}

// TestDependentCountsMatchCoalesceOracle holds the per-column reverse-
// dependency counts (CountDependencyEdgesInTx In, CountDependentsByStatusInTx
// and SearchCountsSQL's by-IDs rdep_count) to the COALESCE queries they
// replaced, on rows chosen where the two could part: rows setting more than
// one target column (COALESCE precedence), an external target spelled like a
// local id, an edge held in both planes, mixed types and statuses, targets
// with no dependents, and a database without wisp_dependencies.
//
// Production schemas forbid a row with two target columns
// (ck_dep_one_target), so the fixture is a scratch database whose tables copy
// the production ones (CREATE TABLE ... LIKE) with the CHECK constraints
// dropped from the copy alone.
func TestDependentCountsMatchCoalesceOracle(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	fixture := newPristineEmbeddedDoltFixture(t, "depcountoracle")
	t.Cleanup(func() { closeEmbeddedDoltStore(t, fixture.store) })
	db, cleanup, err := openCountedConn(t.Context(), fixture.dataDir, fixture.database, &sqlcount.Counts{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := conn.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	const scratch = "depcount_oracle_scratch"
	var tables []string
	rows, err := conn.QueryContext(ctx, "SHOW FULL TABLES WHERE Table_type = 'BASE TABLE'")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name, kind string
		if err := rows.Scan(&name, &kind); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	exec("CREATE DATABASE " + scratch)
	for _, table := range tables {
		exec(fmt.Sprintf("CREATE TABLE `%s`.`%s` LIKE `%s`.`%s`", scratch, table, fixture.database, table))
	}
	exec("USE " + scratch)
	checks, err := conn.QueryContext(ctx, `SELECT table_name, constraint_name FROM information_schema.table_constraints
		WHERE constraint_schema = ? AND constraint_type = 'CHECK' AND table_name IN ('dependencies', 'wisp_dependencies')`, scratch)
	if err != nil {
		t.Fatal(err)
	}
	var drops []string
	for checks.Next() {
		var table, name string
		if err := checks.Scan(&table, &name); err != nil {
			t.Fatal(err)
		}
		drops = append(drops, fmt.Sprintf("ALTER TABLE `%s` DROP CHECK `%s`", table, name))
	}
	if err := checks.Close(); err != nil {
		t.Fatal(err)
	}
	for _, q := range drops {
		exec(q)
	}

	home := func(table, id, status string) {
		exec("INSERT INTO "+table+" (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, created_at, updated_at) VALUES (?, ?, '', '', '', '', ?, 2, 'task', NOW(), NOW())",
			id, id, status)
	}
	for _, h := range [][2]string{{"t1", "open"}, {"a", "open"}, {"b", "closed"}, {"c", "open"}, {"d", "open"}, {"e", "closed"}, {"g", "open"}, {"f", "open"}} {
		home("issues", h[0], h[1])
	}
	for _, h := range [][2]string{{"w1", "open"}, {"wa", "open"}, {"wb", "closed"}, {"wc", "open"}, {"wd", "closed"}} {
		home("wisps", h[0], h[1])
	}
	var nextEdge int
	edge := func(table, from string, issue, wisp, external any, depType string) {
		nextEdge++
		exec("INSERT INTO "+table+" (id, issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external, type, created_by, created_at, metadata) VALUES (?, ?, ?, ?, ?, ?, 'oracle', NOW(), '{}')",
			fmt.Sprintf("edge-%02d", nextEdge), from, issue, wisp, external, depType)
	}
	// Durable plane.
	edge("dependencies", "a", "t1", nil, nil, "blocks")
	edge("dependencies", "b", "t1", nil, nil, "blocks")        // closed dependent
	edge("dependencies", "c", nil, nil, "t1", "blocks")        // external spelled like a local id
	edge("dependencies", "c", "t1", nil, nil, "related")       // second type from the same dependent
	edge("dependencies", "d", "t1", "w1", nil, "blocks")       // issue column wins over wisp
	edge("dependencies", "e", nil, "w1", "t1", "parent-child") // wisp column wins over external
	edge("dependencies", "f", "t1", "w1", "t1", "blocks")      // all three set: issue wins
	edge("dependencies", "g", "t1", nil, nil, "related")       // durable holds g under another type
	edge("dependencies", "a", nil, nil, "ext:x", "blocks")     // unrelated external
	edge("dependencies", "f", nil, nil, nil, "blocks")         // no target at all
	edge("dependencies", "wc", "a", nil, nil, "blocks")        // a dependent with no home row
	// Wisp plane.
	edge("wisp_dependencies", "wa", "t1", nil, nil, "blocks")
	edge("wisp_dependencies", "wb", "t1", nil, nil, "blocks")       // closed wisp dependent
	edge("wisp_dependencies", "a", "t1", nil, nil, "blocks")        // held in both planes
	edge("wisp_dependencies", "g", "t1", nil, nil, "blocks")        // durable holds g as related
	edge("wisp_dependencies", "wc", "t1", nil, "t1", "related")     // issue wins over external
	edge("wisp_dependencies", "wd", nil, "w1", "t1", "blocks")      // wisp wins over external
	edge("wisp_dependencies", "wa", nil, "w1", nil, "parent-child") // wisp target
	edge("wisp_dependencies", "wc", nil, nil, "w1", "blocks")       // external spelled like a wisp id
	edge("wisp_dependencies", "wd", nil, nil, nil, "blocks")        // no target at all

	targets := []string{"t1", "w1", "a", "ext:x", "nope"}
	filters := [][]types.DependencyType{nil, {types.DepBlocks}, {types.DepRelated, types.DepParentChild}}

	checkEdgeCounts := func(t *testing.T, withWisp bool) {
		for _, target := range targets {
			for _, filter := range filters {
				got, err := issueops.CountDependencyEdgesInTx(ctx, conn, target, domain.DepDirectionIn, filter)
				if err != nil {
					t.Fatalf("CountDependencyEdgesInTx(%s, %v): %v", target, filter, err)
				}
				if want := oracleInEdges(t, conn, target, filter, withWisp); got != want {
					t.Errorf("CountDependencyEdgesInTx(%s, In, %v) = %d, COALESCE oracle %d", target, filter, got, want)
				}
			}
		}
	}

	t.Run("edge counts", func(t *testing.T) { checkEdgeCounts(t, true) })

	t.Run("status counts", func(t *testing.T) {
		for _, target := range targets {
			for _, status := range []types.Status{types.StatusOpen, types.StatusClosed} {
				got, err := issueops.CountDependentsByStatusInTx(ctx, conn, target, status)
				if err != nil {
					t.Fatalf("CountDependentsByStatusInTx(%s, %s): %v", target, status, err)
				}
				var durable, wisp int64
				if err := conn.QueryRowContext(ctx, oracleStatusDurable, target, string(status)).Scan(&durable); err != nil {
					t.Fatal(err)
				}
				if err := conn.QueryRowContext(ctx, oracleStatusWisp, target, string(status)).Scan(&wisp); err != nil {
					t.Fatal(err)
				}
				if got != durable+wisp {
					t.Errorf("CountDependentsByStatusInTx(%s, %s) = %d, COALESCE oracle %d", target, status, got, durable+wisp)
				}
			}
		}
	})

	t.Run("search counts by ids", func(t *testing.T) {
		for _, page := range []struct {
			tables sqlbuild.FilterTables
			ids    []string
		}{
			{sqlbuild.IssuesFilterTables, []string{"t1", "a", "c", "nope"}},
			{sqlbuild.WispsFilterTables, []string{"w1", "wa", "nope"}},
		} {
			q, args := sqlbuild.SearchCountsSQL(page.tables, page.ids, "", "", "", true, sqlbuild.CountsHydration{})
			rows, err := conn.QueryContext(ctx, "SELECT id, rdep_count FROM ("+q+") page", args...)
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
			if len(got) != len(page.ids)-1 {
				t.Errorf("%s page returned %v, want a row for each of %v but nope", page.tables.Main, got, page.ids)
			}
			for id, n := range got {
				var want int64
				if err := conn.QueryRowContext(ctx, oracleBlockers, id).Scan(&want); err != nil {
					t.Fatal(err)
				}
				if n != want {
					t.Errorf("%s rdep_count(%s) = %d, COALESCE oracle %d", page.tables.Main, id, n, want)
				}
			}
		}
	})

	// Without wisp_dependencies the edge count is the durable plane's, as
	// before; the status count still fails, as it always has.
	t.Run("missing wisp plane", func(t *testing.T) {
		exec("DROP TABLE wisp_dependencies")
		checkEdgeCounts(t, false)
		if _, err := issueops.CountDependentsByStatusInTx(ctx, conn, "t1", types.StatusOpen); err == nil {
			t.Error("CountDependentsByStatusInTx without wisp_dependencies succeeded, want the error it always returned")
		}
		if err := conn.QueryRowContext(ctx, oracleStatusWisp, "t1", "open").Scan(new(int64)); err == nil {
			t.Error("the oracle status query without wisp_dependencies succeeded; the error expectation above is stale")
		}
	})
}
