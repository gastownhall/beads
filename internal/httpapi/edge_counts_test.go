package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The wire edge of GET /v0/beads/dependencies:count, on a fake role.
//
// The handler is a decoder, a bound and a projection — the plane span, the
// missing-anchor rule, the status asymmetry and the de-duplication are
// issueops.GraphCounter's, held to on three legs by its own contract and shown
// against real Dolt in cmd/bd. What only these cases can show is that the
// request a caller SENDS becomes the request the role RECEIVES, that the
// role's answer reaches the wire in the shape the document promises, and that
// each of the role's four refusals arrives naming the right parameter.

func newEdgeCountServer(t *testing.T, result issueops.EdgeCountResult) (*testServer, *roleGraphCounter) {
	t.Helper()
	counter := &roleGraphCounter{result: result}
	return newTestServer(t, rolesConfig(Config{GraphCounter: counter})), counter
}

// TestCountDependencyEdgesProjectsTheWholeRequest drives every parameter, alone
// and together, because they are independent members of one request and a
// handler that decoded one into another's field would answer the all-together
// case correctly and every single-parameter case wrong.
//
// `direction` gets both of its values: the vocabulary is CLOSED and the two
// answers are about different edge sets, so a handler that passed a constant
// would look right on whichever value the fixture happened to use.
func TestCountDependencyEdgesProjectsTheWholeRequest(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query string
		want  issueops.EdgeCountRequest
	}{
		{
			name:  "anchors and the outbound direction",
			query: "?issue_id=bd-1&issue_id=bd-2&direction=out",
			want: issueops.EdgeCountRequest{
				IDs:       []string{"bd-1", "bd-2"},
				Direction: issueops.EdgeDirectionOut,
			},
		},
		{
			name:  "the inbound direction",
			query: "?issue_id=bd-1&direction=in",
			want: issueops.EdgeCountRequest{
				IDs:       []string{"bd-1"},
				Direction: issueops.EdgeDirectionIn,
			},
		},
		{
			name:  "types narrow the edges",
			query: "?issue_id=bd-1&direction=out&type=blocks&type=parent-child",
			want: issueops.EdgeCountRequest{
				IDs:       []string{"bd-1"},
				Direction: issueops.EdgeDirectionOut,
				Types:     []types.DependencyType{types.DepBlocks, types.DepParentChild},
			},
		},
		{
			name:  "status rides with the inbound direction",
			query: "?issue_id=bd-1&direction=in&status=open",
			want: issueops.EdgeCountRequest{
				IDs:       []string{"bd-1"},
				Direction: issueops.EdgeDirectionIn,
				Status:    "open",
			},
		},
		{
			// A repeated id is NOT collapsed here. De-duplication is the
			// ROLE's promise, made on the request it received, so a handler
			// that collapsed first would be a second implementation of it —
			// and one the role's own contract could never observe.
			name:  "a repeated id reaches the role as sent",
			query: "?issue_id=bd-1&issue_id=bd-1&direction=out",
			want: issueops.EdgeCountRequest{
				IDs:       []string{"bd-1", "bd-1"},
				Direction: issueops.EdgeDirectionOut,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts, counter := newEdgeCountServer(t, issueops.EdgeCountResult{})

			resp := ts.get(t, "/v0/beads/dependencies:count"+tc.query)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
			}
			reqs := counter.countRequests()
			if len(reqs) != 1 {
				t.Fatalf("%d counts ran, want 1", len(reqs))
			}
			if !reflect.DeepEqual(reqs[0], tc.want) {
				t.Errorf("EdgeCountRequest = %+v, want %+v", reqs[0], tc.want)
			}
		})
	}
}

// TestCountDependencyEdgesAnswersPerAnchor is the response half: the role's
// answer arrives per anchor, in the role's order, with both members on every
// entry.
//
// The fixture mixes a present anchor with edges, a present anchor with NONE and
// a MISSING one, because those are the three states a caller has to be able to
// tell apart and two of them share a count of 0. A response that dropped
// `missing`, or omitted a zero `count`, would make them indistinguishable while
// still carrying every anchor.
func TestCountDependencyEdgesAnswersPerAnchor(t *testing.T) {
	ts, _ := newEdgeCountServer(t, issueops.EdgeCountResult{Anchors: []issueops.AnchorEdgeCount{
		{ID: "bd-1", Count: 3},
		{ID: "bd-empty", Count: 0},
		{ID: "bd-gone", Count: 0, Missing: true},
	}})

	resp := ts.get(t, "/v0/beads/dependencies:count?issue_id=bd-1&issue_id=bd-empty&issue_id=bd-gone&direction=out")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var body struct {
		Anchors []struct {
			ID      *string `json:"id"`
			Count   *int64  `json:"count"`
			Missing *bool   `json:"missing"`
		} `json:"anchors"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	if len(body.Anchors) != 3 {
		t.Fatalf("%d anchors, want 3: %s", len(body.Anchors), raw)
	}
	for i, want := range []struct {
		id      string
		count   int64
		missing bool
	}{
		{"bd-1", 3, false},
		{"bd-empty", 0, false},
		{"bd-gone", 0, true},
	} {
		got := body.Anchors[i]
		// Pointers throughout: an OMITTED member is the failure this case
		// exists for, and a value-typed decode would read it as the zero that
		// happens to be correct for two of these three rows.
		if got.ID == nil || got.Count == nil || got.Missing == nil {
			t.Fatalf("anchor %d is missing a member: %s", i, raw)
		}
		if *got.ID != want.id || *got.Count != want.count || *got.Missing != want.missing {
			t.Errorf("anchor %d = {%q %d %v}, want {%q %d %v}",
				i, *got.ID, *got.Count, *got.Missing, want.id, want.count, want.missing)
		}
	}
	// The index IS the order promise: the role answers in the order the request
	// first named each anchor, and the handler is a projection that may not
	// sort or reshape it.
	if strings.Index(string(raw), `"bd-1"`) > strings.Index(string(raw), `"bd-gone"`) {
		t.Errorf("the anchors were reordered: %s", raw)
	}
}

// TestCountDependencyEdgesAnswersAnEmptyArrayNotNull pins the one thing a Go
// nil slice gets wrong on the way out. `anchors` is required, so a client is
// entitled to range over it without a nil check.
func TestCountDependencyEdgesAnswersAnEmptyArrayNotNull(t *testing.T) {
	ts, _ := newEdgeCountServer(t, issueops.EdgeCountResult{})

	resp := ts.get(t, "/v0/beads/dependencies:count?issue_id=bd-1&direction=out")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(raw), `"anchors":[]`) {
		t.Errorf("body = %s, want an empty `anchors` array rather than null", raw)
	}
}

// TestCountDependencyEdgesNamesTheRefusedParameter is where the handler earns
// its keep: the role publishes ONE sentinel for four different mistakes, and
// which parameter the client is told to fix comes from re-asking the request
// the validator's own questions.
//
// The last two cases are the ones a naive picker fails. A request carrying TWO
// offenders must name the one the validator reached FIRST — its order is part
// of its contract, and naming the second sends the caller to fix a parameter
// the server never evaluated.
func TestCountDependencyEdgesNamesTheRefusedParameter(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query string
		param string
	}{
		{"no direction at all", "?issue_id=bd-1", "direction"},
		{"a direction outside the closed set", "?issue_id=bd-1&direction=both", "direction"},
		{"status beside the outbound direction", "?issue_id=bd-1&direction=out&status=open", "status"},
		{"an empty id", "?issue_id=bd-1&issue_id=&direction=out", "issue_id"},
		{"an unusable edge type", "?issue_id=bd-1&direction=out&type=", "type"},
		{
			// direction is checked before status: a request that is wrong
			// about both is a refusal about the direction.
			name:  "a bad direction beside a misplaced status",
			query: "?issue_id=bd-1&direction=sideways&status=open",
			param: "direction",
		},
		{
			// status is checked before the per-entry id scan.
			name:  "a misplaced status beside an empty id",
			query: "?issue_id=&direction=out&status=open",
			param: "status",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			counter := &roleGraphCounter{}
			ts := newTestServer(t, rolesConfig(Config{GraphCounter: counter}))
			// The refusals under test are the ROLE's, so the fake has to raise
			// them rather than the handler pre-empting them — which is the
			// arrangement the handler's doc comment describes.
			counter.err = issueops.ErrValidation

			resp := ts.get(t, "/v0/beads/dependencies:count"+tc.query)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if body["code"] != string(CodeInvalidArgument) {
				t.Errorf("code = %v, want invalid_argument", body["code"])
			}
			if body["param"] != tc.param {
				t.Errorf("param = %v, want %q", body["param"], tc.param)
			}
			if body["reason"] != string(ReasonInvalidValue) {
				t.Errorf("reason = %v, want invalid_value", body["reason"])
			}
		})
	}
}

// TestCountDependencyEdgesBoundsTheQuestion pins this operation's OWN limits —
// the two the handler holds rather than the role, because they are statements
// about what one HTTP request may ask for and not about what an edge count
// means.
//
// Both assert that the role was never reached. A bound that refused after the
// read would have bounded nothing that costs anything.
func TestCountDependencyEdgesBoundsTheQuestion(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query string
	}{
		{"no anchors at all", "?direction=out"},
		{"one anchor past the cap", "?direction=out" + strings.Repeat("&issue_id=bd-1", maxDependencyAnchors+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts, counter := newEdgeCountServer(t, issueops.EdgeCountResult{})

			resp := ts.get(t, "/v0/beads/dependencies:count"+tc.query)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if body["param"] != "issue_id" || body["reason"] != string(ReasonInvalidValue) {
				t.Errorf("body = %v, want invalid_argument on issue_id with reason invalid_value", body)
			}
			if n := len(counter.countRequests()); n != 0 {
				t.Errorf("%d counts ran; a refused request must not reach the database", n)
			}
		})
	}

	// The cap itself is reachable: a request AT the bound is served. Without
	// this the two cases above would pass on an off-by-one that refused the
	// hundredth anchor too.
	ts, counter := newEdgeCountServer(t, issueops.EdgeCountResult{})
	resp := ts.get(t, "/v0/beads/dependencies:count?direction=out"+strings.Repeat("&issue_id=bd-1", maxDependencyAnchors))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("at the cap: status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	if n := len(counter.countRequests()); n != 1 {
		t.Errorf("%d counts ran at the cap, want 1", n)
	}
}

// TestCountDependencyEdgesStaysStrictAboutParameters is the transport's own
// 400s, the ones no role refusal is behind: a key this server does not know,
// and a single-valued parameter sent twice.
//
// The repeated-`direction` case matters more here than on most operations. The
// parameter is required and its vocabulary is closed, so silently resolving a
// repeat to one of its values would answer a DIFFERENT question from the one
// the caller asked — and the caller would have no way to notice.
func TestCountDependencyEdgesStaysStrictAboutParameters(t *testing.T) {
	for _, tc := range []struct {
		name   string
		query  string
		param  string
		reason Reason
	}{
		{"an unknown key", "?issue_id=bd-1&direction=out&bogus=1", "bogus", ReasonUnknownParameter},
		{"a near miss on a real parameter", "?issue_id=bd-1&directions=out", "directions", ReasonUnknownParameter},
		{"direction sent twice", "?issue_id=bd-1&direction=out&direction=in", "direction", ReasonInvalidValue},
		{"status sent twice", "?issue_id=bd-1&direction=in&status=open&status=closed", "status", ReasonInvalidValue},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts, counter := newEdgeCountServer(t, issueops.EdgeCountResult{})

			resp := ts.get(t, "/v0/beads/dependencies:count"+tc.query)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if body["param"] != tc.param || body["reason"] != string(tc.reason) {
				t.Errorf("body = %v, want param %q with reason %s", body, tc.param, tc.reason)
			}
			if n := len(counter.countRequests()); n != 0 {
				t.Errorf("%d counts ran for a refused request", n)
			}
		})
	}
}

// TestCountDependencyEdgesReachesItsOwnRole is the wiring pin, and it is not
// ceremony: EdgeReader and GraphCounter are two roles on the same collection
// with adjacent accessors, so a handler wired to the reader would answer this
// operation's requests from the wrong surface. The reader's fake records what
// it was asked, and it must be asked nothing.
func TestCountDependencyEdgesReachesItsOwnRole(t *testing.T) {
	counter := &roleGraphCounter{}
	reader := &roleEdgeReader{}
	ts := newTestServer(t, rolesConfig(Config{GraphCounter: counter, EdgeReader: reader}))

	resp := ts.get(t, "/v0/beads/dependencies:count?issue_id=bd-1&direction=out")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	if n := len(counter.countRequests()); n != 1 {
		t.Errorf("%d counts ran, want 1", n)
	}
	if n := len(reader.edgeRequests()); n != 0 {
		t.Errorf("%d stored-edge reads ran; the count must not reach EdgeReader", n)
	}
}

// TestCountDependencyEdgesKeepsANonValidationFailureAtFiveHundred is the other
// half of classifying on the sentinel: an error that is NOT the role's
// request-validation refusal must not be reported as the caller's fault.
func TestCountDependencyEdgesKeepsANonValidationFailureAtFiveHundred(t *testing.T) {
	counter := &roleGraphCounter{err: errors.New("backend is unreachable")}
	ts := newTestServer(t, rolesConfig(Config{GraphCounter: counter}))

	resp := ts.get(t, "/v0/beads/dependencies:count?issue_id=bd-1&direction=out")
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if body["code"] != string(CodeInternal) {
		t.Errorf("code = %v, want internal", body["code"])
	}
}
