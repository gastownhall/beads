package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The close's test header applies unchanged: these are pure, they run the
// reopen path end to end over a real listener against a fake ROLE, and what a
// fake cannot prove — the close→reopen round trip clearing close_reason and
// closed_by_session, and the reason landing on the issue's event stream — lives
// in cmd/bd's TestProxiedServerServeClose against real Dolt.

const reopenPath = "/v0/beads/issues/bd-1:reopen"

func (ts *testServer) reopenIssue(t *testing.T, path, body string) *http.Response {
	t.Helper()
	return ts.postBody(t, path, "application/json", body)
}

func newReopenServer(t *testing.T, lifecycle *roleLifecycle) *testServer {
	t.Helper()
	return newTestServer(t, rolesConfig(Config{Lifecycle: lifecycle}))
}

func reopenedIssue(id string) *types.Issue {
	return seededIssue(id, "alice", types.StatusOpen)
}

// TestReopenWritesOnceAndAnswersWithTheRowItWrote is the happy path, plus the
// one thing this operation adds that the close does not: the handler names the
// history entry, so an entry reads the same whichever backend served it.
func TestReopenWritesOnceAndAnswersWithTheRowItWrote(t *testing.T) {
	lifecycle := &roleLifecycle{reopenResult: issueops.ReopenResult{Issue: reopenedIssue("bd-1"), Changed: true}}
	ts := newReopenServer(t, lifecycle)

	resp := ts.reopenIssue(t, reopenPath, `{"actor":"alice","reason":"the fix regressed"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}

	want := issueops.ReopenRequest{
		Actor: "alice", IssueID: "bd-1", Reason: "the fix regressed", Provenance: reopenProvenance,
	}
	if got := lifecycle.reopenRequests(); len(got) != 1 || got[0] != want {
		t.Fatalf("the role received %+v, want exactly one %+v", got, want)
	}

	body := decodeBody(t, resp)
	if body["already_open"] != false {
		t.Errorf("already_open = %v, want false on a reopen that changed the row", body["already_open"])
	}
	issue, ok := body["issue"].(map[string]any)
	if !ok {
		t.Fatalf("issue = %#v, want an object", body["issue"])
	}
	if issue["status"] != string(types.StatusOpen) {
		t.Errorf("the response does not carry the reopened row: %v", issue)
	}
	// The reason is recorded on the EVENT, not on a field of the issue, and the
	// document says so — publishing it here would tell a client to read it back
	// from the wrong place.
	if _, present := body["reason"]; present {
		t.Errorf("the response carries the reopen reason; it belongs on the event stream: %v", body)
	}
}

// TestReopenNamesItsHistoryEntry pins the provenance label as a value rather
// than leaving it to the role's default. The implementations do not agree on
// that default — "bd: reopen issue" against "reopen issue" — so a workspace
// served by one backend today and another tomorrow would grow two spellings of
// one event.
func TestReopenNamesItsHistoryEntry(t *testing.T) {
	lifecycle := &roleLifecycle{reopenResult: issueops.ReopenResult{Issue: reopenedIssue("bd-1"), Changed: true}}
	ts := newReopenServer(t, lifecycle)

	if resp := ts.reopenIssue(t, reopenPath, `{"actor":"alice"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	got := lifecycle.reopenRequests()
	if len(got) != 1 {
		t.Fatalf("the role was called %d times, want 1", len(got))
	}
	if got[0].Provenance == "" {
		t.Fatal("the reopen carries no provenance, so the history entry reads differently per backend")
	}
	if !strings.Contains(got[0].Provenance, "serve") {
		t.Errorf("provenance = %q; it is meant to name THIS surface", got[0].Provenance)
	}
}

// TestReopenIsIdempotentForAnIssueThatWasNeverDone is the operation's whole
// idempotency story, and the reason it has no 409: a reopen of a non-done issue
// changes nothing and SUCCEEDS.
func TestReopenIsIdempotentForAnIssueThatWasNeverDone(t *testing.T) {
	lifecycle := &roleLifecycle{reopenResult: issueops.ReopenResult{Issue: reopenedIssue("bd-1"), Changed: false}}
	ts := newReopenServer(t, lifecycle)

	resp := ts.reopenIssue(t, reopenPath, `{"actor":"alice"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if body["already_open"] != true {
		t.Errorf("already_open = %v, want true for an issue that was never done", body["already_open"])
	}
	if _, ok := body["issue"].(map[string]any); !ok {
		t.Errorf("the idempotent answer dropped the row: %v", body)
	}
}

// TestReopenPublishesNoConflictCode is the absence, asserted. The operation's
// row says it has no 409 and the spec documents none; this is the third leg —
// nothing in the frozen set for this operation is a conflict, so a future
// change that gave reopen a policy guard has to land its code deliberately.
func TestReopenPublishesNoConflictCode(t *testing.T) {
	codes, ok := operationCodes[OpReopenIssue]
	if !ok {
		t.Fatalf("no %s row in the handler table", OpReopenIssue)
	}
	for _, c := range codes {
		if c.Status() == http.StatusConflict {
			t.Errorf("reopen publishes the conflict code %q; it has no policy guard, so there is nothing to refuse", c)
		}
	}
}

// TestReopenUnknownIDIs404 keeps the miss on the shape the document already
// describes.
func TestReopenUnknownIDIs404(t *testing.T) {
	lifecycle := &roleLifecycle{reopenErr: fmt.Errorf("reopen bd-9: %w", issueops.ErrNotFound)}
	ts := newReopenServer(t, lifecycle)

	resp := ts.reopenIssue(t, "/v0/beads/issues/bd-9:reopen", `{"actor":"alice"}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", resp.StatusCode, readAll(t, resp))
	}
	if body := decodeBody(t, resp); body["code"] != string(CodeNotFound) {
		t.Errorf("code = %v, want %s", body["code"], CodeNotFound)
	}
}

// TestReopenRejectsTheShapesTheDocumentRefuses is the body vocabulary, refused
// at the wire edge with nothing reaching the role. `session` and `force` are
// here as REFUSALS: they are the close's members, and a client that sends them
// to this operation is told so by name rather than having them ignored.
func TestReopenRejectsTheShapesTheDocumentRefuses(t *testing.T) {
	for _, tc := range []struct {
		name  string
		body  string
		param string
	}{
		{"no actor", `{"reason":"x"}`, "actor"},
		{"blank actor", `{"actor":"   "}`, "actor"},
		{"actor with a newline", "{\"actor\":\"alice\\nbd: reopen bd-1 by mallory\"}", "actor"},
		{"null actor", `{"actor":null}`, "actor"},
		{"oversize actor", `{"actor":"` + strings.Repeat("x", 300) + `"}`, "actor"},
		{"the close's session member", `{"actor":"alice","session":"s"}`, "session"},
		{"the close's force member", `{"actor":"alice","force":true}`, "force"},
		{"unknown member", `{"actor":"alice","cascade":true}`, "cascade"},
		{"reason is not a string", `{"actor":"alice","reason":7}`, "reason"},
		{"null reason", `{"actor":"alice","reason":null}`, "reason"},
		{"oversize reason", `{"actor":"alice","reason":"` + strings.Repeat("x", 300) + `"}`, "reason"},
		{"reason with a control character", "{\"actor\":\"alice\",\"reason\":\"why\\nbd: forged\"}", "reason"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lifecycle := &roleLifecycle{reopenResult: issueops.ReopenResult{Issue: reopenedIssue("bd-1")}}
			ts := newReopenServer(t, lifecycle)

			resp := ts.reopenIssue(t, reopenPath, tc.body)
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
			if got := lifecycle.reopenRequests(); len(got) != 0 {
				t.Errorf("a refused body reached the role: %+v", got)
			}
		})
	}
}

// TestReopenRefusesAForeignMediaType and TestReopenRefusesAQueryParameter: the
// two document-level rules, on the newest write.
func TestReopenRefusesAForeignMediaType(t *testing.T) {
	lifecycle := &roleLifecycle{}
	ts := newReopenServer(t, lifecycle)

	resp := ts.postBody(t, reopenPath, "text/plain", `{"actor":"alice"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	if body := decodeBody(t, resp); body["param"] != "Content-Type" {
		t.Errorf("param = %v, want Content-Type", body["param"])
	}
	if got := lifecycle.reopenRequests(); len(got) != 0 {
		t.Errorf("a foreign media type reached the role: %+v", got)
	}
}

func TestReopenRefusesAQueryParameter(t *testing.T) {
	lifecycle := &roleLifecycle{}
	ts := newReopenServer(t, lifecycle)

	resp := ts.reopenIssue(t, reopenPath+"?reason=x", `{"actor":"alice"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	if body := decodeBody(t, resp); body["reason"] != string(ReasonUnknownParameter) {
		t.Errorf("reason = %v, want %s", body["reason"], ReasonUnknownParameter)
	}
	if got := lifecycle.reopenRequests(); len(got) != 0 {
		t.Errorf("a query string reached the role: %+v", got)
	}
}

// TestReopenRefusesALifecycleThatAnswersWithNothing is checkedLifecycle.Reopen's
// reason for existing, the close's hazard on the mirror operation.
func TestReopenRefusesALifecycleThatAnswersWithNothing(t *testing.T) {
	lifecycle := &roleLifecycle{reopenResult: issueops.ReopenResult{Changed: true}}
	ts := newReopenServer(t, lifecycle)

	resp := ts.reopenIssue(t, reopenPath, `{"actor":"alice"}`)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", resp.StatusCode, readAll(t, resp))
	}
	if body := decodeBody(t, resp); body["code"] != string(CodeInternal) {
		t.Errorf("code = %v, want %s", body["code"], CodeInternal)
	}
	if !strings.Contains(ts.stderr.String(), "request_error") {
		t.Errorf("a broken role produced no request_error line:\n%s", ts.stderr.String())
	}
}

// TestReopenPathReachesItsHandler drives the path the DOCUMENT spells, and
// proves the dispatcher hands it to THIS operation rather than to one of the
// two rows already sharing the pattern.
func TestReopenPathReachesItsHandler(t *testing.T) {
	lifecycle := &roleLifecycle{reopenResult: issueops.ReopenResult{Issue: reopenedIssue("bd-1"), Changed: true}}
	ts := newReopenServer(t, lifecycle)

	if resp := ts.reopenIssue(t, reopenPath, `{"actor":"alice"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("POST the documented reopen path: status = %d, want 200", resp.StatusCode)
	}
	line := findLogLine(t, ts.stderr.String(), "path="+reopenPath)
	if !strings.Contains(line, "op="+OpReopenIssue) {
		t.Errorf("the documented reopen path is served by another operation:\n%s", line)
	}
	// And it reached the reopen verb, not the close's — the dispatcher chooses
	// by suffix, and a mis-set customMethod would land here silently.
	if got := lifecycle.closeRequests(); len(got) != 0 {
		t.Errorf("the reopen path reached the close verb: %+v", got)
	}
}

// TestReopenTakesADatabaseSlot: no write on this surface is exempt.
func TestReopenTakesADatabaseSlot(t *testing.T) {
	for _, rt := range routeTable {
		if rt.op != OpReopenIssue {
			continue
		}
		if rt.bypassSemaphore {
			t.Error("the reopen route bypasses the database semaphore; only handlers that touch no database may")
		}
		if !rt.implemented {
			t.Error("the reopen route is still marked unimplemented, so capabilities will not advertise it")
		}
		return
	}
	t.Fatalf("no %s row in the route table", OpReopenIssue)
}

// TestCloseAndReopenAreDistinctOnOnePattern is the pair test the dispatcher
// makes possible and therefore has to earn: three rows now share one ServeMux
// registration, and each documented path must reach its own verb.
func TestCloseAndReopenAreDistinctOnOnePattern(t *testing.T) {
	lifecycle := &roleLifecycle{
		closeResult:  issueops.CloseResult{Issue: closedIssue("bd-1"), Changed: true},
		reopenResult: issueops.ReopenResult{Issue: reopenedIssue("bd-1"), Changed: true},
	}
	ts := newReopenServer(t, lifecycle)

	if resp := ts.closeIssue(t, closePath, `{"actor":"alice","reason":"done"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("close: status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	if resp := ts.reopenIssue(t, reopenPath, `{"actor":"alice","reason":"not done"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("reopen: status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}

	closes, reopens := lifecycle.closeRequests(), lifecycle.reopenRequests()
	if len(closes) != 1 || closes[0].Reason != "done" {
		t.Errorf("the close verb received %+v, want exactly the close's body", closes)
	}
	if len(reopens) != 1 || reopens[0].Reason != "not done" {
		t.Errorf("the reopen verb received %+v, want exactly the reopen's body", reopens)
	}
}

// TestReopenResolvesAcrossBothPlanes, as the close does and the claim
// deliberately does not.
func TestReopenResolvesAcrossBothPlanes(t *testing.T) {
	wisp := seededIssue("bd-w1", "alice", types.StatusOpen)
	lifecycle := &roleLifecycle{reopenResult: issueops.ReopenResult{Issue: wisp, Changed: true}}
	ts := newReopenServer(t, lifecycle)

	resp := ts.reopenIssue(t, "/v0/beads/issues/bd-w1:reopen", `{"actor":"alice"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reopening a wisp id: status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	if got := lifecycle.reopenRequests(); len(got) != 1 || got[0].IssueID != "bd-w1" {
		t.Fatalf("the role received %+v, want the wisp id unchanged", got)
	}
}
