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
	// CodeAlreadyClaimed carries the holder in the `assignee` extension
	// member, read inside the same transaction — never parsed out of the
	// sentinel's message text.
	CodeAlreadyClaimed Code = "already_claimed"
	CodeNotClaimable   Code = "not_claimable"
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
	CodeNotFound:         http.StatusNotFound,
	CodeAlreadyClaimed:   http.StatusConflict,
	CodeNotClaimable:     http.StatusConflict,
	CodeNotClosable:      http.StatusConflict,
	CodeDependencyCycle:  http.StatusConflict,
	CodeDependencyExists: http.StatusConflict,

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
)

// staticDetail is the ONLY `detail` a 5xx may carry. The underlying error goes
// to the server log and nowhere else: driver and dial errors routinely embed
// the DSN — go-sql-driver renders connection targets as
// user@tcp(127.0.0.1:PORT)/db, net dial errors carry the same host:port — and
// query errors can carry SQL fragments. The moment the server is bound with
// --allow-non-loopback, a verbose 5xx detail is an information-disclosure
// channel to network peers. 4xx details stay specific: they reflect the
// caller's own input back, not server internals.
var staticDetail = map[Code]string{
	CodeBusy:          "the server is busy; retry shortly",
	CodeDBUnavailable: "database temporarily unavailable; retry",
	CodeInternal:      "internal server error",
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
	// OpCloseIssue is the second half of the agent loop this surface exists to
	// serve: claim, work, close. It is a named lifecycle action rather than a
	// status patch because Close carries semantics a patch has nowhere to put —
	// the reason and session under first-close-wins, the done-status
	// normalization, and the close policy vocabulary.
	OpCloseIssue = "closeIssue"
	// OpReopenIssue is the close's mirror, and it completes the lifecycle pair
	// so a recovery flow works end to end over this surface. It is the one
	// write here with no conflict code: reopen has no policy guard.
	OpReopenIssue = "reopenIssue"
	// OpUpdateIssue edits the FIELDS of one issue. Lifecycle is deliberately not
	// among them: status belongs to close/reopen/claim, which carry the policy
	// and conflict vocabulary, and assignee belongs to the claim's
	// compare-and-set. Keeping both out is why this operation has no 409.
	OpUpdateIssue          = "updateIssue"
	OpListSettings         = "listSettings"
	OpGetSetting           = "getSetting"
	OpListDependencyCycles = "listDependencyCycles"
	// OpListDependencies reads STORED EDGE ROWS for several issues at once.
	// It is a separate operation from getIssue's embedded `dependencies`
	// member because it answers per named issue, reports the ids that named
	// nothing, and returns edges whose target this database holds no row for.
	OpListDependencies = "listDependencies"
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
	// OpBatchCreateIssues creates many issues as one transaction, or none.
	OpBatchCreateIssues = "batchCreateIssues"
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
)

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
var operationCodes = map[string][]Code{
	// Liveness answers from the process and touches nothing that can fail.
	OpHealth: nil,
	// v0 serves a startup snapshot without touching the database, so there is
	// no 503 here. If a later slice makes this a DB-probing readiness
	// endpoint, db_unavailable joins this row and the spec in the same change.
	OpGetContext:    {CodeInternal},
	OpListReadyWork: {CodeInvalidArgument, CodeBusy, CodeDBUnavailable, CodeInternal},
	OpListIssues:    {CodeInvalidArgument, CodeInvalidCursor, CodeBusy, CodeDBUnavailable, CodeInternal},
	// The 400 is this operation's own, the getStats precedent: a malformed
	// `include_comments` or `include_dependents` is a bad value on a parameter
	// this server knows, not the document-level unknown-key rule this table
	// omits.
	OpGetIssue: {CodeInvalidArgument, CodeNotFound, CodeBusy, CodeDBUnavailable, CodeInternal},
	// No 400 of its own: the operation takes no parameters, so the only
	// invalid_argument it can raise is the document-level unknown-query-key
	// rule this table deliberately omits.
	OpListSettings: {CodeBusy, CodeDBUnavailable, CodeInternal},
	// No 404: a key nothing stored and a key stored as the empty string are one
	// answer on this surface, so the only refusal a key can earn is the 400
	// that says it was not a key.
	OpGetSetting: {CodeInvalidArgument, CodeBusy, CodeDBUnavailable, CodeInternal},
	// The 400 here is this operation's own, not the document-level
	// unknown-parameter rule: a malformed `skip_blocked`, and the EMPTY
	// `assignee` the document refuses rather than answering with the rows that
	// have no assignee.
	OpGetStats: {CodeInvalidArgument, CodeBusy, CodeDBUnavailable, CodeInternal},
	// The cycle sweep takes no parameters at all, so it has no 400 of its own:
	// the two uniform ones above are the whole of its invalid-argument story.
	OpListDependencyCycles: {CodeBusy, CodeDBUnavailable, CodeInternal},
	// No not_found here, deliberately: an id that names nothing is reported in
	// the response's `missing` member, so a batch keeps the answers for the ids
	// that were found. A 404 would discard them.
	OpListDependencies: {CodeInvalidArgument, CodeBusy, CodeDBUnavailable, CodeInternal},
	// The same vocabulary as the stored-edge read beside it, and no not_found
	// for a stronger version of the same reason: this operation probes no id's
	// existence at all, so there is nothing it could 404 on.
	OpListBlockingAnnotations: {CodeInvalidArgument, CodeBusy, CodeDBUnavailable, CodeInternal},
	// The 404 is the difference from the row above: this operation has ONE
	// anchor, so there is no other answer to preserve by reporting the miss in
	// the body. Its 400 is its own — an empty root, a direction outside the
	// closed set, a non-positive max_depth — all three the ROLE's ErrValidation
	// reaching the wire.
	OpGetDependencyTree: {CodeInvalidArgument, CodeNotFound, CodeBusy, CodeDBUnavailable, CodeInternal},
	// The same vocabulary as the listing it sizes: it takes the same filters
	// and can refuse them the same way. limit=0's mode-dependent refusal has no
	// analog here because there is no limit to pass.
	OpCountReadyWork: {CodeInvalidArgument, CodeBusy, CodeDBUnavailable, CodeInternal},
	// The listing's vocabulary minus the cursor: this operation has none, so
	// invalid_cursor cannot arise. An unparseable EXPRESSION is an
	// invalid_argument on `q` rather than a code of its own — a client's
	// recovery is the same as for any other malformed parameter value.
	OpQueryIssues: {CodeInvalidArgument, CodeBusy, CodeDBUnavailable, CodeInternal},
	// The 400 here is the widest on this surface, and most of it is not this
	// handler's: the unfiltered durable sweep, the unrecognized tier and the
	// malformed glob are all the ROLE's ErrValidation reaching the wire through
	// failSweepErr. No 404 — this operation names no id — and no 409: a bead
	// another sweep already took is simply not in the set.
	OpSweepIssues: {CodeInvalidArgument, CodeBusy, CodeDBUnavailable, CodeInternal},
	// CodeNotFound is the one this operation has and the sweep does not: a
	// sweep describes a set that can legitimately be empty, while a delete
	// names beads and an id that resolves to nothing is a caller mistake.
	OpDeleteIssues: {CodeInvalidArgument, CodeNotFound, CodeBusy, CodeDBUnavailable, CodeInternal},
	OpClaimIssue: {
		CodeInvalidArgument, CodeNotFound, CodeAlreadyClaimed, CodeNotClaimable,
		CodeBusy, CodeDBUnavailable, CodeInternal,
	},
	// The 409 is close POLICY, and it is the only conflict this operation has:
	// an unforced close refused for open children or for a live blocker. There
	// is no already_claimed here — closing work somebody else holds is not a
	// refusal on this surface — and the idempotent re-close is a 200 carrying
	// `already_closed`, the claim's answer to the same question.
	OpCloseIssue: {
		CodeInvalidArgument, CodeNotFound, CodeNotClosable,
		CodeBusy, CodeDBUnavailable, CodeInternal,
	},
	// NO 409, and the absence is the point. Close has a policy guard — open
	// children, a live blocker — and reopen is the direction that takes an issue
	// OUT of the done category rather than putting it in, so there is no state
	// of the graph that can refuse it and nothing to name. The idempotent case
	// is a 200 carrying `already_open`, not a conflict.
	OpReopenIssue: {
		CodeInvalidArgument, CodeNotFound,
		CodeBusy, CodeDBUnavailable, CodeInternal,
	},
	// No conflict code, and the excluded members are exactly where one would
	// have entered: `status` would drag in close policy, `assignee` the claim
	// fence, `parent_id` the graph vocabulary. The 400 is the body vocabulary
	// plus the ROLE's ErrValidation — a workspace-vocabulary issue_type, a
	// field-length refusal that slipped the edge check — through failUpdate.
	OpUpdateIssue: {
		CodeInvalidArgument, CodeNotFound,
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
	OpBatchCreateIssues: {CodeInvalidArgument, CodeBusy, CodeDBUnavailable, CodeInternal},
	// No 404 and no conflict code: this is an UPSERT with a server-derivable
	// key, so there is no resource it can fail to address and no row it can
	// collide with. Its 400 is the body vocabulary plus the ROLE's two
	// refusals — empty content, and content no key can be derived from.
	OpRememberMemory: {CodeInvalidArgument, CodeBusy, CodeDBUnavailable, CodeInternal},
	// THE 404 IS THE DIVERGENCE, and it is deliberate. OpGetSetting has none
	// because on that plane an absent key and a key stored empty are one answer
	// the CLI itself prints identically, so a 404 would publish an invented
	// distinction. Here the CLI already distinguishes a miss — `bd recall` has
	// an exit-code contract for it — and the role answers Found rather than a
	// value, so the 404 reports a distinction that exists. The stored-empty row
	// falls on the miss side of it, which the document states.
	OpGetMemory: {CodeInvalidArgument, CodeNotFound, CodeBusy, CodeDBUnavailable, CodeInternal},
	// The read's vocabulary exactly, because the two operations address the same
	// resource the same way and this one's Found false is the same answer: a key
	// nothing stored is a 404 and nothing was removed. No 409 — a memory another
	// caller already forgot is simply not there, which is what the 404 says.
	OpForgetMemory: {CodeInvalidArgument, CodeNotFound, CodeBusy, CodeDBUnavailable, CodeInternal},
	// The 400 here IS this operation's own, unlike listSettings' absent row: it
	// has one parameter, and a repeated `q` is refused rather than resolved to
	// one of its values. No 404 — a search matching nothing is an empty page,
	// because a question about a set has an answer even when the set is empty.
	OpListMemories: {CodeInvalidArgument, CodeBusy, CodeDBUnavailable, CodeInternal},
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
		CodeInvalidArgument, CodeEventsJournalDisabled, CodeEventsJournalTruncated,
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
		CodeInvalidArgument, CodeEventsJournalDisabled, CodeEventsJournalTruncated,
		CodeEventsWatchSaturated, CodeBusy, CodeDBUnavailable, CodeInternal,
	},
	// NO 404, for a stronger version of the batch create's reason: an edge that
	// is not there is `removed: false`, and an endpoint id that names nothing
	// holds no edge either, so this operation probes no id's existence and has
	// nothing it could report a miss on — listBlockingAnnotations' argument,
	// applied to a write. No conflict code either: the removal is idempotent, so
	// another caller having got there first is a success rather than a collision.
	OpRemoveDependency: {CodeInvalidArgument, CodeBusy, CodeDBUnavailable, CodeInternal},
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
		CodeInvalidArgument, CodeDependencyCycle, CodeDependencyExists,
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
	w.WriteHeader(res.Problem.Status)
	_ = json.NewEncoder(w).Encode(res.Problem)
}
