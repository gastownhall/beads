package conformance

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// This file holds the contract every implementation of
// publicops.DependencyEditor must satisfy. There are three of them — the
// direct store, the embedded store, and the unit-of-work backend — and the
// first two share a transaction body the third does not, so a rule asserted
// against one has repeatedly drifted on the others. A run on the third
// backend therefore catches WRAPPER AND ENGINE divergence between the two that
// share a body; only the unit-of-work run is an independent vote on the body.
//
// The first four cases pin the one thing the three had actually diverged on:
// which PLANE an edge lands in. `bd dep add <wisp-id> <target>` writes the
// ephemeral graph, and it has to, because a wisp has no row in the issues
// plane for an edge to hang off.
//
// The rest pin the role's older semantics — request-order echo, idempotency,
// refusal atomicity, the typed refusals, application order, external targets,
// request hygiene and the one-history-entry-per-landed-request rule. Each is
// written to what issueops/dependencyeditor.go PROMISES; where an
// implementation disagrees, the case still asserts the doc and the losing
// backend's wiring parks it with skipKnownDivergence.

// DependencyEditorFixture supplies adapter-specific storage access for the
// dependency-editor assertions.
type DependencyEditorFixture struct {
	// IssuePrefix namespaces the ids each assertion seeds, so several of them
	// can share one database.
	IssuePrefix string
	Editor      publicops.DependencyEditor
	// CreateIssue seeds a durable issue in the issues plane.
	CreateIssue func(context.Context, *types.Issue, string) error
	// CreateWisp seeds an ephemeral issue in the wisps plane. It is a separate
	// field rather than an Ephemeral flag on CreateIssue because the three
	// adapters reach the two planes through different verbs.
	CreateWisp  func(context.Context, *types.Issue, string) error
	QueryScalar func(context.Context, string, []any, ...any) error
	// CountHistory reports how many history entries the fixture's branch has.
	// The cases that need it take it before and after the operation under test,
	// because two commits made inside one second tie on date and their relative
	// order is not something to rely on.
	//
	// A nil CountHistory means "this backend cannot observe history here", and
	// the cases that need it then skip with that reason rather than pass
	// quietly. It is non-nil on all three backends today.
	CountHistory func(context.Context) (int, error)
}

// RunDependencyEditorRoutesWispSourcedEdgeToTheWispPlane is the regression pin
// for a wisp-sourced edge. Both the edge and its event follow the SOURCE:
// wisp_dependencies and wisp_events, never the durable pair. Pinning the
// durable pair at zero is the half that catches a regression writing an edge
// whose issue_id names a row the issues plane does not have.
func RunDependencyEditorRoutesWispSourcedEdgeToTheWispPlane(t *testing.T, ctx context.Context, fixture DependencyEditorFixture) {
	t.Helper()
	wisp := fixture.IssuePrefix + "-wdep-src"
	target := fixture.IssuePrefix + "-wdep-tgt"
	seedDependencyEditorWisp(t, ctx, fixture, wisp)
	seedDependencyEditorIssue(t, ctx, fixture, target)

	if _, err := fixture.Editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
		Actor: "writer",
		Edges: []publicops.DependencyEdge{{IssueID: wisp, DependsOnID: target, Type: publicops.DepBlocks}},
	}); err != nil {
		t.Fatalf("AddDependencies from a wisp source: %v", err)
	}

	assertDependencyEdgeCount(t, ctx, fixture, "wisp_dependencies", wisp, target, 1)
	assertDependencyEdgeCount(t, ctx, fixture, "dependencies", wisp, target, 0)
	assertDependencyEditorEventCount(t, ctx, fixture, "wisp_events", wisp, types.EventDependencyAdded, 1)
	assertDependencyEditorEventCount(t, ctx, fixture, "events", wisp, types.EventDependencyAdded, 0)
}

// RunDependencyEditorMixedBatchWritesBothPlanes proves one request can span
// the two planes. The batch is one transaction with two write paths in it, so
// this is the case that catches a routing decision taken once for the whole
// request instead of once per edge.
func RunDependencyEditorMixedBatchWritesBothPlanes(t *testing.T, ctx context.Context, fixture DependencyEditorFixture) {
	t.Helper()
	wisp := fixture.IssuePrefix + "-mixed-wisp"
	issue := fixture.IssuePrefix + "-mixed-issue"
	target := fixture.IssuePrefix + "-mixed-target"
	seedDependencyEditorWisp(t, ctx, fixture, wisp)
	seedDependencyEditorIssue(t, ctx, fixture, issue)
	seedDependencyEditorIssue(t, ctx, fixture, target)

	if _, err := fixture.Editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
		Actor: "writer",
		Edges: []publicops.DependencyEdge{
			{IssueID: wisp, DependsOnID: target, Type: publicops.DepBlocks},
			{IssueID: issue, DependsOnID: target, Type: publicops.DepBlocks},
		},
	}); err != nil {
		t.Fatalf("AddDependencies on a mixed-plane batch: %v", err)
	}

	assertDependencyEdgeCount(t, ctx, fixture, "wisp_dependencies", wisp, target, 1)
	assertDependencyEdgeCount(t, ctx, fixture, "dependencies", wisp, target, 0)
	assertDependencyEdgeCount(t, ctx, fixture, "dependencies", issue, target, 1)
	assertDependencyEdgeCount(t, ctx, fixture, "wisp_dependencies", issue, target, 0)
}

// RunDependencyEditorMixedBatchRefusalRollsBackBothPlanes proves the
// all-or-nothing promise survives the plane split: a refusal on the durable
// edge must take the ephemeral edge written before it back out too.
//
// The refusal is a type conflict rather than a cycle so that it fires at the
// edge, after the earlier edge is already written, rather than at the
// whole-graph gate.
func RunDependencyEditorMixedBatchRefusalRollsBackBothPlanes(t *testing.T, ctx context.Context, fixture DependencyEditorFixture) {
	t.Helper()
	wisp := fixture.IssuePrefix + "-rb-wisp"
	issue := fixture.IssuePrefix + "-rb-issue"
	target := fixture.IssuePrefix + "-rb-target"
	seedDependencyEditorWisp(t, ctx, fixture, wisp)
	seedDependencyEditorIssue(t, ctx, fixture, issue)
	seedDependencyEditorIssue(t, ctx, fixture, target)

	if _, err := fixture.Editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
		Actor: "writer",
		Edges: []publicops.DependencyEdge{{IssueID: issue, DependsOnID: target, Type: publicops.DepBlocks}},
	}); err != nil {
		t.Fatalf("seed the durable edge that the retype will collide with: %v", err)
	}

	_, err := fixture.Editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
		Actor: "writer",
		Edges: []publicops.DependencyEdge{
			{IssueID: wisp, DependsOnID: target, Type: publicops.DepBlocks},
			{IssueID: issue, DependsOnID: target, Type: publicops.DepRelated},
		},
	})
	var conflict *publicops.DependencyTypeConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("mixed-batch retype error = %v, want *DependencyTypeConflictError", err)
	}
	assertDependencyEdgeCount(t, ctx, fixture, "wisp_dependencies", wisp, target, 0)
	assertDependencyEdgeCount(t, ctx, fixture, "dependencies", wisp, target, 0)
	assertDependencyEditorEventCount(t, ctx, fixture, "wisp_events", wisp, types.EventDependencyAdded, 0)
	assertDependencyEdgeTypedCount(t, ctx, fixture, "dependencies", issue, target, string(publicops.DepBlocks), 1)
}

// RunDependencyEditorRefusesCrossPlaneCycle pins bd-xe27: the scheduling graph
// is one graph across the two tables, so an edge closing a loop that leaves
// the issues plane and comes back through the wisp plane is refused.
func RunDependencyEditorRefusesCrossPlaneCycle(t *testing.T, ctx context.Context, fixture DependencyEditorFixture) {
	t.Helper()
	issue := fixture.IssuePrefix + "-xcycle-issue"
	wisp := fixture.IssuePrefix + "-xcycle-wisp"
	seedDependencyEditorIssue(t, ctx, fixture, issue)
	seedDependencyEditorWisp(t, ctx, fixture, wisp)

	// issue -> wisp lands in the durable table with a wisp target.
	if _, err := fixture.Editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
		Actor: "writer",
		Edges: []publicops.DependencyEdge{{IssueID: issue, DependsOnID: wisp, Type: publicops.DepBlocks}},
	}); err != nil {
		t.Fatalf("AddDependencies issue -> wisp: %v", err)
	}
	assertDependencyEdgeCount(t, ctx, fixture, "dependencies", issue, wisp, 1)

	// wisp -> issue would close the loop through the other table.
	_, err := fixture.Editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
		Actor: "writer",
		Edges: []publicops.DependencyEdge{{IssueID: wisp, DependsOnID: issue, Type: publicops.DepBlocks}},
	})
	if !errors.Is(err, publicops.ErrDependencyCycle) {
		t.Fatalf("cross-plane cycle error = %v, want ErrDependencyCycle", err)
	}
	assertDependencyEdgeCount(t, ctx, fixture, "wisp_dependencies", wisp, issue, 0)

	// The same loop, hidden from the per-edge probe, must still be refused by
	// the whole-graph gate.
	_, err = fixture.Editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
		Actor:                 "writer",
		SkipPerEdgeCycleCheck: true,
		Edges:                 []publicops.DependencyEdge{{IssueID: wisp, DependsOnID: issue, Type: publicops.DepBlocks}},
	})
	if !errors.Is(err, publicops.ErrDependencyCycle) {
		t.Fatalf("cross-plane cycle error with the probe skipped = %v, want ErrDependencyCycle from the final gate", err)
	}
	assertDependencyEdgeCount(t, ctx, fixture, "wisp_dependencies", wisp, issue, 0)
}

// RunDependencyEditorAddedEchoesTheRequestOrder pins AddDependenciesResult.Added
// (issueops/dependencyeditor.go:76-81): all-or-nothing means Added is either
// every requested edge or the call failed, so it echoes the REQUEST — a caller
// reporting what landed reads the result and never has to know which.
//
// The request deliberately leads with a blocking edge and ends with a
// parent-child one, which is the reverse of the order the edges are APPLIED in
// (dependencyeditor.go:61-64). The application order must not leak into the
// result.
func RunDependencyEditorAddedEchoesTheRequestOrder(t *testing.T, ctx context.Context, fixture DependencyEditorFixture) {
	t.Helper()
	source := fixture.IssuePrefix + "-echo-source"
	target := fixture.IssuePrefix + "-echo-target"
	child := fixture.IssuePrefix + "-echo-child"
	for _, id := range []string{source, target, child} {
		seedDependencyEditorIssue(t, ctx, fixture, id)
	}

	edges := []publicops.DependencyEdge{
		{IssueID: source, DependsOnID: target, Type: publicops.DepBlocks},
		{IssueID: child, DependsOnID: source, Type: publicops.DepParentChild},
	}
	result, err := fixture.Editor.AddDependencies(ctx, publicops.AddDependenciesRequest{Actor: "writer", Edges: edges})
	if err != nil {
		t.Fatalf("AddDependencies: %v", err)
	}
	if len(result.Added) != len(edges) {
		t.Fatalf("Added = %#v, want %d edges", result.Added, len(edges))
	}
	for i, want := range edges {
		if result.Added[i] != want {
			t.Errorf("Added[%d] = %#v, want %#v: the result echoes the REQUEST order, not the order the edges were applied in", i, result.Added[i], want)
		}
	}

	assertDependencyEdgeTypedCount(t, ctx, fixture, "dependencies", source, target, string(publicops.DepBlocks), 1)
	assertDependencyEdgeTypedCount(t, ctx, fixture, "dependencies", child, source, string(publicops.DepParentChild), 1)
}

// RunDependencyEditorSameTypeReAddIsIdempotent pins the same-type re-add
// clause: an edge that already exists with the SAME type is idempotent and
// refuses nothing. The row count is what makes it idempotent rather than
// merely tolerated.
//
// It also pins the clause's second half, that a request which wrote no durable
// edge records NO history entry. The doc reaches that by the same argument the
// removal side already made — nothing was written, so there is nothing to
// version — and the two halves belong in one case because the history delta is
// only meaningful next to the row count that explains it.
//
// The event stream is the third observable and the sharpest one: the leaf
// promises a dependency_added entry for a GENUINELY NEW edge, so a backend
// that re-emitted on the no-op would leave a history of work that did not
// happen even where the row count could not tell.
func RunDependencyEditorSameTypeReAddIsIdempotent(t *testing.T, ctx context.Context, fixture DependencyEditorFixture) {
	t.Helper()
	source := fixture.IssuePrefix + "-idem-source"
	target := fixture.IssuePrefix + "-idem-target"
	seedDependencyEditorIssue(t, ctx, fixture, source)
	seedDependencyEditorIssue(t, ctx, fixture, target)

	request := publicops.AddDependenciesRequest{
		Actor: "writer",
		Edges: []publicops.DependencyEdge{{IssueID: source, DependsOnID: target, Type: publicops.DepBlocks}},
	}
	if _, err := fixture.Editor.AddDependencies(ctx, request); err != nil {
		t.Fatalf("AddDependencies first: %v", err)
	}
	assertDependencyEditorEventCount(t, ctx, fixture, "events", source, types.EventDependencyAdded, 1)

	assertHistoryDelta := dependencyEditorHistoryProbe(t, ctx, fixture)
	result, err := fixture.Editor.AddDependencies(ctx, request)
	if err != nil {
		t.Fatalf("re-adding the same edge with the same type refused: %v", err)
	}
	if len(result.Added) != 1 || result.Added[0] != request.Edges[0] {
		t.Errorf("Added on the idempotent re-add = %#v, want the requested edge: the result echoes the request either way", result.Added)
	}
	assertDependencyEditorOutgoingCount(t, ctx, fixture, "dependencies", source, 1)
	assertDependencyEdgeTypedCount(t, ctx, fixture, "dependencies", source, target, string(publicops.DepBlocks), 1)
	assertDependencyEditorEventCount(t, ctx, fixture, "events", source, types.EventDependencyAdded, 1)
	assertHistoryDelta(0, "every edge of the request already existed with the requested type, so nothing was written and nothing is versioned")
}

// RunDependencyEditorRepeatsWithinOneRequestCollapse pins the clause that
// answers a request naming ONE PAIR TWICE. The same-type re-add rule is stated
// for the pair rather than for the call, so the second occurrence inside a
// request finds the first already written and the pair is applied once — while
// the result still echoes every edge the caller asked for, because
// AddDependenciesResult.Added is all-or-nothing and echoes the REQUEST.
//
// The second half is the conflict: two DIFFERENT types for one pair in one
// request raise the same *DependencyTypeConflictError a pre-existing edge of
// the other type raises, and the all-or-nothing rule takes the first
// occurrence back out with it. That the refusal is the TYPED one is the part
// worth pinning — a backend that treated a within-request repeat as a distinct
// insert would surface a primary-key violation instead, which is the same
// refusal with none of the information.
func RunDependencyEditorRepeatsWithinOneRequestCollapse(t *testing.T, ctx context.Context, fixture DependencyEditorFixture) {
	t.Helper()
	source := fixture.IssuePrefix + "-repeat-source"
	target := fixture.IssuePrefix + "-repeat-target"
	seedDependencyEditorIssue(t, ctx, fixture, source)
	seedDependencyEditorIssue(t, ctx, fixture, target)

	edge := publicops.DependencyEdge{IssueID: source, DependsOnID: target, Type: publicops.DepBlocks}
	assertHistoryDelta := dependencyEditorHistoryProbe(t, ctx, fixture)
	result, err := fixture.Editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
		Actor: "writer",
		Edges: []publicops.DependencyEdge{edge, edge},
	})
	if err != nil {
		t.Fatalf("AddDependencies naming one pair twice: %v — a repeat is the same-type re-add rule applied inside the request", err)
	}
	if len(result.Added) != 2 || result.Added[0] != edge || result.Added[1] != edge {
		t.Errorf("Added = %#v, want both requested edges: the result echoes the REQUEST, not the rows written", result.Added)
	}
	assertDependencyEditorOutgoingCount(t, ctx, fixture, "dependencies", source, 1)
	assertDependencyEdgeTypedCount(t, ctx, fixture, "dependencies", source, target, string(publicops.DepBlocks), 1)
	assertDependencyEditorEventCount(t, ctx, fixture, "events", source, types.EventDependencyAdded, 1)
	assertHistoryDelta(1, "one pair named twice is one edge written, so it is still ONE history entry")

	conflictSource := fixture.IssuePrefix + "-repeat-conflict-source"
	conflictTarget := fixture.IssuePrefix + "-repeat-conflict-target"
	seedDependencyEditorIssue(t, ctx, fixture, conflictSource)
	seedDependencyEditorIssue(t, ctx, fixture, conflictTarget)

	_, err = fixture.Editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
		Actor: "writer",
		Edges: []publicops.DependencyEdge{
			{IssueID: conflictSource, DependsOnID: conflictTarget, Type: publicops.DepBlocks},
			{IssueID: conflictSource, DependsOnID: conflictTarget, Type: publicops.DepRelated},
		},
	})
	var conflict *publicops.DependencyTypeConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("one pair given two types in one request: error = %v, want *DependencyTypeConflictError", err)
	}
	assertDependencyEditorNoEdgesFrom(t, ctx, fixture, conflictSource)
}

// RunDependencyEditorAttributesItsEventsToTheActor pins what
// AddDependenciesRequest.Actor and RemoveDependencyRequest.Actor are FOR: the
// leaf says each is what the entry the operation records in the source's event
// stream is attributed to, and the result carries it nowhere else.
//
// Every other case in this file reads those rows as a plane-routing probe and
// counts them. This is the one that reads the attribution, and it uses a
// different actor for the add and the removal so a backend that stamped the
// row from anything but the request under way — a session identity, the
// creator, the previous actor — fails it.
func RunDependencyEditorAttributesItsEventsToTheActor(t *testing.T, ctx context.Context, fixture DependencyEditorFixture) {
	t.Helper()
	source := fixture.IssuePrefix + "-actor-source"
	target := fixture.IssuePrefix + "-actor-target"
	seedDependencyEditorIssue(t, ctx, fixture, source)
	seedDependencyEditorIssue(t, ctx, fixture, target)

	if _, err := fixture.Editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
		Actor: "edge-author",
		Edges: []publicops.DependencyEdge{{IssueID: source, DependsOnID: target, Type: publicops.DepBlocks}},
	}); err != nil {
		t.Fatalf("AddDependencies: %v", err)
	}
	assertDependencyEditorEventActor(t, ctx, fixture, source, types.EventDependencyAdded, "edge-author")

	removed, err := fixture.Editor.RemoveDependency(ctx, publicops.RemoveDependencyRequest{
		Actor: "edge-remover", IssueID: source, DependsOnID: target,
	})
	if err != nil || !removed.Removed {
		t.Fatalf("RemoveDependency = %#v, %v; want Removed true", removed, err)
	}
	assertDependencyEditorEventActor(t, ctx, fixture, source, types.EventDependencyRemoved, "edge-remover")
}

// RunDependencyEditorRetypeRefusalLeavesTheOriginalEdge pins the other half of
// the same clause (dependencyeditor.go:145-146): a conflicting type on a pair
// that already has an edge is *DependencyTypeConflictError, and because the
// request is all-or-nothing the edge that was already there is untouched.
func RunDependencyEditorRetypeRefusalLeavesTheOriginalEdge(t *testing.T, ctx context.Context, fixture DependencyEditorFixture) {
	t.Helper()
	source := fixture.IssuePrefix + "-retype-source"
	target := fixture.IssuePrefix + "-retype-target"
	seedDependencyEditorIssue(t, ctx, fixture, source)
	seedDependencyEditorIssue(t, ctx, fixture, target)

	if _, err := fixture.Editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
		Actor: "writer",
		Edges: []publicops.DependencyEdge{{IssueID: source, DependsOnID: target, Type: publicops.DepBlocks}},
	}); err != nil {
		t.Fatalf("seed the edge the retype will collide with: %v", err)
	}

	_, err := fixture.Editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
		Actor: "writer",
		Edges: []publicops.DependencyEdge{{IssueID: source, DependsOnID: target, Type: publicops.DepRelated}},
	})
	var conflict *publicops.DependencyTypeConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("retype error = %v, want *DependencyTypeConflictError", err)
	}
	assertDependencyEditorOutgoingCount(t, ctx, fixture, "dependencies", source, 1)
	assertDependencyEdgeTypedCount(t, ctx, fixture, "dependencies", source, target, string(publicops.DepBlocks), 1)
	assertDependencyEdgeTypedCount(t, ctx, fixture, "dependencies", source, target, string(publicops.DepRelated), 0)
}

// RunDependencyEditorRefusalWritesNothing pins dependencyeditor.go:143-144 —
// any refused edge refuses the whole request and writes nothing — on BOTH
// refusal paths: the per-edge cycle probe, which fires with an earlier edge of
// the same request already written, and the whole-graph gate that runs when
// SkipPerEdgeCycleCheck turned the probe off (dependencyeditor.go:66-70).
//
// The skip-probe half needs three edges because the cycle it closes only
// exists once all three of the request's own edges are in place, which is
// exactly what a per-edge probe cannot see.
func RunDependencyEditorRefusalWritesNothing(t *testing.T, ctx context.Context, fixture DependencyEditorFixture) {
	t.Helper()
	a := fixture.IssuePrefix + "-refuse-a"
	b := fixture.IssuePrefix + "-refuse-b"
	c := fixture.IssuePrefix + "-refuse-c"
	for _, id := range []string{a, b, c} {
		seedDependencyEditorIssue(t, ctx, fixture, id)
	}

	_, err := fixture.Editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
		Actor: "writer",
		Edges: []publicops.DependencyEdge{
			{IssueID: a, DependsOnID: b, Type: publicops.DepBlocks},
			{IssueID: b, DependsOnID: a, Type: publicops.DepBlocks},
		},
	})
	if !errors.Is(err, publicops.ErrDependencyCycle) {
		t.Fatalf("per-edge cycle error = %v, want ErrDependencyCycle", err)
	}
	assertDependencyEditorNoEdgesFrom(t, ctx, fixture, a, b)

	_, err = fixture.Editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
		Actor:                 "writer",
		SkipPerEdgeCycleCheck: true,
		Edges: []publicops.DependencyEdge{
			{IssueID: a, DependsOnID: b, Type: publicops.DepBlocks},
			{IssueID: b, DependsOnID: c, Type: publicops.DepBlocks},
			{IssueID: c, DependsOnID: a, Type: publicops.DepBlocks},
		},
	})
	if !errors.Is(err, publicops.ErrDependencyCycle) {
		t.Fatalf("skip-probe cycle error = %v, want ErrDependencyCycle from the final gate", err)
	}
	assertDependencyEditorNoEdgesFrom(t, ctx, fixture, a, b, c)
}

// RunDependencyEditorRemoveIsIdempotent pins
// RemoveDependencyResult.Removed (dependencyeditor.go:97-101, :180-181): a
// missing edge is Removed false with a NIL error, not ErrNotFound, because an
// agent replaying its own teardown should not have to classify an error to
// discover it already ran.
func RunDependencyEditorRemoveIsIdempotent(t *testing.T, ctx context.Context, fixture DependencyEditorFixture) {
	t.Helper()
	source := fixture.IssuePrefix + "-rm-source"
	target := fixture.IssuePrefix + "-rm-target"
	seedDependencyEditorIssue(t, ctx, fixture, source)
	seedDependencyEditorIssue(t, ctx, fixture, target)

	if _, err := fixture.Editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
		Actor: "writer",
		Edges: []publicops.DependencyEdge{{IssueID: source, DependsOnID: target, Type: publicops.DepBlocks}},
	}); err != nil {
		t.Fatalf("seed the edge to remove: %v", err)
	}

	request := publicops.RemoveDependencyRequest{Actor: "writer", IssueID: source, DependsOnID: target}
	removed, err := fixture.Editor.RemoveDependency(ctx, request)
	if err != nil {
		t.Fatalf("RemoveDependency: %v", err)
	}
	if !removed.Removed {
		t.Fatal("Removed = false, want true for an edge that existed")
	}
	assertDependencyEditorOutgoingCount(t, ctx, fixture, "dependencies", source, 0)
	assertDependencyEditorEventCount(t, ctx, fixture, "events", source, types.EventDependencyRemoved, 1)

	removed, err = fixture.Editor.RemoveDependency(ctx, request)
	if err != nil {
		t.Fatalf("replayed RemoveDependency error = %v, want nil: a missing edge is a success", err)
	}
	if removed.Removed {
		t.Error("Removed = true, want false for an edge that was already gone")
	}
	assertDependencyEditorEventCount(t, ctx, fixture, "events", source, types.EventDependencyRemoved, 1)
}

// RunDependencyEditorAppliesParentChildBeforeBlockingEdges pins
// dependencyeditor.go:61-64: edges are applied parent-child first REGARDLESS of
// request order, so the complete planned hierarchy is visible before any
// blocking edge is validated against it.
//
// The ordering is what decides the ANSWER. Applied hierarchy-first, the
// grandparent is an ancestor of the child by the time `child blocks grand` is
// checked, so the request is refused with the hierarchy sentinel and nothing is
// written. Applied in request order, that blocking edge is validated against a
// hierarchy that does not exist yet — a backend doing that answers something
// else, or nothing at all.
func RunDependencyEditorAppliesParentChildBeforeBlockingEdges(t *testing.T, ctx context.Context, fixture DependencyEditorFixture) {
	t.Helper()
	grand := fixture.IssuePrefix + "-pcfirst-grand"
	parent := fixture.IssuePrefix + "-pcfirst-parent"
	child := fixture.IssuePrefix + "-pcfirst-child"
	for _, id := range []string{grand, parent, child} {
		seedDependencyEditorIssue(t, ctx, fixture, id)
	}

	_, err := fixture.Editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
		Actor: "writer",
		Edges: []publicops.DependencyEdge{
			{IssueID: child, DependsOnID: grand, Type: publicops.DepBlocks},
			{IssueID: child, DependsOnID: parent, Type: publicops.DepParentChild},
			{IssueID: parent, DependsOnID: grand, Type: publicops.DepParentChild},
		},
	})
	var conflict *publicops.DependencyHierarchyConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("error = %v, want *DependencyHierarchyConflictError: the hierarchy this request creates is applied first", err)
	}
	if !conflict.BlockerIsAncestor {
		t.Errorf("conflict = %#v, want the blocker reported as the child's ancestor", conflict)
	}
	assertDependencyEditorNoEdgesFrom(t, ctx, fixture, child, parent)
}

// RunDependencyEditorAcceptsAnExternalTarget pins the first half of
// dependencyeditor.go:150-156: a target's existence is checked only where the
// backend can see it, and an "external:" reference is not something this
// database holds, so there is no unknown-target refusal to make.
func RunDependencyEditorAcceptsAnExternalTarget(t *testing.T, ctx context.Context, fixture DependencyEditorFixture) {
	t.Helper()
	source := fixture.IssuePrefix + "-ext-source"
	const target = "external:https://example.invalid/tracker/17"
	seedDependencyEditorIssue(t, ctx, fixture, source)

	if _, err := fixture.Editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
		Actor: "writer",
		Edges: []publicops.DependencyEdge{{IssueID: source, DependsOnID: target, Type: publicops.DepBlocks}},
	}); err != nil {
		t.Fatalf("AddDependencies onto an external: target: %v", err)
	}
	assertDependencyEdgeTypedCount(t, ctx, fixture, "dependencies", source, target, string(publicops.DepBlocks), 1)
}

// RunDependencyEditorAcceptsAForeignRepoTarget pins the second half of
// dependencyeditor.go:150-156: an issue in ANOTHER REPOSITORY is equally not
// something this database holds.
//
// It is its own case rather than a second assertion in the external one
// because the two reach the target column by different routes — an "external:"
// prefix is a string test, a foreign repository is an id-prefix comparison
// against the source — so a backend can hold one promise and break the other.
// This is the promise the fk_dep_issue_target foreign key on
// depends_on_issue_id (internal/storage/schema/cli_migrations.go:141,155,168)
// turns into a write failure whenever a backend classifies the target as a
// local issue.
func RunDependencyEditorAcceptsAForeignRepoTarget(t *testing.T, ctx context.Context, fixture DependencyEditorFixture) {
	t.Helper()
	source := fixture.IssuePrefix + "-foreign-source"
	const target = "otherrig-9001"
	seedDependencyEditorIssue(t, ctx, fixture, source)

	if _, err := fixture.Editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
		Actor: "writer",
		Edges: []publicops.DependencyEdge{{IssueID: source, DependsOnID: target, Type: publicops.DepBlocks}},
	}); err != nil {
		t.Fatalf("AddDependencies onto a target in another repository: %v", err)
	}
	assertDependencyEdgeTypedCount(t, ctx, fixture, "dependencies", source, target, string(publicops.DepBlocks), 1)
}

// RunDependencyEditorRefusesBlockingEdgeAcrossItsOwnHierarchy pins
// dependencyeditor.go:146-148: a blocking edge that would gate an issue on its
// own ancestor or descendant is *DependencyHierarchyConflictError.
//
// Both directions are asserted because they are two construction sites of the
// same typed sentinel, and the flag that distinguishes them is part of the
// value a caller reads.
func RunDependencyEditorRefusesBlockingEdgeAcrossItsOwnHierarchy(t *testing.T, ctx context.Context, fixture DependencyEditorFixture) {
	t.Helper()
	grand := fixture.IssuePrefix + "-hier-grand"
	parent := fixture.IssuePrefix + "-hier-parent"
	child := fixture.IssuePrefix + "-hier-child"
	for _, id := range []string{grand, parent, child} {
		seedDependencyEditorIssue(t, ctx, fixture, id)
	}
	if _, err := fixture.Editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
		Actor: "writer",
		Edges: []publicops.DependencyEdge{
			{IssueID: parent, DependsOnID: grand, Type: publicops.DepParentChild},
			{IssueID: child, DependsOnID: parent, Type: publicops.DepParentChild},
		},
	}); err != nil {
		t.Fatalf("seed the hierarchy: %v", err)
	}

	_, err := fixture.Editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
		Actor: "writer",
		Edges: []publicops.DependencyEdge{{IssueID: child, DependsOnID: grand, Type: publicops.DepBlocks}},
	})
	var ancestor *publicops.DependencyHierarchyConflictError
	if !errors.As(err, &ancestor) {
		t.Fatalf("gating a child on its ancestor: error = %v, want *DependencyHierarchyConflictError", err)
	}
	if !ancestor.BlockerIsAncestor {
		t.Errorf("conflict = %#v, want BlockerIsAncestor true", ancestor)
	}
	assertDependencyEdgeTypedCount(t, ctx, fixture, "dependencies", child, grand, string(publicops.DepBlocks), 0)

	_, err = fixture.Editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
		Actor: "writer",
		Edges: []publicops.DependencyEdge{{IssueID: grand, DependsOnID: child, Type: publicops.DepBlocks}},
	})
	var descendant *publicops.DependencyHierarchyConflictError
	if !errors.As(err, &descendant) {
		t.Fatalf("gating an ancestor on its descendant: error = %v, want *DependencyHierarchyConflictError", err)
	}
	if descendant.BlockerIsAncestor {
		t.Errorf("conflict = %#v, want BlockerIsAncestor false: the blocker is the DESCENDANT here", descendant)
	}
	assertDependencyEdgeTypedCount(t, ctx, fixture, "dependencies", grand, child, string(publicops.DepBlocks), 0)
}

// RunDependencyEditorRefusesSelfDependencyWithTheProbeSkipped pins
// dependencyeditor.go:66-70: SkipPerEdgeCycleCheck never drops the
// self-dependency refusal. An edge that points an issue at itself is refused
// with ErrSelfDependency with or without the flag, and for every type — a
// non-scheduling self-edge is never seen by a cycle probe at all, so the flag
// is not what was refusing it.
func RunDependencyEditorRefusesSelfDependencyWithTheProbeSkipped(t *testing.T, ctx context.Context, fixture DependencyEditorFixture) {
	t.Helper()
	source := fixture.IssuePrefix + "-self-source"
	seedDependencyEditorIssue(t, ctx, fixture, source)

	for _, probe := range []struct {
		name    string
		skip    bool
		depType publicops.DependencyType
	}{
		{"blocking edge, probe on", false, publicops.DepBlocks},
		{"blocking edge, probe skipped", true, publicops.DepBlocks},
		{"non-scheduling edge, probe skipped", true, publicops.DepRelated},
	} {
		_, err := fixture.Editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
			Actor:                 "writer",
			SkipPerEdgeCycleCheck: probe.skip,
			Edges:                 []publicops.DependencyEdge{{IssueID: source, DependsOnID: source, Type: probe.depType}},
		})
		if !errors.Is(err, publicops.ErrSelfDependency) {
			t.Errorf("%s: error = %v, want ErrSelfDependency", probe.name, err)
		}
	}
	assertDependencyEditorNoEdgesFrom(t, ctx, fixture, source)
}

// RunDependencyEditorRecordsOneHistoryEntryPerLandedRequest pins
// dependencyeditor.go:142-143 and :100-101: an assertion is ONE durable act
// with one history entry however many edges it wrote, and a removal that found
// nothing writes nothing and records no entry.
//
// The counts are deltas taken around each call rather than absolutes, because
// seeding versions its own writes.
func RunDependencyEditorRecordsOneHistoryEntryPerLandedRequest(t *testing.T, ctx context.Context, fixture DependencyEditorFixture) {
	t.Helper()
	if fixture.CountHistory == nil {
		t.Skip("fixture.CountHistory is nil: this backend cannot observe history, so the one-entry-per-request clause cannot be checked here")
	}
	a := fixture.IssuePrefix + "-hist-a"
	b := fixture.IssuePrefix + "-hist-b"
	c := fixture.IssuePrefix + "-hist-c"
	for _, id := range []string{a, b, c} {
		seedDependencyEditorIssue(t, ctx, fixture, id)
	}

	before := dependencyEditorHistoryCount(t, ctx, fixture)
	if _, err := fixture.Editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
		Actor: "writer",
		Edges: []publicops.DependencyEdge{
			{IssueID: a, DependsOnID: b, Type: publicops.DepBlocks},
			{IssueID: a, DependsOnID: c, Type: publicops.DepRelated},
		},
	}); err != nil {
		t.Fatalf("AddDependencies: %v", err)
	}
	assertDependencyEditorHistoryDelta(t, ctx, fixture, before, 1, "two edges are ONE history entry")

	before = dependencyEditorHistoryCount(t, ctx, fixture)
	if _, err := fixture.Editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
		Actor: "writer",
		Edges: []publicops.DependencyEdge{{IssueID: b, DependsOnID: a, Type: publicops.DepBlocks}},
	}); !errors.Is(err, publicops.ErrDependencyCycle) {
		t.Fatalf("cycle error = %v, want ErrDependencyCycle", err)
	}
	assertDependencyEditorHistoryDelta(t, ctx, fixture, before, 0, "a refused request records no history")

	before = dependencyEditorHistoryCount(t, ctx, fixture)
	removed, err := fixture.Editor.RemoveDependency(ctx, publicops.RemoveDependencyRequest{
		Actor: "writer", IssueID: a, DependsOnID: b,
	})
	if err != nil || !removed.Removed {
		t.Fatalf("RemoveDependency = %#v, %v; want Removed true", removed, err)
	}
	assertDependencyEditorHistoryDelta(t, ctx, fixture, before, 1, "a removal that found its edge is one history entry")

	before = dependencyEditorHistoryCount(t, ctx, fixture)
	removed, err = fixture.Editor.RemoveDependency(ctx, publicops.RemoveDependencyRequest{
		Actor: "writer", IssueID: a, DependsOnID: b,
	})
	if err != nil || removed.Removed {
		t.Fatalf("replayed RemoveDependency = %#v, %v; want Removed false and a nil error", removed, err)
	}
	assertDependencyEditorHistoryDelta(t, ctx, fixture, before, 0, "a removal that found nothing records no history")
}

// RunDependencyEditorRecordsNoHistoryForAnAllEphemeralRequest pins
// dependencyeditor.go:122-123: only the durable half of the graph is versioned,
// so a request made entirely of ephemeral edges records no history entry. An
// edge is ephemeral when its SOURCE is, independently of the target's plane,
// which is why one of the two edges here points at a durable issue.
func RunDependencyEditorRecordsNoHistoryForAnAllEphemeralRequest(t *testing.T, ctx context.Context, fixture DependencyEditorFixture) {
	t.Helper()
	if fixture.CountHistory == nil {
		t.Skip("fixture.CountHistory is nil: this backend cannot observe history, so the no-entry-for-ephemeral-edges clause cannot be checked here")
	}
	wisp := fixture.IssuePrefix + "-ephhist-wisp"
	otherWisp := fixture.IssuePrefix + "-ephhist-wisp2"
	issue := fixture.IssuePrefix + "-ephhist-issue"
	seedDependencyEditorWisp(t, ctx, fixture, wisp)
	seedDependencyEditorWisp(t, ctx, fixture, otherWisp)
	seedDependencyEditorIssue(t, ctx, fixture, issue)

	before := dependencyEditorHistoryCount(t, ctx, fixture)
	if _, err := fixture.Editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
		Actor: "writer",
		Edges: []publicops.DependencyEdge{
			{IssueID: wisp, DependsOnID: issue, Type: publicops.DepBlocks},
			{IssueID: otherWisp, DependsOnID: wisp, Type: publicops.DepRelated},
		},
	}); err != nil {
		t.Fatalf("AddDependencies on an all-ephemeral request: %v", err)
	}
	assertDependencyEdgeTypedCount(t, ctx, fixture, "wisp_dependencies", wisp, issue, string(publicops.DepBlocks), 1)
	assertDependencyEdgeTypedCount(t, ctx, fixture, "wisp_dependencies", otherWisp, wisp, string(publicops.DepRelated), 1)
	assertDependencyEditorHistoryDelta(t, ctx, fixture, before, 0,
		"the wisp plane is not versioned, so a request made entirely of ephemeral edges records no history entry")
}

// RunDependencyEditorSnapshotsTheRequest pins dependencyeditor.go:137-138:
// implementations never mutate caller-owned request values and snapshot the
// request at method entry.
//
// The observable half of that is the result. Added is documented as echoing
// the request, and an implementation that echoed it by handing the caller's own
// slice back would satisfy every equality check in this file while quietly
// aliasing state the caller still owns — so the caller mutates its slice after
// the call and the result must not move.
func RunDependencyEditorSnapshotsTheRequest(t *testing.T, ctx context.Context, fixture DependencyEditorFixture) {
	t.Helper()
	source := fixture.IssuePrefix + "-snap-source"
	target := fixture.IssuePrefix + "-snap-target"
	seedDependencyEditorIssue(t, ctx, fixture, source)
	seedDependencyEditorIssue(t, ctx, fixture, target)

	edges := []publicops.DependencyEdge{{IssueID: source, DependsOnID: target, Type: publicops.DepBlocks}}
	request := publicops.AddDependenciesRequest{Actor: "writer", Edges: edges}
	result, err := fixture.Editor.AddDependencies(ctx, request)
	if err != nil {
		t.Fatalf("AddDependencies: %v", err)
	}
	if edges[0] != (publicops.DependencyEdge{IssueID: source, DependsOnID: target, Type: publicops.DepBlocks}) {
		t.Fatalf("the caller's edge = %#v after the call, want it untouched", edges[0])
	}

	edges[0].DependsOnID = "rewritten-by-the-caller"
	if len(result.Added) != 1 || result.Added[0].DependsOnID != target {
		t.Fatalf("Added = %#v after the caller rewrote its own slice, want the edge it asked for: the result must not alias the request", result.Added)
	}
}

// RunDependencyEditorValidationRefusalsWriteNothing pins
// dependencyeditor.go:139-140: deterministic request-validation failures match
// ErrValidation and leave persistent state unchanged.
//
// The sentinel itself is pinned against the shared Validate* functions in a
// unit test; what only a live backend can show is the second half — that the
// refusal happened before anything was written, including for the edges of a
// batch that preceded the invalid one.
func RunDependencyEditorValidationRefusalsWriteNothing(t *testing.T, ctx context.Context, fixture DependencyEditorFixture) {
	t.Helper()
	source := fixture.IssuePrefix + "-valid-source"
	target := fixture.IssuePrefix + "-valid-target"
	seedDependencyEditorIssue(t, ctx, fixture, source)
	seedDependencyEditorIssue(t, ctx, fixture, target)

	valid := publicops.DependencyEdge{IssueID: source, DependsOnID: target, Type: publicops.DepBlocks}
	for _, refusal := range []struct {
		name    string
		request publicops.AddDependenciesRequest
	}{
		{"no actor", publicops.AddDependenciesRequest{Edges: []publicops.DependencyEdge{valid}}},
		{"no edges", publicops.AddDependenciesRequest{Actor: "writer"}},
		{"an edge with no type", publicops.AddDependenciesRequest{Actor: "writer", Edges: []publicops.DependencyEdge{
			valid,
			{IssueID: source, DependsOnID: target + "-2"},
		}}},
	} {
		if _, err := fixture.Editor.AddDependencies(ctx, refusal.request); !errors.Is(err, publicops.ErrValidation) {
			t.Errorf("AddDependencies with %s: error = %v, want ErrValidation", refusal.name, err)
		}
	}
	if _, err := fixture.Editor.RemoveDependency(ctx, publicops.RemoveDependencyRequest{
		IssueID: source, DependsOnID: target,
	}); !errors.Is(err, publicops.ErrValidation) {
		t.Errorf("RemoveDependency with no actor: error = %v, want ErrValidation", err)
	}
	assertDependencyEditorNoEdgesFrom(t, ctx, fixture, source)
}

// RunDependencyEditorRoutesWispSourcedRemovalToTheWispPlane pins the REMOVE
// half of dependencyeditor.go:117-123. An edge follows its source on BOTH
// methods, so removing a wisp-sourced edge deletes from wisp_dependencies,
// leaves the durable pair untouched, and records no history entry — only the
// durable half is versioned.
//
// The add side of that clause got four cases because routing is what the three
// implementations had actually diverged on. The remove side had none at this
// seam, and it is a genuinely separate decision in the code: the add pins
// routing from a source set read once for the whole request, a removal reads it
// per call.
//
// The wisp_events assertions are contractual now rather than a probe: the leaf
// promises the entry follows the source's plane, and says why that matters
// most for an ephemeral edge — the wisp plane is not versioned, so the entry is
// the only record the operation leaves.
func RunDependencyEditorRoutesWispSourcedRemovalToTheWispPlane(t *testing.T, ctx context.Context, fixture DependencyEditorFixture) {
	t.Helper()
	wisp := fixture.IssuePrefix + "-wrm-wisp"
	target := fixture.IssuePrefix + "-wrm-target"
	seedDependencyEditorWisp(t, ctx, fixture, wisp)
	seedDependencyEditorIssue(t, ctx, fixture, target)

	if _, err := fixture.Editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
		Actor: "writer",
		Edges: []publicops.DependencyEdge{{IssueID: wisp, DependsOnID: target, Type: publicops.DepBlocks}},
	}); err != nil {
		t.Fatalf("seed the wisp-sourced edge to remove: %v", err)
	}
	assertDependencyEdgeCount(t, ctx, fixture, "wisp_dependencies", wisp, target, 1)

	request := publicops.RemoveDependencyRequest{Actor: "remover", IssueID: wisp, DependsOnID: target}
	assertHistoryDelta := dependencyEditorHistoryProbe(t, ctx, fixture)
	removed, err := fixture.Editor.RemoveDependency(ctx, request)
	if err != nil {
		t.Fatalf("RemoveDependency on a wisp-sourced edge: %v", err)
	}
	if !removed.Removed {
		t.Error("Removed = false, want true: the edge was there, in the plane its SOURCE lives in")
	}
	assertDependencyEditorOutgoingCount(t, ctx, fixture, "wisp_dependencies", wisp, 0)
	assertDependencyEditorOutgoingCount(t, ctx, fixture, "dependencies", wisp, 0)
	assertDependencyEditorEventCount(t, ctx, fixture, "wisp_events", wisp, types.EventDependencyRemoved, 1)
	assertDependencyEditorEventCount(t, ctx, fixture, "events", wisp, types.EventDependencyRemoved, 0)
	assertHistoryDelta(0, "the wisp plane is not versioned, so removing an ephemeral edge records no history entry")

	assertHistoryDelta = dependencyEditorHistoryProbe(t, ctx, fixture)
	removed, err = fixture.Editor.RemoveDependency(ctx, request)
	if err != nil {
		t.Fatalf("replayed RemoveDependency on a wisp-sourced edge = %v, want nil: a missing edge is a success in either plane", err)
	}
	if removed.Removed {
		t.Error("Removed = true, want false for a wisp-sourced edge that was already gone")
	}
	assertDependencyEditorEventCount(t, ctx, fixture, "wisp_events", wisp, types.EventDependencyRemoved, 1)
	assertHistoryDelta(0, "a removal that found nothing records no history in either plane")
}

// RunDependencyEditorRefusesAGhostSource pins the fifth refusal in
// dependencyeditor.go:143-148 — "a SOURCE that does not exist" — together with
// its all-or-nothing consequence: the edge of the same request that was already
// written comes back out.
//
// The ghost id is a strict PREFIX of a seeded id rather than a random unknown
// one, so the case doubles as the pin for dependencyeditor.go:34-37: both
// endpoints are exact canonical ids and there is no prefix resolution on this
// contract. A backend that resolved it would land the edge under the seeded id
// instead of refusing, and the ordinary unknown-id spelling could never tell.
//
// This refusal is checked ahead of the target's (dependencies.go:214-235), so
// the target here is deliberately a real seeded issue: the refusal under test
// is about the source alone.
//
// SPEC-GAP bd-yby99.9: the doc now states that the refusal HAPPENS and states
// that it deliberately names no identity yet, so err != nil is still the whole
// assertion — pinning a sentinel or a message here would assert more than the
// leaf says, and the leaf says the anonymity is a gap rather than a promise.
func RunDependencyEditorRefusesAGhostSource(t *testing.T, ctx context.Context, fixture DependencyEditorFixture) {
	t.Helper()
	source := fixture.IssuePrefix + "-ghostsrc-known"
	target := fixture.IssuePrefix + "-ghostsrc-target"
	seedDependencyEditorIssue(t, ctx, fixture, source)
	seedDependencyEditorIssue(t, ctx, fixture, target)
	ghost := source[:len(source)-1]

	if _, err := fixture.Editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
		Actor: "writer",
		Edges: []publicops.DependencyEdge{
			{IssueID: source, DependsOnID: target, Type: publicops.DepBlocks},
			{IssueID: ghost, DependsOnID: target, Type: publicops.DepBlocks},
		},
	}); err == nil {
		t.Fatalf("AddDependencies from the nonexistent source %q = nil error, want a refusal: %q exists but is a different id", ghost, source)
	}
	assertDependencyEditorNoEdgesFrom(t, ctx, fixture, source, ghost)
}

// RunDependencyEditorRefusesAMissingLocalTarget pins the refusal half of the
// target-existence clause. Existence is checked "only where the backend can
// see it", and the two acceptance cases above pin what that therefore does NOT
// refuse — an "external:" reference and an issue in another repository. The
// leaf now states the half neither of them covers outright: a target whose
// absence the backend CAN see — same prefix, no "external:" marker, no row —
// is refused and nothing is written. Without it the clause reads as a blanket
// amnesty, which is the opposite of what it says.
//
// SPEC-GAP bd-yby99.9: the refusal's IDENTITY is still unnamed, and the leaf
// now says so deliberately, so err != nil is all this asserts.
func RunDependencyEditorRefusesAMissingLocalTarget(t *testing.T, ctx context.Context, fixture DependencyEditorFixture) {
	t.Helper()
	source := fixture.IssuePrefix + "-notgt-source"
	seedDependencyEditorIssue(t, ctx, fixture, source)
	// Same prefix as the source, so it is neither an external reference nor
	// another repository's id: this database is exactly where it would be.
	missing := fixture.IssuePrefix + "-notgt-ghost"

	if _, err := fixture.Editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
		Actor: "writer",
		Edges: []publicops.DependencyEdge{{IssueID: source, DependsOnID: missing, Type: publicops.DepBlocks}},
	}); err == nil {
		t.Fatalf("AddDependencies onto the missing local target %q = nil error, want a refusal", missing)
	}
	assertDependencyEditorNoEdgesFrom(t, ctx, fixture, source)
}

// dependencyEditorUnlistedType is a DependencyType deliberately absent from the
// Dep* constants: a plausible workspace-configured value, short enough for the
// column, and outside the scheduling set, so nothing but the open-set rule
// decides whether it is accepted.
const dependencyEditorUnlistedType publicops.DependencyType = "caused-by"

// RunDependencyEditorAcceptsATypeOutsideTheConstants pins the open-set rule
// (dependencyeditor.go:13-19, :41-44) END TO END. An implementation that
// refused a type absent from the constants "would break every workspace that
// spelled one of its own", and the constants "never authorize" a value — so
// acceptance cannot be a property of the listed six.
//
// The validator half is owned by a unit test against
// ValidateAddDependenciesRequest. What only a live backend can show is the rest
// of the write path: the scheduling-edge switch that reads the type, the target
// classification, the type column itself, and the existing-edge lookup that has
// to recognize the unlisted value as the SAME type on a re-add rather than
// raising a conflict against something it does not know.
func RunDependencyEditorAcceptsATypeOutsideTheConstants(t *testing.T, ctx context.Context, fixture DependencyEditorFixture) {
	t.Helper()
	source := fixture.IssuePrefix + "-opentype-source"
	target := fixture.IssuePrefix + "-opentype-target"
	seedDependencyEditorIssue(t, ctx, fixture, source)
	seedDependencyEditorIssue(t, ctx, fixture, target)

	request := publicops.AddDependenciesRequest{
		Actor: "writer",
		Edges: []publicops.DependencyEdge{{IssueID: source, DependsOnID: target, Type: dependencyEditorUnlistedType}},
	}
	result, err := fixture.Editor.AddDependencies(ctx, request)
	if err != nil {
		t.Fatalf("AddDependencies with the unlisted type %q: %v — the vocabulary is an open set", dependencyEditorUnlistedType, err)
	}
	if len(result.Added) != 1 || result.Added[0].Type != dependencyEditorUnlistedType {
		t.Errorf("Added = %#v, want the edge as spelled: the result must not normalize a type to a listed one", result.Added)
	}
	assertDependencyEdgeTypedCount(t, ctx, fixture, "dependencies", source, target, string(dependencyEditorUnlistedType), 1)

	if _, err := fixture.Editor.AddDependencies(ctx, request); err != nil {
		t.Fatalf("re-adding the unlisted-type edge refused: %v — same type is idempotent whatever the type is", err)
	}
	assertDependencyEditorOutgoingCount(t, ctx, fixture, "dependencies", source, 1)
	assertDependencyEdgeTypedCount(t, ctx, fixture, "dependencies", source, target, string(dependencyEditorUnlistedType), 1)
}

// RunDependencyEditorRemovesOnlyTheNamedEdge pins "removes exactly the named
// edge" (dependencyeditor.go:180) as a statement about the edges it does NOT
// touch: a sibling edge from the same source survives a removal aimed at its
// neighbor.
//
// The edge removed is the "external:" one, which makes this the REMOVAL half of
// :136-139 as well. A target the backend cannot resolve is still a target a
// caller may name, and it lives in a different column from an ordinary one —
// so an implementation that matched removals against depends_on_issue_id alone
// reports Removed false here and leaves the edge in place.
func RunDependencyEditorRemovesOnlyTheNamedEdge(t *testing.T, ctx context.Context, fixture DependencyEditorFixture) {
	t.Helper()
	source := fixture.IssuePrefix + "-rmnamed-source"
	sibling := fixture.IssuePrefix + "-rmnamed-sibling"
	const external = "external:https://example.invalid/tracker/44"
	seedDependencyEditorIssue(t, ctx, fixture, source)
	seedDependencyEditorIssue(t, ctx, fixture, sibling)

	if _, err := fixture.Editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
		Actor: "writer",
		Edges: []publicops.DependencyEdge{
			{IssueID: source, DependsOnID: sibling, Type: publicops.DepBlocks},
			{IssueID: source, DependsOnID: external, Type: publicops.DepRelated},
		},
	}); err != nil {
		t.Fatalf("seed the two edges from one source: %v", err)
	}
	assertDependencyEditorOutgoingCount(t, ctx, fixture, "dependencies", source, 2)

	removed, err := fixture.Editor.RemoveDependency(ctx, publicops.RemoveDependencyRequest{
		Actor: "remover", IssueID: source, DependsOnID: external,
	})
	if err != nil {
		t.Fatalf("RemoveDependency naming the external target: %v", err)
	}
	if !removed.Removed {
		t.Error("Removed = false, want true: an external target is still an edge a caller can name")
	}
	assertDependencyEdgeTypedCount(t, ctx, fixture, "dependencies", source, external, string(publicops.DepRelated), 0)
	assertDependencyEdgeTypedCount(t, ctx, fixture, "dependencies", source, sibling, string(publicops.DepBlocks), 1)
	assertDependencyEditorOutgoingCount(t, ctx, fixture, "dependencies", source, 1)
}

// RunDependencyEditorSkipPerEdgeCycleCheckDropsOnlyTheProbe pins the rest of
// dependencyeditor.go:66-71. Two existing cases show what the flag does NOT
// drop — the whole-graph gate and the self-dependency refusal — but both are
// refusals, and so is every other skip=true request in this file. Nothing yet
// shows the flag doing the thing it exists for: a caller wiring a large acyclic
// graph gets the graph, and gets it persisted.
//
// The second half is that the other typed refusals still fire under it. A
// backend that read the flag as "skip validation" rather than "skip the
// per-edge cycle probe" passes every existing case in this file and fails this
// one.
func RunDependencyEditorSkipPerEdgeCycleCheckDropsOnlyTheProbe(t *testing.T, ctx context.Context, fixture DependencyEditorFixture) {
	t.Helper()
	a := fixture.IssuePrefix + "-skipok-a"
	b := fixture.IssuePrefix + "-skipok-b"
	c := fixture.IssuePrefix + "-skipok-c"
	for _, id := range []string{a, b, c} {
		seedDependencyEditorIssue(t, ctx, fixture, id)
	}

	result, err := fixture.Editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
		Actor:                 "writer",
		SkipPerEdgeCycleCheck: true,
		Edges: []publicops.DependencyEdge{
			{IssueID: a, DependsOnID: b, Type: publicops.DepBlocks},
			{IssueID: b, DependsOnID: c, Type: publicops.DepBlocks},
		},
	})
	if err != nil {
		t.Fatalf("AddDependencies on an acyclic graph with the probe skipped: %v", err)
	}
	if len(result.Added) != 2 {
		t.Errorf("Added = %#v, want both edges", result.Added)
	}
	assertDependencyEdgeTypedCount(t, ctx, fixture, "dependencies", a, b, string(publicops.DepBlocks), 1)
	assertDependencyEdgeTypedCount(t, ctx, fixture, "dependencies", b, c, string(publicops.DepBlocks), 1)

	_, err = fixture.Editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
		Actor:                 "writer",
		SkipPerEdgeCycleCheck: true,
		Edges:                 []publicops.DependencyEdge{{IssueID: a, DependsOnID: b, Type: publicops.DepRelated}},
	})
	var conflict *publicops.DependencyTypeConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("retype with the probe skipped: error = %v, want *DependencyTypeConflictError: the flag drops the cycle probe, not the typed refusals", err)
	}
	assertDependencyEdgeTypedCount(t, ctx, fixture, "dependencies", a, b, string(publicops.DepBlocks), 1)
	assertDependencyEditorOutgoingCount(t, ctx, fixture, "dependencies", a, 1)
}

// RunDependencyEditorRecordsOneHistoryEntryForAMixedPlaneRequest interpolates
// between the two endpoints the file already pins: an all-durable request is
// one entry (RunDependencyEditorRecordsOneHistoryEntryPerLandedRequest) and an
// all-ephemeral one is none
// (RunDependencyEditorRecordsNoHistoryForAnAllEphemeralRequest). A request that
// mixes the planes is still ONE transaction (dependencyeditor.go:121) of which
// only the durable half is versioned (:117-118) — so one entry. Not two, which
// is what a backend versioning per plane rather than per request would record,
// and not zero, which is what a backend deciding "this request touches the
// ephemeral plane" once for the whole request would record.
func RunDependencyEditorRecordsOneHistoryEntryForAMixedPlaneRequest(t *testing.T, ctx context.Context, fixture DependencyEditorFixture) {
	t.Helper()
	if fixture.CountHistory == nil {
		t.Skip("fixture.CountHistory is nil: this backend cannot observe history, so the mixed-plane entry count cannot be checked here")
	}
	wisp := fixture.IssuePrefix + "-mixhist-wisp"
	issue := fixture.IssuePrefix + "-mixhist-issue"
	target := fixture.IssuePrefix + "-mixhist-target"
	seedDependencyEditorWisp(t, ctx, fixture, wisp)
	seedDependencyEditorIssue(t, ctx, fixture, issue)
	seedDependencyEditorIssue(t, ctx, fixture, target)

	before := dependencyEditorHistoryCount(t, ctx, fixture)
	if _, err := fixture.Editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
		Actor: "writer",
		Edges: []publicops.DependencyEdge{
			{IssueID: wisp, DependsOnID: target, Type: publicops.DepBlocks},
			{IssueID: issue, DependsOnID: target, Type: publicops.DepBlocks},
		},
	}); err != nil {
		t.Fatalf("AddDependencies on a mixed-plane request: %v", err)
	}
	assertDependencyEdgeCount(t, ctx, fixture, "wisp_dependencies", wisp, target, 1)
	assertDependencyEdgeCount(t, ctx, fixture, "dependencies", issue, target, 1)
	assertDependencyEditorHistoryDelta(t, ctx, fixture, before, 1,
		"a request spanning both planes is one transaction with one versioned half, so it is ONE history entry")
}

// RunDependencyEditorWritesTheTargetIntoItsTypedColumn pins the target
// CLASSIFICATION (issueops.ClassifyDepTarget) that dependencyeditor.go:150-156
// only describes from the outside. A dependency row holds its target in one of
// three typed columns — depends_on_issue_id, depends_on_wisp_id,
// depends_on_external — and which one it is is not cosmetic: only
// depends_on_issue_id carries fk_dep_issue_target into issues(id)
// (internal/storage/schema/cli_migrations.go:141,155,168), so a wisp or a
// foreign id filed there is a write that fails, and a local issue filed
// anywhere else is an edge no foreign key protects from a later delete.
//
// EVERY OTHER CASE IN THIS FILE IS BLIND TO THIS. They read the target through
// COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external), which
// resolves to the same id whichever column holds it — so a body that filed a
// wisp target as external passes all of them and fails only here.
//
// Both source planes are exercised because routing the SOURCE (which table the
// row lands in) and classifying the TARGET (which column inside it) are
// independent decisions, and a wisp-sourced edge is where they are easiest to
// conflate.
func RunDependencyEditorWritesTheTargetIntoItsTypedColumn(t *testing.T, ctx context.Context, fixture DependencyEditorFixture) {
	t.Helper()
	issue := fixture.IssuePrefix + "-tcol-issue"
	wisp := fixture.IssuePrefix + "-tcol-wisp"
	issueTarget := fixture.IssuePrefix + "-tcol-issuetgt"
	wispTarget := fixture.IssuePrefix + "-tcol-wisptgt"
	const external = "external:https://example.invalid/tracker/61"
	const foreign = "othertcol-9001"
	seedDependencyEditorIssue(t, ctx, fixture, issue)
	seedDependencyEditorIssue(t, ctx, fixture, issueTarget)
	seedDependencyEditorWisp(t, ctx, fixture, wisp)
	seedDependencyEditorWisp(t, ctx, fixture, wispTarget)

	if _, err := fixture.Editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
		Actor: "writer",
		Edges: []publicops.DependencyEdge{
			{IssueID: issue, DependsOnID: issueTarget, Type: publicops.DepBlocks},
			{IssueID: issue, DependsOnID: wispTarget, Type: publicops.DepRelated},
			{IssueID: issue, DependsOnID: external, Type: publicops.DepRelated},
			{IssueID: issue, DependsOnID: foreign, Type: publicops.DepRelated},
			{IssueID: wisp, DependsOnID: wispTarget, Type: publicops.DepBlocks},
			{IssueID: wisp, DependsOnID: issueTarget, Type: publicops.DepRelated},
		},
	}); err != nil {
		t.Fatalf("AddDependencies over the four target classes: %v", err)
	}

	for _, want := range []struct {
		table  string
		source string
		target string
		column string
		why    string
	}{
		{"dependencies", issue, issueTarget, "depends_on_issue_id",
			"a local issue is the one target class the foreign key can hold"},
		{"dependencies", issue, wispTarget, "depends_on_wisp_id",
			"a wisp has no row in issues, so the issue-keyed column would fail its foreign key"},
		{"dependencies", issue, external, "depends_on_external",
			"an external: reference names something outside this database entirely"},
		{"dependencies", issue, foreign, "depends_on_external",
			"another repository's id lives in that rig's database, not this one"},
		{"wisp_dependencies", wisp, wispTarget, "depends_on_wisp_id",
			"the ephemeral plane classifies its targets by the same rule"},
		{"wisp_dependencies", wisp, issueTarget, "depends_on_issue_id",
			"a wisp-sourced edge onto a durable issue still files the target as an issue"},
	} {
		assertDependencyEditorTargetColumn(t, ctx, fixture, want.table, want.source, want.target, want.column, want.why)
	}
}

// RunDependencyEditorRefusesBlockingEdgeAcrossAWispHierarchy is the EPHEMERAL
// half of dependencyeditor.go:146-148.
//
// RunDependencyEditorRefusesBlockingEdgeAcrossItsOwnHierarchy seeds durable
// issues, and the hierarchy walk it exercises reads a UNION of both dependency
// tables. A body that read only the durable one passes that case and fails this
// one, and the deadlock it lets through is real: a wisp gated on its own
// ancestor never becomes ready, because the ancestor inherits the descendant's
// blocked state.
//
// The last arm is the boundary the walk deliberately does not cross. It climbs
// child → parent only, so two children of one wisp parent share a hierarchy
// COMPONENT but not a hierarchy LINE, and an ordering edge between them stays
// legal.
func RunDependencyEditorRefusesBlockingEdgeAcrossAWispHierarchy(t *testing.T, ctx context.Context, fixture DependencyEditorFixture) {
	t.Helper()
	parent := fixture.IssuePrefix + "-whier-parent"
	child := fixture.IssuePrefix + "-whier-child"
	sibling := fixture.IssuePrefix + "-whier-sibling"
	for _, id := range []string{parent, child, sibling} {
		seedDependencyEditorWisp(t, ctx, fixture, id)
	}
	if _, err := fixture.Editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
		Actor: "writer",
		Edges: []publicops.DependencyEdge{
			{IssueID: child, DependsOnID: parent, Type: publicops.DepParentChild},
			{IssueID: sibling, DependsOnID: parent, Type: publicops.DepParentChild},
		},
	}); err != nil {
		t.Fatalf("seed the wisp hierarchy: %v", err)
	}

	_, err := fixture.Editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
		Actor: "writer",
		Edges: []publicops.DependencyEdge{{IssueID: parent, DependsOnID: child, Type: publicops.DepBlocks}},
	})
	var descendant *publicops.DependencyHierarchyConflictError
	if !errors.As(err, &descendant) {
		t.Fatalf("gating a wisp parent on its own child: error = %v, want *DependencyHierarchyConflictError", err)
	}
	if descendant.BlockerIsAncestor {
		t.Errorf("conflict = %#v, want BlockerIsAncestor false: the blocker is the DESCENDANT here", descendant)
	}

	_, err = fixture.Editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
		Actor: "writer",
		// conditional-blocks rather than blocks: the guard covers the whole
		// scheduling set, and this type has no constant in the public sample.
		Edges: []publicops.DependencyEdge{{IssueID: child, DependsOnID: parent, Type: types.DepConditionalBlocks}},
	})
	var ancestor *publicops.DependencyHierarchyConflictError
	if !errors.As(err, &ancestor) {
		t.Fatalf("gating a wisp child on its own parent: error = %v, want *DependencyHierarchyConflictError", err)
	}
	if !ancestor.BlockerIsAncestor {
		t.Errorf("conflict = %#v, want BlockerIsAncestor true", ancestor)
	}
	// The hierarchy refusal beats the type conflict the pair already carries:
	// the pair has a parent-child row, and a body checking the existing edge
	// first would report the wrong reason for the right refusal.
	var conflict *publicops.DependencyTypeConflictError
	if errors.As(err, &conflict) {
		t.Errorf("error = %#v, want the hierarchy conflict: the deadlock is the reason, not the pre-existing row", conflict)
	}

	if _, err := fixture.Editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
		Actor: "writer",
		Edges: []publicops.DependencyEdge{{IssueID: sibling, DependsOnID: child, Type: publicops.DepBlocks}},
	}); err != nil {
		t.Fatalf("ordering two children of one wisp parent: %v — siblings share a component, not a line", err)
	}
	assertDependencyEdgeTypedCount(t, ctx, fixture, "wisp_dependencies", sibling, child, string(publicops.DepBlocks), 1)
}

// RunDependencyEditorRefusesACycleThroughAParentChildHop pins the edge set the
// ADD-TIME gate walks, which is NOT the set DetectCycles walks.
// cycle_detector_contract.go says so from the other side — `parent-child` is
// outside the report's walk, "which the ADD-time gate does walk" — and nothing
// pinned the half that sentence asserts.
//
// It matters because a blocked parent propagates its blocked state to its
// children, so a loop alternating `blocks` and `parent-child` hops is a
// livelock in which nothing ever becomes ready, even though no `blocks` cycle
// exists anywhere in it. Every other cycle case in this file closes a loop made
// of blocking edges only, so a body narrowed to those passes all of them.
//
// Both closing orientations are asserted because they enter the gate
// differently: a blocking closing edge is checked by the per-edge probe on its
// own type, and a parent-child closing edge is one whose OWN type the walk has
// to include to see the loop at all.
//
// The last arm is the false-positive guard, and it is the reason this is one
// case rather than two: a body that treated every parent-child hop as a cycle
// would pass both refusals and refuse the ordinary shape — a chain of tasks
// each gating an epic it does not belong to — that the refusals exist to
// distinguish from.
func RunDependencyEditorRefusesACycleThroughAParentChildHop(t *testing.T, ctx context.Context, fixture DependencyEditorFixture) {
	t.Helper()
	// A closing BLOCKS edge: parentA -> childA -> parentB -> childB -> parentA,
	// alternating parent-child and blocks hops.
	parentA := fixture.IssuePrefix + "-pchop-b-pa"
	parentB := fixture.IssuePrefix + "-pchop-b-pb"
	childA := fixture.IssuePrefix + "-pchop-b-ca"
	childB := fixture.IssuePrefix + "-pchop-b-cb"
	for _, id := range []string{parentA, parentB, childA, childB} {
		seedDependencyEditorIssue(t, ctx, fixture, id)
	}
	if _, err := fixture.Editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
		Actor: "writer",
		Edges: []publicops.DependencyEdge{
			{IssueID: childA, DependsOnID: parentA, Type: publicops.DepParentChild},
			{IssueID: childB, DependsOnID: parentB, Type: publicops.DepParentChild},
			{IssueID: parentB, DependsOnID: childA, Type: publicops.DepBlocks},
		},
	}); err != nil {
		t.Fatalf("seed the combined graph: %v", err)
	}
	_, err := fixture.Editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
		Actor: "writer",
		Edges: []publicops.DependencyEdge{{IssueID: parentA, DependsOnID: childB, Type: publicops.DepBlocks}},
	})
	if !errors.Is(err, publicops.ErrDependencyCycle) {
		t.Fatalf("a blocking edge closing a loop through parent-child hops: error = %v, want ErrDependencyCycle", err)
	}
	assertDependencyEditorNoEdgesFrom(t, ctx, fixture, parentA)

	// The same loop, with the PARENT-CHILD edge as the one that closes it.
	pcParentA := fixture.IssuePrefix + "-pchop-p-pa"
	pcParentB := fixture.IssuePrefix + "-pchop-p-pb"
	pcChildA := fixture.IssuePrefix + "-pchop-p-ca"
	pcChildB := fixture.IssuePrefix + "-pchop-p-cb"
	for _, id := range []string{pcParentA, pcParentB, pcChildA, pcChildB} {
		seedDependencyEditorIssue(t, ctx, fixture, id)
	}
	if _, err := fixture.Editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
		Actor: "writer",
		Edges: []publicops.DependencyEdge{
			{IssueID: pcChildA, DependsOnID: pcParentA, Type: publicops.DepParentChild},
			{IssueID: pcParentB, DependsOnID: pcChildA, Type: publicops.DepBlocks},
			{IssueID: pcParentA, DependsOnID: pcChildB, Type: publicops.DepBlocks},
		},
	}); err != nil {
		t.Fatalf("seed the combined graph for the parent-child closing edge: %v", err)
	}
	_, err = fixture.Editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
		Actor: "writer",
		Edges: []publicops.DependencyEdge{{IssueID: pcChildB, DependsOnID: pcParentB, Type: publicops.DepParentChild}},
	})
	if !errors.Is(err, publicops.ErrDependencyCycle) {
		t.Fatalf("a parent-child edge closing a loop through blocking hops: error = %v, want ErrDependencyCycle", err)
	}
	assertDependencyEditorNoEdgesFrom(t, ctx, fixture, pcChildB)

	// The acyclic shape the two refusals must not also refuse: each child gates
	// an epic it does not belong to, and belongs to the next one along.
	const levels = 4
	chain := make([]publicops.DependencyEdge, 0, 2*levels)
	for i := 0; i < levels; i++ {
		parent := fmt.Sprintf("%s-pchop-chain-p%d", fixture.IssuePrefix, i)
		child := fmt.Sprintf("%s-pchop-chain-c%d", fixture.IssuePrefix, i)
		seedDependencyEditorIssue(t, ctx, fixture, parent)
		seedDependencyEditorIssue(t, ctx, fixture, child)
		chain = append(chain, publicops.DependencyEdge{IssueID: parent, DependsOnID: child, Type: publicops.DepBlocks})
		if i > 0 {
			previous := fmt.Sprintf("%s-pchop-chain-c%d", fixture.IssuePrefix, i-1)
			chain = append(chain, publicops.DependencyEdge{IssueID: previous, DependsOnID: parent, Type: publicops.DepParentChild})
		}
	}
	if _, err := fixture.Editor.AddDependencies(ctx, publicops.AddDependenciesRequest{Actor: "writer", Edges: chain}); err != nil {
		t.Fatalf("an acyclic chain alternating blocking and parent-child hops: %v — the walk must not read every hierarchy as a loop", err)
	}
}

// RunDependencyEditorRefusesASamePlaneEdgeClosingACrossPlaneCycle is the half
// of bd-xe27 that RunDependencyEditorRefusesCrossPlaneCycle cannot reach.
//
// There, the edge under test CROSSES the planes itself, so a gate that decided
// to merge the two tables from the edge in front of it still refuses. Here both
// endpoints of the new edge live in ONE plane and only the INTERIOR of the loop
// leaves it — so the gate has to merge the tables unconditionally, from the
// graph rather than from the edge. That distinction was a real defect: the
// merged two-session check ran only when the closing edge crossed tiers, and a
// same-tier closing edge saw one session and let the cycle commit.
//
// Both orientations are asserted, because "the plane the endpoints share" and
// "the plane the interior visits" swap between them and the two tables are not
// symmetric — only one of them is versioned.
func RunDependencyEditorRefusesASamePlaneEdgeClosingACrossPlaneCycle(t *testing.T, ctx context.Context, fixture DependencyEditorFixture) {
	t.Helper()
	// Durable endpoints, the interior hop through the ephemeral plane:
	// issueA -> issueB (new) with issueB -> wisp -> issueA already stored.
	issueA := fixture.IssuePrefix + "-splane-issue-a"
	issueB := fixture.IssuePrefix + "-splane-issue-b"
	throughWisp := fixture.IssuePrefix + "-splane-via-wisp"
	seedDependencyEditorIssue(t, ctx, fixture, issueA)
	seedDependencyEditorIssue(t, ctx, fixture, issueB)
	seedDependencyEditorWisp(t, ctx, fixture, throughWisp)
	if _, err := fixture.Editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
		Actor: "writer",
		Edges: []publicops.DependencyEdge{
			{IssueID: issueB, DependsOnID: throughWisp, Type: publicops.DepBlocks},
			{IssueID: throughWisp, DependsOnID: issueA, Type: publicops.DepBlocks},
		},
	}); err != nil {
		t.Fatalf("seed the path that leaves the durable plane and comes back: %v", err)
	}
	_, err := fixture.Editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
		Actor: "writer",
		Edges: []publicops.DependencyEdge{{IssueID: issueA, DependsOnID: issueB, Type: publicops.DepBlocks}},
	})
	if !errors.Is(err, publicops.ErrDependencyCycle) {
		t.Fatalf("a durable edge closing a loop whose interior runs through the wisp plane: error = %v, want ErrDependencyCycle", err)
	}
	assertDependencyEditorNoEdgesFrom(t, ctx, fixture, issueA)

	// Ephemeral endpoints, the interior hop through the durable plane.
	wispA := fixture.IssuePrefix + "-splane-wisp-a"
	wispB := fixture.IssuePrefix + "-splane-wisp-b"
	throughIssue := fixture.IssuePrefix + "-splane-via-issue"
	seedDependencyEditorWisp(t, ctx, fixture, wispA)
	seedDependencyEditorWisp(t, ctx, fixture, wispB)
	seedDependencyEditorIssue(t, ctx, fixture, throughIssue)
	if _, err := fixture.Editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
		Actor: "writer",
		Edges: []publicops.DependencyEdge{
			{IssueID: wispB, DependsOnID: throughIssue, Type: publicops.DepBlocks},
			{IssueID: throughIssue, DependsOnID: wispA, Type: publicops.DepBlocks},
		},
	}); err != nil {
		t.Fatalf("seed the path that leaves the ephemeral plane and comes back: %v", err)
	}
	_, err = fixture.Editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
		Actor: "writer",
		Edges: []publicops.DependencyEdge{{IssueID: wispA, DependsOnID: wispB, Type: publicops.DepBlocks}},
	})
	if !errors.Is(err, publicops.ErrDependencyCycle) {
		t.Fatalf("an ephemeral edge closing a loop whose interior runs through the durable plane: error = %v, want ErrDependencyCycle", err)
	}
	assertDependencyEditorNoEdgesFrom(t, ctx, fixture, wispA)
}

func seedDependencyEditorIssue(t *testing.T, ctx context.Context, fixture DependencyEditorFixture, id string) {
	t.Helper()
	if err := fixture.CreateIssue(ctx, dependencyEditorSeed(id, false), "seed"); err != nil {
		t.Fatalf("seed issue %s: %v", id, err)
	}
}

func seedDependencyEditorWisp(t *testing.T, ctx context.Context, fixture DependencyEditorFixture, id string) {
	t.Helper()
	if err := fixture.CreateWisp(ctx, dependencyEditorSeed(id, true), "seed"); err != nil {
		t.Fatalf("seed wisp %s: %v", id, err)
	}
}

func dependencyEditorSeed(id string, ephemeral bool) *types.Issue {
	return &types.Issue{
		ID:        id,
		Title:     id,
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: ephemeral,
	}
}

// assertDependencyEdgeCount counts edges from source to target in one
// dependency table. The target is matched through the resolved target
// expression because a target's own class decides which typed column holds it,
// independently of the source routing under test.
func assertDependencyEdgeCount(t *testing.T, ctx context.Context, fixture DependencyEditorFixture, table, source, target string, want int) {
	t.Helper()
	var got int
	//nolint:gosec // G201: table is one of the contract's two hardcoded names.
	query := "SELECT COUNT(*) FROM " + table +
		" WHERE issue_id = ? AND COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) = ?"
	if err := fixture.QueryScalar(ctx, query, []any{source, target}, &got); err != nil {
		t.Fatalf("count %s edges %s -> %s: %v", table, source, target, err)
	}
	if got != want {
		t.Errorf("%s edges %s -> %s = %d, want %d", table, source, target, got, want)
	}
}

func assertDependencyEdgeTypedCount(t *testing.T, ctx context.Context, fixture DependencyEditorFixture, table, source, target, depType string, want int) {
	t.Helper()
	var got int
	//nolint:gosec // G201: table is one of the contract's two hardcoded names.
	query := "SELECT COUNT(*) FROM " + table +
		" WHERE issue_id = ? AND COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) = ? AND type = ?"
	if err := fixture.QueryScalar(ctx, query, []any{source, target, depType}, &got); err != nil {
		t.Fatalf("count %s %s edges %s -> %s: %v", table, depType, source, target, err)
	}
	if got != want {
		t.Errorf("%s %s edges %s -> %s = %d, want %d", table, depType, source, target, got, want)
	}
}

// assertDependencyEditorTargetColumn checks WHICH typed target column holds an
// edge's target, which assertDependencyEdgeCount's COALESCE deliberately cannot
// see. It asserts the whole triple rather than the wanted column alone: a body
// that wrote the id into two columns would satisfy the positive check and still
// hold a row no reader can classify.
func assertDependencyEditorTargetColumn(t *testing.T, ctx context.Context, fixture DependencyEditorFixture, table, source, target, wantColumn, why string) {
	t.Helper()
	for _, column := range []string{"depends_on_issue_id", "depends_on_wisp_id", "depends_on_external"} {
		want := 0
		if column == wantColumn {
			want = 1
		}
		var got int
		//nolint:gosec // G201: table is one of the contract's two hardcoded names, column one of the three above.
		query := "SELECT COUNT(*) FROM " + table + " WHERE issue_id = ? AND " + column + " = ?"
		if err := fixture.QueryScalar(ctx, query, []any{source, target}, &got); err != nil {
			t.Fatalf("count %s.%s rows %s -> %s: %v", table, column, source, target, err)
		}
		if got != want {
			t.Errorf("%s.%s rows %s -> %s = %d, want %d: the target belongs in %s — %s",
				table, column, source, target, got, want, wantColumn, why)
		}
	}
}

// assertDependencyEditorEventCount counts events of one type for one issue.
// Both event types the role writes go through it — the add cases read
// dependency_added, the removal cases dependency_removed — because the two
// differ only in that argument, and a second helper pinned to one of them was
// a copy waiting to drift.
func assertDependencyEditorEventCount(t *testing.T, ctx context.Context, fixture DependencyEditorFixture, table, issueID string, eventType types.EventType, want int) {
	t.Helper()
	var got int
	//nolint:gosec // G201: table is one of the contract's two hardcoded names.
	query := "SELECT COUNT(*) FROM " + table + " WHERE issue_id = ? AND event_type = ?"
	if err := fixture.QueryScalar(ctx, query, []any{issueID, string(eventType)}, &got); err != nil {
		t.Fatalf("count %s %s rows for %s: %v", table, eventType, issueID, err)
	}
	if got != want {
		t.Errorf("%s %s rows for %s = %d, want %d", table, eventType, issueID, got, want)
	}
}

// assertDependencyEditorEventActor reads the actor off the one event of a
// given type for one issue. It reads the DURABLE stream only: the attribution
// clause is about who is recorded, and the plane that record lands in is
// already pinned by the routing cases.
func assertDependencyEditorEventActor(t *testing.T, ctx context.Context, fixture DependencyEditorFixture, issueID string, eventType types.EventType, want string) {
	t.Helper()
	var got string
	if err := fixture.QueryScalar(ctx,
		"SELECT COALESCE(actor, '') FROM events WHERE issue_id = ? AND event_type = ?",
		[]any{issueID, string(eventType)}, &got); err != nil {
		t.Fatalf("read the %s actor for %s: %v", eventType, issueID, err)
	}
	if got != want {
		t.Errorf("%s actor for %s = %q, want %q: the entry is attributed to the request's Actor", eventType, issueID, got, want)
	}
}

// assertDependencyEditorOutgoingCount counts ALL outgoing edges from one source
// in one table. Paired with assertDependencyEdgeTypedCount it pins a whole
// neighbor set: the typed count says the right edge is there, this one says
// nothing else is.
func assertDependencyEditorOutgoingCount(t *testing.T, ctx context.Context, fixture DependencyEditorFixture, table, source string, want int) {
	t.Helper()
	var got int
	//nolint:gosec // G201: table is one of the contract's two hardcoded names.
	query := "SELECT COUNT(*) FROM " + table + " WHERE issue_id = ?"
	if err := fixture.QueryScalar(ctx, query, []any{source}, &got); err != nil {
		t.Fatalf("count %s edges from %s: %v", table, source, err)
	}
	if got != want {
		t.Errorf("%s edges from %s = %d, want %d", table, source, got, want)
	}
}

// assertDependencyEditorNoEdgesFrom is how a refusal case spells "the request
// is all-or-nothing". It counts BOTH planes for each source, because a refused
// request that left an ephemeral edge behind is exactly the partial commit the
// all-or-nothing rule exists to prevent.
func assertDependencyEditorNoEdgesFrom(t *testing.T, ctx context.Context, fixture DependencyEditorFixture, sources ...string) {
	t.Helper()
	for _, source := range sources {
		for _, table := range []string{"dependencies", "wisp_dependencies"} {
			assertDependencyEditorOutgoingCount(t, ctx, fixture, table, source, 0)
		}
	}
}

func dependencyEditorHistoryCount(t *testing.T, ctx context.Context, fixture DependencyEditorFixture) int {
	t.Helper()
	entries, err := fixture.CountHistory(ctx)
	if err != nil {
		t.Fatalf("CountHistory: %v", err)
	}
	return entries
}

func assertDependencyEditorHistoryDelta(t *testing.T, ctx context.Context, fixture DependencyEditorFixture, before, want int, why string) {
	t.Helper()
	after := dependencyEditorHistoryCount(t, ctx, fixture)
	if after-before != want {
		t.Errorf("history entries went %d -> %d (delta %d), want a delta of %d: %s", before, after, after-before, want, why)
	}
}

// dependencyEditorHistoryProbe takes the history count now and returns the
// assertion for the delta since. It is for cases whose SUBJECT is something
// else and whose history count is one assertion among several: those must keep
// running on a fixture that cannot observe history rather than skip whole, the
// way the two cases about history itself correctly do.
func dependencyEditorHistoryProbe(t *testing.T, ctx context.Context, fixture DependencyEditorFixture) func(want int, why string) {
	t.Helper()
	if fixture.CountHistory == nil {
		return func(int, string) {
			t.Log("fixture.CountHistory is nil: this backend cannot observe history, so only the history half of this case is unchecked")
		}
	}
	before := dependencyEditorHistoryCount(t, ctx, fixture)
	return func(want int, why string) {
		t.Helper()
		assertDependencyEditorHistoryDelta(t, ctx, fixture, before, want, why)
	}
}
