package issueops

import (
	"context"

	"github.com/steveyegge/beads/internal/types"
)

// TreeNode is one node of a walked dependency tree: the issue, the depth it was
// first reached at, the node it was reached FROM, and the type of the edge that
// led there.
//
// It is the canonical struct rather than a view of one — the same value
// `bd dep tree --json` marshals — so the CLI's output and the HTTP body are one
// compatibility domain and a field added to an issue appears on both at once.
//
// TRUNCATED IS DEAD AND THIS ROLE NEVER SETS IT. No writer in the tree has ever
// set it: the walk stops at MaxDepth by not descending, and a node that was not
// reached is absent rather than present-and-flagged. It stays on the struct
// because it is on the wire — `bd dep tree --json` has always emitted
// `"truncated": false` — and removing it is a shape change nobody asked this
// commit for. Read it as always false.
type TreeNode = types.TreeNode

// TreeDirection selects which way a walk follows the dependency graph.
//
// It is a STRING rather than a bool because there are three answers and the two
// front doors already spell them these three ways. `bd dep tree --reverse` is
// the deprecated spelling of TreeUp and the CLI translates it before it gets
// here; nothing below this contract knows about it.
type TreeDirection string

const (
	// TreeDown follows what the root DEPENDS ON — the issues that block it.
	TreeDown TreeDirection = "down"
	// TreeUp follows what DEPENDS ON the root — the issues it blocks.
	TreeUp TreeDirection = "up"
	// TreeBoth walks each way and returns one list; see TreeResult.Nodes for
	// the order and for what the two walks do and do not share.
	TreeBoth TreeDirection = "both"
)

// WalkTreeRequest describes one recursive walk of the dependency graph from a
// single root.
//
// IT IS ANCHORED, WHICH IS WHY IT IS NOT A CycleDetector REQUEST, and it is
// RECURSIVE, which is why it is not a Relations or an EdgeReader request. Both
// of those roles say so in their own docs: "a recursive walk has a depth, a
// cycle policy and a node shape of its own". All three are fields or promises
// below.
type WalkTreeRequest struct {
	// RootID is the EXACT canonical id the walk starts from.
	//
	// EXACT is a promise this role keeps rather than an obligation it puts on
	// the caller, for the reason RelatedRequest.ID and EdgeReadRequest.IDs both
	// give: a case variant, an id carrying surrounding whitespace, a prefix of a
	// real id and a real id with a suffix are all misses, because an affordance
	// that can answer ABOUT A DIFFERENT ISSUE than the caller named has no place
	// on a contract an unattended client calls. A front door that wants
	// partial-id resolution resolves BEFORE it calls, which is also what lets a
	// routed root be resolved against the store that actually holds it.
	//
	// An empty RootID is ErrValidation. A RootID that names nothing in either
	// plane is ErrNotFound — unlike EdgeReader, which reports a miss per anchor,
	// because there is exactly one anchor here and no other answer to preserve.
	RootID string

	// Direction selects the edge direction. The empty string means TreeDown, so
	// a zero request walks dependencies — the same default both front doors
	// apply. Any other value is ErrValidation: the vocabulary is CLOSED, which
	// is the difference from a dependency type, and a fourth spelling silently
	// read as "down" would answer a question nobody asked.
	Direction TreeDirection

	// MaxDepth bounds how many LEVELS the walk descends, counting the root as
	// level one. MaxDepth 1 is the root alone; MaxDepth 3 is the root, its
	// neighbors and their neighbors.
	//
	// IT IS REQUIRED, and 0 is ErrValidation rather than "unbounded". A zero
	// int is what an uninitialized request carries, and the answer to an
	// uninitialized recursive walk must not be "walk the whole graph": on a
	// large workspace that is the request that takes the database down. The CLI
	// supplies its own default (50) at the front door, where a default belongs,
	// and states it in --help. A negative value is ErrValidation for the same
	// reason.
	//
	// THE BOUND IS ON THE WALK, NOT ON THE ANSWER. A node beyond MaxDepth is
	// ABSENT, not present and marked: nothing sets TreeNode.Truncated, and a
	// caller cannot tell a tree that ended from a tree that was cut. That is
	// today's behavior stated rather than changed; giving the answer a
	// truncation marker is a new field and a new promise, not a refactor.
	MaxDepth int

	// Status, when set, prunes the walked tree to the nodes carrying that status
	// AND THE ANCESTOR CHAIN OF EACH SURVIVOR, so what comes back is still a
	// tree rather than a scatter of orphans. The empty string prunes nothing.
	//
	// IT IS A POST-WALK PRUNE AND NOT A FILTER ON THE WALK, and the difference
	// is observable: a matching node BEHIND a non-matching one is still reached,
	// because the walk descends through the non-matcher and the prune then keeps
	// it as an ancestor. Filtering during the walk would have cut the subtree
	// off entirely. Both front doors have always done it this way.
	//
	// A PRUNE THAT MATCHES NOTHING RETURNS NO NODES AT ALL, including the root —
	// the root is kept only as somebody's ancestor, never for its own sake. That
	// is a sharp edge and it is stated because it is the behavior both routes
	// ship: `bd dep tree X --status=closed` on a tree with no closed member
	// prints nothing rather than printing X.
	//
	// The value is NOT checked against the workspace's status vocabulary. An
	// unrecognized status matches nothing and prunes everything, which is the
	// same loose reading Counter states for CountRequest, and it is stated here
	// rather than tightened inside a refactor.
	Status Status

	// MaxRows is a DEFENSIVE CAP rather than a page, exactly as ListRequest's
	// is. It bounds how many NODES the answer may carry before the whole answer
	// is refused; 0 disables it. A walk whose result exceeds it comes back as
	// *internal/storage/issueops.ErrTooManyRows — carrying the count observed,
	// the cap, and MaxRowsSource's attribution — and NO TREE. The type is named
	// here rather than left as "an error" so a caller can tell the cap firing
	// from any other failure with errors.As; this leaf does not import it, and
	// no answer depends on that.
	//
	// IT IS CHECKED AFTER THE WALK AND AFTER THE PRUNE, which is post-hoc and is
	// not what a circuit breaker would ideally be. A tree walk has no query
	// filter to thread a cap through — it is a recursion of single-row reads —
	// so the whole tree is built and then counted. The cap therefore bounds what
	// a caller is HANDED, not what the database was asked to do. That is what
	// `bd dep tree`'s own --help has always said, and stating it here keeps the
	// promise honest rather than implied.
	//
	// EVERY IMPLEMENTATION HONORS IT, as ListRequest.MaxRows is honored
	// everywhere too — here because the cap lives in the one shared walk body
	// all three backends run, rather than in two query paths that have to size
	// the same window.
	MaxRows int
	// MaxRowsSource attributes the cap to whatever knob set it — "--max-rows",
	// "BEADS_MAX_ROWS", or empty for a library caller — and that attribution is
	// what the refusal text reads back. It decides no answer: a request is
	// refused on MaxRows alone, and this only decides how the refusal reads.
	MaxRowsSource string
}

// TreeResult is one walked tree, flattened.
type TreeResult struct {
	// Nodes is the tree as a FLAT LIST in walk order, never nil for a
	// successful call. A node's place in the tree is read from its Depth and its
	// ParentID rather than from nesting: TreeNode carries no children, so a
	// renderer rebuilds the shape from the two fields.
	//
	// THE ORDER IS DEPTH-FIRST PRE-ORDER: a node appears before every node it
	// led to, and a subtree is contiguous. It is not sorted, and it is not
	// promised to be stable against a change in the underlying edge rows, for
	// the honest reason that the adjacency comes back in the storage layer's
	// order and this role does not re-sort it. Two calls against an unchanged
	// database do agree; that is the property `bd dep tree` has always had and
	// the one a caller may rely on. It is deliberately WEAKER than
	// CycleReport's canonicalization, which had to be strong because the walk
	// there chose which cycles exist.
	//
	// EVERY NODE APPEARS AT MOST ONCE PER WALK, at the depth and parent of the
	// FIRST path that reached it. That single rule is both the cycle policy and
	// the diamond policy:
	//
	//   - A CYCLE TERMINATES. Revisiting a node on the current path stops the
	//     descent, so the walk finishes on a cyclic graph instead of recursing
	//     forever. This role therefore never fails on a cycle and never reports
	//     one; `bd dep cycles` and issueops.CycleDetector are where a cycle is
	//     an answer.
	//   - A SHARED SUBTREE IS RENDERED ONCE. A diamond — two parents reaching
	//     one child — shows the child under whichever parent the walk reached
	//     first, and the second parent has no visible edge to it. There is no
	//     option to show it twice. `bd dep tree --show-all-paths` is a
	//     DOCUMENTED NO-OP that predates this role: it was accepted and threaded
	//     and never read by any walk, and nobody has specified what "all paths"
	//     means for a DAG with shared subtrees. This contract deliberately does
	//     not invent one.
	//
	// FOR TreeBoth the two walks are INDEPENDENT and the answer is their
	// concatenation: every up-tree node except the root, in the up walk's order,
	// followed by the whole down tree beginning with the root. The root appears
	// ONCE, from the down walk. The two halves may therefore repeat a node
	// between them — an issue that both blocks and is blocked by something in
	// the other half — and each half's Depth is measured from the root along its
	// own direction, so a node's Depth does not say which half it came from and
	// neither does anything else on it. That is the shape both front doors have
	// always rendered; it is stated because a caller aggregating Nodes must not
	// assume the ids are distinct.
	//
	// The two walks DO see one database state. On every implementation they run
	// inside one transaction, so an edge added between them cannot make the up
	// half and the down half describe different graphs. That was not true
	// before this role: both front doors made two independent calls.
	Nodes []*TreeNode
}

// TreeWalker describes walking the dependency graph from ONE ROOT and answering
// with the nodes it reached: the operation `bd dep tree` performs. Like
// Lifecycle, Reader, ReadyClaimer, DependencyEditor, Relations, EdgeReader and
// CycleDetector it is a role with its own accessor. A new capability gets a new
// role interface and its own accessor; never append a method here.
//
// IT IS ITS OWN ROLE, AND THE TWO ROLES NEXT DOOR BOTH SAY WHY IN ADVANCE.
// Relations answers about the edges around ONE issue and stops there;
// EdgeReader answers raw stored rows for MANY anchors and stops there. Both
// exclude the tree in the same words — "a recursive walk has a depth, a cycle
// policy and a node shape of its own" — and all three of those are on this
// contract: MaxDepth, the first-visit rule on TreeResult.Nodes, and TreeNode.
// Folding the walk into either of them would have put a depth and a cycle
// policy on a request that has no recursion, which is the accretion the
// governing rule exists to stop.
//
// IT IS NOT CycleDetector EITHER, and the pairing is closer than it looks. Both
// walk edges; the difference is that a cycle has NO ANCHOR and a tree is
// nothing BUT its anchor. A cycle report is a property of the whole graph, so
// its request is empty; this request cannot be answered at all without a root.
// They also disagree about what an edge IS — see the next paragraph — so one
// workspace can honestly have no cycles and a tree that revisits a node.
//
// WHAT COUNTS AS AN EDGE HERE is every dependency type EXCEPT `relates-to`.
// That is WIDER than CycleDetector's walk, which follows `blocks` and
// `conditional-blocks` only, and it is deliberate rather than an oversight in
// either place: `bd dep tree` is the picture of how work hangs together, so
// `parent-child`, `discovered-from` and `tracks` belong in it, while a cycle
// report is about SCHEDULING DEADLOCK and a mutual `parent-child` is not one.
// `relates-to` is excluded here because it is symmetric annotation — following
// it would make the "tree" the connected component.
//
// `related` AND `relates-to` ARE TWO DIFFERENT TYPES (types.DepRelated,
// types.DepRelatesTo) and only the second is excluded. That is worth naming
// because the two read as synonyms and the exclusion looks like it should cover
// both; it does not, and no test would have caught the confusion.
//
// BOTH PLANES ARE ONE GRAPH, as they are for CycleDetector. The walk follows an
// edge from an issue into a wisp and back, and hydrates each node from whichever
// plane holds it, so an ephemeral step in the middle of a chain does not end the
// picture.
//
// A NODE THE DATABASE CANNOT DESCRIBE ENDS THAT BRANCH, and this is the one
// place this role is deliberately LESS honest than CycleDetector, which carries
// an unhydratable member with a nil issue and marks the cycle Partial. It
// cannot do the same: TreeNode IS an issue — the id and the description are one
// struct, not two fields — so there is no shape for "this node is on the tree
// and cannot be described". An edge whose target is an `external:` reference or
// an id in another repository's namespace therefore contributes no node, and
// nothing in the answer says a branch stopped for that reason rather than
// because it ended. That is today's behavior on both routes, stated rather
// than changed; fixing it is a node-shape change and wants its own bead.
//
// Implementations never mutate caller-owned request values, snapshot the
// request at method entry, and apply validation and normalization only to
// attempt-local clones. WalkTreeRequest travels by value and carries no
// reference members, so there is nothing here a body could write through to the
// caller.
//
// Walking is a READ. Nothing here records a history entry, fires a completion
// hook or changes a row, and a refusal changes nothing either. Deterministic
// request-validation failures match ErrValidation; a root that is not there
// matches ErrNotFound; result values are unspecified when error is non-nil.
type TreeWalker interface {
	// WalkTree returns the nodes reachable from the request's root.
	//
	// THE WHOLE WALK IS ONE CONSISTENT VIEW. The root's existence probe, every
	// adjacency read and every hydration see one database state, and for
	// TreeBoth that covers both directions. A caller can therefore rely on the
	// answer describing a graph that existed, rather than a stitching of several
	// that did.
	//
	// A ROOT WITH NO EDGES IS A ONE-NODE TREE AND A NIL ERROR, not an empty
	// answer: the root itself is always the first node of a down or a single
	// direction walk, so a caller can tell "this issue depends on nothing" from
	// "this issue is not there", which is ErrNotFound. The one way a successful
	// call comes back with no nodes at all is a Status prune that matched
	// nothing; see WalkTreeRequest.Status.
	WalkTree(ctx context.Context, req WalkTreeRequest) (TreeResult, error)
}
