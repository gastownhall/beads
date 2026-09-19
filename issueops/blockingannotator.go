package issueops

import (
	"context"
)

// BlockingRequest describes one blocking-annotation read over the ids of a page
// that has already been fetched.
//
// It is a BATCH request in the same sense EdgeReadRequest is: the question is
// asked about a whole page at once, because that is what a renderer needs and
// because asking it per row would cost one round trip per row.
type BlockingRequest struct {
	// IDs are the ids to annotate, each an EXACT canonical id. There is no
	// fuzzy, prefix or substring resolution here, for the reason
	// EdgeReadRequest.IDs gives: an affordance that can answer ABOUT A
	// DIFFERENT ISSUE than the caller named has no place on a contract an
	// unattended client calls.
	//
	// AN EMPTY ENTRY IS ErrValidation. The empty string names nothing a caller
	// can have meant, and an annotation keyed by name has nowhere to put a
	// nameless row.
	//
	// AN EMPTY SLICE IS NOT AN ERROR: it asks about no ids and the answer is no
	// annotations, for the reason EdgeReadRequest.IDs and Counter both give.
	//
	// REPEATS COLLAPSE, at the position of the first mention, exactly as they do
	// for EdgeReadRequest: the answer is keyed by id and a second entry for the
	// same key carries no second fact.
	IDs []string
}

// IssueBlocking is one issue's blocking decoration.
//
// The json tags are the wire names: this struct IS the element
// GET /v0/beads/dependencies/blocking emits, so there is no second shape for
// the same fact.
type IssueBlocking struct {
	// ID is the annotated id, spelled exactly as the request spelled it.
	ID string `json:"id"`
	// BlockedBy are the OPEN issues this one is blocked by: the targets of its
	// `blocks` edges whose own status is not closed.
	//
	// Never nil for a successful call, so a caller that marshals it emits an
	// empty array rather than null.
	BlockedBy []string `json:"blocked_by"`
	// Blocks are the issues this one blocks: the sources of the `blocks` edges
	// pointing AT it. It is empty when this issue is itself closed, which is
	// the same rule BlockedBy applies from the other end — an edge is live
	// exactly when its blocker is open — stated twice because the two arms read
	// the status of different rows to decide it.
	//
	// Never nil for a successful call.
	Blocks []string `json:"blocks"`
	// Parent is this issue's parent id, or empty when it has none and when the
	// parent it has is closed.
	//
	// AT MOST ONE is reported. The schema permits an issue to carry several
	// `parent-child` edges, and this role reports one of them; WHICH one is not
	// specified here, because both implementations reduce to a single parent
	// before this contract sees the rows and neither picks deliberately. A
	// caller that needs every structural edge asks EdgeReader, which answers
	// with the rows.
	Parent string `json:"parent,omitempty"`
}

// BlockingResult is the per-id answer.
type BlockingResult struct {
	// Items carries one entry per DISTINCT requested id, in the order the
	// request first named it. It is never nil for a successful call, and it is
	// a slice rather than three maps for the reason the three maps are the
	// shape this role replaces: a caller handed BlockedBy, Blocks and Parent
	// separately can be handed two of them, and an id present in one and absent
	// from another says nothing a reader can act on.
	Items []IssueBlocking
}

// BlockingAnnotator describes the DERIVED blocking decoration a listing prints
// beside each row — `(parent: X, blocked by: Y, blocks: Z)` — and, like
// EdgeReader, a role with its own accessor. A new capability gets a new role
// interface and its own accessor; never append a method here.
//
// IT IS NOT EdgeReader, and the two are deliberately separate (the design pass
// asked and the answer was two roles, not one). EdgeReader answers with the
// STORED ROWS: every edge type, targets spelled as stored including
// `external:` references and ids belonging to another repository, no status
// consulted, and a per-anchor Missing flag from an existence probe. This role
// answers a DERIVED, BEST-EFFORT summary: two edge types out of the whole
// vocabulary, closed blockers dropped, the inbound direction included, ids and
// nothing else. Merging them would give one role two answer shapes and two miss
// policies, which is the accretion the governing rule exists to stop.
//
// IT IS NOT Reader GROWTH either. Reader.List answers with a PAGE — rows, an
// order, a limit and a has-more verdict. This is an annotation OVER a page that
// has already been chosen: it has no order of its own, nothing it returns
// depends on how the page was selected, and a caller holding ids from anywhere
// can ask it. A ListRequest field would have implied the annotation was part of
// the query, and a page fetched with a cap or a cursor would then carry a knob
// that decides nothing.
//
// THERE IS NO MISSING FLAG, which is the sharpest difference from EdgeReader
// and it is a decision rather than an omission. This role runs no existence
// probe: an id that names nothing and an id with no live blocking edges
// decorate identically — nothing is printed beside either — so the probe would
// be a read whose answer no caller can use. A front door that needs to know
// whether an id exists asks Reader.Get or EdgeReader, both of which say.
//
// BEST-EFFORT MEANS THE ANSWER IS DERIVED, NOT THAT IT MAY BE WRONG. Every
// promise below holds; what "best-effort" names is the standing the annotation
// has at a front door, which prints it beside a row and never decides anything
// on it. What a front door does with a FAILURE is its own policy and not this
// contract's — and `bd list`'s two routes do not agree on it today: the direct
// one renders the page undecorated and the proxied one fails the command. This
// role reports its failures either way; converging the two is a behavior
// question recorded for the owner as A-blk-1.
//
// A BLOCKER THIS DATABASE HOLDS NO ROW FOR COUNTS AS OPEN. An `external:`
// reference, an id in another repository's namespace, and an id whose issue was
// deleted out from under its edges are all statuses this store cannot read, and
// an unreadable status is not `closed`. So such an edge still blocks. That is
// the conservative reading — a listing that hid a blocker it could not resolve
// would report work as unblocked on the strength of a row it never found — and
// it is the behavior both implementations already have.
//
// BOTH PLANES ARE ONE GRAPH here, as they are for EdgeReader: a durable issue
// blocked by a wisp is reported blocked, and a wisp is annotated from the
// ephemeral dependency tier.
//
// THE ORDER WITHIN BlockedBy AND Blocks IS PINNED: ascending by id, with
// repeats collapsed. It is pinned rather than left to the query because both
// lists are joined into one line of output, so the query's natural order is
// user-visible; and repeats collapse because an id can be reached through
// either dependency tier and a caller counting the entries would otherwise
// count one edge twice.
//
// Implementations never mutate caller-owned request values, snapshot the
// request at method entry, and apply validation and normalization only to
// attempt-local clones. BlockingRequest travels by value, so IDs is the one
// member a body could write through to the caller: reading it into a set is
// fine, sorting or de-duplicating it in place is not.
//
// Annotating is a READ. Nothing here records a history entry, fires a
// completion hook or changes a row, and a refusal changes nothing either.
// Deterministic request-validation failures match ErrValidation; result values
// are unspecified when error is non-nil.
type BlockingAnnotator interface {
	// AnnotateBlocking returns each id's blocking decoration.
	//
	// The outbound edges, the inbound edges and the statuses that decide which
	// of them are live are read from ONE consistent view, so an answer cannot
	// report an issue blocked by a row that a later read in the same call would
	// have found closed. That is the same promise EdgeReader makes for its
	// probe and its edge read, and it is why this is one method rather than a
	// caller's edge read followed by a caller's status lookup.
	//
	// A request naming only ids that are not there is a successful call whose
	// every entry is bare: there is no ErrNotFound on this role, for the reason
	// there is no Missing flag on it.
	AnnotateBlocking(ctx context.Context, req BlockingRequest) (BlockingResult, error)
}
