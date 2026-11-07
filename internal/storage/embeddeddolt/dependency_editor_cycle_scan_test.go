//go:build cgo

package embeddeddolt_test

import (
	"errors"
	"regexp"
	"strings"
	"sync"
	"testing"

	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/sqlcount"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// dependencyGraphScan matches the statement the dependency graph reader
// (issueops.AppendSchedulingGraphInTx and AppendBlockingGraphInTx) sends for
// one table: every edge's source, target and type.
var dependencyGraphScan = regexp.MustCompile(`(?s)AS depends_on_id, type\s+FROM (dependencies|wisp_dependencies)\b`)

// TestEmbeddedAddDependenciesSkipsTheGraphScanWhenEveryEdgeWasProbed pins
// the statements issueops.ExecuteAddDependencies sends. With per-edge cycle
// probes on, each scheduling edge's probe runs in the request's own write
// transaction after the request's earlier edges are inserted, so a cycle
// closed by several new edges is refused at the last of them, and the
// whole-graph scan at the end would find nothing the probes did not. That
// scan read every row of both dependency tables, so it must not be sent.
//
// The test calls ExecuteAddDependencies directly on a counted connection,
// not the CLI: `bd dep add` scans the graph once more after it commits, for
// its cycle warning, and that read is outside the storage call.
func TestEmbeddedAddDependenciesSkipsTheGraphScanWhenEveryEdgeWasProbed(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()

	fixture := newPristineEmbeddedDoltFixture(t, "depscan")
	t.Cleanup(func() { closeEmbeddedDoltStore(t, fixture.store) })
	const a, b, c, d, e = "depscan-a", "depscan-b", "depscan-c", "depscan-d", "depscan-e"
	for _, id := range []string{a, b, d, e} {
		if err := fixture.store.CreateIssue(ctx, &types.Issue{
			ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		}, "seed"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", id, err)
		}
	}
	// c is a wisp, so the cycle case below walks both dependency tables.
	if err := fixture.store.CreateIssue(ctx, &types.Issue{
		ID: c, Title: c, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true,
	}, "seed"); err != nil {
		t.Fatalf("CreateIssue(%s): %v", c, err)
	}

	var mu sync.Mutex
	var sent []string
	counts := &sqlcount.Counts{}
	db, cleanup, err := openCountedConn(ctx, fixture.dataDir, fixture.database, counts,
		sqlcount.WithStatementObserver(func(query string) {
			mu.Lock()
			defer mu.Unlock()
			sent = append(sent, query)
		}))
	if err != nil {
		t.Fatalf("openCountedConn: %v", err)
	}
	t.Cleanup(cleanup)

	// execute runs request through ExecuteAddDependencies in a transaction it
	// rolls back, and returns the statements sent inside the call, how many,
	// and the call's error.
	execute := func(t *testing.T, request issueops.AddDependenciesRequest) ([]string, int64, error) {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("BeginTx: %v", err)
		}
		defer func() { _ = tx.Rollback() }()
		mu.Lock()
		sent = nil
		mu.Unlock()
		before := counts.Snapshot()
		_, _, execErr := storageissueops.ExecuteAddDependencies(ctx, tx, request)
		total := counts.Snapshot().Sub(before).Total()
		mu.Lock()
		statements := append([]string(nil), sent...)
		mu.Unlock()
		return statements, total, execErr
	}
	graphScans := func(statements []string) []string {
		var scans []string
		for _, s := range statements {
			if dependencyGraphScan.MatchString(s) {
				scans = append(scans, strings.Join(strings.Fields(s), " "))
			}
		}
		return scans
	}

	acyclic := []issueops.DependencyEdge{{IssueID: d, DependsOnID: e, Type: types.DepBlocks}}

	t.Run("probed request sends no graph scan", func(t *testing.T) {
		statements, total, err := execute(t, issueops.AddDependenciesRequest{Actor: "tester", Edges: acyclic})
		if err != nil {
			t.Fatalf("ExecuteAddDependencies: %v", err)
		}
		t.Logf("one blocks edge with the per-edge probe: %d statements", total)
		if scans := graphScans(statements); len(scans) != 0 {
			t.Errorf("ExecuteAddDependencies read the whole dependency graph after probing every edge:\n%s",
				strings.Join(scans, "\n"))
		}
	})

	// The control: the matcher above must see the scan when it is sent, or
	// the subtest before it proves nothing. With the probes skipped the scan
	// is the only cycle check, and it reads only the scheduling types.
	t.Run("unprobed request still scans, filtered to the scheduling types", func(t *testing.T) {
		statements, total, err := execute(t, issueops.AddDependenciesRequest{
			Actor: "tester", Edges: acyclic, SkipPerEdgeCycleCheck: true,
		})
		if err != nil {
			t.Fatalf("ExecuteAddDependencies: %v", err)
		}
		t.Logf("one blocks edge with the per-edge probe skipped: %d statements", total)
		scans := graphScans(statements)
		if len(scans) != 2 {
			t.Fatalf("graph scans = %d, want one per dependency table:\n%s", len(scans), strings.Join(scans, "\n"))
		}
		for _, scan := range scans {
			if !strings.Contains(scan, "WHERE type IN ('blocks', 'conditional-blocks', 'parent-child')") {
				t.Errorf("graph scan reads every edge type, want only the scheduling types: %s", scan)
			}
		}
	})

	// The cycle c -> a -> b -> c closes only through all three new edges, in
	// both dependency tables (c is a wisp, so its edge lands in
	// wisp_dependencies), and across the two insert phases: the parent-child
	// edge is applied first although it comes last in the request. The probe
	// for the last edge sees the other two because they were inserted earlier
	// in the same transaction.
	t.Run("cycle closed only by the request's own edges is still refused", func(t *testing.T) {
		statements, _, err := execute(t, issueops.AddDependenciesRequest{
			Actor: "tester",
			Edges: []issueops.DependencyEdge{
				{IssueID: a, DependsOnID: b, Type: types.DepBlocks},
				{IssueID: b, DependsOnID: c, Type: types.DepConditionalBlocks},
				{IssueID: c, DependsOnID: a, Type: types.DepParentChild},
			},
		})
		if !errors.Is(err, issueops.ErrDependencyCycle) {
			t.Fatalf("ExecuteAddDependencies error = %v, want ErrDependencyCycle from the per-edge probe", err)
		}
		if scans := graphScans(statements); len(scans) != 0 {
			t.Errorf("the refusal came after a graph scan, want it from the per-edge probe:\n%s", strings.Join(scans, "\n"))
		}
	})
}
