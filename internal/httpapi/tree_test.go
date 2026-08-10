package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

const treePath = "/v0/beads/dependencies/tree"

// treeServer binds a roles-backed server whose tree walker answers with walker.
// Listen requires a COMPLETE source, so rolesConfig fills in the rest.
func treeServer(t *testing.T, walker *roleTreeWalker) *testServer {
	t.Helper()
	return newTestServer(t, rolesConfig(Config{TreeWalker: walker}))
}

func treeNode(id string, depth int, parentID string, edge types.DependencyType) *types.TreeNode {
	return &types.TreeNode{
		Issue:          types.Issue{ID: id, Title: strings.ToUpper(id), Status: types.StatusOpen, Priority: 2},
		Depth:          depth,
		ParentID:       parentID,
		EdgeFromParent: edge,
	}
}

// TestTreeCarriesTheRolesAnswerToTheWire is the whole point of the operation:
// the body is the role's node list projected onto the envelope, in order, with
// no shaping in between. It asserts the JSON rather than the Go value because
// the alias is what makes them the same thing — apigen.TreeNode IS
// types.TreeNode — so the only way for this to fail is a handler that reordered,
// renamed or dropped something.
func TestTreeCarriesTheRolesAnswerToTheWire(t *testing.T) {
	ts := treeServer(t, &roleTreeWalker{result: issueops.TreeResult{Nodes: []*types.TreeNode{
		treeNode("bd-root", 0, "", ""),
		treeNode("bd-child", 1, "bd-root", types.DepBlocks),
		treeNode("bd-grand", 2, "bd-child", types.DepParentChild),
	}}})

	resp := ts.get(t, treePath+"?root_id=bd-root")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	if hasMore, _ := body["has_more"].(bool); hasMore {
		t.Error("has_more = true; this operation takes no limit, so nothing can truncate the walk")
	}
	items, ok := body["items"].([]any)
	if !ok || len(items) != 3 {
		t.Fatalf("items = %#v, want the role's three nodes", body["items"])
	}
	for i, want := range []struct {
		id     string
		depth  float64
		parent string
	}{
		{"bd-root", 0, ""},
		{"bd-child", 1, "bd-root"},
		{"bd-grand", 2, "bd-child"},
	} {
		node, _ := items[i].(map[string]any)
		if node["id"] != want.id {
			t.Errorf("item %d id = %v, want %q: the answer is in walk order", i, node["id"], want.id)
		}
		if node["depth"] != want.depth {
			t.Errorf("item %d depth = %v, want %v", i, node["depth"], want.depth)
		}
		if node["parent_id"] != want.parent {
			t.Errorf("item %d parent_id = %v, want %q", i, node["parent_id"], want.parent)
		}
		if node["truncated"] != false {
			t.Errorf("item %d truncated = %v, want an explicit false: the field is documented as always false", i, node["truncated"])
		}
	}
	// The root carries no edge, so `edge_from_parent` is omitted rather than
	// emitted empty.
	root, _ := items[0].(map[string]any)
	if _, present := root["edge_from_parent"]; present {
		t.Errorf("the root carries an edge_from_parent key (%#v); it was reached from nothing", root["edge_from_parent"])
	}
	child, _ := items[1].(map[string]any)
	if child["edge_from_parent"] != string(types.DepBlocks) {
		t.Errorf("child edge_from_parent = %v, want %q", child["edge_from_parent"], types.DepBlocks)
	}
}

// TestTreeHandsTheWiresParametersToTheRoleUnchanged is the half that proves the
// handler decides nothing: every parameter the document publishes arrives on the
// request, spelled as the role spells it.
func TestTreeHandsTheWiresParametersToTheRoleUnchanged(t *testing.T) {
	walker := &roleTreeWalker{}
	ts := treeServer(t, walker)

	ts.get(t, treePath+"?root_id=bd-1&direction=up&max_depth=3&status=in_progress")

	reqs := walker.walkRequests()
	if len(reqs) != 1 {
		t.Fatalf("walk requests = %+v, want exactly one", reqs)
	}
	want := issueops.WalkTreeRequest{
		RootID:    "bd-1",
		Direction: issueops.TreeUp,
		MaxDepth:  3,
		Status:    types.StatusInProgress,
	}
	if reqs[0] != want {
		t.Errorf("walk request = %+v, want %+v", reqs[0], want)
	}
	// No max_rows reaches the role from this surface: the defensive cap is a CLI
	// circuit breaker and the document does not publish one here.
	if reqs[0].MaxRows != 0 || reqs[0].MaxRowsSource != "" {
		t.Errorf("walk request carries a cap (%d, %q); this surface publishes none",
			reqs[0].MaxRows, reqs[0].MaxRowsSource)
	}
}

// TestTreeDefaultsTheDirectionAndTheDepth pins the two defaults the DOCUMENT
// states, which are the front door's rather than the role's: the role refuses a
// zero depth outright, so a handler that forwarded an unset one would 400 every
// request that left max_depth off.
func TestTreeDefaultsTheDirectionAndTheDepth(t *testing.T) {
	walker := &roleTreeWalker{}
	ts := treeServer(t, walker)

	ts.get(t, treePath+"?root_id=bd-1")

	reqs := walker.walkRequests()
	if len(reqs) != 1 {
		t.Fatalf("walk requests = %+v, want exactly one", reqs)
	}
	if reqs[0].Direction != issueops.TreeDown {
		t.Errorf("direction = %q, want the documented default %q", reqs[0].Direction, issueops.TreeDown)
	}
	if reqs[0].MaxDepth != defaultTreeDepth {
		t.Errorf("max_depth = %d, want the documented default %d", reqs[0].MaxDepth, defaultTreeDepth)
	}
}

// TestTreeAnswersAnEmptyArrayRatherThanNull pins the `never null` half of the
// envelope. The role's own slice is empty rather than nil, but the wire promise
// must not depend on it keeping that.
func TestTreeAnswersAnEmptyArrayRatherThanNull(t *testing.T) {
	ts := treeServer(t, &roleTreeWalker{})

	var body struct {
		Items   *[]json.RawMessage `json:"items"`
		HasMore bool               `json:"has_more"`
	}
	resp := ts.get(t, treePath+"?root_id=bd-1")
	defer func() { _ = resp.Body.Close() }()
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Items == nil {
		t.Fatal("items is null, want an empty array")
	}
	if len(*body.Items) != 0 {
		t.Fatalf("items = %v, want empty", *body.Items)
	}
}

// TestTreeRefusesTheDocumentedBadRequests pins each 400 the operation can raise
// AND which parameter it attributes it to: `param` is what a client dispatches
// on, and the three have different recoveries.
func TestTreeRefusesTheDocumentedBadRequests(t *testing.T) {
	for _, test := range []struct {
		name  string
		query string
		param string
		role  *roleTreeWalker
	}{
		{
			name:  "no root_id",
			query: "",
			param: "root_id",
		},
		{
			name:  "an empty root_id",
			query: "?root_id=",
			param: "root_id",
		},
		{
			name:  "a direction outside the closed set",
			query: "?root_id=bd-1&direction=sideways",
			param: "direction",
		},
		{
			name:  "a non-integer max_depth",
			query: "?root_id=bd-1&max_depth=deep",
			param: "max_depth",
		},
		{
			// This one is the ROLE's refusal reaching the wire rather than the
			// handler's: the decode accepts 0 happily, and treewalker.go is what
			// says a zero depth is not "unbounded".
			name:  "a zero max_depth",
			query: "?root_id=bd-1&max_depth=0",
			param: "max_depth",
			role: &roleTreeWalker{err: fmt.Errorf("%w: max depth must be at least 1, got 0",
				issueops.ErrValidation)},
		},
		{
			name:  "an unknown parameter",
			query: "?root_id=bd-1&limit=5",
			param: "limit",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			role := test.role
			if role == nil {
				role = &roleTreeWalker{}
			}
			ts := treeServer(t, role)

			resp := ts.get(t, treePath+test.query)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", resp.StatusCode)
			}
			body := decodeBody(t, resp)
			if body["code"] != string(CodeInvalidArgument) {
				t.Errorf("code = %v, want invalid_argument", body["code"])
			}
			if body["param"] != test.param {
				t.Errorf("param = %v, want %q", body["param"], test.param)
			}
		})
	}
}

// TestTreeAnswers404ForARootThatIsNotThere pins the difference from
// GET /v0/beads/dependencies, which reports a miss in the body: this operation
// has ONE anchor, so there is no other answer to preserve.
func TestTreeAnswers404ForARootThatIsNotThere(t *testing.T) {
	ts := treeServer(t, &roleTreeWalker{err: fmt.Errorf("%w: issue bd-ghost", storage.ErrNotFound)})

	resp := ts.get(t, treePath+"?root_id=bd-ghost")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if code := decodeBody(t, resp)["code"]; code != string(CodeNotFound) {
		t.Errorf("code = %v, want not_found", code)
	}
}

// TestTreeMapsARoleFailureThroughTheOneErrorMapping keeps the walk on the same
// classification every other route uses.
func TestTreeMapsARoleFailureThroughTheOneErrorMapping(t *testing.T) {
	ts := treeServer(t, &roleTreeWalker{err: errors.New("backend is unreachable")})

	resp := ts.get(t, treePath+"?root_id=bd-1")
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
	if code := decodeBody(t, resp)["code"]; code != string(CodeInternal) {
		t.Errorf("code = %v, want internal", code)
	}
}

// TestTreeReachesTheProviderAccessor covers the OTHER database source: the
// handler must reach the role through uow.TreeWalkerSource on the timed
// provider, not through the roles fields, which are nil on this server.
func TestTreeReachesTheProviderAccessor(t *testing.T) {
	ts := newTestServer(t, Config{})

	resp := ts.get(t, treePath+"?root_id=bd-1")
	// A handler wired to the roles field, which this source leaves nil, would
	// panic into a recovered 500 rather than reaching the provider's accessor.
	if strings.Contains(ts.stderr.String(), "event=panic") {
		t.Fatalf("the handler panicked; it read a role this source does not set:\n%s", ts.stderr.String())
	}
	// That the request got past the accessor is the assertion.
	if resp.StatusCode == http.StatusInternalServerError {
		t.Fatalf("status = 500 from the provider-backed source: %s", readAll(t, resp))
	}
}
