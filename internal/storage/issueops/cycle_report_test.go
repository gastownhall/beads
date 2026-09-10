package issueops

import (
	"maps"
	"math/rand"
	"reflect"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// The database-free half of the cycle report. Both of the owner's rulings live
// in pure functions precisely so they can be pinned here, in milliseconds,
// instead of only through three backends that each need a server.
//
// The conformance contract in backend/conformance runs the same clauses against
// real storage, but cycles are refused at write time on every backend, so a
// contract case can seed one small cycle and not the branchy graphs below.

func TestCanonicalCyclePathsRotatesEachCycleToItsLowestID(t *testing.T) {
	// One 3-cycle, spelled starting from each of its three nodes. The walk's
	// entry point is whichever root sorts first, so without rotation the answer
	// would depend on the ids rather than on the graph.
	graph := map[string][]string{
		"b": {"c"},
		"c": {"a"},
		"a": {"b"},
	}
	got := CanonicalCyclePaths(graph)
	want := [][]string{{"a", "b", "c"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cycles = %v, want %v: each cycle is rotated to its lowest id, keeping edge order", got, want)
	}
}

func TestCanonicalCyclePathsSortsTheCycles(t *testing.T) {
	graph := map[string][]string{
		"m": {"n"}, "n": {"m"},
		"a": {"b"}, "b": {"a"},
		"x": {"y"}, "y": {"x"},
	}
	got := CanonicalCyclePaths(graph)
	want := [][]string{{"a", "b"}, {"m", "n"}, {"x", "y"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cycles = %v, want %v", got, want)
	}
}

// TestCanonicalCyclePathsIsIndependentOfMapOrder is the case Q4 exists for.
//
// The previous implementation walked the graph map directly and appended
// neighbours in SQL row order, so the answer moved between runs — including
// under --json, where a caller diffing two snapshots of an unchanged database
// saw changes that were not changes.
//
// The graph is deliberately branchy and multi-cyclic: with one isolated cycle
// nondeterminism can only reorder the answer, but with overlapping cycles a
// different walk finds a DIFFERENT SET of back edges, which is the failure that
// ordering the output alone would not have fixed.
func TestCanonicalCyclePathsIsIndependentOfMapOrder(t *testing.T) {
	edges := [][2]string{
		{"a", "b"}, {"b", "c"}, {"c", "a"},
		{"c", "d"}, {"d", "b"},
		{"e", "f"}, {"f", "e"},
		{"a", "e"}, {"g", "a"}, {"g", "f"},
	}

	rng := rand.New(rand.NewSource(1))
	var first [][]string
	for attempt := range 40 {
		shuffled := slices.Clone(edges)
		rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })

		graph := map[string][]string{}
		for _, edge := range shuffled {
			graph[edge[0]] = append(graph[edge[0]], edge[1])
		}
		// Force a fresh randomized iteration order for the ROOTS as well, which
		// is the other half of what used to move.
		for _, node := range slices.Sorted(maps.Keys(graph)) {
			graph[node] = append([]string(nil), graph[node]...)
		}

		got := CanonicalCyclePaths(graph)
		if attempt == 0 {
			first = got
			continue
		}
		if !reflect.DeepEqual(got, first) {
			t.Fatalf("run %d produced %v, run 0 produced %v: the same graph must produce the same report", attempt, got, first)
		}
	}
	if len(first) == 0 {
		t.Fatal("the fixture graph has cycles; the detector found none")
	}
}

// TestCanonicalCyclePathsCollapsesParallelEdges pins the deduplication. Two rows
// for the same edge — one per dependency plane, or a duplicated row — add
// nothing to reachability, and left in place they make the same back edge report
// the same cycle twice.
func TestCanonicalCyclePathsCollapsesParallelEdges(t *testing.T) {
	graph := map[string][]string{
		"a": {"b", "b", "b"},
		"b": {"a", "a"},
	}
	got := CanonicalCyclePaths(graph)
	want := [][]string{{"a", "b"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cycles = %v, want %v: a parallel edge is not a second cycle", got, want)
	}
}

func TestCanonicalCyclePathsFindsSelfLoopsAndReportsNoneForAnAcyclicGraph(t *testing.T) {
	if got := CanonicalCyclePaths(map[string][]string{"a": {"a"}}); !reflect.DeepEqual(got, [][]string{{"a"}}) {
		t.Errorf("self-loop cycles = %v, want [[a]]", got)
	}
	if got := CanonicalCyclePaths(map[string][]string{"a": {"b"}, "b": {"c"}}); len(got) != 0 {
		t.Errorf("acyclic graph produced %v, want no cycles", got)
	}
	if got := CanonicalCyclePaths(nil); len(got) != 0 {
		t.Errorf("empty graph produced %v, want no cycles", got)
	}
}

// TestBuildCyclesReportsAnHonestPartial is the case Q5 exists for: every member
// the lookup could answer for, the ones it could not carried anyway, and the
// cycle marked.
func TestBuildCyclesReportsAnHonestPartial(t *testing.T) {
	rows := map[string]*types.Issue{
		"a": {ID: "a", Title: "A"},
		"c": {ID: "c", Title: "C"},
	}
	cycles := BuildCycles([][]string{{"a", "b", "c"}}, func(id string) *types.Issue { return rows[id] })

	if len(cycles) != 1 {
		t.Fatalf("cycles = %d, want 1", len(cycles))
	}
	cycle := cycles[0]
	if !cycle.Partial {
		t.Error("Partial = false, want true: one member could not be described")
	}
	if got := len(cycle.Members); got != 3 {
		t.Fatalf("members = %d, want 3: an unreadable member is carried, not dropped — a 3-cycle must not render as a 2-cycle", got)
	}
	for i, want := range []string{"a", "b", "c"} {
		if cycle.Members[i].ID != want {
			t.Errorf("member %d id = %q, want %q", i, cycle.Members[i].ID, want)
		}
	}
	if cycle.Members[1].Issue != nil {
		t.Error("the unreadable member carries an issue, want nil")
	}
	if cycle.Members[0].Issue == nil || cycle.Members[2].Issue == nil {
		t.Error("a readable member lost its issue")
	}
}

// TestBuildCyclesCountsAWhollyUnreadableCycle is the other half of the same
// ruling: a cycle nothing can describe is still IN the report, so the count
// cannot shrink because a row went missing.
func TestBuildCyclesCountsAWhollyUnreadableCycle(t *testing.T) {
	cycles := BuildCycles([][]string{{"ghost-a", "ghost-b"}}, func(string) *types.Issue { return nil })
	if len(cycles) != 1 {
		t.Fatalf("cycles = %d, want 1: an undescribable cycle is still a cycle", len(cycles))
	}
	if !cycles[0].Partial || len(cycles[0].Members) != 2 {
		t.Fatalf("cycle = %+v, want both members carried and Partial true", cycles[0])
	}
}

// TestBuildCyclesLooksUpEachMemberOnce pins the cache. A node on several cycles
// is one row, and a report of N cycles over M nodes must not cost N*len(cycle)
// round trips.
func TestBuildCyclesLooksUpEachMemberOnce(t *testing.T) {
	calls := map[string]int{}
	paths := [][]string{{"a", "b"}, {"a", "c"}, {"b", "c"}}
	BuildCycles(paths, func(id string) *types.Issue {
		calls[id]++
		return &types.Issue{ID: id}
	})
	for _, id := range []string{"a", "b", "c"} {
		if calls[id] != 1 {
			t.Errorf("hydrated %q %d times, want 1", id, calls[id])
		}
	}
}

// TestBuildCyclesLeavesACompleteCycleUnmarked keeps Partial from becoming
// decorative: it must be false when every member was described.
func TestBuildCyclesLeavesACompleteCycleUnmarked(t *testing.T) {
	cycles := BuildCycles([][]string{{"a", "b"}}, func(id string) *types.Issue { return &types.Issue{ID: id} })
	if cycles[0].Partial {
		t.Error("Partial = true for a fully described cycle")
	}
}
