package issueops

import (
	"context"
	"fmt"
)

// Ref names ONE issue, either by an id that already exists or by the Key a
// create item earlier in the same request gave itself.
//
// EXACTLY ONE OF THE TWO IS SET. Both set is a caller that cannot say which it
// meant, neither set is a reference to nothing, and both are ErrValidation
// before anything is written.
//
// A KEY REACHES BACKWARD ONLY. A ref used as a TARGET — the row an update or a
// close acts on, or either endpoint of an edge — may name a key only if the
// create item that declares it appears EARLIER in Items. That is forced by
// what the items do rather than chosen for tidiness: an update has to see the
// row it patches, so the row must already exist when the update runs, and
// items run in declaration order. A key declared later is *RefError with
// DeclaredLater true, which is a different diagnosis from a key nothing in the
// request declares at all, and the two are distinguished so a caller can fix
// an ORDERING mistake without hunting for a typo.
//
// CreateItem.MetadataRefs is the one exception and states its own rule.
type Ref struct {
	// Key names a create item in THIS request by its CreateItem.Key.
	Key string
	// ID names a row that already exists, exactly. There is no fuzzy, prefix or
	// cross-repo resolution here, for the reason GetRequest.ID gives.
	ID string
}

// ItemKind names what one item of a batch does. It is a CLOSED set, unlike
// DependencyType: every value here is a verb this role implements, and an
// unknown one is a request the role cannot execute rather than a workspace's
// own vocabulary.
type ItemKind string

// The item vocabulary. Four verbs, and the exclusions are as deliberate as the
// inclusions — see BatchApplier for why reopen, dep_remove and a metadata
// compare-and-set are not here.
const (
	// ItemCreate creates one issue.
	ItemCreate ItemKind = "create"
	// ItemUpdate patches one existing issue.
	ItemUpdate ItemKind = "update"
	// ItemClose closes one existing issue.
	ItemClose ItemKind = "close"
	// ItemDepAdd asserts one dependency edge.
	ItemDepAdd ItemKind = "dep_add"
)

// CreateItem creates one issue and optionally NAMES it, so later items can
// reach the row without knowing the id the request has not minted yet.
type CreateItem struct {
	// Key is this item's name inside the request. It is OPTIONAL: an item
	// nothing refers to needs no name. When present it must be unique across
	// the request's create items, and it is what a later Ref.Key resolves to.
	//
	// It is not stored anywhere and it is not an id. ApplyBatchResult.Keys is
	// where the caller reads the id the key was bound to.
	Key string
	// Issue is the issue to create and must not be nil. It is read under
	// CreateRequest.Issue's rules WITHOUT EXCEPTION — the same fields accepted,
	// the same derived ones ignored, Labels authoritative, Issue.Comments and
	// Issue.Dependencies empty — because edges in this role are ITEMS and an
	// item is the only place an edge is spelled.
	//
	// AN EXPLICIT ID IS CREATE-ONLY, as it is for Lifecycle.Create: an id that
	// already names a row is ErrAlreadyExists and the WHOLE request is refused,
	// because this role is all or nothing.
	//
	// Ephemeral and NoHistory are read PER ITEM, so one request may create
	// durable issues and ephemeral ones together. The two planes hold their
	// edges in different tables, so a dep_add item BETWEEN two rows this
	// request creates on opposite planes is refused with everything else the
	// request asked for.
	Issue *Issue
	// MetadataRefs splices resolved ids into this issue's metadata: each entry
	// writes the id its Ref resolves to as the WHOLE VALUE of one top-level
	// metadata key.
	//
	// IT IS THE ONE PLACE A KEY MAY REACH FORWARD, or name this item's own Key.
	// A plan whose first step records the id of the last one it will spawn is
	// the measured shape (a retry that re-mints a bead and stamps the original's
	// id onto it), and the backward-only rule exists to make a TARGET row
	// exist before an item touches it — which a metadata VALUE does not need.
	// Every id is minted before any splice is applied, so the direction cannot
	// matter here.
	//
	// IT IS A TYPED MAP, NOT TEMPLATING. A "${key}" placeholder inside a JSON
	// string has no escape for a literal dollar-brace, collides with every
	// other templating language a caller's own values might carry, and cannot
	// be type-checked at all. This is one key, one whole value, one level deep.
	//
	// THE SPLICE IS A SECOND WRITE and it says so: the row is created first
	// with the metadata the item spelled, and the resolved keys are written
	// after every id exists, which records an update event on the spliced row.
	// A caller reading the event stream sees a create and then an update, not
	// one create carrying values nothing could have known yet.
	MetadataRefs map[string]Ref
}

// UpdateItem patches one existing issue, under UpdateRequest's rules.
type UpdateItem struct {
	// Target names the issue to patch and must resolve BACKWARD — see Ref.
	Target Ref
	// Patch is the edit, read exactly as UpdateRequest.Patch is — including
	// that Patch.Labels is the whole LabelPatch, so a label REMOVAL is
	// expressible here and not only an addition, and that Patch.Metadata keeps
	// MetadataPatch's own distinction between a value stored as JSON null and
	// one stored as the empty string. Neither is converted to the other on the
	// way through.
	Patch IssuePatch
	// ExpectedVersion, ExpectedStatus and ExpectedAssignee are
	// UpdateRequest's guards, and they are evaluated AS-MODIFIED: against the
	// row as THIS REQUEST has already changed it at this item's position, not
	// against the row as it was when the request began. An item that guards on
	// what an earlier item of the same request just wrote is asking a coherent
	// question and gets a coherent answer.
	//
	// A MISS REFUSES THE WHOLE REQUEST — the opposite of MetadataCAS, and
	// deliberately. A compare-and-set is a retry loop's ordinary path, so a
	// miss there is an answer; here it is one item of a graph the caller
	// intended to land as a unit, and committing the rest would leave a shape
	// nobody asked for. The refusal is ErrVersionMismatch, ErrStatusMismatch or
	// ErrAssigneeMismatch, wrapped in an *ItemError naming the item.
	//
	// EXPECTEDVERSION ON A ROW THIS REQUEST HAS ALREADY TOUCHED IS
	// ErrValidation, checked before anything is written. The token is
	// server-minted and rewritten by the write, so a caller cannot know what it
	// would be mid-request; any value it could send is either the pre-request
	// token (which the guard would refuse) or a guess. Guarding on a row an
	// earlier item created is the same case: the row did not exist when the
	// caller composed the request. ExpectedStatus and ExpectedAssignee carry no
	// such rule — a caller CAN know it wants the status its own earlier item
	// set, and as-modified evaluation gives it exactly that.
	//
	// EVERY Expected* MEMBER IS AN EQUALITY, and that bounds what a caller can
	// express with one. "Only if this issue is not closed" is a NEGATIVE
	// predicate and none of these can spell it; the nearest thing is
	// ExpectedStatus with the one status the row could otherwise hold, which is
	// only equivalent where the row is PROVABLY binary — a bead whose status is
	// open or closed and nothing else. Where a row can carry a third status
	// that guard silently means something narrower than it reads, so check the
	// claim before leaning on it rather than generalizing from the case that
	// worked.
	ExpectedVersion  *int64
	ExpectedStatus   *Status
	ExpectedAssignee *string
	// ForceClosePolicy and ForceAssigneeTransfer are UpdateRequest's, per item,
	// with UpdateRequest's rules — including that ForceAssigneeTransfer without
	// a Patch.Assignee is invalid.
	ForceClosePolicy      bool
	ForceAssigneeTransfer bool
}

// CloseItem closes one existing issue, under CloseRequest's rules.
type CloseItem struct {
	// Target names the issue to close and must resolve BACKWARD — see Ref.
	Target Ref
	// Reason and Session are CloseRequest's, under its first-close-wins rule.
	Reason  string
	Session string
	// Force bypasses blocker and open-child close policy, and nothing else.
	//
	// CLOSE POLICY EVALUATES AT THIS ITEM, against the row as this request has
	// already changed it — the same as-modified rule the update guards take. A
	// LATER item that gives a closed parent an open child is NOT refused,
	// because beads has no global invariant that a closed issue has no open
	// children; the policy is a gate on the closing act, not a constraint the
	// store maintains.
	Force bool
	// ExpectedVersion is CloseRequest's guard, evaluated as-modified and
	// refusing the whole request on a miss. UpdateItem.ExpectedVersion's
	// already-touched rule applies here identically.
	//
	// THERE IS NO ExpectedStatus HERE and its absence is a decision. A close is
	// idempotent — re-closing a closed issue is Changed false — so a guard
	// spelled to refuse an already-closed row is asking for a REFUSAL where
	// this verb answers with a no-op, and that belongs on an update item whose
	// Patch.Status crosses into the done category.
	ExpectedVersion *int64
}

// DepAddItem asserts ONE dependency edge, under DependencyEditor's rules.
type DepAddItem struct {
	// Source and Target are the edge's endpoints. Both must resolve BACKWARD —
	// see Ref — and an edge from a row to itself is ErrSelfDependency.
	//
	// A TARGET MAY BE A ROW THIS DATABASE DOES NOT HOLD: an "external:"
	// reference, or an id whose prefix names another repository. Both are
	// stored as external references exactly as DependencyEditor stores them,
	// so a plan naming work in a sibling rig lands its edge.
	Source Ref
	Target Ref
	// Type is required and must be a usable value, checked for BEING a value —
	// non-empty, within the column's length — and never for membership of the
	// Dep* constants, which name an open set.
	Type DependencyType
	// Metadata is the edge's type-specific JSON blob, empty for the edge types
	// that carry none.
	//
	// A WAITS-FOR EDGE IS NORMALIZED RATHER THAN STORED AS ASKED. An absent,
	// blank or `{}` Metadata on a DepWaitsFor edge is written as
	// {"gate":"all-children"}, because a stored waits-for row must be
	// self-describing: readers predating the gate column's introduction do not
	// default a missing gate, so an empty one is a row those readers get wrong.
	// A Metadata that names a gate keeps it, and a gate that is neither
	// "all-children" nor "any-children" is ErrValidation. Nothing else about
	// this member is interpreted.
	//
	// THERE IS NO TYPED WaitsFor MEMBER, and that is the shape rather than an
	// omission: every measured caller already carries the gate as metadata, the
	// typed spelling lowers to these same bytes, and WaitsForMeta carries
	// members — a spawner, an also-blocks flag — a two-field typed member could
	// not express. One spelling, and it is this one.
	Metadata string
}

// ApplyItem is ONE item of a batch: a kind plus exactly one member matching it.
//
// IT IS A TAGGED UNION RATHER THAN FOUR PARALLEL SLICES, because the request's
// ORDER is load-bearing and four slices cannot express one. It is a tagged
// union rather than an interface because the wire has to publish it, and a
// polymorphic wire member is a schema clients cannot generate a type from.
type ApplyItem struct {
	// Kind selects which member below is read. It must be one of the four
	// constants; an unknown kind is ErrValidation.
	Kind ItemKind
	// Create, Update, Close and DepAdd are the per-kind payloads. EXACTLY ONE
	// must be non-nil and it must be the one Kind names: a kind with no payload
	// is an item that does nothing, and a payload the kind does not name is an
	// item whose two halves disagree. Both are ErrValidation.
	Create *CreateItem
	Update *UpdateItem
	Close  *CloseItem
	DepAdd *DepAddItem
}

// ApplyBatchRequest describes one heterogeneous mutation of the graph.
//
// THE REQUEST IS THE TRANSACTION BOUNDARY, for the reason CreateBatchRequest
// gives, and here it is the whole point rather than an efficiency: a plan that
// creates three issues, wires them and closes the step that spawned them is ONE
// intent, and applying it as five calls leaves four intermediate states a
// crashing caller can strand.
type ApplyBatchRequest struct {
	// Actor is the author of every item and must not be empty. It is
	// attributed to the ONE history entry the request records, because a batch
	// is one act by one caller.
	Actor string
	// Items are the items to apply, in the order they are to be applied. It
	// must not be empty and must not exceed 100 entries.
	//
	// ORDER IS NEVER CHANGED. This role does not reorder parent-child edges
	// ahead of blocking ones the way DependencyEditor does, and the reason is
	// that the items are not all edges: a caller writing "clear the old
	// blockers, then set the new ones" has expressed a sequence, and a role
	// that reordered it would apply the clear after the set. What DependencyEditor
	// buys with reordering, this role buys with the end gate below.
	Items []ApplyItem
	// Provenance labels the version-control history entry, under
	// UpdateRequest.Provenance's rules: it changes how the entry READS, never
	// whether one is recorded. Empty composes a default naming how many items
	// of each kind landed and no ids — a batch can carry a hundred, and an
	// entry naming them all is the diff written twice.
	Provenance string
	// ForceIDPrefix permits an explicit ID outside the configured prefix, for
	// every create item, exactly as CreateBatchRequest.ForceIDPrefix does.
	ForceIDPrefix bool
	// SkipPerEdgeCycleCheck drops the PER-EDGE cycle probe for a caller wiring
	// a large graph, exactly as AddDependenciesRequest.SkipPerEdgeCycleCheck
	// does. It NEVER drops the whole-graph gate that runs once at the end, and
	// it never drops the self-dependency refusal.
	SkipPerEdgeCycleCheck bool
}

// ItemResult reports what ONE item did, at the index the item occupied.
type ItemResult struct {
	// Kind echoes the item's kind, so a caller walking the results does not
	// have to walk the request alongside them.
	Kind ItemKind
	// IssueID is the row the item acted on: the minted or explicit id for a
	// create, the resolved target for an update or a close, and the edge's
	// SOURCE for a dep_add.
	IssueID string
	// DependsOnID is the edge's target, and it is set for ItemDepAdd only.
	DependsOnID string
	// Changed reports whether this item persisted a semantic mutation. A
	// create is always true. An update follows UpdateResult.Changed, a close
	// follows CloseResult.Changed, and a dep_add is false for an idempotent
	// re-add of an edge that already existed with the same type.
	Changed bool
	// RowVersion is the row's optimistic-concurrency token AFTER the item, and
	// it is EQUALITY-ONLY: compare it, never order or interpret it. It is 0 for
	// ItemDepAdd, which acts on no single row's version.
	//
	// ITS COVERAGE IS PARTIAL and the partiality is inherited rather than
	// introduced: the token is rewritten by claim, close, unclaim and the
	// generic update path, and NOT by the direct-update paths that rewrite text
	// without touching it. A caller needing complete change detection combines
	// it with updated_at, status and the label set. See types.Issue.RowVersion.
	RowVersion int64
	// Issue is a detached post-item snapshot with labels and dependency
	// records, hydrated inside the transaction that applied the item — the
	// snapshot CreateResult.Issue and UpdateResult.Issue describe. It is nil
	// for ItemDepAdd, which acts on a graph rather than on a row.
	//
	// IT IS NOT ON THE WIRE, and the split is deliberate rather than an
	// oversight. The Go contract carries it because every decorator over this
	// role is written against result snapshots — the completion hooks hand a
	// script the row it is being told about, and a wrapper that had to re-read
	// each one would be N reads outside the transaction that wrote them. An
	// HTTP client gets ids and a revision and reads what it wants back, and
	// hooks never fire on that surface at all.
	Issue *Issue
}

// ApplyBatchResult reports what the batch applied.
type ApplyBatchResult struct {
	// Keys maps each create item's Key to the id it was bound to. It is the one
	// fact the request cannot carry and every caller needs, and it carries only
	// the keys the request NAMED — an unnamed create item is in Items and not
	// here.
	Keys map[string]string
	// Items has exactly one entry per requested item, in REQUEST ORDER. No
	// entry is ever missing for a successful call: a batch that could not apply
	// every item applied none, so there is no index with nothing to put at it.
	Items []ItemResult
}

// ItemError names the item a batch refusal came from and wraps the refusal.
//
// IT IS A TYPED ERROR WHERE BatchCreator PUT PROSE, and the divergence is
// justified rather than a preference. A create batch's items are homogeneous
// and positional, so an index in a message is enough to find one. These items
// are heterogeneous and may be NAMED, the wire has to publish which item
// refused as members a client can dispatch on, and a client cannot parse an
// index out of prose the implementations do not spell identically.
//
// It never appears WITH per-item outcomes, because there are none: an item that
// refused took the batch down with it, and every other item's outcome would be
// a statement about a transaction that rolled back.
type ItemError struct {
	// Index is the item's position in ApplyBatchRequest.Items.
	Index int
	// Kind is that item's kind.
	Kind ItemKind
	// Key is the create item's own Key, or the Key the refused item's target
	// ref named. It is empty when the item named nothing symbolically.
	Key string
	// IssueID is the id the item was acting on, when one was resolved before
	// the refusal. It is empty when the refusal happened before resolution — a
	// create whose id was never minted, or a ref that resolved to nothing.
	IssueID string
	// Err is the refusal itself, matchable with errors.Is through Unwrap.
	Err error
}

// Error renders the item's position and the refusal.
func (e *ItemError) Error() string {
	where := fmt.Sprintf("item %d (%s)", e.Index, e.Kind)
	switch {
	case e.Key != "" && e.IssueID != "":
		where = fmt.Sprintf("%s key %q -> %s", where, e.Key, e.IssueID)
	case e.Key != "":
		where = fmt.Sprintf("%s key %q", where, e.Key)
	case e.IssueID != "":
		where = fmt.Sprintf("%s %s", where, e.IssueID)
	}
	return fmt.Sprintf("apply batch %s: %v", where, e.Err)
}

// Unwrap exposes the refusal, so errors.Is and errors.As reach the sentinel and
// the typed errors the underlying verbs raise.
func (e *ItemError) Unwrap() error { return e.Err }

// RefError reports a Ref this request cannot resolve, and it is a validation
// refusal: it is raised before anything is written.
type RefError struct {
	// Index is the position of the item holding the bad ref.
	Index int
	// Member names WHICH ref of that item it was — "target", "source" or the
	// metadata key whose ref failed — because an item may hold two.
	Member string
	// Key is the unresolvable key.
	Key string
	// DeclaredLater distinguishes the two diagnoses. True means the key IS
	// declared in this request, but by an item at a LATER index, which is an
	// ordering mistake; false means nothing in the request declares it, which
	// is a typo or a missing item. A caller acts differently on each, and on
	// the wire the two are told apart by whether the member is present.
	DeclaredLater bool
}

// Error renders which ref failed and why.
func (e *RefError) Error() string {
	if e.DeclaredLater {
		return fmt.Sprintf("apply batch item %d: %s references key %q, which is declared later in the request; a key reaches backward only",
			e.Index, e.Member, e.Key)
	}
	return fmt.Sprintf("apply batch item %d: %s references key %q, which no item in the request declares",
		e.Index, e.Member, e.Key)
}

// Unwrap makes every unresolvable ref match ErrValidation, so a front door
// classifies it without knowing this type exists.
func (e *RefError) Unwrap() error { return ErrValidation }

// MaxApplyBatchItems is the largest request this role accepts. It is a FLAT
// bound rather than a cost model, and it is the bound the sibling batch roles
// already run under: a hundred creates and a hundred edges both land inside the
// server's ordinary request deadline today, so a per-item deadline extension
// would be new machinery answering a question nothing has asked.
const MaxApplyBatchItems = 100

// BatchApplier describes applying a HETEROGENEOUS list of graph mutations as
// ONE durable act, and — like Lifecycle, BatchCreator, BatchCloser and
// DependencyEditor — a role with its own accessor. A new capability gets a new
// role interface and its own accessor; never append a method here.
//
// IT IS A DIFFERENT QUESTION FROM ITS NEIGHBORS. BatchCreator creates N issues,
// BatchCloser closes N issues, DependencyEditor asserts N edges: each is one
// verb repeated, and a caller that needs two of them needs two transactions
// with a window between them. This role's unit is a PLAN — create these, wire
// them, close the step that spawned them — and the only way to express that
// today is a hand-composed transaction the caller opens itself, which is the
// composition every front door had to get right.
//
// IT IS ALL OR NOTHING, and unlike BatchCloser that is not a policy choice.
// Per-item outcomes are UNREPRESENTABLE here: an item whose target is a key an
// earlier item failed to create has no outcome to report, because the row it
// would have named does not exist. So there is one outcome and it is the
// request's.
//
// ORDER IS THE CONTRACT. Items apply in declaration order and are never
// reordered. What that costs is what DependencyEditor's parent-child-first pass
// buys: a blocking edge validated before the parent-child edge that makes it a
// hierarchy conflict. So this role runs a REAL END GATE instead — after every
// item has landed, it re-validates every scheduling edge the request added
// against the parent-child closure the whole request produced, and raises
// *DependencyHierarchyConflictError or ErrDependencyCycle from there.
// SkipPerEdgeCycleCheck never drops it.
//
// WHAT IS NOT AN ITEM, and why:
//
//   - REOPEN. Nothing batches reopens, and an update item whose Patch.Status
//     moves a row out of the done category covers the measured need.
//   - DEP_REMOVE. A single removal is already idempotent, so composing it needs
//     no transaction, and adding it would mean deciding what a remove-then-add
//     of the same pair inside one request means.
//   - A METADATA COMPARE-AND-SET. Its contract is that a mismatch is an ANSWER
//     (MetadataCAS.CompareAndSetKey), and this role's contract is that a
//     precondition miss refuses everything. One of the two would have to lie.
//     The guard enters here as UpdateItem's Expected* members instead.
//   - PARENT-ON-CREATE. One edge, one spelling: a parent is a dep_add item of
//     type parent-child, so the ORDER of every edge in the request is total.
//   - INLINE CREATE DEPENDENCIES, for the same reason.
//
// IT IS NOT IDEMPOTENT AND CARRIES NO IDEMPOTENCY KEY. Replaying a request
// applies it again — the creates mint new ids, the edges are idempotent, the
// closes are no-ops. That is deliberate: an idempotency RECORD is itself a
// write, so a caller that needs one makes it an ITEM of the batch, and the
// record then lands or rolls back with the work it describes. A key on the
// request would be a second, weaker mechanism for the same thing.
//
// EPHEMERALITY IS PER ITEM, exactly as it is for BatchCreator: one request may
// create durable issues and wisps together, and an edge BETWEEN the two planes
// whose ends this request creates cannot be written by it. HISTORY IS ONE ENTRY
// FOR THE REQUEST, attributed to Actor, and none at all when nothing durable
// landed — an all-wisp batch writes only to the dolt-ignored wisp tables, so an
// entry naming one would be the sync artifact ignoring them exists to prevent.
//
// THERE IS NO `bd` COMMAND BEHIND THIS ROLE, and that is a decision rather than
// an unfinished half. A plan is composed by a program — the thing that knows
// which step spawned which — and the shell spelling of one would be a file
// format: a second request grammar to version, to validate and to keep in step
// with the wire's. The measured callers already speak HTTP or hold the role
// directly. `bd create --file` and `bd dep add --file` remain the file-shaped
// front doors for the two homogeneous cases they already serve.
//
// Implementations never mutate caller-owned request values, snapshot the
// request at method entry, and apply validation and normalization only to
// attempt-local clones. Deterministic request-validation failures match
// ErrValidation and leave persistent state unchanged.
type BatchApplier interface {
	// ApplyBatch applies every item in order and commits them together.
	// Items mirrors the request's items index for index, and Keys carries the
	// id each named create was bound to.
	//
	// EVERY REFUSAL IS THE REQUEST'S. A refusal raised by an item arrives as an
	// *ItemError naming that item's index, kind, key and resolved id, wrapping
	// the verb's own typed refusal — so ErrAlreadyExists for an occupied id,
	// ErrVersionMismatch, ErrStatusMismatch and ErrAssigneeMismatch for a
	// precondition, *DependencyTypeConflictError, ErrDependencyCycle,
	// *DependencyHierarchyConflictError and the endpoint-not-found errors for
	// an edge, all still match errors.Is and errors.As through it.
	//
	// REFUSALS RAISED BEFORE ANY ITEM RUNS are the request's own and carry no
	// item wrapper unless they name one: an empty Actor, an empty or
	// over-length Items, an item whose Kind and payload disagree, a Ref with
	// neither or both members set, an ExpectedVersion on a row an earlier item
	// already touched. An unresolvable Ref is a *RefError, which names the item
	// itself and matches ErrValidation.
	//
	// The END GATE runs after every item and refuses the whole request, so an
	// edge that is legal on its own and illegal in the graph this request built
	// is caught here and nowhere else.
	ApplyBatch(ctx context.Context, req ApplyBatchRequest) (ApplyBatchResult, error)
}
