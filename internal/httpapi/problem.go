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
// at what the six v0 operations actually need.
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
	CodeInvalidArgument: http.StatusBadRequest,
	CodeInvalidCursor:   http.StatusBadRequest,
	CodeNotFound:        http.StatusNotFound,
	CodeAlreadyClaimed:  http.StatusConflict,
	CodeNotClaimable:    http.StatusConflict,
	CodeBusy:            http.StatusServiceUnavailable,
	CodeDBUnavailable:   http.StatusServiceUnavailable,
	CodeInternal:        http.StatusInternalServerError,
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
	OpListIssues    = "listIssues"
	OpGetIssue      = "getIssue"
	OpClaimIssue    = "claimIssue"
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
// documents them once at the document level rather than repeating them on all
// six operations; these rows carry what an operation produces beyond them.
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
	OpGetIssue:      {CodeNotFound, CodeBusy, CodeDBUnavailable, CodeInternal},
	OpClaimIssue: {
		CodeInvalidArgument, CodeNotFound, CodeAlreadyClaimed, CodeNotClaimable,
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

// ClassifyError maps an error from the storage seam onto the wire. The caller
// is responsible for logging err: everything mapped to a 5xx deliberately
// drops the error text on the floor (see staticDetail).
func ClassifyError(err error) Result {
	switch {
	case err == nil:
		return newResult(CodeInternal, "")

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
