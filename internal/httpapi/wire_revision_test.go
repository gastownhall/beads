package httpapi

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// These are pure, like the rest of the package's edge tests: the whole
// request lifecycle runs over a real listener against a fake provider, so the
// Bd-Wire-Revision gate — where it sits in route(), what it refuses, and
// that it reaches the identity handshake too — is covered on every pull
// request by the unconditional Go test job.

// wireRevisioned drives a request carrying (or omitting) the Bd-Wire-Revision
// header. An empty raw value sends no header at all, which is the
// backward-compatible path.
func (ts *testServer) wireRevisioned(t *testing.T, method, path, raw string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, ts.base+path, nil)
	if err != nil {
		t.Fatalf("new %s %s: %v", method, path, err)
	}
	if raw != "" {
		req.Header.Set(WireRevisionHeader, raw)
	}
	resp, err := ts.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func assertWireRevisionUnsupported(t *testing.T, resp *http.Response, wantMin int) {
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
	if body["param"] != WireRevisionHeader {
		t.Errorf("param = %v, want %s", body["param"], WireRevisionHeader)
	}
	if body["reason"] != string(ReasonWireRevisionUnsupported) {
		t.Errorf("reason = %v, want %s", body["reason"], ReasonWireRevisionUnsupported)
	}
	// The one member that scopes the disclosure: it carries this server's own
	// floor, and it is present ONLY on this refusal.
	min, ok := body["min_wire_revision"].(float64)
	if !ok || int(min) != wantMin {
		t.Errorf("min_wire_revision = %#v, want %d", body["min_wire_revision"], wantMin)
	}
	// wire_revision and bd_version ride alongside min_wire_revision on this
	// same refusal, so a client logging it can report what this server
	// currently is without a second /v0/beads/context round trip.
	wire, ok := body["wire_revision"].(float64)
	if !ok || int(wire) != CurrentWireRevision {
		t.Errorf("wire_revision = %#v, want %d", body["wire_revision"], CurrentWireRevision)
	}
	// bd_version mirrors this server's own ContextResponse.bd_version, which a
	// bare test fixture with no version wired in reports as "" — a string, not
	// an absence, is what's being pinned here.
	if _, ok := body["bd_version"].(string); !ok {
		t.Errorf("bd_version = %#v, want a string", body["bd_version"])
	}
}

// wireRevisionReadServer is newReadServer's provider wiring (reads_test.go),
// needed here because /v0/beads/ready 500s against the bare default
// fakeProvider{} newTestServer(t, Config{}) otherwise falls back to — that
// fake answers the claim path only, not a read.
func wireRevisionReadServer(t *testing.T, tune ...func(*Server)) *testServer {
	t.Helper()
	cfg := Config{Provider: &fakeProvider{
		issues:     &fakeIssues{},
		readIssues: &recordingIssues{},
		readConfig: emptyConfig{},
	}}
	return newTestServer(t, cfg, tune...)
}

// TestWireRevisionAbsentIsTodaysBehavior is the backward-compatibility
// contract: a client that sends no Bd-Wire-Revision header is served exactly
// as before, on both an ordinary route and the identity handshake.
func TestWireRevisionAbsentIsTodaysBehavior(t *testing.T) {
	ts := wireRevisionReadServer(t)

	for _, path := range []string{"/v0/beads/ready", "/v0/beads/context"} {
		resp := ts.wireRevisioned(t, http.MethodGet, path, "")
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s with no header = %d, want 200: %s", path, resp.StatusCode, readAll(t, resp))
		}
	}
}

// TestWireRevisionAtOrAboveFloorServes: a declared revision at or above
// MinClientWireRevision is in range and the request runs exactly as an
// undeclared one does.
func TestWireRevisionAtOrAboveFloorServes(t *testing.T) {
	ts := wireRevisionReadServer(t)

	resp := ts.wireRevisioned(t, http.MethodGet, "/v0/beads/ready", strconv.Itoa(MinClientWireRevision))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a floor-equal revision: %s", resp.StatusCode, readAll(t, resp))
	}
}

// TestWireRevisionBelowFloorRefusesEveryEnforcedRoute mirrors
// TestProjectStampMismatchRefusesEveryEnforcedRoute: a declared revision below
// the server's floor is refused on an ordinary read AND on the identity
// handshake — the one route Bd-Project-Id is exempt on but this header is
// deliberately not, since the handshake is where catching the mismatch is
// cheapest.
//
// The floor is raised to 3 via the tune hook (mirroring semTimeout et al in
// newTestServer's tune pattern) rather than read at its production default of
// 0: a non-negative integer strictly below a floor of 0 does not exist, so
// MinClientWireRevision itself could never exercise this path.
func TestWireRevisionBelowFloorRefusesEveryEnforcedRoute(t *testing.T) {
	const floor = 3
	ts := wireRevisionReadServer(t, func(s *Server) { s.minClientWireRevision = floor })
	below := strconv.Itoa(floor - 1)

	for _, path := range []string{"/v0/beads/ready", "/v0/beads/context"} {
		t.Run(path, func(t *testing.T) {
			resp := ts.wireRevisioned(t, http.MethodGet, path, below)
			assertWireRevisionUnsupported(t, resp, floor)
		})
	}

	// Attributable, like every other middleware refusal.
	line := findLogLine(t, ts.stderr.String(), "op="+OpListReadyWork)
	for _, want := range []string{"status=400", "code=invalid_argument", "refused=" + logValue(below)} {
		if !strings.Contains(line, want) {
			t.Errorf("the refused request's log line is missing %q:\n%s", want, line)
		}
	}
}

// TestWireRevisionMalformedIsInvalidValueNotUnsupported: a header that does
// not parse as a non-negative integer is a different client mistake than a
// revision this server has read and understood to be too old, and must not
// be confused with one by carrying min_wire_revision.
func TestWireRevisionMalformedIsInvalidValueNotUnsupported(t *testing.T) {
	ts := wireRevisionReadServer(t)

	for _, raw := range []string{"not-a-number", "-1", "1.5", "+1", "01", "007", ""} {
		if raw == "" {
			continue // the empty string is the absent-header case, tested elsewhere
		}
		t.Run(raw, func(t *testing.T) {
			resp := ts.wireRevisioned(t, http.MethodGet, "/v0/beads/ready", raw)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if body["reason"] != string(ReasonInvalidValue) {
				t.Errorf("reason = %v, want %s", body["reason"], ReasonInvalidValue)
			}
			if _, present := body["min_wire_revision"]; present {
				t.Errorf("a malformed header disclosed min_wire_revision: %v", body["min_wire_revision"])
			}
		})
	}
}

// TestHealthAnswersDespiteABelowFloorRevision: liveness is exempt. A kubelet
// probe carries no Bd-Wire-Revision of its own, and must not be refused for a
// header it never had a reason to send.
func TestHealthAnswersDespiteABelowFloorRevision(t *testing.T) {
	ts := newTestServer(t, Config{})

	resp := ts.wireRevisioned(t, http.MethodGet, "/healthz", strconv.Itoa(MinClientWireRevision-1))
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /healthz with a below-floor revision = %d, want 200 (this route is exempt)", resp.StatusCode)
	}
}

// TestContextAdvertisesWireRevision: the handshake carries both members every
// client needs to decide whether it can safely proceed.
func TestContextAdvertisesWireRevision(t *testing.T) {
	ts := newTestServer(t, Config{})

	body := decodeBody(t, ts.get(t, "/v0/beads/context"))
	wire, ok := body["wire_revision"].(float64)
	if !ok || int(wire) != CurrentWireRevision {
		t.Errorf("wire_revision = %#v, want %d", body["wire_revision"], CurrentWireRevision)
	}
	min, ok := body["min_client_wire_revision"].(float64)
	if !ok || int(min) != MinClientWireRevision {
		t.Errorf("min_client_wire_revision = %#v, want %d", body["min_client_wire_revision"], MinClientWireRevision)
	}
}

// TestWireRevisionExemptRoutesAreExactlyHealth pins the exempt column by
// enumeration: exactly {OpHealth} skips the Bd-Wire-Revision floor check.
// Unlike projectExempt, the identity handshake is deliberately NOT on this
// list — see wireRevisionExempt's doc comment for why the two diverge.
func TestWireRevisionExemptRoutesAreExactlyHealth(t *testing.T) {
	var exempt []string
	for _, rt := range routeTable {
		if rt.wireRevisionExempt {
			exempt = append(exempt, rt.op)
		}
	}
	slices.Sort(exempt)
	want := []string{OpHealth}
	if !slices.Equal(exempt, want) {
		t.Errorf("wireRevisionExempt routes = %v, want exactly {OpHealth}", exempt)
	}
}

// TestRetiredRevisionsStayBelowTheFloor pins the PRESENCE guarantee
// documented on the `wire_revision` property: 0 and 1 are permanently
// retired, so a decoded 0 can only mean "the server omitted the field," never
// a value a current or future server actually sent. That reading breaks the
// moment either constant moves the wrong way — CurrentWireRevision dropping
// below 2 would let a real server send one of the retired values again, and
// MinClientWireRevision exceeding CurrentWireRevision would make every
// request refused as unsupported by this build's own floor.
func TestRetiredRevisionsStayBelowTheFloor(t *testing.T) {
	if CurrentWireRevision < 2 {
		t.Errorf("CurrentWireRevision = %d, want >= 2: 0 and 1 are retired and must stay below every revision a server can actually send", CurrentWireRevision)
	}
	if MinClientWireRevision > CurrentWireRevision {
		t.Errorf("MinClientWireRevision = %d, want <= CurrentWireRevision (%d)", MinClientWireRevision, CurrentWireRevision)
	}
}

// TestCapabilitiesAdvertiseIssuesListSort: a client learns this server
// accepts GET /v0/beads/issues' sort parameter from the capability list,
// never from the version string.
func TestCapabilitiesAdvertiseIssuesListSort(t *testing.T) {
	ts := newTestServer(t, Config{})

	caps, _ := decodeBody(t, ts.get(t, "/v0/beads/context"))["capabilities"].([]any)
	var got []string
	for _, c := range caps {
		got = append(got, c.(string))
	}
	if !slices.Contains(got, CapIssuesListSort) {
		t.Errorf("capabilities %v do not advertise %q", got, CapIssuesListSort)
	}
}
