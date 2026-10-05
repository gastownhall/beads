// Written fresh for OSS beads S2 (no bd-enterprise source copied).
package wire

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
)

// serveLegacyServer fakes a server old enough to predate #6053: its context
// response omits wire_revision entirely (contextBodyWithWireRevision's 0
// renders it omitted, matching ClientMinWireRevision's doc), and its write
// and getIssue responses still carry `revision` as a bare JSON integer rather
// than the decimal-string shape every apigen response type now declares.
// getIssueBody may be "" for a test that never dispatches getIssue.
func serveLegacyServer(t *testing.T, readyBody, closeBody, getIssueBody string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == PathContext:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(contextBodyWithWireRevision("v0", "", 0, 0, "issues.list", "ready.list", "issues.close", "issues.get")))
		case r.URL.Path == PathReady:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(readyBody))
		case strings.HasSuffix(r.URL.Path, MethodClose):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(closeBody))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, PathIssues+"/") && getIssueBody != "":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(getIssueBody))
		default:
			problemJSON(w, http.StatusNotFound, `{"status":404,"code":"not_found"}`)
		}
	}
}

// getIssue dispatches getIssue directly: there is no GetIssue wrapper method
// on *Client yet (S3), but getIssue is a first-slice, known capability token
// (opCapability's "issues.get"), so Request/dispatch already support it.
func getIssue(t *testing.T, c *Client, id string) (*apigen.IssueDetails, error) {
	t.Helper()
	path, err := IssuePath(id)
	if err != nil {
		t.Fatalf("IssuePath: %v", err)
	}
	var out apigen.IssueDetails
	err = c.dispatch(ctx(t), Request{Op: OpGetIssue, Method: http.MethodGet, Path: path}, &out)
	return &out, err
}

// TestListReadyWorkAgainstAPreSixZeroFiveThreeServer is the read half of HIGH
// #2's coverage: ready.list's response carries no `revision` member at all, so
// a server old enough to omit wire_revision from its handshake must not need
// any tolerance here — this pins that the baseline read path is simply
// unaffected by the legacy-server detection this file adds.
func TestListReadyWorkAgainstAPreSixZeroFiveThreeServer(t *testing.T) {
	c, _ := newTestClient(t, Options{}, nil,
		serveLegacyServer(t, `{"has_more":false,"items":[]}`, `{"revision":42}`, ""))

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
// now decodes, carrying the token's exact decimal digits. CloseIssue is never
// a baseline op, so dispatch's Preflight forces the handshake ahead of Do —
// this is therefore also the "with a prior handshake" case.
func TestCloseIssueAgainstAPreSixZeroFiveThreeServer(t *testing.T) {
	expected := "7"
	c, rec := newTestClient(t, Options{}, nil,
		serveLegacyServer(t, `{"has_more":false,"items":[]}`, `{"revision":99}`, ""))

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

// TestGetIssueAgainstAPreSixZeroFiveThreeServerWithNoPriorHandshake is review
// follow-up HIGH-a's missing-type coverage, the no-handshake-cached half:
// getIssue is a BASELINE operation (baselineOps[OpGetIssue]), so Preflight
// never forces a handshake for it, and a client that has made no other call
// yet dispatches it with c.handshake.snap == nil. Before this fix,
// serverPredatesRevisionStrings answered false in exactly this state — "no
// evidence this is a legacy server" — so apigen.IssueDetails's bare-integer
// `revision` was never tolerated at all for a cold client's first call. It
// must now decode, because the gate treats "no snapshot" as "tolerate" and
// the rewrite itself is scoped (HIGH-b) to be safe either way.
func TestGetIssueAgainstAPreSixZeroFiveThreeServerWithNoPriorHandshake(t *testing.T) {
	c, rec := newTestClient(t, Options{}, nil,
		serveLegacyServer(t, "", "", `{"id":"be-1","revision":7}`))

	details, err := getIssue(t, c, "be-1")
	if err != nil {
		t.Fatalf("getIssue against a pre-#6053 server with no prior handshake: %v", err)
	}
	if details.Revision != "7" {
		t.Errorf("Revision = %q, want \"7\"", details.Revision)
	}
	if rec.count() != 1 {
		t.Fatalf("getIssue is baseline: expected exactly one request (no forced handshake), got %d", rec.count())
	}
}

// TestGetIssueAgainstAPreSixZeroFiveThreeServerWithPriorHandshake is the same
// coverage with a handshake already cached (forced here by a prior
// Handshake() call, the way a real session that already made a non-baseline
// call would arrive at a later getIssue): serverPredatesRevisionStrings now
// reads WireRevision == 0 off the cached snapshot instead of seeing no
// snapshot at all, and must still tolerate.
func TestGetIssueAgainstAPreSixZeroFiveThreeServerWithPriorHandshake(t *testing.T) {
	c, rec := newTestClient(t, Options{}, nil,
		serveLegacyServer(t, "", "", `{"id":"be-1","revision":7}`))

	if _, err := c.Handshake(ctx(t)); err != nil {
		t.Fatalf("Handshake: %v", err)
	}

	details, err := getIssue(t, c, "be-1")
	if err != nil {
		t.Fatalf("getIssue against a pre-#6053 server with a cached handshake: %v", err)
	}
	if details.Revision != "7" {
		t.Errorf("Revision = %q, want \"7\"", details.Revision)
	}
	if rec.count() != 2 {
		t.Fatalf("expected one context request and one getIssue request, got %d", rec.count())
	}
}

// TestCloseIssueLegacyRewriteNeverTouchesIssueMetadata is review follow-up
// HIGH-b's regression coverage. tolerateLegacyRevisionNumbers used to walk
// every nested object and array in the body looking for a bare-number
// "revision" key; that reached CloseIssueResponse.issue.metadata — a blob
// this client passes through opaquely for the caller, which may itself hold
// a member literally named "revision" at any depth as ordinary user data.
// The rewrite must now touch only the body's own top-level "revision", never
// descending into `issue` at all, so a metadata document with "revision" keys
// at several nesting depths comes back byte-for-byte identical to what the
// legacy server sent.
func TestCloseIssueLegacyRewriteNeverTouchesIssueMetadata(t *testing.T) {
	const metadata = `{"revision":3,"n":[{"revision":4}],"deep":{"inner":{"revision":5}}}`
	legacyBody := `{"already_closed":false,"issue":{"id":"be-1","metadata":` + metadata + `},"open_children":0,"revision":42}`

	expected := "7"
	c, _ := newTestClient(t, Options{}, nil,
		serveLegacyServer(t, "", legacyBody, ""))

	resp, err := c.CloseIssue(ctx(t), "be-1", apigen.CloseIssueRequest{
		Actor:           "agent-1",
		ExpectedVersion: &expected,
	})
	if err != nil {
		t.Fatalf("CloseIssue against a pre-#6053 server: %v", err)
	}
	if resp.Revision != "42" {
		t.Errorf("Revision = %q, want \"42\" (the top-level bare integer, rewritten)", resp.Revision)
	}
	if got := string(resp.Issue.Metadata); got != metadata {
		t.Errorf("Issue.Metadata = %s, want it byte-for-byte unchanged at %s (metadata must never be rewritten)", got, metadata)
	}
}

// TestApplyBatchLegacyItemRevisionsAreRewritten is ApplyBatchResponse's own
// half of HIGH #2: it carries no top-level `revision` of its own, but each of
// its `items` is an apigen.ApplyItemResult, which does — tolerateLegacyRevisionNumbers
// rewrites exactly that nested position (and nothing else nested) when out is
// *apigen.ApplyBatchResponse.
func TestApplyBatchLegacyItemRevisionsAreRewritten(t *testing.T) {
	legacyBody := `{"items":[` +
		`{"kind":"create","issue_id":"be-1","changed":true,"revision":10},` +
		`{"kind":"close","issue_id":"be-2","changed":true,"revision":11}` +
		`]}`

	c, _ := newTestClient(t, Options{}, nil, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == PathContext:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(contextBodyWithWireRevision("v0", "", 0, 0, "issues.batchApply")))
		case r.URL.Path == PathIssuesBatchApply:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(legacyBody))
		default:
			problemJSON(w, http.StatusNotFound, `{"status":404,"code":"not_found"}`)
		}
	})

	resp, err := c.ApplyBatch(ctx(t), ApplyBatchRequest{})
	if err != nil {
		t.Fatalf("ApplyBatch against a pre-#6053 server: %v", err)
	}
	if len(resp.Items) != 2 {
		t.Fatalf("Items = %+v, want 2", resp.Items)
	}
	if resp.Items[0].Revision != "10" || resp.Items[1].Revision != "11" {
		t.Errorf("Items revisions = %q, %q, want \"10\", \"11\"", resp.Items[0].Revision, resp.Items[1].Revision)
	}
}

// revisionSchemaCoverage maps every apigen schema name
// internal/httpapi/wireshape's golden digest records as carrying a `revision`
// member to a representative value revisionBearingResponse must recognize.
//
// "ApplyItemResult" is the one entry with no case of its own in
// revisionBearingResponse's type switch: it is never decoded as Do's own
// `out` (it only ever appears nested inside ApplyBatchResponse.Items), so its
// coverage is ApplyBatchResponse's own case there, plus
// tolerateLegacyRevisionNumbers' separate items[] handling —
// TestApplyBatchLegacyItemRevisionsAreRewritten above is what actually proves
// that half; this map only proves revisionBearingResponse itself still says
// "yes" for the type that carries it.
var revisionSchemaCoverage = map[string]any{
	"ApplyItemResult":      &apigen.ApplyBatchResponse{},
	"CloseIssueResponse":   &apigen.CloseIssueResponse{},
	"IssueDetails":         &apigen.IssueDetails{},
	"ReleaseIssueResponse": &apigen.ReleaseIssueResponse{},
	"ReopenIssueResponse":  &apigen.ReopenIssueResponse{},
	"UpdateIssueResponse":  &apigen.UpdateIssueResponse{},
}

// TestRevisionBearingResponseCoversEveryWireShapeSchemaWithARevisionMember is
// review follow-up HIGH-a's exhaustiveness guard. It derives the set of
// schemas a legacy server's bare-integer `revision` can appear on from the
// same committed golden digest TestWireShapeDigest
// (internal/httpapi/wireshape) guards against drift, rather than hand-listing
// schema names a second time — the grep this file's first version was
// written against missed apigen.IssueDetails precisely because it is a type
// alias for types.IssueDetails, invisible to a grep for "type \w+ struct.*
// revision". A schema that gains a `revision` member later and is not added
// to revisionSchemaCoverage now fails HERE, rather than only showing up as a
// silent decode failure against a real legacy server.
func TestRevisionBearingResponseCoversEveryWireShapeSchemaWithARevisionMember(t *testing.T) {
	path := filepath.Join("..", "..", "httpapi", "wireshape", "testdata", "golden.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the wireshape golden digest: %v", err)
	}
	var digest struct {
		Entries []struct {
			Schema string `json:"schema"`
			Member string `json:"member"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(data, &digest); err != nil {
		t.Fatalf("unmarshaling the wireshape golden digest: %v", err)
	}

	seen := map[string]bool{}
	for _, e := range digest.Entries {
		if e.Member != "revision" {
			continue
		}
		seen[e.Schema] = true
		out, covered := revisionSchemaCoverage[e.Schema]
		if !covered {
			t.Errorf("golden.json schema %q carries a `revision` member with no entry in revisionSchemaCoverage; add one, and extend revisionBearingResponse (or document why it needs no legacy-integer tolerance)", e.Schema)
			continue
		}
		if !revisionBearingResponse(out) {
			t.Errorf("revisionSchemaCoverage says %q is covered by %T, but revisionBearingResponse(%T) returned false", e.Schema, out, out)
		}
	}
	if len(seen) == 0 {
		t.Fatal("golden.json parsing found no schema with a `revision` member; the fixture moved or this test's assumptions about its shape broke")
	}
	for schema := range revisionSchemaCoverage {
		if !seen[schema] {
			t.Errorf("revisionSchemaCoverage lists %q, but golden.json has no entry for it with member \"revision\" anymore; remove the stale entry", schema)
		}
	}
}
