package issueops

import "context"

// BatchCreateItem is one issue a batch create asks for, with the edges that
// issue is created carrying.
//
// It is the per-item HALF of CreateRequest: the fields that describe ONE issue
// live here and the ones that describe the whole act — the actor, the history
// label, the prefix override — live on the request, exactly as BatchCloseItem
// splits away from CloseRequest. Nothing here is a second spelling of a
// CreateRequest field; an item field means what the CreateRequest field of the
// same name means, and where this doc says nothing that is where to read.
type BatchCreateItem struct {
	// Issue is the issue to create and must not be nil. It is read under
	// CreateRequest.Issue's rules WITHOUT EXCEPTION: the same fields are
	// accepted, the same derived ones ignored, Labels are authoritative, and
	// Issue.Comments and Issue.Dependencies must be empty because edges are
	// supplied through the item's own Dependencies below.
	//
	// AN EXPLICIT ID IS CREATE-ONLY, as it is for Lifecycle.Create: an id that
	// already names a row is ErrAlreadyExists and never a silent full-row
	// upsert. It refuses the WHOLE BATCH rather than that item, because this
	// role is all or nothing (see BatchCreator). The documented upsert surface
	// is `bd import`.
	//
	// Ephemeral and NoHistory are read PER ITEM, so one request may create
	// durable issues and ephemeral ones together. The two planes hold their
	// edges in different tables, so an edge BETWEEN the two buckets cannot be
	// written by the batch that creates both of its ends; a request that asks
	// for one is refused with everything else it asked for.
	Issue *Issue
	// Dependencies is the authoritative set of explicit edges created with this
	// item, under CreateDependency's rules.
	//
	// A TARGET MAY BE AN EARLIER ITEM OF THE SAME BATCH, named by an id that
	// earlier item spelled for itself. That is a capability the batch has and a
	// loop over Lifecycle.Create does not, and it is EARLIER rather than any
	// item on purpose: one implementation writes every row before any edge and
	// could resolve a forward reference too, the other creates item by item and
	// cannot, so a forward reference is left unpromised rather than promised
	// where only some backends keep it. An item whose id is generated cannot be
	// referred to at all, because nothing can name an id that does not exist
	// yet.
	Dependencies []CreateDependency
}

// CreateBatchRequest describes one creation of many issues.
//
// THE REQUEST IS THE TRANSACTION BOUNDARY, for the reason CloseBatchRequest
// gives: creating N issues one call at a time is N transactions and N history
// entries, and the caller that wants them to land together has no way to say
// so. Nothing here exposes begin or commit and there is no handle to hold open,
// so the boundary cannot be got wrong — it is wherever the request ends.
type CreateBatchRequest struct {
	// Actor is the creator and must not be empty. It is attributed to every
	// item, because a batch is one act by one caller: an item carrying its own
	// actor would be a second creator inside a history entry that names one.
	Actor string
	// Items are the issues to create, in the order the caller asked for them.
	// It must not be empty, as CloseBatchRequest.Items must not: a write batch
	// that writes nothing is a caller bug rather than a degenerate answer,
	// which is where it differs from the read batches that answer nothing for
	// no anchors.
	//
	// Every item appears in CreateBatchResult.Issues at the same index.
	Items []BatchCreateItem
	// Provenance labels the version-control history entry this batch records,
	// naming the surface the create came from. Empty selects the
	// implementation's default label.
	//
	// It NEVER changes WHETHER history is recorded — only how the entry reads —
	// which is UpdateRequest.Provenance's rule, inherited whole. It is HERE
	// rather than absent, unlike on CreateRequest, because the surface this
	// role serves has always named its source file in the entry
	// (`bd: create 3 issue(s) from plan.md`) and a label composed from the
	// result could not spell a file the result does not carry.
	Provenance string
	// ForceIDPrefix permits an explicit ID outside the configured prefix, for
	// every item. It is request-wide because the flag that spells it is, and
	// because writing outside the workspace's prefix is one decision a caller
	// makes about a whole file rather than a property of one row in it.
	ForceIDPrefix bool
}

// CreateBatchResult reports what the batch created.
type CreateBatchResult struct {
	// Issues has exactly one entry per requested item, in REQUEST ORDER, each a
	// detached post-create snapshot with labels and dependency records — the
	// snapshot CreateResult.Issue describes, minus comments, which a create
	// batch has no way to supply and therefore never has any.
	//
	// No entry is ever nil for a successful call: a batch that could not create
	// every item created none, so there is no index with nothing to put at it.
	//
	// It is where a caller reads the GENERATED IDS, which is the one fact the
	// request cannot carry and every front door needs.
	Issues []*Issue
}

// BatchCreator describes creating many issues as ONE durable act: the write
// side of `bd create --file`, and — like Lifecycle, BatchCloser and
// DependencyEditor — a role with its own accessor. A new capability gets a new
// role interface and its own accessor; never append a method here.
//
// IT IS ALL OR NOTHING, and that is the OPPOSITE of BatchCloser on purpose. A
// batch close skips the id it cannot close and commits the survivors, because
// the work was already done and the batch is only recording it: an agent that
// finished four of five steps and mistyped the fifth keeps the four. Nothing
// has happened yet when a create batch refuses an item, so there are no
// survivors to keep, and half a file is an outcome with no good next move — the
// caller cannot re-run the file without duplicating what landed, and cannot
// learn which half landed without reading the store. The two batch roles
// therefore make opposite promises about partial failure, and each states its
// own so neither is read as the other's default.
//
// WHAT ATOMIC MEANS HERE is everything a caller can observe: one transaction,
// every item or none, at most one history entry, and NO history entry when
// nothing durable landed. An all-ephemeral batch writes only to the
// dolt-ignored wisp tables — ignored precisely so ephemeral work never ships —
// so a durable entry naming one would be the sync artifact ignoring them exists
// to prevent. That is BatchCloser's rule for wisps and it is the same rule
// here.
//
// EVERY REQUESTED EDGE IS WRITTEN OR THE BATCH REFUSES. There is no per-edge
// report and no partial graph, for the reason Lifecycle.Create gives for
// refusing the same case: a create that reported success having silently
// dropped an edge is data loss, because the caller has no way to learn the
// relationship is missing. A target that names no row in this database is
// ErrValidation wrapping ErrNotFound and nothing is created.
//
// WHAT IS NOT "NAMING NO ROW" is a target this database was never going to
// hold: an `external:` reference, or an id whose prefix belongs to ANOTHER
// repository. DependencyEditor accepts both and stores them as external
// references, and so does this role — a plan naming work in a sibling rig
// creates its issues and its edges, exactly as it does one at a time.
//
// `bd create --graph` IS NOT THIS ROLE. A graph plan is an UPSERT over a
// declared shape and may adopt rows that already exist, where every item here
// is a create that refuses an occupied id. Two contracts, so two roles, and the
// second one is not written yet.
//
// Implementations never mutate caller-owned request values, snapshot the
// request at method entry, and apply validation and normalization only to
// attempt-local clones. CreateBatchRequest travels by value, so Items and the
// issues it points at are what a body could otherwise write through to the
// caller — in particular the ID an implementation assigns, which must land on
// the result and not on the caller's issue. Deterministic request-validation
// failures match ErrValidation and leave persistent state unchanged.
type BatchCreator interface {
	// CreateBatch creates every item and commits them together. Issues mirrors
	// Items index for index.
	//
	// EVERY REFUSAL IS THE REQUEST'S, never an item's: there is no per-item
	// outcome to put one in, because an item that refused took the batch down
	// with it. The refusals are the ones Lifecycle.Create raises, in the same
	// typed vocabulary — ErrValidation for a request or content rule,
	// ErrAlreadyExists for an occupied explicit id, ErrValidation wrapping
	// ErrNotFound for an edge whose target is not there — so a caller
	// classifies them with errors.Is rather than by reading prose.
	//
	// WHICH item caused it is in the MESSAGE and nowhere else, deliberately: a
	// structured per-item error would be result shape for an outcome that does
	// not exist. The message is prose and is not a promise — it names the
	// offending item's position, or the value that was refused, or both, and
	// the implementations do not spell it identically. A caller that needs to
	// act per item sends one request per item.
	CreateBatch(ctx context.Context, req CreateBatchRequest) (CreateBatchResult, error)
}
