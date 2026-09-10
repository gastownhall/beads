package conformance

import (
	"context"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// This file holds the contract every implementation of
// publicops.BlockingAnnotator must satisfy. Each case asserts what
// issueops/blockingannotator.go PROMISES, cited by line, rather than what any
// one backend happens to do today; a backend that disagrees is parked at its own
// wiring site with skipKnownDivergence so the case still runs on the ones that
// agree.
//
// There are three wirings — the server-backed store, the embedded store and the
// unit-of-work provider — and TWO BODIES, not three. dolt and embeddeddolt both
// call storage/issueops.ExecuteBlockingAnnotation inside their own read
// transaction, so they are one vote plus an engine check; the unit-of-work
// provider is the second, and it is a genuinely different body:
// GetBlockingInfoAcrossIssuesAndWisps reads BOTH dependency tiers for every id
// and merges, where the store side partitions the ids by plane and reads each
// one's outbound edges from the tier it lives on.
//
// All three share ValidateBlockingRequest, EdgeReadAnchors and
// FinishBlockingAnnotation, so what these cases can catch below those three is
// the EXECUTION half: which tier each seam reads, which row's status it consults
// to decide an edge is live, and whether the reads see one snapshot.
//
// EVERY CASE NAMES THE EXACT IDS IT SEEDED: the three fixtures share one
// database per suite and the two store fixtures share it with every other
// role's cases, so an assertion about "every annotation" would be an assertion
// about the whole workspace.
//
// Deliberately NOT here: existence, which this role does not probe and has no
// flag for (blockingannotator.go:101-106); the mapping from a page of issues to a
// request, which is the command's job; and the edge TYPES outside `blocks` and
// `parent-child`, which are EdgeReader's answer.

// BlockingAnnotatorFixture supplies adapter-specific storage access for the
// blocking-annotation assertions. Every field is named and typed exactly like
// the per-backend roleFixtureKit hook it is filled from.
type BlockingAnnotatorFixture struct {
	// IssuePrefix namespaces the ids each assertion seeds, so several of them
	// can share one database.
	IssuePrefix string
	Annotator   publicops.BlockingAnnotator
	// CreateIssue seeds a durable issue in the issues plane, carrying the STATUS
	// the closed-blocker cases need.
	CreateIssue func(context.Context, *types.Issue, string) error
	// CreateWisp seeds an ephemeral issue in the wisps plane.
	CreateWisp func(context.Context, *types.Issue, string) error
	// AddDependency seeds ONE edge, routed to the plane the edge's source lives
	// in.
	AddDependency func(context.Context, *types.Dependency, string) error
	// CountHistory reports how many history entries the fixture's branch has.
	// A nil hook means "this backend cannot observe history", and the case that
	// needs it SKIPS with that reason rather than passing quietly.
	CountHistory func(context.Context) (int, error)
}

// RunBlockingAnnotatorAnswersOnePerIDInRequestOrder pins
// blockingannotator.go:69-75: one entry per requested id, in the order the
// request named them.
//
// The ids are seeded in the REVERSE of the order the request asks for them, so a
// body answering in the storage seam's natural ascending-by-id order would fail
// here rather than pass by coincidence.
func RunBlockingAnnotatorAnswersOnePerIDInRequestOrder(t *testing.T, ctx context.Context, fixture BlockingAnnotatorFixture) {
	t.Helper()
	first := fixture.IssuePrefix + "-order-c"
	second := fixture.IssuePrefix + "-order-b"
	third := fixture.IssuePrefix + "-order-a"
	seedBlockingIssues(t, ctx, fixture, first, second, third)

	result := annotateBlocking(t, ctx, fixture, publicops.BlockingRequest{IDs: []string{first, second, third}})
	assertBlockingIDs(t, result, first, second, third)
}

// RunBlockingAnnotatorCollapsesRepeatedIDs pins blockingannotator.go:27-29: an
// id named twice is one entry, at the position of its first mention.
//
// The repeat is placed AFTER a different id, so a body that de-duplicated by
// sorting rather than by first mention would answer b, a instead of a, b.
func RunBlockingAnnotatorCollapsesRepeatedIDs(t *testing.T, ctx context.Context, fixture BlockingAnnotatorFixture) {
	t.Helper()
	first := fixture.IssuePrefix + "-dup-b"
	second := fixture.IssuePrefix + "-dup-a"
	seedBlockingIssues(t, ctx, fixture, first, second)

	result := annotateBlocking(t, ctx, fixture, publicops.BlockingRequest{
		IDs: []string{first, second, first, second, first},
	})
	assertBlockingIDs(t, result, first, second)
}

// RunBlockingAnnotatorReportsOpenBlockersOnly pins blockingannotator.go:41-45:
// BlockedBy carries the targets of the `blocks` edges whose own status is not
// closed.
//
// Both blockers are seeded, so the case separates "dropped because closed" from
// "never read at all".
func RunBlockingAnnotatorReportsOpenBlockersOnly(t *testing.T, ctx context.Context, fixture BlockingAnnotatorFixture) {
	t.Helper()
	blocked := fixture.IssuePrefix + "-open-blocked"
	openBlocker := fixture.IssuePrefix + "-open-live"
	closedBlocker := fixture.IssuePrefix + "-open-done"
	seedBlockingIssues(t, ctx, fixture, blocked, openBlocker)
	seedClosedBlockingIssue(t, ctx, fixture, closedBlocker)
	seedBlockingEdge(t, ctx, fixture, blocked, openBlocker, types.DepBlocks)
	seedBlockingEdge(t, ctx, fixture, blocked, closedBlocker, types.DepBlocks)

	result := annotateBlocking(t, ctx, fixture, publicops.BlockingRequest{IDs: []string{blocked}})
	assertBlockedBy(t, result, blocked, openBlocker)
}

// RunBlockingAnnotatorReportsTheInboundDirection pins
// blockingannotator.go:47-53: Blocks carries the sources of the `blocks` edges
// pointing AT this id, and it is empty when this id is itself closed.
//
// The two halves read the status of DIFFERENT rows to reach the same rule — an
// edge is live exactly when its blocker is open — so an implementation that
// checked the wrong end would pass one half and fail the other.
func RunBlockingAnnotatorReportsTheInboundDirection(t *testing.T, ctx context.Context, fixture BlockingAnnotatorFixture) {
	t.Helper()
	openBlocker := fixture.IssuePrefix + "-inbound-live"
	closedBlocker := fixture.IssuePrefix + "-inbound-done"
	blockedA := fixture.IssuePrefix + "-inbound-a"
	blockedB := fixture.IssuePrefix + "-inbound-b"
	seedBlockingIssues(t, ctx, fixture, openBlocker, blockedA, blockedB)
	seedClosedBlockingIssue(t, ctx, fixture, closedBlocker)
	seedBlockingEdge(t, ctx, fixture, blockedA, openBlocker, types.DepBlocks)
	seedBlockingEdge(t, ctx, fixture, blockedB, openBlocker, types.DepBlocks)
	seedBlockingEdge(t, ctx, fixture, blockedA, closedBlocker, types.DepBlocks)

	result := annotateBlocking(t, ctx, fixture, publicops.BlockingRequest{IDs: []string{openBlocker, closedBlocker}})
	assertBlocks(t, result, openBlocker, blockedA, blockedB)
	// The closed one still HAS an inbound edge; what it does not have is a live one.
	assertBlocks(t, result, closedBlocker)
}

// RunBlockingAnnotatorSeparatesParentFromBlockers pins
// blockingannotator.go:55-56 together with :38-49: a `parent-child` edge is the
// Parent and is NOT a blocker, in either direction.
//
// `parent-child` and `blocks` come back from ONE query in both bodies, so an
// implementation that forgot to split on type would report a child as blocked by
// its parent — which is what the compact listing's status icon reads to decide a
// row is blocked.
func RunBlockingAnnotatorSeparatesParentFromBlockers(t *testing.T, ctx context.Context, fixture BlockingAnnotatorFixture) {
	t.Helper()
	parent := fixture.IssuePrefix + "-parent-up"
	child := fixture.IssuePrefix + "-parent-down"
	seedBlockingIssues(t, ctx, fixture, parent, child)
	seedBlockingEdge(t, ctx, fixture, child, parent, types.DepParentChild)

	result := annotateBlocking(t, ctx, fixture, publicops.BlockingRequest{IDs: []string{child, parent}})
	assertParent(t, result, child, parent)
	assertBlockedBy(t, result, child)
	assertBlocks(t, result, child)
	assertParent(t, result, parent, "")
	assertBlockedBy(t, result, parent)
	assertBlocks(t, result, parent)
}

// RunBlockingAnnotatorDropsAClosedParent pins blockingannotator.go:55-56:
// Parent is empty when the parent it has is closed.
//
// It is the same status rule the blocker arm applies, on the arm where it is
// easiest to forget: the parent is structural rather than blocking, so an
// implementation could reasonably have decided a closed parent is still a parent.
func RunBlockingAnnotatorDropsAClosedParent(t *testing.T, ctx context.Context, fixture BlockingAnnotatorFixture) {
	t.Helper()
	child := fixture.IssuePrefix + "-deadparent-child"
	parent := fixture.IssuePrefix + "-deadparent-up"
	seedBlockingIssues(t, ctx, fixture, child)
	seedClosedBlockingIssue(t, ctx, fixture, parent)
	seedBlockingEdge(t, ctx, fixture, child, parent, types.DepParentChild)

	result := annotateBlocking(t, ctx, fixture, publicops.BlockingRequest{IDs: []string{child}})
	assertParent(t, result, child, "")
}

// RunBlockingAnnotatorOrdersAndCollapsesEachList pins
// blockingannotator.go:129-134: ascending by id, repeats collapsed.
//
// The three blockers are seeded in an order that is neither ascending nor its
// reverse, so an implementation answering in insertion order fails here. Both
// lists are joined into ONE LINE of `bd list` output, so the query's natural
// order is user-visible bytes.
func RunBlockingAnnotatorOrdersAndCollapsesEachList(t *testing.T, ctx context.Context, fixture BlockingAnnotatorFixture) {
	t.Helper()
	blocked := fixture.IssuePrefix + "-sort-src"
	late := fixture.IssuePrefix + "-sort-z"
	middle := fixture.IssuePrefix + "-sort-m"
	early := fixture.IssuePrefix + "-sort-a"
	seedBlockingIssues(t, ctx, fixture, blocked, late, middle, early)
	seedBlockingEdge(t, ctx, fixture, blocked, middle, types.DepBlocks)
	seedBlockingEdge(t, ctx, fixture, blocked, late, types.DepBlocks)
	seedBlockingEdge(t, ctx, fixture, blocked, early, types.DepBlocks)

	result := annotateBlocking(t, ctx, fixture, publicops.BlockingRequest{IDs: []string{blocked}})
	assertBlockedBy(t, result, blocked, early, middle, late)
}

// RunBlockingAnnotatorCountsAnUnresolvableBlockerAsOpen pins
// blockingannotator.go:117-123: a blocker this database holds no row for still
// blocks, because an unreadable status is not `closed`.
//
// Two flavors are seeded — an `external:` reference and a dangling id in another
// repository's prefix — because they take different typed target columns. The
// case exists so that "we could not find the blocker, so the work is ready" can
// never be introduced as an optimization.
func RunBlockingAnnotatorCountsAnUnresolvableBlockerAsOpen(t *testing.T, ctx context.Context, fixture BlockingAnnotatorFixture) {
	t.Helper()
	blocked := fixture.IssuePrefix + "-ghostblocker-src"
	external := "external:" + fixture.IssuePrefix + "-ghostblocker-ext"
	foreign := "zzforeign-" + fixture.IssuePrefix + "-ghostblocker"
	seedBlockingIssues(t, ctx, fixture, blocked)
	seedBlockingEdge(t, ctx, fixture, blocked, external, types.DepBlocks)
	seedBlockingEdge(t, ctx, fixture, blocked, foreign, types.DepBlocks)

	result := annotateBlocking(t, ctx, fixture, publicops.BlockingRequest{IDs: []string{blocked}})
	// Ascending by id, and "external:..." sorts before "zzforeign-...".
	assertBlockedBy(t, result, blocked, external, foreign)
}

// RunBlockingAnnotatorReadsBothPlanes pins blockingannotator.go:125-127: the two
// planes are one graph.
//
// Both directions are exercised from one graph because the outbound read is the
// partitioned one and the inbound read is not.
func RunBlockingAnnotatorReadsBothPlanes(t *testing.T, ctx context.Context, fixture BlockingAnnotatorFixture) {
	t.Helper()
	wisp := fixture.IssuePrefix + "-plane-wisp"
	durable := fixture.IssuePrefix + "-plane-durable"
	seedBlockingIssues(t, ctx, fixture, durable)
	seedBlockingWisp(t, ctx, fixture, wisp)
	seedBlockingEdge(t, ctx, fixture, wisp, durable, types.DepBlocks)

	result := annotateBlocking(t, ctx, fixture, publicops.BlockingRequest{IDs: []string{wisp, durable}})
	assertBlockedBy(t, result, wisp, durable)
	assertBlocks(t, result, durable, wisp)
}

// RunBlockingAnnotatorIgnoresNonBlockingEdgeTypes pins the narrowing at
// blockingannotator.go:85-91: this role answers about two edge types out of the
// whole vocabulary, and the rest are EdgeReader's. An implementation that
// annotated every stored edge would print half a knowledge graph beside every
// listing row.
func RunBlockingAnnotatorIgnoresNonBlockingEdgeTypes(t *testing.T, ctx context.Context, fixture BlockingAnnotatorFixture) {
	t.Helper()
	anchor := fixture.IssuePrefix + "-type-src"
	related := fixture.IssuePrefix + "-type-related"
	blocker := fixture.IssuePrefix + "-type-blocker"
	seedBlockingIssues(t, ctx, fixture, anchor, related, blocker)
	seedBlockingEdge(t, ctx, fixture, anchor, related, types.DepRelated)
	seedBlockingEdge(t, ctx, fixture, anchor, blocker, types.DepBlocks)

	result := annotateBlocking(t, ctx, fixture, publicops.BlockingRequest{IDs: []string{anchor, related}})
	assertBlockedBy(t, result, anchor, blocker)
	assertBlocks(t, result, related)
}

// RunBlockingAnnotatorAnnotatesAnAbsentIDBare pins blockingannotator.go:101-106
// — the decision NOT to probe existence.
//
// It asserts the ABSENCE of a distinction, which is why it seeds both halves: an
// id that exists with no live edges and an id that exists nowhere must be
// indistinguishable in the answer. The never-nil clause at :41-42 and :49 is
// checked on the same answer, because a bare entry is exactly where a nil slice
// would survive.
func RunBlockingAnnotatorAnnotatesAnAbsentIDBare(t *testing.T, ctx context.Context, fixture BlockingAnnotatorFixture) {
	t.Helper()
	bare := fixture.IssuePrefix + "-bare-present"
	ghost := fixture.IssuePrefix + "-bare-absent"
	seedBlockingIssues(t, ctx, fixture, bare)

	result := annotateBlocking(t, ctx, fixture, publicops.BlockingRequest{IDs: []string{bare, ghost}})
	assertBlockingIDs(t, result, bare, ghost)
	for _, id := range []string{bare, ghost} {
		assertBlockedBy(t, result, id)
		assertBlocks(t, result, id)
		assertParent(t, result, id, "")
		entry := blockingEntry(t, result, id)
		if entry.BlockedBy == nil || entry.Blocks == nil {
			t.Errorf("entry %s has a nil list (BlockedBy=%v Blocks=%v); the contract promises empty slices",
				id, entry.BlockedBy, entry.Blocks)
		}
	}
}

// RunBlockingAnnotatorResolvesExactIDsOnly pins blockingannotator.go:14-18: a
// prefix of a real id, and an id carrying surrounding whitespace, annotate as
// nothing rather than resolving.
//
// They are bare entries rather than errors, which is this role's spelling of the
// same promise EdgeReadRequest.IDs makes. With no miss flag, a resolution would
// be silent.
func RunBlockingAnnotatorResolvesExactIDsOnly(t *testing.T, ctx context.Context, fixture BlockingAnnotatorFixture) {
	t.Helper()
	blocked := fixture.IssuePrefix + "-exact-blocked"
	blocker := fixture.IssuePrefix + "-exact-blocker"
	seedBlockingIssues(t, ctx, fixture, blocked, blocker)
	seedBlockingEdge(t, ctx, fixture, blocked, blocker, types.DepBlocks)

	prefix := blocked[:len(blocked)-2]
	spaced := " " + blocked + " "
	result := annotateBlocking(t, ctx, fixture, publicops.BlockingRequest{
		IDs: []string{blocked, prefix, spaced},
	})
	assertBlockingIDs(t, result, blocked, prefix, spaced)
	assertBlockedBy(t, result, blocked, blocker)
	assertBlockedBy(t, result, prefix)
	assertBlockedBy(t, result, spaced)
}

// RunBlockingAnnotatorReportsAtMostOneParent pins blockingannotator.go:58-63:
// one parent is reported, and the contract deliberately does not say which.
//
// The case asserts only what is promised — exactly one, and one of the two
// seeded — because both bodies reduce to a single parent before the shared
// epilogue sees the rows. It SKIPS when the fixture cannot build the state at
// all rather than passing quietly: a case that silently exercised one parent
// would read as coverage it is not.
func RunBlockingAnnotatorReportsAtMostOneParent(t *testing.T, ctx context.Context, fixture BlockingAnnotatorFixture) {
	t.Helper()
	child := fixture.IssuePrefix + "-twoparents-child"
	parentA := fixture.IssuePrefix + "-twoparents-a"
	parentB := fixture.IssuePrefix + "-twoparents-b"
	seedBlockingIssues(t, ctx, fixture, child, parentA, parentB)
	seedBlockingEdge(t, ctx, fixture, child, parentA, types.DepParentChild)
	if err := fixture.AddDependency(ctx, &types.Dependency{
		IssueID: child, DependsOnID: parentB, Type: types.DepParentChild,
	}, "blocking-annotator-seed"); err != nil {
		t.Skipf("this backend refuses a second parent-child edge (%v), so an issue with two open parents cannot be built here", err)
	}

	result := annotateBlocking(t, ctx, fixture, publicops.BlockingRequest{IDs: []string{child}})
	parent := blockingEntry(t, result, child).Parent
	if parent != parentA && parent != parentB {
		t.Fatalf("child %s parent = %q, want one of %q or %q", child, parent, parentA, parentB)
	}
}

// RunBlockingAnnotatorAnswersAnEmptyRequest pins blockingannotator.go:24-25: no
// ids is not an error, it is an answer with no annotations.
func RunBlockingAnnotatorAnswersAnEmptyRequest(t *testing.T, ctx context.Context, fixture BlockingAnnotatorFixture) {
	t.Helper()
	result, err := fixture.Annotator.AnnotateBlocking(ctx, publicops.BlockingRequest{})
	if err != nil {
		t.Fatalf("AnnotateBlocking with no ids = %v, want an empty answer", err)
	}
	if len(result.Items) != 0 {
		t.Fatalf("AnnotateBlocking with no ids returned %d entries, want none", len(result.Items))
	}
	if result.Items == nil {
		t.Error("Items is nil; the contract promises it is never nil for a successful call")
	}
}

// RunBlockingAnnotatorRefusesAnEmptyID pins blockingannotator.go:20-22: the
// empty string is ErrValidation rather than a nameless annotation.
//
// The refusal must beat the good id beside it: a body that answered for the ids
// it could would leave the caller an entry it has no name for, and with no miss
// flag there is nothing else in the answer that would say so.
func RunBlockingAnnotatorRefusesAnEmptyID(t *testing.T, ctx context.Context, fixture BlockingAnnotatorFixture) {
	t.Helper()
	anchor := fixture.IssuePrefix + "-emptyid-anchor"
	seedBlockingIssues(t, ctx, fixture, anchor)

	_, err := fixture.Annotator.AnnotateBlocking(ctx, publicops.BlockingRequest{IDs: []string{anchor, ""}})
	if !errors.Is(err, publicops.ErrValidation) {
		t.Fatalf("AnnotateBlocking with an empty id = %v, want ErrValidation", err)
	}
}

// RunBlockingAnnotatorLeavesTheRequestAlone pins the no-mutation clause at
// blockingannotator.go:136-140. IDs is the one member a body could write through
// to the caller, and de-duplicating and sorting are exactly the steps that
// would.
func RunBlockingAnnotatorLeavesTheRequestAlone(t *testing.T, ctx context.Context, fixture BlockingAnnotatorFixture) {
	t.Helper()
	late := fixture.IssuePrefix + "-immutable-z"
	early := fixture.IssuePrefix + "-immutable-a"
	seedBlockingIssues(t, ctx, fixture, late, early)

	ids := []string{late, late, early}
	annotateBlocking(t, ctx, fixture, publicops.BlockingRequest{IDs: ids})

	if len(ids) != 3 || ids[0] != late || ids[1] != late || ids[2] != early {
		t.Errorf("the request's IDs slice is now %v; the contract says a body de-duplicates into its own copy", ids)
	}
}

// RunBlockingAnnotatorWritesNothing pins blockingannotator.go:142-145:
// annotating records no history entry.
//
// The delta is taken around the call rather than as an absolute count, because
// the seeds above it are versioned writes of their own.
func RunBlockingAnnotatorWritesNothing(t *testing.T, ctx context.Context, fixture BlockingAnnotatorFixture) {
	t.Helper()
	if fixture.CountHistory == nil {
		t.Skip("this backend cannot observe history, so the writes-nothing clause cannot be checked here")
	}
	blocked := fixture.IssuePrefix + "-quiet-blocked"
	blocker := fixture.IssuePrefix + "-quiet-blocker"
	ghost := fixture.IssuePrefix + "-quiet-ghost"
	seedBlockingIssues(t, ctx, fixture, blocked, blocker)
	seedBlockingEdge(t, ctx, fixture, blocked, blocker, types.DepBlocks)

	before, err := fixture.CountHistory(ctx)
	if err != nil {
		t.Fatalf("CountHistory before: %v", err)
	}
	annotateBlocking(t, ctx, fixture, publicops.BlockingRequest{IDs: []string{blocked, ghost}})
	// A refusal changes nothing either, so the same delta covers both.
	_, _ = fixture.Annotator.AnnotateBlocking(ctx, publicops.BlockingRequest{IDs: []string{""}})
	after, err := fixture.CountHistory(ctx)
	if err != nil {
		t.Fatalf("CountHistory after: %v", err)
	}
	if after != before {
		t.Fatalf("history entries went %d -> %d across two annotations, want no change", before, after)
	}
}

func annotateBlocking(t *testing.T, ctx context.Context, fixture BlockingAnnotatorFixture, request publicops.BlockingRequest) publicops.BlockingResult {
	t.Helper()
	result, err := fixture.Annotator.AnnotateBlocking(ctx, request)
	if err != nil {
		t.Fatalf("AnnotateBlocking(%v): %v", request.IDs, err)
	}
	return result
}

func seedBlockingIssues(t *testing.T, ctx context.Context, fixture BlockingAnnotatorFixture, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if err := fixture.CreateIssue(ctx, blockingSeed(id, types.StatusOpen, false), "blocking-annotator-seed"); err != nil {
			t.Fatalf("seed issue %s: %v", id, err)
		}
	}
}

// seedClosedBlockingIssue seeds a durable issue that is already closed. The
// status arrives with the row rather than through a close, so the cases that
// assert the status rule do not also depend on a lifecycle role.
func seedClosedBlockingIssue(t *testing.T, ctx context.Context, fixture BlockingAnnotatorFixture, id string) {
	t.Helper()
	if err := fixture.CreateIssue(ctx, blockingSeed(id, types.StatusClosed, false), "blocking-annotator-seed"); err != nil {
		t.Fatalf("seed closed issue %s: %v", id, err)
	}
}

func seedBlockingWisp(t *testing.T, ctx context.Context, fixture BlockingAnnotatorFixture, id string) {
	t.Helper()
	if err := fixture.CreateWisp(ctx, blockingSeed(id, types.StatusOpen, true), "blocking-annotator-seed"); err != nil {
		t.Fatalf("seed wisp %s: %v", id, err)
	}
}

func seedBlockingEdge(t *testing.T, ctx context.Context, fixture BlockingAnnotatorFixture, from, to string, depType types.DependencyType) {
	t.Helper()
	if err := fixture.AddDependency(ctx, &types.Dependency{
		IssueID: from, DependsOnID: to, Type: depType,
	}, "blocking-annotator-seed"); err != nil {
		t.Fatalf("seed edge %s -> %s (%s): %v", from, to, depType, err)
	}
}

func blockingSeed(id string, status types.Status, ephemeral bool) *types.Issue {
	return &types.Issue{
		ID:        id,
		Title:     id,
		Status:    status,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: ephemeral,
	}
}

func blockingEntry(t *testing.T, result publicops.BlockingResult, id string) publicops.IssueBlocking {
	t.Helper()
	for _, entry := range result.Items {
		if entry.ID == id {
			return entry
		}
	}
	t.Fatalf("no entry %q in the answer; got %v", id, blockingIDs(result))
	return publicops.IssueBlocking{}
}

func blockingIDs(result publicops.BlockingResult) []string {
	out := make([]string, 0, len(result.Items))
	for _, entry := range result.Items {
		out = append(out, entry.ID)
	}
	return out
}

func assertBlockingIDs(t *testing.T, result publicops.BlockingResult, want ...string) {
	t.Helper()
	got := blockingIDs(result)
	if len(got) != len(want) {
		t.Fatalf("annotated ids = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("annotated ids = %v, want %v", got, want)
		}
	}
}

// assertBlockedBy compares one entry's blockers IN ORDER, so one helper serves
// both the "which blockers" and the "in what order" assertions.
func assertBlockedBy(t *testing.T, result publicops.BlockingResult, id string, want ...string) {
	t.Helper()
	assertBlockingList(t, "blocked by", id, blockingEntry(t, result, id).BlockedBy, want)
}

func assertBlocks(t *testing.T, result publicops.BlockingResult, id string, want ...string) {
	t.Helper()
	assertBlockingList(t, "blocks", id, blockingEntry(t, result, id).Blocks, want)
}

func assertBlockingList(t *testing.T, label, id string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("entry %s %s = %v, want %v", id, label, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("entry %s %s = %v, want %v", id, label, got, want)
		}
	}
}

func assertParent(t *testing.T, result publicops.BlockingResult, id, want string) {
	t.Helper()
	if got := blockingEntry(t, result, id).Parent; got != want {
		t.Errorf("entry %s parent = %q, want %q", id, got, want)
	}
}
