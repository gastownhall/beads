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

// The IncludeTracks widening: CanonicalMixedCyclePaths adds `tracks` edges to
// the walk but must report a cycle only when a scheduling (blocks/
// conditional-blocks) edge is also on it. This is the gc-818bx guard: the
// shape it exists to surface is a molecule root that blocks-depends
// (transitively) on its own entry step, which tracks-depends back to the
// root, WITHOUT reopening the "thousands of cycles" regression a
// tracks-in-the-plain-walk change caused and had to revert (ordinary convoy
// topology loops constantly through tracks alone).

// TestCanonicalMixedCyclePathsIgnoresAPureTracksLoop pins the regression
// guard directly: a loop made entirely of tracks edges — the shape a convoy
// and its tracked issues form routinely — must not be reported.
func TestCanonicalMixedCyclePathsIgnoresAPureTracksLoop(t *testing.T) {
	graph := map[string][]MixedCycleEdge{
		"a": {{To: "b"}},
		"b": {{To: "c"}},
		"c": {{To: "a"}},
	}
	if got := mustMixedCyclePaths(t, graph); len(got) != 0 {
		t.Errorf("pure-tracks loop reported %v, want no cycles: tracks-only convoy topology is not a deadlock", got)
	}
}

// TestCanonicalMixedCyclePathsFindsTheMoleculeRootShape pins the case gc-818bx
// exists for: a root that blocks-depends (transitively) on its own entry
// step, closed by a tracks edge back to the root. Neither
// CanonicalCyclePaths (no tracks edge at all) nor a plain-tracks walk (no
// scheduling requirement) would report this correctly — the former misses it
// entirely, the latter would drown it in every other tracks loop.
func TestCanonicalMixedCyclePathsFindsTheMoleculeRootShape(t *testing.T) {
	graph := map[string][]MixedCycleEdge{
		"root": {{To: "step", Scheduling: true}},
		"step": {{To: "root"}}, // tracks edge closing the loop
	}
	got := mustMixedCyclePaths(t, graph)
	want := [][]string{{"root", "step"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cycles = %v, want %v: a blocks edge plus a closing tracks edge is exactly the deadlock this option surfaces", got, want)
	}
}

// TestCanonicalMixedCyclePathsFindsASchedulingCycleAfterATracksOnlyBackEdge
// pins the edge-labelled DFS trap from the exact-head review of gc-818bx. A
// global visited set can first close and ignore the tracks-only a-b loop, then
// miss the qualifying a-c-b-a loop because b is visited but no longer on the
// active path when c reaches it.
func TestCanonicalMixedCyclePathsFindsASchedulingCycleAfterATracksOnlyBackEdge(t *testing.T) {
	graph := map[string][]MixedCycleEdge{
		"a": {
			{To: "b"},
			{To: "c", Scheduling: true},
		},
		"b": {{To: "a"}},
		"c": {{To: "b"}},
	}

	got := mustMixedCyclePaths(t, graph)
	want := [][]string{{"a", "c", "b"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cycles = %v, want %v: a tracks-only back edge must not hide a different cycle containing a scheduling edge", got, want)
	}
}

// TestCanonicalMixedCyclePathsStillFindsAPureSchedulingCycle keeps parity with
// CanonicalCyclePaths for the case that needs no tracks edge at all.
func TestCanonicalMixedCyclePathsStillFindsAPureSchedulingCycle(t *testing.T) {
	graph := map[string][]MixedCycleEdge{
		"a": {{To: "b", Scheduling: true}},
		"b": {{To: "a", Scheduling: true}},
	}
	got := mustMixedCyclePaths(t, graph)
	want := [][]string{{"a", "b"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cycles = %v, want %v", got, want)
	}
}

// TestCanonicalMixedCyclePathsAcceptsASchedulingEdgeAnywhereOnTheLoop checks
// that the requirement is "at least one scheduling edge on the cycle", not
// specifically on the closing edge — a longer chain where the blocks edge
// sits in the middle must still be reported.
func TestCanonicalMixedCyclePathsAcceptsASchedulingEdgeAnywhereOnTheLoop(t *testing.T) {
	graph := map[string][]MixedCycleEdge{
		"a": {{To: "b"}},                   // tracks
		"b": {{To: "c", Scheduling: true}}, // blocks, in the middle of the loop
		"c": {{To: "a"}},                   // tracks, closes the loop
	}
	got := mustMixedCyclePaths(t, graph)
	want := [][]string{{"a", "b", "c"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cycles = %v, want %v: a scheduling edge anywhere on the loop must qualify it, not only the closing edge", got, want)
	}
}

// TestCanonicalMixedCyclePathsSelfLoop mirrors
// TestCanonicalCyclePathsFindsSelfLoopsAndReportsNoneForAnAcyclicGraph for the
// mixed graph: a self-loop qualifies exactly when its own edge is scheduling.
func TestCanonicalMixedCyclePathsSelfLoop(t *testing.T) {
	if got := mustMixedCyclePaths(t, map[string][]MixedCycleEdge{"a": {{To: "a", Scheduling: true}}}); !reflect.DeepEqual(got, [][]string{{"a"}}) {
		t.Errorf("blocking self-loop = %v, want [[a]]", got)
	}
	if got := mustMixedCyclePaths(t, map[string][]MixedCycleEdge{"a": {{To: "a"}}}); len(got) != 0 {
		t.Errorf("tracks-only self-loop = %v, want no cycles", got)
	}
	if got := mustMixedCyclePaths(t, nil); len(got) != 0 {
		t.Errorf("empty graph produced %v, want no cycles", got)
	}
}

// TestCanonicalMixedCyclePathsCollapsesParallelEdgesByOringScheduling mirrors
// TestCanonicalCyclePathsCollapsesParallelEdges: two rows for the same pair of
// nodes — one a tracks edge, one blocking — must collapse to one edge that is
// scheduling, not fall out of the requirement because the dedup happened to
// keep the tracks-flavored copy.
func TestCanonicalMixedCyclePathsCollapsesParallelEdgesByOringScheduling(t *testing.T) {
	graph := map[string][]MixedCycleEdge{
		"a": {{To: "b"}, {To: "b", Scheduling: true}},
		"b": {{To: "a"}},
	}
	got := mustMixedCyclePaths(t, graph)
	want := [][]string{{"a", "b"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cycles = %v, want %v: a duplicated edge where either copy is scheduling must count as scheduling", got, want)
	}
}

// mustMixedCyclePaths runs CanonicalMixedCyclePaths and fails the test on its
// error, which only a broken internal invariant can produce.
func mustMixedCyclePaths(t *testing.T, graph map[string][]MixedCycleEdge) [][]string {
	t.Helper()
	paths, err := CanonicalMixedCyclePaths(graph)
	if err != nil {
		t.Fatalf("CanonicalMixedCyclePaths: %v", err)
	}
	return paths
}

// TestCloseMixedCycleReportsAMissingReturnPathAsAnError pins the invariant
// breach as an error rather than a panic. CanonicalMixedCyclePaths cannot
// reach it (it only closes edges inside a strongly connected component), so the
// helper is exercised directly with an edge that has no way back.
func TestCloseMixedCycleReportsAMissingReturnPathAsAnError(t *testing.T) {
	cycle, err := closeMixedCycle(map[string][]string{"a": {"b"}}, "a", "b")
	if err == nil {
		t.Fatalf("closeMixedCycle = %v, nil error; want an error: b cannot reach a", cycle)
	}
	if cycle != nil {
		t.Errorf("closeMixedCycle returned %v alongside its error, want nil", cycle)
	}

	cycle, err = closeMixedCycle(map[string][]string{"a": {"c"}, "c": {"b"}, "b": {"a"}}, "c", "b")
	if err != nil {
		t.Fatalf("closeMixedCycle on a real cycle: %v", err)
	}
	if want := []string{"a", "c", "b"}; !slices.Equal(cycle, want) {
		t.Errorf("closeMixedCycle = %v, want %v: the edge, then its return path, rotated to the lowest id", cycle, want)
	}
}

// TestCanonicalMixedCyclePathsKeepsDeadlocksATracksEdgeFuses is the #6148
// review reproduction. a<->b and c<->d are two separate blocks deadlocks; the
// tracks edges b->c and d->a fuse them into ONE strongly connected component.
// A widening option must not shrink the answer: both deadlocks the default
// walk reports are still reported.
func TestCanonicalMixedCyclePathsKeepsDeadlocksATracksEdgeFuses(t *testing.T) {
	plain := map[string][]string{"a": {"b"}, "b": {"a"}, "c": {"d"}, "d": {"c"}}
	mixed := map[string][]MixedCycleEdge{
		"a": {{To: "b", Scheduling: true}},
		"b": {{To: "a", Scheduling: true}, {To: "c"}},
		"c": {{To: "d", Scheduling: true}},
		"d": {{To: "c", Scheduling: true}, {To: "a"}},
	}
	want := CanonicalCyclePaths(plain)
	if !reflect.DeepEqual(want, [][]string{{"a", "b"}, {"c", "d"}}) {
		t.Fatalf("default walk = %v, want [[a b] [c d]]: the fixture no longer says what this test claims", want)
	}
	got := mustMixedCyclePaths(t, mixed)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("widened = %v, want %v: tracks edges fusing two deadlocks into one component must not drop either", got, want)
	}
}

// TestCanonicalMixedCyclePathsReportsTheChordTheDefaultWalkMisses pins the
// example CycleReport.Cycles documents. On a -> b -> c -> a plus the chord
// a -> c, the default walk crosses a -> c after c is finished and reports only
// a-b-c. The widened report keeps a-b-c and adds a-c, which the chord closes by
// its shortest return path, even with no tracks edge in the graph.
func TestCanonicalMixedCyclePathsReportsTheChordTheDefaultWalkMisses(t *testing.T) {
	if got, want := CanonicalCyclePaths(map[string][]string{"a": {"b", "c"}, "b": {"c"}, "c": {"a"}}), [][]string{{"a", "b", "c"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("default walk = %v, want %v: the documented example no longer holds", got, want)
	}
	got := mustMixedCyclePaths(t, map[string][]MixedCycleEdge{
		"a": {{To: "b", Scheduling: true}, {To: "c", Scheduling: true}},
		"b": {{To: "c", Scheduling: true}},
		"c": {{To: "a", Scheduling: true}},
	})
	if want := [][]string{{"a", "b", "c"}, {"a", "c"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("widened = %v, want %v", got, want)
	}
}

// TestCanonicalMixedCyclePathsIsASupersetOfTheDefaultReport checks the
// widening promise over generated graphs rather than one fixture: on the same
// data, every cycle CanonicalCyclePaths reports on the scheduling edges alone
// is also in the widened report. Each widened cycle must also be a real cycle
// carrying a scheduling edge, stay within the documented size bound, and not
// move when the edge rows arrive in a different order.
func TestCanonicalMixedCyclePathsIsASupersetOfTheDefaultReport(t *testing.T) {
	type rawEdge struct {
		from, to   string
		scheduling bool
	}
	ids := []string{"a", "b", "c", "d", "e", "f"}
	rng := rand.New(rand.NewSource(6148))
	widenedBeyondDefault := 0
	for trial := range 600 {
		n := 2 + rng.Intn(len(ids)-1)
		edges := make([]rawEdge, 0, 3*n)
		for range rng.Intn(3 * n) {
			edges = append(edges, rawEdge{ids[rng.Intn(n)], ids[rng.Intn(n)], rng.Intn(2) == 0})
		}

		build := func(order []rawEdge) (map[string][]string, map[string][]MixedCycleEdge) {
			plain := map[string][]string{}
			mixed := map[string][]MixedCycleEdge{}
			for _, e := range order {
				mixed[e.from] = append(mixed[e.from], MixedCycleEdge{To: e.to, Scheduling: e.scheduling})
				if e.scheduling {
					plain[e.from] = append(plain[e.from], e.to)
				}
			}
			return plain, mixed
		}
		plain, mixed := build(edges)
		want := CanonicalCyclePaths(plain)
		got := mustMixedCyclePaths(t, mixed)

		for _, cycle := range want {
			if !slices.ContainsFunc(got, func(c []string) bool { return slices.Equal(c, cycle) }) {
				t.Fatalf("trial %d, edges %v: default cycle %v is missing from the widened report %v (default report %v)",
					trial, edges, cycle, got, want)
			}
		}

		scheduling := map[[2]string]bool{}
		for _, e := range edges {
			key := [2]string{e.from, e.to}
			scheduling[key] = scheduling[key] || e.scheduling
		}
		schedulingEdges := 0
		for _, isScheduling := range scheduling {
			if isScheduling {
				schedulingEdges++
			}
		}
		if bound := schedulingEdges + len(want); len(got) > bound {
			t.Fatalf("trial %d, edges %v: widened report has %d cycles, above the bound %d (scheduling edges + default cycles)",
				trial, edges, len(got), bound)
		}
		if len(got) < len(want) {
			t.Fatalf("trial %d: widened report %v is smaller than the default %v", trial, got, want)
		}
		if len(got) > len(want) && len(want) > 0 {
			widenedBeyondDefault++
		}
		for _, cycle := range got {
			if len(slices.Compact(slices.Sorted(slices.Values(cycle)))) != len(cycle) {
				t.Fatalf("trial %d, edges %v: widened cycle %v repeats a member", trial, edges, cycle)
			}
			hasScheduling := false
			for i, from := range cycle {
				isScheduling, ok := scheduling[[2]string{from, cycle[(i+1)%len(cycle)]}]
				if !ok {
					t.Fatalf("trial %d, edges %v: widened cycle %v uses %s -> %s, which is not an edge",
						trial, edges, cycle, from, cycle[(i+1)%len(cycle)])
				}
				hasScheduling = hasScheduling || isScheduling
			}
			if !hasScheduling {
				t.Fatalf("trial %d, edges %v: widened cycle %v is made only of tracks edges", trial, edges, cycle)
			}
		}

		shuffled := slices.Clone(edges)
		rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		_, reordered := build(shuffled)
		if again := mustMixedCyclePaths(t, reordered); !reflect.DeepEqual(again, got) {
			t.Fatalf("trial %d: the same edges in another order produced %v, want %v", trial, again, got)
		}
	}
	if widenedBeyondDefault == 0 {
		t.Fatal("no generated graph had a widened report larger than a non-empty default one; the generator is not exercising mixed cycles")
	}
}

// TestCanonicalMixedCyclePathsIsIndependentOfMapOrder mirrors
// TestCanonicalCyclePathsIsIndependentOfMapOrder against the mixed walk: it is
// a separate implementation and must earn the same determinism guarantee
// independently.
func TestCanonicalMixedCyclePathsIsIndependentOfMapOrder(t *testing.T) {
	type rawEdge struct {
		from, to   string
		scheduling bool
	}
	edges := []rawEdge{
		{"a", "b", true}, {"b", "c", false}, {"c", "a", false},
		{"c", "d", true}, {"d", "b", false},
		{"e", "f", false}, {"f", "e", false},
		{"a", "e", true}, {"g", "a", false}, {"g", "f", false},
	}

	rng := rand.New(rand.NewSource(1))
	var first [][]string
	for attempt := range 40 {
		shuffled := slices.Clone(edges)
		rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })

		graph := map[string][]MixedCycleEdge{}
		for _, edge := range shuffled {
			graph[edge.from] = append(graph[edge.from], MixedCycleEdge{To: edge.to, Scheduling: edge.scheduling})
		}

		got := mustMixedCyclePaths(t, graph)
		if attempt == 0 {
			first = got
			continue
		}
		if !reflect.DeepEqual(got, first) {
			t.Fatalf("run %d produced %v, run 0 produced %v: the same graph must produce the same report", attempt, got, first)
		}
	}
	if len(first) == 0 {
		t.Fatal("the fixture graph has qualifying cycles; the detector found none")
	}
}
