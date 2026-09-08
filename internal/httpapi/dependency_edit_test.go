package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The pins for the dependency-graph writes. What is asserted here is the WIRE
// EDGE — that the handlers decode the document's members into the role's
// request faithfully, refuse what the document refuses, map the role's TYPED
// refusals onto the frozen codes, and re-implement nothing the role owns.
//
// These are pure: the whole path runs over a real listener against a fake role,
// so it is covered on every pull request by the unconditional Go test job. What
// a fake structurally cannot prove is what the storage transaction did — that a
// removal really removed and that a refused batch left ZERO edges behind — and
// that lives in cmd/bd's proxied-server integration test against real Dolt.

const removeDependencyPath = "/v0/beads/dependencies:remove"

func newDependencyServer(t *testing.T, editor *roleDependencyEditor) *testServer {
	t.Helper()
	return newTestServer(t, rolesConfig(Config{DependencyEditor: editor}))
}

// TestRemoveDependencyPathReachesItsHandler: the path is a LITERAL
// collection-level custom method, registered beside three literal paths UNDER
// the same collection. ServeMux requires the separating slash, so a 404 here
// would mean the colon spelling is being routed as something else.
func TestRemoveDependencyPathReachesItsHandler(t *testing.T) {
	editor := &roleDependencyEditor{removed: true}
	ts := newDependencyServer(t, editor)

	resp := ts.claim(t, removeDependencyPath, `{"actor":"alice","issue_id":"bd-1","depends_on_id":"bd-2"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	if calls := editor.removeRequests(); len(calls) != 1 {
		t.Fatalf("the role was called %d times, want 1 — the path reached another handler", len(calls))
	}
}

// TestRemoveDependencyForwardsTheNamedEdgeToTheRole is the operation's central
// pin: both endpoints reach the role EXACTLY as sent, and the actor reaches it
// trimmed.
//
// The asymmetry is the point. `actor` is trimmed because the document says so
// for that member; an id is an EXACT canonical id, so trimming one would
// silently address a row the caller did not name.
func TestRemoveDependencyForwardsTheNamedEdgeToTheRole(t *testing.T) {
	editor := &roleDependencyEditor{removed: true}
	ts := newDependencyServer(t, editor)

	resp := ts.claim(t, removeDependencyPath, `{"actor":"  alice  ","issue_id":"bd-1","depends_on_id":"bd-2"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	calls := editor.removeRequests()
	if len(calls) != 1 {
		t.Fatalf("%d removals, want 1", len(calls))
	}
	want := issueops.RemoveDependencyRequest{Actor: "alice", IssueID: "bd-1", DependsOnID: "bd-2"}
	if calls[0] != want {
		t.Errorf("request = %+v, want %+v", calls[0], want)
	}
	if body := decodeBody(t, resp); body["removed"] != true {
		t.Errorf("removed = %v, want true", body["removed"])
	}
}

// TestRemoveDependencyAnswersAMissingEdgeWithSuccess is the operation's whole
// idempotence contract, and the reason its code set has no 404.
//
// A second teardown pass must not have to classify an error to discover it
// already ran, so an edge that was not there is `removed: false` inside a 200 —
// not `not_found`, which would make a replayed removal indistinguishable from a
// request that went to the wrong server.
func TestRemoveDependencyAnswersAMissingEdgeWithSuccess(t *testing.T) {
	editor := &roleDependencyEditor{removed: false}
	ts := newDependencyServer(t, editor)

	resp := ts.claim(t, removeDependencyPath, `{"actor":"alice","issue_id":"bd-1","depends_on_id":"bd-2"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if body["removed"] != false {
		t.Errorf("removed = %v, want false", body["removed"])
	}
	// The member is required by the document, so it must be on the wire even
	// when false: a client reading a missing member as "absent means unknown"
	// is exactly what a bare `omitempty` would produce.
	if _, ok := body["removed"]; !ok {
		t.Error("`removed` is absent from the body; the document requires it")
	}
}

// TestRemoveDependencyRefusesTheShapesTheDocumentRefuses walks the 400
// vocabulary. Every case also asserts the role was NOT called: each of these is
// decidable from the request alone, so none may buy a database transaction.
func TestRemoveDependencyRefusesTheShapesTheDocumentRefuses(t *testing.T) {
	for _, test := range []struct {
		name      string
		body      string
		wantParam string
	}{
		{"unknown member", `{"actor":"alice","issue_id":"bd-1","depends_on_id":"bd-2","force":true}`, "force"},
		{"no actor", `{"issue_id":"bd-1","depends_on_id":"bd-2"}`, "actor"},
		{"null actor", `{"actor":null,"issue_id":"bd-1","depends_on_id":"bd-2"}`, "actor"},
		{"blank actor", `{"actor":"   ","issue_id":"bd-1","depends_on_id":"bd-2"}`, "actor"},
		{"actor with a newline", `{"actor":"alice\nbd: forged","issue_id":"bd-1","depends_on_id":"bd-2"}`, "actor"},
		{"no issue_id", `{"actor":"alice","depends_on_id":"bd-2"}`, "issue_id"},
		{"null issue_id", `{"actor":"alice","issue_id":null,"depends_on_id":"bd-2"}`, "issue_id"},
		{"issue_id is not a string", `{"actor":"alice","issue_id":7,"depends_on_id":"bd-2"}`, "issue_id"},
		{"empty issue_id", `{"actor":"alice","issue_id":"","depends_on_id":"bd-2"}`, "issue_id"},
		{
			"over-long issue_id",
			`{"actor":"alice","issue_id":"` + strings.Repeat("x", types.MaxFieldLen+1) + `","depends_on_id":"bd-2"}`,
			"issue_id",
		},
		{"issue_id with a control character", `{"actor":"alice","issue_id":"bd-1\u0001x","depends_on_id":"bd-2"}`, "issue_id"},
		{"no depends_on_id", `{"actor":"alice","issue_id":"bd-1"}`, "depends_on_id"},
		{"empty depends_on_id", `{"actor":"alice","issue_id":"bd-1","depends_on_id":""}`, "depends_on_id"},
	} {
		t.Run(test.name, func(t *testing.T) {
			editor := &roleDependencyEditor{}
			ts := newDependencyServer(t, editor)

			resp := ts.claim(t, removeDependencyPath, test.body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if body["code"] != string(CodeInvalidArgument) {
				t.Errorf("code = %v, want %q", body["code"], CodeInvalidArgument)
			}
			if body["param"] != test.wantParam {
				t.Errorf("param = %v, want %q — a client dispatches on this rather than on the detail", body["param"], test.wantParam)
			}
			if calls := editor.removeRequests(); len(calls) != 0 {
				t.Errorf("the role was called %d times for a refused request; nothing may be removed", len(calls))
			}
		})
	}
}

// TestRemoveDependencyRequiresTheDocumentedMediaType: the media-type refusal is
// a CSRF control, so it holds on this write exactly as it does on the claim.
func TestRemoveDependencyRequiresTheDocumentedMediaType(t *testing.T) {
	editor := &roleDependencyEditor{}
	ts := newDependencyServer(t, editor)

	resp := ts.postBody(t, removeDependencyPath, "text/plain",
		`{"actor":"alice","issue_id":"bd-1","depends_on_id":"bd-2"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	if body := decodeBody(t, resp); body["param"] != "Content-Type" {
		t.Errorf("param = %v, want Content-Type", body["param"])
	}
	if calls := editor.removeRequests(); len(calls) != 0 {
		t.Error("the role was called for a request with the wrong media type")
	}
}

// TestRemoveDependencyTakesNoQueryParameters: the operation is in the
// document's no-parameter list, so any query key is the uniform 400.
func TestRemoveDependencyTakesNoQueryParameters(t *testing.T) {
	editor := &roleDependencyEditor{}
	ts := newDependencyServer(t, editor)

	resp := ts.claim(t, removeDependencyPath+"?force=1", `{"actor":"alice","issue_id":"bd-1","depends_on_id":"bd-2"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if body["param"] != "force" || body["reason"] != string(ReasonUnknownParameter) {
		t.Errorf("param/reason = %v/%v, want force/%s", body["param"], body["reason"], ReasonUnknownParameter)
	}
	if calls := editor.removeRequests(); len(calls) != 0 {
		t.Error("the role was called for a request carrying a query string")
	}
}

// TestRemoveDependencyMapsRoleFailuresThroughTheSharedClassifier: this
// operation has no failure path of its own — there is no refusal it can earn
// that the shared mapping does not already name — so a role error must land on
// the frozen code its sentinel implies rather than on a blanket 500.
func TestRemoveDependencyMapsRoleFailuresThroughTheSharedClassifier(t *testing.T) {
	for _, test := range []struct {
		name       string
		err        error
		wantStatus int
		wantCode   Code
	}{
		{
			name:       "an exhausted retry budget is retryable",
			err:        &mysql.MySQLError{Number: 1213, Message: "Deadlock found when trying to get lock"},
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   CodeBusy,
		},
		{
			name:       "anything unrecognized is a 500",
			err:        errors.New("dependencies: unexpected"),
			wantStatus: http.StatusInternalServerError,
			wantCode:   CodeInternal,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ts := newDependencyServer(t, &roleDependencyEditor{removeErr: test.err})

			resp := ts.claim(t, removeDependencyPath, `{"actor":"alice","issue_id":"bd-1","depends_on_id":"bd-2"}`)
			if resp.StatusCode != test.wantStatus {
				t.Fatalf("status = %d, want %d: %s", resp.StatusCode, test.wantStatus, readAll(t, resp))
			}
			if body := decodeBody(t, resp); body["code"] != string(test.wantCode) {
				t.Errorf("code = %v, want %q", body["code"], test.wantCode)
			}
		})
	}
}

const addDependenciesPath = "/v0/beads/dependencies:add"

// oneEdge is the smallest well-formed body, so a case that is about something
// else does not have to spell one.
const oneEdge = `{"actor":"alice","edges":[{"issue_id":"bd-1","depends_on_id":"bd-2","type":"blocks"}]}`

// TestAddDependenciesPathReachesItsHandler: a LITERAL collection-level custom
// method beside :remove, so a 404 here would mean the colon spelling is being
// routed as something else.
func TestAddDependenciesPathReachesItsHandler(t *testing.T) {
	editor := &roleDependencyEditor{}
	ts := newDependencyServer(t, editor)

	resp := ts.claim(t, addDependenciesPath, oneEdge)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	if calls := editor.addRequests(); len(calls) != 1 {
		t.Fatalf("the role was called %d times, want 1 — the path reached another handler", len(calls))
	}
}

// TestAddDependenciesForwardsEveryEdgeInRequestOrder is the operation's central
// pin: the whole edge set reaches the role in the caller's order, with the ids
// exact and the type verbatim, and the guarded cycle check left ON.
//
// It is asserted on the REQUEST rather than on the response: an echo carrying
// the right edges says nothing about which edges the role was asked to write.
func TestAddDependenciesForwardsEveryEdgeInRequestOrder(t *testing.T) {
	editor := &roleDependencyEditor{}
	ts := newDependencyServer(t, editor)

	resp := ts.claim(t, addDependenciesPath, `{
		"actor": "  alice  ",
		"edges": [
			{"issue_id":"bd-1","depends_on_id":"bd-2","type":"blocks"},
			{"issue_id":"bd-3","depends_on_id":"bd-1","type":"parent-child"},
			{"issue_id":"bd-4","depends_on_id":"external:JIRA-9","type":"workspace-specific"}
		]
	}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}

	calls := editor.addRequests()
	if len(calls) != 1 {
		t.Fatalf("%d assertions, want 1", len(calls))
	}
	want := issueops.AddDependenciesRequest{
		Actor: "alice",
		Edges: []issueops.DependencyEdge{
			{IssueID: "bd-1", DependsOnID: "bd-2", Type: "blocks"},
			{IssueID: "bd-3", DependsOnID: "bd-1", Type: "parent-child"},
			// The vocabulary is OPEN, so a workspace's own type passes
			// unexamined, and an `external:` target is a legitimate far end.
			{IssueID: "bd-4", DependsOnID: "external:JIRA-9", Type: "workspace-specific"},
		},
	}
	if !reflect.DeepEqual(calls[0], want) {
		t.Errorf("request = %+v, want %+v", calls[0], want)
	}
	// SkipPerEdgeCycleCheck is not published, and a surface that cannot tell a
	// trusted caller from any other is where a default must be the guarded one.
	if calls[0].SkipPerEdgeCycleCheck {
		t.Error("the handler skipped the per-edge cycle check; that flag is unpublished on this surface")
	}
}

// TestAddDependenciesEchoesTheRequestedEdges: all-or-nothing means the answer
// is either every edge or a refusal, so a caller reporting what landed reads
// the RESULT — which must therefore carry the edges, in request order.
func TestAddDependenciesEchoesTheRequestedEdges(t *testing.T) {
	ts := newDependencyServer(t, &roleDependencyEditor{})

	resp := ts.claim(t, addDependenciesPath, `{
		"actor": "alice",
		"edges": [
			{"issue_id":"bd-3","depends_on_id":"bd-1","type":"blocks"},
			{"issue_id":"bd-1","depends_on_id":"bd-2","type":"related"}
		]
	}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	var body struct {
		Added []map[string]any `json:"added"`
	}
	if err := json.Unmarshal([]byte(readAll(t, resp)), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Added == nil {
		t.Fatal("`added` is null; the document promises an array")
	}
	want := []map[string]any{
		{"issue_id": "bd-3", "depends_on_id": "bd-1", "type": "blocks"},
		{"issue_id": "bd-1", "depends_on_id": "bd-2", "type": "related"},
	}
	if !reflect.DeepEqual(body.Added, want) {
		t.Errorf("added = %v, want %v — the echo is in REQUEST order", body.Added, want)
	}
}

// TestAddDependenciesRefusesTheShapesTheDocumentRefuses walks the 400
// vocabulary. Every case also asserts the role was NOT called: each is
// decidable from the request alone, so none may buy a write transaction.
func TestAddDependenciesRefusesTheShapesTheDocumentRefuses(t *testing.T) {
	longID := strings.Repeat("x", types.MaxFieldLen+1)
	longType := strings.Repeat("x", types.MaxDependencyTypeLen+1)
	for _, test := range []struct {
		name      string
		body      string
		wantParam string
	}{
		{"unknown top-level member", `{"actor":"alice","edges":[],"force":true}`, "force"},
		{"no actor", `{"edges":[{"issue_id":"bd-1","depends_on_id":"bd-2","type":"blocks"}]}`, "actor"},
		{"blank actor", `{"actor":"  ","edges":[{"issue_id":"bd-1","depends_on_id":"bd-2","type":"blocks"}]}`, "actor"},
		{"no edges", `{"actor":"alice"}`, "edges"},
		{"empty edges", `{"actor":"alice","edges":[]}`, "edges"},
		{"edges is not an array", `{"actor":"alice","edges":{"issue_id":"bd-1"}}`, "edges"},
		{
			"unknown edge member",
			`{"actor":"alice","edges":[{"issue_id":"bd-1","depends_on_id":"bd-2","type":"blocks","weight":3}]}`,
			"edges[0].weight",
		},
		{"no issue_id", `{"actor":"alice","edges":[{"depends_on_id":"bd-2","type":"blocks"}]}`, "edges[0].issue_id"},
		{"empty issue_id", `{"actor":"alice","edges":[{"issue_id":"","depends_on_id":"bd-2","type":"blocks"}]}`, "edges[0].issue_id"},
		{
			"over-long depends_on_id",
			`{"actor":"alice","edges":[{"issue_id":"bd-1","depends_on_id":"` + longID + `","type":"blocks"}]}`,
			"edges[0].depends_on_id",
		},
		{"no type", `{"actor":"alice","edges":[{"issue_id":"bd-1","depends_on_id":"bd-2"}]}`, "edges[0].type"},
		{"empty type", `{"actor":"alice","edges":[{"issue_id":"bd-1","depends_on_id":"bd-2","type":""}]}`, "edges[0].type"},
		{
			"unstorable type",
			`{"actor":"alice","edges":[{"issue_id":"bd-1","depends_on_id":"bd-2","type":"` + longType + `"}]}`,
			"edges[0].type",
		},
		// Request-INTRINSIC, so a value refusal and not a conflict: the edge is
		// invalid whatever the graph holds.
		{
			"self dependency",
			`{"actor":"alice","edges":[{"issue_id":"bd-1","depends_on_id":"bd-1","type":"blocks"}]}`,
			"edges[0].depends_on_id",
		},
		{
			"the second edge is the bad one",
			`{"actor":"alice","edges":[{"issue_id":"bd-1","depends_on_id":"bd-2","type":"blocks"},` +
				`{"issue_id":"bd-3","depends_on_id":"","type":"blocks"}]}`,
			"edges[1].depends_on_id",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			editor := &roleDependencyEditor{}
			ts := newDependencyServer(t, editor)

			resp := ts.claim(t, addDependenciesPath, test.body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if body["code"] != string(CodeInvalidArgument) {
				t.Errorf("code = %v, want %q", body["code"], CodeInvalidArgument)
			}
			if body["param"] != test.wantParam {
				t.Errorf("param = %v, want %q — a client dispatches on this rather than on the detail", body["param"], test.wantParam)
			}
			if calls := editor.addRequests(); len(calls) != 0 {
				t.Errorf("the role was called %d times for a refused request; nothing may be written", len(calls))
			}
		})
	}
}

// TestAddDependenciesRefusesAnOversizeBatch pins the one size bound this
// operation owns. It bounds how long a request may hold a write transaction, so
// it is refused before the role is reached rather than after.
func TestAddDependenciesRefusesAnOversizeBatch(t *testing.T) {
	editor := &roleDependencyEditor{}
	ts := newDependencyServer(t, editor)

	edges := make([]string, maxAddDependencyEdges+1)
	for i := range edges {
		edges[i] = fmt.Sprintf(`{"issue_id":"bd-%d","depends_on_id":"bd-target","type":"blocks"}`, i)
	}
	resp := ts.claim(t, addDependenciesPath, `{"actor":"alice","edges":[`+strings.Join(edges, ",")+`]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	if body := decodeBody(t, resp); body["param"] != "edges" {
		t.Errorf("param = %v, want edges", body["param"])
	}
	if calls := editor.addRequests(); len(calls) != 0 {
		t.Error("the role was called for an oversize batch")
	}
}

// TestAddDependenciesRequiresTheDocumentedMediaType: the media-type refusal is
// a CSRF control, and this is the widest write on the surface to leave open.
func TestAddDependenciesRequiresTheDocumentedMediaType(t *testing.T) {
	editor := &roleDependencyEditor{}
	ts := newDependencyServer(t, editor)

	resp := ts.postBody(t, addDependenciesPath, "application/x-www-form-urlencoded", oneEdge)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	if body := decodeBody(t, resp); body["param"] != "Content-Type" {
		t.Errorf("param = %v, want Content-Type", body["param"])
	}
	if calls := editor.addRequests(); len(calls) != 0 {
		t.Error("the role was called for a request with the wrong media type")
	}
}

// TestAddDependenciesTakesNoQueryParameters: the operation is in the document's
// no-parameter list, and the key chosen here is the one an optimistic client
// would reach for — the unpublished cycle-check skip.
func TestAddDependenciesTakesNoQueryParameters(t *testing.T) {
	editor := &roleDependencyEditor{}
	ts := newDependencyServer(t, editor)

	resp := ts.claim(t, addDependenciesPath+"?skip_cycle_check=1", oneEdge)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if body["param"] != "skip_cycle_check" || body["reason"] != string(ReasonUnknownParameter) {
		t.Errorf("param/reason = %v/%v, want skip_cycle_check/%s", body["param"], body["reason"], ReasonUnknownParameter)
	}
	if calls := editor.addRequests(); len(calls) != 0 {
		t.Error("the role was called for a request carrying a query string")
	}
}

// TestAddDependenciesMapsTheGraphsTypedRefusals is what the two new codes exist
// for. Every expectation here is read from the role's TYPED fields; a mapping
// that parsed the sentinel's prose would satisfy the status assertions and
// break the moment a message is reworded — which is exactly the coupling a
// client adopting this endpoint is being told it can delete.
func TestAddDependenciesMapsTheGraphsTypedRefusals(t *testing.T) {
	cycleEdges := `{"actor":"alice","edges":[
		{"issue_id":"bd-1","depends_on_id":"bd-2","type":"blocks"},
		{"issue_id":"bd-2","depends_on_id":"bd-1","type":"blocks"}
	]}`

	for _, test := range []struct {
		name        string
		body        string
		err         error
		wantStatus  int
		wantCode    Code
		wantMembers map[string]any
		// absentMembers are the extension members this refusal must NOT carry.
		// Presence is the discriminator between the two dependency_cycle
		// refusals, so an over-eager mapping is a wrong answer rather than a
		// verbose one.
		absentMembers []string
	}{
		{
			name:       "a plain scheduling cycle carries no hierarchy members",
			body:       cycleEdges,
			err:        fmt.Errorf("add dependencies: %w", issueops.ErrDependencyCycle),
			wantStatus: http.StatusConflict,
			wantCode:   CodeDependencyCycle,
			// ABSENCE is the signal: a client reads "no hierarchy members" as
			// "this was the plain cycle".
			absentMembers: []string{"issue_id", "blocker_id", "blocker_is_ancestor"},
		},
		{
			name: "a blocker that is an ANCESTOR carries all three members",
			body: oneEdge,
			err: &issueops.DependencyHierarchyConflictError{
				IssueID: "bd-1", BlockerID: "bd-parent", BlockerIsAncestor: true,
			},
			wantStatus: http.StatusConflict,
			wantCode:   CodeDependencyCycle,
			wantMembers: map[string]any{
				"issue_id": "bd-1", "blocker_id": "bd-parent", "blocker_is_ancestor": true,
			},
		},
		{
			// The other polarity, and the reason the boolean travels through a
			// pointer: `false` must be ON THE WIRE. An omitted member would be
			// read as the plain cycle refusal, which is a different error.
			name: "a blocker that is a DESCENDANT reports the false polarity",
			body: oneEdge,
			err: &issueops.DependencyHierarchyConflictError{
				IssueID: "bd-1", BlockerID: "bd-child", BlockerIsAncestor: false,
			},
			wantStatus: http.StatusConflict,
			wantCode:   CodeDependencyCycle,
			wantMembers: map[string]any{
				"issue_id": "bd-1", "blocker_id": "bd-child", "blocker_is_ancestor": false,
			},
		},
		{
			name: "a pair carrying another type is dependency_exists with both types",
			body: oneEdge,
			err: &issueops.DependencyTypeConflictError{
				IssueID: "bd-1", DependsOnID: "bd-2", ExistingType: "related", RequestedType: "blocks",
			},
			wantStatus: http.StatusConflict,
			wantCode:   CodeDependencyExists,
			wantMembers: map[string]any{
				"existing_type": "related", "requested_type": "blocks",
			},
			absentMembers: []string{"issue_id", "blocker_id", "blocker_is_ancestor"},
		},
		{
			// A 400 and not a 404: the refusal is about the request BODY, and
			// there is no id in the path to have missed.
			name: "a ghost source is a 400 naming that edge's issue_id",
			body: oneEdge,
			err: &issueops.DependencyEndpointNotFoundError{
				IssueID: "bd-1", DependsOnID: "bd-2", MissingID: "bd-1",
				Err: issueops.ErrDependencySourceNotFound,
			},
			wantStatus:  http.StatusBadRequest,
			wantCode:    CodeInvalidArgument,
			wantMembers: map[string]any{"param": "edges[0].issue_id"},
		},
		{
			name: "a locally-absent target is a 400 naming that edge's depends_on_id",
			body: oneEdge,
			err: &issueops.DependencyEndpointNotFoundError{
				IssueID: "bd-1", DependsOnID: "bd-2", MissingID: "bd-2",
				Err: issueops.ErrDependencyTargetNotFound,
			},
			wantStatus:  http.StatusBadRequest,
			wantCode:    CodeInvalidArgument,
			wantMembers: map[string]any{"param": "edges[0].depends_on_id"},
		},
		{
			name:        "the role's own validation refusal is a 400 on edges",
			body:        oneEdge,
			err:         fmt.Errorf("%w: add dependencies requires a dependency type", storage.ErrValidation),
			wantStatus:  http.StatusBadRequest,
			wantCode:    CodeInvalidArgument,
			wantMembers: map[string]any{"param": "edges"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ts := newDependencyServer(t, &roleDependencyEditor{addErr: test.err})

			resp := ts.claim(t, addDependenciesPath, test.body)
			if resp.StatusCode != test.wantStatus {
				t.Fatalf("status = %d, want %d: %s", resp.StatusCode, test.wantStatus, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if body["code"] != string(test.wantCode) {
				t.Fatalf("code = %v, want %q", body["code"], test.wantCode)
			}
			for member, want := range test.wantMembers {
				got, present := body[member]
				if !present {
					t.Errorf("`%s` is absent; the refusal must be rebuildable from the members, not from the prose", member)
					continue
				}
				if got != want {
					t.Errorf("%s = %v, want %v", member, got, want)
				}
			}
			for _, member := range test.absentMembers {
				if _, present := body[member]; present {
					t.Errorf("`%s` is present on a refusal that does not carry it; member presence is the discriminator", member)
				}
			}
			// The role's prose is not the wire. Its type-conflict message names
			// a CLI command, which is the clearest thing a leaked message would
			// drag onto an HTTP surface.
			if detail, _ := body["detail"].(string); strings.Contains(detail, "bd dep") {
				t.Errorf("detail quotes the role's message: %q", detail)
			}
		})
	}
}

// TestAddDependenciesNamesTheRefusedEdgeByBothEndpoints is why
// DependencyEndpointNotFoundError carries the whole edge rather than only the
// missing id: the refusal is the REQUEST's, so the only way to point a client
// at one of its own edges is to find it by the pair.
func TestAddDependenciesNamesTheRefusedEdgeByBothEndpoints(t *testing.T) {
	refused := &issueops.DependencyEndpointNotFoundError{
		IssueID: "bd-9", DependsOnID: "bd-ghost", MissingID: "bd-ghost",
		Err: issueops.ErrDependencyTargetNotFound,
	}
	ts := newDependencyServer(t, &roleDependencyEditor{addErr: refused})

	resp := ts.claim(t, addDependenciesPath, `{
		"actor": "alice",
		"edges": [
			{"issue_id":"bd-1","depends_on_id":"bd-2","type":"blocks"},
			{"issue_id":"bd-9","depends_on_id":"bd-2","type":"blocks"},
			{"issue_id":"bd-9","depends_on_id":"bd-ghost","type":"blocks"}
		]
	}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	// Index 2, not index 1: both share a source, so a match on `issue_id` alone
	// would name the wrong edge.
	if body := decodeBody(t, resp); body["param"] != "edges[2].depends_on_id" {
		t.Errorf("param = %v, want edges[2].depends_on_id", body["param"])
	}
}
