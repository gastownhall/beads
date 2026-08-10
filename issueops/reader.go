package issueops

import (
	"context"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// IssueWithCounts is one row of a work page: the issue plus its relationship
// cardinalities.
type IssueWithCounts = types.IssueWithCounts

// IssueDetails is one issue with its labels, edges and cardinalities.
type IssueDetails = types.IssueDetails

// MolType classifies a molecule.
type MolType = types.MolType

// WispType classifies an ephemeral record.
type WispType = types.WispType

// ReadyRequest describes one ready-work query.
//
// It is a HIGH-LEVEL request, not a filter: normalization, alias expansion,
// validation and defaulting all happen inside the implementation. A caller
// that wants ready work says what it wants, never how the query is shaped —
// which is the whole reason two front doors cannot answer this question
// differently.
type ReadyRequest struct {
	// IssueType restricts the type. Only shorthand alias expansion is applied
	// (mr, feat, mol, enhancement, dec, adr); an unrecognized type matches
	// nothing rather than failing. Setting it drops the default type
	// exclusions, ExcludeTypes included.
	IssueType string
	// Assignee restricts to one actor. Unassigned wins over a stale Assignee.
	Assignee string
	// Unassigned restricts to rows with no assignee.
	Unassigned bool

	// Labels must ALL be present; LabelsAny requires at least one;
	// ExcludeLabels must be absent. All three are raw: normalization happens
	// inside.
	Labels        []string
	LabelsAny     []string
	ExcludeLabels []string
	// LabelPattern is a glob and LabelRegex a regular expression, both matched
	// against labels.
	LabelPattern string
	LabelRegex   string

	// Priority is an exact priority. It is a pointer because 0 is a real
	// priority: a value-plus-flag pair would let one half be filled in without
	// the other, and P0 has already been lost that way once.
	Priority *int

	// ParentID restricts to recursive descendants of one issue.
	ParentID string
	// MolType restricts to one molecule type.
	MolType *MolType

	// IncludeDeferred admits rows whose defer_until is still in the future;
	// IncludeEphemeral admits wisp-plane rows.
	IncludeDeferred  bool
	IncludeEphemeral bool

	// ExcludeTypes names types to exclude. Entries may be comma-separated;
	// splitting and alias expansion happen inside. Ignored when IssueType is
	// set.
	ExcludeTypes []string

	// MetadataFields is a top-level metadata equality filter and
	// HasMetadataKey a top-level key-presence filter. Keys are validated
	// inside.
	MetadataFields map[string]string
	HasMetadataKey string

	// Sort is the ready ordering: hybrid, priority or oldest. Empty resolves to
	// hybrid at the storage layer, which no front door should rely on — both
	// surfaces send a concrete policy, because hybrid demotes older
	// high-priority work and therefore changes the item SET once Limit
	// truncates.
	Sort string

	// Limit bounds the page. Nil means the shared ready default; 0 means
	// unlimited. It is a pointer so that "unset" and "explicitly unlimited"
	// stay distinguishable, which is what lets one constant serve both
	// surfaces.
	Limit *int
	// Offset skips the first N matching rows, on EVERY implementation. The
	// page a caller receives is the rows at positions [Offset, Offset+Limit)
	// of the answer the same request returns unpaged, in the same order.
	//
	// WHERE the skip happens is not a backend's choice either. One seam
	// renders OFFSET and one renders LIMIT without it, so both bodies reach
	// past the skipped rows and drop them in the shared page epilogue
	// (internal/workapi.FinishPageAt) — which is also the only sequence that
	// is right for a sort SQL cannot express, since that order first exists
	// after the fetch.
	//
	// An Offset past the end of the result set is an empty page and a nil
	// error, not a failure: a pager that walks off the end has its answer.
	//
	// A ready request carries no keyset position, so there is no portable way
	// to page ready work. A caller that must page across backends pages a
	// ListRequest instead — see ListRequest.Offset.
	Offset int

	// Brief drops the free-form text from every row: Description, Design,
	// AcceptanceCriteria, Notes, Payload and Waiters come back zero-valued and
	// the row carries types.Issue.IsLitePartial. See ListRequest.Brief, which
	// is the same knob on the other operation and carries the full contract.
	Brief bool
}

// ListRequest describes one issue-list query.
//
// Like ReadyRequest it is high-level: the default
// status/pinned/template/gate/infra exclusions, type validation, status
// parsing and limit defaulting are all applied inside.
//
// PINNED is TWO exclusions rather than one, because a row can be pinned two
// unrelated ways and each is hidden by its own predicate. The `pinned` STATUS
// is one of the statuses the default status exclusion covers, alongside
// `closed`; the separate Pinned FLAG — a marker on a row of any status — is
// hidden by a predicate of its own. A row carrying either is absent from a
// default listing, and the knob that takes each back off is a different one:
// see Status for the first and PinnedFlag for the second.
type ListRequest struct {
	// Status selects statuses; one name, or a comma-separated OR set. Setting
	// it REPLACES the default exclusions rather than fighting them.
	//
	// It replaces the STATUS ones only. The pinned-flag predicate survives it,
	// so `pinned` and `hooked` are the two spellings that also drop that
	// predicate — every other status narrows to unflagged rows.
	Status string
	// IssueType is validated against the workspace vocabulary — unlike
	// ReadyRequest.IssueType, which matches nothing rather than failing.
	IssueType   string
	Assignee    string
	TitleSearch string
	SpecPrefix  string
	// IDFilter is a comma-separated id set.
	IDFilter string

	Labels        []string
	LabelsAny     []string
	ExcludeLabels []string
	LabelPattern  string
	LabelRegex    string

	TitleContains    string
	DescContains     string
	NotesContains    string
	ExternalContains string
	ExternalRef      string

	CreatedBefore *time.Time
	CreatedAfter  *time.Time
	UpdatedAfter  *time.Time
	UpdatedBefore *time.Time
	ClosedAfter   *time.Time
	ClosedBefore  *time.Time
	DeferAfter    *time.Time
	DeferBefore   *time.Time
	DueAfter      *time.Time
	DueBefore     *time.Time

	EmptyDesc  bool
	NoAssignee bool
	NoLabels   bool
	SkipLabels bool
	// SkipCounts suppresses the CARDINALITY hydration exactly as SkipLabels
	// suppresses the label one: DependencyCount, DependentCount and
	// CommentCount come back ZERO, and a caller must read a zero as UNKNOWN
	// rather than as none. Nothing else about the page moves — the rows, their
	// order, Parent and the has-more verdict are what they would have been —
	// because this chooses what is HYDRATED, never which rows match.
	//
	// It is here for the renderings that print a page without those three
	// numbers. Each is its own aggregate join, and the reverse-blocker one is
	// the expensive member: it joins on an expression the embedded engine's
	// planner cannot index, which is the per-call cost that makes a counted
	// page the wrong shape for a listing that shows no counts.
	//
	// Like SkipLabels it is NOT carried onto the ReadyFlag arm; the counts are
	// hydrated there either way, which costs time and not correctness.
	SkipCounts bool

	// Brief suppresses the FREE-FORM TEXT the way SkipLabels suppresses labels
	// and SkipCounts the cardinalities: Description, Design,
	// AcceptanceCriteria, Notes, Payload and Waiters are not selected and come
	// back zero-valued. Nothing else about the page moves — the rows, their
	// order, Parent and the has-more verdict are what they would have been —
	// because this chooses what is HYDRATED, never which rows match. A
	// predicate over a heavy column (DescContains, NotesContains, EmptyDesc)
	// keeps selecting exactly the rows it selects today: WHERE is independent
	// of the SELECT shape.
	//
	// A BLANK FIELD IS AMBIGUOUS ON THE WIRE and the row says so in process:
	// all six are omitempty, so a projected row marshals identically to a
	// genuinely textless one, and the row carries types.Issue.IsLitePartial to
	// tell them apart for an in-process caller. That flag is json:"-", so a
	// wire consumer distinguishes them by having asked — the same shape the
	// repo has already argued about twice (ga-clgh/CommentsOmitted, #5550).
	//
	// UNLIKE SkipLabels and SkipCounts it IS carried onto the ReadyFlag arm,
	// and onto ReadyRequest.Brief beside it. Those two drop a NUMBER the ready
	// renderings print; this drops a body no listing prints, so carrying it is
	// what makes `--ready` and `bd ready` answer the same request the same way.
	//
	// It is the storage layer's types.IssueFilter.Lite / types.WorkFilter.Lite
	// under the name the CLI and the MCP integration already use for a
	// projection (bd show --brief-deps, the MCP's brief).
	Brief bool

	// Priority is exact; PriorityMin and PriorityMax bound a range. All three
	// are pointers for the same reason ReadyRequest.Priority is.
	Priority    *int
	PriorityMin *int
	PriorityMax *int

	// PinnedFlag and NoPinnedFlag select on the Pinned FLAG, which is not the
	// `pinned` status — see this type's own doc for why there are two of them.
	//
	// PinnedFlag answers with the FLAGGED rows and only them, at any status: it
	// drops the default status exclusions on the way, so a closed pinned row —
	// which a default listing hides twice over — is in that answer.
	//
	// NoPinnedFlag asks for the UNFLAGGED rows, which a default listing already
	// answers with. Its work is holding that predicate in place where AllFlag
	// or a pinned/hooked Status would otherwise have dropped it, so on a
	// default listing it changes nothing and on those it narrows.
	//
	// On the ReadyFlag arm neither applies: that set decides pinned for itself,
	// which is why PinnedFlag is refused there and NoPinnedFlag merely accepted
	// (see AllFlag).
	PinnedFlag       bool
	NoPinnedFlag     bool
	IncludeTemplates bool
	IncludeGates     bool
	IncludeInfra     bool
	// IncludeEphemeral admits the EPHEMERAL PLANE — the wisps TABLE, which a
	// default listing does not read at all — and admits nothing else.
	//
	// WHAT IS IN THAT PLANE is not only true ephemerals. The wisps table holds
	// every row the durable plane does not: wisps proper (ephemeral = 1) AND
	// no-history rows, which live there with ephemeral = 0. Both arrive
	// together, because this selects a TABLE rather than testing a column.
	//
	// False, the zero value, is the listing every caller has today: the durable
	// issues table alone. True merges the wisps table IN ADDITION, so under the
	// same filters the answer is a SUPERSET of the false answer. It never
	// narrows, and it never becomes ephemeral-only.
	//
	// IT IS NOT THE SAME MECHANISM AS ReadyRequest.IncludeEphemeral, despite
	// the shared name and the shared "admit in addition" reading, and the
	// difference is observable. The ready query reads BOTH planes either way
	// and its flag adds a per-ROW predicate (ephemeral = 0) when unset; this
	// one selects which TABLES are read at all. So a no-history row — in the
	// wisps table with ephemeral = 0 — is already in a DEFAULT ready answer and
	// is absent from a default listing until this flag is set. Match the two
	// fields for intent, never for row-level equivalence.
	//
	// IT IS A PLANE KNOB, NOT A TYPE KNOB — the difference from the three
	// fields above it. Each of those takes a TYPE exclusion back off; this
	// takes none off, so an ephemeral row whose type a default listing already
	// excludes stays excluded. THAT INCLUDES THE INFRA TYPES, which is the
	// combination most likely to surprise: the configured infra vocabulary
	// (agent, role and message by default) is excluded by TYPE, so ephemeral
	// agent/role/message rows need IncludeInfra as well as — or instead of —
	// this. IncludeInfra does BOTH, which makes it strictly wider; what this
	// field alone reaches is the ephemeral rows of the types a listing already
	// shows.
	//
	// THE MERGED ANSWER IS ONE ORDER. Both planes are ordered together by
	// SortBy as if they were a single table — not the durable rows followed by
	// the ephemeral ones — so a Limit truncates the merged order rather than
	// one plane's. Both implementations produce it (one merge-sorts two ordered
	// legs, the other ORDERs a SQL union), and the keyset position
	// (AfterCreatedAt/AfterID) is applied to BOTH legs, so a paged walk over
	// the merged order drops no row and repeats none.
	//
	// The caveat that walk inherits is the one every multi-page walk already
	// has, and this does not deepen it: a page is not a snapshot. A row written
	// — or, for a wisp, compacted away — between two pages is seen or missed
	// according to where it falls relative to the position, and the ephemeral
	// plane merely turns over faster than the durable one.
	//
	// On the ReadyFlag arm it is CARRIED rather than dropped; see that field.
	IncludeEphemeral bool
	// IncludeAllTypes lifts every default suppression that hides a bead from a
	// listing on account of WHAT IT IS: the three type knobs above (templates,
	// gates, infra types) AND the ephemeral plane, so the wisps table is read
	// too. Frontends whose contract is "never hide a bead" — `bd human list`,
	// where a human label is an explicit request for a person's attention —
	// set this one field instead of enumerating the knobs and then drifting
	// from them when a fourth suppression lands.
	//
	// IT IS THE UNION OF THOSE FLAGS, NOT A NEW MECHANISM. Setting it is
	// equivalent to IncludeTemplates + IncludeGates + IncludeInfra +
	// IncludeEphemeral; it narrows nothing and admits nothing they cannot.
	// Both the type suppressions and the plane decision live in workapi's
	// applyTypeSuppressions, which this flag skips entirely — so a suppression
	// added there is lifted here automatically, which is the whole point.
	//
	// IT SAYS NOTHING ABOUT STATUS. The done/frozen exclusions and the pinned
	// default are a separate axis and still apply; --status (including "all")
	// is how a caller lifts those.
	IncludeAllTypes bool
	// ExcludeTypes entries may be comma-separated; splitting happens inside.
	ExcludeTypes []string

	ParentID string
	NoParent bool
	MolType  *MolType
	// WispType matches the wisp_type COLUMN, which both tables carry. It is
	// therefore a predicate and not a plane selector: it does not admit the
	// ephemeral plane, and on a default listing — where that plane is
	// suppressed — it narrows the durable rows to those whose wisp_type
	// matches, which no ordinary durable row has. The combination that answers
	// with rows is IncludeEphemeral (or IncludeInfra) plus this, and it is
	// lawful rather than refused: the two compose as an ordinary AND, admitting
	// the plane and then narrowing it to one classification.
	WispType *WispType

	DeferredFlag bool
	OverdueFlag  bool

	// MetadataFields is a top-level metadata equality filter and
	// HasMetadataKey a top-level key-presence filter. Keys are validated
	// inside, as they are for ReadyRequest.
	MetadataFields map[string]string
	HasMetadataKey string

	// AllFlag drops the default status exclusions.
	//
	// ReadyFlag switches the query to the blocker-aware ready set. It does NOT
	// simply add a blocker predicate to this listing: that query reads a
	// narrower filter vocabulary than this request can describe, and only part
	// of the request reaches it.
	//
	// WHAT IT CARRIES: IssueType, all five label forms, Assignee, NoAssignee,
	// the exact Priority, ParentID, MolType, WispType, MetadataFields,
	// HasMetadataKey, the type exclusions (ExcludeTypes, and with them
	// IncludeGates and IncludeInfra), IncludeEphemeral — the ready query has an
	// ephemeral gate of its own, so the plane bit crosses intact and
	// IncludeInfra's plane half crosses with it — Limit, Offset and the MaxRows
	// cap with its attribution. SortBy and Reverse
	// still apply, because the display order is applied to the page after the
	// query rather than inside it. Status and AllFlag are resolved to "open"
	// and have no further effect: ready work is open work.
	//
	// WHAT IT REFUSES: every other filter here is one the ready query cannot
	// carry, so combining it with ReadyFlag returns ErrValidation naming the
	// fields rather than answering a wider question than was asked. That is
	// IDFilter, TitleSearch, SpecPrefix, the four *Contains fields,
	// ExternalRef, every Created/Updated/Closed/Defer/Due bound, DeferredFlag,
	// OverdueFlag, EmptyDesc, NoLabels, NoParent, PinnedFlag, PriorityMin,
	// PriorityMax, and the keyset position. There is no fallback: no
	// combination of the two silently widens the answer.
	//
	// TWO THINGS THE READY SET DECIDES FOR ITSELF, which no field here
	// overrides. It never returns pinned issues, which is why NoPinnedFlag is
	// accepted and PinnedFlag refused. And it applies no template predicate at
	// all, so this request's default template exclusion does not reach it and
	// IncludeTemplates changes nothing here: a template is left out of a
	// ReadyFlag listing only when its issue type is one the ready query
	// already excludes. SkipLabels and SkipCounts are likewise not carried —
	// labels and cardinalities are hydrated either way, which costs time and
	// not correctness.
	AllFlag   bool
	ReadyFlag bool

	// SortBy names the display order and Reverse inverts it. A sort the
	// database cannot express (natural-numeric id order) is resolved inside by
	// fetching the full result set and trimming, so no caller has to know
	// which sorts those are.
	SortBy  string
	Reverse bool

	// Limit bounds the page the caller RECEIVES. Nil means the shared list
	// default; 0 means unlimited. The row limit actually pushed into the query
	// is derived from it inside the implementation, together with the
	// over-fetch that detects truncation.
	Limit *int
	// Offset skips the first N matching rows, on EVERY implementation, and the
	// page is the rows at positions [Offset, Offset+Limit) of the answer the
	// same request returns unpaged. See ReadyRequest.Offset for where the skip
	// is applied and why that is not a backend's choice either.
	//
	// THE PORTABLE WAY TO PAGE IS THE KEYSET POSITION below,
	// AfterCreatedAt/AfterID: every implementation honors it, and it does not
	// skip or repeat rows when the result set changes underneath a walk.
	// Offset is here for the surfaces that already published it.
	Offset int

	// AfterCreatedAt and AfterID carry a decoded keyset position in the
	// (created_at DESC, id ASC) order. The opaque token that encodes them is a
	// transport concern and never reaches this contract.
	AfterCreatedAt *time.Time
	AfterID        string

	// MaxRows is a DEFENSIVE CAP rather than a page. It bounds how many rows
	// the query may match before the whole answer is refused; 0 disables it.
	// A request whose result set exceeds it comes back as
	// *internal/storage/issueops.ErrTooManyRows — carrying the count observed,
	// the cap, and MaxRowsSource's attribution — and NO PAGE. That is the
	// difference from Limit, whose overflow is an ordinary truncated page with
	// HasMore set. It is a circuit breaker for a caller that would rather fail
	// than wait. The type is named here rather than left as "an error" so a
	// caller can tell the cap firing from any other failure with errors.As;
	// this leaf does not import it, and no answer depends on that.
	//
	// EVERY IMPLEMENTATION HONORS IT, and they agree on when it fires: when
	// the query matched more rows than the cap allows AND the window the
	// request asked the query to TOUCH could have exceeded it. THAT WINDOW IS
	// Limit+Offset, NOT Limit. A row Offset skips is a row the query matched,
	// so an offset walks a caller toward the breaker and never past it: a cap
	// of Limit+Offset or looser never fires, because a query bounded to that
	// many rows cannot break it, and one more row of offset is what flips the
	// same request to a refusal. The two seams size that window through one
	// function (internal/storage/issueops.SearchProbeLimit) so they cannot
	// disagree about the boundary.
	//
	// Unlike SkipCounts and SkipLabels it IS carried onto the ReadyFlag arm.
	MaxRows int
	// MaxRowsSource attributes the cap to whatever knob set it — "--max-rows",
	// "BEADS_MAX_ROWS", or empty for a library caller — and that attribution is
	// what the refusal text reads back. It decides no answer: a request is
	// refused on MaxRows alone, and this only decides how the refusal reads.
	MaxRowsSource string
}

// GetRequest describes one issue-detail lookup.
type GetRequest struct {
	// ID is the exact canonical id. There is no fuzzy, prefix or substring
	// resolution here: an interactive affordance that can resolve to a
	// different issue than the caller named has no place on a contract two
	// front doors share. The issue-to-wisp fallback DOES happen inside.
	//
	// THAT FALLBACK NEEDS NO PLANE ORDER, and this contract deliberately
	// states none: no LOCAL WRITE PATH can make an id resident in both planes,
	// so the state an order would arbitrate is not reachable by writing
	// through this library. Every local write path closes it. A guarded create
	// probes BOTH tables before inserting
	// and answers ErrAlreadyExists for an id either one already holds. Every
	// batch create — import, a graph apply, a cooked formula, a markdown
	// import — refuses on top of that an id already resident in the SIBLING
	// table, skipping the row rather than hard-failing only on the
	// auto-import recovery path, which leaves the resident row alone.
	// Promotion and demotion insert into the target plane and delete from the
	// source inside ONE transaction, so their dual-presence window never
	// commits. This is why the implementations are free to probe the planes in
	// either order, and why they in fact differ — a documented non-issue
	// rather than a latent divergence (bd-yby99.22).
	//
	// TWO CAVEATS ARE WORTH CARRYING, and the second is the load-bearing one.
	//
	// First: the graph-apply path served by a unit-of-work provider gets its
	// refusal from a preflight ABOVE storage rather than from the create
	// guard, so a future caller that reached the graph-apply use case directly
	// would be the thing that made this reachable.
	//
	// Second, and NOT closed by anything here: REPLICATION. The durable plane
	// merges from a remote on pull, the ephemeral tables are dolt-ignored and
	// local-only, and no post-merge check reconciles the two — the collision
	// guard is reachable only from the create path. So a clone holding a wisp
	// whose id another clone promoted to durable receives that id into the
	// issues plane while its own wisp row survives. That needs a deterministic
	// id to collide at all, which explicit-id creates and the identically
	// minted infra wisps supply. It is a REAL residual path, it is outside
	// this library's reach, and the resulting state is worse than an
	// unspecified plane order: the merge-based lookups behind ready and search
	// hard-error for the whole store. A plane order here would not rescue it.
	//
	// So the honest statement is the scoped one — no local write path reaches
	// dual residency, replication can, and a caller that hits it has a
	// corrupt store rather than an ordering question (bd-yby99.22).
	ID string
	// IncludeDependents and IncludeComments populate the two expensive row
	// lists. Both default off: the detail view carries counts, and a caller
	// that wants the rows asks for them.
	IncludeDependents bool
	IncludeComments   bool

	// BriefDeps reduces each dependency to its identity-and-shape fields.
	BriefDeps bool
}

// IssuePage is one page of work. Ready and List share it deliberately: both
// surfaces of both operations emit IssueWithCounts today, and a leaner page
// type for ready would drift at the field level the moment anything compared
// the two.
type IssuePage struct {
	// Items is the page, in the operation's order. Never nil for a successful
	// call.
	Items []*IssueWithCounts
	// HasMore reports that the limit truncated the result.
	HasMore bool
}

// Reader describes guarded issue queries: the read counterpart of Lifecycle,
// and — like Lifecycle — a role with its own accessor. A new capability gets a
// new role interface and its own accessor; never append a method here.
//
// Each method takes the whole request and performs filter and default
// construction INTERNALLY. A caller of this interface can only say
// rd.List(ctx, req): the four-step ritual of building a config source, loading
// config, building a filter and executing it is not reachable through it.
// Implementations never mutate caller-owned request values.
//
// WHERE THAT IS ENFORCED, precisely, because the difference matters:
//
//   - The HTTP surface is on the role for all three of its issue reads, and
//     two lint rules make writing a filter there a lint failure rather than a
//     review comment. THE CLAIM at the bottom of this comment names them, and
//     names what they cover and what they do not.
//
//   - `bd show --json`'s DETAIL VIEW is on the role on BOTH routes: the direct
//     one reaches it through store.IssueReader(), the proxied one through the
//     unit-of-work provider's accessor. Its ALTERNATE views are not, and are
//     not the same question: --refs, --children, --thread and --as-of each
//     answer with their own shape, which this contract does not describe and
//     no HTTP operation serves. --short is not either; it prints one line from
//     the issue, not a detail view.
//
//     The shared implementation behind the store accessor
//     lives in internal/workapi/storereader, which the
//     cmd-bd-role-constructors depguard rule keeps out of cmd/bd: the
//     accessor is where each storage decorator adds its layer, so a command
//     that constructed a reader directly would get an unspanned one. `bd show`
//     keeps its own id RESOLUTION — fuzzy ids, cross-repo routing, --current —
//     and hands this contract the canonical id it resolved, because an
//     affordance that can answer with a different issue than the caller named
//     has no place on a contract an unattended HTTP client also calls.
//
//   - `bd list` IS, on both routes, for its PAGE — every output mode but the
//     two named next. --json and every text rendering call List below and
//     nothing else; --ready is not a route of its own on either side, it is
//     ReadyFlag, and picking the ready query from it is this role's job.
//     The two exceptions consume the FILTER as a VALUE and no page can express
//     either: --watch re-runs it on a ticker, and the hierarchical --parent
//     tree re-parents a copy of it at every level. What is NOT an exception,
//     though it looks like one, is the whole-graph dependency-record load
//     behind --format and the pretty tree: that is a different question from
//     both this role's and BlockingAnnotator's, so those renderings take their
//     page from here and load the graph beside it.
//
//     The other thing `bd list` used to do here was the per-page blocking
//     decoration, which is issueops.BlockingAnnotator's on both routes now —
//     its own role, because it is a DERIVED annotation over ids a page already
//     chose rather than a page.
//
//   - `bd ready` is NOT, on either route, and will not be until there are more
//     roles to route it through. It consumes the FILTER itself for --claim,
//     --gated, --explain and --mol. Two of its questions HAVE left the filter
//     behind, each for the role that owns it: --claim is ReadyClaimer's, and
//     the published total is ReadyCounter's, on both routes.
//
// WHAT `bd ready` DOES SHARE, stated exactly, because "not on the role" is not
// the same as "unprotected":
//
//   - CONSTRUCTION. Every route, and both implementations of this interface,
//     build from these same request types through the same two builders in
//     internal/workapi, which the builders' golden files pin.
//   - EXECUTION, on the PROXIED route only. The direct route keeps an epilogue
//     of its own and cannot give it up: it answers the strictly larger question
//     "how many rows did the limit hide" and publishes the total in its
//     pagination meta, where this role answers only "were any hidden".
//     Collapsing them would change one surface's published output. That second
//     question is not off-role, though — it is ReadyCounter's, which both
//     routes now ask through their own accessor, over this same ReadyRequest;
//     what stays outside this role is the PAGE's epilogue, not the count beside
//     it.
//
// THE CLAIM, stated once and in full so it can be checked sentence by
// sentence. SHARED: all three issue reads on the HTTP surface go through this
// role, so does `bd show --json`'s detail view on both its routes, and so does
// `bd list`'s page on both of its — in every mode but --watch and the
// hierarchical --parent tree, which take the filter instead. `bd ready` is not
// on it and shares instead the request types above, the two builders in
// internal/workapi that their golden files pin, and workapi.FinishPage on its
// proxied route only. ENFORCED,
// and by what: depguard (httpapi-transport-boundary) denies internal/workapi
// from every non-test file of internal/httpapi, so no builder is callable
// there, and a forbidigo rule denies naming types.IssueFilter or
// types.WorkFilter there at all, so no filter is writable there either — both
// are directory-scoped with no per-file exception, so a file added to that
// package tomorrow is covered the moment it exists. That same forbidigo rule
// covers cmd/bd deny-by-default with 59 named exceptions, so the files
// implementing `bd list` and `bd show` cannot write a filter, and neither can
// a file they are split or renamed into unless the new name lands on that
// list. NOT ENFORCED: the rule forbids NAMING those types, not holding a
// value, so the property is "no filter is written there", not "every filter
// there came from a builder"; test files are exempt from both rules, because
// the oracles hold filters in order to inspect them; `bd ready`'s files are
// among the 59, since its listing and --claim are handed the filter itself and
// the blocked-issue views in those files name one directly, so it is guarded by
// the builder and the golden files and not by the linter;
// cmd/bd/list_show_filter_modes.go is among them too and STAYS there — the
// count did not drop with this flip, because that file is where `bd list`'s
// two filter-consuming modes and `bd show --current` live and all three still
// need to name the type;
// GET /healthz and GET /v0/beads/context are not issue queries and are on no
// role; `bd ready`'s direct route and `bd list`'s hierarchical tree run
// epilogues of their own; and none of this is a merge gate — the rules run in
// `make ci-pr-lint` on every pull request and aggregate into the ci-gate job,
// but main carries no branch protection beyond deletion and non-fast-forward,
// so no check is GitHub-required and a red gate binds by convention.
//
// Closing the rest needs more roles (a claim role, an explain role), not more
// methods here.
type Reader interface {
	// Ready returns unblocked open work in the requested policy's order.
	Ready(ctx context.Context, req ReadyRequest) (IssuePage, error)
	// List returns issues under the request's filters, in the requested
	// display order.
	List(ctx context.Context, req ListRequest) (IssuePage, error)
	// Get returns one issue's detail view. A miss — for both the issue and the
	// wisp table — is ErrNotFound; a backend failure passes through unchanged
	// and never decays into not-found.
	Get(ctx context.Context, req GetRequest) (*IssueDetails, error)
}
