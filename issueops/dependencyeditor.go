package issueops

import (
	"context"

	"github.com/steveyegge/beads/internal/types"
)

// DependencyType names one kind of edge between two issues.
type DependencyType = types.DependencyType

// Dependency types in COMMON USE.
//
// This is an OPEN SET, and these constants are a documented sample of it — not
// a closed enum, not exhaustive, and not a whitelist. A workspace configures
// its own vocabulary, so nothing in this package validates a DependencyType
// against this list, and an implementation that refused a type absent from it
// would break every workspace that spelled one of its own. Adding a value here
// documents a value; it never authorizes one.
//
// They exist at all because internal/types is unimportable outside this
// module. Every request and result below CARRIES a DependencyType, so without
// these an external embedder can hold one but has no spell-checked way to name
// one — the same reason the error sentinels are re-exported in errors.go.
const (
	DepBlocks         = types.DepBlocks
	DepParentChild    = types.DepParentChild
	DepRelated        = types.DepRelated
	DepDiscoveredFrom = types.DepDiscoveredFrom
	DepRepliesTo      = types.DepRepliesTo
	DepWaitsFor       = types.DepWaitsFor
)

// DependencyEdge is one directed edge: IssueID depends on DependsOnID, with
// the given Type. Both endpoints are exact canonical ids — there is no fuzzy,
// prefix or cross-repo resolution on this contract, for the reason
// GetRequest.ID gives.
type DependencyEdge struct {
	IssueID     string
	DependsOnID string
	// Type is required and must be a usable value. It is checked for being a
	// value at all — non-empty, within the column's length — and NEVER for
	// membership of the constants above, which name an open set.
	Type DependencyType
}

// AddDependenciesRequest describes one set of edges asserted together.
//
// IT IS ALL-OR-NOTHING, and unlike the batch close that is not a policy choice
// but the shape of the question. A close acts on issues that are independent
// of one another, so skipping one and committing the rest loses nothing; edges
// asserted in one request describe a GRAPH, and half a graph is a graph nobody
// asked for — the cycle a caller was refused for is exactly the state a
// partial commit would leave behind.
type AddDependenciesRequest struct {
	// Actor is the author of the edges and must not be empty. It is what the
	// dependency_added entry each genuinely new edge records is attributed to,
	// which is the only place a reader finds who wired an edge: the result
	// does not carry it.
	Actor string
	// Edges are the edges to assert, in the caller's order. It must not be
	// empty. Edges are applied parent-child first regardless of this order, so
	// the complete planned hierarchy is visible before any blocking edge is
	// validated against it.
	Edges []DependencyEdge
	// SkipPerEdgeCycleCheck drops the per-edge cycle probe for a caller wiring
	// a large graph, trading validation cost for speed. It NEVER drops the
	// whole-graph gate that runs once at the end, and it never drops the
	// self-dependency refusal: an edge that points an issue at itself is
	// refused with or without it.
	SkipPerEdgeCycleCheck bool
}

// AddDependenciesResult reports the edges that landed.
type AddDependenciesResult struct {
	// Added is the edges this request wrote, in request order. All-or-nothing
	// means it is either every requested edge or the call failed, so it echoes
	// the request — deliberately, the way CloseOutcome echoes its IssueID: a
	// caller reporting what landed reads the RESULT, and never has to know
	// which of the two it is safe to read.
	Added []DependencyEdge
}

// RemoveDependencyRequest describes the removal of one edge.
type RemoveDependencyRequest struct {
	// Actor is the remover and must not be empty. It is what the
	// dependency_removed entry a successful removal records is attributed to,
	// the mirror of AddDependenciesRequest.Actor.
	Actor string
	// IssueID and DependsOnID name the edge exactly. Neither may be empty.
	IssueID     string
	DependsOnID string
}

// RemoveDependencyResult reports what the removal found.
type RemoveDependencyResult struct {
	// Removed is false when no such edge existed. That is a SUCCESS, not a
	// refusal: removing an edge twice leaves the same graph as removing it
	// once, and an agent replaying its own teardown should not have to
	// classify an error to discover it already ran. Nothing is written for it
	// and no history entry is recorded.
	Removed bool
}

// DependencyEditor describes guarded edits to the dependency graph: the write
// side of `bd dep add` and `bd dep remove`, and — like Lifecycle, Reader,
// ReadyClaimer and BatchCloser — a role with its own accessor. A new
// capability gets a new role interface and its own accessor; never append a
// method here.
//
// It is its own role rather than two more Lifecycle verbs because an edge is
// not a field of an issue. It has two endpoints, and every refusal it can
// raise — a cycle, a hierarchy deadlock, a type conflict — is a statement
// about the GRAPH the two endpoints sit in rather than about either row. A
// patch has nowhere to put that.
//
// An edge FOLLOWS ITS SOURCE. Both methods edit the durable graph `bd dep`
// shows and `bd dolt log` versions when the source is a durable issue, and the
// ephemeral graph when it is an ephemeral one — a wisp has no durable row for
// an edge to hang off, so there is no third answer. One request may mix the
// two; it is still one transaction, and still all-or-nothing across both.
// Only the durable half is versioned, so a request made entirely of ephemeral
// edges records no history entry.
//
// A TARGET's plane is independent of its source's: either class may depend on
// the other.
//
// EACH METHOD LEAVES A TRAIL IN THE SOURCE'S EVENT STREAM, attributed to the
// request's Actor: a genuinely new edge records a dependency_added entry, and
// a removal that found its edge a dependency_removed one. Work that wrote
// nothing records nothing — an idempotent same-type re-add and a removal that
// found no edge both leave the stream as they found it. The trail follows the
// source's plane exactly as the edge does, which is what makes it readable on
// an ephemeral edge at all: the wisp plane is not versioned, so for a
// wisp-sourced edge this is the only record the operation leaves.
//
// Implementations never mutate caller-owned request values, snapshot the
// request at method entry, and apply validation and normalization only to
// attempt-local clones. Deterministic request-validation failures match
// ErrValidation and leave persistent state unchanged.
type DependencyEditor interface {
	// AddDependencies asserts every edge in the request as ONE durable act:
	// one transaction, one history entry. Any refused edge refuses the whole
	// request and writes nothing — a self-dependency (ErrSelfDependency), a
	// scheduling cycle (ErrDependencyCycle), a conflicting type on a pair that
	// already has an edge (*DependencyTypeConflictError), a blocking edge that
	// would gate an issue on its own ancestor or descendant
	// (*DependencyHierarchyConflictError), or a SOURCE that does not exist.
	//
	// A target's existence is checked only where the backend can see it. A
	// target may legitimately be an "external:" reference or an issue in
	// another repository, neither of which this database holds, so there is no
	// blanket unknown-target refusal to promise. Where the backend CAN see the
	// absence — an id in this database's own namespace, carrying no
	// "external:" marker and matching no row — the edge is refused, and the
	// request writes nothing like any other refusal.
	//
	// NEITHER EXISTENCE REFUSAL NAMES AN IDENTITY YET. The four refusals above
	// hand a caller a sentinel or a typed value to branch on; a ghost source
	// and a missing local target are only "an error", and not even the same
	// error text on every implementation. So a caller cannot today tell a
	// ghost endpoint from an infrastructure failure. That is a known gap
	// (bd-yby99.9) rather than a promise the refusal will stay anonymous, and
	// nothing should be written that string-matches it.
	//
	// An edge that already exists with the SAME type is idempotent and refuses
	// nothing, and REPETITION WITHIN ONE REQUEST answers the same way: the
	// second occurrence of a pair finds the first already written, so the pair
	// is applied once, and the result still echoes every edge as requested.
	// Two DIFFERENT types for one pair in one request is the same
	// *DependencyTypeConflictError a pre-existing edge of the other type
	// raises, and writes nothing.
	//
	// A request that wrote no durable edge — because every one of them already
	// existed with the requested type — records NO history entry, the same way
	// RemoveDependencyResult states it for a removal that found nothing. The
	// one entry is recorded when at least one durable edge was genuinely
	// written, and one is all that is recorded however many were.
	AddDependencies(ctx context.Context, req AddDependenciesRequest) (AddDependenciesResult, error)
	// RemoveDependency removes exactly the named edge. It is idempotent: a
	// missing edge is Removed false with a nil error, not ErrNotFound.
	RemoveDependency(ctx context.Context, req RemoveDependencyRequest) (RemoveDependencyResult, error)
}
