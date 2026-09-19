package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// These cover the transport half of POST /v0/beads/issues:batchCreate — the
// body shape, the refusals this operation owns, and how a role refusal is named
// on the wire. Everything below the wire is the role's, pinned by
// backend/conformance/batch_creator_contract.go at all three backends.

const batchCreatePath = "/v0/beads/issues:batchCreate"

func newBatchCreateServer(t *testing.T, creator *roleBatchCreator) *testServer {
	t.Helper()
	return newTestServer(t, rolesConfig(Config{BatchCreator: creator}))
}

// TestBatchCreatePassesTheRequestToTheRoleAndAnswersWithWhatItCreated pins the
// projection in both directions: the wire members become the role's request,
// and the role's issues come back in request order carrying the ids only the
// server can assign.
func TestBatchCreatePassesTheRequestToTheRoleAndAnswersWithWhatItCreated(t *testing.T) {
	creator := &roleBatchCreator{}
	ts := newBatchCreateServer(t, creator)

	resp := ts.claim(t, batchCreatePath, `{"actor":"alice","items":[
		{"title":"first","priority":1,"issue_type":"bug","labels":["api"],
		 "dependencies":[{"target_id":"bd-9","type":"blocks"}]},
		{"title":"second","description":"body"}
	]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	items, _ := body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("items = %v, want the two issues the role created", body["items"])
	}
	for i, want := range []string{"bd-batch-0", "bd-batch-1"} {
		issue, _ := items[i].(map[string]any)
		if issue["id"] != want {
			t.Errorf("items[%d].id = %v, want %q: the generated ids come back in request order", i, issue["id"], want)
		}
	}
	if _, present := body["has_more"]; present {
		t.Error("the response carries has_more; this is not a page and a client must not look for a second one")
	}

	reqs := creator.createRequests()
	if len(reqs) != 1 {
		t.Fatalf("role calls = %d, want 1", len(reqs))
	}
	got := reqs[0]
	if got.Actor != "alice" {
		t.Errorf("actor = %q, want the one the wire named", got.Actor)
	}
	if len(got.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(got.Items))
	}
	first := got.Items[0]
	if first.Issue.Title != "first" || first.Issue.Priority != 1 || first.Issue.IssueType != types.TypeBug {
		t.Errorf("item 0 = %+v, want the wire's title, priority and issue_type", *first.Issue)
	}
	if first.Issue.Status != types.StatusOpen {
		t.Errorf("item 0 status = %q, want open: a created issue starts open", first.Issue.Status)
	}
	if len(first.Issue.Labels) != 1 || first.Issue.Labels[0] != "api" {
		t.Errorf("item 0 labels = %v, want the wire's", first.Issue.Labels)
	}
	if len(first.Dependencies) != 1 ||
		first.Dependencies[0].TargetID != "bd-9" || first.Dependencies[0].Type != types.DepBlocks {
		t.Errorf("item 0 dependencies = %+v, want the one edge the wire named", first.Dependencies)
	}
	// Absent members are absent, not zero-valued strings the role has to
	// second-guess: an omitted priority is the workspace default.
	if got.Items[1].Issue.Priority != 0 || got.Items[1].Issue.Description != "body" {
		t.Errorf("item 1 = %+v, want the wire's description and no priority", *got.Items[1].Issue)
	}
	// No provenance: the file name a CLI batch spells has no analogue here, so
	// the entry reads as the implementation's default rather than as a lie.
	if got.Provenance != "" {
		t.Errorf("provenance = %q, want empty: this surface names no source file", got.Provenance)
	}
}

// TestBatchCreateRefusesTheShapesTheDocumentRefuses walks the 400s this
// operation owns. Every one of them is answered BEFORE the role is reached, so
// a refusal here is also the proof that nothing was created.
func TestBatchCreateRefusesTheShapesTheDocumentRefuses(t *testing.T) {
	for _, test := range []struct {
		name      string
		body      string
		wantParam string
	}{
		{"no actor", `{"items":[{"title":"t"}]}`, "actor"},
		{"blank actor", `{"actor":"   ","items":[{"title":"t"}]}`, "actor"},
		{"actor with a newline", "{\"actor\":\"alice\\nbd: create\",\"items\":[{\"title\":\"t\"}]}", "actor"},
		{"null actor", `{"actor":null,"items":[{"title":"t"}]}`, "actor"},
		{"no items", `{"actor":"alice"}`, "items"},
		{"empty items", `{"actor":"alice","items":[]}`, "items"},
		{"items is not an array", `{"actor":"alice","items":{"title":"t"}}`, "items"},
		{"unknown top-level member", `{"actor":"alice","items":[{"title":"t"}],"dry_run":true}`, "dry_run"},
		{"unknown item member", `{"actor":"alice","items":[{"title":"t","id":"bd-1"}]}`, "items[0].id"},
		{"unknown edge member", `{"actor":"alice","items":[{"title":"t","dependencies":[{"target_id":"bd-9","type":"blocks","reverse":true}]}]}`, "items[0].dependencies.reverse"},
		{"no title", `{"actor":"alice","items":[{"description":"d"}]}`, "items[0].title"},
		{"blank title", `{"actor":"alice","items":[{"title":"  "}]}`, "items[0].title"},
		{"priority out of range", `{"actor":"alice","items":[{"title":"t","priority":9}]}`, "items[0].priority"},
		{"priority is not a number", `{"actor":"alice","items":[{"title":"t","priority":"high"}]}`, "items[0]"},
		{"edge with no target", `{"actor":"alice","items":[{"title":"t","dependencies":[{"target_id":"","type":"blocks"}]}]}`, "items[0].dependencies.target_id"},
		// A value at all, not membership of a list: the edge vocabulary is
		// OPEN, so "nonsense" is a legal type here and only an unstorable one
		// is refused.
		{"edge with an unstorable type", `{"actor":"alice","items":[{"title":"t","dependencies":[{"target_id":"bd-9","type":"` + strings.Repeat("x", types.MaxDependencyTypeLen+1) + `"}]}]}`, "items[0].dependencies.type"},
		{"the second item is the bad one", `{"actor":"alice","items":[{"title":"ok"},{"title":""}]}`, "items[1].title"},
	} {
		t.Run(test.name, func(t *testing.T) {
			creator := &roleBatchCreator{}
			ts := newBatchCreateServer(t, creator)

			resp := ts.claim(t, batchCreatePath, test.body)
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
			if calls := creator.createRequests(); len(calls) != 0 {
				t.Errorf("the role was called %d times for a refused request; nothing may be created", len(calls))
			}
		})
	}
}

// TestBatchCreateRefusesAnOversizeBatch pins the one size bound this operation
// owns. It is a bound on how long a request may hold a write transaction, so it
// is refused before the role is reached rather than after.
func TestBatchCreateRefusesAnOversizeBatch(t *testing.T) {
	creator := &roleBatchCreator{}
	ts := newBatchCreateServer(t, creator)

	items := make([]string, maxBatchCreateItems+1)
	for i := range items {
		items[i] = fmt.Sprintf(`{"title":"issue %d"}`, i)
	}
	resp := ts.claim(t, batchCreatePath, `{"actor":"alice","items":[`+strings.Join(items, ",")+`]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	if body := decodeBody(t, resp); body["param"] != "items" {
		t.Errorf("param = %v, want items", body["param"])
	}
	if calls := creator.createRequests(); len(calls) != 0 {
		t.Errorf("the role was called for an oversize batch")
	}
}

// TestBatchCreateNamesARoleRefusalInTheServersOwnWords is the case
// failBatchCreate exists for. The absent-target refusal arrives as
// ErrValidation WRAPPING ErrNotFound, and the shared classifier reaches
// ErrNotFound first — so without the operation's own failure path this 400
// would be a 404 for a request that addressed no resource at all. The detail
// is the server's own sentence: the role's carries a driver error naming
// tables and constraints.
func TestBatchCreateNamesARoleRefusalInTheServersOwnWords(t *testing.T) {
	for _, test := range []struct {
		name       string
		err        error
		wantDetail string
	}{
		{
			name: "an absent edge target is a 400, not a 404",
			err: fmt.Errorf("create batch item 1: %w: create: dependency target does not exist: "+
				"Error 1452 (23000): Cannot add or update a child row: a foreign key constraint fails "+
				"(`beads`.`dependencies`): %w", storage.ErrValidation, storage.ErrNotFound),
			wantDetail: "a dependency target names no issue in this workspace; nothing was created",
		},
		{
			name:       "a content refusal is a 400",
			err:        fmt.Errorf("create batch item 0: %w: create: invalid issue type", storage.ErrValidation),
			wantDetail: "an item was refused by this workspace's own validation; nothing was created",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ts := newBatchCreateServer(t, &roleBatchCreator{err: test.err})

			resp := ts.claim(t, batchCreatePath, `{"actor":"alice","items":[{"title":"t"}]}`)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if body["code"] != string(CodeInvalidArgument) {
				t.Errorf("code = %v, want %q", body["code"], CodeInvalidArgument)
			}
			if body["detail"] != test.wantDetail {
				t.Errorf("detail = %v, want %q", body["detail"], test.wantDetail)
			}
			if detail, _ := body["detail"].(string); strings.Contains(detail, "foreign key") {
				t.Error("the detail quotes the storage error; 4xx details reflect the caller's input, not server internals")
			}
		})
	}
}

// TestBatchCreateReportsANonValidationRoleFailureAsTheGenericFailure pins the
// other half of failBatchCreate: an error that is not the role's own refusal
// still goes through the shared classifier, so contention and outages keep the
// codes every other route uses.
func TestBatchCreateReportsANonValidationRoleFailureAsTheGenericFailure(t *testing.T) {
	ts := newBatchCreateServer(t, &roleBatchCreator{err: errors.New("connection reset by the void")})

	resp := ts.claim(t, batchCreatePath, `{"actor":"alice","items":[{"title":"t"}]}`)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", resp.StatusCode, readAll(t, resp))
	}
	if body := decodeBody(t, resp); body["code"] != string(CodeInternal) {
		t.Errorf("code = %v, want %q", body["code"], CodeInternal)
	}
}

// TestBatchCreateRefusesACreatorThatAnswersWithNothing pins
// checkedBatchCreator: a nil entry would be a nil dereference in the handler,
// so it is the generic 500 and above all not a panic.
func TestBatchCreateRefusesACreatorThatAnswersWithNothing(t *testing.T) {
	for _, test := range []struct {
		name   string
		issues []*types.Issue
	}{
		{"a nil entry", []*types.Issue{nil}},
		{"fewer issues than items", []*types.Issue{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ts := newBatchCreateServer(t, &roleBatchCreator{issues: test.issues})

			resp := ts.claim(t, batchCreatePath, `{"actor":"alice","items":[{"title":"t"}]}`)
			if resp.StatusCode != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500: %s", resp.StatusCode, readAll(t, resp))
			}
			assertNoPanic(t, ts)
		})
	}
}

// TestBatchCreateRefusesANonJSONContentType pins the CSRF control this
// operation inherits from the claim: a JSON content type is not CORS-"simple",
// so a cross-origin write always triggers a preflight this server never
// approves.
func TestBatchCreateRefusesANonJSONContentType(t *testing.T) {
	creator := &roleBatchCreator{}
	ts := newBatchCreateServer(t, creator)

	resp := ts.postBody(t, batchCreatePath, "text/plain", `{"actor":"alice","items":[{"title":"t"}]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	if body := decodeBody(t, resp); body["param"] != "Content-Type" {
		t.Errorf("param = %v, want Content-Type", body["param"])
	}
	if calls := creator.createRequests(); len(calls) != 0 {
		t.Error("the role was called for a request that skipped the preflight")
	}
}

// TestBatchCreateRefusesAQueryParameter pins that the document-level
// unknown-parameter rule reaches this operation too: it declares no parameters,
// so every query key is refused.
func TestBatchCreateRefusesAQueryParameter(t *testing.T) {
	ts := newBatchCreateServer(t, &roleBatchCreator{})

	resp := ts.claim(t, batchCreatePath+"?dry_run=true", `{"actor":"alice","items":[{"title":"t"}]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if body["param"] != "dry_run" || body["reason"] != string(ReasonUnknownParameter) {
		t.Errorf("param/reason = %v/%v, want dry_run/unknown_parameter", body["param"], body["reason"])
	}
}

// TestBatchCreateIsNotReachableByOtherMethods pins that the collection custom
// method is a POST and nothing else, so a GET of it is the catch-all's answer
// rather than a listing.
func TestBatchCreateIsNotReachableByOtherMethods(t *testing.T) {
	ts := newBatchCreateServer(t, &roleBatchCreator{})

	resp := ts.get(t, batchCreatePath)
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("GET %s = 200; the custom method is a POST", batchCreatePath)
	}
}

// TestBatchCreateDoesNotShadowTheClaimRoute pins the one routing risk the new
// path carries: both are POSTs under /v0/beads/issues, and the claim's pattern
// is a wildcard wide enough to have swallowed this one had the separating slash
// not been there.
func TestBatchCreateDoesNotShadowTheClaimRoute(t *testing.T) {
	issues := &fakeIssues{issue: seededIssue("bd-1", "alice", types.StatusInProgress)}
	ts, _ := newClaimServer(t, issues)

	resp := ts.claim(t, "/v0/beads/issues/bd-1:claim", `{"actor":"alice"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("claim status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
}

var _ issueops.BatchCreator = (*roleBatchCreator)(nil)
