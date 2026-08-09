package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// These are pure: the close path runs end to end over a real listener against a
// fake ROLE, so the wire edge — path split, media type, body rules, actor and
// text rules, response and problem shapes — is covered on every pull request by
// the PR workflow's unconditional Go test job. What a fake cannot prove is the
// transaction: first-close-wins read back through a second close, and a forced
// close over real open children, live in cmd/bd's proxied-server integration
// test against real Dolt (TestProxiedServerServeClose). The role-level
// transition is owned against a real store by
// internal/storage/uow/lifecycle_close_reopen_contract_test.go, which this
// slice cites rather than duplicates.

const closePath = "/v0/beads/issues/bd-1:close"

func (ts *testServer) closeIssue(t *testing.T, path, body string) *http.Response {
	t.Helper()
	return ts.postBody(t, path, "application/json", body)
}

// newCloseServer wires a server over the store-shaped source with a lifecycle
// role the case controls. Every other role is a placeholder: Listen refuses a
// partial source, and a close reaches none of them.
func newCloseServer(t *testing.T, lifecycle *roleLifecycle) *testServer {
	t.Helper()
	return newTestServer(t, rolesConfig(Config{Lifecycle: lifecycle}))
}

func closedIssue(id string) *types.Issue {
	issue := seededIssue(id, "alice", types.StatusClosed)
	issue.CloseReason = "shipped"
	issue.ClosedBySession = "session-7"
	return issue
}

// TestCloseWritesOnceAndAnswersWithTheRowItWrote is the happy path and the two
// things a client depends on: the role receives exactly what the body asked
// for, and the response carries the issue as it stands after the close.
func TestCloseWritesOnceAndAnswersWithTheRowItWrote(t *testing.T) {
	lifecycle := &roleLifecycle{closeResult: issueops.CloseResult{Issue: closedIssue("bd-1"), Changed: true}}
	ts := newCloseServer(t, lifecycle)

	resp := ts.closeIssue(t, closePath, `{"actor":"alice","reason":"shipped","session":"session-7"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}

	want := issueops.CloseRequest{Actor: "alice", IssueID: "bd-1", Reason: "shipped", Session: "session-7"}
	if got := lifecycle.closeRequests(); len(got) != 1 || got[0] != want {
		t.Fatalf("the role received %+v, want exactly one %+v", got, want)
	}

	body := decodeBody(t, resp)
	if body["already_closed"] != false {
		t.Errorf("already_closed = %v, want false on a close that changed the row", body["already_closed"])
	}
	// An unforced close that got this far had no open children, and the
	// document promises the member is present rather than omitted.
	if body["open_children"] != float64(0) {
		t.Errorf("open_children = %v, want 0", body["open_children"])
	}
	issue, ok := body["issue"].(map[string]any)
	if !ok {
		t.Fatalf("issue = %#v, want an object", body["issue"])
	}
	if issue["status"] != string(types.StatusClosed) || issue["close_reason"] != "shipped" {
		t.Errorf("the response does not carry the closed row: %v", issue)
	}
}

// TestCloseIsIdempotentForAnAlreadyClosedIssue pins the wire's name for the
// role's unchanged result. It is the re-claim's answer to the same question: an
// agent replaying its own recovery should not have to classify an error to
// learn it already ran.
func TestCloseIsIdempotentForAnAlreadyClosedIssue(t *testing.T) {
	lifecycle := &roleLifecycle{closeResult: issueops.CloseResult{Issue: closedIssue("bd-1"), Changed: false}}
	ts := newCloseServer(t, lifecycle)

	resp := ts.closeIssue(t, closePath, `{"actor":"alice","reason":"a second opinion"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if body["already_closed"] != true {
		t.Errorf("already_closed = %v, want true for a re-close", body["already_closed"])
	}
	// The response still carries the row, and the row still carries what the
	// FIRST close wrote — the second reason never reached a column.
	issue, ok := body["issue"].(map[string]any)
	if !ok {
		t.Fatalf("issue = %#v, want an object", body["issue"])
	}
	if issue["close_reason"] != "shipped" {
		t.Errorf("close_reason = %v; a re-close must not rewrite the record of why the work ended", issue["close_reason"])
	}
}

// TestCloseForwardsForceAndReportsWhatItBypassed: `force` is the ROLE's bypass,
// so the handler's whole job is to pass it through — and to publish the count
// the role reports, which is exactly what a caller who forced past the guard
// wants to know.
func TestCloseForwardsForceAndReportsWhatItBypassed(t *testing.T) {
	lifecycle := &roleLifecycle{closeResult: issueops.CloseResult{
		Issue: closedIssue("bd-1"), Changed: true, OpenChildren: 3,
	}}
	ts := newCloseServer(t, lifecycle)

	resp := ts.closeIssue(t, closePath, `{"actor":"alice","force":true}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	if got := lifecycle.closeRequests(); len(got) != 1 || !got[0].Force {
		t.Fatalf("the role received %+v, want Force set", got)
	}
	if body := decodeBody(t, resp); body["open_children"] != float64(3) {
		t.Errorf("open_children = %v, want the 3 the role reported", body["open_children"])
	}
}

// TestCloseOpenChildrenIsATypedConflict is the whole reason not_closable exists
// as a code and open_children as a member: a client classifies the refusal and
// reads the count without substring-matching "3 open child issue(s)".
func TestCloseOpenChildrenIsATypedConflict(t *testing.T) {
	lifecycle := &roleLifecycle{closeErr: fmt.Errorf("close bd-1: %w",
		&issueops.CloseOpenChildrenError{IssueID: "bd-1", OpenChildren: 3})}
	ts := newCloseServer(t, lifecycle)

	resp := ts.closeIssue(t, closePath, `{"actor":"alice"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if body["code"] != string(CodeNotClosable) {
		t.Errorf("code = %v, want %s", body["code"], CodeNotClosable)
	}
	if body["open_children"] != float64(3) {
		t.Errorf("open_children = %v, want the count the refusing transaction observed", body["open_children"])
	}
}

// TestCloseBlockedIsTheSameCodeWithoutTheCount is the other half of the
// discriminator the document promises. Both refusals are `not_closable` and
// both are bypassed by `force`; only one can say how many children it saw, and
// MEMBER PRESENCE is how a client tells them apart.
func TestCloseBlockedIsTheSameCodeWithoutTheCount(t *testing.T) {
	lifecycle := &roleLifecycle{closeErr: fmt.Errorf("close bd-1: %w", issueops.ErrCloseBlocked)}
	ts := newCloseServer(t, lifecycle)

	resp := ts.closeIssue(t, closePath, `{"actor":"alice"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if body["code"] != string(CodeNotClosable) {
		t.Errorf("code = %v, want %s", body["code"], CodeNotClosable)
	}
	if _, present := body["open_children"]; present {
		t.Errorf("the blocker refusal reported open_children, which is the other refusal's discriminator: %v", body)
	}
}

// TestCloseUnknownIDIs404 keeps the miss on the shape the document already
// describes rather than inventing one for this operation.
func TestCloseUnknownIDIs404(t *testing.T) {
	lifecycle := &roleLifecycle{closeErr: fmt.Errorf("close bd-9: %w", issueops.ErrNotFound)}
	ts := newCloseServer(t, lifecycle)

	resp := ts.closeIssue(t, "/v0/beads/issues/bd-9:close", `{"actor":"alice"}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", resp.StatusCode, readAll(t, resp))
	}
	if body := decodeBody(t, resp); body["code"] != string(CodeNotFound) {
		t.Errorf("code = %v, want %s", body["code"], CodeNotFound)
	}
}

// TestCloseRejectsTheShapesTheDocumentRefuses is the body vocabulary, refused
// at the wire edge. Every case asserts that NOTHING reached the role: a 400
// this handler can answer is a 400 no transaction should be opened for.
func TestCloseRejectsTheShapesTheDocumentRefuses(t *testing.T) {
	for _, tc := range []struct {
		name  string
		body  string
		param string
	}{
		{"no actor", `{"reason":"x"}`, "actor"},
		{"blank actor", `{"actor":"   "}`, "actor"},
		{"actor with a newline", "{\"actor\":\"alice\\nbd: close bd-1 by mallory\"}", "actor"},
		{"actor with a C1 control character", `{"actor":"alice\u0085bd: close"}`, "actor"},
		{"null actor", `{"actor":null}`, "actor"},
		{"actor is not a string", `{"actor":7}`, "actor"},
		{"oversize actor", `{"actor":"` + strings.Repeat("x", 300) + `"}`, "actor"},
		{"unknown member", `{"actor":"alice","cascade":true}`, "cascade"},
		{"reason is not a string", `{"actor":"alice","reason":7}`, "reason"},
		// `null` is refused rather than read as a clear: absence already means
		// "not supplied", so a null would publish a third state with no meaning.
		{"null reason", `{"actor":"alice","reason":null}`, "reason"},
		{"oversize reason", `{"actor":"alice","reason":"` + strings.Repeat("x", 300) + `"}`, "reason"},
		{"reason with a control character", "{\"actor\":\"alice\",\"reason\":\"done\\nbd: forged\"}", "reason"},
		{"session is not a string", `{"actor":"alice","session":[]}`, "session"},
		{"null session", `{"actor":"alice","session":null}`, "session"},
		{"session with a control character", "{\"actor\":\"alice\",\"session\":\"s\\u0000\"}", "session"},
		{"force is not a boolean", `{"actor":"alice","force":"yes"}`, "force"},
		{"null force", `{"actor":"alice","force":null}`, "force"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lifecycle := &roleLifecycle{closeResult: issueops.CloseResult{Issue: closedIssue("bd-1")}}
			ts := newCloseServer(t, lifecycle)

			resp := ts.closeIssue(t, closePath, tc.body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if body["code"] != string(CodeInvalidArgument) {
				t.Errorf("code = %v, want %s", body["code"], CodeInvalidArgument)
			}
			if body["param"] != tc.param {
				t.Errorf("param = %v, want %q", body["param"], tc.param)
			}
			if got := lifecycle.closeRequests(); len(got) != 0 {
				t.Errorf("a refused body reached the role: %+v", got)
			}
		})
	}
}

// TestCloseRefusesABodyItCannotRead covers the refusals with no nameable
// member, where `param` is documented absent.
func TestCloseRefusesABodyItCannotRead(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"not an object", `["actor"]`},
		{"a bare null", `null`},
		{"trailing garbage", `{"actor":"alice"} {"actor":"mallory"}`},
		{"unparseable", `{`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lifecycle := &roleLifecycle{}
			ts := newCloseServer(t, lifecycle)

			resp := ts.closeIssue(t, closePath, tc.body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if body["code"] != string(CodeInvalidArgument) {
				t.Errorf("code = %v, want %s", body["code"], CodeInvalidArgument)
			}
			if _, present := body["param"]; present {
				t.Errorf("a body with no nameable part reported a param: %v", body)
			}
			if got := lifecycle.closeRequests(); len(got) != 0 {
				t.Errorf("a refused body reached the role: %+v", got)
			}
		})
	}
}

// TestCloseRefusesAForeignMediaType: the CSRF control, on the surface's newest
// write. A JSON content type is not CORS-simple, so a cross-origin close always
// triggers a preflight this server never approves.
func TestCloseRefusesAForeignMediaType(t *testing.T) {
	lifecycle := &roleLifecycle{}
	ts := newTestServer(t, rolesConfig(Config{Lifecycle: lifecycle}))

	resp := ts.postBody(t, closePath, "text/plain", `{"actor":"alice"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	if body := decodeBody(t, resp); body["param"] != "Content-Type" {
		t.Errorf("param = %v, want Content-Type", body["param"])
	}
	if got := lifecycle.closeRequests(); len(got) != 0 {
		t.Errorf("a foreign media type reached the role: %+v", got)
	}
}

// TestCloseRefusesAQueryParameter: this operation publishes none, so every key
// is the document-level unknown-parameter refusal.
func TestCloseRefusesAQueryParameter(t *testing.T) {
	lifecycle := &roleLifecycle{}
	ts := newCloseServer(t, lifecycle)

	resp := ts.closeIssue(t, closePath+"?force=true", `{"actor":"alice"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if body["reason"] != string(ReasonUnknownParameter) {
		t.Errorf("reason = %v, want %s", body["reason"], ReasonUnknownParameter)
	}
	if got := lifecycle.closeRequests(); len(got) != 0 {
		t.Errorf("a query string reached the role: %+v", got)
	}
}

// TestCloseRefusesALifecycleThatAnswersWithNothing is checkedLifecycle's
// reason for existing: handleClose dereferences the issue the role returned, so
// a role reporting success without one is a nil pointer panic on a live server
// unless it is folded into the generic 500 first.
func TestCloseRefusesALifecycleThatAnswersWithNothing(t *testing.T) {
	lifecycle := &roleLifecycle{closeResult: issueops.CloseResult{Changed: true}}
	ts := newCloseServer(t, lifecycle)

	resp := ts.closeIssue(t, closePath, `{"actor":"alice"}`)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", resp.StatusCode, readAll(t, resp))
	}
	if body := decodeBody(t, resp); body["code"] != string(CodeInternal) {
		t.Errorf("code = %v, want %s", body["code"], CodeInternal)
	}
	// The fault is in the log, which is the only place a 5xx detail may say
	// what happened.
	if !strings.Contains(ts.stderr.String(), "request_error") {
		t.Errorf("a broken role produced no request_error line:\n%s", ts.stderr.String())
	}
}

// TestClosePathReachesItsHandler drives the path the DOCUMENT spells, which is
// the one thing route parity cannot check for a custom-method row: it declares
// its spec path instead of deriving it from the pattern. The parity test bounds
// the shape of that exception; only a request proves the shared pattern
// actually serves the documented path — and serves it as THIS operation rather
// than as the claim that registered the pattern first.
func TestClosePathReachesItsHandler(t *testing.T) {
	lifecycle := &roleLifecycle{closeResult: issueops.CloseResult{Issue: closedIssue("bd-1"), Changed: true}}
	ts := newCloseServer(t, lifecycle)

	if resp := ts.closeIssue(t, closePath, `{"actor":"alice"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("POST the documented close path: status = %d, want 200", resp.StatusCode)
	}
	line := findLogLine(t, ts.stderr.String(), "path="+closePath)
	if !strings.Contains(line, "op="+OpCloseIssue) {
		t.Errorf("the documented close path is served by another operation:\n%s", line)
	}
}

// TestCloseTakesADatabaseSlot: the newest write is not exempt from the
// in-flight limit. An exempt write would keep opening connections while every
// reader is already queued — the saturation case the semaphore exists for.
func TestCloseTakesADatabaseSlot(t *testing.T) {
	for _, rt := range routeTable {
		if rt.op != OpCloseIssue {
			continue
		}
		if rt.bypassSemaphore {
			t.Error("the close route bypasses the database semaphore; only handlers that touch no database may")
		}
		if !rt.implemented {
			t.Error("the close route is still marked unimplemented, so capabilities will not advertise it")
		}
		return
	}
	t.Fatalf("no %s row in the route table", OpCloseIssue)
}

// TestCloseMapsARetryableRoleFailureOntoTheDocumentedRetry keeps the shared
// error mapping load-bearing for this operation: an exhausted retry budget is
// a 503 the client comes back from, never the generic 500.
func TestCloseMapsARetryableRoleFailureOntoTheDocumentedRetry(t *testing.T) {
	lifecycle := &roleLifecycle{closeErr: ErrBusy}
	ts := newCloseServer(t, lifecycle)

	resp := ts.closeIssue(t, closePath, `{"actor":"alice"}`)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", resp.StatusCode, readAll(t, resp))
	}
	if body := decodeBody(t, resp); body["code"] != string(CodeBusy) {
		t.Errorf("code = %v, want %s", body["code"], CodeBusy)
	}
	if got := resp.Header.Get("Retry-After"); got == "" {
		t.Error("a retryable refusal carries no Retry-After")
	}
}

// TestCloseTrimsTheActor pins that the value the role is handed is the trimmed
// one, not the caller's whitespace — the same rule the claim applies, applied
// by the same shared function.
func TestCloseTrimsTheActor(t *testing.T) {
	lifecycle := &roleLifecycle{closeResult: issueops.CloseResult{Issue: closedIssue("bd-1"), Changed: true}}
	ts := newCloseServer(t, lifecycle)

	if resp := ts.closeIssue(t, closePath, `{"actor":"  alice  "}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	if got := lifecycle.closeRequests(); len(got) != 1 || got[0].Actor != "alice" {
		t.Errorf("the role received actor %q, want the trimmed value", got[0].Actor)
	}
}

// TestCloseErrorsMapThroughTheSharedClassification pins the two close-policy
// sentinels onto one code at the mapping rather than at the handler, so any
// other operation that ever reaches them answers the same way.
func TestCloseErrorsMapThroughTheSharedClassification(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"open children", &issueops.CloseOpenChildrenError{IssueID: "bd-1", OpenChildren: 2}},
		{"the bare open-children sentinel", issueops.ErrCloseOpenChildren},
		{"a live blocker", issueops.ErrCloseBlocked},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := ClassifyError(fmt.Errorf("wrapped: %w", tc.err))
			if res.Problem.Status != http.StatusConflict || res.Problem.Code != string(CodeNotClosable) {
				t.Errorf("ClassifyError = %d/%s, want 409/%s", res.Problem.Status, res.Problem.Code, CodeNotClosable)
			}
			// The mapping never attaches the member — failClose does, from the
			// typed error's field — so a code-only classification stays silent.
			if res.Problem.OpenChildren != nil {
				t.Errorf("the shared mapping attached open_children: %v", *res.Problem.OpenChildren)
			}
		})
	}
}

// TestCloseIsNotReachableByOtherMethods: the dispatcher is registered for POST
// alone, so the documented path under any other method is an unrouted path.
//
// PATCH is excluded and has its own test below. `PATCH /v0/beads/issues/{id}`
// is a documented operation on a single-segment wildcard, so it MATCHES this
// path — as an update of an issue whose id happens to be "bd-1:close". That is
// the detail path's long-standing behavior (GET has always answered the same
// way), and what matters is that it never executes as a close.
func TestCloseIsNotReachableByOtherMethods(t *testing.T) {
	ts := newCloseServer(t, &roleLifecycle{})

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		req, err := http.NewRequest(method, ts.base+closePath, strings.NewReader(`{"actor":"alice"}`))
		if err != nil {
			t.Fatalf("new %s request: %v", method, err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := ts.client.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, closePath, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s %s: status = %d, want 404", method, closePath, resp.StatusCode)
		}
	}
}

// TestCloseRefusesUnrowableIDsBeforeAnyDatabaseWork: the id bound is the
// dispatcher's and it is shared, so it holds for this operation without this
// handler carrying a copy of it. The answer is the SAME 404 a real miss gets —
// a distinct refusal would let a caller map the server's notion of a
// well-formed id.
func TestCloseRefusesUnrowableIDsBeforeAnyDatabaseWork(t *testing.T) {
	for _, tc := range []struct{ name, id string }{
		{"longer than the column", strings.Repeat("b", types.MaxFieldLen+1)},
		{"carrying a control character", "bd-1%00"},
		{"carrying a C1 introducer", "bd-1%C2%9B"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lifecycle := &roleLifecycle{}
			ts := newCloseServer(t, lifecycle)

			resp := ts.closeIssue(t, "/v0/beads/issues/"+tc.id+":close", `{"actor":"alice"}`)
			if resp.StatusCode != http.StatusNotFound {
				t.Fatalf("status = %d, want 404: %s", resp.StatusCode, readAll(t, resp))
			}
			if got := lifecycle.closeRequests(); len(got) != 0 {
				t.Errorf("an unrowable id reached the role: %+v", got)
			}
		})
	}
}

// TestCloseBodyCapIsEnforcedWhileReading pins that the megabyte cap refuses
// before the whole body is buffered, which is what makes it a bound rather than
// a check.
func TestCloseBodyCapIsEnforcedWhileReading(t *testing.T) {
	lifecycle := &roleLifecycle{}
	ts := newCloseServer(t, lifecycle)

	oversize := `{"actor":"alice","reason":"` + strings.Repeat("x", maxJSONBodyBytes+1) + `"}`
	resp := ts.closeIssue(t, closePath, oversize)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	if got := lifecycle.closeRequests(); len(got) != 0 {
		t.Errorf("an oversize body reached the role: %+v", got)
	}
}

// TestFailCloseNeverParsesTheRefusalsProse is the rule the whole 409 exists to
// enforce, asserted at the seam: the member comes from the typed error's field,
// so an error whose MESSAGE mentions open children but whose type is not the
// typed one carries no member.
func TestFailCloseNeverParsesTheRefusalsProse(t *testing.T) {
	lifecycle := &roleLifecycle{closeErr: fmt.Errorf(
		"cannot close bd-1: 4 open child issue(s); close children first: %w", issueops.ErrCloseBlocked)}
	ts := newCloseServer(t, lifecycle)

	resp := ts.closeIssue(t, closePath, `{"actor":"alice"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", resp.StatusCode, readAll(t, resp))
	}
	if body := decodeBody(t, resp); body["open_children"] != nil {
		t.Errorf("open_children = %v; it was read out of the message rather than the typed field", body["open_children"])
	}
}

// TestCloseResolvesAcrossBothPlanes is the divergence from the claim, pinned:
// the claim addresses the issues table only, and this operation does not copy
// that. Nothing here decides the plane — the ROLE does — so what this asserts
// is that the handler publishes no IssuePlaneOnly-style narrowing of its own.
func TestCloseResolvesAcrossBothPlanes(t *testing.T) {
	wisp := seededIssue("bd-w1", "alice", types.StatusClosed)
	lifecycle := &roleLifecycle{closeResult: issueops.CloseResult{Issue: wisp, Changed: true}}
	ts := newCloseServer(t, lifecycle)

	resp := ts.closeIssue(t, "/v0/beads/issues/bd-w1:close", `{"actor":"alice"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("closing a wisp id: status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	if got := lifecycle.closeRequests(); len(got) != 1 || got[0].IssueID != "bd-w1" {
		t.Fatalf("the role received %+v, want the wisp id unchanged", got)
	}
}
