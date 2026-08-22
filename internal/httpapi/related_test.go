package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The wire edge of GET /v0/beads/issues/{id}/related, on a fake role.
//
// The handler is a decoder, a path bound and a projection — which planes the
// anchor is resolved against, which dependency tables the neighbors come from,
// the pinned order, and the dropping of an edge whose far end this database
// holds no row for are all issueops.Relations', held to on three legs by its own
// contract and shown against real Dolt in cmd/bd. What only these cases can show
// is that the request a caller SENDS becomes the request the role RECEIVES, that
// the role's answer reaches the wire in the shape the document promises, that
// the single-anchor miss is a 404 rather than an empty page, and that each of
// the role's reachable refusals arrives naming the right parameter.

func newRelatedServer(t *testing.T, items []*issueops.RelatedIssue) (*testServer, *roleRelations) {
	t.Helper()
	rel := &roleRelations{items: items}
	return newTestServer(t, rolesConfig(Config{Relations: rel})), rel
}

// relatedNeighbor is the smallest element the role can answer with: an issue
// and the type of the edge that led to it.
func relatedNeighbor(id string, depType types.DependencyType) *issueops.RelatedIssue {
	return &issueops.RelatedIssue{
		Issue:          types.Issue{ID: id, Title: "neighbor " + id},
		DependencyType: depType,
	}
}

// TestListRelatedIssuesProjectsTheWholeRequest drives every part of the request
// — the path anchor, both directions, and the repeatable type filter — alone and
// together, because they are independent members of one request and a handler
// that decoded one into another's field would answer the all-together case
// correctly and every narrower case wrong.
//
// `direction` gets BOTH of its values. The vocabulary is closed and the two
// answers are the inverse graph of each other with identical shapes, so a
// handler that passed a constant would look right on whichever value a fixture
// happened to use — which is the exact failure issueops.RelationDirection
// refuses a default to prevent.
func TestListRelatedIssuesProjectsTheWholeRequest(t *testing.T) {
	for _, tc := range []struct {
		name  string
		path  string
		query string
		want  issueops.RelatedRequest
	}{
		{
			name:  "the outgoing direction",
			path:  "bd-1",
			query: "?direction=out",
			want:  issueops.RelatedRequest{ID: "bd-1", Direction: issueops.RelationOut},
		},
		{
			name:  "the incoming direction",
			path:  "bd-1",
			query: "?direction=in",
			want:  issueops.RelatedRequest{ID: "bd-1", Direction: issueops.RelationIn},
		},
		{
			name:  "types narrow the edges",
			path:  "bd-1",
			query: "?direction=out&type=blocks&type=parent-child",
			want: issueops.RelatedRequest{
				ID:        "bd-1",
				Direction: issueops.RelationOut,
				Types:     []types.DependencyType{types.DepBlocks, types.DepParentChild},
			},
		},
		{
			// The anchor is the PATH segment, percent-decoded once. An id
			// carrying a character the URL grammar reserves reaches the role
			// spelled as the caller meant it, not as the wire carried it.
			name:  "a percent-escaped anchor is decoded once",
			path:  "bd%2Fslash",
			query: "?direction=in",
			want:  issueops.RelatedRequest{ID: "bd/slash", Direction: issueops.RelationIn},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts, rel := newRelatedServer(t, nil)

			resp := ts.get(t, "/v0/beads/issues/"+tc.path+"/related"+tc.query)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
			}
			reqs := rel.relatedRequests()
			if len(reqs) != 1 {
				t.Fatalf("%d neighbor reads ran, want 1", len(reqs))
			}
			if !reflect.DeepEqual(reqs[0], tc.want) {
				t.Errorf("RelatedRequest = %+v, want %+v", reqs[0], tc.want)
			}
		})
	}
}

// TestListRelatedIssuesAnswersTheRolesRowsInOrder is the response half: the
// role's answer reaches the wire as `items`, in the role's own order, with each
// element carrying its edge type beside the issue's own fields.
//
// The ORDER assertion is what makes this more than a shape check. The order is
// the ROLE's promise and the handler is a projection that may not sort or
// reshape it, so the fixture is handed to the fake in the order the role would
// answer and compared position by position.
func TestListRelatedIssuesAnswersTheRolesRowsInOrder(t *testing.T) {
	ts, _ := newRelatedServer(t, []*issueops.RelatedIssue{
		relatedNeighbor("bd-a", types.DepBlocks),
		relatedNeighbor("bd-b", types.DepParentChild),
		relatedNeighbor("bd-c", types.DepRelated),
	})

	resp := ts.get(t, "/v0/beads/issues/bd-anchor/related?direction=in")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var body struct {
		Items []struct {
			ID             *string `json:"id"`
			Title          *string `json:"title"`
			DependencyType *string `json:"dependency_type"`
			// Not a member of this element, and asserted as an ABSENCE below:
			// see TestListRelatedIssuesCarriesNoRevision.
			Revision *int64 `json:"revision"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	if len(body.Items) != 3 {
		t.Fatalf("%d items, want 3: %s", len(body.Items), raw)
	}
	for i, want := range []struct {
		id      string
		depType string
	}{
		{"bd-a", string(types.DepBlocks)},
		{"bd-b", string(types.DepParentChild)},
		{"bd-c", string(types.DepRelated)},
	} {
		got := body.Items[i]
		if got.ID == nil || got.DependencyType == nil || got.Title == nil {
			t.Fatalf("item %d is missing a member: %s", i, raw)
		}
		// The ROW, not just the sequence: an answer in the right order whose
		// elements carry the wrong edge type is still wrong, and the edge type
		// is the one member this element adds to a plain issue.
		if *got.ID != want.id || *got.DependencyType != want.depType {
			t.Errorf("item %d = {%q %q}, want {%q %q}", i, *got.ID, *got.DependencyType, want.id, want.depType)
		}
	}
}

// TestListRelatedIssuesCarriesNoRevision pins the element-shape rule this
// operation inherits: the optimistic-concurrency token lives on the detail read
// and nowhere else, and the elements here are the pinned Go struct
// GET /v0/beads/issues/{id} already carries under `dependencies` and
// `dependents`.
//
// It is asserted on the BYTES rather than through a decode, because the failure
// it guards is a member ARRIVING — a decode into a struct without the field
// cannot see one, and a decode into a struct with it reads an absent member as
// the zero that would look correct.
//
// WHAT IT ADDS OVER TestWireTagBijection, measured rather than assumed. Giving
// types.Issue.RowVersion a json tag reddens both this and that one, so on the
// mutation most likely to happen this case is redundant. What it covers alone is
// the other direction: a handler that stopped projecting onto the pinned alias
// and built an envelope element of its own could publish a token with the
// bijection intact, because that gate compares the canonical struct against the
// schema and never looks at a body. This is the wire-side half.
func TestListRelatedIssuesCarriesNoRevision(t *testing.T) {
	neighbor := relatedNeighbor("bd-a", types.DepBlocks)
	// A non-zero token on the row the role answers with, so an element that
	// published it would be visible rather than indistinguishable from a zero.
	neighbor.RowVersion = 918273645
	ts, _ := newRelatedServer(t, []*issueops.RelatedIssue{neighbor})

	resp := ts.get(t, "/v0/beads/issues/bd-anchor/related?direction=out")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if strings.Contains(string(raw), "revision") || strings.Contains(string(raw), "918273645") {
		t.Errorf("body carries a revision member: %s", raw)
	}
}

// TestListRelatedIssuesAnswersAnEmptyArrayNotNull pins the one thing a Go nil
// slice gets wrong on the way out. `items` is required, so a client is entitled
// to range over it without a nil check — and an issue with no neighbors in the
// requested direction is the COMMON case here, not an edge one.
func TestListRelatedIssuesAnswersAnEmptyArrayNotNull(t *testing.T) {
	ts, _ := newRelatedServer(t, nil)

	resp := ts.get(t, "/v0/beads/issues/bd-anchor/related?direction=out")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(raw), `"items":[]`) {
		t.Errorf("body = %s, want an empty `items` array rather than null", raw)
	}
}

// TestListRelatedIssuesAnswersAnAbsentAnchorWithNotFound is the difference from
// every other graph read on this surface, and the reason this operation carries
// a 404 at all: those are batched and report a miss per anchor because failing
// would throw away the answers that were found, and here there is one anchor and
// nothing to preserve.
//
// It matters because the two answers are otherwise identical. An issue with no
// neighbors and an id that names nothing would both be an empty `items`, and
// the empty list is the common case — so a typo would never surface.
func TestListRelatedIssuesAnswersAnAbsentAnchorWithNotFound(t *testing.T) {
	rel := &roleRelations{err: storage.ErrNotFound}
	ts := newTestServer(t, rolesConfig(Config{Relations: rel}))

	resp := ts.get(t, "/v0/beads/issues/bd-gone/related?direction=out")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", resp.StatusCode, readAll(t, resp))
	}
	if body := decodeBody(t, resp); body["code"] != string(CodeNotFound) {
		t.Errorf("code = %v, want not_found", body["code"])
	}
	// The role WAS asked. A 404 decided at the edge would mean the anchor was
	// never probed, which is a different operation.
	if n := len(rel.relatedRequests()); n != 1 {
		t.Errorf("%d neighbor reads ran, want 1 — the miss is the role's answer, not the handler's guess", n)
	}
}

// TestListRelatedIssuesNamesTheRefusedParameter is where the handler earns its
// keep: the role publishes ONE sentinel for the mistakes a request can make, and
// which parameter the client is told to fix comes from re-asking the request
// ValidateRelatedRequest's own questions.
//
// Both of the validator's wire-reachable refusals are here. Its third — an empty
// anchor id — is deliberately absent, because the path bound turns that into the
// 404 the case above covers; a picker that named `id` for a bad direction would
// send the caller to change a path segment that was fine.
func TestListRelatedIssuesNamesTheRefusedParameter(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query string
		param string
	}{
		{"no direction at all", "", "direction"},
		{"a direction outside the closed set", "?direction=both", "direction"},
		{"a direction that is nearly right", "?direction=OUT", "direction"},
		{"an unusable edge type", "?direction=out&type=", "type"},
		{
			// The direction is checked FIRST, which is
			// ValidateRelatedRequest's own order: a request wrong about both
			// is a refusal about the direction, because naming the type would
			// send the caller to fix a parameter the validator never reached.
			name:  "a bad direction beside an unusable type",
			query: "?direction=sideways&type=",
			param: "direction",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The refusals under test are the ROLE's, so the fake has to raise
			// them rather than the handler pre-empting them — which is the
			// arrangement the handler's doc comment describes.
			rel := &roleRelations{err: issueops.ErrValidation}
			ts := newTestServer(t, rolesConfig(Config{Relations: rel}))

			resp := ts.get(t, "/v0/beads/issues/bd-1/related"+tc.query)
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

// TestListRelatedIssuesRefusesAnImpossibleAnchorFromTheEdge: an id longer than
// the column, or one carrying a control character a percent-escape decoded to,
// names no row that can exist. Answering it from the edge costs nothing and
// gives the SAME 404 a real miss gets, so a caller cannot map this server's
// notion of a well-formed id.
//
// It is also what makes ValidateRelatedRequest's empty-id refusal unreachable,
// which the parameter picker above relies on.
func TestListRelatedIssuesRefusesAnImpossibleAnchorFromTheEdge(t *testing.T) {
	long := strings.Repeat("x", types.MaxFieldLen+1)
	for _, id := range []string{long, "bd-%01"} {
		ts, rel := newRelatedServer(t, nil)

		resp := ts.get(t, "/v0/beads/issues/"+id+"/related?direction=out")
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("anchor %q: status = %d, want 404", id, resp.StatusCode)
			continue
		}
		if body := decodeBody(t, resp); body["code"] != string(CodeNotFound) {
			t.Errorf("anchor %q: code = %v, want not_found", id, body["code"])
		}
		if n := len(rel.relatedRequests()); n != 0 {
			t.Errorf("anchor %q reached the role; a refusal from the edge must not buy a database round trip", id)
		}
	}
}

// TestListRelatedIssuesAnswersAQueryRefusalBeforeTheAnchorBound pins the order
// handleGetIssue already has, for its reason: a refused query string is a 400
// that names what to fix, and deciding the id first would answer it with a 404
// and lose the refusal.
func TestListRelatedIssuesAnswersAQueryRefusalBeforeTheAnchorBound(t *testing.T) {
	ts, rel := newRelatedServer(t, nil)

	resp := ts.get(t, "/v0/beads/issues/bd-%01/related?direction=out&bogus=1")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if body["param"] != "bogus" || body["reason"] != string(ReasonUnknownParameter) {
		t.Errorf("body = %v, want param=bogus reason=unknown_parameter", body)
	}
	if n := len(rel.relatedRequests()); n != 0 {
		t.Errorf("%d neighbor reads ran for a refused request", n)
	}
}

// TestListRelatedIssuesStaysStrictAboutParameters is the transport's own 400s,
// the ones no role refusal is behind: a key this server does not know, and a
// single-valued parameter sent twice.
//
// The repeated-`direction` case matters more here than on most operations. The
// parameter is required and its vocabulary is closed, so silently resolving a
// repeat to one of its values would walk the INVERSE graph from the one the
// caller asked for, with the same shape and no way to notice.
func TestListRelatedIssuesStaysStrictAboutParameters(t *testing.T) {
	for _, tc := range []struct {
		name   string
		query  string
		param  string
		reason Reason
	}{
		{"an unknown key", "?direction=out&bogus=1", "bogus", ReasonUnknownParameter},
		{"a near miss on a real parameter", "?directions=out", "directions", ReasonUnknownParameter},
		{"direction sent twice", "?direction=out&direction=in", "direction", ReasonInvalidValue},
		{
			// The anchor is the PATH, so a query parameter naming it is a key
			// this operation does not know — and not a second spelling of the
			// batched reads' `issue_id`.
			name:   "the batched reads' anchor parameter",
			query:  "?direction=out&issue_id=bd-2",
			param:  "issue_id",
			reason: ReasonUnknownParameter,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts, rel := newRelatedServer(t, nil)

			resp := ts.get(t, "/v0/beads/issues/bd-1/related"+tc.query)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if body["param"] != tc.param || body["reason"] != string(tc.reason) {
				t.Errorf("body = %v, want param %q with reason %s", body, tc.param, tc.reason)
			}
			if n := len(rel.relatedRequests()); n != 0 {
				t.Errorf("%d neighbor reads ran for a refused request", n)
			}
		})
	}
}

// TestListRelatedIssuesReachesItsOwnRole is the wiring pin, and it is not
// ceremony: Relations, EdgeReader and Reader all answer about this issue's
// edges through adjacent accessors, so a handler wired to any of the others
// would answer this operation's requests from the wrong surface and still
// produce a plausible body. Their fakes record what they were asked, and they
// must be asked nothing.
//
// The sub-resource path is the other half of the same question: it sits one
// segment past GET /v0/beads/issues/{id}, so a route that matched too widely
// would answer this request from the detail read.
func TestListRelatedIssuesReachesItsOwnRole(t *testing.T) {
	rel := &roleRelations{}
	edges := &roleEdgeReader{}
	rd := &roleReader{}
	ts := newTestServer(t, rolesConfig(Config{Relations: rel, EdgeReader: edges, Reader: rd}))

	resp := ts.get(t, "/v0/beads/issues/bd-1/related?direction=out")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	if n := len(rel.relatedRequests()); n != 1 {
		t.Errorf("%d neighbor reads ran, want 1", n)
	}
	if n := len(edges.edgeRequests()); n != 0 {
		t.Errorf("%d stored-edge reads ran; the neighbor read must not reach EdgeReader", n)
	}
	if n := len(rd.getRequests()); n != 0 {
		t.Errorf("%d detail reads ran; the sub-resource path must not fall through to getIssue", n)
	}
}

// TestListRelatedIssuesKeepsANonValidationFailureAtFiveHundred is the other half
// of classifying on the sentinel: an error that is neither the role's
// request-validation refusal nor its miss must not be reported as the caller's
// fault.
func TestListRelatedIssuesKeepsANonValidationFailureAtFiveHundred(t *testing.T) {
	rel := &roleRelations{err: errors.New("backend is unreachable")}
	ts := newTestServer(t, rolesConfig(Config{Relations: rel}))

	resp := ts.get(t, "/v0/beads/issues/bd-1/related?direction=out")
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", resp.StatusCode, readAll(t, resp))
	}
	if body := decodeBody(t, resp); body["code"] != string(CodeInternal) {
		t.Errorf("code = %v, want internal", body["code"])
	}
}

// TestListRelatedIssuesSurvivesANilNeighbor pins the reason this role goes out
// UNWRAPPED where checkedReader exists: the role answers with a slice of
// POINTERS, and a caller-supplied one that put a nil in it would be a panic on a
// live server if the projection dereferenced blindly. It drops the element
// instead, exactly as wireItems and wireEdges already do.
func TestListRelatedIssuesSurvivesANilNeighbor(t *testing.T) {
	ts, _ := newRelatedServer(t, []*issueops.RelatedIssue{
		relatedNeighbor("bd-a", types.DepBlocks),
		nil,
		relatedNeighbor("bd-c", types.DepRelated),
	})

	resp := ts.get(t, "/v0/beads/issues/bd-anchor/related?direction=out")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var body struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	if len(body.Items) != 2 {
		t.Errorf("%d items, want the two real neighbors: %s", len(body.Items), raw)
	}
	assertNoPanic(t, ts)
}
