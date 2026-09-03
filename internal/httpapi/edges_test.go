package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// These cover the transport half of GET /v0/beads/dependencies: the two size
// refusals this operation owns, how a role refusal is named on the wire, and the
// two never-null members. Everything below the wire is the role's, and is pinned
// by backend/conformance/edge_reader_contract.go at all three backends.

func newEdgesServer(t *testing.T, edges *roleEdgeReader) *testServer {
	t.Helper()
	return newTestServer(t, rolesConfig(Config{EdgeReader: edges}))
}

// TestDependenciesFlattenTheRoleAnswer pins the wire projection: the per-anchor
// answer becomes ONE flat array of edges in anchor order, which is the shape
// `bd dep list a b c --json` emits, plus the ghost anchors on their own member.
func TestDependenciesFlattenTheRoleAnswer(t *testing.T) {
	edges := &roleEdgeReader{result: issueops.EdgeReadResult{Anchors: []issueops.AnchorEdges{
		{ID: "bd-1", Edges: []*types.Dependency{
			{IssueID: "bd-1", DependsOnID: "bd-2", Type: types.DepBlocks},
			{IssueID: "bd-1", DependsOnID: "external:ticket-9", Type: types.DepRelated},
		}},
		{ID: "bd-3", Missing: true},
		{ID: "bd-4", Edges: []*types.Dependency{
			{IssueID: "bd-4", DependsOnID: "bd-2", Type: types.DepBlocks},
		}},
	}}}
	ts := newEdgesServer(t, edges)

	body := decodeBody(t, ts.get(t, "/v0/beads/dependencies?issue_id=bd-1&issue_id=bd-3&issue_id=bd-4"))
	items, _ := body["items"].([]any)
	if len(items) != 3 {
		t.Fatalf("items = %v, want the three edges the role returned", body["items"])
	}
	want := []string{"bd-2", "external:ticket-9", "bd-2"}
	for i, item := range items {
		edge, _ := item.(map[string]any)
		if edge["depends_on_id"] != want[i] {
			t.Errorf("items[%d].depends_on_id = %v, want %q (anchor order, then the role's edge order)", i, edge["depends_on_id"], want[i])
		}
		// The row's surrogate key is not selected by this read, so `omitempty`
		// keeps it off the wire entirely rather than shipping an empty string.
		if _, present := edge["id"]; present {
			t.Errorf("items[%d] carries an `id`; the source-keyed read does not select it", i)
		}
	}
	if missing, _ := body["missing"].([]any); len(missing) != 1 || missing[0] != "bd-3" {
		t.Errorf("missing = %v, want just the ghost anchor", body["missing"])
	}
}

// TestDependenciesNeverAnswerNull pins both members as empty arrays rather than
// null, which the document states for each of them: a client that ranges over
// the answer must not have to nil-check either one.
func TestDependenciesNeverAnswerNull(t *testing.T) {
	edges := &roleEdgeReader{result: issueops.EdgeReadResult{Anchors: []issueops.AnchorEdges{
		{ID: "bd-1"},
	}}}
	ts := newEdgesServer(t, edges)

	raw := readAll(t, ts.get(t, "/v0/beads/dependencies?issue_id=bd-1"))
	if strings.Contains(raw, "null") {
		t.Fatalf("body = %s, want empty arrays rather than null", raw)
	}
}

// TestDependenciesBoundTheQuestion pins the two refusals this operation owns.
// They are on the ANCHOR COUNT rather than on the answer because `bd dep list`
// has no limit, so a `limit` here would make the two front doors default
// differently; see maxDependencyAnchors.
func TestDependenciesBoundTheQuestion(t *testing.T) {
	edges := &roleEdgeReader{}
	ts := newEdgesServer(t, edges)

	for _, tc := range []struct {
		name  string
		query string
	}{
		{"no issue_id at all", "/v0/beads/dependencies"},
		{"an empty issue_id list", "/v0/beads/dependencies?type=blocks"},
		{"more anchors than the cap", "/v0/beads/dependencies?" + repeatedIssueIDs(maxDependencyAnchors+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := ts.get(t, tc.query)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if body["code"] != string(CodeInvalidArgument) || body["param"] != "issue_id" || body["reason"] != string(ReasonInvalidValue) {
				t.Errorf("problem = %v, want invalid_argument on issue_id with invalid_value", body)
			}
		})
	}

	// The cap is a bound, not a rejection of large requests: exactly the cap
	// is served.
	if resp := ts.get(t, "/v0/beads/dependencies?"+repeatedIssueIDs(maxDependencyAnchors)); resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d for exactly %d anchors, want 200: %s", resp.StatusCode, maxDependencyAnchors, readAll(t, resp))
	}
}

// TestDependenciesRefuseAnUnknownParameter keeps this operation under the
// document's uniform rule: an unrecognized filter parameter WIDENS the result
// set, so it is version skew and not something to ignore.
func TestDependenciesRefuseAnUnknownParameter(t *testing.T) {
	ts := newEdgesServer(t, &roleEdgeReader{})

	resp := ts.get(t, "/v0/beads/dependencies?issue_id=bd-1&direction=up")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if body["param"] != "direction" || body["reason"] != string(ReasonUnknownParameter) {
		t.Errorf("problem = %v, want unknown_parameter on direction", body)
	}
}

// TestADependencyRoleRefusalIsTheDocumentedBadRequest pins the mapping from the
// role's ErrValidation onto the two parameters that can cause it.
//
// The role owns both refusals — an empty id, an unusable dependency type — so
// the handler names the parameter from the request it still holds: an empty
// entry can only have come from issue_id, so anything else is type.
func TestADependencyRoleRefusalIsTheDocumentedBadRequest(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query string
		param string
	}{
		{"an empty id", "/v0/beads/dependencies?issue_id=bd-1&issue_id=", "issue_id"},
		{"an unusable type", "/v0/beads/dependencies?issue_id=bd-1&type=", "type"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := newEdgesServer(t, &roleEdgeReader{
				err: fmt.Errorf("read edges: %w", issueops.ErrValidation),
			})
			resp := ts.get(t, tc.query)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if body["code"] != string(CodeInvalidArgument) || body["param"] != tc.param || body["reason"] != string(ReasonInvalidValue) {
				t.Errorf("problem = %v, want invalid_argument on %q with invalid_value", body, tc.param)
			}
		})
	}

	// Anything that is NOT a validation refusal keeps going through the one
	// mapping in problem.go rather than becoming a wrong 400.
	ts := newEdgesServer(t, &roleEdgeReader{err: errors.New("backend is unreachable")})
	if resp := ts.get(t, "/v0/beads/dependencies?issue_id=bd-1"); resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d for an opaque role failure, want 500: %s", resp.StatusCode, readAll(t, resp))
	}
}

func repeatedIssueIDs(n int) string {
	parts := make([]string, 0, n)
	for i := 0; i < n; i++ {
		parts = append(parts, fmt.Sprintf("issue_id=bd-%d", i))
	}
	return strings.Join(parts, "&")
}
