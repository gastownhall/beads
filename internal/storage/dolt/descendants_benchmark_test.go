package dolt

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// The descendant-walk fixture is sized like a working Gas City database rather
// than a unit fixture: the walk's cost is a function of the whole parent-child
// edge relation, not just of the subtree under the probed parent, so a fixture
// with only the probed subtree in it cannot show the difference between a walk
// that probes an index and one that re-scans the relation per recursion row.
// The field measurement behind this benchmark had 4,641 dependency rows and
// 483 descendants under the probed parent.
const (
	benchProbeDescendants = 500 // durable descendants under the probed parent
	benchProbeWisps       = 5   // wisp children of the probed parent (wisp_dependencies rows)
	benchOtherTrees       = 40  // unrelated molecule/epic trees
	benchOtherTreeSize    = 100 // descendants in each unrelated tree
	benchSeedBatch        = 500 // rows per bulk insert while seeding
)

// seedDescendantForest creates one probed tree and benchOtherTrees unrelated
// ones, every node attached to a random earlier node of its own tree by a
// parent-child edge, plus a few wisp children under the probed root so the
// ready path's wisp leg has work to do, and returns the probed tree's root
// id. Edges are written with a bulk INSERT rather than AddDependency:
// AddDependency commits per edge, which would make seeding the dominant cost
// of the benchmark run.
func seedDescendantForest(tb testing.TB, ctx context.Context, store *DoltStore) string {
	tb.Helper()
	rng := rand.New(rand.NewSource(1)) //nolint:gosec // deterministic fixture, not security-sensitive

	var issues []*types.Issue
	var edges [][2]string // child, parent
	grow := func(prefix string, n int) string {
		root := prefix + "-root"
		issues = append(issues, benchForestIssue(root))
		nodes := []string{root}
		for i := 0; i < n; i++ {
			id := fmt.Sprintf("%s-%04d", prefix, i)
			issues = append(issues, benchForestIssue(id))
			edges = append(edges, [2]string{id, nodes[rng.Intn(len(nodes))]})
			nodes = append(nodes, id)
		}
		return root
	}
	probe := grow("bench-probe", benchProbeDescendants)
	for t := 0; t < benchOtherTrees; t++ {
		grow(fmt.Sprintf("bench-t%02d", t), benchOtherTreeSize)
	}

	for start := 0; start < len(issues); start += benchSeedBatch {
		end := min(start+benchSeedBatch, len(issues))
		if err := store.CreateIssues(ctx, issues[start:end], "bench"); err != nil {
			tb.Fatalf("seed issues [%d:%d]: %v", start, end, err)
		}
	}
	for start := 0; start < len(edges); start += benchSeedBatch {
		end := min(start+benchSeedBatch, len(edges))
		var sb strings.Builder
		args := make([]any, 0, 2*(end-start))
		sb.WriteString("INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by) VALUES ")
		for i, e := range edges[start:end] {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString("(UUID(), ?, ?, 'parent-child', NOW(), 'bench')")
			args = append(args, e[0], e[1])
		}
		if _, err := store.db.ExecContext(ctx, sb.String(), args...); err != nil {
			tb.Fatalf("seed edges [%d:%d]: %v", start, end, err)
		}
	}
	for i := 0; i < benchProbeWisps; i++ {
		wisp := benchForestIssue(fmt.Sprintf("bench-probe-wisp-%02d", i))
		wisp.Ephemeral = true
		if err := store.CreateIssue(ctx, wisp, "bench"); err != nil {
			tb.Fatalf("seed wisp %s: %v", wisp.ID, err)
		}
		if _, err := store.db.ExecContext(ctx,
			"INSERT INTO wisp_dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by) VALUES (UUID(), ?, ?, 'parent-child', NOW(), 'bench')",
			wisp.ID, probe); err != nil {
			tb.Fatalf("seed wisp edge %s: %v", wisp.ID, err)
		}
	}
	// A freshly bulk-loaded table has no statistics yet, and the planner's
	// join order for the walk depends on them; a working database has them.
	if _, err := store.db.ExecContext(ctx, "ANALYZE TABLE issues, wisps, dependencies, wisp_dependencies"); err != nil {
		tb.Fatalf("analyze seeded tables: %v", err)
	}
	return probe
}

func benchForestIssue(id string) *types.Issue {
	return &types.Issue{
		ID:        id,
		Title:     id,
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
}

// BenchmarkDescendantWalk measures the transitive parent-child walk behind
// `bd ready --parent` / `bd blocked --parent` on its own.
func BenchmarkDescendantWalk(b *testing.B) {
	store, cleanup := setupBenchStore(b)
	defer cleanup()

	ctx := context.Background()
	root := seedDescendantForest(b, ctx, store)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ids, err := store.getDescendantIDs(ctx, root)
		if err != nil {
			b.Fatalf("descendant walk: %v", err)
		}
		if want := benchProbeDescendants + benchProbeWisps; len(ids) != want {
			b.Fatalf("descendant walk returned %d ids, want %d", len(ids), want)
		}
	}
}

// BenchmarkReadyWorkParent measures the whole `bd ready --parent` read path,
// which is where the walk's cost reaches users.
func BenchmarkReadyWorkParent(b *testing.B) {
	store, cleanup := setupBenchStore(b)
	defer cleanup()

	ctx := context.Background()
	root := seedDescendantForest(b, ctx, store)
	filter := types.WorkFilter{ParentID: &root, Limit: 100}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := store.GetReadyWork(ctx, filter); err != nil {
			b.Fatalf("ready work --parent: %v", err)
		}
	}
}
