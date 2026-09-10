package issueops

import (
	"context"

	"github.com/steveyegge/beads/internal/types"
)

// Dependency is one stored dependency edge, exactly as the database holds it:
// a source id, a target id, a type and the edge's own annotations.
//
// It is the ROW and not a view of it, which is what makes this role different
// from Relations. A target may be an "external:" reference or an id belonging
// to ANOTHER repository — DependencyEditor accepts both — and this database
// holds no issue for either. Relations drops such an edge, because it answers
// with the issue on the far end and there is none. Here the edge IS the answer,
// so it is returned with its target spelled as stored and nothing looked up.
//
// The ID field is EMPTY on everything this role returns. The source-keyed read
// behind it selects the six wire columns and not the row's surrogate key (see
// types.Dependency.ID, which says so), so a caller must not use it to tell two
// edges apart; the (IssueID, DependsOnID, Type) triple is what identifies an
// edge here.
type Dependency = types.Dependency

// EdgeReadRequest describes one raw-edge read over several anchors at once.
//
// It is a BATCH request in the sense that batching is the question rather than
// an optimization of it: `bd dep list a b c` asks what each of three named
// issues depends on, and answers per source. Asking the same question three
// times would give three answers a caller then has to key and merge, and would
// cost three round trips where the storage seam offers one.
type EdgeReadRequest struct {
	// IDs are the anchors, each an EXACT canonical id.
	//
	// EXACT is a promise this role keeps rather than an obligation it puts on
	// the caller, for the reason RelatedRequest.ID gives: a case variant, an id
	// carrying surrounding whitespace, a prefix of a real id and a real id with
	// a suffix are all misses, because an affordance that can answer ABOUT A
	// DIFFERENT ISSUE than the caller named has no place on a contract an
	// unattended client calls. A front door that wants partial-id resolution
	// resolves BEFORE it calls, which is also what lets a routed anchor be
	// resolved against the store that actually holds it.
	//
	// A miss is PER ANCHOR and is reported on that anchor rather than failing
	// the call — see AnchorEdges.Missing. That is the whole difference in miss
	// semantics between this role and Relations, which is anchored on one issue
	// and answers ErrNotFound.
	//
	// AN EMPTY ENTRY IS ErrValidation, not a ghost. The empty string names
	// nothing a caller can have meant, and reporting it as a missing anchor
	// would put a nameless row in an answer keyed by name.
	//
	// AN EMPTY SLICE IS NOT AN ERROR: it asks about no anchors and the answer
	// is no anchors, for the reason Counter answers 0 rather than refusing an
	// empty predicate. A caller looping over a list it filtered to nothing
	// should not have to special-case the empty case to avoid an error.
	//
	// REPEATS COLLAPSE. An id named twice is one anchor, at the position of its
	// first mention: the answer is keyed by anchor, and a second entry for the
	// same key carries no second fact — it would only invite a caller
	// aggregating the result to count the same edges twice.
	IDs []string

	// Types restricts the answer to these edge types. Empty means every type.
	//
	// An entry is checked for being a value at all — non-empty, within the
	// column's length — and NEVER for membership of a known-types list, exactly
	// as RelatedRequest.Types is: the vocabulary is OPEN, so a workspace's own
	// type has to be able to filter. An unusable entry is ErrValidation rather
	// than a filter that quietly matches nothing.
	//
	// The filter narrows EDGES, never anchors. An anchor whose every edge the
	// filter rejects comes back present with no edges, which is a different
	// fact from an anchor that is not there.
	Types []DependencyType
}

// AnchorEdges is one anchor's stored outgoing edges, or the report that the
// anchor is not there.
type AnchorEdges struct {
	// ID is the anchor, spelled exactly as the request spelled it.
	ID string
	// Edges are the anchor's stored outgoing edges — the rows whose source is
	// this anchor — after the request's type filter.
	//
	// THE ORDER IS PINNED: ascending by target id, with the edge type breaking
	// a tie. It is pinned rather than left to the query for the reason
	// Relations pins its own order: the rows come from one of two dependency
	// tables depending on which plane the anchor lives on, so the natural order
	// is an artifact of that placement — stable enough to look deliberate and
	// not stable enough to rely on.
	//
	// The tiebreaker is a TOTALITY guarantee rather than an observable one: the
	// stores hold at most one edge per (source, target) pair — a second one
	// with a different type is refused, not added — so no answer reaching a
	// caller today has two edges to break a tie between. It is stated because
	// the order must be total whatever the schema later permits, and it is
	// stated as unreachable so nobody writes a test that cannot be built.
	//
	// It is never nil for a successful call, so a caller that marshals it emits
	// an empty array rather than null.
	Edges []*Dependency
	// Missing reports that no issue and no wisp carries this id.
	//
	// It exists because an empty edge list is otherwise indistinguishable from
	// a typo, and the empty list is the COMMON case — most issues depend on
	// nothing — so the typo would never surface. That is the same reason
	// Relations probes its anchor's existence; the difference is only that a
	// batch cannot answer ErrNotFound without discarding the anchors it did
	// find.
	//
	// A missing anchor carries no edges. The converse does not hold: an anchor
	// that is present with no edges, and one whose edges the type filter
	// removed, both report false with an empty list.
	//
	// DANGLING EDGES ARE NOT MISSING ANCHORS. This flag is about the SOURCE.
	// An edge whose TARGET names nothing — an external reference, a
	// foreign-repository id, or a row left behind by a deleted issue — is
	// returned as stored, and nothing here reports on it: the target is not an
	// anchor of this request and the role does not probe it.
	Missing bool
}

// EdgeReadResult is the per-anchor answer.
type EdgeReadResult struct {
	// Anchors carries one entry per DISTINCT requested id, in the order the
	// request first named it. It is never nil for a successful call, and it is
	// a slice rather than a map because the request's order is part of the
	// answer: both front doors print the anchors in the order the caller named
	// them, and a map would have made that ordering each surface's own
	// invention.
	Anchors []AnchorEdges
}

// EdgeReader describes reading STORED DEPENDENCY EDGES for several anchors at
// once: the answer `bd dep list a b c` gives, and — like Relations — a role
// with its own accessor. A new capability gets a new role interface and its own
// accessor; never append a method here.
//
// IT IS ITS OWN ROLE RATHER THAN A SECOND Relations METHOD, and the reason is
// worth stating because the two roles look alike from a distance. Relations is
// SINGLE-ANCHOR and answers with HYDRATED ISSUES: its result is the issue on
// the far end of each edge, carrying that issue's title, status and priority,
// and an edge with no far end in this database is silently not a neighbor. It
// answers ErrNotFound for an anchor that is not there, because with one anchor
// there is nothing else to say. This role answers with the EDGE RECORDS
// THEMSELVES, for MANY anchors, keyed by source, and it reports a missing
// anchor per anchor rather than failing the call. Different answer shape,
// different miss policy, different arity: folding them together would give one
// role two of each, which is the accretion the governing rule exists to stop.
//
// IT IS ALSO NOT BlockingAnnotator. That role answers a DERIVED, best-effort
// decoration — the open blockers and the parent of a page of issues — where
// this one answers raw stored rows with no derivation and no policy.
//
// THE DIRECTION IS OUTGOING, and there is no parameter for it. The question
// this role answers is "what does each of these depend on"; the inbound
// direction is a different read against a different key (the batched
// target-keyed mirror), it has its own de-duplication rule across the two
// dependency tables, and no front door asks for it in bulk today. A role born
// with a direction nothing sets would be a promise nothing checks — see
// CountRequest's missing free-text field for the same decision. When something
// needs the inbound bulk read it gets its own role, exactly as this one did.
//
// THE DEPENDENCY TREE IS NOT HERE EITHER, for the reason Relations gives: a
// recursive walk has a depth, a cycle policy and a node shape of its own.
//
// Implementations never mutate caller-owned request values, snapshot the
// request at method entry, and apply validation and normalization only to
// attempt-local clones. EdgeReadRequest travels by value, so IDs and Types are
// the members a body could otherwise write through to the caller: reading them
// into a set is fine, sorting or de-duplicating them in place is not.
//
// Reading edges is a READ. Nothing here records a history entry, fires a
// completion hook or changes a row, and a refusal changes nothing either.
// Deterministic request-validation failures match ErrValidation; result values
// are unspecified when error is non-nil.
type EdgeReader interface {
	// ReadEdges returns each anchor's stored outgoing edges.
	//
	// The anchors' existence and their edges are read from ONE consistent view,
	// so an anchor cannot be reported missing by a probe that raced a create
	// the edge read then saw. That is the same promise Relations makes for its
	// single anchor, and it is the reason this is one method rather than a
	// caller's existence check followed by a caller's edge read.
	//
	// A request naming only anchors that are not there is a successful call
	// whose every entry reports Missing: there is no ErrNotFound on this role,
	// because a batch that failed for one absent id would throw away the
	// answers for the ids that were found.
	ReadEdges(ctx context.Context, req EdgeReadRequest) (EdgeReadResult, error)
}
