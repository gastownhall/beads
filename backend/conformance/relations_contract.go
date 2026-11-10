package conformance

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// This file holds the neighbor-query contract every implementation of
// publicops.Relations must satisfy. There are three accessors — the direct
// store, the embedded store and the unit-of-work provider — but only TWO
// bodies: dolt and embeddeddolt both call
// storageissueops.ValidateRelatedRequest + ExecuteRelated and differ only in
// the transaction wrapper and the engine underneath, while uow reaches the
// same two queries through its dependency use case. Wiring the contract three
// times is still worth it — the third run catches wrapper and engine
// divergence, which is what the embedded suite has historically caught — but
// it is not three independent votes on the shared body.
//
// WHAT IT PINS is what the leaf doc promises and nothing else: the order, the
// direction split, ErrNotFound-versus-an-empty-page, the wisp plane, the open
// type vocabulary, and the request snapshot. Each case is written to
// issueops/relations.go's stated
// promise rather than to any implementation's current behavior; where an
// implementation disagrees, the case still asserts the doc and the losing
// backend's WIRING file parks it (see bd-yby99).
//
// ONE THING DELIBERATELY ABSENT, recorded here so it reads as a decision
// rather than as an omission:
//
// The EDGE-TYPE TIEBREAK half of the order (relations.go:91-92, "with the edge
// type breaking a tie") is not pinned here because the write path makes the
// state unreachable: two rows for the same (source, target) pair are refused
// per dependency table with a DependencyTypeConflictError
// (internal/storage/issueops/dependencies.go:258-264, confirmed by running it
// on the dolt fixture), and the two tables are source-routed, so one anchor's
// answer can never carry the same neighbor id twice. The tiebreak is defensive
// code in the shared FinishRelatedPage and is pinned where it is reachable, as
// a unit test of that function
// (internal/storage/issueops/edge_role_requests_test.go:293-322). What this
// contract pins instead is the half a database CAN produce: ascending neighbor
// id across BOTH dependency planes, which is the artifact the doc names.
//
// TWO CLAUSES HERE WERE SILENCES IN THE LEAF (bd-yby99.14 and bd-yby99.15)
// until the owner ruled on them. Both were measured on all three fixtures
// before a sentence was written, both were adopted into the leaf, and both are
// now pinned — by RunRelationsLeavesAnExternalTargetOutOfTheAnswer and
// RunRelationsResolvesTheAnchorIDExactly respectively.

// RelationsFixture supplies adapter-specific storage access for the
// neighbor-query assertions.
type RelationsFixture struct {
	// IssuePrefix namespaces the ids each assertion seeds, so several of them
	// can share one database.
	IssuePrefix string
	Relations   publicops.Relations
	// CreateIssue seeds a durable issue in the issues plane.
	CreateIssue func(context.Context, *types.Issue, string) error
	// CreateWisp seeds an ephemeral issue in the wisps plane. It is a separate
	// field rather than an Ephemeral flag on CreateIssue because the three
	// adapters reach the two planes through different verbs.
	CreateWisp func(context.Context, *types.Issue, string) error
	// AddDependency seeds ONE edge through the backend's own dependency verb,
	// so a case sets its graph up without going through the role it is
	// checking. The edge is routed to the plane its SOURCE lives in.
	AddDependency func(context.Context, *types.Dependency, string) error
	// QueryScalar runs a single-row query and scans it. Relations writes
	// nothing, so this is not here to check a state transition: it is here so
	// the cross-plane ordering case can prove its seed really did straddle the
	// two dependency tables, instead of quietly degenerating into a
	// single-plane case if the routing rules move.
	QueryScalar func(context.Context, string, []any, ...any) error
}

// RunRelationsAnswersInThePinnedOrder pins the order the role answers in:
// ascending by the neighbor's id (relations.go:91-95). The edges are seeded in
// DESCENDING id order so an implementation that returned insertion order — or
// the query's natural order — fails rather than coincides.
//
// It also pins the ROW, not just the sequence: relations.go:9-10 says a result
// row carries the edge's type alongside the issue's own fields, so an answer
// in the right order whose rows carry the wrong type is still wrong.
func RunRelationsAnswersInThePinnedOrder(t *testing.T, ctx context.Context, fixture RelationsFixture) {
	t.Helper()
	anchor := fixture.IssuePrefix + "-ord-anchor"
	first := fixture.IssuePrefix + "-ord-n1"
	second := fixture.IssuePrefix + "-ord-n2"
	third := fixture.IssuePrefix + "-ord-n3"
	seedRelationsIssue(t, ctx, fixture, anchor)
	seedRelationsIssue(t, ctx, fixture, first)
	seedRelationsIssue(t, ctx, fixture, second)
	seedRelationsIssue(t, ctx, fixture, third)

	seedRelationsEdge(t, ctx, fixture, anchor, third, publicops.DepWaitsFor)
	seedRelationsEdge(t, ctx, fixture, anchor, second, publicops.DepRelated)
	seedRelationsEdge(t, ctx, fixture, anchor, first, publicops.DepBlocks)

	items := relationsPage(t, ctx, fixture, publicops.RelatedRequest{ID: anchor, Direction: publicops.RelationOut})
	assertRelationsPage(t, items, []string{
		first + ":" + string(publicops.DepBlocks),
		second + ":" + string(publicops.DepRelated),
		third + ":" + string(publicops.DepWaitsFor),
	}, "out neighbors of an anchor whose edges were written in descending id order")

	// The issue's own fields travel with the edge type. The seeds title each
	// issue with its own id, so one comparison covers the whole row's identity.
	for _, item := range items {
		if item.Title != item.ID {
			t.Errorf("neighbor %s carries Title %q, want the seeded issue's own field %q", item.ID, item.Title, item.ID)
		}
	}
}

// RunRelationsOrdersNeighborsFromBothPlanesTogether is the load-bearing half
// of the order. The doc's reason for pinning it is that the rows come from
// "two dependency tables read in sequence" (relations.go:92-95), so the case
// that matters is the one whose neighbors are split across those two tables
// with their ids INTERLEAVED: read plane-by-plane the answer would be
// b, d, a, c, and the contract says a, b, c, d.
//
// Incoming is the direction that can straddle the planes at all. An edge is
// routed by its SOURCE, so one anchor's outgoing edges always share a table,
// while its dependents may be durable issues (dependencies) or wisps
// (wisp_dependencies) in any mixture.
func RunRelationsOrdersNeighborsFromBothPlanesTogether(t *testing.T, ctx context.Context, fixture RelationsFixture) {
	t.Helper()
	anchor := fixture.IssuePrefix + "-plane-anchor"
	wispA := fixture.IssuePrefix + "-plane-a"
	issueB := fixture.IssuePrefix + "-plane-b"
	wispC := fixture.IssuePrefix + "-plane-c"
	issueD := fixture.IssuePrefix + "-plane-d"
	seedRelationsIssue(t, ctx, fixture, anchor)
	seedRelationsWisp(t, ctx, fixture, wispA)
	seedRelationsIssue(t, ctx, fixture, issueB)
	seedRelationsWisp(t, ctx, fixture, wispC)
	seedRelationsIssue(t, ctx, fixture, issueD)

	// Descending again, so neither insertion order nor plane order can be
	// mistaken for the pinned order.
	seedRelationsEdge(t, ctx, fixture, issueD, anchor, publicops.DepBlocks)
	seedRelationsEdge(t, ctx, fixture, wispC, anchor, publicops.DepWaitsFor)
	seedRelationsEdge(t, ctx, fixture, issueB, anchor, publicops.DepRelated)
	seedRelationsEdge(t, ctx, fixture, wispA, anchor, publicops.DepBlocks)

	// The seed really is cross-plane, and stays that way if the routing rules
	// move: without this the case could silently become a single-plane one.
	assertRelationsEdgeInPlane(t, ctx, fixture, "wisp_dependencies", wispA, anchor)
	assertRelationsEdgeInPlane(t, ctx, fixture, "wisp_dependencies", wispC, anchor)
	assertRelationsEdgeInPlane(t, ctx, fixture, "dependencies", issueB, anchor)
	assertRelationsEdgeInPlane(t, ctx, fixture, "dependencies", issueD, anchor)

	items := relationsPage(t, ctx, fixture, publicops.RelatedRequest{ID: anchor, Direction: publicops.RelationIn})
	assertRelationsPage(t, items, []string{
		wispA + ":" + string(publicops.DepBlocks),
		issueB + ":" + string(publicops.DepRelated),
		wispC + ":" + string(publicops.DepWaitsFor),
		issueD + ":" + string(publicops.DepBlocks),
	}, "in neighbors interleaved across the durable and wisp dependency tables")
}

// RunRelationsAnswersAWispTargetInTheOutDirection pins that a neighbor's PLANE
// does not decide whether it is answered: RelationOut is "the issues the anchor
// depends on" (relations.go:34-35) and a result row is "one issue on the far end
// of an edge" (relations.go:9-10), neither of which admits an exception for the
// ephemeral plane.
//
// It exists because the OUT read reaches a target through a different column
// than any other case does. An edge is routed by its SOURCE, so a durable
// anchor's outgoing edges all live in `dependencies` — but the target's own
// class picks the typed column that holds it, and a wisp target lands in
// depends_on_wisp_id. Every other seed in this file puts durable ids there, and
// the cross-plane order case (RunRelationsOrdersNeighborsFromBothPlanesTogether)
// reaches its wisps as edge SOURCES matched by issue_id, so the wisp-target
// column path was unread. An implementation that resolved out-targets from
// depends_on_issue_id alone would silently drop wisp dependencies from `bd dep
// list` and pass every other case here.
//
// The two column probes are what make that specific. Without them the case
// would still pass if the routing rules moved the wisp edge somewhere else, and
// it would no longer be reading the branch it was written for.
func RunRelationsAnswersAWispTargetInTheOutDirection(t *testing.T, ctx context.Context, fixture RelationsFixture) {
	t.Helper()
	anchor := fixture.IssuePrefix + "-wout-anchor"
	first := fixture.IssuePrefix + "-wout-a"
	wisp := fixture.IssuePrefix + "-wout-b"
	third := fixture.IssuePrefix + "-wout-c"
	seedRelationsIssue(t, ctx, fixture, anchor)
	seedRelationsIssue(t, ctx, fixture, first)
	seedRelationsWisp(t, ctx, fixture, wisp)
	seedRelationsIssue(t, ctx, fixture, third)

	// Descending again, and the wisp sits in the MIDDLE of the pinned order, so
	// an implementation that appended cross-plane targets rather than sorting
	// them in fails on the sequence as well as on the membership.
	seedRelationsEdge(t, ctx, fixture, anchor, third, publicops.DepWaitsFor)
	seedRelationsEdge(t, ctx, fixture, anchor, wisp, publicops.DepRelated)
	seedRelationsEdge(t, ctx, fixture, anchor, first, publicops.DepBlocks)

	assertRelationsEdgeInPlane(t, ctx, fixture, "dependencies", anchor, wisp)
	assertRelationsTargetColumn(t, ctx, fixture, "depends_on_wisp_id", anchor, wisp)
	assertRelationsTargetColumn(t, ctx, fixture, "depends_on_issue_id", anchor, first)

	items := relationsPage(t, ctx, fixture, publicops.RelatedRequest{ID: anchor, Direction: publicops.RelationOut})
	assertRelationsPage(t, items, []string{
		first + ":" + string(publicops.DepBlocks),
		wisp + ":" + string(publicops.DepRelated),
		third + ":" + string(publicops.DepWaitsFor),
	}, "out neighbors of an anchor whose middle dependency is a wisp")

	// The wisp neighbor is hydrated from its own plane, not left as a bare id.
	for _, item := range items {
		if item.Title != item.ID {
			t.Errorf("neighbor %s carries Title %q, want the seeded issue's own field %q", item.ID, item.Title, item.ID)
		}
	}
}

// RunRelationsRefusesTheZeroDirection pins the rule the direction type exists
// for (relations.go:27-30, 55-57): the zero value is refused rather than
// defaulted, and there is no implicit "both". Out and in answer inverse
// questions with identical shapes, so a caller handed the wrong one has
// nothing to notice.
//
// The anchor is seeded so a passing implementation cannot be one that refused
// the request for the wrong reason.
func RunRelationsRefusesTheZeroDirection(t *testing.T, ctx context.Context, fixture RelationsFixture) {
	t.Helper()
	anchor := fixture.IssuePrefix + "-dirnone-anchor"
	seedRelationsIssue(t, ctx, fixture, anchor)

	for _, test := range []struct {
		name      string
		direction publicops.RelationDirection
	}{
		{"the zero direction", ""},
		{"an invented both", publicops.RelationDirection("both")},
	} {
		_, err := fixture.Relations.Related(ctx, publicops.RelatedRequest{ID: anchor, Direction: test.direction})
		if !errors.Is(err, publicops.ErrValidation) {
			t.Errorf("Related with %s (%q) error = %v, want ErrValidation", test.name, test.direction, err)
		}
	}
}

// RunRelationsSeparatesNoNeighborsFromNoSuchIssue pins relations.go:50-53 and
// :97 together, because each is only meaningful with the other: "this issue
// has no dependencies" and "there is no such issue" are different facts, and
// an implementation that answered both with an empty page would pass either
// assertion alone.
func RunRelationsSeparatesNoNeighborsFromNoSuchIssue(t *testing.T, ctx context.Context, fixture RelationsFixture) {
	t.Helper()
	lonely := fixture.IssuePrefix + "-lonely"
	absent := fixture.IssuePrefix + "-absent"
	seedRelationsIssue(t, ctx, fixture, lonely)

	items, err := fixture.Relations.Related(ctx, publicops.RelatedRequest{ID: lonely, Direction: publicops.RelationOut})
	if err != nil {
		t.Fatalf("Related on a seeded issue with no edges: %v", err)
	}
	if items == nil {
		t.Error("Related returned nil for a successful call, want an empty slice a caller marshals as [] rather than null")
	}
	if len(items) != 0 {
		t.Errorf("neighbors of an edgeless issue = %v, want none", relationsPageKeys(items))
	}

	if _, err := fixture.Relations.Related(ctx, publicops.RelatedRequest{
		ID: absent, Direction: publicops.RelationOut,
	}); !errors.Is(err, publicops.ErrNotFound) {
		t.Errorf("Related on an id that names neither plane error = %v, want ErrNotFound and not an empty page", err)
	}
}

// RunRelationsResolvesAWispAnchor pins the other half of "both planes are
// searched" (relations.go:50-51). The edgeless wisp is the sharper of the two
// probes: an existence check that looked only at the issues plane would report
// ErrNotFound for it, and no neighbor read would ever run.
func RunRelationsResolvesAWispAnchor(t *testing.T, ctx context.Context, fixture RelationsFixture) {
	t.Helper()
	wisp := fixture.IssuePrefix + "-wisp-anchor"
	target := fixture.IssuePrefix + "-wisp-target"
	bare := fixture.IssuePrefix + "-wisp-bare"
	seedRelationsWisp(t, ctx, fixture, wisp)
	seedRelationsWisp(t, ctx, fixture, bare)
	seedRelationsIssue(t, ctx, fixture, target)
	seedRelationsEdge(t, ctx, fixture, wisp, target, publicops.DepBlocks)

	items := relationsPage(t, ctx, fixture, publicops.RelatedRequest{ID: wisp, Direction: publicops.RelationOut})
	assertRelationsPage(t, items, []string{target + ":" + string(publicops.DepBlocks)},
		"out neighbors of a wisp anchor")

	empty, err := fixture.Relations.Related(ctx, publicops.RelatedRequest{ID: bare, Direction: publicops.RelationOut})
	if err != nil {
		t.Fatalf("Related on a wisp with no edges: %v — the anchor probe missed the wisps plane", err)
	}
	if empty == nil || len(empty) != 0 {
		t.Errorf("neighbors of an edgeless wisp = %v (nil=%v), want an empty non-nil page", relationsPageKeys(empty), empty == nil)
	}
}

// RunRelationsFiltersByAnOpenTypeVocabulary pins relations.go:58-65: the type
// vocabulary is OPEN, so a workspace's own type has to be able to filter. An
// implementation that validated Types against the Dep* constants would break
// every workspace that spelled one of its own, and this is the case that
// catches it.
//
// The empty-result probe at the end is the companion to :77: a usable type no
// edge happens to carry is an empty non-nil page, not an error — it is only an
// UNUSABLE entry that must be refused, which is the next case.
func RunRelationsFiltersByAnOpenTypeVocabulary(t *testing.T, ctx context.Context, fixture RelationsFixture) {
	t.Helper()
	const invented = types.DependencyType("workspace-invented")
	const unused = types.DependencyType("workspace-unused")
	anchor := fixture.IssuePrefix + "-filter-anchor"
	blocker := fixture.IssuePrefix + "-filter-n1"
	related := fixture.IssuePrefix + "-filter-n2"
	custom := fixture.IssuePrefix + "-filter-n3"
	seedRelationsIssue(t, ctx, fixture, anchor)
	seedRelationsIssue(t, ctx, fixture, blocker)
	seedRelationsIssue(t, ctx, fixture, related)
	seedRelationsIssue(t, ctx, fixture, custom)
	seedRelationsEdge(t, ctx, fixture, anchor, blocker, publicops.DepBlocks)
	seedRelationsEdge(t, ctx, fixture, anchor, related, publicops.DepRelated)
	seedRelationsEdge(t, ctx, fixture, anchor, custom, invented)

	for _, test := range []struct {
		name  string
		types []publicops.DependencyType
		want  []string
	}{
		{
			name: "no filter answers every type",
			want: []string{
				blocker + ":" + string(publicops.DepBlocks),
				related + ":" + string(publicops.DepRelated),
				custom + ":" + string(invented),
			},
		},
		{
			name:  "a workspace-invented type filters",
			types: []publicops.DependencyType{invented},
			want:  []string{custom + ":" + string(invented)},
		},
		{
			name:  "several types narrow to their union, still in the pinned order",
			types: []publicops.DependencyType{publicops.DepRelated, publicops.DepBlocks},
			want: []string{
				blocker + ":" + string(publicops.DepBlocks),
				related + ":" + string(publicops.DepRelated),
			},
		},
		{
			name:  "a usable type no edge carries is an empty page, not an error",
			types: []publicops.DependencyType{unused},
			want:  nil,
		},
	} {
		items := relationsPage(t, ctx, fixture, publicops.RelatedRequest{
			ID: anchor, Direction: publicops.RelationOut, Types: test.types,
		})
		if items == nil {
			t.Errorf("%s: Related returned nil for a successful call, want an empty slice", test.name)
			continue
		}
		assertRelationsPage(t, items, test.want, test.name)
	}
}

// RunRelationsRefusesAnUnusableTypeFilter pins the refusal half of
// relations.go:61-65: an entry that is not a value at all is ErrValidation,
// "rather than a filter that quietly matches nothing".
//
// Both entries here are unusable under every reading of the clause. The
// boundary itself — an entry longer than the type COLUMN — is its own case
// below, so a failure there does not take these two with it.
func RunRelationsRefusesAnUnusableTypeFilter(t *testing.T, ctx context.Context, fixture RelationsFixture) {
	t.Helper()
	anchor := fixture.IssuePrefix + "-badfilter-anchor"
	seedRelationsIssue(t, ctx, fixture, anchor)

	for _, test := range []struct {
		name    string
		depType publicops.DependencyType
	}{
		{"an empty entry", ""},
		{"an entry past every length bound", publicops.DependencyType(strings.Repeat("x", 51))},
	} {
		_, err := fixture.Relations.Related(ctx, publicops.RelatedRequest{
			ID:        anchor,
			Direction: publicops.RelationOut,
			Types:     []publicops.DependencyType{test.depType},
		})
		if !errors.Is(err, publicops.ErrValidation) {
			t.Errorf("Related with %s (%d chars) error = %v, want ErrValidation", test.name, len(test.depType), err)
		}
	}
}

// RunRelationsRefusesATypeFilterOverTheColumnLength pins the clause's own
// yardstick: an entry is usable when it is "non-empty, within the COLUMN's
// length" (relations.go:61-62). Both dependency planes store the type in a
// VARCHAR(32) (migrations 0002:4 and 0021:14), so a 40-character type is one
// no edge can ever carry — the insert fails with "string ... is too large for
// column 'type'" — and accepting it as a filter is exactly the quiet
// match-nothing the clause forbids.
//
// The shared validator bounds a type at types.MaxDependencyTypeLen, which is
// that column width (bd-yby99.3 narrowed it from a looser 50), so the refusal
// arrives from one place ahead of every backend.
func RunRelationsRefusesATypeFilterOverTheColumnLength(t *testing.T, ctx context.Context, fixture RelationsFixture) {
	t.Helper()
	anchor := fixture.IssuePrefix + "-longfilter-anchor"
	seedRelationsIssue(t, ctx, fixture, anchor)

	overLong := publicops.DependencyType(strings.Repeat("x", 40))
	_, err := fixture.Relations.Related(ctx, publicops.RelatedRequest{
		ID:        anchor,
		Direction: publicops.RelationOut,
		Types:     []publicops.DependencyType{overLong},
	})
	if !errors.Is(err, publicops.ErrValidation) {
		t.Errorf("Related with a %d-character type — longer than the VARCHAR(32) the planes store — error = %v, want ErrValidation",
			len(overLong), err)
	}
}

// RunRelationsDirectionSelectsTheInverseGraph pins relations.go:34-38 against
// ONE seed graph read both ways: out is what the anchor depends on, in is what
// depends on the anchor. Asserting the two directions separately, on separate
// graphs, is what lets an inversion hide — and an inversion is the failure the
// zero-value direction rule exists to prevent, so the contract has to be able
// to see it.
func RunRelationsDirectionSelectsTheInverseGraph(t *testing.T, ctx context.Context, fixture RelationsFixture) {
	t.Helper()
	anchor := fixture.IssuePrefix + "-dir-anchor"
	// The anchor depends on its blocker; its dependent depends on the anchor.
	blocker := fixture.IssuePrefix + "-dir-blocker"
	dependent := fixture.IssuePrefix + "-dir-dependent"
	seedRelationsIssue(t, ctx, fixture, anchor)
	seedRelationsIssue(t, ctx, fixture, blocker)
	seedRelationsIssue(t, ctx, fixture, dependent)
	seedRelationsEdge(t, ctx, fixture, anchor, blocker, publicops.DepBlocks)
	seedRelationsEdge(t, ctx, fixture, dependent, anchor, publicops.DepBlocks)

	out := relationsPage(t, ctx, fixture, publicops.RelatedRequest{ID: anchor, Direction: publicops.RelationOut})
	assertRelationsPage(t, out, []string{blocker + ":" + string(publicops.DepBlocks)},
		"out neighbors, which are what the anchor depends on")

	in := relationsPage(t, ctx, fixture, publicops.RelatedRequest{ID: anchor, Direction: publicops.RelationIn})
	assertRelationsPage(t, in, []string{dependent + ":" + string(publicops.DepBlocks)},
		"in neighbors, which are what depends on the anchor")
}

// RunRelationsLeavesTheCallersRequestAlone is the request-snapshot tripwire for
// relations.go:83-87: implementations never mutate caller-owned request values
// and normalize only attempt-local clones.
//
// RelatedRequest travels by value, so Types is the only member that can carry a
// write back to the caller, and it is populated here with exactly what an
// in-place normalizer would reach for — a duplicate entry, in descending order.
// Both bodies read it into a set today, so this is a tripwire rather than a bug
// report: it fails the day a sort or a dedupe is written against the caller's
// own backing array instead of a clone.
//
// The request answers a real page rather than an empty one, because a filter
// matching nothing can be short-circuited before the type set is ever built —
// the path where no mutation would happen anyway.
func RunRelationsLeavesTheCallersRequestAlone(t *testing.T, ctx context.Context, fixture RelationsFixture) {
	t.Helper()
	anchor := fixture.IssuePrefix + "-snap-anchor"
	blocker := fixture.IssuePrefix + "-snap-n1"
	related := fixture.IssuePrefix + "-snap-n2"
	seedRelationsIssue(t, ctx, fixture, anchor)
	seedRelationsIssue(t, ctx, fixture, blocker)
	seedRelationsIssue(t, ctx, fixture, related)
	seedRelationsEdge(t, ctx, fixture, anchor, blocker, publicops.DepBlocks)
	seedRelationsEdge(t, ctx, fixture, anchor, related, publicops.DepRelated)

	build := func() publicops.RelatedRequest {
		return publicops.RelatedRequest{
			ID:        anchor,
			Direction: publicops.RelationOut,
			Types: []publicops.DependencyType{
				publicops.DepRelated,
				publicops.DepBlocks,
				publicops.DepRelated,
			},
		}
	}
	request := build()
	want := build()

	items := relationsPage(t, ctx, fixture, request)
	assertRelationsPage(t, items, []string{
		blocker + ":" + string(publicops.DepBlocks),
		related + ":" + string(publicops.DepRelated),
	}, "out neighbors under a duplicated, descending type filter")

	if !reflect.DeepEqual(request, want) {
		t.Errorf("Related mutated the caller's request:\n got %#v\nwant %#v", request, want)
	}
}

// RunRelationsLeavesAnExternalTargetOutOfTheAnswer pins what a row's own type
// decides: "It is an ISSUE and can be nothing else ... Related leaves it out,
// with no placeholder row and no error" (relations.go:12-17).
//
// The two seeds are the two shapes issueops.IsExternalDepTarget names — an
// "external:" reference and a target whose id prefix belongs to another
// repository — because each reaches depends_on_external by a different rule and
// a backend can implement one and miss the other, which is exactly what
// bd-ocrn7 was. DependencyEditor accepts both
// (RunDependencyEditorAcceptsAnExternalTarget, ...AcceptsAForeignRepoTarget),
// which is what makes an unanswerable edge a state a workspace really reaches
// rather than a hypothetical.
//
// The resolvable neighbor is what keeps the case from passing vacuously: an
// implementation that answered nothing at all would satisfy an assertion about
// absence alone. The column probes are what keep it reading the branch it was
// written for — without them a routing change could quietly turn both seeds
// into ordinary durable targets, and the case would still pass while asserting
// something else entirely.
//
// The ErrNotFound at the end is the same fact from the other side: an id this
// database holds no row for is not an ANCHOR either (relations.go:50-53), so
// there is no direction from which the external edge becomes visible through
// this role.
func RunRelationsLeavesAnExternalTargetOutOfTheAnswer(t *testing.T, ctx context.Context, fixture RelationsFixture) {
	t.Helper()
	anchor := fixture.IssuePrefix + "-ext-anchor"
	neighbor := fixture.IssuePrefix + "-ext-n"
	external := "external:" + fixture.IssuePrefix + "-ext-tracker"
	const foreign = "otherrig-9001"
	seedRelationsIssue(t, ctx, fixture, anchor)
	seedRelationsIssue(t, ctx, fixture, neighbor)

	seedRelationsEdge(t, ctx, fixture, anchor, neighbor, publicops.DepBlocks)
	seedRelationsEdge(t, ctx, fixture, anchor, external, publicops.DepRelated)
	seedRelationsEdge(t, ctx, fixture, anchor, foreign, publicops.DepWaitsFor)

	assertRelationsTargetColumn(t, ctx, fixture, "depends_on_external", anchor, external)
	assertRelationsTargetColumn(t, ctx, fixture, "depends_on_external", anchor, foreign)
	assertRelationsTargetColumn(t, ctx, fixture, "depends_on_issue_id", anchor, neighbor)

	items := relationsPage(t, ctx, fixture, publicops.RelatedRequest{ID: anchor, Direction: publicops.RelationOut})
	assertRelationsPage(t, items, []string{neighbor + ":" + string(publicops.DepBlocks)},
		"out neighbors of an anchor with three edges, two of which point outside this database")

	for _, id := range []string{external, foreign} {
		if _, err := fixture.Relations.Related(ctx, publicops.RelatedRequest{
			ID: id, Direction: publicops.RelationIn,
		}); !errors.Is(err, publicops.ErrNotFound) {
			t.Errorf("Related anchored on %s error = %v, want ErrNotFound: an id neither plane holds is not an anchor", id, err)
		}
	}
}

// RunRelationsResolvesTheAnchorIDExactly pins the anchor clause's promise half
// (relations.go:42-48): "EXACT is a promise the role keeps rather than an
// obligation it puts on the caller", so a case variant, an id carrying
// surrounding whitespace, a prefix of a real id and a real id with a suffix are
// all ErrNotFound.
//
// The case variant is the entry that earns the case. The other three are
// different STRINGS and would miss under any equality; a case variant is the
// same string under a case-insensitive collation, so this is the one spelling
// whose answer today comes from the ENGINE rather than from anything the role
// does — validation checks only that the id is non-empty
// (internal/storage/issueops/relations.go:27-29) and resolution is then a
// `WHERE id = ?` per plane. All three backends run a binary collation and refuse
// it, which is why the promise could be adopted; this case is what holds a
// backend on a case-insensitive collation to it, where an anchor typed in the
// wrong case would otherwise be answered as though it were the right one.
//
// The exact spelling is asserted first and answers a real neighbor, so a body
// that refused every request would fail here rather than pass the refusals.
func RunRelationsResolvesTheAnchorIDExactly(t *testing.T, ctx context.Context, fixture RelationsFixture) {
	t.Helper()
	anchor := fixture.IssuePrefix + "-exact-anchor"
	neighbor := fixture.IssuePrefix + "-exact-n"
	seedRelationsIssue(t, ctx, fixture, anchor)
	seedRelationsIssue(t, ctx, fixture, neighbor)
	seedRelationsEdge(t, ctx, fixture, anchor, neighbor, publicops.DepBlocks)

	items := relationsPage(t, ctx, fixture, publicops.RelatedRequest{ID: anchor, Direction: publicops.RelationOut})
	assertRelationsPage(t, items, []string{neighbor + ":" + string(publicops.DepBlocks)},
		"out neighbors of the anchor spelled exactly")

	for _, test := range []struct{ what, id string }{
		{"a case variant", strings.ToUpper(anchor)},
		{"a trailing space", anchor + " "},
		{"a leading space", " " + anchor},
		{"a prefix of the id", anchor[:len(anchor)-2]},
		{"the id with a suffix", anchor + "x"},
	} {
		got, err := fixture.Relations.Related(ctx, publicops.RelatedRequest{ID: test.id, Direction: publicops.RelationOut})
		if !errors.Is(err, publicops.ErrNotFound) {
			t.Errorf("Related with %s (%q) = (%v, %v), want ErrNotFound: this role resolves the stored spelling only",
				test.what, test.id, relationsPageKeys(got), err)
		}
	}
}

func seedRelationsIssue(t *testing.T, ctx context.Context, fixture RelationsFixture, id string) {
	t.Helper()
	if err := fixture.CreateIssue(ctx, relationsSeed(id, false), "seed"); err != nil {
		t.Fatalf("seed issue %s: %v", id, err)
	}
}

func seedRelationsWisp(t *testing.T, ctx context.Context, fixture RelationsFixture, id string) {
	t.Helper()
	if err := fixture.CreateWisp(ctx, relationsSeed(id, true), "seed"); err != nil {
		t.Fatalf("seed wisp %s: %v", id, err)
	}
}

func seedRelationsEdge(t *testing.T, ctx context.Context, fixture RelationsFixture, from, to string, depType types.DependencyType) {
	t.Helper()
	if err := fixture.AddDependency(ctx, &types.Dependency{IssueID: from, DependsOnID: to, Type: depType}, "seed"); err != nil {
		t.Fatalf("seed edge %s -%s-> %s: %v", from, depType, to, err)
	}
}

// relationsSeed titles each issue with its own id, so a case can check that a
// result row carries the issue's own fields with one comparison.
func relationsSeed(id string, ephemeral bool) *types.Issue {
	return &types.Issue{
		ID:        id,
		Title:     id,
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: ephemeral,
	}
}

// relationsPage runs one request that is expected to succeed.
func relationsPage(t *testing.T, ctx context.Context, fixture RelationsFixture, request publicops.RelatedRequest) []*publicops.RelatedIssue {
	t.Helper()
	items, err := fixture.Relations.Related(ctx, request)
	if err != nil {
		t.Fatalf("Related(%s, %s, types=%v): %v", request.ID, request.Direction, request.Types, err)
	}
	return items
}

// relationsPageKeys renders a page as "id:type" strings IN THE ORDER the role
// answered, so a comparison against a literal compares the pinned order and
// not a set.
func relationsPageKeys(items []*publicops.RelatedIssue) []string {
	keys := make([]string, 0, len(items))
	for _, item := range items {
		keys = append(keys, item.ID+":"+string(item.DependencyType))
	}
	return keys
}

func assertRelationsPage(t *testing.T, items []*publicops.RelatedIssue, want []string, describe string) {
	t.Helper()
	got := relationsPageKeys(items)
	if len(got) != len(want) {
		t.Errorf("%s = %v, want %v", describe, got, want)
		return
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("%s = %v, want %v", describe, got, want)
			return
		}
	}
}

// assertRelationsEdgeInPlane checks that the seeded edge from source to target
// landed in the named dependency table. The target is matched through the
// resolved target expression because a target's own class decides which typed
// column holds it, independently of the source routing under test.
func assertRelationsEdgeInPlane(t *testing.T, ctx context.Context, fixture RelationsFixture, table, source, target string) {
	t.Helper()
	var got int
	//nolint:gosec // G201: table is one of the contract's two hardcoded names.
	query := "SELECT COUNT(*) FROM " + table +
		" WHERE issue_id = ? AND COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) = ?"
	if err := fixture.QueryScalar(ctx, query, []any{source, target}, &got); err != nil {
		t.Fatalf("count %s edges %s -> %s: %v", table, source, target, err)
	}
	if got != 1 {
		t.Fatalf("%s edges %s -> %s = %d, want 1 — the cross-plane seed is no longer cross-plane", table, source, target, got)
	}
}

// assertRelationsTargetColumn checks WHICH typed target column of the durable
// dependency table holds the seeded edge's target. assertRelationsEdgeInPlane
// resolves the target through a COALESCE over all three, so it cannot tell a
// wisp target from a durable one; a case whose whole subject is the column path
// the read walks needs the column named.
func assertRelationsTargetColumn(t *testing.T, ctx context.Context, fixture RelationsFixture, column, source, target string) {
	t.Helper()
	var got int
	//nolint:gosec // G201: column is one of the contract's hardcoded target-column names.
	query := "SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND " + column + " = ?"
	if err := fixture.QueryScalar(ctx, query, []any{source, target}, &got); err != nil {
		t.Fatalf("count dependencies edges %s -> %s held in %s: %v", source, target, column, err)
	}
	if got != 1 {
		t.Fatalf("dependencies edges %s -> %s held in %s = %d, want 1 — the seed no longer exercises that column path",
			source, target, column, got)
	}
}
