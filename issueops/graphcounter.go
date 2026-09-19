package issueops

import (
	"context"
)

// EdgeDirection names which end of a dependency edge an anchor sits on. The set
// is CLOSED and there is no zero value in it: a request that leaves the
// direction unset is ErrValidation rather than a count in some default
// direction.
//
// Requiring it is the one deliberate unfriendliness on this request. The two
// answers are about DIFFERENT EDGE SETS — "what does this depend on" and "what
// depends on this" — and a workspace where most issues have edges in only one
// direction returns the same number for both often enough that a caller who
// meant the other one would not notice for a long time. The raw storage seam
// spells the difference in the METHOD NAME (CountDependents, CountDependencies)
// and cannot make that mistake; a request field can, so it is required instead.
type EdgeDirection string

const (
	// EdgeDirectionOut counts the anchor's DEPENDENCIES: edges whose source is
	// the anchor. This is the direction `bd dep list` reads and the number
	// `bd show` prints as the dependency count.
	EdgeDirectionOut EdgeDirection = "out"
	// EdgeDirectionIn counts the anchor's DEPENDENTS: edges whose target is the
	// anchor.
	EdgeDirectionIn EdgeDirection = "in"
)

// EdgeCountRequest describes one edge-cardinality question asked of several
// anchors at once.
//
// IT IS THE COUNTING SIBLING OF EdgeReadRequest and is spelled to match it
// field for field where the two share a meaning — IDs de-duplicate onto first
// mention, Types narrows edges and never anchors, an empty slice asks about no
// anchors — so a caller holding both cannot believe the two questions are asked
// of different sets. Two things are here that are not there: Direction, which
// EdgeReadRequest deliberately lacks (edgereader.go's EdgeReader doc says the
// inbound bulk read "gets its own role" when something needs it, and counting
// is where something did), and Status.
//
// THERE IS NO PAGE, no limit and no offset, for the reason CountRequest gives:
// a count is a cardinality, and bounding the scan would answer "how many of the
// first N".
//
// THERE IS NO "BOTH" DIRECTION. A caller that wants the pair asks twice, which
// is what every front door in the tree already does — internal/workapi's detail
// assembly calls CountDependents and CountDependencies as two separate reads
// today. Answering both in one call would mean two numbers per anchor, and then
// Status — which narrows by a row only the inbound direction has (see that
// field) — would apply to one of them and silently not to the other. A field
// that quietly governs half an answer is the shape this request is written to
// avoid.
type EdgeCountRequest struct {
	// IDs are the anchors, each an EXACT canonical id, with the same
	// promise-rather-than-obligation EdgeReadRequest.IDs states: a case variant,
	// surrounding whitespace, a prefix of a real id and a real id with a suffix
	// are all MISSES, not resolutions. A front door that wants partial-id
	// resolution resolves before it calls.
	//
	// A miss is PER ANCHOR and reported on that anchor — see
	// AnchorEdgeCount.Missing — rather than failing the call.
	//
	// AN EMPTY ENTRY IS ErrValidation. The empty string names nothing a caller
	// can have meant, and reporting it as a missing anchor would put a nameless
	// row in an answer keyed by name.
	//
	// AN EMPTY SLICE IS NOT AN ERROR: it asks about no anchors and the answer is
	// no anchors. The direction is still validated first, so EdgeCountRequest{}
	// is ErrValidation on Direction and not an empty answer.
	//
	// REPEATS COLLAPSE onto the first mention, for EdgeReadRequest.IDs's reason:
	// a second entry for the same anchor carries no second fact and would only
	// invite a caller summing the result to count the same edges twice.
	//
	// THERE IS NO SIZE CAP HERE, exactly as there is none on EdgeReadRequest.IDs.
	// The reads are batched internally, and an in-process caller that holds the
	// role has already paid for the slice it is passing; a cap on the role would
	// be a limit invented for a caller that is not the one that needs it. The
	// bound belongs to the WIRE operation, where the request arrives from
	// somewhere else and the cost of an unbounded one is a stranger's — the
	// split ApplyBatchRequest.Items makes explicitly by bounding at 100. The
	// graph-counts wire slice set it: GET /v0/beads/dependencies:count bounds
	// `issue_id` at minItems 1, maxItems 100.
	IDs []string

	// Direction is which edges the count is over, and it is REQUIRED. An empty
	// or unrecognized value is ErrValidation. See EdgeDirection.
	Direction EdgeDirection

	// Types restricts the count to these edge types. Empty means every type.
	//
	// An entry is checked for being a value at all — non-empty, within the
	// column's length — and NEVER for membership of a known-types list, exactly
	// as EdgeReadRequest.Types is: the vocabulary is OPEN, so a workspace's own
	// type has to be able to filter. An unusable entry is ErrValidation rather
	// than a filter that quietly matches nothing; an unrecognized but usable one
	// is accepted and matches no edge.
	//
	// The filter narrows EDGES, never anchors. An anchor whose every edge it
	// rejects comes back present with a count of 0, which is a different fact
	// from an anchor that is not there.
	Types []DependencyType

	// Status restricts the count to edges whose DEPENDENT — the issue at the
	// SOURCE end, the one doing the depending — is in this stored status. Empty
	// means every status.
	//
	// IT IS LEGAL ONLY WITH EdgeDirectionIn, and a non-empty Status beside
	// EdgeDirectionOut is ErrValidation rather than a filter that is ignored.
	// The asymmetry is the substrate's, not a preference: narrowing by status
	// means joining the far end of the edge to the row that holds its status,
	// and an OUTBOUND edge's far end may be a row this database does not hold at
	// all — DependencyEditor accepts an "external:" reference and an id
	// belonging to another repository as targets. A status filter on that
	// direction would silently drop every dangling edge, which is precisely the
	// class of edge EdgeReader exists to return verbatim. The raw storage seam
	// has the same asymmetry and states it the same way: it publishes
	// CountDependentsByStatus and no CountDependenciesByStatus.
	//
	// It is NOT validated against the workspace vocabulary and is NOT a
	// comma-separated OR set: an unrecognized name matches nothing and counts 0
	// rather than failing, exactly as CountRequest.Status does. A scripted
	// caller counting a status its workspace has since dropped currently reads 0
	// and should keep reading 0.
	//
	// The status is read from the dependent's OWN PLANE: a durable dependent's
	// from the issues table, an ephemeral one's from the wisps table. An edge
	// whose source row has been deleted out from under it joins to nothing and
	// is not counted, which is the one way a status-narrowed count can be
	// smaller than the unnarrowed count by more than the statuses explain.
	Status string
}

// AnchorEdgeCount is one anchor's edge cardinality, or the report that the
// anchor is not there.
type AnchorEdgeCount struct {
	// ID is the anchor, spelled exactly as the request spelled it.
	ID string
	// Count is how many stored edges match the request in the requested
	// direction, after the type and status filters.
	//
	// IT SPANS BOTH DEPENDENCY PLANES. An edge lives in `dependencies` or in
	// `wisp_dependencies` according to which plane its SOURCE sits on, and this
	// count is the total across the two — so a durable issue's dependent count
	// includes the wisps that depend on it, and a wisp's dependency count
	// includes the durable issues it depends on. That is the shipped answer of
	// every raw count this role covers and of the domain body behind the
	// unit-of-work leg (CountDependencyEdgesInTx), and it is what makes the
	// number agree with the row list a caller gets from the matching read.
	//
	// It is a SUM over the two planes and NOT a distinct count of edge rows. The
	// two are the same number unless one row id is present in both tables, which
	// is a durable/ephemeral mirror the paging read de-duplicates for its keyset
	// contract (CountDependentRecords) and this count never has. Nothing in this
	// contract's reach can produce one — a seeded edge is routed to exactly one
	// plane by its source — so the distinction is stated rather than pinned.
	//
	// UNREACHABLE HERE IS NOT UNREACHABLE IN PRODUCTION, and the difference is
	// worth the extra sentence because "the contract cannot build it" reads too
	// easily as "it cannot happen". A collision is real: dependency row ids are
	// derived from (issue_id, target) and omit the table, so a wisp PROMOTED to
	// durable, or two Dolt clones MERGED, leave one logical edge in both tables.
	// TestDependentRecordsCrossTableCollision constructs exactly that state and
	// names those two paths. What this role promises against it is the sum, the
	// same answer the raw counts give, and a caller that needs the distinct
	// total is asking CountDependentRecords' question rather than this one.
	//
	// A missing anchor counts 0.
	Count int64
	// Missing reports that no issue and no wisp carries this id.
	//
	// It exists for AnchorEdges.Missing's reason and one sharper: a count of 0
	// is otherwise indistinguishable from a typo, and 0 is the COMMON answer —
	// most issues have no edges in at least one direction — so the typo would
	// never surface. This is the field that keeps this role from being a
	// question a caller cannot tell it got wrong.
	//
	// A missing anchor's count is 0 even where orphaned rows are still keyed to
	// it: a dependency row whose source has been deleted is orphaned data, and
	// counting it would contradict the flag beside it. That mirrors
	// FinishEdgeRead, which drops such rows from a missing anchor's edge list.
	//
	// DANGLING EDGES ARE NOT MISSING ANCHORS. This flag is about the ANCHOR. An
	// edge whose other end names nothing is counted like any other edge, and
	// nothing here reports on it.
	Missing bool
}

// EdgeCountResult is the per-anchor answer.
type EdgeCountResult struct {
	// Anchors carries one entry per DISTINCT requested id, in the order the
	// request first named it. It is never nil for a successful call, and it is a
	// slice rather than a map for EdgeReadResult.Anchors's reason: the request's
	// order is part of the answer, and a map would have made that ordering each
	// surface's own invention. A map would also be the one result shape a wire
	// operation cannot carry without inventing a key vocabulary.
	Anchors []AnchorEdgeCount
}

// GraphCounter describes counting DEPENDENCY EDGES around several anchors at
// once — and, like Counter, EdgeReader and Relations, a role with its own
// accessor. A new capability gets a new role interface and its own accessor;
// never append a method here.
//
// IT IS ITS OWN ROLE RATHER THAN A THIRD Counter METHOD. Counter answers about
// a set of ISSUES described by a predicate over one table; its request carries
// twenty-odd filter fields and none of them says anything about an edge. This
// role answers about EDGES, is anchored on ids rather than described by a
// predicate, and its answer is per-anchor rather than one number. Folding it in
// would have meant a CountRequest carrying an anchor list and a direction that
// every other count must ignore.
//
// IT IS ALSO NOT EdgeReader COUNTED. That role answers with the edge ROWS, in a
// pinned order, for the outbound direction only; the rows are the answer there,
// and the front door prints them. Here the number is the answer, both directions
// are askable, and no row is materialized — which is the point, because the
// surfaces that want these numbers (`bd show`'s header, a list view's
// per-row decoration) want them for issues whose edges they will never print.
//
// WHAT IS DELIBERATELY NOT HERE, and why, since the never-append rule makes
// each of these a decision rather than an omission:
//
//   - THE COMMENT COUNT. `bd show` prints one beside the two edge counts and
//     the raw seam publishes CountIssueComments, so it is genuinely consumed —
//     but a comment is not an edge, it has no direction, no type and no far
//     end, and every field of EdgeCountRequest below IDs would be meaningless
//     to it. It is a different QUESTION in the sense the role test means, and it
//     belongs beside Commenter.
//   - THE EVENT COUNT. CountEvents has no caller in this tree at all, and the
//     audit journal is a plane of its own with its own retention and scoping
//     rules. It follows the journal, not the graph.
//   - THE ISSUE COUNT and its grouped form, which are Counter's, and the READY
//     count, which is ReadyCounter's.
//   - THE PAIRED, BLOCKS-ONLY BATCH the raw seam publishes as
//     GetDependencyCounts. Its answer is expressible here as two calls with
//     Types set to the blocking type, and its callers are front doors this role
//     does not re-route; naming it as a third method would have added a shape
//     — a map keyed by id carrying two numbers — that no wire operation can
//     carry and that this result deliberately does not have.
//
// Implementations never mutate caller-owned request values, snapshot the
// request at method entry, and apply validation and normalization only to
// attempt-local clones. EdgeCountRequest travels by value, so IDs and Types are
// the members a body could otherwise write through to the caller: reading them
// into a set is fine, sorting or de-duplicating them in place is not.
//
// Counting edges is a READ. Nothing here records a history entry, fires a
// completion hook or changes a row, and a refusal changes nothing either.
// Deterministic request-validation failures match ErrValidation; result values
// are unspecified when error is non-nil.
//
// THERE WAS NO FRONT DOOR AT ALL IN THE SLICE THAT INTRODUCED THIS ROLE,
// neither CLI nor HTTP, and that was written down here rather than left to be
// inferred — the way VersionReconciler writes down its absent HTTP half and
// BatchApplier its absent CLI. What a facade-only slice buys is ONE place where
// the direction, the plane span, the missing-anchor rule and the status
// asymmetry are stated and held to on three legs, before either surface has to
// agree with the other.
//
// THE HTTP HALF HAS SINCE LANDED: GET /v0/beads/dependencies:count publishes
// this role's whole filter surface, and the anchor bound this request
// deliberately leaves to the wire is set there. THE CLI HALF STILL HAS NOT, and
// that is still a decision rather than a gap: the numbers this role answers are
// already printed by `bd show`, which reads them through internal/workapi's
// detail seam — a seam shared with an HTTP handler, so moving it is its own
// change with its own parity argument.
type GraphCounter interface {
	// CountEdges returns each anchor's edge cardinality in the requested
	// direction.
	//
	// The anchors' existence and their edge counts are read from ONE consistent
	// view, so an anchor cannot be reported missing by a probe that raced a
	// create the count then saw. That is the same promise EdgeReader.ReadEdges
	// makes, and it is the reason this is one method rather than a caller's
	// existence check followed by a caller's count.
	//
	// A request naming only anchors that are not there is a successful call
	// whose every entry reports Missing with a count of 0: there is no
	// ErrNotFound on this role, because a batch that failed for one absent id
	// would throw away the answers for the ids that were found — and because a
	// question about a set has an answer even when the set is empty, which is
	// the whole of Counter's not-found story too.
	CountEdges(ctx context.Context, req EdgeCountRequest) (EdgeCountResult, error)
}
