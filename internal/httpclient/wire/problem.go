// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/wire/problem.go@49d1df2f6)
// to OSS beads under the MIT license.
package wire

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/issueops"
)

// The refusals with no analog in the local store's vocabulary, because each is
// a statement about a SERVER rather than about the workspace. Everything else
// in the code table maps to the canonical issueops sentinel the embedded store
// would have returned for the same refusal, so a caller classifies a remote
// refusal with the errors.Is arm it already has.
var (
	// ErrInvalidCursor is a cursor this server did not issue, cannot decode, or
	// issued under a different internal version. The recovery is to restart
	// paging without it, which is why it is not folded into ErrValidation: a
	// stale cursor is a normal client situation, not a client bug.
	ErrInvalidCursor = errors.New("cursor is not valid for this server; restart paging")
	// ErrUnauthenticated is a missing, malformed or unrecognized credential on
	// a server that was configured with one. It never carries the credential or
	// any hint of which of the three it was — the server refuses to say, and
	// repeating a guess here would undo that.
	ErrUnauthenticated = errors.New("bd serve refused the credential")
	// ErrBusy is retryable contention: the server's transaction retry budget was
	// exhausted, or its in-flight limit was saturated.
	ErrBusy = errors.New("bd serve is busy")
	// ErrDBUnavailable is a retryable failure to reach the served database.
	ErrDBUnavailable = errors.New("bd serve cannot reach its database")
	// ErrServerFault is an unexpected server-side failure. Its detail is a fixed
	// string by design, so ProblemError.RequestID is the only handle on the log
	// line that has the real error.
	ErrServerFault = errors.New("bd serve failed")
	// ErrBadRequest is the 4xx default branch: a code this client does not know,
	// on a status class that says the request was wrong. Failing loud is the
	// documented posture — an unknown 4xx is a client bug, and retrying or
	// degrading past one hides it.
	ErrBadRequest = errors.New("bd serve refused the request")

	// The journal codes. This client dials no journal operation — httpstore
	// carries no EventsJournal accessor, so nothing here can produce a request
	// that earns one of these — and they are on the code table anyway, because
	// the vocabulary gate is set-equality with the document rather than with
	// what this client happens to reach today. A code the document publishes
	// and this table omitted would take the 4xx default branch and be reported
	// as a client bug on the day the journal accessor lands.

	// ErrEventsJournalDisabled is the served workspace having its journal OFF.
	// The recovery is entirely the operator's — set `events-journal true` and
	// restart the server — so it is not folded into ErrValidation, which would
	// send a caller looking at its own request.
	ErrEventsJournalDisabled = errors.New("bd serve: the events journal is not enabled on the served workspace")
	// ErrEventsWatchSaturated is the server already holding as many open
	// journal streams as it will. It is NOT ErrBusy despite the shared 503:
	// busy says the database is congested and the same request will work
	// shortly, this says a bounded resource is fully subscribed by connections
	// that may last hours, and the caller has a recovery busy does not offer —
	// the paged read, which holds nothing between requests.
	ErrEventsWatchSaturated = errors.New("bd serve: no journal stream slots are free")
	// ErrPreconditionFailed is a compare-and-set guard that missed on an
	// operation whose contract is that a miss refuses everything. It is the
	// fallback for the shape the server did not name: when `param` says which
	// guard missed, the table answers with the issueops sentinel the embedded
	// store would have returned for the same miss.
	ErrPreconditionFailed = errors.New("bd serve: a precondition on the request did not hold")
	// ErrEventsJournalTruncated is a journal read whose checkpoint has fallen
	// below the retained window. ProblemError carries the window itself
	// (Since/Floor/Head), because the recovery is a decision the consumer makes
	// from those three numbers rather than from this sentence.
	ErrEventsJournalTruncated = errors.New("bd serve: the journal checkpoint is below the retained window")

	// ErrUnknownItemCode is the PER-ITEM analog of the 4xx default branch above:
	// a BatchCloseItemError.code this client does not recognize. Per-item codes
	// grow additively exactly as Problem.code does — the schema tells clients to
	// default-branch on an unknown one — so an unfamiliar code must not hard-fail
	// the whole batch. It lands as a TYPED per-item outcome instead, so a caller
	// tells "this item was refused for a reason my client is too old to name"
	// from a success rather than reading it as one.
	ErrUnknownItemCode = errors.New("bd serve refused a batch-close item with a code this client does not recognize")
)

// UnknownItemCodeError is the typed carrier for that per-item refusal. It keeps
// the code verbatim and the server's optional detail so a renderer can show what
// it could not classify, and unwraps to ErrUnknownItemCode so errors.Is reaches
// it. Both strings are server-controlled, so the layer that builds one strips
// their control runes the way mapProblem strips every other wire field.
type UnknownItemCodeError struct {
	IssueID string
	Code    string
	Detail  string
}

func (e *UnknownItemCodeError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("issue %s: bd serve refused it with an unrecognized batch-close code %q: %s", e.IssueID, e.Code, e.Detail)
	}
	return fmt.Sprintf("issue %s: bd serve refused it with an unrecognized batch-close code %q", e.IssueID, e.Code)
}

func (e *UnknownItemCodeError) Unwrap() error { return ErrUnknownItemCode }

// ProblemError is a non-2xx answer from bd serve, decoded from its RFC 9457
// problem+json body.
//
// It carries the whole envelope because the layers above reconstruct typed
// refusals and user-facing text from it: `code` is the dispatch key, `param`
// and `reason` turn a 400 back into the flag that produced it, `request_id` is
// what makes a 5xx actionable, and the extension members are the typed
// discriminators the server reads inside the refusing transaction.
//
// The extension members are POINTERS because presence is load-bearing on three
// of them: `open_children` separates the two not_closable refusals, `issue_id`
// separates a hierarchy conflict from a plain scheduling cycle, and
// `blocker_is_ancestor` is emitted in both polarities and so cannot use absence
// to mean false.
type ProblemError struct {
	// Op is the operation id that was dialed, and ServerURL the base URL it was
	// dialed against. Neither comes off the wire; both are what the refusal
	// taxonomy needs to name the server in its text.
	Op        string
	ServerURL string

	Status    int
	Code      string
	Title     string
	Detail    string
	Param     string
	Reason    string
	RequestID string

	// CredentialSource names the ladder rung whose credential the server refused,
	// set only on a 401 and only when the provider can report it (design D7). It
	// is a provenance label — an env var name, a command's env var, or the
	// credentials file [host:port] — never the credential itself, so it is safe
	// in error text bound for logs and terminals.
	CredentialSource string

	// RetryAfter is the server's Retry-After header, already bounded (see
	// Options.MaxRetryAfter). Zero when the header was absent, unparseable or
	// in the past.
	RetryAfter time.Duration

	Assignee          *string
	IssueStatus       *string
	OpenChildren      *int
	ExistingType      *string
	RequestedType     *string
	IssueID           *string
	BlockerID         *string
	BlockerIsAncestor *bool

	// The compare-and-set guard's two sides, with `precondition_failed`. `param`
	// says WHICH guard missed and these say what the request asked for and what
	// the row was found holding, split BY TYPE rather than carried as one
	// polymorphic pair — a member that is "a version or a status or an
	// assignee" is a schema alternation, and this document spells three typed
	// pairs instead.
	//
	// They are pointers for the reason every pointer here is one: PRESENCE is
	// load-bearing on all six. An `expected_*` echoes the request, so its
	// absence means the operation carried no such guard — and 0 and "" are both
	// real guards, so neither zero value can stand in for absence. An `actual_*`
	// is present only where the refusing operation can REPORT what it found: an
	// all-or-nothing operation rolls its transaction back, and a value read
	// after the fact would describe a row the refusal never saw, so absence
	// there means "this server cannot tell you", never "it found zero".
	//
	// THE VERSIONS ARE CARRIED AS THE STRINGS THE WIRE SPELLS THEM IN. Since
	// upstream #6053 every revision token on the surface is a decimal string
	// (types.RevisionToken), never a JSON number, so nothing on this path can
	// round one. They are deliberately NOT parsed back to int64 here: the token
	// is opaque and equality-only, `expected_version` is the ECHO of what the
	// request sent (a caller comparing it to its own guard compares
	// types.RevisionToken(guard) to it), and `actual_version` is a diagnostic —
	// the contract says to compose the next guard from a `revision` a WRITE
	// answered with, never from a refusal. Parsing on the refusal path could
	// only turn a well-formed 409 into a decode failure or drop a member whose
	// presence is load-bearing, and nothing in this package reads them
	// numerically. They are stripped like every other server-controlled string.
	ExpectedVersion  *string
	ActualVersion    *string
	ExpectedStatus   *string
	ActualStatus     *string
	ExpectedAssignee *string
	ActualAssignee   *string

	// The batch-item members, on a refusal from an operation whose items are
	// HETEROGENEOUS and may be named — issues:batchApply is the whole
	// population. They are the only place the offender exists: that operation
	// is all or nothing, so a refusal carries no per-item result array for a
	// client to find it in, and the server reads these off the role's own typed
	// *issueops.ItemError inside the transaction that refused.
	//
	// PRESENCE IS LOAD-BEARING ON ALL OF THEM, which is why the index is a
	// pointer and not an int: 0 is the FIRST ITEM and a real answer, so a bare
	// int could not tell it from a refusal that named no item at all. Key and
	// IssueID are absent for real states rather than gaps — not every item
	// carries a key, and a create whose id was never minted has none to report.
	//
	// ItemIssueID is deliberately NOT ProblemError.IssueID, and the divergence
	// is the server's rather than a spelling choice here: IssueID's PRESENCE
	// discriminates the dependency_cycle hierarchy refusal from the plain
	// scheduling one, so a batch reusing it would fire that discriminator on
	// refusals it says nothing about.
	ItemIndex   *int
	ItemKind    *string
	ItemKey     *string
	ItemIssueID *string
	// DeclaredLater tells the two unresolvable-key diagnoses apart: true is an
	// ORDERING mistake — the key IS declared, by a later item, and a key
	// reaches backward only — and false is a key nothing in the request
	// declares, which is a typo or a missing item. It is emitted in BOTH
	// polarities, so it is a pointer for the reason BlockerIsAncestor is one:
	// absence has to mean "this refusal was not about a key", never false.
	DeclaredLater *bool

	// The journal-truncation window, with `events_journal_truncated`: the
	// checkpoint the window begins after, the lowest seq still retained, and the
	// highest seq ever assigned.
	//
	// They are carried rather than folded into the sentinel because that
	// sentinel is storage.EventsJournalTruncatedError, and this package
	// deliberately imports no storage — the whole point of respelling the
	// operation ids here is that a process talking to a bd serve does not link
	// the engine. A consumer with a journal accessor rebuilds the typed error
	// from these three; without them the recovery (resume from floor-1 and
	// accept the gap, or re-baseline) would have to be parsed out of prose.
	Since *int64
	Floor *int64
	Head  *int64

	// Err is the canonical sentinel this code maps to. It is what Unwrap
	// returns, so errors.Is and errors.As reach it.
	Err error
}

func (e *ProblemError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: bd serve at %s answered %d", e.Op, e.ServerURL, e.Status)
	if e.Code != "" {
		fmt.Fprintf(&b, " %s", e.Code)
	}
	if e.Detail != "" {
		fmt.Fprintf(&b, ": %s", e.Detail)
	}
	// The 5xx detail is a fixed string per code and says nothing about the
	// underlying failure, so the correlation id is the client's only handle on
	// the one server log line that has it.
	if e.Status >= 500 && e.RequestID != "" {
		fmt.Fprintf(&b, " (request_id %s)", e.RequestID)
	}
	// Naming the rung the refused credential came from is what turns "the server
	// said 401" into something the operator can act on: it says WHICH source to
	// rotate or fix (design D7). The server never says which of missing,
	// malformed or unrecognized it was, and this does not guess — it reports only
	// the client-side provenance of the credential it presented.
	if e.Status == http.StatusUnauthorized && e.CredentialSource != "" {
		fmt.Fprintf(&b, " (credential from %s)", e.CredentialSource)
	}
	return b.String()
}

func (e *ProblemError) Unwrap() error { return e.Err }

// Retryable reports whether the server said to come back. Only the 503 class
// says so: an unknown 4xx is a client bug and every other 5xx is a fault the
// same request will earn again.
func (e *ProblemError) Retryable() bool {
	return e.Status == http.StatusServiceUnavailable ||
		errors.Is(e.Err, ErrBusy) || errors.Is(e.Err, ErrDBUnavailable)
}

// ReasonProjectMismatch is the Problem `reason` the server sets on a refusal of
// a Bd-Project-Id stamp for the wrong workspace, spelled exactly as the server's
// httpapi.ReasonProjectMismatch (held to it by
// TestProjectMismatchReasonMatchesTheServerConstant). It is the discriminator
// mapProblem reads to build the typed *ProjectMismatchError, ahead of the code
// table: the code is the generic invalid_argument, so reason is the only thing
// that tells this wrong-server refusal from a malformed-argument one.
const ReasonProjectMismatch = "project_mismatch"

// ReasonWireRevisionUnsupported is the Problem `reason` the server sets when
// this client's declared Bd-Wire-Revision header named a revision below the
// server's own min_client_wire_revision, spelled exactly as the server's
// httpapi.ReasonWireRevisionUnsupported (held to it by
// TestTheProjectIdentityVocabularyMatchesTheServer). Like ReasonProjectMismatch
// it rides the generic invalid_argument code, so reason is the only thing that
// tells this refusal apart from an ordinary malformed argument.
const ReasonWireRevisionUnsupported = "wire_revision_unsupported"

// ErrWireRevisionUnsupported reports that bd serve refused this client's
// declared Bd-Wire-Revision header as older than the revision it now
// requires. See WireRevisionUnsupportedError.
var ErrWireRevisionUnsupported = errors.New("bd serve requires a newer client wire revision than this client declared")

// WireRevisionUnsupportedError is the per-request mirror of the handshake's
// own gate (wire.WireRevisionSkewError, handshake.go): the server has read
// this client's declared Bd-Wire-Revision header (wire.ClientWireRevision,
// stamped on every request by Client.stampRequest) and refused it as below
// the floor it enforces now. Unlike WireRevisionSkewError — raised locally,
// from a 200 ContextResponse, when THIS client decides the SERVER's wire
// shape is one it cannot speak — this one is the SERVER's own refusal, and it
// can arrive on any request, including the context fetch the handshake
// itself makes (ContextResponse.MinClientWireRevision's doc: "raised ... on
// GET /v0/beads/context too"), in which case it surfaces through
// Client.Handshake exactly the way any other *ProblemError from GetContext
// does — no separate handling is needed there.
type WireRevisionUnsupportedError struct {
	ServerURL string
	BdVersion string
	// ClientWireRevision is this build's own declared revision, which is what
	// was refused.
	ClientWireRevision int
	// MinClientWireRevision is the server's own floor, from the refusal's
	// min_wire_revision extension member.
	MinClientWireRevision int
	// ServerWireRevision is the server's own current wire_revision, from the
	// refusal's wire_revision extension member.
	ServerWireRevision int
}

func (e *WireRevisionUnsupportedError) Error() string {
	return fmt.Sprintf("bd serve at %s (bd_version %s) requires a client wire revision of at least %d; this client declared %d",
		e.ServerURL, e.BdVersion, e.MinClientWireRevision, e.ClientWireRevision)
}

func (e *WireRevisionUnsupportedError) Unwrap() error { return ErrWireRevisionUnsupported }

// target names the ids the wire's conflict members leave out. The server does
// not echo the issue a claim refused or the edge a dependency_exists collided
// with — the request already said — so the caller supplies them and the
// reconstructed sentinel comes out whole.
type target struct {
	op          string
	serverURL   string
	issueID     string
	dependsOnID string
	// expectID is the workspace's own pinned project id (Client.expectID), which
	// the wire never carries — it is a client-side fact. A project_mismatch
	// refusal names the server's own id in server_project_id; pairing it with this
	// expected id is what lets the typed *ProjectMismatchError say BOTH sides of
	// the disagreement without the client parsing the refusal's prose.
	expectID string
}

// mapProblem decodes an RFC 9457 body and maps it onto the canonical sentinel
// for its code.
//
// `code` is the ONLY dispatch key, per the document: a code carries one frozen
// status, so reading the status instead would be a second, weaker copy of the
// same table. The status is consulted for exactly one thing — the default
// branch for a code this client has never heard of, which is how the vocabulary
// stays additive.
func mapProblem(t target, status int, header http.Header, body []byte, maxRetryAfter time.Duration) *ProblemError {
	var p apigen.Problem
	// A non-2xx that is not problem+json at all is a real case: a proxy, a load
	// balancer or a stray server on the port answers with HTML. It falls through
	// to the status-class default branch with an empty code, which is exactly
	// what an unrecognized code does.
	_ = json.Unmarshal(body, &p)

	// Every string that comes off the wire here is server-controlled and ends up
	// in an error rendered to a terminal or a log line — often through a %v sink
	// that never touches the CLI's output-boundary strip (bd close/show/reopen/
	// update write each per-id failure straight to os.Stderr). So the control
	// runes are removed HERE, the moment the bytes become a typed error, and both
	// the direct fields AND the extension members the typed conflicts are
	// reconstructed from below (sentinelFor) are cleaned — an issue id echoed by a
	// dependency-cycle conflict is as server-controlled as the detail string. The
	// pointer members keep their presence, which is load-bearing discrimination on
	// three of them. Tab and newline are stripped too (unlike the display-layer
	// strip): a problem field is a single value with no legitimate framing.
	e := &ProblemError{
		Op:                t.op,
		ServerURL:         t.serverURL,
		Status:            status,
		Code:              stripControlRunes(p.Code),
		Title:             stripControlRunes(p.Title),
		Detail:            stripControlRunes(deref(p.Detail)),
		Param:             stripControlRunes(deref(p.Param)),
		Reason:            stripControlRunes(deref(p.Reason)),
		RequestID:         stripControlRunes(p.RequestId),
		RetryAfter:        retryAfter(header, maxRetryAfter),
		Assignee:          stripControlRunesPtr(p.Assignee),
		IssueStatus:       stripControlRunesPtr(p.IssueStatus),
		OpenChildren:      p.OpenChildren,
		ExistingType:      stripControlRunesPtr(p.ExistingType),
		RequestedType:     stripControlRunesPtr(p.RequestedType),
		IssueID:           stripControlRunesPtr(p.IssueId),
		BlockerID:         stripControlRunesPtr(p.BlockerId),
		BlockerIsAncestor: p.BlockerIsAncestor,
		Since:             p.Since,
		Floor:             p.Floor,
		Head:              p.Head,
		// The guard pair. All six are strings on the wire — the two versions
		// are decimal revision tokens (types.RevisionToken) and are carried as
		// the strings the server spelled, see the field comment — and all six
		// are stripped like every other server-controlled field, keeping their
		// PRESENCE, which is the state that discriminates.
		ExpectedVersion:  stripControlRunesPtr(p.ExpectedVersion),
		ActualVersion:    stripControlRunesPtr(p.ActualVersion),
		ExpectedStatus:   stripControlRunesPtr(p.ExpectedStatus),
		ActualStatus:     stripControlRunesPtr(p.ActualStatus),
		ExpectedAssignee: stripControlRunesPtr(p.ExpectedAssignee),
		ActualAssignee:   stripControlRunesPtr(p.ActualAssignee),
		// The batch-item members. The three strings are stripped like every
		// other server-controlled field and keep their presence; the index and
		// the ordering discriminator travel as the generated pointers, which is
		// what keeps a zero index and a false polarity apart from absence.
		ItemIndex:     p.ItemIndex,
		ItemKind:      stripControlRunesPtr(p.ItemKind),
		ItemKey:       stripControlRunesPtr(p.ItemKey),
		ItemIssueID:   stripControlRunesPtr(p.ItemIssueId),
		DeclaredLater: p.DeclaredLater,
	}
	legacyRevisionFields(body, e)

	// The wrong-server arm, discriminated on REASON and taken BEFORE the code
	// table. A project_mismatch is spelled invalid_argument on the wire, so the
	// code table would map it to ErrValidation and lose the whole signal; the
	// reason is what tells it apart. It is the one refusal that carries
	// server_project_id — the id the server actually serves — which is stripped
	// here at the decode boundary like every other server-controlled field and is
	// deliberately NOT a generic ProblemError member: it exists only to build this
	// typed error, paired with the workspace's own expected id, and unwraps
	// ErrProjectMismatch so cmd/bd renders one recovery block for both this
	// per-request path and the handshake identity gate. Database and RepoRoot are
	// left empty: this refusal discloses only the server's id, not its location.
	if e.Reason == ReasonProjectMismatch {
		e.Err = &ProjectMismatchError{
			ServerURL: t.serverURL,
			Expected:  t.expectID,
			Got:       stripControlRunes(deref(p.ServerProjectId)),
		}
		return e
	}

	// The other document-level arm, discriminated the same way and for the
	// same reason: wire_revision_unsupported rides the generic
	// invalid_argument code too, so reason is what tells it apart. The three
	// extension members it alone carries are read straight off the problem —
	// stripped like every other server-controlled field — rather than folded
	// into the generic ProblemError, because this refusal is a statement about
	// the SERVER's wire shape, not about the request's arguments.
	if e.Reason == ReasonWireRevisionUnsupported {
		e.Err = &WireRevisionUnsupportedError{
			ServerURL:             t.serverURL,
			BdVersion:             stripControlRunes(deref(p.BdVersion)),
			ClientWireRevision:    ClientWireRevision,
			MinClientWireRevision: deref(p.MinWireRevision),
			ServerWireRevision:    deref(p.WireRevision),
		}
		return e
	}

	e.Err = sentinelFor(e, t)
	return e
}

// decodeRevisionToken reads a revision-bearing problem member that may be
// encoded either as the decimal string every server since upstream #6053
// uses (types.RevisionToken) or as the bare JSON integer a server old enough
// to omit ContextResponse.wire_revision entirely still sends (see
// ClientMinWireRevision's doc: a decoded 0 means the server omitted the
// member, and 0 is exactly the pre-#6053 integer-token generation). Both
// shapes name the same opaque token — this client never parses either one
// back to int64 (ProblemError.ExpectedVersion's doc) — so tolerating the
// older shape here is only about surviving the decode, never about deriving a
// number from it.
func decodeRevisionToken(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, true
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err == nil {
		return n.String(), true
	}
	return "", false
}

// revisionNeedsFallback reports whether a *string revision field still needs
// legacyRevisionFields' integer-tolerant recovery. A genuinely absent member
// decodes to nil, as expected — but encoding/json's indirect() allocates a
// pointer to the zero value ("") for a *string field BEFORE it discovers the
// JSON value's type doesn't match, when the member IS present with the wrong
// shape (a bare integer). That pre-allocation means a type-mismatched field
// comes out of the primary json.Unmarshal(body, &p) as a non-nil pointer to
// "", not nil — so checking only for nil here would never recover exactly the
// one case this fallback exists for. No legitimate server response uses a
// literal empty-string revision token (ProblemError.ExpectedVersion's doc:
// both are opaque, server-defined, non-numeric tokens an operation always
// names when it fires at all), so treating "" the same as nil costs nothing.
func revisionNeedsFallback(p *string) bool {
	return p == nil || *p == ""
}

// legacyRevisionFields backfills expected_version/actual_version when the
// primary decode above left them needing fallback (see revisionNeedsFallback).
// That happens exactly when a pre-#6053 server sent one as a bare JSON
// integer rather than the decimal string every server since has used:
// json.Unmarshal(body, &p) already tolerates the mismatch (a bad field does
// not abort decoding the rest, and mapProblem deliberately discards that
// error), so the only member actually lost is the one with the wrong shape —
// and that is exactly what this recovers, from the same bytes, by parsing the
// two revision keys alone and accepting either shape.
func legacyRevisionFields(body []byte, e *ProblemError) {
	if !revisionNeedsFallback(e.ExpectedVersion) && !revisionNeedsFallback(e.ActualVersion) {
		return
	}
	var raw struct {
		ExpectedVersion json.RawMessage `json:"expected_version"`
		ActualVersion   json.RawMessage `json:"actual_version"`
	}
	if json.Unmarshal(body, &raw) != nil {
		return
	}
	if revisionNeedsFallback(e.ExpectedVersion) {
		if v, ok := decodeRevisionToken(raw.ExpectedVersion); ok {
			v = stripControlRunes(v)
			e.ExpectedVersion = &v
		}
	}
	if revisionNeedsFallback(e.ActualVersion) {
		if v, ok := decodeRevisionToken(raw.ActualVersion); ok {
			v = stripControlRunes(v)
			e.ActualVersion = &v
		}
	}
}

// codeSentinel is the whole problem-code vocabulary this client knows, and the
// canonical error each code becomes.
//
// It is a table rather than a switch so the vocabulary is ENUMERABLE:
// TestTheCodeTableIsSetEqualWithTheDocumentedVocabulary walks it against the
// `x-bd-codes` rows of the shipped OpenAPI document, in both directions, which
// a switch statement could not be held to.
var codeSentinel = map[string]func(*ProblemError, target) error{
	"invalid_argument": func(*ProblemError, target) error { return issueops.ErrValidation },
	"invalid_cursor":   func(*ProblemError, target) error { return ErrInvalidCursor },
	"unauthenticated":  func(*ProblemError, target) error { return ErrUnauthenticated },
	"not_found":        func(*ProblemError, target) error { return issueops.ErrNotFound },
	"already_exists":   func(*ProblemError, target) error { return issueops.ErrAlreadyExists },
	"busy":             func(*ProblemError, target) error { return ErrBusy },
	"db_unavailable":   func(*ProblemError, target) error { return ErrDBUnavailable },
	"internal":         func(*ProblemError, target) error { return ErrServerFault },

	// The journal codes, on the table for the reason stated beside their
	// sentinels: set-equality with the document, not with what this client
	// reaches today.
	"events_journal_disabled":  func(*ProblemError, target) error { return ErrEventsJournalDisabled },
	"events_journal_truncated": func(*ProblemError, target) error { return ErrEventsJournalTruncated },
	"events_watch_saturated":   func(*ProblemError, target) error { return ErrEventsWatchSaturated },

	// `param` names WHICH guard missed, in the same spelling a 400 on the same
	// operation would use, so the client answers with the issueops sentinel the
	// embedded store returns for that miss and a caller classifies both
	// backends with one errors.Is arm. An unrecognized (or absent) param takes
	// the generic sentinel rather than guessing one of the three.
	//
	// The MEMBER is read off the end of the param rather than compared whole,
	// because a batch operation qualifies it by the item that carried the
	// guard: `items[1].update.expected_version` is the same member as
	// `expected_version` and means the same miss. A whole-string comparison
	// answered the generic sentinel for every guarded item in a plan, which is
	// a caller told "a precondition failed" where a local backend tells it
	// WHICH — see paramMember.
	"precondition_failed": func(e *ProblemError, _ target) error {
		switch paramMember(e.Param) {
		case "expected_version":
			return issueops.ErrVersionMismatch
		case "expected_status":
			return issueops.ErrStatusMismatch
		case "expected_assignee":
			return issueops.ErrAssigneeMismatch
		default:
			return ErrPreconditionFailed
		}
	},

	"already_claimed": func(e *ProblemError, t target) error {
		return claimConflict(e, t, issueops.ErrAlreadyClaimed)
	},
	"not_claimable": func(e *ProblemError, t target) error {
		return claimConflict(e, t, issueops.ErrNotClaimable)
	},

	// Member PRESENCE is the discriminator the server documents: the
	// open-children refusal carries the count, the live-blocker one carries
	// nothing. Reading `detail` to tell them apart is exactly what the extension
	// member exists to prevent.
	"not_closable": func(e *ProblemError, t target) error {
		if e.OpenChildren != nil {
			return &issueops.CloseOpenChildrenError{IssueID: refusedIssueID(e, t), OpenChildren: *e.OpenChildren}
		}
		return issueops.ErrCloseBlocked
	},

	// not_releasable is the RELEASE's refusal, and it is REACHED: httpReleaser
	// dials issues:release (releaser.go, client wave ga-f352s) and nine of the
	// eleven Releaser contracts run through this mapping on the served tier.
	//
	// It was on this table BEFORE it was reachable, for the journal codes'
	// reason — set-equality with the document rather than with what this client
	// happens to dial — and that is worth keeping now that the wait is over: a
	// code the document publishes and this table omitted would take the 4xx
	// default branch and be reported as a client bug on the day an accessor
	// started earning it.
	//
	// The role splits the refusal in two where the wire does not: ErrNotClaimed
	// for a row holding no claim, ErrNotReleasable for a status that will not
	// accept one. One code covers both by the server's own deliberate choice
	// (see httpapi's CodeNotReleasable), and it carries no member that tells
	// them apart, so this maps to the STATUS sentinel — the wider of the two,
	// and the one whose message does not assert a fact the wire never sent.
	"not_releasable": func(*ProblemError, target) error { return issueops.ErrNotReleasable },

	// [Added: OSS publishes notes_overwrite_refused (internal/httpapi/problem.go
	// CodeNotesOverwrite, status 409) for an update whose Patch.Notes would
	// replace existing notes without an explicit overwrite flag. This code had
	// no entry at all, so a server sending it fell through to the 4xx default
	// and reported as an undifferentiated client bug instead of the typed
	// sentinel callers already switch on locally.]
	"notes_overwrite_refused": func(*ProblemError, target) error { return issueops.ErrNotesOverwrite },

	// The same rule again: `issue_id` present means the edge named the issue's
	// own ancestor or descendant, absent means a plain scheduling cycle. The
	// three hierarchy members travel together and rebuild the typed error whole
	// — including BlockerIsAncestor's false polarity, which is why the wire
	// sends the boolean rather than omitting it.
	"dependency_cycle": func(e *ProblemError, _ target) error {
		if e.IssueID != nil {
			return &issueops.DependencyHierarchyConflictError{
				IssueID:           *e.IssueID,
				BlockerID:         deref(e.BlockerID),
				BlockerIsAncestor: deref(e.BlockerIsAncestor),
			}
		}
		return issueops.ErrDependencyCycle
	},

	// The endpoints are not on the wire — the request already said them — so
	// they come from the caller and the typed error comes out whole. A batch add
	// that refused on one of many edges leaves them empty, because the wire does
	// not say WHICH edge collided.
	"dependency_exists": func(e *ProblemError, t target) error {
		return &issueops.DependencyTypeConflictError{
			IssueID:       t.issueID,
			DependsOnID:   t.dependsOnID,
			ExistingType:  deref(e.ExistingType),
			RequestedType: deref(e.RequestedType),
		}
	},
}

// Codes lists the problem codes this client maps, sorted. Anything outside it
// takes the status-class default branch.
func Codes() []string {
	out := make([]string, 0, len(codeSentinel))
	for code := range codeSentinel {
		out = append(out, code)
	}
	slices.Sort(out)
	return out
}

func sentinelFor(e *ProblemError, t target) error {
	if row, ok := codeSentinel[e.Code]; ok {
		return row(e, t)
	}

	// The default branch. Adding a code is not a breaking change, so a client
	// that hard-failed on an unknown one would break on every server newer than
	// itself; falling back to the status class is what makes the vocabulary
	// additive.
	switch {
	case e.Status == http.StatusUnauthorized:
		// A 401 is authentication whether or not the answer carried a problem
		// document, and it must be classified BEFORE the 4xx default. bd serve's
		// own 401 arrives with a code and never reaches here; what does is a 401
		// from something in FRONT of it — a gateway or a proxy edge rejecting an
		// expired credential before the request ever reaches bd — which answers
		// with its own html or nothing at all. Falling through to ErrBadRequest
		// told the caller its REQUEST was malformed, which sends an operator
		// looking at flags for a credential to rotate.
		return ErrUnauthenticated
	case e.Status == http.StatusServiceUnavailable:
		return ErrBusy
	case e.Status >= 500:
		return ErrServerFault
	case e.Status >= 400:
		return ErrBadRequest
	default:
		// A 1xx or 3xx reaching here is not a problem document at all. 3xx is
		// refused before this point (redirects are not followed), so this is the
		// unreachable arm — it fails as a server fault rather than as success.
		return ErrServerFault
	}
}

// claimConflict rebuilds *issueops.ClaimConflictError from the two extension
// members the refusing transaction read. The wrapped sentinel is what
// errors.Is matches, so a caller that only wants "someone else has it" needs no
// errors.As at all.
func claimConflict(e *ProblemError, t target, sentinel error) error {
	return &issueops.ClaimConflictError{
		IssueID:  refusedIssueID(e, t),
		Assignee: deref(e.Assignee),
		Status:   issueops.Status(deref(e.IssueStatus)),
		Err:      sentinel,
	}
}

// refusedIssueID names the row a reconstructed conflict is about.
//
// The REQUEST is the first source and stays the first source: the conflict
// members the server leaves out are the ones the request already said, which is
// what target exists for. But a BATCH names as many rows as it has items, so
// there is no single request id to supply — and on that operation the server
// publishes the one it acted on as `item_issue_id`, read inside the refusing
// transaction. Without this fallback every conflict rebuilt from a plan named
// the empty string, which reads as "the server did not say" when the server
// said it plainly.
func refusedIssueID(e *ProblemError, t target) string {
	if t.issueID != "" {
		return t.issueID
	}
	return deref(e.ItemIssueID)
}

// paramMember reads the offending MEMBER off a problem's `param`.
//
// A single-resource operation spells it bare (`expected_version`); a batch
// qualifies it by the item that carried it (`items[1].update.expected_version`)
// so a client can find the offender in a request it composed. The member is the
// last dotted segment either way, and reading it is what lets one code table
// serve both spellings.
func paramMember(param string) string {
	if i := strings.LastIndex(param, "."); i >= 0 {
		return param[i+1:]
	}
	return param
}

// retryAfter reads the header RFC 9110 defines in two spellings and bounds it.
//
// The bound is not politeness: the header is server-controlled input, and an
// unbounded honor is a server (or anything that can answer as one) parking the
// caller's process for as long as it likes. A value past the bound is clamped
// rather than dropped, because the server still meant "not immediately".
//
// Honoring it means bounding it and handing it to the caller — this layer never
// sleeps and never resends. Half of this surface is non-idempotent custom-method
// POSTs (claim, close, sweep, delete, the dependency writes), and a transport
// that slept and resent one of those on a 503 would double-apply a write whose
// first attempt may well have committed before the server ran out of slots.
// Which 503s are safe to come back to is the dispatch layer's question, and it
// is the layer that knows the answer.
func retryAfter(header http.Header, max time.Duration) time.Duration {
	raw := strings.TrimSpace(header.Get("Retry-After"))
	if raw == "" {
		return 0
	}
	var d time.Duration
	if secs, err := strconv.Atoi(raw); err == nil {
		d = time.Duration(secs) * time.Second
	} else if at, err := http.ParseTime(raw); err == nil {
		d = time.Until(at)
	} else {
		return 0
	}
	if d < 0 {
		return 0
	}
	if max > 0 && d > max {
		return max
	}
	return d
}

func deref[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}

// stripControlRunes removes every control rune from a server-controlled problem
// field: category Cc — the C0 block, DEL and the C1 block, where ESC 0x1B and CSI
// 0x9B live — plus the U+2028/U+2029 line and paragraph separators. It mirrors the
// server's own actor rule (httpapi's isControlChar) and, unlike the CLI's
// display-layer strip, keeps nothing back: a problem field is a single value, so
// tab and newline in one are as much an injection as an escape introducer. This
// is the source-of-truth layer — it makes a decoded *ProblemError safe in EVERY
// field before it reaches any of the many stderr sinks that render it via %v.
func stripControlRunes(s string) string {
	if !strings.ContainsFunc(s, isControlRune) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if isControlRune(r) {
			return -1
		}
		return r
	}, s)
}

// stripControlRunesPtr strips a pointer member in place-safe fashion, preserving
// nil. Presence is load-bearing on three of the extension members (an explicit
// value versus an absent one discriminates two refusals), so a cleaned value is
// returned through a fresh pointer and an absent one stays absent.
func stripControlRunesPtr(p *string) *string {
	if p == nil {
		return nil
	}
	s := stripControlRunes(*p)
	return &s
}

func isControlRune(r rune) bool {
	return unicode.IsControl(r) || r == '\u2028' || r == '\u2029'
}
