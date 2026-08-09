package conformance

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// This file holds the semantic contract every implementation of
// publicops.Deleter must satisfy. Each case asserts what issueops/deleter.go
// PROMISES, cited by line, rather than what any one backend happens to do; a
// backend that genuinely disagrees is parked at its own wiring site with
// skipKnownDivergence so the case still runs on the ones that agree.
//
// THERE ARE TWO BODIES BEHIND THE THREE WIRINGS. dolt and embeddeddolt share
// internal/storage/issueops.DeleteInTx and differ only in how they reach a
// transaction; the unit-of-work provider reaches the same questions through the
// domain use cases and is genuinely separate code. So the wirings are one vote
// plus an engine check, and a second independent vote.
//
// backend/conformance/audit_issue-lifecycle.go pins cascade, force, the orphan
// guard and the dry-run counts at the STORAGE SEAM, and cannot see the role at
// all. The cases below are the promises the ROLE adds on top of it: id
// normalization, the ordered refusals, the typed errors, the reference rewrite
// inside the transaction, and the history entry.
//
// EVERY CASE NAMESPACES ITS SEEDS with fixture.IssuePrefix and its own tag: the
// three wirings share one database across the whole role suite, and a delete
// can destroy another case's fixture.

// DeleterFixture supplies adapter-specific storage access for the named-row
// erasure assertions.
type DeleterFixture struct {
	// IssuePrefix namespaces the ids each assertion seeds, so several of them
	// can share one database.
	IssuePrefix string
	// Deleter is the surface under test.
	Deleter publicops.Deleter
	// CreateIssue seeds a durable issue in the issues plane.
	CreateIssue func(context.Context, *types.Issue, string) error
	// CreateWisp seeds an ephemeral issue in the wisps plane.
	CreateWisp func(context.Context, *types.Issue, string) error
	// AddDependency seeds ONE edge, routed to the plane the edge's SOURCE
	// lives in.
	AddDependency func(context.Context, *types.Dependency, string) error
	// QueryScalar runs a single-row query and scans it. It is how these cases
	// check the one thing a delete result cannot be trusted to report about
	// itself: whether the rows are really gone.
	QueryScalar func(context.Context, string, []any, ...any) error
	// CountHistory reports how many history entries the fixture's branch has.
	// A nil hook means "this backend cannot observe history", and the case that
	// needs it SKIPS with that reason rather than passing quietly.
	CountHistory func(context.Context) (int, error)
}

// RunDeleterRefusesAMalformedRequest pins the request rules that need no
// database (issueops/deleter.go, DeleteRequest.IDs: "an empty or all-blank
// slice is ErrValidation rather than a no-op").
//
// A no-op is the dangerous answer, not merely the sloppy one: a caller whose id
// list came out empty because its own construction broke would read "deleted 0"
// and conclude the workspace was already clean.
func RunDeleterRefusesAMalformedRequest(t *testing.T, ctx context.Context, fixture DeleterFixture) {
	t.Helper()
	for _, test := range []struct {
		name    string
		request publicops.DeleteRequest
	}{
		{"no ids", publicops.DeleteRequest{Force: true}},
		{"nil ids", publicops.DeleteRequest{IDs: nil, Force: true}},
		{"blank id", publicops.DeleteRequest{IDs: []string{"   "}, Force: true}},
		{"blank id beside a real one", publicops.DeleteRequest{IDs: []string{"x", ""}, Force: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := fixture.Deleter.Delete(ctx, test.request)
			if !errors.Is(err, publicops.ErrValidation) {
				t.Fatalf("Delete(%s) error = %v, want ErrValidation", test.name, err)
			}
			if result.Deleted != 0 {
				t.Errorf("refused delete reported Deleted = %d, want 0", result.Deleted)
			}
		})
	}
}

// RunDeleterRefusesAnAbsentID pins the all-or-nothing promise
// (issueops/deleter.go, DeleteRequest.IDs: "An id that names none is
// ErrNotFound and NOTHING IS DELETED — not even the ids beside it that did
// resolve").
//
// It is asserted with a REAL id beside the typo, because that is the only
// arrangement that can fail: an implementation that deleted as it resolved
// would pass a single-absent-id case perfectly. And under DryRun too, because a
// preview that succeeded here would tell a caller to go ahead.
func RunDeleterRefusesAnAbsentID(t *testing.T, ctx context.Context, fixture DeleterFixture) {
	t.Helper()
	stored := deleterSeedIssue(t, ctx, fixture, "gone", "real")
	absent := fixture.IssuePrefix + "-gone-nosuchrow"

	for _, dryRun := range []bool{false, true} {
		result, err := fixture.Deleter.Delete(ctx, publicops.DeleteRequest{
			IDs:    []string{stored, absent},
			Force:  true,
			DryRun: dryRun,
		})
		if !errors.Is(err, publicops.ErrNotFound) {
			t.Fatalf("Delete(dryRun=%v) with an absent id error = %v, want ErrNotFound", dryRun, err)
		}
		var notFound *publicops.NotFoundError
		if !errors.As(err, &notFound) {
			t.Fatalf("Delete(dryRun=%v) error = %v, want *NotFoundError", dryRun, err)
		}
		if !reflect.DeepEqual(notFound.IDs, []string{absent}) {
			t.Errorf("NotFoundError.IDs = %v, want [%s] — the id that resolved is not missing", notFound.IDs, absent)
		}
		if result.Deleted != 0 {
			t.Errorf("refused delete reported Deleted = %d, want 0", result.Deleted)
		}
		deleterAssertIssueRows(t, ctx, fixture, 1, stored)
	}
}

// RunDeleterRefusesDependentsOutsideTheRequest pins the guard this role took
// off the CLI (issueops/deleter.go, DeleteRequest.Force: "WITHOUT Cascade AND
// WITHOUT Force, a named row that some row OUTSIDE the request depends on is
// refused"), and the exception beside it: a dependent the request DID name is
// not a dependent for this purpose.
//
// The refusal is asserted with its EFFECT as well as its type: a guard that
// returned the error after deleting would satisfy an errors.Is assertion
// perfectly.
func RunDeleterRefusesDependentsOutsideTheRequest(t *testing.T, ctx context.Context, fixture DeleterFixture) {
	t.Helper()
	blocker := deleterSeedIssue(t, ctx, fixture, "guard", "blocker")
	dependent := deleterSeedIssue(t, ctx, fixture, "guard", "dependent")
	deleterAddEdge(t, ctx, fixture, dependent, blocker)

	for _, dryRun := range []bool{false, true} {
		result, err := fixture.Deleter.Delete(ctx, publicops.DeleteRequest{
			IDs:    []string{blocker},
			DryRun: dryRun,
		})
		if !errors.Is(err, publicops.ErrDependentsOutsideRequest) {
			t.Fatalf("Delete(dryRun=%v) unforced over a dependent: error = %v, want ErrDependentsOutsideRequest", dryRun, err)
		}
		var blocked *publicops.DependentsOutsideRequestError
		if !errors.As(err, &blocked) {
			t.Fatalf("Delete(dryRun=%v) error = %v, want *DependentsOutsideRequestError", dryRun, err)
		}
		if blocked.IssueID != blocker {
			t.Errorf("DependentsOutsideRequestError.IssueID = %q, want %q", blocked.IssueID, blocker)
		}
		if !reflect.DeepEqual(blocked.Dependents, []string{dependent}) {
			t.Errorf("DependentsOutsideRequestError.Dependents = %v, want [%s]", blocked.Dependents, dependent)
		}
		if result.Deleted != 0 {
			t.Errorf("refused delete reported Deleted = %d, want 0", result.Deleted)
		}
		deleterAssertIssueRows(t, ctx, fixture, 2, blocker, dependent)
	}

	// Naming BOTH ends is not a guarded case: the request is deleting the edge
	// too. Refusing it would make `bd delete a b` fail on exactly the pair a
	// caller took care to list together.
	result, err := fixture.Deleter.Delete(ctx, publicops.DeleteRequest{IDs: []string{blocker, dependent}})
	if err != nil {
		t.Fatalf("Delete() naming both ends of the edge: error = %v, want nil — a dependent INSIDE the request is not a dependent", err)
	}
	if result.Deleted != 2 {
		t.Errorf("Deleted = %d, want 2", result.Deleted)
	}
	deleterAssertIssueRows(t, ctx, fixture, 0, blocker, dependent)
}

// RunDeleterForceOrphansDependents pins what --force MEANS after this commit
// (issueops/deleter.go, DeleteRequest.Force: "deletes the named rows and leaves
// rows that depended on them ORPHANED"). It is the case that catches the drift
// this commit removes: the proxied route used to hardcode cascade, so `--force`
// there deleted the dependent instead of orphaning it.
func RunDeleterForceOrphansDependents(t *testing.T, ctx context.Context, fixture DeleterFixture) {
	t.Helper()
	blocker := deleterSeedIssue(t, ctx, fixture, "force", "blocker")
	dependent := deleterSeedIssue(t, ctx, fixture, "force", "dependent")
	deleterAddEdge(t, ctx, fixture, dependent, blocker)

	result := deleterDelete(t, ctx, fixture, publicops.DeleteRequest{
		IDs:   []string{blocker},
		Force: true,
	})
	if result.Deleted != 1 {
		t.Errorf("Deleted = %d, want 1 — force deletes the NAMED row and nothing else", result.Deleted)
	}
	if !reflect.DeepEqual(result.Orphaned, []string{dependent}) {
		t.Errorf("Orphaned = %v, want [%s]", result.Orphaned, dependent)
	}
	deleterAssertIssueRows(t, ctx, fixture, 0, blocker)
	deleterAssertIssueRows(t, ctx, fixture, 1, dependent)
	// The orphan keeps its row and loses its edge; a dependent still pointing
	// at a deleted id would be a dangling edge rather than an orphan.
	deleterAssertEdgeRows(t, ctx, fixture, 0, dependent, blocker)
}

// RunDeleterCascadeDeletesTheClosure pins the other mode (issueops/deleter.go,
// DeleteRequest.Cascade) over a chain rather than a single edge, because
// "transitive" is the whole claim and a one-edge fixture cannot tell a
// transitive expansion from a direct one. It also pins the interaction the leaf
// spells out: a request carrying BOTH Cascade and Force is legal and behaves as
// Cascade.
func RunDeleterCascadeDeletesTheClosure(t *testing.T, ctx context.Context, fixture DeleterFixture) {
	t.Helper()
	root := deleterSeedIssue(t, ctx, fixture, "casc", "root")
	middle := deleterSeedIssue(t, ctx, fixture, "casc", "middle")
	leaf := deleterSeedIssue(t, ctx, fixture, "casc", "leaf")
	bystander := deleterSeedIssue(t, ctx, fixture, "casc", "bystander")
	deleterAddEdge(t, ctx, fixture, middle, root)
	deleterAddEdge(t, ctx, fixture, leaf, middle)

	result := deleterDelete(t, ctx, fixture, publicops.DeleteRequest{
		IDs:     []string{root},
		Cascade: true,
		Force:   true,
	})
	if result.Deleted != 3 {
		t.Errorf("Deleted = %d, want 3 — the closure is root, its dependent and ITS dependent", result.Deleted)
	}
	if len(result.Orphaned) != 0 {
		t.Errorf("Orphaned = %v, want empty: a cascade leaves nothing outside the set to orphan", result.Orphaned)
	}
	deleterAssertIssueRows(t, ctx, fixture, 0, root, middle, leaf)
	deleterAssertIssueRows(t, ctx, fixture, 1, bystander)
}

// RunDeleterCascadeFromAWispRootDeletesTheClosure pins the CROSS-PLANE quadrant
// of the cascade (issueops/deleter.go, DeleteRequest.Cascade: "also deletes the
// TRANSITIVE CLOSURE of everything that depends on the named rows, IN BOTH
// PLANES").
//
// The other cascade cases are durable-only or edge-free, so nothing else
// asserts what a cascade rooted at a WISP does to the durable rows hanging off
// it. `bd wisp gc` deletes wisps with cascade hardcoded, so routine
// housekeeping runs through this quadrant.
//
// The chain is two edges deep on purpose, and its second link is durable, so
// this cannot be satisfied by a body that merely follows one cross-plane edge.
func RunDeleterCascadeFromAWispRootDeletesTheClosure(t *testing.T, ctx context.Context, fixture DeleterFixture) {
	t.Helper()
	root := deleterSeedWisp(t, ctx, fixture, "wcasc", "root")
	dependent := deleterSeedIssue(t, ctx, fixture, "wcasc", "dependent")
	grandchild := deleterSeedIssue(t, ctx, fixture, "wcasc", "grandchild")
	bystander := deleterSeedIssue(t, ctx, fixture, "wcasc", "bystander")
	deleterAddEdge(t, ctx, fixture, dependent, root)
	deleterAddEdge(t, ctx, fixture, grandchild, dependent)

	result := deleterDelete(t, ctx, fixture, publicops.DeleteRequest{
		IDs:     []string{root},
		Cascade: true,
		Force:   true,
	})
	if result.Deleted != 3 {
		t.Errorf("Deleted = %d, want 3 — the wisp, the durable row that depends on it, and ITS dependent", result.Deleted)
	}
	deleterAssertWispRows(t, ctx, fixture, 0, root)
	deleterAssertIssueRows(t, ctx, fixture, 0, dependent, grandchild)
	deleterAssertIssueRows(t, ctx, fixture, 1, bystander)
}

// RunDeleterGuardsAWispNamedWithADurableDependent pins that the dependents
// guard has NO WISP EXEMPTION (issueops/deleter.go, DeleteRequest.Force:
// "WITHOUT Cascade AND WITHOUT Force, a NAMED ROW that some row OUTSIDE the
// request depends on is refused" — a named row, not a named durable row).
//
// The other two guard cases are durable-only, so a body that partitioned the
// request and asked the guard only about the durable half passed them while
// silently orphaning a workspace's graph through the other half. Both halves of
// the clause are asserted here — the unforced refusal and the forced orphan
// report — because a body can get either one right on its own.
func RunDeleterGuardsAWispNamedWithADurableDependent(t *testing.T, ctx context.Context, fixture DeleterFixture) {
	t.Helper()
	wisp := deleterSeedWisp(t, ctx, fixture, "wguard", "wisp")
	dependent := deleterSeedIssue(t, ctx, fixture, "wguard", "dependent")
	deleterAddEdge(t, ctx, fixture, dependent, wisp)

	for _, dryRun := range []bool{false, true} {
		result, err := fixture.Deleter.Delete(ctx, publicops.DeleteRequest{
			IDs:    []string{wisp},
			DryRun: dryRun,
		})
		if !errors.Is(err, publicops.ErrDependentsOutsideRequest) {
			t.Fatalf("Delete(dryRun=%v) unforced over a wisp with a durable dependent: error = %v, want ErrDependentsOutsideRequest", dryRun, err)
		}
		var blocked *publicops.DependentsOutsideRequestError
		if !errors.As(err, &blocked) {
			t.Fatalf("Delete(dryRun=%v) error = %v, want *DependentsOutsideRequestError", dryRun, err)
		}
		if blocked.IssueID != wisp {
			t.Errorf("DependentsOutsideRequestError.IssueID = %q, want the named wisp %q", blocked.IssueID, wisp)
		}
		if !reflect.DeepEqual(blocked.Dependents, []string{dependent}) {
			t.Errorf("DependentsOutsideRequestError.Dependents = %v, want [%s]", blocked.Dependents, dependent)
		}
		if result.Deleted != 0 {
			t.Errorf("refused delete reported Deleted = %d, want 0", result.Deleted)
		}
		deleterAssertWispRows(t, ctx, fixture, 1, wisp)
		deleterAssertIssueRows(t, ctx, fixture, 1, dependent)
	}

	result := deleterDelete(t, ctx, fixture, publicops.DeleteRequest{
		IDs:   []string{wisp},
		Force: true,
	})
	if result.Deleted != 1 {
		t.Errorf("Deleted = %d, want 1 — force deletes the NAMED wisp and nothing else", result.Deleted)
	}
	if !reflect.DeepEqual(result.Orphaned, []string{dependent}) {
		t.Errorf("Orphaned = %v, want [%s] — the cross-plane edge orphans a durable row too", result.Orphaned, dependent)
	}
	deleterAssertWispRows(t, ctx, fixture, 0, wisp)
	deleterAssertIssueRows(t, ctx, fixture, 1, dependent)
	deleterAssertEdgeRows(t, ctx, fixture, 0, dependent, wisp)
}

// RunDeleterGuardsADurableNamedWithAWispDependent is the fourth quadrant of the
// both-planes-both-ends guard, and the one the suite was missing: a WISP as the
// DEPENDENT, so the edge lands in wisp_dependencies, with the durable row it
// depends on named unforced.
//
// The two bodies scan different tables to answer it, so this is one of the few
// places three green legs really would be two independent votes — and neither
// was being taken.
func RunDeleterGuardsADurableNamedWithAWispDependent(t *testing.T, ctx context.Context, fixture DeleterFixture) {
	t.Helper()
	blocker := deleterSeedIssue(t, ctx, fixture, "dguard", "durable blocker")
	dependent := deleterSeedWisp(t, ctx, fixture, "dguard", "wisp dependent")
	deleterAddEdge(t, ctx, fixture, dependent, blocker)

	for _, dryRun := range []bool{false, true} {
		result, err := fixture.Deleter.Delete(ctx, publicops.DeleteRequest{
			IDs:    []string{blocker},
			DryRun: dryRun,
		})
		if !errors.Is(err, publicops.ErrDependentsOutsideRequest) {
			t.Fatalf("Delete(dryRun=%v) unforced over a durable row with a wisp dependent: error = %v, want ErrDependentsOutsideRequest", dryRun, err)
		}
		var blocked *publicops.DependentsOutsideRequestError
		if !errors.As(err, &blocked) {
			t.Fatalf("Delete(dryRun=%v) error = %v, want *DependentsOutsideRequestError", dryRun, err)
		}
		if blocked.IssueID != blocker {
			t.Errorf("DependentsOutsideRequestError.IssueID = %q, want the named durable row %q", blocked.IssueID, blocker)
		}
		if !reflect.DeepEqual(blocked.Dependents, []string{dependent}) {
			t.Errorf("DependentsOutsideRequestError.Dependents = %v, want [%s] — the wisp on the other end of the edge counts", blocked.Dependents, dependent)
		}
		if result.Deleted != 0 {
			t.Errorf("refused delete reported Deleted = %d, want 0", result.Deleted)
		}
		deleterAssertIssueRows(t, ctx, fixture, 1, blocker)
		deleterAssertWispRows(t, ctx, fixture, 1, dependent)
	}

	result := deleterDelete(t, ctx, fixture, publicops.DeleteRequest{
		IDs:   []string{blocker},
		Force: true,
	})
	if result.Deleted != 1 {
		t.Errorf("Deleted = %d, want 1 — force deletes the NAMED durable row and nothing else", result.Deleted)
	}
	if !reflect.DeepEqual(result.Orphaned, []string{dependent}) {
		t.Errorf("Orphaned = %v, want [%s] — the orphan is a wisp, and it is still an orphan", result.Orphaned, dependent)
	}
	deleterAssertIssueRows(t, ctx, fixture, 0, blocker)
	deleterAssertWispRows(t, ctx, fixture, 1, dependent)
	deleterAssertEdgeRows(t, ctx, fixture, 0, dependent, blocker)
}

// RunDeleterCountsCrossPlaneEdgesItRemoves pins DeleteResult.Dependencies over
// the two edge shapes that cross planes, which no case counted.
//
// The number is a claim about what the delete DID, and both routes print it —
// `bd delete` says "Removed N dependency link(s)" and the wire field carries
// it. The unit-of-work body counted each plane's ids against that plane's
// table only, so an edge whose deleted end is the TARGET in the OTHER plane's
// table was counted by neither query while being removed anyway. The store
// bodies scan inbound edges across both tables and did not have the gap, so
// the two routes reported different numbers for one delete.
//
// Both directions are seeded at once and the row count is asserted beside the
// number, because "reported 2" and "removed 2" are different claims and only
// one of them was ever checked here.
func RunDeleterCountsCrossPlaneEdgesItRemoves(t *testing.T, ctx context.Context, fixture DeleterFixture) {
	t.Helper()
	// A durable dependent of a deleted WISP: the edge lives in `dependencies`
	// with the wisp as the target.
	wisp := deleterSeedWisp(t, ctx, fixture, "xcount", "wisp")
	durableDependent := deleterSeedIssue(t, ctx, fixture, "xcount", "durabledep")
	deleterAddEdge(t, ctx, fixture, durableDependent, wisp)

	// A wisp dependent of a deleted DURABLE row: the edge lives in
	// `wisp_dependencies` with the durable row as the target.
	durable := deleterSeedIssue(t, ctx, fixture, "xcount", "durable")
	wispDependent := deleterSeedWisp(t, ctx, fixture, "xcount", "wispdep")
	deleterAddEdge(t, ctx, fixture, wispDependent, durable)

	// Both edges are really there before the delete, so a zero count afterwards
	// is a removal rather than a fixture that never seeded them.
	deleterAssertEdgeRows(t, ctx, fixture, 1, durableDependent, wisp)
	deleterAssertWispEdgeRows(t, ctx, fixture, 1, wispDependent, durable)

	result := deleterDelete(t, ctx, fixture, publicops.DeleteRequest{
		IDs:   []string{wisp, durable},
		Force: true,
	})
	if result.Deleted != 2 {
		t.Fatalf("Deleted = %d, want 2 — force deletes the two NAMED rows", result.Deleted)
	}
	if result.Dependencies != 2 {
		t.Errorf("Dependencies = %d, want 2: both removed edges cross a plane, and an edge is "+
			"counted whichever end of it was deleted", result.Dependencies)
	}
	// The edges really are gone, so the number under-reported real removals
	// rather than describing a delete that did less.
	deleterAssertEdgeRows(t, ctx, fixture, 0, durableDependent, wisp)
	deleterAssertWispEdgeRows(t, ctx, fixture, 0, wispDependent, durable)
}

// RunDeleterNeverCallsALiveRowDeleted pins the invariant that makes the
// under-deleting cascade impossible to reintroduce quietly: THE SET WHOSE
// CITATIONS ARE REWRITTEN IS THE SET THAT WAS DELETED.
//
// It is written as a biconditional rather than a count, because the counts
// cannot see it: an implementation whose rewrite set is a strict SUPERSET of
// its deletion set reports a perfectly plausible ReferencesUpdated and leaves
// a neighbor's description saying a LIVE issue is gone.
//
// The fixture is the cheapest arrangement in which the two sets can differ: a
// wisp root, a durable row reachable only through it, and a third row that the
// durable one DEPENDS ON — a graph neighbor of the deletion set (in scope for
// the rewrite) that is never in the closure, so it always survives.
func RunDeleterNeverCallsALiveRowDeleted(t *testing.T, ctx context.Context, fixture DeleterFixture) {
	t.Helper()
	root := deleterSeedWisp(t, ctx, fixture, "live", "root")
	dependent := deleterSeedIssue(t, ctx, fixture, "live", "dependent")

	survivor := deleterIssue(fixture, "live", "survivor", false)
	survivor.Description = "unblocks " + dependent + ", tracked under " + root
	deleterSeed(t, ctx, fixture, survivor)
	deleterAddEdge(t, ctx, fixture, dependent, root)
	deleterAddEdge(t, ctx, fixture, dependent, survivor.ID)

	deleterDelete(t, ctx, fixture, publicops.DeleteRequest{
		IDs:     []string{root},
		Cascade: true,
		Force:   true,
		Actor:   "deleter-contract",
	})

	deleterAssertIssueRows(t, ctx, fixture, 1, survivor.ID)
	text := deleterText(t, ctx, fixture, survivor.ID, "description")
	for _, cited := range []struct {
		id    string
		table string
	}{
		{dependent, "issues"},
		{root, "wisps"},
	} {
		marker := "[deleted:" + cited.id + "]"
		gone := deleterRowCount(t, ctx, fixture, cited.table, cited.id) == 0
		switch {
		case gone && !strings.Contains(text, marker):
			t.Errorf("%s is gone but the survivor's description still cites it verbatim: %q", cited.id, text)
		case !gone && strings.Contains(text, marker):
			t.Errorf("%s STILL EXISTS and the survivor's description calls it deleted: %q — "+
				"the rewrite set is wider than the deletion set", cited.id, text)
		case !gone && !strings.Contains(text, cited.id):
			t.Errorf("%s still exists but the survivor's description no longer names it: %q", cited.id, text)
		}
	}
}

// RunDeleterErasesAcrossBothPlanes pins that one request reaches BOTH tiers
// (issueops/deleter.go, DeleteRequest.IDs: "in either plane"). One `--from-file`
// batch may legitimately mix a wisp with an issue, and a body that routed the
// whole request by one flag would silently report the wisp as gone.
func RunDeleterErasesAcrossBothPlanes(t *testing.T, ctx context.Context, fixture DeleterFixture) {
	t.Helper()
	issue := deleterSeedIssue(t, ctx, fixture, "plane", "issue")
	wisp := deleterSeedWisp(t, ctx, fixture, "plane", "wisp")

	result := deleterDelete(t, ctx, fixture, publicops.DeleteRequest{
		IDs:   []string{issue, wisp},
		Force: true,
	})
	if result.Deleted != 2 {
		t.Errorf("Deleted = %d, want 2 — one row from each plane", result.Deleted)
	}
	deleterAssertIssueRows(t, ctx, fixture, 0, issue)
	deleterAssertWispRows(t, ctx, fixture, 0, wisp)
}

// RunDeleterCollapsesDuplicateIDs pins the normalization (issueops/deleter.go,
// DeleteRequest.IDs: "DUPLICATES COLLAPSE"). An id repeated in a `--from-file`
// list is one row, and an implementation that did not collapse would either
// double-count the answer or fail its second DELETE.
func RunDeleterCollapsesDuplicateIDs(t *testing.T, ctx context.Context, fixture DeleterFixture) {
	t.Helper()
	id := deleterSeedIssue(t, ctx, fixture, "dupe", "target")

	result := deleterDelete(t, ctx, fixture, publicops.DeleteRequest{
		IDs:   []string{id, id, "  " + id + "  "},
		Force: true,
	})
	if result.Deleted != 1 {
		t.Errorf("Deleted = %d, want 1 for one id named three times", result.Deleted)
	}
	deleterAssertIssueRows(t, ctx, fixture, 0, id)
}

// RunDeleterRewritesReferencesInNeighbors pins the rewrite
// (issueops/deleter.go, Deleter.Delete: "WHICH ROWS GET REWRITTEN"): every
// occurrence of a deleted id in a graph neighbor's four long text fields
// becomes `[deleted:<id>]`, matched at word boundaries.
//
// The NEGATIVE half is the half worth having. A neighbor also cites an id that
// merely has the deleted one as a PREFIX, and that citation must survive: a
// bare substring match would corrupt a bystander's text on every delete, and no
// count in the result would show it.
func RunDeleterRewritesReferencesInNeighbors(t *testing.T, ctx context.Context, fixture DeleterFixture) {
	t.Helper()
	target := deleterSeedIssue(t, ctx, fixture, "refs", "target")
	// An id the target's id is a strict prefix of, so the word-boundary rule
	// has something to get wrong.
	lookalike := deleterSeedIssue(t, ctx, fixture, "refs", "targetx")

	neighbor := deleterIssue(fixture, "refs", "neighbor", false)
	neighbor.Description = "blocked by " + target + "."
	neighbor.Notes = "see " + target + " and " + lookalike
	neighbor.Design = "(" + target + ")"
	neighbor.AcceptanceCriteria = target + " closes"
	deleterSeed(t, ctx, fixture, neighbor)
	deleterAddEdge(t, ctx, fixture, neighbor.ID, target)

	// A row that CITES the target but shares no edge with it. The leaf says
	// the rewrite is scoped to graph neighbors, so this one is left alone.
	stranger := deleterIssue(fixture, "refs", "stranger", false)
	stranger.Description = "also mentions " + target
	deleterSeed(t, ctx, fixture, stranger)

	result := deleterDelete(t, ctx, fixture, publicops.DeleteRequest{
		IDs:   []string{target},
		Force: true,
		Actor: "deleter-contract",
	})
	if result.ReferencesUpdated != 1 {
		t.Errorf("ReferencesUpdated = %d, want 1 — it counts ROWS, and one row was rewritten", result.ReferencesUpdated)
	}

	marker := "[deleted:" + target + "]"
	for _, field := range []string{"description", "notes", "design", "acceptance_criteria"} {
		got := deleterText(t, ctx, fixture, neighbor.ID, field)
		if !strings.Contains(got, marker) {
			t.Errorf("neighbor %s = %q, want it to contain %q", field, got, marker)
		}
		if strings.Contains(got, "[deleted:"+lookalike+"]") {
			t.Errorf("neighbor %s = %q: the lookalike id was rewritten too", field, got)
		}
	}
	if got := deleterText(t, ctx, fixture, neighbor.ID, "notes"); !strings.Contains(got, lookalike) {
		t.Errorf("neighbor notes = %q, want the lookalike id %q intact", got, lookalike)
	}
	if got := deleterText(t, ctx, fixture, fixture.IssuePrefix+"-refs-stranger", "description"); strings.Contains(got, marker) {
		t.Errorf("a row with no edge to the deleted id was rewritten: %q", got)
	}
}

// RunDeleterDryRunChangesNothing pins the preview promise
// (issueops/deleter.go, Deleter.Delete: "A DRY RUN CHANGES NOTHING"): the
// preview reports the counts the real deletion goes on to report, rewrites
// nothing, and leaves every row where it was.
//
// The two are compared against EACH OTHER rather than against literals: a
// preview whose number differs from the run it precedes is worse than no
// preview.
func RunDeleterDryRunChangesNothing(t *testing.T, ctx context.Context, fixture DeleterFixture) {
	t.Helper()
	first := deleterSeedIssue(t, ctx, fixture, "dry", "1")
	second := deleterSeedIssue(t, ctx, fixture, "dry", "2")
	deleterAddEdge(t, ctx, fixture, second, first)

	request := publicops.DeleteRequest{IDs: []string{first, second}, Actor: "deleter-contract"}
	preview := deleterDelete(t, ctx, fixture, deleterWithDryRun(request, true))
	if !preview.DryRun {
		t.Errorf("preview.DryRun = false, want the request echoed")
	}
	if preview.Deleted != 2 {
		t.Errorf("preview.Deleted = %d, want 2", preview.Deleted)
	}
	if preview.ReferencesUpdated != 0 {
		t.Errorf("preview.ReferencesUpdated = %d, want 0: a preview rewrites nothing", preview.ReferencesUpdated)
	}
	deleterAssertIssueRows(t, ctx, fixture, 2, first, second)

	actual := deleterDelete(t, ctx, fixture, request)
	if actual.Deleted != preview.Deleted {
		t.Errorf("Deleted: preview said %d, the run said %d", preview.Deleted, actual.Deleted)
	}
	if actual.Dependencies != preview.Dependencies {
		t.Errorf("Dependencies: preview said %d, the run said %d", preview.Dependencies, actual.Dependencies)
	}
	if actual.Labels != preview.Labels || actual.Events != preview.Events {
		t.Errorf("Labels/Events: preview said %d/%d, the run said %d/%d",
			preview.Labels, preview.Events, actual.Labels, actual.Events)
	}
	deleterAssertIssueRows(t, ctx, fixture, 0, first, second)
}

// RunDeleterRecordsAtMostOneHistoryEntry pins the versioning clause
// (issueops/deleter.go, Deleter.Delete: "one call records AT MOST ONE entry").
//
// AT MOST, not exactly one: only the server-backed store records an entry at
// all, so asserting exactly one would be asserting a property of one wiring.
// The DRY RUN half is the sharp one — a preview that left a commit behind
// would be a mutation wearing a preview's name.
func RunDeleterRecordsAtMostOneHistoryEntry(t *testing.T, ctx context.Context, fixture DeleterFixture) {
	t.Helper()
	if fixture.CountHistory == nil {
		t.Skip("this backend cannot observe history, so the entry-per-call clause is unobservable here")
	}
	first := deleterSeedIssue(t, ctx, fixture, "hist", "1")
	second := deleterSeedIssue(t, ctx, fixture, "hist", "2")
	request := publicops.DeleteRequest{IDs: []string{first, second}, Force: true}

	before := deleterHistory(t, ctx, fixture)
	deleterDelete(t, ctx, fixture, deleterWithDryRun(request, true))
	if after := deleterHistory(t, ctx, fixture); after != before {
		t.Errorf("history went %d -> %d across a DRY RUN, want no entry at all", before, after)
	}

	before = deleterHistory(t, ctx, fixture)
	deleterDelete(t, ctx, fixture, request)
	if after := deleterHistory(t, ctx, fixture); after < before || after > before+1 {
		t.Errorf("history went %d -> %d across one delete of 2 rows, want at most one more entry", before, after)
	}
}

// RunDeleterDoesNotMutateTheCallerRequest pins the no-mutation promise
// (issueops/deleter.go, DeleteRequest: "IDs is read, never written through, and
// never sorted in place").
//
// Every implementation NORMALIZES the id slice, and normalizing in place would
// hand the caller back a shorter, trimmed, reordered version of the list it
// passed — the list a CLI then echoes in its confirmation hint.
func RunDeleterDoesNotMutateTheCallerRequest(t *testing.T, ctx context.Context, fixture DeleterFixture) {
	t.Helper()
	id := deleterSeedIssue(t, ctx, fixture, "immutable", "target")
	ids := []string{"  " + id + "  ", id, fixture.IssuePrefix + "-immutable-absent"}
	snapshot := append([]string(nil), ids...)

	request := publicops.DeleteRequest{IDs: ids, Actor: "deleter-contract", Force: true, DryRun: true}
	requestSnapshot := request

	// The absent id makes this a refusal, which is deliberate: a body that
	// normalizes in place does it BEFORE it discovers the request is doomed,
	// so the failing path is where the mutation would survive unnoticed.
	if _, err := fixture.Deleter.Delete(ctx, request); !errors.Is(err, publicops.ErrNotFound) {
		t.Fatalf("Delete() error = %v, want ErrNotFound", err)
	}
	if !reflect.DeepEqual(ids, snapshot) {
		t.Errorf("the caller's id slice changed across the call: got %v, want %v", ids, snapshot)
	}
	if !reflect.DeepEqual(request.IDs, requestSnapshot.IDs) {
		t.Errorf("the caller's request changed across the call: got %+v, want %+v", request, requestSnapshot)
	}
}

// --- fixture helpers -------------------------------------------------------

// RunDeleterSettlesTheSurvivorsOfADeletedBlocker pins the blocked-state clause
// on the erasure verb: the rows it deletes are gone, so the promise is about
// the SURVIVORS.
//
// It is the same obligation the reference rewrite has, and it fails the same
// way when it is missed: an orphaned row left carrying is_blocked = 1 for a
// blocker that no longer exists is unblockable by any verb — nothing can close
// or remove the cause, because the cause is not there. Only a full repair
// clears it.
//
// The subjects cover all three arms the deletion's affected set has to walk:
// the direct depender, its parent-child descendant, and a WISP depender whose
// edge lives in the ephemeral table.
//
// Force is required rather than incidental: the dependers live outside the
// request, so an unforced delete is refused. That is the orphaning path
// exactly, which is the path where the flag would otherwise be stranded.
func RunDeleterSettlesTheSurvivorsOfADeletedBlocker(t *testing.T, ctx context.Context, fixture DeleterFixture) {
	t.Helper()
	blocker := deleterSeedIssue(t, ctx, fixture, "bsdel", "blocker")
	depender := deleterSeedIssue(t, ctx, fixture, "bsdel", "depender")
	child := deleterSeedIssue(t, ctx, fixture, "bsdel", "child")
	wispDepender := deleterSeedWisp(t, ctx, fixture, "bsdel", "wispdep")
	controlBlocker := deleterSeedIssue(t, ctx, fixture, "bsdel", "ctlblocker")
	controlDepender := deleterSeedIssue(t, ctx, fixture, "bsdel", "ctldepender")
	deleterAddEdge(t, ctx, fixture, depender, blocker)
	deleterAddParentChildEdge(t, ctx, fixture, child, depender)
	deleterAddEdge(t, ctx, fixture, wispDepender, blocker)
	deleterAddEdge(t, ctx, fixture, controlDepender, controlBlocker)

	probe := newBlockedStateProbe(ctx, fixture.QueryScalar)
	probe.requirePlaneResidency(t, blockedWisp(wispDepender))
	probe.requireBlockedByOpenBlocker(t, blockedIssue(depender), blockedIssue(blocker), "the direct depender of the row about to be deleted")
	probe.requireBlockedByOpenBlocker(t, blockedWisp(wispDepender), blockedIssue(blocker), "the cross-plane depender")
	probe.requireBlockedWithNoDirectBlockerEdges(t, blockedIssue(child), "the child inherits and holds no blocker of its own")
	probe.requireBlockedByOpenBlocker(t, blockedIssue(controlDepender), blockedIssue(controlBlocker), "the control's blocker is not deleted")

	flip := probe.watchFlip(t,
		[]blockedStateRow{blockedIssue(depender), blockedIssue(child), blockedWisp(wispDepender)},
		[]blockedStateRow{blockedIssue(controlDepender)})

	result, err := fixture.Deleter.Delete(ctx, publicops.DeleteRequest{IDs: []string{blocker}, Force: true, Actor: "deleter"})
	if err != nil {
		t.Fatalf("force-delete the blocker %s: %v", blocker, err)
	}
	if result.Deleted != 1 {
		t.Fatalf("Deleted = %d, want the 1 named row", result.Deleted)
	}

	flip.requireFlippedTo(t, 0, "a survivor whose only blocker was erased is left unblocked, and so is everything that inherited from it")
}

// RunDeleterSettlesTheChildrenOfADeletedParent is the other arm of the same
// affected set, and it needs its own case because it is reached by a different
// query: the depender arm walks edges INTO the deleted row, this one walks the
// parent-child edges OUT of it to the children that inherited from it.
//
// The child survives the force-delete and its parent does not, so what is left
// is a row that was blocked only because of a row that no longer exists.
func RunDeleterSettlesTheChildrenOfADeletedParent(t *testing.T, ctx context.Context, fixture DeleterFixture) {
	t.Helper()
	blocker := deleterSeedIssue(t, ctx, fixture, "bsdelpc", "blocker")
	parent := deleterSeedIssue(t, ctx, fixture, "bsdelpc", "parent")
	child := deleterSeedIssue(t, ctx, fixture, "bsdelpc", "child")
	controlBlocker := deleterSeedIssue(t, ctx, fixture, "bsdelpc", "ctlblocker")
	controlParent := deleterSeedIssue(t, ctx, fixture, "bsdelpc", "ctlparent")
	controlChild := deleterSeedIssue(t, ctx, fixture, "bsdelpc", "ctlchild")
	deleterAddEdge(t, ctx, fixture, parent, blocker)
	deleterAddParentChildEdge(t, ctx, fixture, child, parent)
	deleterAddEdge(t, ctx, fixture, controlParent, controlBlocker)
	deleterAddParentChildEdge(t, ctx, fixture, controlChild, controlParent)

	probe := newBlockedStateProbe(ctx, fixture.QueryScalar)
	probe.requireBlockedWithNoDirectBlockerEdges(t, blockedIssue(child), "the child is blocked only through the parent about to be deleted")
	probe.requireBlockedWithNoDirectBlockerEdges(t, blockedIssue(controlChild), "the control child's parent stays")

	flip := probe.watchFlip(t, []blockedStateRow{blockedIssue(child)}, []blockedStateRow{blockedIssue(controlChild)})

	if _, err := fixture.Deleter.Delete(ctx, publicops.DeleteRequest{IDs: []string{parent}, Force: true, Actor: "deleter"}); err != nil {
		t.Fatalf("force-delete the blocked parent %s: %v", parent, err)
	}
	var survives int
	if err := fixture.QueryScalar(ctx, "SELECT COUNT(*) FROM issues WHERE id = ?", []any{child}, &survives); err != nil {
		t.Fatalf("count the surviving child %s: %v", child, err)
	}
	if survives != 1 {
		t.Fatalf("child %s has %d rows after a forced delete of its parent, want the orphaned survivor this case is about", child, survives)
	}

	flip.requireFlippedTo(t, 0, "a child orphaned from a blocked parent inherits nothing, and the deleting transaction settles it")
}

// RunDeleterSettlesTheSurvivorsOfADeletedWispBlocker is the same obligation
// with the deleted row in the OTHER PLANE, and it is a different query rather
// than the same one with different data: the deletion's affected set walks the
// two planes through two separate loads keyed on two separate columns
// (internal/storage/issueops/blocked_state.go AffectedByDeletionInTx, which
// asks depends_on_issue_id about the deleted issues and depends_on_wisp_id
// about the deleted wisps). Every other Deleter case in this file names durable
// rows only, so the second load ran against an empty list every time and could
// have been deleted with nothing going red.
//
// IT IS ROLE-REACHABLE. DeleteRequest.IDs "names the rows to delete, IN EITHER
// PLANE", and the unforced guard has "no wisp exemption at either end", so a
// forced `bd delete <wisp>` with durable dependents is a documented mode rather
// than an out-of-band poke — which is why blocked_state.go's wisp carve-out,
// which covers the store's own wisp-delete entry points, does not cover this
// one.
//
// The survivors span both planes and both distances: a DURABLE depender of the
// wisp, that depender's parent-child child, and a WISP depender whose edge
// lives in the ephemeral table. The durable depender is the sharpest — its edge
// sits in `dependencies` with the target in depends_on_wisp_id, which is the
// exact pairing a body that classified the affected set by the DEPENDER's plane
// instead of the deleted row's would miss.
//
// Force is required for the same reason the sibling case gives, and here it is
// also the mode the leaf calls out by name: without it the dependents outside
// the request refuse the delete.
func RunDeleterSettlesTheSurvivorsOfADeletedWispBlocker(t *testing.T, ctx context.Context, fixture DeleterFixture) {
	t.Helper()
	wispBlocker := deleterSeedWisp(t, ctx, fixture, "bsdelw", "wispblocker")
	depender := deleterSeedIssue(t, ctx, fixture, "bsdelw", "depender")
	child := deleterSeedIssue(t, ctx, fixture, "bsdelw", "child")
	wispDepender := deleterSeedWisp(t, ctx, fixture, "bsdelw", "wispdep")
	controlBlocker := deleterSeedIssue(t, ctx, fixture, "bsdelw", "ctlblocker")
	controlDepender := deleterSeedIssue(t, ctx, fixture, "bsdelw", "ctldepender")
	deleterAddEdge(t, ctx, fixture, depender, wispBlocker)
	deleterAddParentChildEdge(t, ctx, fixture, child, depender)
	deleterAddEdge(t, ctx, fixture, wispDepender, wispBlocker)
	deleterAddEdge(t, ctx, fixture, controlDepender, controlBlocker)

	probe := newBlockedStateProbe(ctx, fixture.QueryScalar)
	// Residency is asserted on the row being deleted above all: a "wisp" that
	// had leaked a durable row would make this case a second copy of its
	// sibling.
	probe.requirePlaneResidency(t, blockedWisp(wispBlocker))
	probe.requirePlaneResidency(t, blockedWisp(wispDepender))
	probe.requirePlaneResidency(t, blockedIssue(depender))
	probe.requireBlockedByOpenBlocker(t, blockedIssue(depender), blockedWisp(wispBlocker),
		"the DURABLE depender of the ephemeral row about to be deleted")
	probe.requireBlockedByOpenBlocker(t, blockedWisp(wispDepender), blockedWisp(wispBlocker),
		"the same-plane depender, whose edge lives in the ephemeral table")
	probe.requireBlockedWithNoDirectBlockerEdges(t, blockedIssue(child),
		"the child inherits across the plane boundary and holds no blocker of its own")
	probe.requireBlockedByOpenBlocker(t, blockedIssue(controlDepender), blockedIssue(controlBlocker),
		"the control's blocker is a durable row this delete never names")

	flip := probe.watchFlip(t,
		[]blockedStateRow{blockedIssue(depender), blockedIssue(child), blockedWisp(wispDepender)},
		[]blockedStateRow{blockedIssue(controlDepender)})

	result, err := fixture.Deleter.Delete(ctx, publicops.DeleteRequest{IDs: []string{wispBlocker}, Force: true, Actor: "deleter"})
	if err != nil {
		t.Fatalf("force-delete the wisp blocker %s: %v", wispBlocker, err)
	}
	if result.Deleted != 1 {
		t.Fatalf("Deleted = %d, want the 1 named wisp", result.Deleted)
	}
	// The zeros below mean "blocked by nothing" only if the cause is really
	// gone; a delete that reported 1 and left the row would make them describe
	// a live blocker.
	var survives int
	if err := fixture.QueryScalar(ctx, "SELECT COUNT(*) FROM wisps WHERE id = ?", []any{wispBlocker}, &survives); err != nil {
		t.Fatalf("count the deleted wisp %s: %v", wispBlocker, err)
	}
	if survives != 0 {
		t.Fatalf("wisp %s still has %d row(s) after a forced delete, want it gone", wispBlocker, survives)
	}

	flip.requireFlippedTo(t, 0,
		"a survivor whose only blocker was an erased WISP is left unblocked, in both planes, and so is everything that inherited from it")
}

// deleterAddParentChildEdge is deleterAddEdge for the one edge type that is not
// a block: the blocked-state cases hang a child off a subject to observe
// INHERITANCE, which parent-child is what carries.
func deleterAddParentChildEdge(t *testing.T, ctx context.Context, fixture DeleterFixture, child, parent string) {
	t.Helper()
	if err := fixture.AddDependency(ctx, &types.Dependency{
		IssueID:     child,
		DependsOnID: parent,
		Type:        types.DepParentChild,
	}, "deleter-seed"); err != nil {
		t.Fatalf("seeding %s edge %s -> %s: %v", types.DepParentChild, child, parent, err)
	}
}

func deleterIssue(fixture DeleterFixture, tag, name string, ephemeral bool) *types.Issue {
	return &types.Issue{
		ID:        fmt.Sprintf("%s-%s-%s", fixture.IssuePrefix, tag, name),
		Title:     tag + " " + name,
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: ephemeral,
	}
}

// deleterSeed writes one issue through the plane its Ephemeral flag names.
func deleterSeed(t *testing.T, ctx context.Context, fixture DeleterFixture, issue *types.Issue) string {
	t.Helper()
	create := fixture.CreateIssue
	if issue.Ephemeral {
		create = fixture.CreateWisp
	}
	if err := create(ctx, issue, "deleter-seed"); err != nil {
		t.Fatalf("seeding %s: %v", issue.ID, err)
	}
	return issue.ID
}

func deleterSeedIssue(t *testing.T, ctx context.Context, fixture DeleterFixture, tag, name string) string {
	t.Helper()
	return deleterSeed(t, ctx, fixture, deleterIssue(fixture, tag, name, false))
}

// deleterSeedWisp is its ephemeral sibling.
func deleterSeedWisp(t *testing.T, ctx context.Context, fixture DeleterFixture, tag, name string) string {
	t.Helper()
	return deleterSeed(t, ctx, fixture, deleterIssue(fixture, tag, name, true))
}

// deleterAddEdge makes dependent depend on blocker.
func deleterAddEdge(t *testing.T, ctx context.Context, fixture DeleterFixture, dependent, blocker string) {
	t.Helper()
	err := fixture.AddDependency(ctx, &types.Dependency{
		IssueID:     dependent,
		DependsOnID: blocker,
		Type:        types.DepBlocks,
	}, "deleter-seed")
	if err != nil {
		t.Fatalf("seeding edge %s -> %s: %v", dependent, blocker, err)
	}
}

func deleterWithDryRun(request publicops.DeleteRequest, dryRun bool) publicops.DeleteRequest {
	request.DryRun = dryRun
	return request
}

func deleterDelete(t *testing.T, ctx context.Context, fixture DeleterFixture, request publicops.DeleteRequest) publicops.DeleteResult {
	t.Helper()
	result, err := fixture.Deleter.Delete(ctx, request)
	if err != nil {
		t.Fatalf("Delete(%+v) error = %v", request, err)
	}
	return result
}

// deleterAssertIssueRows counts the named ids in the ISSUES plane, rather than
// trusting the result the delete reported about itself.
func deleterAssertIssueRows(t *testing.T, ctx context.Context, fixture DeleterFixture, want int, ids ...string) {
	t.Helper()
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	var got int
	query := "SELECT COUNT(*) FROM issues WHERE id IN (" + strings.Join(placeholders, ",") + ")"
	if err := fixture.QueryScalar(ctx, query, args, &got); err != nil {
		t.Fatalf("counting issue rows for %v: %v", ids, err)
	}
	if got != want {
		t.Errorf("issue rows for %v = %d, want %d", ids, got, want)
	}
}

// deleterAssertWispRows is deleterAssertIssueRows for the ephemeral plane.
func deleterAssertWispRows(t *testing.T, ctx context.Context, fixture DeleterFixture, want int, ids ...string) {
	t.Helper()
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	var got int
	query := "SELECT COUNT(*) FROM wisps WHERE id IN (" + strings.Join(placeholders, ",") + ")"
	if err := fixture.QueryScalar(ctx, query, args, &got); err != nil {
		t.Fatalf("counting wisp rows for %v: %v", ids, err)
	}
	if got != want {
		t.Errorf("wisp rows for %v = %d, want %d", ids, got, want)
	}
}

// deleterRowCount REPORTS how many rows one id has in one plane instead of
// asserting a number, for the case whose assertion depends on the answer.
//
//nolint:gosec // G201: table is chosen by the caller from the two plane tables.
func deleterRowCount(t *testing.T, ctx context.Context, fixture DeleterFixture, table, id string) int {
	t.Helper()
	var got int
	query := fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE id = ?", table)
	if err := fixture.QueryScalar(ctx, query, []any{id}, &got); err != nil {
		t.Fatalf("counting %s rows for %s: %v", table, id, err)
	}
	return got
}

// deleterAssertEdgeRows counts the stored edges from dependent to blocker.
func deleterAssertEdgeRows(t *testing.T, ctx context.Context, fixture DeleterFixture, want int, dependent, blocker string) {
	t.Helper()
	var got int
	query := "SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND " +
		"COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) = ?"
	if err := fixture.QueryScalar(ctx, query, []any{dependent, blocker}, &got); err != nil {
		t.Fatalf("counting edges %s -> %s: %v", dependent, blocker, err)
	}
	if got != want {
		t.Errorf("edges %s -> %s = %d, want %d", dependent, blocker, got, want)
	}
}

// deleterText reads one long text column straight out of the issues table: the
// rewrite is a claim about stored bytes, not about anything the role reports.
//
//nolint:gosec // G201: column is chosen by the caller from a fixed set of four.
func deleterText(t *testing.T, ctx context.Context, fixture DeleterFixture, id, column string) string {
	t.Helper()
	var got string
	query := fmt.Sprintf("SELECT COALESCE(%s, '') FROM issues WHERE id = ?", column)
	if err := fixture.QueryScalar(ctx, query, []any{id}, &got); err != nil {
		t.Fatalf("reading %s.%s: %v", id, column, err)
	}
	return got
}

func deleterHistory(t *testing.T, ctx context.Context, fixture DeleterFixture) int {
	t.Helper()
	entries, err := fixture.CountHistory(ctx)
	if err != nil {
		t.Fatalf("CountHistory(): %v", err)
	}
	return entries
}

// deleterAssertWispEdgeRows is deleterAssertEdgeRows for the wisp plane's edge
// table, where an edge whose SOURCE is a wisp lives.
func deleterAssertWispEdgeRows(t *testing.T, ctx context.Context, fixture DeleterFixture, want int, dependent, blocker string) {
	t.Helper()
	var got int
	query := "SELECT COUNT(*) FROM wisp_dependencies WHERE issue_id = ? AND " +
		"COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) = ?"
	if err := fixture.QueryScalar(ctx, query, []any{dependent, blocker}, &got); err != nil {
		t.Fatalf("counting wisp edges %s -> %s: %v", dependent, blocker, err)
	}
	if got != want {
		t.Errorf("wisp_dependencies rows %s -> %s = %d, want %d", dependent, blocker, got, want)
	}
}
