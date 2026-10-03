package dolt

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// Blocked-state cost at store scale (gastownhall/beads#6506, #6288).
//
// The timings below are the ones the parent-child cascade can regress, and the
// ones a change to shouldBeBlockedIDsUnionScopedSQL must be measured against
// BEFORE it ships:
//
//   - RecomputeAllIsBlockedInTx — the full repair. It runs on every dolt
//     pull/merge (versioncontrolops/blocked_recompute.go), on federation sync,
//     and behind `bd doctor --fix`, so its cost is paid by every clone on
//     every sync, not only by an operator running doctor.
//   - CountIsBlockedInconsistenciesInTx — the read-only detection behind the
//     doctor "Blocked State" check, over an ALREADY CONSISTENT store, which is
//     the case an operator actually hits.
//   - RecomputeIsBlockedForIDsInTx at 2, 20 and 200 ids — the batched
//     mark/unmark the incremental write path runs, isolated from the dolt
//     commit around it. 200 is queryBatchSize (the bulk/import shape); 2 is the
//     small-batch floor where per-statement cost cannot be amortized.
//   - CloseIssue x100 — the incremental write path end to end, which runs the
//     same union scoped to a batch.
//
// The fixture matters as much as the numbers: the parent-child legs
// short-circuit on `p.is_blocked = 1`, so a plane with no blocked rows
// measures nothing at all. This one carries a realistic mix — exogenously
// blocked epics that really do cascade, close-gate epics that (since #6506)
// must not, one depth-2 gate over a grandchild, and a large population of
// ordinary leaf-to-leaf blocks edges.
//
// Skipped under -short. It is a measurement, not an assertion: it has no
// pass/fail threshold, because a threshold in wall-clock time on shared CI
// hardware is a flake. Run it on both sides of a change and compare:
//
//	BEADS_TEST_EMBEDDED_DOLT=1 go test ./internal/storage/dolt/ \
//	  -run TestBlockedStateScaleTiming -count=1 -v
const (
	scaleEpics  = 100 // bs-0000..bs-0099; 0..9 are roots, 10..99 hang under them
	scaleIssues = 1000
	// Leaves bs-0100..bs-0699 carry blocks edges; bs-0700..bs-0999 are their
	// targets, of which all but the last scaleOpenTargets are closed.
	scaleFirstTarget   = 700
	scaleOpenTargets   = 60
	scaleEdgesPerLeaf  = 3
	scaleClosesTimed   = 100
	scaleBenchTimeout  = 20 * time.Minute
	scaleBenchIDFormat = "bs-%04d"
)

func scaleID(n int) string { return fmt.Sprintf(scaleBenchIDFormat, n) }

func TestBlockedStateScaleTiming(t *testing.T) {
	if testing.Short() {
		t.Skip("scale timing: skipped under -short")
	}
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), scaleBenchTimeout)
	defer cancel()

	built := time.Now()
	deps := buildScaleFixture(ctx, t, store)
	t.Logf("fixture: %d issues, %d dependency edges, built in %s",
		scaleIssues, deps, time.Since(built).Round(time.Millisecond))

	// Converge once, untimed: every timing below is over a CONSISTENT store,
	// which is the state a real clone is in when doctor or a pull runs.
	recomputeAllScale(ctx, t, store)
	t.Logf("blocked rows: %d of %d issues", scaleBlockedCount(ctx, t, store), scaleIssues)

	start := time.Now()
	n := scaleCountInconsistencies(ctx, t, store)
	t.Logf("TIMING doctor-count      %s (reported %d inconsistencies)",
		time.Since(start).Round(time.Millisecond), n)

	start = time.Now()
	changed := recomputeAllScale(ctx, t, store)
	t.Logf("TIMING full-repair       %s (changed %d rows)",
		time.Since(start).Round(time.Millisecond), changed)

	// The write path's SQL on its own: the batched mark/unmark the incremental
	// recompute runs, over a spread of ids, with no commit. CloseIssue below is
	// the honest end-to-end number, but most of it is a dolt commit, which on a
	// loaded box swings further than the thing being measured.
	//
	// THREE batch sizes, because the batched path's cost per statement and its
	// cost per id pull in opposite directions and one size hides the other. 200
	// is queryBatchSize — one statement pair, the bulk/import shape an
	// AffectedBy* fan-out reaches. 20 is an ordinary epic-sized fan-out. 2 is
	// the floor: the per-STATEMENT overhead (a scoped exogeneity read, the
	// union built once) with almost no ids to amortize it over, which is where
	// a fixed per-statement cost shows up worst.
	for _, bs := range []struct {
		size, reps int
	}{{2, 50}, {20, 20}, {200, 5}} {
		batchIDs := make([]string, 0, bs.size)
		for i := 0; i < bs.size; i++ {
			batchIDs = append(batchIDs, scaleID((i*5)%scaleIssues))
		}
		txb, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin batched-recompute tx: %v", err)
		}
		start = time.Now()
		for k := 0; k < bs.reps; k++ {
			if err := issueops.RecomputeIsBlockedForIDsInTx(ctx, txb, batchIDs); err != nil {
				_ = txb.Rollback()
				t.Fatalf("RecomputeIsBlockedForIDsInTx(%d ids): %v", bs.size, err)
			}
		}
		elapsedBatch := time.Since(start)
		_ = txb.Rollback()
		t.Logf("TIMING batched-recompute-%03d %s for %d x %d ids (%s each)",
			bs.size, elapsedBatch.Round(time.Millisecond), bs.reps, len(batchIDs),
			(elapsedBatch / time.Duration(bs.reps)).Round(time.Microsecond))
	}

	// The write path end to end: close scaleClosesTimed open leaves one at a
	// time, each through the store's own CloseIssue, which recomputes the
	// affected batch and commits.
	targets := make([]string, 0, scaleClosesTimed)
	for i := scaleFirstTarget + scaleOpenTargets; len(targets) < scaleClosesTimed && i < scaleIssues; i++ {
		targets = append(targets, scaleID(i))
	}
	for i := 100; len(targets) < scaleClosesTimed && i < scaleFirstTarget; i++ {
		targets = append(targets, scaleID(i))
	}
	start = time.Now()
	for _, id := range targets {
		if err := store.CloseIssue(ctx, id, "scale timing", "tester", ""); err != nil {
			t.Fatalf("close %s: %v", id, err)
		}
	}
	elapsed := time.Since(start)
	t.Logf("TIMING write-path        %s for %d CloseIssue (%s each)",
		elapsed.Round(time.Millisecond), len(targets),
		(elapsed / time.Duration(len(targets))).Round(time.Microsecond))
}

// buildScaleFixture writes the graph and returns the number of dependency
// edges it added.
//
// Every blocks edge goes in BEFORE any parent-child edge, and the parent-child
// edges go in with SkipCycleCheck. That is not a convenience: the close-gate
// shape is refused once both edges exist, in either order
// (CheckBlockingHierarchyInTx rejects a blocker that is a descendant,
// CheckDependencyCycleInTx rejects the parent-child edge that closes the
// loop), so one-edge-at-a-time is how the shape arrives in a real store too.
func buildScaleFixture(ctx context.Context, t *testing.T, store *DoltStore) int {
	t.Helper()

	issues := make([]*types.Issue, 0, scaleIssues)
	for i := 0; i < scaleIssues; i++ {
		issues = append(issues, &types.Issue{
			ID:        scaleID(i),
			Title:     "scale " + scaleID(i),
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		})
	}
	if err := store.RunInTransaction(ctx, "scale fixture: issues", func(tx storage.Transaction) error {
		return tx.CreateIssues(ctx, issues, "tester")
	}); err != nil {
		t.Fatalf("create scale issues: %v", err)
	}

	type edge struct {
		src, tgt string
		ty       types.DependencyType
	}
	var edges []edge

	openTarget := func(k int) string {
		return scaleID(scaleFirstTarget + scaleOpenTargets - 1 - (k % scaleOpenTargets))
	}

	// Roots 0..4: a close gate over a GRANDCHILD — leaf 110+r hangs under epic
	// 10+r, which hangs under root r. This is the depth-2 lock: it must not
	// darken anything under the root.
	for r := 0; r < 5; r++ {
		edges = append(edges, edge{scaleID(r), scaleID(110 + r), types.DepBlocks})
	}
	// Roots 5..9: exogenously blocked, so their whole subtree inherits.
	for r := 5; r < 10; r++ {
		edges = append(edges, edge{scaleID(r), openTarget(r), types.DepBlocks})
	}
	// Epics 10..49: exogenously blocked. Epics 50..79: a close gate on one of
	// their own direct children. Epics 80..99: clear.
	for e := 10; e < 50; e++ {
		edges = append(edges, edge{scaleID(e), openTarget(e), types.DepBlocks})
	}
	for e := 50; e < 80; e++ {
		edges = append(edges, edge{scaleID(e), scaleID(100 + e), types.DepBlocks})
	}
	// Ordinary leaf-to-leaf blocks edges: sources 100..699, targets 700..999,
	// most of which are closed below, so only some sources end up blocked.
	strides := []int{13, 29, 47}
	for i := 100; i < scaleFirstTarget; i++ {
		for k := 0; k < scaleEdgesPerLeaf; k++ {
			tgt := scaleFirstTarget + (i*strides[k]+7*k+3)%(scaleIssues-scaleFirstTarget)
			edges = append(edges, edge{scaleID(i), scaleID(tgt), types.DepBlocks})
		}
	}
	// The hierarchy, last: leaves under epics, epics under roots.
	for i := scaleEpics; i < scaleIssues; i++ {
		edges = append(edges, edge{scaleID(i), scaleID(i % scaleEpics), types.DepParentChild})
	}
	for e := 10; e < scaleEpics; e++ {
		edges = append(edges, edge{scaleID(e), scaleID(e % 10), types.DepParentChild})
	}

	// One transaction per chunk: a single transaction over every edge holds the
	// whole fixture in the uncommitted overlay, which is the pathological read
	// shape #6288 documents and would measure the fixture rather than the code.
	const chunk = 250
	for start := 0; start < len(edges); start += chunk {
		end := start + chunk
		if end > len(edges) {
			end = len(edges)
		}
		batch := edges[start:end]
		if err := store.RunInTransaction(ctx, "scale fixture: edges", func(tx storage.Transaction) error {
			for _, e := range batch {
				if err := tx.AddDependencyWithOptions(ctx,
					&types.Dependency{IssueID: e.src, DependsOnID: e.tgt, Type: e.ty},
					"tester", storage.DependencyAddOptions{SkipCycleCheck: true}); err != nil {
					return fmt.Errorf("add %s -> %s (%s): %w", e.src, e.tgt, e.ty, err)
				}
			}
			return nil
		}); err != nil {
			t.Fatalf("scale fixture edges: %v", err)
		}
	}

	// Close most of the target pool so the blocked fraction is a fraction.
	for i := scaleFirstTarget; i < scaleIssues-scaleOpenTargets; i++ {
		if err := store.CloseIssue(ctx, scaleID(i), "scale fixture", "tester", ""); err != nil {
			t.Fatalf("close scale target %s: %v", scaleID(i), err)
		}
	}

	return len(edges)
}

func recomputeAllScale(ctx context.Context, t *testing.T, store *DoltStore) int64 {
	t.Helper()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin scale recompute tx: %v", err)
	}
	changed, err := issueops.RecomputeAllIsBlockedInTx(ctx, tx)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("scale RecomputeAllIsBlockedInTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit scale recompute tx: %v", err)
	}
	return changed
}

func scaleCountInconsistencies(ctx context.Context, t *testing.T, store *DoltStore) int64 {
	t.Helper()
	n, err := issueops.CountIsBlockedInconsistenciesInTx(ctx, store.db)
	if err != nil {
		t.Fatalf("scale CountIsBlockedInconsistenciesInTx: %v", err)
	}
	return n
}

func scaleBlockedCount(ctx context.Context, t *testing.T, store *DoltStore) int {
	t.Helper()
	var n int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM issues WHERE is_blocked = 1").Scan(&n); err != nil {
		t.Fatalf("count blocked rows: %v", err)
	}
	return n
}
