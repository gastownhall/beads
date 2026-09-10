package conformance

import (
	"context"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// This file holds the contract every implementation of publicops.GraphCounter
// must satisfy. Each case asserts what issueops/graphcounter.go PROMISES, cited
// by symbol; a backend that disagrees is parked at its own wiring site with
// skipKnownDivergence so the case still runs on the ones that agree.
//
// THREE LEGS, ONE BODY. All three reach the same
// storage/issueops.ExecuteEdgeCount: the two stores wrap it in their own read
// transaction, and the unit-of-work provider reaches it through the domain
// repository, whose runner publishes exactly the DBTX method set that function
// takes. So a three-leg run here is ONE READING plus two engine checks and
// three wrapper checks — the same arithmetic TreeWalker's and MetadataCAS's
// contracts state — and it is still worth running, because every measured drift
// in the graph family has lived in a WRAPPER. The cases are written for that:
// they assert SENTINELS rather than message text, so a wrapper that loses a
// transaction, drops a request field or breaks errors.Is is what a per-leg
// failure would actually be.
//
// The parts of the answer that are pure — the missing-anchor rule and the
// request vocabulary — are pinned without a database beside the body
// (internal/storage/issueops/edge_counts_test.go), so what is left here is what
// only a real backend can show: which rows the two dependency planes actually
// contribute, and what a status join reaches.
//
// EVERY CASE NAMES THE EXACT IDS IT SEEDED. The three fixtures share one
// database per suite and the two store fixtures share it with every other
// role's cases, so an assertion that read "every anchor" would be an assertion
// about the whole workspace and would fail the moment a sibling suite seeded a
// row.
//
// What is deliberately NOT here:
//   - the comment count and the event count, which this role excludes and says
//     why at GraphCounter's doc;
//   - a "both directions" request, which this role does not have (see
//     EdgeCountRequest's doc) — the two directions are asserted as two calls,
//     which is what a caller wanting the pair does;
//   - anything about the far end's existence, which this role does not probe:
//     a dangling target is counted like any other edge (AnchorEdgeCount.Missing
//     says so), and RunGraphCounterCountsOutboundEdges seeds one to prove it.

// GraphCounterFixture supplies adapter-specific storage access for the
// edge-count assertions. Every field is named and typed exactly like the
// per-backend roleFixtureKit hook it is filled from.
type GraphCounterFixture struct {
	// IssuePrefix namespaces the ids each assertion seeds, so several of them
	// can share one database.
	IssuePrefix  string
	GraphCounter publicops.GraphCounter
	// CreateIssue seeds a durable issue in the issues plane.
	CreateIssue func(context.Context, *types.Issue, string) error
	// CreateWisp seeds an ephemeral issue in the wisps plane. It is a separate
	// field rather than an Ephemeral flag on CreateIssue because the three
	// adapters reach the two planes through different verbs.
	CreateWisp func(context.Context, *types.Issue, string) error
	// AddDependency seeds ONE edge, routed to the plane the edge's source lives
	// in. That routing is what makes the cross-plane cases possible at all: a
	// wisp source puts its edge in wisp_dependencies and nothing else can.
	AddDependency func(context.Context, *types.Dependency, string) error
	// CountHistory reports how many history entries the fixture's branch has.
	// A nil hook means "this backend cannot observe history", and the case that
	// needs it SKIPS rather than passing quietly.
	CountHistory func(context.Context) (int, error)
}

// RunGraphCounterCountsOutboundEdges pins EdgeDirectionOut: the anchor's
// DEPENDENCIES, the edges whose source is the anchor.
//
// One of the three targets is a dangling "external:" reference, which pins
// AnchorEdgeCount.Missing's last paragraph: this role does not probe the far
// end, so an edge to a row no database holds still counts. A body that joined
// the target to an issue table to count would answer 2 here.
func RunGraphCounterCountsOutboundEdges(t *testing.T, ctx context.Context, fixture GraphCounterFixture) {
	t.Helper()
	anchor := fixture.IssuePrefix + "-out-src"
	first := fixture.IssuePrefix + "-out-a"
	second := fixture.IssuePrefix + "-out-b"
	dangling := "external:" + fixture.IssuePrefix + "-out-ext"
	seedGraphCounterIssues(t, ctx, fixture, anchor, first, second)
	seedGraphCounterEdge(t, ctx, fixture, anchor, first, types.DepBlocks)
	seedGraphCounterEdge(t, ctx, fixture, anchor, second, types.DepBlocks)
	seedGraphCounterEdge(t, ctx, fixture, anchor, dangling, types.DepRelated)

	result := countEdges(t, ctx, fixture, publicops.EdgeCountRequest{
		IDs: []string{anchor}, Direction: publicops.EdgeDirectionOut,
	})
	assertGraphCounterCount(t, result, anchor, 3)

	// The two targets have no outbound edges of their own, so the same request
	// answers 0 for them — which is what makes the number above the anchor's
	// and not the workspace's.
	zeroes := countEdges(t, ctx, fixture, publicops.EdgeCountRequest{
		IDs: []string{first, second}, Direction: publicops.EdgeDirectionOut,
	})
	assertGraphCounterCount(t, zeroes, first, 0)
	assertGraphCounterCount(t, zeroes, second, 0)
}

// RunGraphCounterCountsInboundEdges pins EdgeDirectionIn: the anchor's
// DEPENDENTS, the edges whose target is the anchor.
//
// The graph is deliberately ASYMMETRIC — the anchor has two dependents and one
// dependency — so the two directions answer different numbers over the same
// rows. An implementation that ignored Direction, or that answered a constant,
// passes neither this case nor the one above.
func RunGraphCounterCountsInboundEdges(t *testing.T, ctx context.Context, fixture GraphCounterFixture) {
	t.Helper()
	anchor := fixture.IssuePrefix + "-in-target"
	dependentA := fixture.IssuePrefix + "-in-dep-a"
	dependentB := fixture.IssuePrefix + "-in-dep-b"
	dependency := fixture.IssuePrefix + "-in-blocker"
	seedGraphCounterIssues(t, ctx, fixture, anchor, dependentA, dependentB, dependency)
	seedGraphCounterEdge(t, ctx, fixture, dependentA, anchor, types.DepBlocks)
	seedGraphCounterEdge(t, ctx, fixture, dependentB, anchor, types.DepBlocks)
	seedGraphCounterEdge(t, ctx, fixture, anchor, dependency, types.DepBlocks)

	inbound := countEdges(t, ctx, fixture, publicops.EdgeCountRequest{
		IDs: []string{anchor}, Direction: publicops.EdgeDirectionIn,
	})
	assertGraphCounterCount(t, inbound, anchor, 2)

	outbound := countEdges(t, ctx, fixture, publicops.EdgeCountRequest{
		IDs: []string{anchor}, Direction: publicops.EdgeDirectionOut,
	})
	assertGraphCounterCount(t, outbound, anchor, 1)
}

// RunGraphCounterAnswersOnePerAnchorInRequestOrder pins
// EdgeCountResult.Anchors: one entry per requested id, in the order the request
// named them. The ids are seeded in the REVERSE of that order, so a body that
// answered in the storage seam's natural order (ascending by id) fails rather
// than passing by coincidence.
func RunGraphCounterAnswersOnePerAnchorInRequestOrder(t *testing.T, ctx context.Context, fixture GraphCounterFixture) {
	t.Helper()
	first := fixture.IssuePrefix + "-order-c"
	second := fixture.IssuePrefix + "-order-b"
	third := fixture.IssuePrefix + "-order-a"
	seedGraphCounterIssues(t, ctx, fixture, first, second, third)

	result := countEdges(t, ctx, fixture, publicops.EdgeCountRequest{
		IDs: []string{first, second, third}, Direction: publicops.EdgeDirectionOut,
	})
	assertGraphCounterAnchorIDs(t, result, first, second, third)
}

// RunGraphCounterDistinguishesNoEdgesFromNoAnchor pins AnchorEdgeCount.Missing.
// An issue with no dependencies is the COMMON case and answers 0, so a body
// that reported presence from "did any edges come back" would pass every other
// case in this file and fail only here.
//
// The ghost is named BETWEEN two real anchors deliberately: a case whose ghost
// came last could not tell a correct body from one that short-circuited on the
// first miss.
func RunGraphCounterDistinguishesNoEdgesFromNoAnchor(t *testing.T, ctx context.Context, fixture GraphCounterFixture) {
	t.Helper()
	bare := fixture.IssuePrefix + "-bare-present"
	ghost := fixture.IssuePrefix + "-bare-absent"
	real2 := fixture.IssuePrefix + "-bare-real2"
	seedGraphCounterIssues(t, ctx, fixture, bare, real2)

	result := countEdges(t, ctx, fixture, publicops.EdgeCountRequest{
		IDs: []string{bare, ghost, real2}, Direction: publicops.EdgeDirectionOut,
	})
	assertGraphCounterAnchorIDs(t, result, bare, ghost, real2)
	assertGraphCounterMissing(t, result, bare, false)
	assertGraphCounterCount(t, result, bare, 0)
	assertGraphCounterMissing(t, result, ghost, true)
	assertGraphCounterCount(t, result, ghost, 0)
	assertGraphCounterMissing(t, result, real2, false)
}

// RunGraphCounterCollapsesRepeatedAnchors pins EdgeCountRequest.IDs's repeats
// clause: an id named twice is one anchor, at the position of its first
// mention. The repeat is placed AFTER a different anchor, so a body that
// de-duplicated by sorting rather than by first mention answers b, a instead of
// a, b and fails.
func RunGraphCounterCollapsesRepeatedAnchors(t *testing.T, ctx context.Context, fixture GraphCounterFixture) {
	t.Helper()
	first := fixture.IssuePrefix + "-dup-b"
	second := fixture.IssuePrefix + "-dup-a"
	target := fixture.IssuePrefix + "-dup-target"
	seedGraphCounterIssues(t, ctx, fixture, first, second, target)
	seedGraphCounterEdge(t, ctx, fixture, first, target, types.DepBlocks)

	result := countEdges(t, ctx, fixture, publicops.EdgeCountRequest{
		IDs:       []string{first, second, first, second, first},
		Direction: publicops.EdgeDirectionOut,
	})
	assertGraphCounterAnchorIDs(t, result, first, second)
	// One anchor, one tally: a body that accumulated per mention would answer 3.
	assertGraphCounterCount(t, result, first, 1)
}

// RunGraphCounterFiltersEdgesNotAnchors pins EdgeCountRequest.Types: the filter
// narrows edges, and an anchor whose every edge it rejects stays present with a
// count of 0. A body that dropped it would turn a filtered count into a report
// that the issue does not exist.
//
// The second half pins the OPEN vocabulary from the same field: a type no edge
// carries is accepted and matches nothing, rather than being refused.
func RunGraphCounterFiltersEdgesNotAnchors(t *testing.T, ctx context.Context, fixture GraphCounterFixture) {
	t.Helper()
	kept := fixture.IssuePrefix + "-filter-kept"
	dropped := fixture.IssuePrefix + "-filter-dropped"
	blocker := fixture.IssuePrefix + "-filter-blocker"
	related := fixture.IssuePrefix + "-filter-related"
	seedGraphCounterIssues(t, ctx, fixture, kept, dropped, blocker, related)
	seedGraphCounterEdge(t, ctx, fixture, kept, blocker, types.DepBlocks)
	seedGraphCounterEdge(t, ctx, fixture, kept, related, types.DepRelated)
	seedGraphCounterEdge(t, ctx, fixture, dropped, related, types.DepRelated)

	filtered := countEdges(t, ctx, fixture, publicops.EdgeCountRequest{
		IDs:       []string{kept, dropped},
		Direction: publicops.EdgeDirectionOut,
		Types:     []types.DependencyType{types.DepBlocks},
	})
	assertGraphCounterAnchorIDs(t, filtered, kept, dropped)
	assertGraphCounterCount(t, filtered, kept, 1)
	assertGraphCounterMissing(t, filtered, dropped, false)
	assertGraphCounterCount(t, filtered, dropped, 0)

	// Unfiltered, the same anchor carries both edges — so the number above is
	// the filter's work and not the seed's.
	all := countEdges(t, ctx, fixture, publicops.EdgeCountRequest{
		IDs: []string{kept}, Direction: publicops.EdgeDirectionOut,
	})
	assertGraphCounterCount(t, all, kept, 2)

	invented := countEdges(t, ctx, fixture, publicops.EdgeCountRequest{
		IDs:       []string{kept},
		Direction: publicops.EdgeDirectionOut,
		Types:     []types.DependencyType{"workspace-invented-type"},
	})
	assertGraphCounterMissing(t, invented, kept, false)
	assertGraphCounterCount(t, invented, kept, 0)
}

// RunGraphCounterNarrowsInboundByDependentStatus pins EdgeCountRequest.Status:
// the filter is on the DEPENDENT's stored status, the row at the source end of
// the inbound edge.
//
// Both dependents are seeded FIRST and the closed one is seeded closed, because
// the fixture kit's create is the only write these cases have. A body that
// joined the ANCHOR's status instead would answer 2 for open and 0 for closed,
// which is why the anchor is left open and both numbers are asserted.
func RunGraphCounterNarrowsInboundByDependentStatus(t *testing.T, ctx context.Context, fixture GraphCounterFixture) {
	t.Helper()
	anchor := fixture.IssuePrefix + "-status-target"
	openDep := fixture.IssuePrefix + "-status-open"
	closedDep := fixture.IssuePrefix + "-status-closed"
	seedGraphCounterIssues(t, ctx, fixture, anchor, openDep)
	seedGraphCounterIssueWithStatus(t, ctx, fixture, closedDep, types.StatusClosed)
	seedGraphCounterEdge(t, ctx, fixture, openDep, anchor, types.DepBlocks)
	seedGraphCounterEdge(t, ctx, fixture, closedDep, anchor, types.DepBlocks)

	unnarrowed := countEdges(t, ctx, fixture, publicops.EdgeCountRequest{
		IDs: []string{anchor}, Direction: publicops.EdgeDirectionIn,
	})
	assertGraphCounterCount(t, unnarrowed, anchor, 2)

	for _, test := range []struct {
		status string
		want   int64
	}{
		{string(types.StatusOpen), 1},
		{string(types.StatusClosed), 1},
		// Not validated against the workspace vocabulary: an unrecognized name
		// counts 0 rather than failing.
		{"never-a-status-here", 0},
	} {
		narrowed := countEdges(t, ctx, fixture, publicops.EdgeCountRequest{
			IDs: []string{anchor}, Direction: publicops.EdgeDirectionIn, Status: test.status,
		})
		assertGraphCounterMissing(t, narrowed, anchor, false)
		if got := graphCounterAnchor(t, narrowed, anchor).Count; got != test.want {
			t.Errorf("inbound count of %s narrowed to status %q = %d, want %d", anchor, test.status, got, test.want)
		}
	}
}

// RunGraphCounterCountsAcrossBothPlanes pins AnchorEdgeCount.Count's
// plane-spanning clause. The seed routes by SOURCE, so a wisp that depends on a
// durable issue puts its edge in wisp_dependencies and nothing else can — and a
// body that read only `dependencies` answers 1 instead of 2 for the durable
// anchor, and 0 instead of 1 for the wisp.
func RunGraphCounterCountsAcrossBothPlanes(t *testing.T, ctx context.Context, fixture GraphCounterFixture) {
	t.Helper()
	anchor := fixture.IssuePrefix + "-plane-target"
	durableDep := fixture.IssuePrefix + "-plane-durable"
	wispDep := fixture.IssuePrefix + "-plane-wisp"
	seedGraphCounterIssues(t, ctx, fixture, anchor, durableDep)
	seedGraphCounterWisp(t, ctx, fixture, wispDep)
	seedGraphCounterEdge(t, ctx, fixture, durableDep, anchor, types.DepBlocks)
	seedGraphCounterEdge(t, ctx, fixture, wispDep, anchor, types.DepBlocks)

	inbound := countEdges(t, ctx, fixture, publicops.EdgeCountRequest{
		IDs: []string{anchor}, Direction: publicops.EdgeDirectionIn,
	})
	assertGraphCounterCount(t, inbound, anchor, 2)

	// The wisp is an anchor in its own right, on the ephemeral plane, and its
	// outbound edge is the one that lives in wisp_dependencies.
	outbound := countEdges(t, ctx, fixture, publicops.EdgeCountRequest{
		IDs: []string{wispDep}, Direction: publicops.EdgeDirectionOut,
	})
	assertGraphCounterMissing(t, outbound, wispDep, false)
	assertGraphCounterCount(t, outbound, wispDep, 1)
}

// RunGraphCounterNarrowsAWispDependentByStatus pins the ephemeral half of the
// status join, which is a SEPARATE SQL branch from the durable one: the
// dependent's status is read from `wisps` for an edge in wisp_dependencies and
// from `issues` for an edge in dependencies.
//
// A body that joined `issues` for both planes answers 0 here — the wisp has no
// row there — and still passes RunGraphCounterNarrowsInboundByDependentStatus,
// which is why this case exists beside it rather than inside it.
func RunGraphCounterNarrowsAWispDependentByStatus(t *testing.T, ctx context.Context, fixture GraphCounterFixture) {
	t.Helper()
	anchor := fixture.IssuePrefix + "-wispstatus-target"
	wispDep := fixture.IssuePrefix + "-wispstatus-wisp"
	seedGraphCounterIssues(t, ctx, fixture, anchor)
	seedGraphCounterWisp(t, ctx, fixture, wispDep)
	seedGraphCounterEdge(t, ctx, fixture, wispDep, anchor, types.DepBlocks)

	matching := countEdges(t, ctx, fixture, publicops.EdgeCountRequest{
		IDs: []string{anchor}, Direction: publicops.EdgeDirectionIn, Status: string(types.StatusOpen),
	})
	assertGraphCounterCount(t, matching, anchor, 1)

	missing := countEdges(t, ctx, fixture, publicops.EdgeCountRequest{
		IDs: []string{anchor}, Direction: publicops.EdgeDirectionIn, Status: string(types.StatusClosed),
	})
	assertGraphCounterCount(t, missing, anchor, 0)
}

// RunGraphCounterResolvesIDsExactly pins EdgeCountRequest.IDs's exactness
// clause: a prefix of a real id, a case variant and an id carrying whitespace
// are all misses, not resolutions. They are misses rather than errors: this
// role reports them beside the anchor that did resolve.
func RunGraphCounterResolvesIDsExactly(t *testing.T, ctx context.Context, fixture GraphCounterFixture) {
	t.Helper()
	anchor := fixture.IssuePrefix + "-exact-anchor"
	target := fixture.IssuePrefix + "-exact-target"
	seedGraphCounterIssues(t, ctx, fixture, anchor, target)
	seedGraphCounterEdge(t, ctx, fixture, anchor, target, types.DepBlocks)

	prefix := anchor[:len(anchor)-2]
	spaced := " " + anchor + " "
	result := countEdges(t, ctx, fixture, publicops.EdgeCountRequest{
		IDs: []string{anchor, prefix, spaced}, Direction: publicops.EdgeDirectionOut,
	})
	assertGraphCounterAnchorIDs(t, result, anchor, prefix, spaced)
	assertGraphCounterMissing(t, result, anchor, false)
	assertGraphCounterCount(t, result, anchor, 1)
	assertGraphCounterMissing(t, result, prefix, true)
	assertGraphCounterCount(t, result, prefix, 0)
	assertGraphCounterMissing(t, result, spaced, true)
	assertGraphCounterCount(t, result, spaced, 0)
}

// RunGraphCounterAnswersAnEmptyRequest pins EdgeCountRequest.IDs's empty-slice
// clause: no anchors is not an error, it is an answer with no anchors — and the
// slice is never nil, so a front door that marshals it emits [] rather than
// null.
func RunGraphCounterAnswersAnEmptyRequest(t *testing.T, ctx context.Context, fixture GraphCounterFixture) {
	t.Helper()
	result, err := fixture.GraphCounter.CountEdges(ctx, publicops.EdgeCountRequest{
		Direction: publicops.EdgeDirectionIn,
	})
	if err != nil {
		t.Fatalf("CountEdges with no ids = %v, want an empty answer", err)
	}
	if len(result.Anchors) != 0 {
		t.Fatalf("CountEdges with no ids returned %d anchors, want none", len(result.Anchors))
	}
	if result.Anchors == nil {
		t.Error("Anchors is nil; the contract promises it is never nil for a successful call")
	}
}

// RunGraphCounterRefusesAnUnusableRequest pins the request vocabulary at
// ValidateEdgeCountRequest, one refusal per clause of the leaf doc.
//
// The direction cases are asserted with NO IDS, which is the sharp half of the
// ordering promise: an empty request is a refusal about the direction rather
// than the empty answer the case above returns for a well-formed one.
func RunGraphCounterRefusesAnUnusableRequest(t *testing.T, ctx context.Context, fixture GraphCounterFixture) {
	t.Helper()
	anchor := fixture.IssuePrefix + "-refuse-anchor"
	seedGraphCounterIssues(t, ctx, fixture, anchor)

	for _, test := range []struct {
		name string
		req  publicops.EdgeCountRequest
	}{
		{"no direction at all", publicops.EdgeCountRequest{}},
		{"no direction beside real ids", publicops.EdgeCountRequest{IDs: []string{anchor}}},
		{"a direction outside the set", publicops.EdgeCountRequest{
			IDs: []string{anchor}, Direction: publicops.EdgeDirection("sideways")}},
		{"a status on the outbound direction", publicops.EdgeCountRequest{
			IDs: []string{anchor}, Direction: publicops.EdgeDirectionOut, Status: string(types.StatusOpen)}},
		{"an empty id beside a real one", publicops.EdgeCountRequest{
			IDs: []string{anchor, ""}, Direction: publicops.EdgeDirectionOut}},
		{"an unusable dependency type", publicops.EdgeCountRequest{
			IDs: []string{anchor}, Direction: publicops.EdgeDirectionOut,
			Types: []types.DependencyType{""}}},
	} {
		_, err := fixture.GraphCounter.CountEdges(ctx, test.req)
		if !errors.Is(err, publicops.ErrValidation) {
			t.Errorf("CountEdges with %s = %v, want ErrValidation", test.name, err)
		}
	}
}

// RunGraphCounterLeavesTheRequestAlone pins the no-mutation clause on
// GraphCounter. IDs and Types are the two members a body could write through to
// the caller, and de-duplication and filtering are exactly the steps that would.
func RunGraphCounterLeavesTheRequestAlone(t *testing.T, ctx context.Context, fixture GraphCounterFixture) {
	t.Helper()
	anchor := fixture.IssuePrefix + "-immutable-anchor"
	target := fixture.IssuePrefix + "-immutable-target"
	seedGraphCounterIssues(t, ctx, fixture, anchor, target)
	seedGraphCounterEdge(t, ctx, fixture, anchor, target, types.DepBlocks)

	ids := []string{anchor, anchor, target}
	depTypes := []types.DependencyType{types.DepRelated, types.DepBlocks}
	countEdges(t, ctx, fixture, publicops.EdgeCountRequest{
		IDs: ids, Direction: publicops.EdgeDirectionOut, Types: depTypes,
	})

	if len(ids) != 3 || ids[0] != anchor || ids[1] != anchor || ids[2] != target {
		t.Errorf("the request's IDs slice is now %v; the contract says a body de-duplicates into its own copy", ids)
	}
	if len(depTypes) != 2 || depTypes[0] != types.DepRelated || depTypes[1] != types.DepBlocks {
		t.Errorf("the request's Types slice is now %v; the contract says a body reads it without reordering it", depTypes)
	}
}

// RunGraphCounterWritesNothing pins GraphCounter's read clause: counting
// records no history entry. The delta is taken around the calls rather than as
// an absolute count, because the seeds above it are versioned writes of their
// own.
func RunGraphCounterWritesNothing(t *testing.T, ctx context.Context, fixture GraphCounterFixture) {
	t.Helper()
	if fixture.CountHistory == nil {
		t.Skip("this backend cannot observe history, so the writes-nothing clause cannot be checked here")
	}
	anchor := fixture.IssuePrefix + "-quiet-anchor"
	target := fixture.IssuePrefix + "-quiet-target"
	ghost := fixture.IssuePrefix + "-quiet-ghost"
	seedGraphCounterIssues(t, ctx, fixture, anchor, target)
	seedGraphCounterEdge(t, ctx, fixture, anchor, target, types.DepBlocks)

	before, err := fixture.CountHistory(ctx)
	if err != nil {
		t.Fatalf("CountHistory before: %v", err)
	}
	countEdges(t, ctx, fixture, publicops.EdgeCountRequest{
		IDs: []string{anchor, ghost}, Direction: publicops.EdgeDirectionOut,
	})
	// A refusal changes nothing either, so the same delta covers both.
	_, _ = fixture.GraphCounter.CountEdges(ctx, publicops.EdgeCountRequest{IDs: []string{""}})
	after, err := fixture.CountHistory(ctx)
	if err != nil {
		t.Fatalf("CountHistory after: %v", err)
	}
	if after != before {
		t.Fatalf("history entries went %d -> %d across two edge counts, want no change", before, after)
	}
}

func countEdges(t *testing.T, ctx context.Context, fixture GraphCounterFixture, request publicops.EdgeCountRequest) publicops.EdgeCountResult {
	t.Helper()
	result, err := fixture.GraphCounter.CountEdges(ctx, request)
	if err != nil {
		t.Fatalf("CountEdges(%v, %s): %v", request.IDs, request.Direction, err)
	}
	return result
}

func seedGraphCounterIssues(t *testing.T, ctx context.Context, fixture GraphCounterFixture, ids ...string) {
	t.Helper()
	for _, id := range ids {
		seedGraphCounterIssueWithStatus(t, ctx, fixture, id, types.StatusOpen)
	}
}

func seedGraphCounterIssueWithStatus(t *testing.T, ctx context.Context, fixture GraphCounterFixture, id string, status types.Status) {
	t.Helper()
	issue := graphCounterSeed(id, false)
	issue.Status = status
	if err := fixture.CreateIssue(ctx, issue, "graph-counter-seed"); err != nil {
		t.Fatalf("seed issue %s: %v", id, err)
	}
}

func seedGraphCounterWisp(t *testing.T, ctx context.Context, fixture GraphCounterFixture, id string) {
	t.Helper()
	if err := fixture.CreateWisp(ctx, graphCounterSeed(id, true), "graph-counter-seed"); err != nil {
		t.Fatalf("seed wisp %s: %v", id, err)
	}
}

func seedGraphCounterEdge(t *testing.T, ctx context.Context, fixture GraphCounterFixture, from, to string, depType types.DependencyType) {
	t.Helper()
	if err := fixture.AddDependency(ctx, &types.Dependency{
		IssueID: from, DependsOnID: to, Type: depType,
	}, "graph-counter-seed"); err != nil {
		t.Fatalf("seed edge %s -> %s (%s): %v", from, to, depType, err)
	}
}

func graphCounterSeed(id string, ephemeral bool) *types.Issue {
	return &types.Issue{
		ID:        id,
		Title:     id,
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: ephemeral,
	}
}

func graphCounterAnchor(t *testing.T, result publicops.EdgeCountResult, id string) publicops.AnchorEdgeCount {
	t.Helper()
	for _, anchor := range result.Anchors {
		if anchor.ID == id {
			return anchor
		}
	}
	t.Fatalf("no anchor %q in the answer; got %v", id, graphCounterAnchorIDs(result))
	return publicops.AnchorEdgeCount{}
}

func graphCounterAnchorIDs(result publicops.EdgeCountResult) []string {
	out := make([]string, 0, len(result.Anchors))
	for _, anchor := range result.Anchors {
		out = append(out, anchor.ID)
	}
	return out
}

func assertGraphCounterAnchorIDs(t *testing.T, result publicops.EdgeCountResult, want ...string) {
	t.Helper()
	got := graphCounterAnchorIDs(result)
	if len(got) != len(want) {
		t.Fatalf("anchors = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("anchors = %v, want %v", got, want)
		}
	}
}

func assertGraphCounterMissing(t *testing.T, result publicops.EdgeCountResult, id string, want bool) {
	t.Helper()
	if got := graphCounterAnchor(t, result, id).Missing; got != want {
		t.Errorf("anchor %s Missing = %t, want %t", id, got, want)
	}
}

func assertGraphCounterCount(t *testing.T, result publicops.EdgeCountResult, id string, want int64) {
	t.Helper()
	if got := graphCounterAnchor(t, result, id).Count; got != want {
		t.Errorf("anchor %s Count = %d, want %d", id, got, want)
	}
}
