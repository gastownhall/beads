package issueops

import (
	"context"
	"slices"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// The cycle REPORT: the shared body behind issueops.CycleDetector on all three
// backends, split into a pure half and a transactional half so the part that
// decides what the answer MEANS is testable without a database.
// CanonicalCyclePaths holds the determinism ruling and BuildCycles the
// honest-partial one; both are pinned in cycle_report_test.go.

// CanonicalCyclePaths returns the cycles of a blocking graph as id paths, in a
// canonical form: each path rotated so its lowest id comes first, and the paths
// sorted against each other.
//
// EVERY SOURCE OF NONDETERMINISM IS REMOVED HERE, not just the order of the
// answer. A depth-first cycle enumeration records one cycle per BACK EDGE, and
// which edges are back edges depends on the walk: the roots came off a Go map
// and the adjacency lists came out of an unordered SQL read, so two runs against
// an unchanged database could disagree about which cycles exist at all.
//
// Duplicate neighbors are collapsed: a parallel edge adds nothing to
// reachability, and left in place it would make the same back edge report the
// same cycle twice.
//
// The result is NOT every simple cycle in the graph — see
// issueops.CycleReport.Cycles — but it is empty exactly when the graph is
// acyclic, and it is a function of the graph alone.
func CanonicalCyclePaths(graph map[string][]string) [][]string {
	adjacency := make(map[string][]string, len(graph))
	roots := make([]string, 0, len(graph))
	for node, neighbors := range graph {
		roots = append(roots, node)
		sorted := slices.Clone(neighbors)
		slices.Sort(sorted)
		adjacency[node] = slices.Compact(sorted)
	}
	slices.Sort(roots)

	var cycles [][]string
	visited := make(map[string]bool, len(roots))
	onPath := make(map[string]bool, len(roots))
	path := make([]string, 0, len(roots))

	var walk func(node string)
	walk = func(node string) {
		visited[node] = true
		onPath[node] = true
		path = append(path, node)

		for _, neighbor := range adjacency[node] {
			switch {
			case !visited[neighbor]:
				walk(neighbor)
			case onPath[neighbor]:
				// A back edge: the cycle is the suffix of the current path that
				// starts at the neighbor, closed by this edge.
				if start := slices.Index(path, neighbor); start >= 0 {
					cycles = append(cycles, rotateToLowest(path[start:]))
				}
			}
		}

		path = path[:len(path)-1]
		onPath[node] = false
	}

	for _, root := range roots {
		if !visited[root] {
			walk(root)
		}
	}

	slices.SortFunc(cycles, slices.Compare)
	return cycles
}

// rotateToLowest returns a copy of a cycle path rotated so its lowest id comes
// first, preserving edge order. The members of a cycle are distinct — the path
// it is taken from is a simple path — so the lowest id is unique and names
// exactly one rotation.
func rotateToLowest(path []string) []string {
	lowest := 0
	for i, id := range path {
		if id < path[lowest] {
			lowest = i
		}
	}
	out := make([]string, 0, len(path))
	out = append(out, path[lowest:]...)
	out = append(out, path[:lowest]...)
	return out
}

// BuildCycles turns canonical id paths into the role's cycles, calling hydrate
// ONCE per distinct id however many cycles that id sits on.
//
// A LOOKUP THAT FINDS NOTHING DOES NOT FAIL THE REPORT and does not shorten the
// path: hydrate answers nil, the member keeps its id, and the cycle is marked
// partial. The unreadable rows are the ordinary ones — an edge into another
// repository's namespace, an "external:" reference, a row whose edges outlived
// it. Dropping the member instead, which is what this used to do, rendered a
// three-node cycle as a two-node one and dropped a wholly unreadable cycle out
// of the report entirely.
//
// hydrate is a plain lookup rather than a transaction so that the rule above is
// testable without a database; DetectCycleReportInTx supplies the real one.
func BuildCycles(paths [][]string, hydrate func(id string) *types.Issue) []publicops.Cycle {
	seen := make(map[string]*types.Issue, len(paths))
	cycles := make([]publicops.Cycle, 0, len(paths))
	for _, path := range paths {
		cycle := publicops.Cycle{Members: make([]publicops.CycleMember, 0, len(path))}
		for _, id := range path {
			issue, cached := seen[id]
			if !cached {
				issue = hydrate(id)
				seen[id] = issue
			}
			if issue == nil {
				cycle.Partial = true
			}
			cycle.Members = append(cycle.Members, publicops.CycleMember{ID: id, Issue: issue})
		}
		cycles = append(cycles, cycle)
	}
	return cycles
}

// DetectCycleReportInTx is the whole read: build the blocking graph across both
// planes, canonicalize it, and hydrate what it can. It reads the same two tables
// and the same two edge types DetectCyclesInTx reads, because it is the same
// question.
func DetectCycleReportInTx(ctx context.Context, tx DBTX) (publicops.CycleReport, error) {
	graph := make(map[string][]string)
	if err := AppendBlockingGraphInTx(ctx, tx, cycleDetectionTables(), graph); err != nil {
		return publicops.CycleReport{}, err
	}
	hydrate := func(id string) *types.Issue {
		// The error is deliberately not distinguished from a miss: both mean the
		// same thing to the answer, that this database did not describe the node.
		issue, _ := GetIssueInTx(ctx, tx, id)
		return issue
	}
	return publicops.CycleReport{
		Cycles: BuildCycles(CanonicalCyclePaths(graph), hydrate),
	}, nil
}
