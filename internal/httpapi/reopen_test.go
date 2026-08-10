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

// TestReopenPublishesNoPolicyConflictCode is the absence, asserted, and it is
// narrower than it used to be for a reason worth stating rather than quietly
// relaxing.
//
// It once said reopen publishes NO 409 at all. That claim died the moment this
// operation took `expected_version`: a compare-and-set refusal is a statement
// about the request's own premise, which every write can have wrong, and it has
// nothing to do with whether the graph can refuse a reopen. What survives — and
// what the operation's whole posture rests on — is that no POLICY conflict is
// reachable here: reopen takes an issue OUT of the done category, so there is
// no state of the graph to refuse it and `not_closable` must never appear.
//
// Written as an exact set rather than "no conflicts", so a future policy code
// added to this row fails here and has to be landed deliberately.
func TestReopenPublishesNoPolicyConflictCode(t *testing.T) {
	codes, ok := operationCodes[OpReopenIssue]
	if !ok {
		t.Fatalf("no %s row in the handler table", OpReopenIssue)
	}
	var conflicts []Code
	for _, c := range codes {
		if c.Status() == http.StatusConflict {
			conflicts = append(conflicts, c)
		}
	}
	if len(conflicts) != 1 || conflicts[0] != CodePreconditionFailed {
		t.Errorf("reopen's conflict codes are %v, want exactly [%s]: the caller's own guard is the only thing that can refuse a reopen, and a POLICY conflict here would mean the graph had been given a say it does not have",
			conflicts, CodePreconditionFailed)
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

// TestReopenForwardsTheVersionGuard is the close's case on the mirror: the
// member reaches the role as the POINTER it models, and an absent member stays
// nil so "do not check" never collapses into a guard on the never-written
// version 0.
func TestReopenForwardsTheVersionGuard(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want *int64
	}{
		{"a guard is forwarded", `{"actor":"alice","expected_version":9007199254740993}`, guard(guardToken)},
		{"the never-written version is a real guard", `{"actor":"alice","expected_version":0}`, guard(0)},
		{"an absent guard stays nil", `{"actor":"alice"}`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lifecycle := &roleLifecycle{reopenResult: issueops.ReopenResult{Issue: reopenedIssue("bd-1"), Changed: true}}
			ts := newReopenServer(t, lifecycle)

			resp := ts.reopenIssue(t, reopenPath, tc.body)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
			}
			got := lifecycle.reopenRequests()
			if len(got) != 1 {
				t.Fatalf("%d reopens, want 1", len(got))
			}
			switch {
			case tc.want == nil && got[0].ExpectedVersion != nil:
				t.Errorf("ExpectedVersion = %d, want nil", *got[0].ExpectedVersion)
			case tc.want != nil && got[0].ExpectedVersion == nil:
				t.Errorf("ExpectedVersion = nil, want %d", *tc.want)
			case tc.want != nil && *got[0].ExpectedVersion != *tc.want:
				t.Errorf("ExpectedVersion = %d, want %d", *got[0].ExpectedVersion, *tc.want)
			}
			// The history label is still spelled here; a new member must not
			// have displaced it.
			if got[0].Provenance != reopenProvenance {
				t.Errorf("Provenance = %q, want %q", got[0].Provenance, reopenProvenance)
			}
		})
	}
}

// TestReopenAnswersWithTheRowsRevision is the close's case on the mirror, and
// the value is asserted against the ROW the role answered with rather than
// against whatever the handler put there — the assertion that stops this from
// being a case that cannot fail.
//
// The `already_open` no-op carries a revision too: a recovery flow that guards
// its replay needs the token whether or not the replay wrote.
func TestReopenAnswersWithTheRowsRevision(t *testing.T) {
	for _, tc := range []struct {
		name    string
		changed bool
	}{
		{"a reopen that wrote", true},
		{"an issue that was never done", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			issue := reopenedIssue("bd-1")
			issue.RowVersion = guardToken
			lifecycle := &roleLifecycle{reopenResult: issueops.ReopenResult{Issue: issue, Changed: tc.changed}}
			ts := newReopenServer(t, lifecycle)

			resp := ts.reopenIssue(t, reopenPath, `{"actor":"alice"}`)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
			}
			if got := revisionOf(t, resp); got != guardToken {
				t.Errorf("revision = %d, want the row's %d", got, guardToken)
			}
		})
	}
}

// TestReopenRefusesAStaleGuard is this operation's ONLY 409, and it is the case
// failReopen exists for.
//
// Before this slice the handler answered every reopen failure through failErr,
// and ClassifyError has no row for ErrVersionMismatch — so the arm's absence is
// a 500 rather than a worse 409. Mutation-checked: deleting the arm makes this
// case fail with 500.
func TestReopenRefusesAStaleGuard(t *testing.T) {
	lifecycle := &roleLifecycle{reopenErr: fmt.Errorf("reopen bd-1: %w", issueops.ErrVersionMismatch)}
	ts := newReopenServer(t, lifecycle)

	resp := ts.reopenIssue(t, reopenPath, `{"actor":"alice","expected_version":9007199254740993}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if body["code"] != string(CodePreconditionFailed) {
		t.Errorf("code = %v, want %s", body["code"], CodePreconditionFailed)
	}
	if body["param"] != expectedVersionMember {
		t.Errorf("param = %v, want %s", body["param"], expectedVersionMember)
	}
	if _, present := body["expected_version"]; !present {
		t.Errorf("the refusal does not echo the guard the request sent: %v", body)
	}
	if _, present := body["actual_version"]; present {
		t.Errorf("actual_version = %v; the refusing transaction rolled back", body["actual_version"])
	}
}

// TestReopenRefusesAMalformedGuard: the token is an integer and nothing else,
// refused at the edge before any database work.
func TestReopenRefusesAMalformedGuard(t *testing.T) {
	for _, body := range []string{
		`{"actor":"alice","expected_version":"41"}`,
		`{"actor":"alice","expected_version":null}`,
		`{"actor":"alice","expected_version":[]}`,
	} {
		lifecycle := &roleLifecycle{reopenResult: issueops.ReopenResult{Issue: reopenedIssue("bd-1")}}
		ts := newReopenServer(t, lifecycle)

		resp := ts.reopenIssue(t, reopenPath, body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("body %s: status = %d, want 400: %s", body, resp.StatusCode, readAll(t, resp))
			continue
		}
		if problem := decodeBody(t, resp); problem["param"] != expectedVersionMember {
			t.Errorf("body %s: param = %v, want %s", body, problem["param"], expectedVersionMember)
		}
		if calls := lifecycle.reopenRequests(); len(calls) != 0 {
			t.Errorf("body %s: %d reopens reached the role", body, len(calls))
		}
	}
}
