package conformance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// This file holds the contract every implementation of publicops.CycleDetector
// must satisfy. Each case asserts what issueops/cycledetector.go PROMISES,
// cited by line; a backend that disagrees is parked at its own wiring site with
// skipKnownDivergence so the case still runs on the ones that agree.
//
// There are three wirings and TWO INDEPENDENT BODIES. The server-backed and
// embedded stores each wrap issueops.DetectCycleReportInTx in five lines around
// their own transaction, so they are one vote plus an engine check; the
// unit-of-work provider reaches the same function through the domain repository
// and is the second. The canonicalization and the partial-hydration rule are
// pure functions below all three and are pinned without a database in
// internal/storage/issueops/cycle_report_test.go. What these cases add is what
// the pure tests cannot see: which TABLES each seam reads, whether the two
// planes are merged, and which edge types are followed.
//
// SEEDING A CYCLE TAKES RAW SQL, and that is a fact about the system rather
// than a shortcut: every write path refuses to create one, so no supported verb
// produces the state this role exists to report. Cycles reach a real database by
// import, by bulk writes, by concurrent adds that each saw an acyclic graph, and
// from rows written before the gate existed.
//
// EVERY CASE SCOPES ITSELF BY MEMBER SET. The report is global — a cycle has no
// anchor to filter on — and the fixtures share one database with each other's
// cases. So a case seeds ids under the fixture prefix and then looks for the ONE
// reported cycle whose members are exactly those ids.

// CycleDetectorFixture supplies adapter-specific storage access for the cycle
// assertions. Every field but Exec is named and typed exactly like the
// per-backend roleFixtureKit hook it is filled from.
type CycleDetectorFixture struct {
	// IssuePrefix namespaces the ids each assertion seeds.
	IssuePrefix string
	Detector    publicops.CycleDetector
	// CreateIssue seeds a durable issue in the issues plane.
	CreateIssue func(context.Context, *types.Issue, string) error
	// CreateWisp seeds an ephemeral issue in the wisps plane.
	CreateWisp func(context.Context, *types.Issue, string) error
	// Exec runs every statement IN ORDER ON ONE SESSION, and is the only way to
	// seed a cycle at all; see the note above. It is NOT a roleFixtureKit hook —
	// the kit is frozen and exposes reads only.
	//
	// ONE SESSION is load-bearing. Seeding an orphaned edge means turning
	// foreign_key_checks off around the inserts, and that is a SESSION variable:
	// a hook that took one statement at a time would silently re-enable the
	// constraint between the toggle and the insert it was for.
	//
	// A nil Exec means "this backend cannot seed a cycle", and every case that
	// needs one SKIPS with that reason rather than passing quietly.
	Exec func(ctx context.Context, statements []SQLStatement) error
	// CountHistory reports how many history entries the fixture's branch has. A
	// nil hook means "this backend cannot observe history", and the case that
	// needs it SKIPS.
	CountHistory func(context.Context) (int, error)
}

// SQLStatement is one statement of a seeding script.
type SQLStatement struct {
	Query string
	Args  []any
}

// RunCycleDetectorReportsNoCycleForAnAcyclicSubgraph pins cycledetector.go:135
// from the empty side: an acyclic graph is an empty report and a nil error,
// with no ErrNotFound to classify. The nil-slice half IS asserted globally —
// that one is a property of the answer, not of the workspace
// (cycledetector.go:78).
func RunCycleDetectorReportsNoCycleForAnAcyclicSubgraph(t *testing.T, ctx context.Context, fixture CycleDetectorFixture) {
	t.Helper()
	first := fixture.IssuePrefix + "-acyclic-a"
	second := fixture.IssuePrefix + "-acyclic-b"
	seedCycleDetectorIssue(t, ctx, fixture, first)
	seedCycleDetectorIssue(t, ctx, fixture, second)
	seedCycleDetectorEdges(t, ctx, fixture, false, cycleDetectorEdge{Source: first, Target: second})

	report := cycleDetectorReport(t, ctx, fixture)
	if report.Cycles == nil {
		t.Error("Cycles is nil, want an empty slice: an acyclic answer is empty, never null")
	}
	if found := cycleDetectorTouching(report, first, second); len(found) > 0 {
		t.Errorf("an acyclic edge produced %v, want no cycle through either endpoint", found)
	}
}

// RunCycleDetectorFindsADurableCycleRotatedToItsLowestID pins the shape of a
// found cycle: members in EDGE ORDER (cycledetector.go:48-50), rotated so the
// lowest id comes first (cycledetector.go:52-58), the closing edge implied
// rather than repeated, and Partial false when every member was described
// (cycledetector.go:63).
//
// The three ids are seeded so that the edge order and the sorted order are NOT
// the same sequence, which is what makes the rotation assertion say something.
// The Issue on each member is checked by TITLE: a present-but-wrong pointer
// would pass the weaker check.
func RunCycleDetectorFindsADurableCycleRotatedToItsLowestID(t *testing.T, ctx context.Context, fixture CycleDetectorFixture) {
	t.Helper()
	// b -> c -> a -> b. Rotated to the lowest id the path reads a, b, c.
	a := fixture.IssuePrefix + "-tri-a"
	b := fixture.IssuePrefix + "-tri-b"
	c := fixture.IssuePrefix + "-tri-c"
	for _, id := range []string{a, b, c} {
		seedCycleDetectorIssue(t, ctx, fixture, id)
	}
	seedCycleDetectorEdges(t, ctx, fixture, false,
		cycleDetectorEdge{Source: a, Target: b},
		cycleDetectorEdge{Source: b, Target: c},
		cycleDetectorEdge{Source: c, Target: a})

	cycle := cycleDetectorFind(t, cycleDetectorReport(t, ctx, fixture), a, b, c)
	assertCycleDetectorPath(t, cycle, a, b, c)
	if cycle.Partial {
		t.Error("Partial = true for a cycle whose every member is a live issue")
	}
	for _, member := range cycle.Members {
		if member.Issue == nil {
			t.Fatalf("member %s carries no issue, want the seeded row", member.ID)
			continue
		}
		if member.Issue.Title != member.ID {
			t.Errorf("member %s hydrated to the row titled %q, want %q: the hydration read a different row than the walk named",
				member.ID, member.Issue.Title, member.ID)
		}
	}
}

// RunCycleDetectorReportsTheSameCyclesEveryRun pins cycledetector.go:52-58 and
// :91-92: two calls against an unchanged database must agree, id for id and
// cycle for cycle.
//
// It compares the WHOLE report rather than the case's own cycle: the walk used
// to iterate a Go map, so an unrelated cycle elsewhere could change which back
// edges this one's nodes were reached by. Several calls, because a map with few
// keys can iterate the same way twice by chance.
func RunCycleDetectorReportsTheSameCyclesEveryRun(t *testing.T, ctx context.Context, fixture CycleDetectorFixture) {
	t.Helper()
	// A branchy fixture: two cycles sharing a node, plus a tail into them, so
	// the walk has a real choice of entry points and back edges.
	hub := fixture.IssuePrefix + "-det-hub"
	left := fixture.IssuePrefix + "-det-left"
	right := fixture.IssuePrefix + "-det-right"
	tail := fixture.IssuePrefix + "-det-tail"
	for _, id := range []string{hub, left, right, tail} {
		seedCycleDetectorIssue(t, ctx, fixture, id)
	}
	seedCycleDetectorEdges(t, ctx, fixture, false,
		cycleDetectorEdge{Source: hub, Target: left}, cycleDetectorEdge{Source: left, Target: hub},
		cycleDetectorEdge{Source: hub, Target: right}, cycleDetectorEdge{Source: right, Target: hub},
		cycleDetectorEdge{Source: tail, Target: hub})

	first := cycleDetectorPaths(cycleDetectorReport(t, ctx, fixture))
	for run := 2; run <= 4; run++ {
		got := cycleDetectorPaths(cycleDetectorReport(t, ctx, fixture))
		if !reflect.DeepEqual(got, first) {
			t.Fatalf("run %d reported %v, run 1 reported %v: the same database must report the same cycles", run, got, first)
		}
	}
	if len(first) == 0 {
		t.Fatal("the seeded graph has cycles; the detector found none")
	}
}

// RunCycleDetectorMergesTheDurableAndEphemeralPlanes pins
// cycledetector.go:120-122: the two dependency planes are one graph, so a cycle
// that runs issue → wisp → issue is found. This is the clause a single-table
// read passes every other case and fails here.
func RunCycleDetectorMergesTheDurableAndEphemeralPlanes(t *testing.T, ctx context.Context, fixture CycleDetectorFixture) {
	t.Helper()
	issue := fixture.IssuePrefix + "-plane-issue"
	wisp := fixture.IssuePrefix + "-plane-wisp"
	seedCycleDetectorIssue(t, ctx, fixture, issue)
	seedCycleDetectorWisp(t, ctx, fixture, wisp)

	// The edge follows its SOURCE's plane, and its target column follows the
	// TARGET's class — the same routing the dependency editor performs.
	seedCycleDetectorEdges(t, ctx, fixture, false,
		cycleDetectorEdge{Table: "dependencies", Source: issue, Target: wisp, TargetColumn: "depends_on_wisp_id"},
		cycleDetectorEdge{Table: "wisp_dependencies", Source: wisp, Target: issue})

	cycle := cycleDetectorFind(t, cycleDetectorReport(t, ctx, fixture), issue, wisp)
	assertCycleDetectorPath(t, cycle, issue, wisp)
	if cycle.Partial {
		t.Error("Partial = true: a wisp is hydratable, so a cross-plane cycle is fully described")
	}
}

// RunCycleDetectorFollowsOnlyBlockingEdges pins cycledetector.go:110-119: the
// walk follows `blocks` and `conditional-blocks`, and nothing else — not
// `waits-for`, whose gate semantics make a mutual wait legitimate, and not
// `parent-child`, which the ADD-time gate does walk.
//
// The conditional-blocks half is asserted in the same case as the exclusions on
// purpose: a body narrowed to `blocks` alone would pass an exclusion-only case,
// and a body that walked everything would pass an inclusion-only one.
func RunCycleDetectorFollowsOnlyBlockingEdges(t *testing.T, ctx context.Context, fixture CycleDetectorFixture) {
	t.Helper()
	for _, test := range []struct {
		edgeType types.DependencyType
		slug     string
		want     bool
	}{
		{types.DepConditionalBlocks, "cond", true},
		{types.DepWaitsFor, "wait", false},
		{types.DepParentChild, "kid", false},
		{types.DepRelated, "rel", false},
	} {
		first := fmt.Sprintf("%s-edge-%s-a", fixture.IssuePrefix, test.slug)
		second := fmt.Sprintf("%s-edge-%s-b", fixture.IssuePrefix, test.slug)
		seedCycleDetectorIssue(t, ctx, fixture, first)
		seedCycleDetectorIssue(t, ctx, fixture, second)
		seedCycleDetectorEdges(t, ctx, fixture, false,
			cycleDetectorEdge{Source: first, Target: second, Type: test.edgeType},
			cycleDetectorEdge{Source: second, Target: first, Type: test.edgeType})

		found := cycleDetectorTouching(cycleDetectorReport(t, ctx, fixture), first, second)
		switch {
		case test.want && len(found) == 0:
			t.Errorf("a mutual %s pair produced no cycle; that edge type schedules work and belongs to the walk", test.edgeType)
		case !test.want && len(found) > 0:
			t.Errorf("a mutual %s pair produced %v; that edge type is outside this walk", test.edgeType, found)
		}
	}
}

// RunCycleDetectorReportsAnHonestPartial pins cycledetector.go:140-144 and
// :24-33 against real storage: a member the database cannot describe keeps its
// place on the path, carries no issue, and marks the cycle. The previous body
// dropped the member, so this three-node cycle came back as a TWO-node cycle and
// looked complete.
func RunCycleDetectorReportsAnHonestPartial(t *testing.T, ctx context.Context, fixture CycleDetectorFixture) {
	t.Helper()
	live := fixture.IssuePrefix + "-partial-a"
	alsoLive := fixture.IssuePrefix + "-partial-c"
	ghost := fixture.IssuePrefix + "-partial-b-ghost"
	seedCycleDetectorIssue(t, ctx, fixture, live)
	seedCycleDetectorIssue(t, ctx, fixture, alsoLive)

	seedCycleDetectorEdges(t, ctx, fixture, true,
		cycleDetectorEdge{Source: live, Target: ghost},
		cycleDetectorEdge{Source: ghost, Target: alsoLive},
		cycleDetectorEdge{Source: alsoLive, Target: live})

	cycle := cycleDetectorFind(t, cycleDetectorReport(t, ctx, fixture), live, ghost, alsoLive)
	if got := len(cycle.Members); got != 3 {
		t.Fatalf("members = %d, want 3: an undescribable member is carried, not dropped — a 3-cycle must not render as a 2-cycle", got)
	}
	assertCycleDetectorPath(t, cycle, live, ghost, alsoLive)
	if !cycle.Partial {
		t.Error("Partial = false, want true: one member has no row behind it")
	}
	for _, member := range cycle.Members {
		switch {
		case member.ID == ghost && member.Issue != nil:
			t.Errorf("the ghost member hydrated to %+v, want a nil issue", member.Issue)
		case member.ID != ghost && member.Issue == nil:
			t.Errorf("member %s lost its issue; one unreadable member must not blank the readable ones", member.ID)
		}
	}
}

// RunCycleDetectorCountsAWhollyUndescribableCycle pins cycledetector.go:81-85:
// a cycle no member of which can be described is still IN the report, so the
// count cannot shrink because rows went missing. Under the previous body this
// cycle produced nothing at all, so `bd dep cycles` printed a smaller number and
// a workspace looked healthier than it was.
func RunCycleDetectorCountsAWhollyUndescribableCycle(t *testing.T, ctx context.Context, fixture CycleDetectorFixture) {
	t.Helper()
	first := fixture.IssuePrefix + "-ghost-a"
	second := fixture.IssuePrefix + "-ghost-b"
	seedCycleDetectorEdges(t, ctx, fixture, true,
		cycleDetectorEdge{Source: first, Target: second},
		cycleDetectorEdge{Source: second, Target: first})

	cycle := cycleDetectorFind(t, cycleDetectorReport(t, ctx, fixture), first, second)
	assertCycleDetectorPath(t, cycle, first, second)
	if !cycle.Partial {
		t.Error("Partial = false, want true: neither member has a row behind it")
	}
	for _, member := range cycle.Members {
		if member.Issue != nil {
			t.Errorf("member %s hydrated to %+v, want a nil issue", member.ID, member.Issue)
		}
	}
}

// RunCycleDetectorWritesNothing pins cycledetector.go:130-131: detecting is a
// read. It is asserted on the history log rather than on a row read-back
// because every versioned unit of work in this tree ends in a Dolt commit, so a
// sweep that took a write transaction would show up here even when it changed
// no column.
func RunCycleDetectorWritesNothing(t *testing.T, ctx context.Context, fixture CycleDetectorFixture) {
	t.Helper()
	if fixture.CountHistory == nil {
		t.Skip("fixture cannot observe history: CountHistory is nil, so the detecting-is-a-read clause is unpinned on this backend")
	}
	before, err := fixture.CountHistory(ctx)
	if err != nil {
		t.Fatalf("count history entries: %v", err)
	}

	cycleDetectorReport(t, ctx, fixture)
	cycleDetectorReport(t, ctx, fixture)

	after, err := fixture.CountHistory(ctx)
	if err != nil {
		t.Fatalf("count history entries: %v", err)
	}
	if after != before {
		t.Errorf("history entries went %d -> %d across two sweeps, want no change: detecting is a read", before, after)
	}
}

// cycleDetectorEdge is one raw blocking edge a case seeds. The zero value of
// every optional field is the ordinary durable, issue-targeted `blocks` edge.
type cycleDetectorEdge struct {
	// Table is "dependencies" (default) or "wisp_dependencies". It follows the
	// SOURCE's plane, exactly as a real add does.
	Table string
	// TargetColumn is the typed column the target id lands in:
	// depends_on_issue_id (default), depends_on_wisp_id, or depends_on_external.
	TargetColumn string
	Source       string
	Target       string
	// Type defaults to blocks.
	Type types.DependencyType
}

// seedCycleDetectorEdges writes edges with raw SQL, bypassing every cycle gate.
//
// It writes them all in ONE session because of orphaned: a cycle whose members
// are not all rows needs foreign_key_checks off, and that is a session variable.
// The constraint is real — dependencies.issue_id references issues(id) and
// wisp_dependencies.issue_id references wisps(id), both ON DELETE CASCADE.
//
// Each edge's primary key is derived from the edge rather than random, so that
// re-running a suite against a surviving database is idempotent, and hashed
// because the column is CHAR(36) and the ids are not.
func seedCycleDetectorEdges(t *testing.T, ctx context.Context, fixture CycleDetectorFixture, orphaned bool, edges ...cycleDetectorEdge) {
	t.Helper()
	if fixture.Exec == nil {
		t.Skip("fixture cannot write raw SQL: Exec is nil, and no supported verb creates a cycle, so this backend cannot be given one to report")
	}

	var script []SQLStatement
	if orphaned {
		script = append(script, SQLStatement{Query: "SET foreign_key_checks = 0"})
	}
	for _, edge := range edges {
		table := edge.Table
		if table == "" {
			table = "dependencies"
		}
		column := edge.TargetColumn
		if column == "" {
			column = "depends_on_issue_id"
		}
		edgeType := edge.Type
		if edgeType == "" {
			edgeType = types.DepBlocks
		}
		//nolint:gosec // G201: table and column are chosen from the closed sets above.
		query := fmt.Sprintf(
			"INSERT INTO %s (id, issue_id, %s, type, created_at, created_by, metadata) VALUES (?, ?, ?, ?, NOW(), 'seed', '{}')",
			table, column)
		script = append(script, SQLStatement{
			Query: query,
			Args:  []any{cycleDetectorEdgeID(edge.Source, edge.Target, edgeType), edge.Source, edge.Target, string(edgeType)},
		})
	}
	if orphaned {
		script = append(script, SQLStatement{Query: "SET foreign_key_checks = 1"})
	}

	if err := fixture.Exec(ctx, script); err != nil {
		t.Fatalf("seed %d edge(s) (orphaned=%v): %v", len(edges), orphaned, err)
	}
}

// cycleDetectorEdgeID is a stable 36-character key for one edge.
func cycleDetectorEdgeID(source, target string, edgeType types.DependencyType) string {
	sum := sha256.Sum256([]byte(source + "\x00" + target + "\x00" + string(edgeType)))
	return "cyc" + hex.EncodeToString(sum[:])[:33]
}

func seedCycleDetectorIssue(t *testing.T, ctx context.Context, fixture CycleDetectorFixture, id string) {
	t.Helper()
	if err := fixture.CreateIssue(ctx, cycleDetectorSeed(id), "seed"); err != nil {
		t.Fatalf("seed issue %s: %v", id, err)
	}
}

func seedCycleDetectorWisp(t *testing.T, ctx context.Context, fixture CycleDetectorFixture, id string) {
	t.Helper()
	issue := cycleDetectorSeed(id)
	issue.Ephemeral = true
	if err := fixture.CreateWisp(ctx, issue, "seed"); err != nil {
		t.Fatalf("seed wisp %s: %v", id, err)
	}
}

// cycleDetectorSeed titles each row with its own id, so a hydration assertion
// can tell the right row from a row.
func cycleDetectorSeed(id string) *types.Issue {
	return &types.Issue{
		ID:        id,
		Title:     id,
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
}

func cycleDetectorReport(t *testing.T, ctx context.Context, fixture CycleDetectorFixture) publicops.CycleReport {
	t.Helper()
	report, err := fixture.Detector.DetectCycles(ctx, publicops.DetectCyclesRequest{})
	if err != nil {
		t.Fatalf("DetectCycles: %v", err)
	}
	return report
}

// cycleDetectorPaths renders a report as id paths, which is what the
// determinism case compares.
func cycleDetectorPaths(report publicops.CycleReport) [][]string {
	out := make([][]string, 0, len(report.Cycles))
	for _, cycle := range report.Cycles {
		path := make([]string, 0, len(cycle.Members))
		for _, member := range cycle.Members {
			path = append(path, member.ID)
		}
		out = append(out, path)
	}
	return out
}

// cycleDetectorFind returns the one reported cycle whose members are exactly
// ids, and fails the case when there is not exactly one. Matching on the whole
// member SET rather than on containment is what makes the scoping honest: a
// body that added a node to the path, or dropped one, does not pass.
func cycleDetectorFind(t *testing.T, report publicops.CycleReport, ids ...string) publicops.Cycle {
	t.Helper()
	want := slices.Sorted(slices.Values(ids))
	var matches []publicops.Cycle
	for _, cycle := range report.Cycles {
		got := make([]string, 0, len(cycle.Members))
		for _, member := range cycle.Members {
			got = append(got, member.ID)
		}
		slices.Sort(got)
		if slices.Equal(got, want) {
			matches = append(matches, cycle)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("cycles over exactly {%s} = %d, want 1; the whole report was %v",
			strings.Join(want, ", "), len(matches), cycleDetectorPaths(report))
	}
	return matches[0]
}

// cycleDetectorTouching returns the paths of every reported cycle that mentions
// any of ids, for the cases that assert a cycle is ABSENT.
func cycleDetectorTouching(report publicops.CycleReport, ids ...string) [][]string {
	var out [][]string
	for _, path := range cycleDetectorPaths(report) {
		if slices.ContainsFunc(path, func(id string) bool { return slices.Contains(ids, id) }) {
			out = append(out, path)
		}
	}
	return out
}

// assertCycleDetectorPath checks a cycle's path against the expected edge
// order, which the caller may state starting anywhere: it rotates the
// expectation itself.
func assertCycleDetectorPath(t *testing.T, cycle publicops.Cycle, edgeOrder ...string) {
	t.Helper()
	lowest := 0
	for i, id := range edgeOrder {
		if id < edgeOrder[lowest] {
			lowest = i
		}
	}
	want := append(append([]string{}, edgeOrder[lowest:]...), edgeOrder[:lowest]...)

	got := make([]string, 0, len(cycle.Members))
	for _, member := range cycle.Members {
		got = append(got, member.ID)
	}
	if !slices.Equal(got, want) {
		t.Errorf("path = %v, want %v: members run in edge order from the lowest id, with the closing edge implied", got, want)
	}
}
