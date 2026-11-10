package httpapi

import (
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

// These are pure, like the rest of the package's edge tests: the whole request
// lifecycle runs over a real listener against a fake provider, so the
// Bd-Project-Id gate — where it sits in route(), what it refuses, and what a
// refusal never mutates — is covered on every pull request by the unconditional
// Go test job.

const serverProjectID = "server-proj"

// stamped drives a request carrying (or omitting) the Bd-Project-Id header. An
// empty stamp sends no header at all, which is the backward-compatible path.
func (ts *testServer) stamped(t *testing.T, method, path, stamp, body string) *http.Response {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, ts.base+path, r)
	if err != nil {
		t.Fatalf("new %s %s: %v", method, path, err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if stamp != "" {
		req.Header.Set(ProjectIDHeader, stamp)
	}
	resp, err := ts.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func assertProjectMismatch(t *testing.T, resp *http.Response, wantOwn string) {
	t.Helper()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	if got := resp.Header.Get("Content-Type"); got != "application/problem+json; charset=utf-8" {
		t.Errorf("Content-Type = %q, want problem+json", got)
	}
	body := decodeBody(t, resp)
	if body["code"] != string(CodeInvalidArgument) {
		t.Errorf("code = %v, want %s", body["code"], CodeInvalidArgument)
	}
	if body["param"] != ProjectIDHeader {
		t.Errorf("param = %v, want %s", body["param"], ProjectIDHeader)
	}
	if body["reason"] != string(ReasonProjectMismatch) {
		t.Errorf("reason = %v, want %s", body["reason"], ReasonProjectMismatch)
	}
	// The one member that scopes the disclosure: it carries the server's own
	// project id, and it is present ONLY on this refusal.
	if spid, ok := body["server_project_id"].(string); !ok || spid != wantOwn {
		t.Errorf("server_project_id = %#v, want %q", body["server_project_id"], wantOwn)
	}
}

// stampServer wires a provider-backed server whose own project id is known, so a
// wrong stamp has something concrete to disagree with.
func stampServer(t *testing.T) (*testServer, *fakeProvider, *fakeIssues) {
	t.Helper()
	issues := &fakeIssues{issue: seededIssue("bd-1", "alice", types.StatusInProgress)}
	provider := &fakeProvider{issues: issues}
	ts := newTestServer(t, Config{
		Provider:  provider,
		Workspace: domain.ContextInfo{ProjectID: serverProjectID},
	})
	return ts, provider, issues
}

// TestProjectStampMismatchRefusesEveryEnforcedRoute drives one representative of
// each request shape the surface has — a GET read, a custom-method POST write, a
// PATCH, the plain-collection POST create, and the batch write — each carrying a
// stamp for a workspace this server does not serve. Every one is a 400
// project_mismatch that names the header and discloses the server's own id, and
// NONE of them reaches the database: no unit of work opens, so no row and no
// history line can change. createIssue and applyBatch are here on purpose: they
// are ops upstream added after the check landed, and they inherit enforcement for
// free precisely because route() is the single choke point in front of every
// non-exempt handler (applyBatch through dispatchCustomMethod).
func TestProjectStampMismatchRefusesEveryEnforcedRoute(t *testing.T) {
	ts, provider, issues := stampServer(t)

	for _, tc := range []struct {
		name, method, path, body string
	}{
		{"GET read", http.MethodGet, "/v0/beads/ready", ""},
		{"custom-method write", http.MethodPost, claimPath, `{"actor":"alice"}`},
		{"PATCH", http.MethodPatch, updatePath, `{"actor":"alice","patch":{"title":"x"}}`},
		{"createIssue write", http.MethodPost, createPath, `{"actor":"alice","title":"one"}`},
		{"applyBatch write", http.MethodPost, batchApplyPath, `{"actor":"alice","items":[{"kind":"create","create":{"title":"one"}}]}`},
		{"batchClose write", http.MethodPost, batchClosePath, `{"actor":"alice","items":[{"id":"bd-1"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := ts.stamped(t, tc.method, tc.path, "other-proj", tc.body)
			assertProjectMismatch(t, resp, serverProjectID)
		})
	}

	// The whole point of putting the check ahead of the semaphore and the
	// handler: a misdirected request buys no database work.
	if uows := provider.openedUOWs(); len(uows) != 0 {
		t.Errorf("a refused request opened %d units of work; the mismatch must be caught before any database work", len(uows))
	}
	if c := issues.claimed(); len(c) != 0 {
		t.Errorf("a refused claim reached the CAS: %v", c)
	}

	// And it is attributable, like every other middleware refusal: the request
	// line books the operation and names the stamp it turned down.
	line := findLogLine(t, ts.stderr.String(), "op="+OpClaimIssue)
	for _, want := range []string{"status=400", "code=invalid_argument", "refused=" + logValue("other-proj")} {
		if !strings.Contains(line, want) {
			t.Errorf("the refused claim's request line is missing %q:\n%s", want, line)
		}
	}
}

// TestProjectStampAbsentIsTodaysBehavior is the backward-compatibility contract:
// a client that sends no Bd-Project-Id header is served exactly as before, even
// though this server has a project id it COULD enforce against. Enforcement is
// triggered by the header's arrival, not by the server holding an id.
func TestProjectStampAbsentIsTodaysBehavior(t *testing.T) {
	ts, provider, issues := stampServer(t)

	resp := ts.claim(t, claimPath, `{"actor":"alice"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 for an unstamped request: %s", resp.StatusCode, readAll(t, resp))
	}
	if c := issues.claimed(); len(c) != 1 {
		t.Errorf("the unstamped claim did not reach the CAS: %v", c)
	}
	if uows := provider.openedUOWs(); len(uows) != 1 {
		t.Errorf("opened %d units of work, want 1: an unstamped request must run the handler", len(uows))
	}
}

// TestProjectStampMatchingServes: a stamp equal to the server's own project id
// is a match, and the request runs exactly as an unstamped one does.
func TestProjectStampMatchingServes(t *testing.T) {
	ts, provider, issues := stampServer(t)

	resp := ts.stamped(t, http.MethodPost, claimPath, serverProjectID, `{"actor":"alice"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a matching stamp: %s", resp.StatusCode, readAll(t, resp))
	}
	if c := issues.claimed(); len(c) != 1 {
		t.Errorf("the matching-stamp claim did not reach the CAS: %v", c)
	}
	if uows := provider.openedUOWs(); len(uows) != 1 {
		t.Errorf("opened %d units of work, want 1", len(uows))
	}
}

// TestHealthAndContextAnswerDespiteAWrongStamp: the two exempt reads must answer
// whatever a caller stamps. Liveness that went dark on a misconfigured client
// would defeat its own purpose, and the identity handshake is where a client
// LEARNS the project id — so it cannot itself require a matching one.
func TestHealthAndContextAnswerDespiteAWrongStamp(t *testing.T) {
	ts := newTestServer(t, Config{Workspace: domain.ContextInfo{ProjectID: serverProjectID}})

	for _, path := range []string{"/healthz", "/v0/beads/context"} {
		resp := ts.stamped(t, http.MethodGet, path, "other-proj", "")
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s with a wrong stamp = %d, want 200 (this route is exempt)", path, resp.StatusCode)
		}
	}

	// The handshake even hands back the very id a client would need to stamp
	// correctly next time.
	body := decodeBody(t, ts.stamped(t, http.MethodGet, "/v0/beads/context", "other-proj", ""))
	if body["project_id"] != serverProjectID {
		t.Errorf("context project_id = %v, want %q", body["project_id"], serverProjectID)
	}
}

// TestProjectStampRefusalDoesNotLeakThroughTheHostGate pins the ordering that
// scopes the disclosure. On a server with no token file there is no
// authentication layer at all, so the earliest gate a request meets is the
// Host allowlist middleware, which runs before the mux dispatches to route()
// and therefore before the stamp is ever compared. With a token file the 401
// sits between the two — behind the Host gate, ahead of the stamp (route()) —
// so it only widens the guarantee this test pins. A request that fails BOTH —
// a foreign Host and a wrong stamp — is answered by the Host gate, and that
// answer must not carry server_project_id.
//
// It is the OSS analog of the auth-before-check ordering: server_project_id is
// added by exactly one constructor (ProjectMismatch), reached only after every
// earlier gate has passed, so no earlier refusal can disclose the server's
// identity. A deployment that adds an authentication layer inherits the same
// property for free — it, too, is a gate in front of route().
func TestProjectStampRefusalDoesNotLeakThroughTheHostGate(t *testing.T) {
	ts := newTestServer(t, Config{Workspace: domain.ContextInfo{ProjectID: serverProjectID}})

	req, err := http.NewRequest(http.MethodGet, ts.base+"/v0/beads/ready", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Host = "evil.example"
	req.Header.Set(ProjectIDHeader, "other-proj")
	resp, err := ts.client.Do(req)
	if err != nil {
		t.Fatalf("GET with foreign Host and wrong stamp: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	// The Host gate answered, not the stamp check.
	if body["param"] != "Host" {
		t.Errorf("param = %v, want Host (the earlier gate must win)", body["param"])
	}
	if body["reason"] == string(ReasonProjectMismatch) {
		t.Errorf("the stamp check ran despite a refused Host: reason = %v", body["reason"])
	}
	// And it disclosed nothing about the server's identity.
	if _, present := body["server_project_id"]; present {
		t.Errorf("a refusal raised before the stamp check disclosed server_project_id: %v", body["server_project_id"])
	}
}

// TestCapabilitiesAdvertiseProjectEnforce: a client learns this server enforces
// the stamp from the capability list, never from the version string.
func TestCapabilitiesAdvertiseProjectEnforce(t *testing.T) {
	ts := newTestServer(t, Config{})

	caps, _ := decodeBody(t, ts.get(t, "/v0/beads/context"))["capabilities"].([]any)
	var got []string
	for _, c := range caps {
		got = append(got, c.(string))
	}
	if !slices.Contains(got, CapProjectEnforce) {
		t.Errorf("capabilities %v do not advertise %q", got, CapProjectEnforce)
	}
}

// TestProjectExemptRoutesAreExactlyHealthAndContext pins the exempt column by
// ENUMERATION: exactly {OpHealth, OpGetContext} skip the Bd-Project-Id stamp
// check, and every other route is enforced. The exemption is deliberately NOT
// coupled to bypassSemaphore. events:watch is why: it bypasses the semaphore —
// its database slot moves to each individual read so the open stream never
// holds one for its whole life — yet it carries journal data, so a stamp for
// the wrong workspace must still be refused. Only the two reads that touch no
// workspace data at all, liveness and the identity handshake, are exempt.
func TestProjectExemptRoutesAreExactlyHealthAndContext(t *testing.T) {
	var exempt []string
	for _, rt := range routeTable {
		if rt.projectExempt {
			exempt = append(exempt, rt.op)
		}
	}
	slices.Sort(exempt)
	want := []string{OpGetContext, OpHealth}
	slices.Sort(want)
	if !slices.Equal(exempt, want) {
		t.Errorf("projectExempt routes = %v, want exactly {OpHealth, OpGetContext}", exempt)
	}

	// events:watch specifically is the invariant that replaced the old
	// projectExempt==bypassSemaphore coupling: a bypassSemaphore row that stays
	// project-stamp-enforced. Guard it by name so a future edit cannot quietly
	// exempt the stream by flipping the column it no longer tracks.
	var sawWatch bool
	for _, rt := range routeTable {
		if rt.op != OpWatchEvents {
			continue
		}
		sawWatch = true
		if !rt.bypassSemaphore {
			t.Error("events:watch no longer bypasses the semaphore; this case no longer proves the two columns are decoupled")
		}
		if rt.projectExempt {
			t.Error("events:watch is projectExempt; a data-carrying stream must stay project-stamp-enforced even though it bypasses the semaphore")
		}
	}
	if !sawWatch {
		t.Error("no events:watch row in the route table; the decoupling case cannot be evaluated")
	}
}

// TestSpecDocumentsTheProjectStampContract is the spec-parity half: every Reason
// the server can emit is documented in the reason prose, the refusal's
// server-identity member is a declared Problem member, and the document-level
// rule names the header, the machine reason and the capability. Prose is
// generated from nothing, so without this the document could fall silently out
// of step with the code that emits the refusal.
func TestSpecDocumentsTheProjectStampContract(t *testing.T) {
	doc := loadSpec(t)
	problem := mapAt(t, mapAt(t, mapAt(t, doc, "components"), "schemas"), "Problem")
	props := mapAt(t, problem, "properties")

	reasonDesc, _ := mapAt(t, props, "reason")["description"].(string)
	for _, r := range []Reason{ReasonUnknownParameter, ReasonInvalidValue, ReasonProjectMismatch} {
		if !strings.Contains(reasonDesc, "`"+string(r)+"`") {
			t.Errorf("the Problem.reason prose does not document the reason %q the server can emit", r)
		}
	}

	if _, ok := props["server_project_id"]; !ok {
		t.Error("the Problem schema does not declare server_project_id, the member the mismatch refusal sets")
	}

	desc, _ := mapAt(t, doc, "info")["description"].(string)
	for _, want := range []string{ProjectIDHeader, string(ReasonProjectMismatch), "server_project_id", CapProjectEnforce} {
		if !strings.Contains(desc, want) {
			t.Errorf("the document-level rules do not mention %q", want)
		}
	}
}
