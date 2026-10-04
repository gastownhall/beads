// Package batchbench measures single and batched creates against a database
// that already holds a large dependency graph (batchfixtures.LargeGraphInserts),
// the case a fresh-database benchmark cannot show: per-create work that
// scales with the size of the stored graph rather than with the request.
// Each backend's test package runs Run against its own engine, opt-in via
// EnvVar, and the numbers are logged rather than asserted (wall time and
// allocation are host-sensitive).
package batchbench

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/batchfixtures"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/sqlcount"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// EnvVar opts in: set it to the number of graph issues to pre-populate
// (about two edges each), e.g. 50000 for ~100k edges.
const EnvVar = "BEADS_BENCH_LARGE_GRAPH"

// CreatesEnvVar optionally sets how many creates each measured shape
// performs (default DefaultCreates): the per-edge recursive checks of older
// code take seconds per create on a large graph.
const CreatesEnvVar = "BEADS_BENCH_LARGE_GRAPH_CREATES"

// DefaultCreates is the number of creates per shape when CreatesEnvVar is
// unset.
const DefaultCreates = 20

// ApplyCreatesEnvVar optionally overrides the apply-batch shape's create
// count on its own: its dep_add items take AddDependencyInTx's per-edge
// recursive checks, which run for minutes per edge on a 100k-edge graph.
const ApplyCreatesEnvVar = "BEADS_BENCH_LARGE_GRAPH_APPLY_CREATES"

// Issues returns the graph size EnvVar asks for, skipping t when unset.
func Issues(t *testing.T) int {
	t.Helper()
	n, err := strconv.Atoi(os.Getenv(EnvVar))
	if err != nil || n <= 0 {
		t.Skipf("set %s=<issues> (e.g. 50000) to run the large-graph create benchmark", EnvVar)
	}
	return n
}

// Run populates db (a fresh migrated database) with issues graph issues
// under prefix, commits them as a Dolt version, then measures three shapes,
// each performing CreatesEnvVar (default DefaultCreates) creates, logging
// wall time, statements and Go heap allocation:
//
//   - create+parent+blocker: ExecuteCreate (the guarded single create every
//     surface's `bd create --parent P --deps B` reaches) under an existing
//     parent with an existing blocker, one transaction per create;
//   - create+2deps: CreateIssuesInTxWithResult with one issue carrying two
//     blocks edges to existing issues (the store's CreateIssue path);
//   - apply-batch: one ApplyBatchInTx of that many creates, each followed by a
//     parent-child and a blocks dep_add to existing issues.
func Run(t *testing.T, db *sql.DB, counts *sqlcount.Counts, prefix string, issues int) {
	t.Helper()
	creates := DefaultCreates
	if n, err := strconv.Atoi(os.Getenv(CreatesEnvVar)); err == nil && n > 0 {
		creates = n
	}
	ctx := context.Background()
	start := time.Now()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range batchfixtures.LargeGraphInserts(prefix, issues) {
		if _, err := tx.ExecContext(ctx, st.SQL, st.Args...); err != nil {
			_ = tx.Rollback()
			t.Fatalf("populate: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'large graph fixture')"); err != nil {
		t.Fatalf("commit fixture: %v", err)
	}
	var edges int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dependencies").Scan(&edges); err != nil {
		t.Fatal(err)
	}
	t.Logf("large graph: %d issues, %d edges, populated in %s", issues, edges, time.Since(start).Round(time.Millisecond))

	pick := func(i, salt int) string { return batchfixtures.LargeGraphID(prefix, (i*7919+salt*104729)%issues) }
	measure := func(name string, creates int, body func()) {
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		counts.Reset()
		start := time.Now()
		body()
		elapsed := time.Since(start)
		runtime.ReadMemStats(&after)
		s := counts.Snapshot()
		t.Logf("BENCH %-22s %d creates: %8s total, %6.1f ms/create, %6d statements, %8.1f MiB Go alloc",
			name, creates, elapsed.Round(time.Millisecond), float64(elapsed.Milliseconds())/float64(creates), s.Total(),
			float64(after.TotalAlloc-before.TotalAlloc)/(1<<20))
	}
	inTx := func(body func(tx *sql.Tx) error) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := body(tx); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}

	measure("create+parent+blocker", creates, func() {
		for i := 0; i < creates; i++ {
			inTx(func(tx *sql.Tx) error {
				_, _, err := issueops.ExecuteCreate(ctx, tx, publicops.CreateRequest{
					Actor:        "bench",
					Issue:        &types.Issue{Title: fmt.Sprintf("bench child %d", i), Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
					ParentID:     pick(i, 1),
					Dependencies: []publicops.CreateDependency{{TargetID: pick(i, 2), Type: types.DepBlocks}},
				})
				return err
			})
		}
	})
	measure("create+2deps", creates, func() {
		for i := 0; i < creates; i++ {
			inTx(func(tx *sql.Tx) error {
				id := fmt.Sprintf("%s-b2d%03d", prefix, i)
				_, err := issueops.CreateIssuesInTxWithResult(ctx, tx, []*types.Issue{{
					ID: id, Title: fmt.Sprintf("bench two deps %d", i), Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
					Dependencies: []*types.Dependency{
						{IssueID: id, DependsOnID: pick(i, 3), Type: types.DepBlocks},
						{IssueID: id, DependsOnID: pick(i, 4), Type: types.DepBlocks},
					},
				}}, "bench", storage.BatchCreateOptions{})
				return err
			})
		}
	})
	applyCreates := creates
	if n, err := strconv.Atoi(os.Getenv(ApplyCreatesEnvVar)); err == nil && n > 0 {
		applyCreates = n
	}
	measure("apply-batch", applyCreates, func() {
		var items []publicops.ApplyItem
		for i := 0; i < applyCreates; i++ {
			key := fmt.Sprintf("k%d", i)
			items = append(items,
				publicops.ApplyItem{Kind: publicops.ItemCreate, Create: &publicops.CreateItem{Key: key, Issue: &types.Issue{
					Title: fmt.Sprintf("bench apply %d", i), Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}}},
				publicops.ApplyItem{Kind: publicops.ItemDepAdd, DepAdd: &publicops.DepAddItem{
					Source: publicops.Ref{Key: key}, Target: publicops.Ref{ID: pick(i, 5)}, Type: publicops.DepParentChild}},
				publicops.ApplyItem{Kind: publicops.ItemDepAdd, DepAdd: &publicops.DepAddItem{
					Source: publicops.Ref{Key: key}, Target: publicops.Ref{ID: pick(i, 6)}, Type: publicops.DepBlocks}},
			)
		}
		plan, err := storage.PlanApplyBatch(publicops.ApplyBatchRequest{Actor: "bench", Items: items})
		if err != nil {
			t.Fatal(err)
		}
		inTx(func(tx *sql.Tx) error {
			_, _, err := issueops.ApplyBatchInTx(ctx, tx, plan)
			return err
		})
	})
}
