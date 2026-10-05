// Written fresh for OSS beads S2 (no bd-enterprise source copied).
package wire

import (
	"net/http"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
)

// serveLegacyServer fakes a server old enough to predate #6053: its context
// response omits wire_revision entirely (contextBodyWithWireRevision's 0
// renders it omitted, matching ClientMinWireRevision's doc), and its write
// responses still carry `revision` as a bare JSON integer rather than the
// decimal-string shape every apigen response type now declares.
func serveLegacyServer(t *testing.T, readyBody, closeBody string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == PathContext:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(contextBodyWithWireRevision("v0", "", 0, 0, "issues.list", "ready.list", "issues.close")))
		case r.URL.Path == PathReady:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(readyBody))
		case strings.HasSuffix(r.URL.Path, MethodClose):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(closeBody))
		default:
			problemJSON(w, http.StatusNotFound, `{"status":404,"code":"not_found"}`)
		}
	}
}

// TestListReadyWorkAgainstAPreSixZeroFiveThreeServer is the read half of HIGH
// #2's coverage: ready.list's response carries no `revision` member at all, so
// a server old enough to omit wire_revision from its handshake must not need
// any tolerance here — this pins that the baseline read path is simply
// unaffected by the legacy-server detection this file adds.
func TestListReadyWorkAgainstAPreSixZeroFiveThreeServer(t *testing.T) {
	c, _ := newTestClient(t, Options{}, nil,
		serveLegacyServer(t, `{"has_more":false,"items":[]}`, `{"revision":42}`))

	page, err := c.ListReadyWork(ctx(t), nil)
	if err != nil {
		t.Fatalf("ListReadyWork against a pre-#6053 server: %v", err)
	}
	if page.HasMore || len(page.Items) != 0 {
		t.Fatalf("ListReadyWork: got %+v, want an empty page", page)
	}
}

// TestCloseIssueAgainstAPreSixZeroFiveThreeServer is the guarded-write half:
// CloseIssueRequest carries ExpectedVersion, and the fake server answers the
// close with a bare-integer `revision` — the shape a server that predates
// #6053 actually sends, and the shape apigen.CloseIssueResponse.Revision
// (declared `string`) could not decode before this file's fix. Before the
// fix this failed with an untyped json.UnmarshalTypeError; this pins that it
// now decodes, carrying the token's exact decimal digits.
func TestCloseIssueAgainstAPreSixZeroFiveThreeServer(t *testing.T) {
	expected := "7"
	c, rec := newTestClient(t, Options{}, nil,
		serveLegacyServer(t, `{"has_more":false,"items":[]}`, `{"revision":99}`))

	resp, err := c.CloseIssue(ctx(t), "be-1", apigen.CloseIssueRequest{
		Actor:           "agent-1",
		ExpectedVersion: &expected,
	})
	if err != nil {
		t.Fatalf("CloseIssue against a pre-#6053 server: %v", err)
	}
	if resp.Revision != "99" {
		t.Errorf("Revision = %q, want \"99\" (the bare integer's exact decimal spelling)", resp.Revision)
	}
	if rec.count() != 2 {
		t.Fatalf("expected one context request and one close request, got %d requests", rec.count())
	}
}

// TestCloseIssueAgainstAModernServerIsUntouched proves the rewrite never fires
// against a server that already sends the decimal-string shape: the handshake
// carries a real wire_revision, so serverPredatesRevisionStrings is false and
// tolerateLegacyRevisionNumbers never runs.
func TestCloseIssueAgainstAModernServerIsUntouched(t *testing.T) {
	expected := "7"
	c, _ := newTestClient(t, Options{}, nil, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == PathContext:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(contextBodyWithWireRevision("v0", "", ClientWireRevision, 0, "issues.close")))
		case strings.HasSuffix(r.URL.Path, MethodClose):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"revision":"99"}`))
		default:
			problemJSON(w, http.StatusNotFound, `{"status":404,"code":"not_found"}`)
		}
	})

	resp, err := c.CloseIssue(ctx(t), "be-1", apigen.CloseIssueRequest{
		Actor:           "agent-1",
		ExpectedVersion: &expected,
	})
	if err != nil {
		t.Fatalf("CloseIssue against a modern server: %v", err)
	}
	if resp.Revision != "99" {
		t.Errorf("Revision = %q, want \"99\"", resp.Revision)
	}
}
