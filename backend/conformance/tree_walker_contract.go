package conformance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	storeops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// This file holds the contract every implementation of publicops.TreeWalker
// must satisfy. Each case asserts what issueops/treewalker.go PROMISES, cited by
// line, rather than what any one backend happens to do today; a backend that
// disagrees is parked at its own wiring site with skipKnownDivergence so the
// case still runs on the ones that agree.
//
// THERE ARE THREE WIRINGS AND ONE BODY, which is unusual enough to state at the
// top. Every other role in this directory has at least two genuinely different
// implementations to compare; this one does not. All three reach
// storage/issueops.WalkDependencyTreeInTx — the two stores wrap it in five lines
// around their own transaction, the unit of work reaches it through the domain
// repository — so a three-leg run is ONE VOTE plus an engine check, and these
// cases are not evidence that two independent readings of the walk agree.
//
// What the three legs DO buy, and it is not nothing: every measured drift in the
// graph family has lived in the WRAPPERS rather than in the walk. The two stores
// differ in how they reach a transaction and the unit of work differs in whether
// it opens one at all, and a wrapper that lost the transaction, dropped the
// request, or swallowed a typed refusal would fail here on exactly one leg.
// Several cases are written to make that visible — the refusals assert the
// SENTINEL rather than the message, and the cap case asserts the typed fields —
// because those are what a wrapper actually mangles.
//
// The parts that decide what the answer MEANS below the transaction — the
// request vocabulary, the ancestor-keeping prune, the two-walk concatenation —
// are PURE and are pinned without a database in
// internal/storage/issueops/tree_walk_test.go. What these cases add is
// everything the pure tests cannot see: which planes the walk reads, which edge
// types it follows, whether the depth bound is applied to the descent, whether a
// cycle terminates, and whether the root probe and the walk see one state.
//
// EVERY CASE NAMES THE EXACT IDS IT SEEDED. The three fixtures share one
// database per suite and the two store fixtures share it with every other role's
// cases, so an assertion about "the tree" would be an assertion about the whole
// workspace.

// TreeWalkerFixture supplies adapter-specific storage access for the
// dependency-tree assertions. Every field but Exec is named and typed exactly
// like the per-backend roleFixtureKit hook it is filled from, so a wiring is kit
// plus accessor plus prefix with no adapter in between.
type TreeWalkerFixture struct {
	// IssuePrefix namespaces the ids each assertion seeds, so several of them
	// can share one database.
	IssuePrefix string
	TreeWalker  publicops.TreeWalker
	// CreateIssue seeds a durable issue in the issues plane.
	CreateIssue func(context.Context, *types.Issue, string) error
	// CreateWisp seeds an ephemeral issue in the wisps plane.
	CreateWisp func(context.Context, *types.Issue, string) error
	// AddDependency seeds ONE edge, routed to the plane the edge's source lives
	// in. It goes through the storage seam, so it REFUSES a cycle — which is why
	// the cycle case below needs Exec instead.
	AddDependency func(context.Context, *types.Dependency, string) error
	// Exec runs every statement IN ORDER ON ONE SESSION, and is the only way to
	// seed a CYCLE: every supported write path refuses to create one, so there is
	// no verb that produces the state the cycle case walks. It is NOT a
	// roleFixtureKit hook — the kit is frozen and exposes reads only — so each
	// wiring supplies its own short closure over the raw SQL access that
	// backend's tests already use.
	//
	// A nil Exec means "this backend cannot seed a cycle", and the case that
	// needs one SKIPS with that reason rather than passing quietly.
	Exec func(ctx context.Context, statements []SQLStatement) error
	// CountHistory reports how many history entries the fixture's branch has. A
	// nil hook means "this backend cannot observe history", and the case that
	// needs it SKIPS with that reason.
	CountHistory func(context.Context) (int, error)
}

// RunTreeWalkerWalksTheDependenciesOfARoot pins the shape of an ordinary
// answer: treewalker.go:151-159 (depth-first pre-order, root first),
// :144-149 (Depth and ParentID are how the shape is read) and
// treewalker.go:269-275 (a root is always the first node).
//
// The chain is three deep so pre-order says something a two-node tree could not:
// the grandchild must follow the child, not merely appear.
func RunTreeWalkerWalksTheDependenciesOfARoot(t *testing.T, ctx context.Context, fixture TreeWalkerFixture) {
	t.Helper()
	root := fixture.IssuePrefix + "-down-root"
	child := fixture.IssuePrefix + "-down-child"
	grandchild := fixture.IssuePrefix + "-down-grandchild"
	seedTreeWalkerIssues(t, ctx, fixture, root, child, grandchild)
	seedTreeWalkerEdge(t, ctx, fixture, root, child, types.DepBlocks)
	seedTreeWalkerEdge(t, ctx, fixture, child, grandchild, types.DepBlocks)

	result := walkTree(t, ctx, fixture, publicops.WalkTreeRequest{RootID: root, MaxDepth: 10})
	assertTreeWalkerIDs(t, result, root, child, grandchild)

	byID := treeWalkerByID(t, result)
	assertTreeWalkerNode(t, byID, root, 0, "", "")
	assertTreeWalkerNode(t, byID, child, 1, root, types.DepBlocks)
	assertTreeWalkerNode(t, byID, grandchild, 2, child, types.DepBlocks)
	// treewalker.go:17-23: nothing sets Truncated, on any node, ever.
	for _, node := range result.Nodes {
		if node.Truncated {
			t.Errorf("node %s carries Truncated = true; treewalker.go:17-23 says no implementation sets it", node.ID)
		}
	}
}

// RunTreeWalkerWalksDependentsWhenAskedUp pins treewalker.go:34-37: the up
// direction follows what depends ON the root.
//
// The same graph as the down case read from the other end, so a body that
// ignored Direction and always walked one way answers with one node here and
// fails.
func RunTreeWalkerWalksDependentsWhenAskedUp(t *testing.T, ctx context.Context, fixture TreeWalkerFixture) {
	t.Helper()
	blocker := fixture.IssuePrefix + "-up-blocker"
	dependent := fixture.IssuePrefix + "-up-dependent"
	seedTreeWalkerIssues(t, ctx, fixture, blocker, dependent)
	seedTreeWalkerEdge(t, ctx, fixture, dependent, blocker, types.DepBlocks)

	up := walkTree(t, ctx, fixture, publicops.WalkTreeRequest{
		RootID: blocker, MaxDepth: 10, Direction: publicops.TreeUp,
	})
	assertTreeWalkerIDs(t, up, blocker, dependent)

	// And the same root walked DOWN reaches nothing, which is what makes the
	// assertion above about direction rather than about reachability.
	down := walkTree(t, ctx, fixture, publicops.WalkTreeRequest{
		RootID: blocker, MaxDepth: 10, Direction: publicops.TreeDown,
	})
	assertTreeWalkerIDs(t, down, blocker)
}

// RunTreeWalkerBoundsTheDescentAtMaxDepth pins treewalker.go:75-77 (the root is
// level one) and :87-92 (a node beyond the bound is ABSENT, not present and
// marked).
//
// Both halves matter. A body that counted levels from zero would return two
// nodes for MaxDepth 1; a body that walked the whole graph and then truncated
// the list would return the same ids but would have paid for the whole walk, and
// the Truncated assertion is the only thing that would tell.
func RunTreeWalkerBoundsTheDescentAtMaxDepth(t *testing.T, ctx context.Context, fixture TreeWalkerFixture) {
	t.Helper()
	root := fixture.IssuePrefix + "-depth-root"
	child := fixture.IssuePrefix + "-depth-child"
	grandchild := fixture.IssuePrefix + "-depth-grandchild"
	seedTreeWalkerIssues(t, ctx, fixture, root, child, grandchild)
	seedTreeWalkerEdge(t, ctx, fixture, root, child, types.DepBlocks)
	seedTreeWalkerEdge(t, ctx, fixture, child, grandchild, types.DepBlocks)

	assertTreeWalkerIDs(t, walkTree(t, ctx, fixture, publicops.WalkTreeRequest{RootID: root, MaxDepth: 1}), root)
	assertTreeWalkerIDs(t, walkTree(t, ctx, fixture, publicops.WalkTreeRequest{RootID: root, MaxDepth: 2}), root, child)

	bounded := walkTree(t, ctx, fixture, publicops.WalkTreeRequest{RootID: root, MaxDepth: 2})
	for _, node := range bounded.Nodes {
		if node.Truncated {
			t.Errorf("node %s carries Truncated = true at a bound that cut the walk; treewalker.go:87-92 says the cut is invisible", node.ID)
		}
	}
}

// RunTreeWalkerTerminatesOnACycle pins treewalker.go:165-169: revisiting a node
// stops the descent, so a cyclic graph is answered rather than hung, and no node
// is repeated.
//
// SEEDING ONE TAKES RAW SQL, and that is a fact about the system rather than a
// shortcut — the same fact the cycle-detector contract records. Every supported
// write refuses to create a cycle, so there is no verb that produces the state
// this case walks.
//
// It also pins the one thing that distinguishes this role from CycleDetector on
// the same graph: a cycle here is not an ANSWER and not an error. The walk
// simply ends.
func RunTreeWalkerTerminatesOnACycle(t *testing.T, ctx context.Context, fixture TreeWalkerFixture) {
	t.Helper()
	a := fixture.IssuePrefix + "-cyc-a"
	b := fixture.IssuePrefix + "-cyc-b"
	c := fixture.IssuePrefix + "-cyc-c"
	seedTreeWalkerIssues(t, ctx, fixture, a, b, c)
	seedTreeWalkerCycleEdges(t, ctx, fixture,
		treeWalkerEdge{Source: a, Target: b},
		treeWalkerEdge{Source: b, Target: c},
		treeWalkerEdge{Source: c, Target: a})

	result := walkTree(t, ctx, fixture, publicops.WalkTreeRequest{RootID: a, MaxDepth: 50})
	assertTreeWalkerIDs(t, result, a, b, c)
}

// RunTreeWalkerRendersASharedSubtreeOnce pins treewalker.go:170-178: a diamond
// shows the shared child under whichever parent the walk reached first, and
// there is no option to show it twice.
//
// This is the clause `--show-all-paths` would have changed, and the case exists
// so that implementing that flag as a side effect of some later refactor fails
// here rather than shipping.
func RunTreeWalkerRendersASharedSubtreeOnce(t *testing.T, ctx context.Context, fixture TreeWalkerFixture) {
	t.Helper()
	root := fixture.IssuePrefix + "-diamond-root"
	left := fixture.IssuePrefix + "-diamond-left"
	right := fixture.IssuePrefix + "-diamond-right"
	shared := fixture.IssuePrefix + "-diamond-shared"
	seedTreeWalkerIssues(t, ctx, fixture, root, left, right, shared)
	seedTreeWalkerEdge(t, ctx, fixture, root, left, types.DepBlocks)
	seedTreeWalkerEdge(t, ctx, fixture, root, right, types.DepBlocks)
	seedTreeWalkerEdge(t, ctx, fixture, left, shared, types.DepBlocks)
	seedTreeWalkerEdge(t, ctx, fixture, right, shared, types.DepBlocks)

	result := walkTree(t, ctx, fixture, publicops.WalkTreeRequest{RootID: root, MaxDepth: 10})
	if got := treeWalkerCount(result, shared); got != 1 {
		t.Fatalf("the shared node appears %d times, want 1: treewalker.go:170-178 forbids a second path", got)
	}
	assertTreeWalkerIDSet(t, result, root, left, right, shared)
}

// RunTreeWalkerMergesTheDurableAndEphemeralPlanes pins treewalker.go:234-238:
// an ephemeral step in the middle of a chain does not end the picture.
//
// This is the clause a single-table walk passes every other case in this file
// and fails here.
func RunTreeWalkerMergesTheDurableAndEphemeralPlanes(t *testing.T, ctx context.Context, fixture TreeWalkerFixture) {
	t.Helper()
	root := fixture.IssuePrefix + "-plane-root"
	wisp := fixture.IssuePrefix + "-plane-wisp"
	tail := fixture.IssuePrefix + "-plane-tail"
	seedTreeWalkerIssues(t, ctx, fixture, root, tail)
	seedTreeWalkerWisp(t, ctx, fixture, wisp)
	seedTreeWalkerEdge(t, ctx, fixture, root, wisp, types.DepBlocks)
	seedTreeWalkerEdge(t, ctx, fixture, wisp, tail, types.DepBlocks)

	result := walkTree(t, ctx, fixture, publicops.WalkTreeRequest{RootID: root, MaxDepth: 10})
	assertTreeWalkerIDs(t, result, root, wisp, tail)
}

// RunTreeWalkerFollowsEveryTypeButRelatesTo pins treewalker.go:220-233: the walk
// is WIDER than the cycle report's — it follows a non-blocking type — and it
// excludes `relates-to` alone.
//
// The two edges hang off the same root so one call proves both halves, and the
// `related` edge is deliberately present beside the `relates-to` one: they read
// as synonyms, only the second is excluded, and treewalker.go:226-233 says so
// because nothing else would have caught the confusion.
func RunTreeWalkerFollowsEveryTypeButRelatesTo(t *testing.T, ctx context.Context, fixture TreeWalkerFixture) {
	t.Helper()
	root := fixture.IssuePrefix + "-types-root"
	discovered := fixture.IssuePrefix + "-types-discovered"
	related := fixture.IssuePrefix + "-types-related"
	relatesTo := fixture.IssuePrefix + "-types-relatesto"
	seedTreeWalkerIssues(t, ctx, fixture, root, discovered, related, relatesTo)
	seedTreeWalkerEdge(t, ctx, fixture, root, discovered, types.DepDiscoveredFrom)
	seedTreeWalkerEdge(t, ctx, fixture, root, related, types.DepRelated)
	seedTreeWalkerEdge(t, ctx, fixture, root, relatesTo, types.DepRelatesTo)

	result := walkTree(t, ctx, fixture, publicops.WalkTreeRequest{RootID: root, MaxDepth: 10})
	assertTreeWalkerIDSet(t, result, root, discovered, related)
}

// RunTreeWalkerPrunesEachHalfOfABothWalk combines Status with TreeBoth, which
// no case did.
//
// The two are independently covered and their INTERACTION was the gap. A `both`
// answer may legitimately carry one id TWICE with a DIFFERENT ParentID in each
// half; the prune keyed its parent chain by id, so one last-write-wins map
// answered for two different facts and a survivor in the up half had its
// ancestors looked up through the DOWN half's parents. The real ancestors were
// dropped and the answer came back with nodes whose ParentID named something
// absent — the scatter of orphans the prune promise exists to prevent.
//
// THE FIXTURE IS THE POINT. `mid` must reach the root by one path walking down
// and a different path walking up, which takes a three-node ring:
//
//	root --parent-child--> mid --discovered-from--> side --blocks--> root
//
// Walking DOWN, mid's parent is the root; walking UP it is side. The ring is
// not a blocking cycle — one edge is discovered-from — so the ordinary
// dependency writer accepts it, no raw SQL required. `leaf` is the closed row
// that survives the prune, hanging off mid in the UP half only.
//
// THE ASSERTION IS THE INVARIANT, not a node list: every ParentID must name a
// node that is present. That is what a renderer rebuilding the shape from Depth
// and ParentID depends on, whichever half a survivor came from.
func RunTreeWalkerPrunesEachHalfOfABothWalk(t *testing.T, ctx context.Context, fixture TreeWalkerFixture) {
	t.Helper()
	root := fixture.IssuePrefix + "-bothprune-root"
	mid := fixture.IssuePrefix + "-bothprune-mid"
	side := fixture.IssuePrefix + "-bothprune-side"
	leaf := fixture.IssuePrefix + "-bothprune-leaf"
	seedTreeWalkerIssues(t, ctx, fixture, root, mid, side)
	seedTreeWalkerIssueWithStatus(t, ctx, fixture, leaf, types.StatusClosed)

	seedTreeWalkerEdge(t, ctx, fixture, root, mid, types.DepParentChild)
	seedTreeWalkerEdge(t, ctx, fixture, mid, side, types.DepDiscoveredFrom)
	seedTreeWalkerEdge(t, ctx, fixture, side, root, types.DepBlocks)
	seedTreeWalkerEdge(t, ctx, fixture, leaf, mid, types.DepBlocks)

	result := walkTree(t, ctx, fixture, publicops.WalkTreeRequest{
		RootID: root, MaxDepth: 10, Direction: publicops.TreeBoth, Status: types.StatusClosed,
	})

	present := make(map[string]bool, len(result.Nodes))
	for _, node := range result.Nodes {
		present[node.ID] = true
	}
	for _, node := range result.Nodes {
		if node.ParentID != "" && !present[node.ParentID] {
			t.Errorf("node %s names parent %s, which is not in the answer: a pruned tree must not "+
				"return orphans (present: %v)", node.ID, node.ParentID, treeWalkerIDs(result))
		}
	}
	if !present[leaf] {
		t.Errorf("the closed row is missing from the pruned `both` answer: %v", treeWalkerIDs(result))
	}
	if !present[root] {
		t.Errorf("the root is missing though a survivor chains to it: %v", treeWalkerIDs(result))
	}
}

// RunTreeWalkerAnswersBothDirectionsWithTheRootOnce pins treewalker.go:179-189:
// a `both` walk concatenates the up half without its root and the down half
// with it, so the root appears exactly once.
func RunTreeWalkerAnswersBothDirectionsWithTheRootOnce(t *testing.T, ctx context.Context, fixture TreeWalkerFixture) {
	t.Helper()
	root := fixture.IssuePrefix + "-both-root"
	blocker := fixture.IssuePrefix + "-both-blocker"
	dependent := fixture.IssuePrefix + "-both-dependent"
	seedTreeWalkerIssues(t, ctx, fixture, root, blocker, dependent)
	seedTreeWalkerEdge(t, ctx, fixture, root, blocker, types.DepBlocks)
	seedTreeWalkerEdge(t, ctx, fixture, dependent, root, types.DepBlocks)

	result := walkTree(t, ctx, fixture, publicops.WalkTreeRequest{
		RootID: root, MaxDepth: 10, Direction: publicops.TreeBoth,
	})
	if got := treeWalkerCount(result, root); got != 1 {
		t.Fatalf("the root appears %d times in a `both` answer, want exactly 1", got)
	}
	assertTreeWalkerIDSet(t, result, root, blocker, dependent)
	// The up half comes first, root excluded, then the whole down tree. Asserting
	// the ORDER is what tells a concatenation from a set union.
	assertTreeWalkerIDs(t, result, dependent, root, blocker)
}

// RunTreeWalkerPrunesByStatusKeepingAncestors pins treewalker.go:98-103: the
// prune runs AFTER the walk, so a match behind a non-match is still reached and
// its non-matching ancestor is kept to keep the answer a tree.
func RunTreeWalkerPrunesByStatusKeepingAncestors(t *testing.T, ctx context.Context, fixture TreeWalkerFixture) {
	t.Helper()
	root := fixture.IssuePrefix + "-prune-root"
	mid := fixture.IssuePrefix + "-prune-mid"
	deep := fixture.IssuePrefix + "-prune-deep"
	seedTreeWalkerIssues(t, ctx, fixture, root, deep)
	seedTreeWalkerIssueWithStatus(t, ctx, fixture, mid, types.StatusClosed)
	seedTreeWalkerEdge(t, ctx, fixture, root, mid, types.DepBlocks)
	seedTreeWalkerEdge(t, ctx, fixture, mid, deep, types.DepBlocks)

	result := walkTree(t, ctx, fixture, publicops.WalkTreeRequest{
		RootID: root, MaxDepth: 10, Status: types.StatusOpen,
	})
	assertTreeWalkerIDs(t, result, root, mid, deep)
}

// RunTreeWalkerPrunesEverythingWhenNothingMatches pins treewalker.go:104-109 —
// the sharp edge. The root is kept only as somebody's ancestor, so a tree with
// no matching member comes back with NO nodes and a nil error.
//
// It is a separate case from the one above because it is the half a caller is
// most likely to be surprised by, and because "empty and successful" is exactly
// the answer a body that failed the prune could also produce.
func RunTreeWalkerPrunesEverythingWhenNothingMatches(t *testing.T, ctx context.Context, fixture TreeWalkerFixture) {
	t.Helper()
	root := fixture.IssuePrefix + "-nomatch-root"
	child := fixture.IssuePrefix + "-nomatch-child"
	seedTreeWalkerIssues(t, ctx, fixture, root, child)
	seedTreeWalkerEdge(t, ctx, fixture, root, child, types.DepBlocks)

	result := walkTree(t, ctx, fixture, publicops.WalkTreeRequest{
		RootID: root, MaxDepth: 10, Status: types.StatusClosed,
	})
	if len(result.Nodes) != 0 {
		t.Fatalf("a prune that matched nothing kept %d nodes, want 0: treewalker.go:104-109", len(result.Nodes))
	}
	if result.Nodes == nil {
		t.Error("Nodes is nil, want an empty slice: treewalker.go:145-146 says never nil for a successful call")
	}
}

// RunTreeWalkerAnswersARootWithNoEdges pins treewalker.go:269-275: a root that
// depends on nothing is a ONE-NODE tree and a nil error, which is what lets a
// caller tell it from a root that is not there.
func RunTreeWalkerAnswersARootWithNoEdges(t *testing.T, ctx context.Context, fixture TreeWalkerFixture) {
	t.Helper()
	lonely := fixture.IssuePrefix + "-lonely"
	seedTreeWalkerIssues(t, ctx, fixture, lonely)

	result := walkTree(t, ctx, fixture, publicops.WalkTreeRequest{RootID: lonely, MaxDepth: 10})
	assertTreeWalkerIDs(t, result, lonely)
}

// RunTreeWalkerRefusesAnAbsentRoot pins treewalker.go:63-66: a root that names
// nothing in either plane is ErrNotFound, not an empty tree.
//
// It asserts the SENTINEL rather than the message, which is the assertion a
// wrapper can actually fail: the unit-of-work leg reaches the same body through
// a repository whose siblings all wrap their errors, and a wrap that broke
// errors.Is would leave both front doors unable to tell a miss from a fault.
func RunTreeWalkerRefusesAnAbsentRoot(t *testing.T, ctx context.Context, fixture TreeWalkerFixture) {
	t.Helper()
	ghost := fixture.IssuePrefix + "-ghost-root"

	_, err := fixture.TreeWalker.WalkTree(ctx, publicops.WalkTreeRequest{RootID: ghost, MaxDepth: 10})
	if err == nil {
		t.Fatalf("WalkTree(%s) succeeded, want ErrNotFound: no such issue or wisp", ghost)
	}
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("WalkTree(%s) error = %v, want ErrNotFound", ghost, err)
	}
}

// RunTreeWalkerResolvesTheRootIDExactly pins treewalker.go:52-66: a case
// variant, surrounding whitespace, a prefix and a suffixed id are all misses.
//
// RunTreeWalkerRefusesAnAbsentRoot cannot stand in for it — a ghost id with no
// near-real sibling is satisfied by exact, prefix, fuzzy and collation-loose
// resolution alike. The case variant is the entry that earns this: it is the
// same string under a case-insensitive collation, so the answer comes from the
// engine rather than the role. A loose root here is a wrong GRAPH, not a wrong
// row.
func RunTreeWalkerResolvesTheRootIDExactly(t *testing.T, ctx context.Context, fixture TreeWalkerFixture) {
	t.Helper()
	root := fixture.IssuePrefix + "-exactroot"
	child := fixture.IssuePrefix + "-exactroot-child"
	seedTreeWalkerIssues(t, ctx, fixture, root, child)
	seedTreeWalkerEdge(t, ctx, fixture, root, child, types.DepBlocks)

	result := walkTree(t, ctx, fixture, publicops.WalkTreeRequest{RootID: root, MaxDepth: 10})
	assertTreeWalkerIDs(t, result, root, child)

	for _, test := range []struct{ what, id string }{
		{"a case variant", strings.ToUpper(root)},
		{"a trailing space", root + " "},
		{"a leading space", " " + root},
		{"a prefix of the id", root[:len(root)-2]},
		{"the id with a suffix", root + "x"},
	} {
		_, err := fixture.TreeWalker.WalkTree(ctx, publicops.WalkTreeRequest{RootID: test.id, MaxDepth: 10})
		if !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("WalkTree with %s (%q) error = %v, want ErrNotFound: this role resolves the stored spelling only",
				test.what, test.id, err)
		}
	}
}

// RunTreeWalkerCrossesPlanesFromAWispRootAndUpward covers the two cross-plane
// quadrants RunTreeWalkerMergesTheDurableAndEphemeralPlanes leaves open: a WISP
// as the root, and an UP walk crossing a plane.
//
// A root probe reading only the issues table fails the first. An up-adjacency
// reading only `dependencies` rather than the union fails the second — and
// passes every other case here, because the up case is durable-only. All three
// legs share one walk body, so nothing else will catch a regression in it.
func RunTreeWalkerCrossesPlanesFromAWispRootAndUpward(t *testing.T, ctx context.Context, fixture TreeWalkerFixture) {
	t.Helper()
	head := fixture.IssuePrefix + "-xplane-head"
	wisp := fixture.IssuePrefix + "-xplane-wisp"
	tail := fixture.IssuePrefix + "-xplane-tail"
	seedTreeWalkerIssues(t, ctx, fixture, head, tail)
	seedTreeWalkerWisp(t, ctx, fixture, wisp)
	seedTreeWalkerEdge(t, ctx, fixture, head, wisp, types.DepBlocks)
	seedTreeWalkerEdge(t, ctx, fixture, wisp, tail, types.DepBlocks)

	// The wisp resolves as a root and its DOWN walk crosses back into the
	// durable plane.
	down := walkTree(t, ctx, fixture, publicops.WalkTreeRequest{
		RootID: wisp, MaxDepth: 10, Direction: publicops.TreeDown,
	})
	assertTreeWalkerIDs(t, down, wisp, tail)

	// And its UP walk crosses the other way, over an edge stored in the wisp
	// plane, to the durable row that depends on it.
	up := walkTree(t, ctx, fixture, publicops.WalkTreeRequest{
		RootID: wisp, MaxDepth: 10, Direction: publicops.TreeUp,
	})
	assertTreeWalkerIDs(t, up, wisp, head)

	// The durable tail walked UP reaches the wisp and then the durable head, so
	// the up-adjacency crosses a plane in the MIDDLE of a chain and not only at
	// the root.
	upFromTail := walkTree(t, ctx, fixture, publicops.WalkTreeRequest{
		RootID: tail, MaxDepth: 10, Direction: publicops.TreeUp,
	})
	assertTreeWalkerIDs(t, upFromTail, tail, wisp, head)
}

// RunTreeWalkerRefusesAnInvalidRequest pins the request vocabulary at the
// backends: treewalker.go:63 (empty root), :68-72 (a direction outside the
// closed set) and :79-85 (a zero or negative depth).
//
// The pure test in internal/storage/issueops pins the same rules against the
// validator directly. This case exists because a wrapper is what would drop the
// request on the floor — the unit-of-work leg passes it through a repository
// method, and a body that validated and then walked with a default would answer
// instead of refusing.
func RunTreeWalkerRefusesAnInvalidRequest(t *testing.T, ctx context.Context, fixture TreeWalkerFixture) {
	t.Helper()
	root := fixture.IssuePrefix + "-invalid-root"
	seedTreeWalkerIssues(t, ctx, fixture, root)

	for _, test := range []struct {
		name string
		req  publicops.WalkTreeRequest
	}{
		{"an empty root", publicops.WalkTreeRequest{MaxDepth: 1}},
		{"a zero depth", publicops.WalkTreeRequest{RootID: root}},
		{"a negative depth", publicops.WalkTreeRequest{RootID: root, MaxDepth: -3}},
		{"a direction outside the closed set", publicops.WalkTreeRequest{RootID: root, MaxDepth: 1, Direction: "sideways"}},
	} {
		_, err := fixture.TreeWalker.WalkTree(ctx, test.req)
		if err == nil {
			t.Errorf("WalkTree with %s succeeded, want ErrValidation", test.name)
			continue
		}
		if !errors.Is(err, publicops.ErrValidation) {
			t.Errorf("WalkTree with %s: error = %v, want ErrValidation", test.name, err)
		}
	}
}

// RunTreeWalkerRefusesAWalkOverTheRowCap pins treewalker.go:116-135: a walk
// whose node count exceeds MaxRows comes back as *ErrTooManyRows carrying the
// count, the cap and the attribution — and NO tree.
//
// EVERY LEG HONORS IT, which is the difference from ListRequest.MaxRows, whose
// unit-of-work arm refuses the field with ErrUnsupported. The cap lives in the
// one shared body here, so the unit-of-work leg is where a wrapper that dropped
// the field would show up — and that leg is exactly the one `bd dep tree
// --max-rows` used to refuse outright.
func RunTreeWalkerRefusesAWalkOverTheRowCap(t *testing.T, ctx context.Context, fixture TreeWalkerFixture) {
	t.Helper()
	root := fixture.IssuePrefix + "-cap-root"
	first := fixture.IssuePrefix + "-cap-first"
	second := fixture.IssuePrefix + "-cap-second"
	seedTreeWalkerIssues(t, ctx, fixture, root, first, second)
	seedTreeWalkerEdge(t, ctx, fixture, root, first, types.DepBlocks)
	seedTreeWalkerEdge(t, ctx, fixture, root, second, types.DepBlocks)

	// Three nodes, cap of two.
	_, err := fixture.TreeWalker.WalkTree(ctx, publicops.WalkTreeRequest{
		RootID: root, MaxDepth: 10, MaxRows: 2, MaxRowsSource: "--max-rows",
	})
	if err == nil {
		t.Fatal("WalkTree over the cap succeeded, want *ErrTooManyRows")
	}
	var capErr *storeops.ErrTooManyRows
	if !errors.As(err, &capErr) {
		t.Fatalf("WalkTree over the cap: error = %T %v, want *issueops.ErrTooManyRows", err, err)
	}
	if capErr.Found != 3 || capErr.Cap != 2 || capErr.Source != "--max-rows" {
		t.Errorf("cap error = {Found:%d Cap:%d Source:%q}, want {3 2 \"--max-rows\"}: the typed fields are what the CLI's exit-2 message reads back",
			capErr.Found, capErr.Cap, capErr.Source)
	}

	// The same walk under a cap it fits inside answers normally, so the case is
	// about the cap rather than about the graph.
	under := walkTree(t, ctx, fixture, publicops.WalkTreeRequest{
		RootID: root, MaxDepth: 10, MaxRows: 3, MaxRowsSource: "--max-rows",
	})
	assertTreeWalkerIDSet(t, under, root, first, second)
}

// RunTreeWalkerWritesNothing pins treewalker.go:256-259: walking is a read, so
// no history entry lands — not for a successful walk and not for a refusal.
func RunTreeWalkerWritesNothing(t *testing.T, ctx context.Context, fixture TreeWalkerFixture) {
	t.Helper()
	if fixture.CountHistory == nil {
		t.Skip("fixture cannot observe history: CountHistory is nil")
	}
	root := fixture.IssuePrefix + "-write-root"
	child := fixture.IssuePrefix + "-write-child"
	seedTreeWalkerIssues(t, ctx, fixture, root, child)
	seedTreeWalkerEdge(t, ctx, fixture, root, child, types.DepBlocks)

	before, err := fixture.CountHistory(ctx)
	if err != nil {
		t.Fatalf("CountHistory before: %v", err)
	}
	walkTree(t, ctx, fixture, publicops.WalkTreeRequest{RootID: root, MaxDepth: 10})
	if _, err := fixture.TreeWalker.WalkTree(ctx, publicops.WalkTreeRequest{RootID: root}); err == nil {
		t.Error("a zero depth succeeded; this half of the case needs a refusal to observe")
	}
	after, err := fixture.CountHistory(ctx)
	if err != nil {
		t.Fatalf("CountHistory after: %v", err)
	}
	if after != before {
		t.Errorf("history grew by %d across a walk and a refusal, want 0", after-before)
	}
}

// --- helpers -------------------------------------------------------------

// treeWalkerEdge is one raw edge of a seeding script; see seedTreeWalkerCycleEdges.
type treeWalkerEdge struct {
	Source string
	Target string
	Type   types.DependencyType
}

func walkTree(t *testing.T, ctx context.Context, fixture TreeWalkerFixture, req publicops.WalkTreeRequest) publicops.TreeResult {
	t.Helper()
	result, err := fixture.TreeWalker.WalkTree(ctx, req)
	if err != nil {
		t.Fatalf("WalkTree(%+v): %v", req, err)
	}
	if result.Nodes == nil {
		t.Fatalf("WalkTree(%+v) returned nil Nodes; treewalker.go:145-146 says never nil for a successful call", req)
	}
	return result
}

func seedTreeWalkerIssues(t *testing.T, ctx context.Context, fixture TreeWalkerFixture, ids ...string) {
	t.Helper()
	for _, id := range ids {
		seedTreeWalkerIssueWithStatus(t, ctx, fixture, id, types.StatusOpen)
	}
}

func seedTreeWalkerIssueWithStatus(t *testing.T, ctx context.Context, fixture TreeWalkerFixture, id string, status types.Status) {
	t.Helper()
	issue := treeWalkerSeed(id, false)
	issue.Status = status
	if err := fixture.CreateIssue(ctx, issue, "tree-walker-seed"); err != nil {
		t.Fatalf("seed issue %s: %v", id, err)
	}
}

func seedTreeWalkerWisp(t *testing.T, ctx context.Context, fixture TreeWalkerFixture, id string) {
	t.Helper()
	if err := fixture.CreateWisp(ctx, treeWalkerSeed(id, true), "tree-walker-seed"); err != nil {
		t.Fatalf("seed wisp %s: %v", id, err)
	}
}

func seedTreeWalkerEdge(t *testing.T, ctx context.Context, fixture TreeWalkerFixture, from, to string, depType types.DependencyType) {
	t.Helper()
	if err := fixture.AddDependency(ctx, &types.Dependency{
		IssueID: from, DependsOnID: to, Type: depType,
	}, "tree-walker-seed"); err != nil {
		t.Fatalf("seed edge %s -> %s (%s): %v", from, to, depType, err)
	}
}

// seedTreeWalkerCycleEdges writes edges with RAW SQL, which is the only way to
// produce a cycle: the storage seam behind AddDependency refuses one.
func seedTreeWalkerCycleEdges(t *testing.T, ctx context.Context, fixture TreeWalkerFixture, edges ...treeWalkerEdge) {
	t.Helper()
	if fixture.Exec == nil {
		t.Skip("fixture cannot write raw SQL: Exec is nil, and no supported verb creates a cycle, so this backend cannot be given one to walk")
	}
	script := make([]SQLStatement, 0, len(edges))
	for _, edge := range edges {
		edgeType := edge.Type
		if edgeType == "" {
			edgeType = types.DepBlocks
		}
		script = append(script, SQLStatement{
			Query: "INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by, metadata) VALUES (?, ?, ?, ?, NOW(), 'seed', '{}')",
			Args:  []any{treeWalkerEdgeID(edge.Source, edge.Target, edgeType), edge.Source, edge.Target, string(edgeType)},
		})
	}
	if err := fixture.Exec(ctx, script); err != nil {
		t.Fatalf("seed %d cycle edge(s): %v", len(edges), err)
	}
}

// treeWalkerEdgeID is a stable 36-character key for one edge.
func treeWalkerEdgeID(source, target string, edgeType types.DependencyType) string {
	sum := sha256.Sum256([]byte(source + "\x00" + target + "\x00" + string(edgeType)))
	return "twk" + hex.EncodeToString(sum[:])[:33]
}

func treeWalkerSeed(id string, ephemeral bool) *types.Issue {
	return &types.Issue{
		ID:        id,
		Title:     id,
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: ephemeral,
	}
}

func treeWalkerIDs(result publicops.TreeResult) []string {
	ids := make([]string, 0, len(result.Nodes))
	for _, node := range result.Nodes {
		ids = append(ids, node.ID)
	}
	return ids
}

func treeWalkerCount(result publicops.TreeResult, id string) int {
	n := 0
	for _, node := range result.Nodes {
		if node.ID == id {
			n++
		}
	}
	return n
}

func treeWalkerByID(t *testing.T, result publicops.TreeResult) map[string]*types.TreeNode {
	t.Helper()
	out := make(map[string]*types.TreeNode, len(result.Nodes))
	for _, node := range result.Nodes {
		out[node.ID] = node
	}
	return out
}

// assertTreeWalkerIDs pins the answer's ids AND their ORDER.
func assertTreeWalkerIDs(t *testing.T, result publicops.TreeResult, want ...string) {
	t.Helper()
	got := treeWalkerIDs(result)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("walk = [%s], want [%s]", strings.Join(got, ", "), strings.Join(want, ", "))
	}
}

// assertTreeWalkerIDSet pins the answer's ids WITHOUT their order, for the cases
// whose graph gives the walk a genuine choice of which neighbor to take first.
// The adjacency comes back in the storage layer's order and treewalker.go:151-159
// deliberately does not promise a sort, so pinning an order here would be
// pinning an artifact.
func assertTreeWalkerIDSet(t *testing.T, result publicops.TreeResult, want ...string) {
	t.Helper()
	got := map[string]int{}
	for _, id := range treeWalkerIDs(result) {
		got[id]++
	}
	if len(got) != len(want) {
		t.Fatalf("walk = %v, want the set %v", treeWalkerIDs(result), want)
	}
	for _, id := range want {
		if got[id] != 1 {
			t.Fatalf("walk = %v, want %s exactly once", treeWalkerIDs(result), id)
		}
	}
}

func assertTreeWalkerNode(t *testing.T, byID map[string]*types.TreeNode, id string, depth int, parentID string, edge types.DependencyType) {
	t.Helper()
	node, ok := byID[id]
	if !ok {
		t.Fatalf("no node %s in the answer", id)
	}
	if node.Depth != depth {
		t.Errorf("node %s Depth = %d, want %d", id, node.Depth, depth)
	}
	if node.ParentID != parentID {
		t.Errorf("node %s ParentID = %q, want %q", id, node.ParentID, parentID)
	}
	if node.EdgeFromParent != edge {
		t.Errorf("node %s EdgeFromParent = %q, want %q", id, node.EdgeFromParent, edge)
	}
	if node.Title != id {
		t.Errorf("node %s hydrated to the row titled %q, want %q: the hydration read a different row than the walk named",
			id, node.Title, id)
	}
}
