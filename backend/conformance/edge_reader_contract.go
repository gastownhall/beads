package conformance

import (
	"context"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// This file holds the contract every implementation of publicops.EdgeReader
// must satisfy. Each case asserts what issueops/edgereader.go PROMISES, cited
// by line; a backend that disagrees is parked at its own wiring site with
// skipKnownDivergence so the case still runs on the ones that agree.
//
// There are three wirings — the server-backed store, the embedded store and the
// unit-of-work provider — and TWO BODIES, not three. dolt and embeddeddolt both
// call storage/issueops.ExecuteEdgeRead inside their own read transaction, so
// they are one vote plus an engine check; the unit-of-work provider is the
// second, and it is a genuinely different body: it probes existence through two
// batched by-id use-case reads instead of a batched EXISTS over the two tables.
// What all three share is ValidateEdgeReadRequest, EdgeReadAnchors and
// FinishEdgeRead — the request rules, the de-duplication and the ordering — so
// what these cases catch below those three is the EXECUTION half.
//
// EVERY CASE NAMES THE EXACT IDS IT SEEDED. The three fixtures share one
// database per suite and the two store fixtures share it with every other
// role's cases, so an assertion that read "every anchor" would be an assertion
// about the whole workspace and would fail the moment a sibling suite seeded a
// row.
//
// What is deliberately NOT here:
//   - the mapping from `bd dep list`'s arguments to a request, which is the
//     command's job;
//   - the INBOUND direction, which this role does not have (edgereader.go:157-164
//     says why) and which Relations answers hydrated for one anchor;
//   - anything about the dependency TARGET's existence, which this role does
//     not probe (edgereader.go:117-121).

// EdgeReaderFixture supplies adapter-specific storage access for the
// stored-edge assertions. Every field is named and typed exactly like the
// per-backend roleFixtureKit hook it is filled from.
type EdgeReaderFixture struct {
	// IssuePrefix namespaces the ids each assertion seeds, so several of them
	// can share one database.
	IssuePrefix string
	EdgeReader  publicops.EdgeReader
	// CreateIssue seeds a durable issue in the issues plane.
	CreateIssue func(context.Context, *types.Issue, string) error
	// CreateWisp seeds an ephemeral issue in the wisps plane. It is a separate
	// field rather than an Ephemeral flag on CreateIssue because the three
	// adapters reach the two planes through different verbs.
	CreateWisp func(context.Context, *types.Issue, string) error
	// AddDependency seeds ONE edge, routed to the plane the edge's source lives
	// in.
	AddDependency func(context.Context, *types.Dependency, string) error
	// CountHistory reports how many history entries the fixture's branch has.
	// A nil hook means "this backend cannot observe history", and the case that
	// needs it SKIPS rather than passing quietly.
	CountHistory func(context.Context) (int, error)
}

// RunEdgeReaderAnswersOnePerAnchorInRequestOrder pins edgereader.go:127-133:
// one entry per requested id, in the order the request named them. The ids are
// seeded in the REVERSE of that order, so a body that answered in the storage
// seam's natural order (ascending by source id) fails rather than passing by
// coincidence.
func RunEdgeReaderAnswersOnePerAnchorInRequestOrder(t *testing.T, ctx context.Context, fixture EdgeReaderFixture) {
	t.Helper()
	first := fixture.IssuePrefix + "-order-c"
	second := fixture.IssuePrefix + "-order-b"
	third := fixture.IssuePrefix + "-order-a"
	seedEdgeReaderIssues(t, ctx, fixture, first, second, third)

	result := readEdges(t, ctx, fixture, publicops.EdgeReadRequest{IDs: []string{first, second, third}})
	assertEdgeReaderAnchorIDs(t, result, first, second, third)
}

// RunEdgeReaderReportsAMissingAnchorRatherThanFailing pins
// edgereader.go:45-48 and :188-191 together: a miss is reported ON THE ANCHOR
// and the call still succeeds, so the anchors that were found still come back.
//
// The ghost is named BETWEEN two real anchors deliberately: a case whose ghost
// came last could not tell a correct body from one that short-circuited on the
// first miss.
func RunEdgeReaderReportsAMissingAnchorRatherThanFailing(t *testing.T, ctx context.Context, fixture EdgeReaderFixture) {
	t.Helper()
	real1 := fixture.IssuePrefix + "-ghost-real1"
	real2 := fixture.IssuePrefix + "-ghost-real2"
	ghost := fixture.IssuePrefix + "-ghost-absent"
	seedEdgeReaderIssues(t, ctx, fixture, real1, real2)

	result := readEdges(t, ctx, fixture, publicops.EdgeReadRequest{IDs: []string{real1, ghost, real2}})
	assertEdgeReaderAnchorIDs(t, result, real1, ghost, real2)
	assertEdgeReaderMissing(t, result, real1, false)
	assertEdgeReaderMissing(t, result, ghost, true)
	assertEdgeReaderMissing(t, result, real2, false)
}

// RunEdgeReaderDistinguishesNoEdgesFromNoAnchor pins edgereader.go:104-115. An
// issue with no dependencies is the COMMON case, so a body that reported
// presence from "did any edges come back" would pass every other case in this
// file and fail only here.
func RunEdgeReaderDistinguishesNoEdgesFromNoAnchor(t *testing.T, ctx context.Context, fixture EdgeReaderFixture) {
	t.Helper()
	bare := fixture.IssuePrefix + "-bare-present"
	ghost := fixture.IssuePrefix + "-bare-absent"
	seedEdgeReaderIssues(t, ctx, fixture, bare)

	result := readEdges(t, ctx, fixture, publicops.EdgeReadRequest{IDs: []string{bare, ghost}})
	assertEdgeReaderMissing(t, result, bare, false)
	assertEdgeReaderEdgeTargets(t, result, bare)
	assertEdgeReaderMissing(t, result, ghost, true)
	assertEdgeReaderEdgeTargets(t, result, ghost)

	// Never nil, so a front door that marshals the answer emits [] rather than
	// null (edgereader.go:101-102).
	for _, anchor := range result.Anchors {
		if anchor.Edges == nil {
			t.Errorf("anchor %s has nil Edges; the contract promises an empty slice", anchor.ID)
		}
	}
}

// RunEdgeReaderReturnsTargetsVerbatim pins edgereader.go:12-17: an edge whose
// target this database holds no row for is still an edge, returned with its
// target spelled as stored.
//
// This is the sharpest difference from Relations, which drops such an edge
// because it has no far end to hydrate. Two flavors are seeded — an
// "external:" reference and a dangling id in another repository's prefix —
// because they take different typed target columns.
func RunEdgeReaderReturnsTargetsVerbatim(t *testing.T, ctx context.Context, fixture EdgeReaderFixture) {
	t.Helper()
	anchor := fixture.IssuePrefix + "-verbatim-src"
	external := "external:" + fixture.IssuePrefix + "-verbatim-ext"
	foreign := "zzforeign-" + fixture.IssuePrefix + "-verbatim"
	seedEdgeReaderIssues(t, ctx, fixture, anchor)
	seedEdgeReaderEdge(t, ctx, fixture, anchor, external, types.DepRelated)
	seedEdgeReaderEdge(t, ctx, fixture, anchor, foreign, types.DepRelated)

	result := readEdges(t, ctx, fixture, publicops.EdgeReadRequest{IDs: []string{anchor}})
	assertEdgeReaderMissing(t, result, anchor, false)
	// Sorted by target id, and "external:..." sorts before "zzforeign-...".
	assertEdgeReaderEdgeTargets(t, result, anchor, external, foreign)
}

// RunEdgeReaderCollapsesRepeatedAnchors pins edgereader.go:59-62: an id named
// twice is one anchor, at the position of its first mention. The repeat is
// placed AFTER a different anchor, so a body that de-duplicated by sorting
// rather than by first mention answers b, a instead of a, b and fails.
func RunEdgeReaderCollapsesRepeatedAnchors(t *testing.T, ctx context.Context, fixture EdgeReaderFixture) {
	t.Helper()
	first := fixture.IssuePrefix + "-dup-b"
	second := fixture.IssuePrefix + "-dup-a"
	seedEdgeReaderIssues(t, ctx, fixture, first, second)

	result := readEdges(t, ctx, fixture, publicops.EdgeReadRequest{
		IDs: []string{first, second, first, second, first},
	})
	assertEdgeReaderAnchorIDs(t, result, first, second)
}

// RunEdgeReaderOrdersEdgesByTarget pins edgereader.go:87-99.
//
// The three edges are inserted in an order that is neither the target order nor
// its reverse, so an implementation answering in insertion order fails here.
//
// ONLY THE PRIMARY KEY IS EXERCISED: all three backends refuse a second edge
// for a (source, target) pair they already hold, so an answer with two edges to
// break a tie between cannot be seeded through AddDependency.
func RunEdgeReaderOrdersEdgesByTarget(t *testing.T, ctx context.Context, fixture EdgeReaderFixture) {
	t.Helper()
	anchor := fixture.IssuePrefix + "-sort-src"
	late := fixture.IssuePrefix + "-sort-z"
	middle := fixture.IssuePrefix + "-sort-m"
	early := fixture.IssuePrefix + "-sort-a"
	seedEdgeReaderIssues(t, ctx, fixture, anchor, late, middle, early)
	seedEdgeReaderEdge(t, ctx, fixture, anchor, middle, types.DepRelated)
	seedEdgeReaderEdge(t, ctx, fixture, anchor, late, types.DepRelated)
	seedEdgeReaderEdge(t, ctx, fixture, anchor, early, types.DepBlocks)

	result := readEdges(t, ctx, fixture, publicops.EdgeReadRequest{IDs: []string{anchor}})
	assertEdgeReaderEdgeTargets(t, result, anchor, early, middle, late)

	// The surrogate key is empty on every row here, which is why it is NOT a
	// third sort term (edgereader.go:19-23).
	for _, edge := range edgeReaderAnchor(t, result, anchor).Edges {
		if edge.ID != "" {
			t.Errorf("edge %s -> %s carries ID %q; the source-keyed read does not select it and the contract says it is empty",
				edge.IssueID, edge.DependsOnID, edge.ID)
		}
	}
}

// RunEdgeReaderFiltersEdgesNotAnchors pins edgereader.go:73-75: the type filter
// narrows edges, and an anchor whose every edge it rejects stays present with
// none. A body that dropped it would turn a filtered listing into a report that
// the issue does not exist.
func RunEdgeReaderFiltersEdgesNotAnchors(t *testing.T, ctx context.Context, fixture EdgeReaderFixture) {
	t.Helper()
	kept := fixture.IssuePrefix + "-filter-kept"
	dropped := fixture.IssuePrefix + "-filter-dropped"
	blocker := fixture.IssuePrefix + "-filter-blocker"
	related := fixture.IssuePrefix + "-filter-related"
	seedEdgeReaderIssues(t, ctx, fixture, kept, dropped, blocker, related)
	seedEdgeReaderEdge(t, ctx, fixture, kept, blocker, types.DepBlocks)
	seedEdgeReaderEdge(t, ctx, fixture, kept, related, types.DepRelated)
	seedEdgeReaderEdge(t, ctx, fixture, dropped, related, types.DepRelated)

	result := readEdges(t, ctx, fixture, publicops.EdgeReadRequest{
		IDs:   []string{kept, dropped},
		Types: []types.DependencyType{types.DepBlocks},
	})
	assertEdgeReaderAnchorIDs(t, result, kept, dropped)
	assertEdgeReaderEdgeTargets(t, result, kept, blocker)
	assertEdgeReaderMissing(t, result, dropped, false)
	assertEdgeReaderEdgeTargets(t, result, dropped)
}

// RunEdgeReaderReadsBothPlanes pins the anchor probe against edgereader.go:104:
// "no issue AND no wisp carries this id". A wisp anchor is present, and its
// edges come back from the ephemeral tier the seed routed them to. The three
// bodies reach the two planes by different routes: a batched EXISTS over
// `issues` then `wisps` on the store side, and GetIssuesByIDs plus
// GetWispsByIDs on the unit-of-work side.
func RunEdgeReaderReadsBothPlanes(t *testing.T, ctx context.Context, fixture EdgeReaderFixture) {
	t.Helper()
	wisp := fixture.IssuePrefix + "-plane-wisp"
	target := fixture.IssuePrefix + "-plane-target"
	seedEdgeReaderIssues(t, ctx, fixture, target)
	seedEdgeReaderWisp(t, ctx, fixture, wisp)
	seedEdgeReaderEdge(t, ctx, fixture, wisp, target, types.DepBlocks)

	result := readEdges(t, ctx, fixture, publicops.EdgeReadRequest{IDs: []string{wisp}})
	assertEdgeReaderMissing(t, result, wisp, false)
	assertEdgeReaderEdgeTargets(t, result, wisp, target)
}

// RunEdgeReaderResolvesExactIDsOnly pins edgereader.go:36-43: a prefix of a
// real id, a case variant and an id carrying whitespace are all misses, not
// resolutions. They are misses rather than errors: this role reports them
// beside the anchor that did resolve.
func RunEdgeReaderResolvesExactIDsOnly(t *testing.T, ctx context.Context, fixture EdgeReaderFixture) {
	t.Helper()
	anchor := fixture.IssuePrefix + "-exact-anchor"
	target := fixture.IssuePrefix + "-exact-target"
	seedEdgeReaderIssues(t, ctx, fixture, anchor, target)
	seedEdgeReaderEdge(t, ctx, fixture, anchor, target, types.DepBlocks)

	prefix := anchor[:len(anchor)-2]
	spaced := " " + anchor + " "
	result := readEdges(t, ctx, fixture, publicops.EdgeReadRequest{
		IDs: []string{anchor, prefix, spaced},
	})
	assertEdgeReaderAnchorIDs(t, result, anchor, prefix, spaced)
	assertEdgeReaderMissing(t, result, anchor, false)
	assertEdgeReaderMissing(t, result, prefix, true)
	assertEdgeReaderMissing(t, result, spaced, true)
}

// RunEdgeReaderAnswersAnEmptyRequest pins edgereader.go:54-57: no anchors is
// not an error, it is an answer with no anchors.
func RunEdgeReaderAnswersAnEmptyRequest(t *testing.T, ctx context.Context, fixture EdgeReaderFixture) {
	t.Helper()
	result, err := fixture.EdgeReader.ReadEdges(ctx, publicops.EdgeReadRequest{})
	if err != nil {
		t.Fatalf("ReadEdges with no ids = %v, want an empty answer", err)
	}
	if len(result.Anchors) != 0 {
		t.Fatalf("ReadEdges with no ids returned %d anchors, want none", len(result.Anchors))
	}
	if result.Anchors == nil {
		t.Error("Anchors is nil; the contract promises it is never nil for a successful call")
	}
}

// RunEdgeReaderRefusesAnEmptyID pins edgereader.go:50-52: the empty string is
// ErrValidation rather than a nameless ghost. The refusal must beat the good
// anchor beside it — a body that answered for the ids it could would leave a
// caller with an anchor it has no name for.
func RunEdgeReaderRefusesAnEmptyID(t *testing.T, ctx context.Context, fixture EdgeReaderFixture) {
	t.Helper()
	anchor := fixture.IssuePrefix + "-emptyid-anchor"
	seedEdgeReaderIssues(t, ctx, fixture, anchor)

	_, err := fixture.EdgeReader.ReadEdges(ctx, publicops.EdgeReadRequest{IDs: []string{anchor, ""}})
	if !errors.Is(err, publicops.ErrValidation) {
		t.Fatalf("ReadEdges with an empty id = %v, want ErrValidation", err)
	}
}

// RunEdgeReaderRefusesAnUnusableType pins edgereader.go:67-71: an entry no edge
// could ever carry is ErrValidation rather than a filter that matches nothing.
// The vocabulary itself stays OPEN, which the second half asserts: a
// workspace's own type is accepted and simply matches no edge here.
func RunEdgeReaderRefusesAnUnusableType(t *testing.T, ctx context.Context, fixture EdgeReaderFixture) {
	t.Helper()
	anchor := fixture.IssuePrefix + "-badtype-anchor"
	target := fixture.IssuePrefix + "-badtype-target"
	seedEdgeReaderIssues(t, ctx, fixture, anchor, target)
	seedEdgeReaderEdge(t, ctx, fixture, anchor, target, types.DepBlocks)

	_, err := fixture.EdgeReader.ReadEdges(ctx, publicops.EdgeReadRequest{
		IDs:   []string{anchor},
		Types: []types.DependencyType{""},
	})
	if !errors.Is(err, publicops.ErrValidation) {
		t.Fatalf("ReadEdges with an empty dependency type = %v, want ErrValidation", err)
	}

	result := readEdges(t, ctx, fixture, publicops.EdgeReadRequest{
		IDs:   []string{anchor},
		Types: []types.DependencyType{"workspace-invented-type"},
	})
	assertEdgeReaderMissing(t, result, anchor, false)
	assertEdgeReaderEdgeTargets(t, result, anchor)
}

// RunEdgeReaderLeavesTheRequestAlone pins the no-mutation clause at
// edgereader.go:169-173. IDs and Types are the two members a body could write
// through to the caller, and de-duplication and filtering are exactly the steps
// that would.
func RunEdgeReaderLeavesTheRequestAlone(t *testing.T, ctx context.Context, fixture EdgeReaderFixture) {
	t.Helper()
	anchor := fixture.IssuePrefix + "-immutable-anchor"
	target := fixture.IssuePrefix + "-immutable-target"
	seedEdgeReaderIssues(t, ctx, fixture, anchor, target)
	seedEdgeReaderEdge(t, ctx, fixture, anchor, target, types.DepBlocks)

	ids := []string{anchor, anchor, target}
	depTypes := []types.DependencyType{types.DepRelated, types.DepBlocks}
	readEdges(t, ctx, fixture, publicops.EdgeReadRequest{IDs: ids, Types: depTypes})

	if len(ids) != 3 || ids[0] != anchor || ids[1] != anchor || ids[2] != target {
		t.Errorf("the request's IDs slice is now %v; the contract says a body de-duplicates into its own copy", ids)
	}
	if len(depTypes) != 2 || depTypes[0] != types.DepRelated || depTypes[1] != types.DepBlocks {
		t.Errorf("the request's Types slice is now %v; the contract says a body reads it without reordering it", depTypes)
	}
}

// RunEdgeReaderWritesNothing pins edgereader.go:175-178: reading edges records
// no history entry. The delta is taken around the call rather than as an
// absolute count, because the seeds above it are versioned writes of their own.
func RunEdgeReaderWritesNothing(t *testing.T, ctx context.Context, fixture EdgeReaderFixture) {
	t.Helper()
	if fixture.CountHistory == nil {
		t.Skip("this backend cannot observe history, so the writes-nothing clause cannot be checked here")
	}
	anchor := fixture.IssuePrefix + "-quiet-anchor"
	target := fixture.IssuePrefix + "-quiet-target"
	ghost := fixture.IssuePrefix + "-quiet-ghost"
	seedEdgeReaderIssues(t, ctx, fixture, anchor, target)
	seedEdgeReaderEdge(t, ctx, fixture, anchor, target, types.DepBlocks)

	before, err := fixture.CountHistory(ctx)
	if err != nil {
		t.Fatalf("CountHistory before: %v", err)
	}
	readEdges(t, ctx, fixture, publicops.EdgeReadRequest{IDs: []string{anchor, ghost}})
	// A refusal changes nothing either, so the same delta covers both.
	_, _ = fixture.EdgeReader.ReadEdges(ctx, publicops.EdgeReadRequest{IDs: []string{""}})
	after, err := fixture.CountHistory(ctx)
	if err != nil {
		t.Fatalf("CountHistory after: %v", err)
	}
	if after != before {
		t.Fatalf("history entries went %d -> %d across two edge reads, want no change", before, after)
	}
}

func readEdges(t *testing.T, ctx context.Context, fixture EdgeReaderFixture, request publicops.EdgeReadRequest) publicops.EdgeReadResult {
	t.Helper()
	result, err := fixture.EdgeReader.ReadEdges(ctx, request)
	if err != nil {
		t.Fatalf("ReadEdges(%v): %v", request.IDs, err)
	}
	return result
}

func seedEdgeReaderIssues(t *testing.T, ctx context.Context, fixture EdgeReaderFixture, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if err := fixture.CreateIssue(ctx, edgeReaderSeed(id, false), "edge-reader-seed"); err != nil {
			t.Fatalf("seed issue %s: %v", id, err)
		}
	}
}

func seedEdgeReaderWisp(t *testing.T, ctx context.Context, fixture EdgeReaderFixture, id string) {
	t.Helper()
	if err := fixture.CreateWisp(ctx, edgeReaderSeed(id, true), "edge-reader-seed"); err != nil {
		t.Fatalf("seed wisp %s: %v", id, err)
	}
}

func seedEdgeReaderEdge(t *testing.T, ctx context.Context, fixture EdgeReaderFixture, from, to string, depType types.DependencyType) {
	t.Helper()
	if err := fixture.AddDependency(ctx, &types.Dependency{
		IssueID: from, DependsOnID: to, Type: depType,
	}, "edge-reader-seed"); err != nil {
		t.Fatalf("seed edge %s -> %s (%s): %v", from, to, depType, err)
	}
}

func edgeReaderSeed(id string, ephemeral bool) *types.Issue {
	return &types.Issue{
		ID:        id,
		Title:     id,
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: ephemeral,
	}
}

func edgeReaderAnchor(t *testing.T, result publicops.EdgeReadResult, id string) publicops.AnchorEdges {
	t.Helper()
	for _, anchor := range result.Anchors {
		if anchor.ID == id {
			return anchor
		}
	}
	t.Fatalf("no anchor %q in the answer; got %v", id, edgeReaderAnchorIDs(result))
	return publicops.AnchorEdges{}
}

func edgeReaderAnchorIDs(result publicops.EdgeReadResult) []string {
	out := make([]string, 0, len(result.Anchors))
	for _, anchor := range result.Anchors {
		out = append(out, anchor.ID)
	}
	return out
}

func assertEdgeReaderAnchorIDs(t *testing.T, result publicops.EdgeReadResult, want ...string) {
	t.Helper()
	got := edgeReaderAnchorIDs(result)
	if len(got) != len(want) {
		t.Fatalf("anchors = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("anchors = %v, want %v", got, want)
		}
	}
}

func assertEdgeReaderMissing(t *testing.T, result publicops.EdgeReadResult, id string, want bool) {
	t.Helper()
	if got := edgeReaderAnchor(t, result, id).Missing; got != want {
		t.Errorf("anchor %s Missing = %t, want %t", id, got, want)
	}
}

// assertEdgeReaderEdgeTargets compares the anchor's edge targets IN ORDER, so
// one helper serves both the "which edges" and the "in what order" assertions.
func assertEdgeReaderEdgeTargets(t *testing.T, result publicops.EdgeReadResult, id string, want ...string) {
	t.Helper()
	edges := edgeReaderAnchor(t, result, id).Edges
	got := make([]string, 0, len(edges))
	for _, edge := range edges {
		if edge.IssueID != id {
			t.Errorf("anchor %s carries an edge whose source is %s; the answer is keyed by source", id, edge.IssueID)
		}
		got = append(got, edge.DependsOnID)
	}
	if len(got) != len(want) {
		t.Fatalf("anchor %s edge targets = %v, want %v", id, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("anchor %s edge targets = %v, want %v", id, got, want)
		}
	}
}
