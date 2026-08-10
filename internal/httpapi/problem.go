package httpapi

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"syscall"

	"github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/issueops"
)

// Every non-2xx byte this server emits is an RFC 9457 problem+json document
// (apigen.Problem, generated from the spec — there is one error shape here).
// This file owns the whole mapping: sentinel error in, status + machine code
// out, matched exclusively with errors.Is/errors.As. Never `err != nil ->
// status`.
//
// SENTINEL NOTE: the mapping keys on the sentinel VALUES, never on where they
// are declared. A parallel refactor relocates the canonical Err* declarations
// into a leaf issueops package and aliases them back from internal/storage
// with the same pointers, so errors.Is keeps matching either spelling with no
// edit here. Do not re-express these rows as package-path or message checks.

// Code is the machine-readable member of the problem envelope, and the only
// member a client may dispatch on.
//
// The vocabulary is a one-way door: renaming or removing a documented
// status+code pair breaks the wire. Adding one does not, which is why clients
// are told to default-branch on unknown codes within a status class. Keep it
// at what the v0 operations actually need.
type Code string

// The v0 code vocabulary. Every value here is documented in the spec, and
// TestSpecStatusCodesMatchHandlerTable fails if the two ever disagree.
const (
	// CodeInvalidArgument covers every request-validation refusal: an unknown
	// query parameter, a malformed value, an invalid actor, an unparseable or
	// oversized body, limit=0 under --allow-non-loopback, and the Host-header
	// middleware. The 400 carries `param` and `reason` so clients never have
	// to tell those cases apart by reading prose.
	CodeInvalidArgument Code = "invalid_argument"
	// CodeInvalidCursor is a separate code because a stale or foreign cursor
	// is a normal client situation with an obvious recovery (restart paging),
	// not a client bug.
	CodeInvalidCursor Code = "invalid_cursor"
	CodeNotFound      Code = "not_found"
	// CodeAlreadyClaimed reports a live foreign holder. Where the `assignee`
	// extension member is present it is the holder, read inside the same
	// transaction and never parsed out of the sentinel's message text — but
	// PRESENCE IS PER PRODUCER, and there are four:
	//
	//   - claimIssue always attaches it: the claim's conflict path reads the
	//     row it lost to, so it has the holder in hand.
	//   - updateIssue and applyBatch attach it CONDITIONALLY, when the refusing
	//     error carried one. The assignee fence
	//     (AuthorizeAssigneeTransferWithPools) refuses without naming the
	//     holder, so an implementation that reported none leaves it absent.
	//   - releaseIssue NEVER attaches it. It is the same fence pointed the
	//     other way and it names nobody.
	//
	// A client therefore treats the member as optional on every operation but
	// the claim, and re-reads the row when it is absent. Absence means "this
	// refusal could not name the holder", never "nobody holds it".
	CodeAlreadyClaimed Code = "already_claimed"
	CodeNotClaimable   Code = "not_claimable"
	// CodeNotReleasable is a row refusing to give up a claim, and it covers
	// the two conditions releaseIssue's role reports for that: the row holds
	// no claim, or its status is neither open nor in_progress.
	//
	// A 409 for CodeNotClosable's reason: the body is well-formed and STATE
	// refuses it, so the same request succeeds or fails on something the client
	// cannot see without reading it.
	//
	// IT IS MINTED RATHER THAN FOLDED INTO CodeNotClaimable, which is the
	// nearest existing code and covers a superset of the same statuses today.
	// The coincidence is not a contract: the release transition is pinned to
	// {open, in_progress} by releasableStatus and the claim's eligibility is a
	// separate predicate, so the two sets are free to diverge and would then be
	// one code meaning two things. Worse, the unheld half would be an outright
	// lie — an open, unassigned row is the MOST claimable row a workspace has,
	// and answering `not_claimable` about it would send a client somewhere
	// there is nothing to find.
	//
	// IT COVERS BOTH CONDITIONS UNDER ONE CODE, and that is a deliberate
	// start-narrow CHOICE rather than a consequence of missing information.
	//
	// THE TWO CONDITIONS ARE FULLY DISTINGUISHABLE HERE. ErrNotClaimed and
	// ErrNotReleasable are two distinct typed sentinels, and failRelease holds
	// both in one case arm — so a future split needs no archeology and no
	// prose-scraping: it is a mapping change in that arm plus a code in this
	// block and a line in the document. What IS unavailable typed is the
	// OBSERVATION either refusal made — the status it saw, the emptiness of the
	// assignee — because both format those into their messages and this surface
	// does not scrape its own prose. That is why the code carries no extension
	// member, and it is a narrower statement than "the refusals are
	// indistinguishable", which they are not.
	//
	// The split is deferred rather than refused because one code is the
	// reversible direction: splitting later is an ADDITION, which the document
	// already tells clients to tolerate, while merging two published codes into
	// one is the removal that breaks the wire. A client that needs the
	// distinction before then reads the row, which the operation description
	// tells it to do for a safety reason rather than a taste one.
	CodeNotReleasable Code = "not_releasable"
	// CodeNotClosable is close policy refusing an unforced close: open
	// children, or a live blocker. The open-children refusal carries the count
	// in the `open_children` extension member, read inside the refusing
	// transaction — never parsed out of the sentinel's message text — and its
	// PRESENCE is how a client tells the two refusals apart without prose.
	//
	// A 409 rather than the delete precedent's 400: this is a statement about
	// the current state of one named resource, so the same request succeeds or
	// fails on state the client cannot see without reading it. That is the
	// not_claimable situation and it gets the not_claimable answer.
	CodeNotClosable Code = "not_closable"
	// CodeDependencyCycle covers BOTH never-makes-progress refusals a requested
	// edge set can earn: a scheduling cycle, and a blocking edge against the
	// issue's own ancestor or descendant. They are one code because they have
	// one client recovery — rethink the edge, with no force bypass for either —
	// and codes are the vocabulary of recovery. The typed distinction is NOT
	// lost: the hierarchy refusal additionally carries `issue_id`, `blocker_id`
	// and `blocker_is_ancestor`, read inside the refusing transaction, and
	// member presence is the discriminator.
	CodeDependencyCycle Code = "dependency_cycle"
	// CodeDependencyExists is the pair that already carries an edge of a
	// DIFFERENT type, with both types in `existing_type`/`requested_type`.
	CodeDependencyExists Code = "dependency_exists"
	// CodeAlreadyExists is a create whose EXPLICIT id already names a stored
	// row. `param` names the offending member and, on a batch operation, the
	// item members name which item carried it.
	//
	// A 409 rather than a 400, and the distinction is the one CodeNotClosable
	// draws: the request is well-formed and stays well-formed: what refuses it
	// is STATE the client cannot see without reading it, and recovery is to
	// look at that state — adopt the row, pick another id, or stop. A 400 says
	// "this body is malformed, fix it and it will work", which is false here:
	// the identical body succeeded before the id was taken and would succeed
	// again against a workspace that never took it.
	//
	// It is minted rather than folded into CodeInvalidArgument because the two
	// are not narrowable later: widening a 400 to a 409 changes the status an
	// existing client dispatches on, while a 409 that turns out to be
	// unreachable retires for free.
	CodeAlreadyExists Code = "already_exists"
	// CodePreconditionFailed is a compare-and-set guard that MISSED on an
	// operation whose contract is that a miss refuses everything. The expected
	// values travel in typed extension members; a batch operation adds the
	// members naming which item carried the guard.
	//
	// A 409 for CodeNotClosable's reason: the request is fine as a request and
	// the STATE refuses it, so the same body succeeds or fails on something the
	// client cannot see without reading it.
	//
	// IT IS NOT THE ANSWER TO EVERY LOST COMPARE-AND-SET on this surface, and
	// the split is the point. Where a miss is the ordinary path of a retry loop
	// — the metadata compare-and-set — it is a 200 carrying the current value,
	// because putting a loop's normal iteration in the error channel would make
	// the value that loop needs next travel as a problem member. This code is
	// for the opposite contract: a guard on one step of a plan the caller meant
	// to land whole, where a miss took the entire request down and there is
	// nothing to report but the refusal.
	//
	// The expected/actual members are SPLIT BY TYPE — expected_version and
	// actual_version, expected_status and actual_status, expected_assignee and
	// actual_assignee — rather than one polymorphic pair, because a generic
	// `expected`/`actual` of "a version or a status or an assignee" is a schema
	// alternation and this document's x-go-type doctrine admits no composition
	// keyword to spell one.
	CodePreconditionFailed Code = "precondition_failed"
	// CodeEventsJournalDisabled is the events journal being OFF on the served
	// workspace. It exists because the alternative answer is a lie: a disabled
	// journal reads as zero rows and a head of zero, which is byte-identical to
	// an enabled journal nothing has written to yet — so a consumer would poll a
	// workspace that will never produce a record and call it "caught up"
	// forever. This is the one refusal on this surface that a client cannot
	// discover any other way.
	//
	// A 409 for the reason CodeNotClosable gives: it is a statement about the
	// current state of the workspace, which the same request stops earning the
	// moment an operator sets `events-journal true` and restarts the server.
	// Not a 404 — the operation and the resource both exist — and not a 501,
	// which this surface reserves for an operation this BUILD does not
	// implement, whereas every build implements this one.
	CodeEventsJournalDisabled Code = "events_journal_disabled"
	// CodeEventsJournalTruncated is a journal read whose checkpoint has fallen
	// below the retained window. The value is storage's own constant, so the
	// code a `bd events tail --json` failure carries and the code this surface
	// emits cannot drift to two spellings of one condition.
	//
	// A 410 Gone, and the only one in the vocabulary: the records the caller
	// asked for existed, were addressable by exactly this request, and have been
	// deliberately deleted. That is what 410 means and what 404 does not — a 404
	// would say the resource never existed and invite a retry, and the whole
	// point of this refusal is that retrying the same `since` can never succeed.
	// The `since`, `floor` and `head` members carry the window the server can
	// still serve, so the recovery (resume from `floor - 1` and accept the gap,
	// or re-baseline) is a decision the client makes from data rather than prose.
	CodeEventsJournalTruncated Code = Code(storage.EventsJournalTruncatedCode)
	// CodeEventsWatchSaturated is this server already holding as many open
	// journal streams as it will. It is not CodeBusy, even though both are a
	// 503 with a Retry-After, because the two carry different recoveries: busy
	// says the database is congested and the same request will work shortly,
	// while this says a bounded resource is fully subscribed by connections that
	// may last hours — and the caller has a second recovery available that busy
	// does not offer, namely the paged read, which holds nothing between
	// requests and is never refused for this reason.
	CodeEventsWatchSaturated Code = "events_watch_saturated"
	// CodeUnauthenticated is a missing, malformed or unrecognized bearer
	// credential on a server that was configured with one. It is a DEPLOYMENT
	// posture, not a property of the operation: a server started with no token
	// file never emits it. The three client mistakes are deliberately one code
	// — telling them apart on the wire would tell an unauthenticated caller
	// which of its guesses was closer.
	CodeUnauthenticated Code = "unauthenticated"
	// CodeBusy is retryable contention: the transaction retry budget was
	// exhausted, or the in-flight request limit was saturated.
	CodeBusy Code = "busy"
	// CodeDBUnavailable is a retryable connectivity failure reaching the
	// database.
	CodeDBUnavailable Code = "db_unavailable"
	CodeInternal      Code = "internal"
)

// codeClientClosed is a LOG-ONLY outcome, not wire vocabulary: it is
// deliberately absent from codeStatus, from operationCodes and from the
// document, and it never reaches a response body — the client it describes has
// already gone. It exists so that the request line does not book a client
// hanging up as a server fault. See failErr.
const codeClientClosed Code = "client_closed"

// codeStatus freezes one HTTP status per code. A code that could arrive with
// two different statuses would defeat the point of dispatching on it.
var codeStatus = map[Code]int{
	CodeInvalidArgument:  http.StatusBadRequest,
	CodeInvalidCursor:    http.StatusBadRequest,
	CodeUnauthenticated:  http.StatusUnauthorized,
	CodeNotFound:         http.StatusNotFound,
	CodeAlreadyClaimed:   http.StatusConflict,
	CodeNotClaimable:     http.StatusConflict,
	CodeNotClosable:      http.StatusConflict,
	CodeNotReleasable:    http.StatusConflict,
	CodeDependencyCycle:  http.StatusConflict,
	CodeDependencyExists: http.StatusConflict,
	CodeAlreadyExists:    http.StatusConflict,

	CodePreconditionFailed: http.StatusConflict,

	CodeEventsJournalDisabled:  http.StatusConflict,
	CodeEventsJournalTruncated: http.StatusGone,
	CodeEventsWatchSaturated:   http.StatusServiceUnavailable,

	CodeBusy:          http.StatusServiceUnavailable,
	CodeDBUnavailable: http.StatusServiceUnavailable,
	CodeInternal:      http.StatusInternalServerError,
}

// Status returns the HTTP status frozen to c, or 0 if c is not in the v0
// vocabulary.
func (c Code) Status() int { return codeStatus[c] }

// Reason distinguishes the two client postures behind a 400
// CodeInvalidArgument, so that telling them apart never requires parsing
// `detail`. The set may grow; clients default-branch on unknown values.
type Reason string

const (
	// ReasonUnknownParameter means this server does not know that parameter:
	// version skew. The client degrades or falls back. It is also a client's
	// only per-parameter capability probe, since `capabilities` is
	// operation-level.
	ReasonUnknownParameter Reason = "unknown_parameter"
	// ReasonInvalidValue means the server will not act on that value:
	// malformed, outside the vocabulary, or legal-but-refused in this
	// server's configuration (limit=0 under --allow-non-loopback). The
	// recovery is always to send something different, never to retry; the
	// detail says which case it was.
	ReasonInvalidValue Reason = "invalid_value"
	// ReasonProjectMismatch means the request stamped a Bd-Project-Id that is
	// not the project this server serves. Like the Host-header refusal it is a
	// document-level 400 reachable on every enforced route rather than
	// per-operation behavior, and it is the one refusal that carries
	// `server_project_id`. The recovery is to stop stamping this server with
	// another workspace's id, never to retry the same request.
	ReasonProjectMismatch Reason = "project_mismatch"
)

// staticDetail is the set of codes whose `detail` is FIXED, whatever the
// caller or the call site passed. newResult overrides the supplied detail for
// every code listed here, which is what makes the guarantee structural rather
// than a rule each call site has to remember.
//
// Every 5xx is on it. The underlying error goes to the server log and nowhere
// else: driver and dial errors routinely embed the DSN — go-sql-driver renders
// connection targets as user@tcp(127.0.0.1:PORT)/db, net dial errors carry the
// same host:port — and query errors can carry SQL fragments. The moment the
// server is bound with --allow-non-loopback, a verbose 5xx detail is an
// information-disclosure channel to network peers.
//
// The 401 is on it for the mirror-image reason, and it is the one row that is
// not a 5xx: the caller's own input here is a CREDENTIAL. Ordinary 4xx details
// stay specific precisely because they reflect the caller's input back, which
// is exactly what must never happen to a presented token — it would land in
// every client log and proxy trace between here and the caller.
var staticDetail = map[Code]string{
	CodeUnauthenticated: "missing or invalid bearer token",
	CodeBusy:            "the server is busy; retry shortly",
	CodeDBUnavailable:   "database temporarily unavailable; retry",
	CodeInternal:        "internal server error",
}

// Retry-After values, in seconds.
const (
	// retryAfterContention follows an exhausted transaction retry budget. The
	// budget spans many seconds of observed write contention, so a one-second
	// comeback would invite a convoy of retries that each hold a slot while
	// they wait — starving reads exactly when the server is busiest.
	retryAfterContention = 5
	// retryAfterSaturation follows an in-flight-limit wait timeout. Slot
	// pressure clears quickly.
	retryAfterSaturation = 1
	// retryAfterWatchSaturation follows a refused journal stream. Streams are
	// held for as long as their consumers stay connected — minutes to hours —
	// so a slot opens on a human timescale rather than a request one, and the
	// one-second comeback the slot limiter offers would be a busy loop against a
	// condition that has not changed.
	retryAfterWatchSaturation = 30
)

// ErrBusy reports that the in-flight request limiter refused to admit the
// request within its bounded wait. The limiter itself lands with the server;
// the sentinel lives here so the mapping owns the whole 503 vocabulary.
var ErrBusy = errors.New("server busy")

// Operation ids, matching the spec's operationId values exactly.
const (
	OpHealth        = "health"
	OpGetContext    = "getContext"
	OpListReadyWork = "listReadyWork"
	OpGetStats      = "getStats"
	OpListIssues    = "listIssues"
	OpGetIssue      = "getIssue"
	OpClaimIssue    = "claimIssue"
	// OpBatchCloseIssues closes many issues as one transaction, behind
	// issueops.BatchCloser. It is the surface's ONLY operation whose 200 body
	// carries refusals: the role is deliberately not all-or-nothing, so an id
	// it turns down is skipped and the survivors commit.
	//
	// Its problem vocabulary is therefore narrow rather than wide — everything
	// an ITEM can earn lives in that item's outcome, and a problem document
	// from this operation means the batch NEVER RAN.
	OpBatchCloseIssues = "batchCloseIssues"
	// OpClaimNextIssue takes ONE ready issue and hands it back claimed, behind
	// issueops.ReadyClaimer. It is the surface's first operation that names no
	// row at all: the caller sends a QUESTION — the ready listing's own filter
	// vocabulary — and the role picks the answer.
	//
	// It exists to retire a RACE rather than a round trip. The listing-then-claim
	// composition it replaces reads a row another agent claims before the second
	// request arrives, so a fleet polling one queue earns 409s for rows it was
	// correctly offered.
	OpClaimNextIssue = "claimNextIssue"
	// OpReleaseIssue gives a claim back — the claim's inverse, and what
	// `bd unclaim` spells. It is a named lifecycle action rather than a status
	// patch for OpCloseIssue's reason: an update spells the release three
	// fields at a time, which puts the transition's definition in the caller,
	// and the lease it drops is the part a patch cannot express at all.
	//
	// It is the one write on this surface that is NOT idempotent, and the
	// asymmetry with the claim is a fact about the two post-states rather than
	// a preference: a claim's post-state names the claimant, so a re-claim is
	// recognizable and can answer 200. A release leaves an anonymous row, so
	// "I released this twice", "a reaper beat me to it" and "nothing ever
	// claimed it" are one row — and one 200 for three situations that want
	// different things from a caller. It answers 409 instead and lets the
	// caller decide which of them it can live with.
	OpReleaseIssue = "releaseIssue"
	// OpCloseIssue is the second half of the agent loop this surface exists to
	// serve: claim, work, close. It is a named lifecycle action rather than a
	// status patch because Close carries semantics a patch has nowhere to put —
	// the reason and session under first-close-wins, the done-status
	// normalization, and the close policy vocabulary.
	OpCloseIssue = "closeIssue"
	// OpReopenIssue is the close's mirror, and it completes the lifecycle pair
	// so a recovery flow works end to end over this surface. It is the one write
	// here with no POLICY conflict code: reopen takes an issue OUT of the done
	// category, so there is no state of the graph that can refuse it. Its one
	// 409 is the caller's own compare-and-set, which is a fact about the
	// request's premise rather than about the graph.
	OpReopenIssue = "reopenIssue"
	// OpUpdateIssue edits the FIELDS of one issue, including the three that
	// carry policy: `status`, `assignee` and `parent_id`.
	//
	// It USED to exclude all three, and to have no 409 because of it. The
	// exclusion did not survive contact with a caller: an edit that moves a
	// status alongside other fields in one transaction is the thing two calls
	// cannot do, `issues:batchApply`'s update item has published all three since
	// it landed, and keeping them off here meant the two operations disagreed
	// about what patching one issue means. They are the SAME refusals here as
	// there — close policy, the assignee fence, the graph's two — and the named
	// lifecycle operations keep the semantics a status write has nowhere to put.
	OpUpdateIssue          = "updateIssue"
	OpListSettings         = "listSettings"
	OpGetSetting           = "getSetting"
	OpListDependencyCycles = "listDependencyCycles"
	// OpSetSetting stores one setting, replacing whatever was there. It is the
	// surface's first PUT, and the method IS the argument: the caller names the
	// resource by path and sends the value that becomes its whole state.
	// rememberMemory posts to a COLLECTION because its key may be derived from
	// the content; here the caller can always name what it is writing.
	OpSetSetting = "setSetting"
	// OpUnsetSetting removes one setting. It is the second DELETE on this
	// surface and the one that does NOT 404 on a key nothing stored: this role
	// reports no affected-row count, so the operation states an intended end
	// state rather than an act performed. See its operationCodes row.
	OpUnsetSetting = "unsetSetting"
	// OpListDependencies reads STORED EDGE ROWS for several issues at once.
	// It is a separate operation from getIssue's embedded `dependencies`
	// member because it answers per named issue, reports the ids that named
	// nothing, and returns edges whose target this database holds no row for.
	OpListDependencies = "listDependencies"
	// OpCountDependencyEdges sizes each anchor's edge set in ONE named
	// direction, behind issueops.GraphCounter. It is NOT listDependencies
	// counted: that operation is outgoing-only and takes no direction, this one
	// REQUIRES one and answers about either end, so the two agree on a number
	// only at direction=out. It is also not a third Counter method — that role
	// answers about a set of ISSUES described by a predicate, and this one about
	// EDGES anchored on ids, per anchor.
	OpCountDependencyEdges = "countDependencyEdges"
	// OpListRelatedIssues reads ONE issue's neighbors in a named direction,
	// behind issueops.Relations. It is NOT listDependencies narrowed to one
	// anchor: that operation answers the stored edge ROWS with their targets
	// spelled as stored, and this one answers the ISSUES on the far end — so an
	// edge whose target this database holds no row for is a row there and no
	// neighbor here, and the two answer different arities of question.
	//
	// It is a SUB-RESOURCE OF THE ISSUE rather than a member of the dependency
	// collection, and the argument is ELEMENT IDENTITY rather than a claim about
	// what that collection answers with — getDependencyTree answers hydrated
	// TreeNodes, so "everything under /dependencies is about edges" would be
	// false. What decides it is narrower and checkable: the rows here are the
	// SAME pinned struct getIssue already carries under `dependencies` and
	// `dependents`, so this operation is that pair, standalone,
	// direction-parameterized and type-filterable — and it belongs on the
	// resource whose members it publishes.
	OpListRelatedIssues = "listRelatedIssues"
	// OpListBlockingAnnotations reads the DERIVED blocking decoration for
	// several issues at once — open blockers, issues blocked, and the parent.
	// It is separate from listDependencies because it answers a summary over
	// two edge types with a status rule applied, where that one returns the
	// stored rows and applies nothing.
	OpListBlockingAnnotations = "listBlockingAnnotations"
	// OpGetDependencyTree walks the dependency graph from ONE root. It is
	// separate from listDependencies because that one answers raw edge rows for
	// many anchors at one hop, and this one recurses from a single anchor with a
	// depth, a cycle policy and a node shape of its own.
	OpGetDependencyTree = "getDependencyTree"
	OpCountReadyWork    = "countReadyWork"
	OpQueryIssues       = "queryIssues"
	// OpCountIssues sizes a set the ISSUE listing describes, behind
	// issueops.Counter. It is a sibling of countReadyWork rather than a mode of
	// it: that one sizes the READY predicate, which is dependency-aware and not
	// expressible as a filter over one table, and this one sizes a predicate.
	//
	// It is also NOT `listIssues` with the page taken off, which is the mistake
	// its own document spends a section on: a listing hides closed, pinned,
	// template and gate rows and a count hides none of them, so the two answer
	// about different sets for the same parameters. That difference is the
	// ROLE's and is the reason Counter is not a counted variant of Reader.
	//
	// One operation carries both of the role's methods. `group_by` selects the
	// bucketed shape, and the grouped response is the scalar response plus one
	// member — the same schema, not a second contract wearing one id.
	OpCountIssues = "countIssues"
	// OpRemoveDependency is the first WRITE to the dependency graph on this
	// surface, behind issueops.DependencyEditor. It names one edge by both its
	// endpoints, because an edge has two and neither alone identifies it.
	OpRemoveDependency = "removeDependency"
	// OpAddDependencies is the graph's other write: a BATCH of edges asserted
	// as one transaction, or none of them. It is the operation that owns both
	// new conflict codes, because both are statements about the graph a
	// requested edge set would produce.
	OpAddDependencies = "addDependencies"
	// OpSweepIssues is one of the two DESTRUCTIVE operations on this surface:
	// bulk clearance of closed beads from one tier, behind issueops.Sweeper.
	OpSweepIssues = "sweepIssues"
	// OpDeleteIssues is the other DESTRUCTIVE operation: erasure of beads the
	// request NAMES, behind issueops.Deleter. It is the one operation here whose
	// refusals include a question about the GRAPH — a named bead with a
	// dependent the request did not name.
	OpDeleteIssues = "deleteIssues"
	// OpCreateIssue creates ONE issue, with its parent, its explicit edges and
	// its waits-for gate, as one transaction. It is the plain collection POST
	// batchCreateIssues left free by spelling itself as a custom method.
	//
	// It publishes the whole create vocabulary rather than that operation's
	// narrow item — `status`, `sender`, `metadata`, `ephemeral`, `no_history`
	// and an explicit `id` included — which is what makes it usable for a
	// caller composing a real row, and which is also where its two conflict
	// codes come from: an occupied id, and the graph refusing the edges the
	// request asked for.
	OpCreateIssue = "createIssue"
	// OpAddComment appends one comment to the thread an issue owns, behind
	// issueops.Commenter. It is the surface's first write on a SUB-RESOURCE
	// COLLECTION, and a plain collection POST for OpCreateIssue's reason:
	// creating one member of the collection a path names is what POST means.
	//
	// The row it creates is the same pinned Comment getIssue already carries
	// under `comments`, which is what puts the operation on the issue rather
	// than on a collection of its own. The collection publishes no GET,
	// deliberately: no role answers a comment PAGE, and inventing one here
	// would be this surface deciding a paging contract the role declined.
	OpAddComment = "addComment"
	// OpBatchCreateIssues creates many issues as one transaction, or none.
	OpBatchCreateIssues = "batchCreateIssues"
	// OpApplyBatch applies an ORDERED, heterogeneous plan — creates, updates,
	// closes and dependency edges together — as one transaction, or none of it.
	// It is the only operation here whose request expresses a graph that
	// references its own items: a create may NAME itself and later items address
	// it by that name.
	//
	// It is therefore the widest refusal vocabulary on this surface, and every
	// entry is inherited rather than invented: it can earn the lifecycle's
	// close-policy conflict, the claim's assignee fence and both of the graph's
	// conflicts, because it performs all three families of write.
	OpApplyBatch = "applyBatch"
	// OpRememberMemory stores one memory, behind memoryops.Memories. It is the
	// first operation on this surface that reaches a role outside issueops: the
	// memory plane is user data riding in the config table, not settings, and
	// the two have different merge semantics and a different miss contract.
	OpRememberMemory = "rememberMemory"
	// OpGetMemory reads one memory by key. It is the ONE operation on this
	// surface that answers a miss with a 404 where its settings counterpart
	// deliberately does not: see its operationCodes row.
	OpGetMemory = "getMemory"
	// OpForgetMemory is the THIRD destructive operation on this surface, and the
	// only one that is a DELETE method: it names one memory by path, carries no
	// body and takes no flags, which is what that method already means. The two
	// destructive issue operations are collection-level custom methods because
	// they act on a set the request describes.
	OpForgetMemory = "forgetMemory"
	// OpListMemories enumerates the memory plane, narrowed by one search term.
	// It is the operation that makes stored memories DISCOVERABLE rather than
	// merely readable by a caller who already knows a key.
	OpListMemories = "listMemories"
	// OpListEvents pages the durable mutation journal from a caller-held
	// checkpoint. It is the first operation on this surface whose paging is not
	// a cursor this server minted: `since` is a sequence number the journal
	// itself assigned, so a consumer's position survives a restart on either
	// side and is meaningful to `bd events tail` as well.
	//
	// It is a READ of a log, not a subscription: the paged form has no follow
	// mode and never will. The retention contract is what makes that safe to
	// publish — a consumer that falls too far behind is told so with
	// `events_journal_truncated` rather than served a silently shortened history.
	OpListEvents = "listEvents"
	// OpWatchEvents pushes the same journal over a held-open text/event-stream
	// response, resuming from the same checkpoint. It is a sibling of the paged
	// read rather than a mode of it: the contracts differ in media type,
	// lifetime, limits and capacity, and one operation carrying both would have
	// documented two of everything under one operationId.
	//
	// It is the surface's ONLY streaming operation, and the only one whose
	// response can report a failure after its status is written — a prune that
	// races an open stream arrives as a named event rather than as the 410 the
	// same condition earns at connect.
	OpWatchEvents = "watchEvents"
	// OpCompareAndSetMetadata conditionally sets one metadata key on an issue.
	// It is the only WRITE on this surface whose ordinary refusal is a 200: a
	// lost race is the answer to the question the caller asked, and the value
	// that refused the swap is what its retry needs next.
	OpCompareAndSetMetadata = "compareAndSetMetadata"
)

// specBearerScheme is the name of the document's securityScheme for the bearer
// token. It is a constant so TestSpecSecurityMatchesRouteTable compares the
// route table's exemption column against the same string the document uses.
const specBearerScheme = "bearerToken"

// operationCodes is the per-operation problem vocabulary: exactly the codes
// that operation's handler can produce, and therefore exactly what the spec
// documents for it. TestSpecStatusCodesMatchHandlerTable asserts set-equality
// in both directions, so an undocumented emission and an unemittable
// documented status both fail CI.
//
// Two 400 invalid_argument paths are reachable on every route including
// /healthz — the Host-header middleware, and the unknown-query-parameter
// refusal every decoder performs — and both are deliberately absent from every
// row here. They are uniform rules, not per-operation behavior, and the spec
// documents them once at the document level rather than repeating them on
// every operation; these rows carry what an operation produces beyond them.
// Keep the two documents in step: a row here and the document-level prose are
// the only two places that carve-out exists.
//
// The 401 is NOT one of those uniform rules and does appear in the rows below.
// It is per-operation surface, and the policy is every operation except
// liveness: auth is enforced in route() for every row whose authExempt column
// is false, and OpHealth is the only row that sets it, so a probe can answer
// with no credential. Stating it per-operation rather than once at the document
// level is what makes TestSpecStatusCodesMatchHandlerTable require the 401s to
// be documented in the same change as the check that emits them — including for
// an operation added later, which inherits enforcement the moment it is routed
// and so must carry CodeUnauthenticated here from its first commit.
var operationCodes = map[string][]Code{
	// Liveness answers from the process, touches nothing that can fail, and
	// carries no credential: it is the one auth-exempt row of the route table.
	OpHealth: nil,
	// v0 serves a startup snapshot without touching the database, so there is
	// no 503 here. If a later slice makes this a DB-probing readiness
	// endpoint, db_unavailable joins this row and the spec in the same change.
	OpGetContext:    {CodeUnauthenticated, CodeInternal},
	OpListReadyWork: {CodeInvalidArgument, CodeUnauthenticated, CodeBusy, CodeDBUnavailable, CodeInternal},
	OpListIssues:    {CodeInvalidArgument, CodeInvalidCursor, CodeUnauthenticated, CodeBusy, CodeDBUnavailable, CodeInternal},
	// The 400 is this operation's own, the getStats precedent: a malformed
	// `include_comments` or `include_dependents` is a bad value on a parameter
	// this server knows, not the document-level unknown-key rule this table
	// omits.
	OpGetIssue: {CodeInvalidArgument, CodeUnauthenticated, CodeNotFound, CodeBusy, CodeDBUnavailable, CodeInternal},
	// No 400 of its own: the operation takes no parameters, so the only
	// invalid_argument it can raise is the document-level unknown-query-key
	// rule this table deliberately omits.
	OpListSettings: {CodeUnauthenticated, CodeBusy, CodeDBUnavailable, CodeInternal},
	// No 404: a key nothing stored and a key stored as the empty string are one
	// answer on this surface, so the only refusal a key can earn is the 400
	// that says it was not a key.
	OpGetSetting: {CodeInvalidArgument, CodeUnauthenticated, CodeBusy, CodeDBUnavailable, CodeInternal},
	// getSetting's row PLUS the ROLE's refusals, which is the whole difference
	// between the read half and the write half. Two of the role's three are
	// reachable — `issue_prefix` in either spelling, and a `status.custom` that
	// does not parse — and both arrive as the 400 they are, on the sentinel,
	// through the shared ErrValidation line every role-backed handler here draws.
	//
	// NO 404 and no conflict code, both inherited from the read beside it. A key
	// nothing stored and a key stored empty are one answer on this plane, so
	// there is no resource this write can fail to address; and the write is an
	// unconditional replace, so there is no state for it to lose a race against.
	// A `revision` guard would need a row version this plane does not hold.
	OpSetSetting: {CodeInvalidArgument, CodeUnauthenticated, CodeBusy, CodeDBUnavailable, CodeInternal},
	// getSetting's row EXACTLY, and that is this operation's whole error story:
	// it takes the same parameter, judges it the same way, and reaches a role
	// whose only refusal — an empty key — the path bound has already made
	// unreachable. Its 400 is therefore entirely the transport's.
	//
	// THE ABSENT 404 IS THE DIVERGENCE FROM forgetMemory, which addresses the
	// same shape of resource with the same method and answers 404 for a key it
	// held nothing under. That role reports Found; this one cannot — the storage
	// seam discards the affected-row count on all three legs — so a 404 here
	// would publish a distinction this server would have to invent.
	OpUnsetSetting: {CodeInvalidArgument, CodeUnauthenticated, CodeBusy, CodeDBUnavailable, CodeInternal},
	// The 400 here is this operation's own, not the document-level
	// unknown-parameter rule: a malformed `skip_blocked`, and the EMPTY
	// `assignee` the document refuses rather than answering with the rows that
	// have no assignee.
	OpGetStats: {CodeInvalidArgument, CodeUnauthenticated, CodeBusy, CodeDBUnavailable, CodeInternal},
	// The cycle sweep takes no parameters at all, so it has no 400 of its own:
	// the two uniform ones above are the whole of its invalid-argument story.
	OpListDependencyCycles: {CodeUnauthenticated, CodeBusy, CodeDBUnavailable, CodeInternal},
	// No not_found here, deliberately: an id that names nothing is reported in
	// the response's `missing` member, so a batch keeps the answers for the ids
	// that were found. A 404 would discard them.
	OpListDependencies: {CodeInvalidArgument, CodeUnauthenticated, CodeBusy, CodeDBUnavailable, CodeInternal},
	// The stored-edge read's vocabulary exactly, and no not_found for its
	// reason: an id that names nothing is reported on its own anchor, so a
	// batch keeps the answers for the ids that were found. The role has no
	// ErrNotFound at all, which its doc states.
	//
	// Its 400 is BOTH the transport's and the ROLE's, which is what separates
	// this row from GET /v0/beads/issues:count beside it. That operation
	// refuses its one enum at the edge and reaches no role refusal; here
	// ValidateEdgeCountRequest runs inside the single shared body — the role
	// has one body on all three legs, so the check could not belong to an
	// accessor — and four of its refusals are reachable over the wire: a
	// missing or unrecognized direction, a status beside direction=out, an
	// empty id, and a dependency type no edge could carry. Each reaches the
	// client as the 400 it is, on the sentinel, with the parameter named in the
	// validator's own order.
	OpCountDependencyEdges: {CodeInvalidArgument, CodeUnauthenticated, CodeBusy, CodeDBUnavailable, CodeInternal},
	// The same vocabulary as the stored-edge read beside it, and no not_found
	// for a stronger version of the same reason: this operation probes no id's
	// existence at all, so there is nothing it could 404 on.
	OpListBlockingAnnotations: {CodeInvalidArgument, CodeUnauthenticated, CodeBusy, CodeDBUnavailable, CodeInternal},
	// getDependencyTree's row exactly, and for its reasons: ONE anchor, so a
	// miss is the 404 it is rather than a per-anchor flag — there is no other
	// answer to preserve by reporting it in the body, and an empty neighbor
	// list is the common case, so a typo answered with one would never surface.
	//
	// Its 400 is BOTH the transport's and the ROLE's. The transport owns the
	// unknown key and the repeated single-valued parameter; ValidateRelatedRequest
	// owns the two that are about this request's MEANING — a missing or
	// unrecognized direction, and a dependency type no edge could carry — and each
	// reaches the client as the 400 it is, on the sentinel, with the parameter
	// named. The validator's third refusal, an empty anchor id, is unreachable
	// here: the id is a PATH segment this handler bounds before the role is
	// asked, and an id that fails that bound is the 404 a real miss gets.
	OpListRelatedIssues: {CodeInvalidArgument, CodeUnauthenticated, CodeNotFound, CodeBusy, CodeDBUnavailable, CodeInternal},
	// The 404 is the difference from the row above: this operation has ONE
	// anchor, so there is no other answer to preserve by reporting the miss in
	// the body. Its 400 is its own — an empty root, a direction outside the
	// closed set, a non-positive max_depth — all three the ROLE's ErrValidation
	// reaching the wire.
	OpGetDependencyTree: {CodeInvalidArgument, CodeUnauthenticated, CodeNotFound, CodeBusy, CodeDBUnavailable, CodeInternal},
	// The same vocabulary as the listing it sizes: it takes the same filters
	// and can refuse them the same way. limit=0's mode-dependent refusal has no
	// analog here because there is no limit to pass.
	OpCountReadyWork: {CodeInvalidArgument, CodeUnauthenticated, CodeBusy, CodeDBUnavailable, CodeInternal},
	// The ready count's vocabulary exactly, and for the same reasons: a
	// cardinality has no page, so there is no cursor to invalidate and no
	// unlimited-read refusal to make; and no 404, because a predicate matching
	// nothing is 0 — the role has no ErrNotFound at all, which its own doc
	// states, since a question about a set has an answer even when the set is
	// empty.
	//
	// Its 400 is ENTIRELY THE TRANSPORT'S, which is the one way this row differs
	// from the listings' beside it: a malformed boolean, integer or timestamp, a
	// repeated single-valued parameter, and a `group_by` outside the closed set.
	//
	// No ROLE refusal is reachable. issueops.Counter has exactly one
	// ErrValidation — ValidateCountGroup's unknown dimension, since
	// BuildCountFilter cannot fail — and countGroupOf refuses that dimension at
	// the edge, so the shared read failure path never classifies a count. An
	// unrecognized status or type is not a refusal at all here; the role
	// promises it matches nothing and answers 0.
	// TestCountGroupEnumMatchesTheRolesVocabulary is what keeps that true.
	OpCountIssues: {CodeInvalidArgument, CodeUnauthenticated, CodeBusy, CodeDBUnavailable, CodeInternal},
	// The listing's vocabulary minus the cursor: this operation has none, so
	// invalid_cursor cannot arise. An unparseable EXPRESSION is an
	// invalid_argument on `q` rather than a code of its own — a client's
	// recovery is the same as for any other malformed parameter value.
	OpQueryIssues: {CodeInvalidArgument, CodeUnauthenticated, CodeBusy, CodeDBUnavailable, CodeInternal},
	// The 400 here is the widest on this surface, and most of it is not this
	// handler's: the unfiltered durable sweep, the unrecognized tier and the
	// malformed glob are all the ROLE's ErrValidation reaching the wire through
	// failSweepErr. No 404 — this operation names no id — and no 409: a bead
	// another sweep already took is simply not in the set.
	OpSweepIssues: {CodeInvalidArgument, CodeUnauthenticated, CodeBusy, CodeDBUnavailable, CodeInternal},
	// CodeNotFound is the one this operation has and the sweep does not: a
	// sweep describes a set that can legitimately be empty, while a delete
	// names beads and an id that resolves to nothing is a caller mistake.
	//
	// precondition_failed is `expected_version`'s, and it is the 409 form for
	// the reason that code documents: a miss refuses the whole request and
	// leaves nothing to report but the refusal. It ranks BELOW the 404 — a
	// request that named no row has nothing to be stale about — and ABOVE the
	// dependents refusal, which is the role's own order and is what makes the
	// wire's answer the same one `bd delete` gives.
	//
	// The ARITY refusal is a 400 rather than a second conflict: a token beside
	// two distinct ids is a malformed request, not a statement about state.
	OpDeleteIssues: {
		CodeInvalidArgument, CodeUnauthenticated, CodeNotFound, CodePreconditionFailed,
		CodeBusy, CodeDBUnavailable, CodeInternal,
	},
	OpClaimIssue: {
		CodeInvalidArgument, CodeUnauthenticated, CodeNotFound, CodeAlreadyClaimed, CodeNotClaimable,
		CodeBusy, CodeDBUnavailable, CodeInternal,
	},
	// The NARROWEST write vocabulary on this surface, and the narrowness is the
	// contract rather than an oversight. A problem document from this operation
	// means the batch never ran; every refusal an ITEM can earn — not_found for
	// an id naming no row, not_closable for close policy — travels in that
	// item's outcome inside a 200. A 404 here would say the operation went to
	// the wrong place, and a 409 would say the whole batch was refused, and
	// neither is ever true of a per-item refusal.
	OpBatchCloseIssues: {CodeInvalidArgument, CodeUnauthenticated, CodeBusy, CodeDBUnavailable, CodeInternal},
	// NO 409 AND NO 404, and both absences are this operation's contract rather
	// than an omission. There is no id to have missed, and a row a racing agent
	// took is simply not in the set this claim scanned — the role walks past it
	// inside the transaction, which is the whole reason the operation exists.
	// An empty ready front is a 200 with the row absent, not a refusal.
	//
	// The 400 is the ready listing's filter vocabulary plus this operation's own
	// `limit` refusal plus the body rules, and the ROLE's ErrValidation behind
	// them, which is defensively unreachable.
	OpClaimNextIssue: {CodeInvalidArgument, CodeUnauthenticated, CodeBusy, CodeDBUnavailable, CodeInternal},
	// THREE conflict codes, and only one of them is new. `already_claimed` is
	// the ownership fence, inherited from updateIssue's assignee arm: the same
	// situation — a live foreign owner refusing a write — with the same two
	// bypasses, spelled `force` and `expected_assignee` here. `precondition_failed`
	// is the `expected_assignee` guard, inherited from the same operation and
	// carrying the same members for the same reason: the request's expectation,
	// never an observation.
	//
	// `not_releasable` is the mint, and its own doc carries the analysis.
	//
	// The 404 is the path id's, on the terms updateIssue states. There is no
	// `not_claimable` here even though the claim's status refusal is the nearest
	// neighbor — see CodeNotReleasable for why that reuse was refused.
	OpReleaseIssue: {
		CodeInvalidArgument, CodeUnauthenticated, CodeNotFound,
		CodeAlreadyClaimed, CodeNotReleasable, CodePreconditionFailed,
		CodeBusy, CodeDBUnavailable, CodeInternal,
	},
	// TWO 409s, and they answer different questions with `code` as the only
	// discriminator a client needs.
	//
	// not_closable is close POLICY: an unforced close refused for open children
	// or for a live blocker. There is no already_claimed here — closing work
	// somebody else holds is not a refusal on this surface — and the idempotent
	// re-close is a 200 carrying `already_closed`, the claim's answer to the
	// same question.
	//
	// precondition_failed is `expected_version`'s, in the 409 form for the
	// reason that code documents, and it is CHECKED FIRST — before policy and
	// before the idempotent re-close, which is the role's own order
	// (issueops.Lifecycle.Close). `force` bypasses policy and never it: the two
	// members make unrelated claims.
	OpCloseIssue: {
		CodeInvalidArgument, CodeUnauthenticated, CodeNotFound, CodeNotClosable, CodePreconditionFailed,
		CodeBusy, CodeDBUnavailable, CodeInternal,
	},
	// ONE 409, and it is a PRECONDITION rather than a policy. Close has a policy
	// guard — open children, a live blocker — and reopen is the direction that
	// takes an issue OUT of the done category rather than putting it in, so
	// there is no state of the graph that can refuse it: not_closable is absent
	// here and always will be. What `expected_version` refuses is the request's
	// own premise, which every write can have wrong. The idempotent case is
	// still a 200 carrying `already_open`, unless a guard came with it.
	OpReopenIssue: {
		CodeInvalidArgument, CodeUnauthenticated, CodeNotFound, CodePreconditionFailed,
		CodeBusy, CodeDBUnavailable, CodeInternal,
	},
	// FOUR conflict codes, and the three members that publish them are exactly
	// the ones this row used to say would drag them in: `status` brought close
	// policy, `assignee` the fence, `parent_id` the graph vocabulary. Publishing
	// the members is what made the codes reachable, and every one of them is
	// inherited from an operation that already has it — this is applyBatch's row
	// for a single update item, minus `already_exists`, which needs a create.
	//
	// precondition_failed is the guard trio's, and it is the 409 form rather
	// than compareAndSetMetadata's 200 for the reason that code documents: a
	// miss here refuses the whole write and leaves nothing to report but the
	// refusal, where a lost metadata swap is a retry loop's ordinary iteration
	// carrying the value it needs next.
	//
	// The 404 is still the PATH id only. A `patch.parent_id` that names nothing
	// is an edge endpoint and stays a 400, conforming to addDependencies.
	//
	// The 400 is the body vocabulary plus the ROLE's ErrValidation — a
	// workspace-vocabulary issue_type or status, a metadata key the query layer
	// could not spell, a field-length refusal that slipped the edge check —
	// through failUpdate.
	OpUpdateIssue: {
		CodeInvalidArgument, CodeUnauthenticated, CodeNotFound,
		CodePreconditionFailed, CodeNotClosable, CodeAlreadyClaimed,
		CodeDependencyCycle, CodeDependencyExists,
		CodeBusy, CodeDBUnavailable, CodeInternal,
	},
	// No not_found. The role refuses an edge whose target names nothing, and
	// that refusal is about the REQUEST BODY the client sent, not about a
	// resource this operation was asked to address — there is no id in the path
	// to have missed. A 404 here would tell a client its request went to the
	// wrong place.
	//
	// No conflict code either: this operation publishes no `id` member, so no
	// item can collide with a stored row and the role's ErrAlreadyExists is
	// unreachable from the wire.
	OpBatchCreateIssues: {CodeInvalidArgument, CodeUnauthenticated, CodeBusy, CodeDBUnavailable, CodeInternal},
	// The batch's row plus the two codes its narrower vocabulary cannot earn,
	// and both additions are members rather than judgement calls.
	//
	// already_exists arrives with `id`: that operation publishes none, so its
	// items can never collide with a stored row and the role's ErrAlreadyExists
	// is unreachable from its wire. Here it is reachable and it is a 409 for
	// CodeNotClosable's reason — the body is well-formed and STATE refuses it.
	//
	// dependency_cycle arrives with `parent_id` and `dependencies[].reverse`:
	// the first places the new row inside a hierarchy the caller cannot see, so
	// a blocking edge against its own ancestor is refusable, and the second
	// writes an edge INTO the id being minted, which is the only way a create
	// can close a scheduling cycle at all. It is addDependencies' 409 unchanged,
	// including the hierarchy discriminator.
	//
	// NO 404 and no dependency_exists. A target that names nothing is a
	// statement about the request body rather than a resource this operation was
	// asked to address — batchCreateIssues' argument, and there is no id in this
	// path to have missed. And the only type conflict a create can raise is
	// between two edges of the SAME request, which is a malformed body: no
	// stored edge can name a pair whose endpoint is an id this request is
	// minting.
	OpCreateIssue: {
		CodeInvalidArgument, CodeUnauthenticated, CodeAlreadyExists, CodeDependencyCycle,
		CodeBusy, CodeDBUnavailable, CodeInternal,
	},
	// The widest row here, and every code on it is inherited from an operation
	// that already has it: this one performs the lifecycle's writes, the claim's
	// assignee transfer and the graph's edge assertions inside one transaction,
	// so it can earn any refusal any of them can.
	//
	// THERE IS A CONFLICT CODE HERE, unlike the metadata compare-and-set, and
	// that is the whole difference between the two contracts rather than a
	// difference of taste. A lost compare-and-set is that operation's ORDINARY
	// path — a retry loop is its designed caller — so a 409 there would put the
	// normal iteration in the error channel and force the value the loop needs
	// next into a problem member. Here a precondition miss refuses every item in
	// the request, so there is no partial outcome to report on a 200 and nothing
	// for a client to do with one: the refusal IS the answer, and it belongs
	// where every other "the state says no" on this surface lives.
	//
	// The 404 is the delete's, not the batch create's: `update` and `close`
	// items NAME rows this request acts on, so a target that resolves to nothing
	// is a resource the request failed to address. An EDGE endpoint stays a 400,
	// conforming to addDependencies — nothing in that refusal is about a
	// resource this operation was asked to address.
	OpApplyBatch: {
		CodeInvalidArgument, CodeUnauthenticated, CodeNotFound,
		CodePreconditionFailed, CodeNotClosable, CodeAlreadyClaimed, CodeAlreadyExists,
		CodeDependencyCycle, CodeDependencyExists,
		CodeBusy, CodeDBUnavailable, CodeInternal,
	},
	// getDependencyTree's row, and it is the same shape for the same two
	// reasons: ONE anchor, so an id that names nothing is the 404 it is rather
	// than a per-item flag, and a 400 that is BOTH the transport's and the
	// ROLE's.
	//
	// NO CONFLICT CODE, and the absence is the operation's contract. A thread is
	// append-only and this write touches no field of the issue, so there is no
	// row state a guard could be stale about and no concurrent comment for this
	// one to collide with — which is also why there is no `expected_version`
	// member to earn a precondition_failed with.
	//
	// Of the role's three ErrValidation refusals exactly ONE is reachable here.
	// An empty author is refused at the edge under `actor`'s rules, which are
	// strictly stronger, and an empty issue id cannot arrive at all — a ServeMux
	// wildcard does not match an empty segment, and an id that fails the path
	// bound is the 404 a real miss gets. So the blank body is the whole of what
	// the role can refuse over this wire, which is why failAddComment names one
	// parameter rather than re-asking the validator's questions.
	OpAddComment: {CodeInvalidArgument, CodeUnauthenticated, CodeNotFound, CodeBusy, CodeDBUnavailable, CodeInternal},
	// No 404 and no conflict code: this is an UPSERT with a server-derivable
	// key, so there is no resource it can fail to address and no row it can
	// collide with. Its 400 is the body vocabulary plus the ROLE's two
	// refusals — empty content, and content no key can be derived from.
	OpRememberMemory: {CodeInvalidArgument, CodeUnauthenticated, CodeBusy, CodeDBUnavailable, CodeInternal},
	// THE 404 IS THE DIVERGENCE, and it is deliberate. OpGetSetting has none
	// because on that plane an absent key and a key stored empty are one answer
	// the CLI itself prints identically, so a 404 would publish an invented
	// distinction. Here the CLI already distinguishes a miss — `bd recall` has
	// an exit-code contract for it — and the role answers Found rather than a
	// value, so the 404 reports a distinction that exists. The stored-empty row
	// falls on the miss side of it, which the document states.
	OpGetMemory: {CodeInvalidArgument, CodeUnauthenticated, CodeNotFound, CodeBusy, CodeDBUnavailable, CodeInternal},
	// The read's vocabulary exactly, because the two operations address the same
	// resource the same way and this one's Found false is the same answer: a key
	// nothing stored is a 404 and nothing was removed. No 409 — a memory another
	// caller already forgot is simply not there, which is what the 404 says.
	OpForgetMemory: {CodeInvalidArgument, CodeUnauthenticated, CodeNotFound, CodeBusy, CodeDBUnavailable, CodeInternal},
	// The 400 here IS this operation's own, unlike listSettings' absent row: it
	// has one parameter, and a repeated `q` is refused rather than resolved to
	// one of its values. No 404 — a search matching nothing is an empty page,
	// because a question about a set has an answer even when the set is empty.
	OpListMemories: {CodeInvalidArgument, CodeUnauthenticated, CodeBusy, CodeDBUnavailable, CodeInternal},
	// The two journal codes are this operation's alone and neither has a
	// precedent elsewhere on this surface, because no other operation reads a
	// LOG. The 400 is its own too: `since` is required, and a negative or
	// unparseable checkpoint is refused rather than treated as zero — `seq > -5`
	// would quietly serve the whole journal as if it were a legitimate resume,
	// which is the same refusal `bd events tail --since` makes.
	//
	// No 404. A checkpoint at or past the head is a caught-up 200 with an empty
	// list, because "nothing new yet" is an answer about a log rather than a
	// missing resource, and a poller that got a 404 for being up to date would
	// have to treat the surface's miss vocabulary as a normal steady state.
	OpListEvents: {
		CodeInvalidArgument, CodeUnauthenticated, CodeEventsJournalDisabled, CodeEventsJournalTruncated,
		CodeBusy, CodeDBUnavailable, CodeInternal,
	},
	// THE PAGED READ'S VOCABULARY PLUS ONE, and the shared part is the point:
	// every connect-time refusal a poller can earn, a stream earns identically,
	// because the stream only opens once the same first read has succeeded. The
	// 410 in particular is a real 410 here and not an in-band event — that
	// mapping applies to a prune that races an ALREADY OPEN stream, where no
	// status is left to send.
	//
	// events_watch_saturated is the addition, and it is the only code on this
	// surface that describes a limit on connections rather than on data. Its 503
	// therefore documents three codes where every other operation's documents
	// two.
	OpWatchEvents: {
		CodeInvalidArgument, CodeUnauthenticated, CodeEventsJournalDisabled, CodeEventsJournalTruncated,
		CodeEventsWatchSaturated, CodeBusy, CodeDBUnavailable, CodeInternal,
	},
	// NO 404, for a stronger version of the batch create's reason: an edge that
	// is not there is `removed: false`, and an endpoint id that names nothing
	// holds no edge either, so this operation probes no id's existence and has
	// nothing it could report a miss on — listBlockingAnnotations' argument,
	// applied to a write. No conflict code either: the removal is idempotent, so
	// another caller having got there first is a success rather than a collision.
	// NO CONFLICT CODE, and the absence is this operation's whole posture. A
	// lost compare-and-set is a 200 carrying `swapped: false` and the current
	// value, because a retry loop is the DESIGNED caller and a 409 would put its
	// ordinary path in the error channel — and would have to smuggle the value
	// that loop needs next into a problem extension member. The 404 is here for
	// the id, which is the one refusal a caller cannot converge on.
	OpCompareAndSetMetadata: {
		CodeInvalidArgument, CodeUnauthenticated, CodeNotFound,
		CodeBusy, CodeDBUnavailable, CodeInternal,
	},
	OpRemoveDependency: {CodeInvalidArgument, CodeUnauthenticated, CodeBusy, CodeDBUnavailable, CodeInternal},
	// The two conflict codes are this operation's alone, and both say the same
	// kind of thing: the request is fine as a request and the GRAPH refuses it,
	// which the caller cannot know without reading state it does not have. That
	// is the claim's not_claimable situation and it gets the claim's answer, a
	// typed 409 whose extension members are read inside the refusing
	// transaction. The delete's 400-for-a-graph-refusal precedent does not
	// apply: that one is about request COMPLETENESS — send cascade or force —
	// and neither of these has a force to send.
	//
	// No 404: an endpoint that names nothing is a refusal of the request BODY,
	// so it joins the 400 with every other body refusal (batchCreateIssues'
	// argument). Nothing was written in any of these cases.
	OpAddDependencies: {
		CodeInvalidArgument, CodeUnauthenticated, CodeDependencyCycle, CodeDependencyExists,
		CodeBusy, CodeDBUnavailable, CodeInternal,
	},
}

// Result is a problem response ready to be written: the envelope plus the
// transport-level Retry-After the code implies.
type Result struct {
	Problem apigen.Problem
	// RetryAfterSeconds is written as the Retry-After header when positive.
	RetryAfterSeconds int
}

// WithAssignee attaches the `assignee` extension member (the current holder of
// a claimed issue). Populate it from a read in the claim's own transaction —
// never by parsing the sentinel's message fragments.
func (r Result) WithAssignee(assignee string) Result {
	r.Problem.Assignee = &assignee
	return r
}

// WithIssueStatus attaches the `issue_status` extension member (the issue's
// status at the moment of refusal). Same rule as WithAssignee.
func (r Result) WithIssueStatus(status string) Result {
	r.Problem.IssueStatus = &status
	return r
}

// WithOpenChildren attaches the `open_children` extension member (how many open
// children the transaction that refused a close observed). Same rule as
// WithAssignee: it comes from the typed error's own field, read inside that
// transaction, never from parsing the refusal's prose.
//
// Its PRESENCE is load-bearing. It is attached for the open-children refusal
// and withheld for the live-blocker one, which is how a client tells the two
// apart without reading `detail`.
func (r Result) WithOpenChildren(n int) Result {
	r.Problem.OpenChildren = &n
	return r
}

// WithDependencyTypeConflict attaches the two `dependency_exists` extension
// members: the type the pair already carries and the type the request asked
// for. Populate them from *issueops.DependencyTypeConflictError's fields — the
// typed error the role raises inside the transaction that saw the stored edge —
// never by parsing its message.
func (r Result) WithDependencyTypeConflict(existing, requested string) Result {
	r.Problem.ExistingType = &existing
	r.Problem.RequestedType = &requested
	return r
}

// WithHierarchyConflict attaches the three extension members that distinguish
// the HIERARCHY refusal from a plain scheduling cycle inside the one
// `dependency_cycle` code. Their PRESENCE is the discriminator, and the three
// together are enough to rebuild
// *issueops.DependencyHierarchyConflictError whole — BlockerIsAncestor in both
// polarities included, which is why the boolean travels through a pointer and
// is emitted when false.
//
// They can only come from the refusing transaction, for the reason
// ClaimConflictError's do and more so: the conflicting hierarchy may exist only
// inside the batch that was rolled back, so no read after the fact can recover
// it.
func (r Result) WithHierarchyConflict(issueID, blockerID string, blockerIsAncestor bool) Result {
	r.Problem.IssueId = &issueID
	r.Problem.BlockerId = &blockerID
	r.Problem.BlockerIsAncestor = &blockerIsAncestor
	return r
}

// WithBatchItem attaches the four extension members that name WHICH item of a
// batch earned a refusal: its index, its kind, the key it gave itself or the
// key its target ref named, and the id it had resolved when the refusal
// happened.
//
// They come from *issueops.ItemError's own fields — the typed error the role
// raises rather than the prose it formats — for the reason every other typed
// member on this envelope does, and one more that is this operation's alone:
// the request is all or nothing, so there is no per-item result array for a
// client to find the offender in. These members are the only place it exists.
//
// `item_key` and `item_issue_id` are OMITTED WHEN EMPTY, and both absences mean
// something. An item that named nothing symbolically has no key; an item
// refused before its target resolved — a create whose id was never minted, a
// ref that resolved to nothing — has no id.
//
// It is `item_issue_id` rather than `issue_id` deliberately: `issue_id` is
// already a PRESENCE-DISCRIMINATING member of the `dependency_cycle` hierarchy
// refusal, and reusing it would make that discriminator fire on a refusal it
// says nothing about.
func (r Result) WithBatchItem(index int, kind, key, issueID string) Result {
	r.Problem.ItemIndex = &index
	r.Problem.ItemKind = &kind
	if key != "" {
		r.Problem.ItemKey = &key
	}
	if issueID != "" {
		r.Problem.ItemIssueId = &issueID
	}
	return r
}

// WithDeclaredLater attaches the `declared_later` member, which tells an
// unresolvable key that IS declared by a later item from one nothing in the
// request declares at all. The first is an ordering mistake and the second is a
// typo, and a client fixes them differently.
//
// It is emitted in BOTH polarities and never omitted to mean false, for
// WithHierarchyConflict's reason applied to a 400: an absent member on this
// operation's 400s means "this refusal was not about a key" and must not be
// readable as "the key was not declared later".
func (r Result) WithDeclaredLater(declaredLater bool) Result {
	r.Problem.DeclaredLater = &declaredLater
	return r
}

// WithExpectedVersion attaches the `expected_version` member of a
// `precondition_failed`: the row version the request guarded on.
//
// It is the REQUEST's value rather than a read, which is why there is no
// `actual_version` beside it here — see PreconditionFailed.
func (r Result) WithExpectedVersion(expected int64) Result {
	r.Problem.ExpectedVersion = &expected
	return r
}

// WithExpectedStatus attaches the `expected_status` member of a
// `precondition_failed`. Same source and same rule as WithExpectedVersion.
func (r Result) WithExpectedStatus(expected string) Result {
	r.Problem.ExpectedStatus = &expected
	return r
}

// WithExpectedAssignee attaches the `expected_assignee` member of a
// `precondition_failed`. Same source and same rule as WithExpectedVersion.
func (r Result) WithExpectedAssignee(expected string) Result {
	r.Problem.ExpectedAssignee = &expected
	return r
}

// PreconditionFailed builds the 409 for a compare-and-set guard that missed on
// an operation where a miss refuses the whole request.
//
// The detail says what a client does next rather than what the row held,
// because on this contract those are different facts: the transaction that saw
// the mismatch rolled back, so a value read afterwards describes a row the
// refusal never saw. The role's refusals carry the expectation and not the
// observation, so `actual_version`, `actual_status` and `actual_assignee` stay
// absent here — the envelope declares them for an operation whose role can
// report what it found, and inventing one from a later read would be worse than
// omitting it.
func PreconditionFailed() Result {
	return newResult(CodePreconditionFailed,
		"a precondition guard did not match; nothing was written, so re-read the row and recompose the request rather than retrying it")
}

// WithJournalWindow attaches the three `events_journal_truncated` extension
// members: the checkpoint the reported window begins after, the lowest seq
// still retained, and the highest seq ever assigned.
//
// They come from *storage.EventsJournalTruncatedError's own fields — computed
// inside the transaction that saw the gap — for the reason every other typed
// member on this envelope does, and one more: `since` is NOT always the value
// the caller sent. On an interior hole it is the last seq the server could
// serve contiguously from the caller's checkpoint, which is strictly the more
// useful number and cannot be recovered by a client that assumed it was
// echoing its own input back.
//
// All three are emitted together and none is omitted to mean zero: a head of 0
// is a real journal state (nothing has ever been written), and a client
// computing `floor - 1` needs the value rather than an absence to interpret.
func (r Result) WithJournalWindow(since, floor, head int64) Result {
	r.Problem.Since = &since
	r.Problem.Floor = &floor
	r.Problem.Head = &head
	return r
}

// WithRequestID sets the `request_id` member, the correlation id echoed in the
// request log line. It is what makes a 5xx actionable: the body carries a fixed
// static detail by design, so the id is the client's only handle on the one log
// line that has the real error. The document requires it on every problem
// response, which is why this sets it unconditionally — an id this server
// failed to mint travels as an empty string rather than as a missing required
// member.
func (r Result) WithRequestID(id string) Result {
	r.Problem.RequestId = id
	return r
}

func newResult(code Code, detail string) Result {
	status := code.Status()
	if status == 0 {
		// Unreachable unless a code is added without a status; fail closed
		// rather than emitting a 0 status line.
		status = http.StatusInternalServerError
		code = CodeInternal
		detail = staticDetail[CodeInternal]
	}
	if static, ok := staticDetail[code]; ok {
		// 5xx detail is fixed per code, whatever the caller passed.
		detail = static
	}
	p := apigen.Problem{
		Status: status,
		Title:  http.StatusText(status),
		Code:   string(code),
	}
	if detail != "" {
		p.Detail = &detail
	}
	return Result{Problem: p}
}

// InvalidArgument builds the 400 for a request the server refuses to
// interpret. param names the offending query parameter, body member or header;
// pass "" only when the input has no nameable part (a body that fails to parse
// at all). detail may quote the caller's own input — it is not server state.
func InvalidArgument(param string, reason Reason, detail string) Result {
	res := newResult(CodeInvalidArgument, detail)
	if param != "" {
		res.Problem.Param = &param
	}
	r := string(reason)
	res.Problem.Reason = &r
	return res
}

// ProjectMismatch builds the 400 for a request whose Bd-Project-Id header names
// a workspace this server does not serve. got is the id the client stamped; own
// is this server's own project id, disclosed in the `server_project_id`
// extension member so a stamped client can tell a wrong-server refusal from a
// malformed one without parsing `detail`.
//
// This is the ONLY refusal on the surface that sets `server_project_id`, and it
// is raised only after the request has cleared the Host gate (and, in a
// deployment that adds one, its authentication layer): a request turned away by
// an earlier gate is answered before the stamp is ever compared, so it never
// discloses the server's identity. Presence of the member is therefore the
// signal that this specific check — and nothing earlier — fired.
func ProjectMismatch(got, own string) Result {
	res := InvalidArgument(ProjectIDHeader, ReasonProjectMismatch,
		"the "+ProjectIDHeader+" header names project "+strconv.Quote(got)+", which this server does not serve")
	res.Problem.ServerProjectId = &own
	return res
}

// InvalidCursor builds the 400 for a cursor this server did not issue, cannot
// decode, or issued under a different internal version.
func InvalidCursor() Result {
	return newResult(CodeInvalidCursor, "cursor is not valid for this server; restart paging without it")
}

// NotFound builds the 404 for an id this server cannot resolve. It is one
// function rather than a literal per site so that a handler which decides a
// miss WITHOUT reading storage — an id no row could hold — is indistinguishable
// on the wire from one that read and missed. A client that could tell them
// apart would be probing which ids are well-formed.
func NotFound() Result {
	return newResult(CodeNotFound, "no issue or wisp with that id")
}

// EventsJournalDisabled builds the 409 for a workspace whose journal is off.
//
// The detail names the setting rather than describing the state, because the
// recovery is entirely on the SERVER side — a client can do nothing about it —
// and the human reading this response is the operator who has to go turn it on.
func EventsJournalDisabled() Result {
	return newResult(CodeEventsJournalDisabled,
		"the durable events journal is not enabled on this workspace; set `events-journal true` and restart the server")
}

// EventsJournalTruncated builds the 410 for a checkpoint below the retained
// window, carrying the window the server can still serve.
//
// The detail is the storage error's OWN sentence, which is the one `bd events
// tail` prints. A consumer that reads both surfaces sees one description of one
// condition, and this is a 4xx, so reflecting the server's own state here is
// within what staticDetail allows.
func EventsJournalTruncated(err *storage.EventsJournalTruncatedError) Result {
	return newResult(CodeEventsJournalTruncated, err.Error()).
		WithJournalWindow(err.Since, err.Floor, err.Head)
}

// EventsWatchSaturated builds the 503 for a stream this server will not hold.
//
// The detail names the alternative rather than the limit, because unlike every
// other 503 here this one has a recovery that is not "wait": the paged read
// answers the same records from the same checkpoint and is not capped this way.
func EventsWatchSaturated() Result {
	res := newResult(CodeEventsWatchSaturated,
		"this server is already holding as many journal streams as it will; retry later, or read the same records from the same checkpoint with GET /v0/beads/events")
	res.RetryAfterSeconds = retryAfterWatchSaturation
	return res
}

// MemoryNotFound builds the 404 for a key this workspace holds no memory under.
//
// A separate constructor rather than a detail argument on NotFound, because the
// two say different things: that one is about the issue id space, and reusing
// its sentence here would tell a client its memory key was an issue id. The
// detail deliberately does NOT distinguish an absent row from one stored as the
// empty string — the role cannot see the difference, so the wire must not claim
// to.
func MemoryNotFound() Result {
	return newResult(CodeNotFound, "this workspace holds no memory under that key")
}

// ClassifyError maps an error from the storage seam onto the wire. The caller
// is responsible for logging err: everything mapped to a 5xx deliberately
// drops the error text on the floor (see staticDetail).
//
// ErrVersionMismatch — AND EVERY OTHER COMPARE-AND-SET SENTINEL — IS
// DELIBERATELY ABSENT FROM THIS FUNCTION, because the 409 it earns cannot be
// built from the error alone: `precondition_failed` echoes the value the
// REQUEST guarded on, and this function is handed an error and nothing else.
// So an operation that publishes `expected_version` (or `expected_status`, or
// `expected_assignee`) MUST match the sentinel TYPED in its own failure path,
// before anything reaches failErr — updateIssue, closeIssue, reopenIssue,
// deleteIssues, releaseIssue and applyBatch each do. A handler that forgets is
// not answering a worse 4xx: neither storage leg wraps these in ErrValidation,
// so the miss falls through the default arm below and every guard failure on
// that operation is a GENERIC 500. Mutation-verified on the single-issue
// handlers.
//
// THE RULE IS WRITTEN HERE BECAUSE SIX HANDLERS FOUND IT SEPARATELY. failUpdate
// worked it out, failRelease worked it out again and called it "failUpdate's
// hazard, in a sharper form", and the guard slice worked it out three more
// times. Six independent discoveries of one fact are a fact nobody recorded
// where the seventh author would look — which is here, since a handler author
// asking "does the shared mapping cover my sentinel?" reads this function and
// finds no row.
func ClassifyError(err error) Result {
	// The one row that carries data out of the error rather than only a code.
	// It lives HERE, in the shared mapping, rather than in the events handler:
	// the journal read is reached from two database sources through two
	// different plumbings, and a mapping a handler applied itself would be one
	// `if` away from a pruned-past checkpoint arriving as a generic 500 on
	// whichever arm forgot it.
	var truncated *storage.EventsJournalTruncatedError

	switch {
	case err == nil:
		return newResult(CodeInternal, "")

	case errors.As(err, &truncated):
		return EventsJournalTruncated(truncated)

	// Not-found normalization belongs in the shared read path, which folds the
	// two miss shapes (a wrapped sql.ErrNoRows, and a nil issue with a nil
	// error) into storage.ErrNotFound. The bare sql.ErrNoRows row below is
	// defense in depth for a path that has not been normalized yet: a missing
	// issue must never surface as a 500. The converse mistake — treating any
	// error as a miss — stays closed, because only these two sentinels reach
	// 404 and everything else falls through to 500.
	case errors.Is(err, storage.ErrNotFound), errors.Is(err, sql.ErrNoRows):
		return NotFound()

	case errors.Is(err, storage.ErrAlreadyClaimed):
		return newResult(CodeAlreadyClaimed, "issue is claimed by another actor")

	case errors.Is(err, storage.ErrNotClaimable):
		return newResult(CodeNotClaimable, "issue is not in a claimable state")

	// The two close-policy refusals share one code. They are the same statement
	// to a client — the close was refused for the state of the graph around
	// this issue, and `force` is the bypass for both — and what distinguishes
	// them on the wire is the `open_children` member failClose attaches, not a
	// second vocabulary entry.
	case errors.Is(err, issueops.ErrCloseOpenChildren):
		return newResult(CodeNotClosable, "issue has open children; close them first or close with force")

	case errors.Is(err, issueops.ErrCloseBlocked):
		return newResult(CodeNotClosable, "issue is blocked; clear the blocker or close with force")

	case errors.Is(err, ErrBusy):
		res := newResult(CodeBusy, "")
		res.RetryAfterSeconds = retryAfterSaturation
		return res

	// The retry budget is spent inside uow.RunTxResult; reaching here means it
	// gave up, so the request is retryable at the client's cadence, not the
	// server's.
	case uow.IsSerializationError(err):
		res := newResult(CodeBusy, "")
		res.RetryAfterSeconds = retryAfterContention
		return res

	case isUnavailable(err):
		res := newResult(CodeDBUnavailable, "")
		res.RetryAfterSeconds = retryAfterContention
		return res

	default:
		return newResult(CodeInternal, "")
	}
}

// isUnavailable reports whether err is a failure to reach the database at all
// — the server or proxy being down, idle-stopped, or dropping connections —
// as opposed to a failure while executing a statement.
//
// The list is empirical and safe to extend: it only chooses between two 5xx
// codes that carry identical static detail, so a miss costs a less useful
// `code`, never a disclosure.
func isUnavailable(err error) bool {
	// context.DeadlineExceeded satisfies net.Error, so it must be excluded
	// before the net.Error test below: a tripped per-request deadline is a
	// generic 500, not a claim that the database is unreachable.
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, driver.ErrBadConn) || errors.Is(err, mysql.ErrInvalidConn) {
		return true
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

// Write emits res as application/problem+json. Success bodies are written by
// their handlers; every non-2xx byte goes through here.
func Write(w http.ResponseWriter, res Result) {
	h := w.Header()
	h.Set("Content-Type", "application/problem+json; charset=utf-8")
	if res.RetryAfterSeconds > 0 {
		h.Set("Retry-After", strconv.Itoa(res.RetryAfterSeconds))
	}
	// RFC 9110 requires the challenge on a 401, and it is set here rather than
	// at the refusal so a second 401 site could not forget it — the same reason
	// Retry-After is set from the code above.
	if res.Problem.Code == string(CodeUnauthenticated) {
		h.Set("WWW-Authenticate", "Bearer")
	}
	w.WriteHeader(res.Problem.Status)
	_ = json.NewEncoder(w).Encode(res.Problem)
}
