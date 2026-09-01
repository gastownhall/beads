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
// CanonicalCyclePaths holds the determinism ruling, CanonicalMixedCyclePaths
// the IncludeTracks widening, and BuildCycles the honest-partial one; all
// three are pinned in cycle_report_test.go.

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

// CanonicalMixedCyclePaths finds one qualifying cycle per strongly connected
// component in a graph that also carries `tracks` edges
// (MixedCycleEdge.Scheduling false). A component qualifies only when it
// contains a scheduling edge: every internal edge of a strongly connected
// component lies on a cycle, so choosing one such edge and a stable return
// path cannot miss a mixed cycle merely because a tracks-only back edge was
// visited first.
//
// Requiring a scheduling edge avoids the "thousands of cycles" regression
// AppendMixedCycleGraphInTx documents: ordinary convoy/tracking topology loops
// constantly through tracks alone, and none of those loops is a deadlock.
// WITH the requirement, the shape this exists for — a molecule root that
// blocks-depends (transitively) on its own entry step, which tracks-depends
// back to the root — is reported.
//
// Nodes, adjacency lists, components, the chosen scheduling edge, and its
// return path are all ordered. The answer therefore depends only on the graph,
// not Go map or SQL row order.
func CanonicalMixedCyclePaths(graph map[string][]MixedCycleEdge) [][]string {
	adjacency := make(map[string][]MixedCycleEdge, len(graph))
	nodeSet := make(map[string]struct{}, len(graph))
	for node, edges := range graph {
		nodeSet[node] = struct{}{}
		scheduling := make(map[string]bool, len(edges))
		for _, edge := range edges {
			nodeSet[edge.To] = struct{}{}
			scheduling[edge.To] = scheduling[edge.To] || edge.Scheduling
		}
		targets := make([]string, 0, len(scheduling))
		for to := range scheduling {
			targets = append(targets, to)
		}
		slices.Sort(targets)
		deduped := make([]MixedCycleEdge, 0, len(targets))
		for _, to := range targets {
			deduped = append(deduped, MixedCycleEdge{To: to, Scheduling: scheduling[to]})
		}
		adjacency[node] = deduped
	}
	nodes := make([]string, 0, len(nodeSet))
	plain := make(map[string][]string, len(nodeSet))
	for node := range nodeSet {
		nodes = append(nodes, node)
		for _, edge := range adjacency[node] {
			plain[node] = append(plain[node], edge.To)
		}
	}
	slices.Sort(nodes)

	var cycles [][]string
	for _, component := range mixedStronglyConnectedComponents(adjacency, nodes) {
		members := make(map[string]bool, len(component))
		for _, node := range component {
			members[node] = true
		}

		var from, to string
		found := false
		for _, node := range component {
			for _, edge := range adjacency[node] {
				if edge.Scheduling && members[edge.To] {
					from, to = node, edge.To
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		if !found {
			continue
		}

		returnPath := reachPath(plain, to, from)
		if returnPath == nil {
			continue // Impossible for two members of one strongly connected component.
		}
		cycle := append([]string{from}, returnPath[:len(returnPath)-1]...)
		cycles = append(cycles, rotateToLowest(cycle))
	}
	slices.SortFunc(cycles, slices.Compare)
	return cycles
}

// mixedStronglyConnectedComponents returns Tarjan components with both their
// members and the component list sorted. Targets without outgoing edges are in
// nodes too, so singleton sinks remain part of the traversal even though they
// can never qualify on their own.
func mixedStronglyConnectedComponents(adjacency map[string][]MixedCycleEdge, nodes []string) [][]string {
	nextIndex := 0
	index := make(map[string]int, len(nodes))
	lowlink := make(map[string]int, len(nodes))
	onStack := make(map[string]bool, len(nodes))
	stack := make([]string, 0, len(nodes))
	components := make([][]string, 0)

	var visit func(string)
	visit = func(node string) {
		nextIndex++
		index[node] = nextIndex
		lowlink[node] = nextIndex
		stack = append(stack, node)
		onStack[node] = true

		for _, edge := range adjacency[node] {
			neighbor := edge.To
			if index[neighbor] == 0 {
				visit(neighbor)
				lowlink[node] = min(lowlink[node], lowlink[neighbor])
			} else if onStack[neighbor] {
				lowlink[node] = min(lowlink[node], index[neighbor])
			}
		}

		if lowlink[node] != index[node] {
			return
		}
		component := make([]string, 0)
		for {
			last := len(stack) - 1
			member := stack[last]
			stack = stack[:last]
			onStack[member] = false
			component = append(component, member)
			if member == node {
				break
			}
		}
		slices.Sort(component)
		components = append(components, component)
	}

	for _, node := range nodes {
		if index[node] == 0 {
			visit(node)
		}
	}
	slices.SortFunc(components, slices.Compare)
	return components
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
// and, by default, the same two edge types DetectCyclesInTx reads, because it
// is the same question. req.IncludeTracks answers a wider question instead —
// see DetectCyclesRequest and CanonicalMixedCyclePaths.
func DetectCycleReportInTx(ctx context.Context, tx DBTX, req publicops.DetectCyclesRequest) (publicops.CycleReport, error) {
	hydrate := func(id string) *types.Issue {
		// The error is deliberately not distinguished from a miss: both mean the
		// same thing to the answer, that this database did not describe the node.
		issue, _ := GetIssueInTx(ctx, tx, id)
		return issue
	}

	if req.IncludeTracks {
		graph := make(map[string][]MixedCycleEdge)
		if err := AppendMixedCycleGraphInTx(ctx, tx, cycleDetectionTables(), graph); err != nil {
			return publicops.CycleReport{}, err
		}
		return publicops.CycleReport{
			Cycles: BuildCycles(CanonicalMixedCyclePaths(graph), hydrate),
		}, nil
	}

	graph := make(map[string][]string)
	if err := AppendBlockingGraphInTx(ctx, tx, cycleDetectionTables(), graph); err != nil {
		return publicops.CycleReport{}, err
	}
	return publicops.CycleReport{
		Cycles: BuildCycles(CanonicalCyclePaths(graph), hydrate),
	}, nil
}
