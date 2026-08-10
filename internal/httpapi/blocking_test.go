package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/steveyegge/beads/issueops"
)

// These cover the transport half of GET /v0/beads/dependencies/blocking: that
// the request reaches the role unaltered, the two size refusals this operation
// owns, how a role refusal is named on the wire, and the never-null members.
// Everything below the wire — which edge types count, which row's status decides
// an edge is live, the order within each list — is the role's, and is pinned by
// backend/conformance/blocking_annotator_contract.go at all three backends.

func newBlockingServer(t *testing.T, annotator *roleBlockingAnnotator) *testServer {
	t.Helper()
	return newTestServer(t, rolesConfig(Config{BlockingAnnotator: annotator}))
}

// TestBlockingAnnotationsPassTheIDsThroughUnaltered pins the half of this
// handler that is easiest to get wrong by helping: the ids go to the role in
// the order they arrived, repeats and all. Collapsing them is the ROLE's
// promise, and a handler that did it first would make the two front doors
// collapse in different places.
func TestBlockingAnnotationsPassTheIDsThroughUnaltered(t *testing.T) {
	annotator := &roleBlockingAnnotator{}
	ts := newBlockingServer(t, annotator)

	ts.get(t, "/v0/beads/dependencies/blocking?issue_id=bd-2&issue_id=bd-1&issue_id=bd-2")

	reads := annotator.blockingRequests()
	if len(reads) != 1 {
		t.Fatalf("the role saw %d requests, want exactly one", len(reads))
	}
	want := []string{"bd-2", "bd-1", "bd-2"}
	if len(reads[0].IDs) != len(want) {
		t.Fatalf("role saw IDs = %v, want %v", reads[0].IDs, want)
	}
	for i := range want {
		if reads[0].IDs[i] != want[i] {
			t.Fatalf("role saw IDs = %v, want %v", reads[0].IDs, want)
		}
	}
}

// TestBlockingAnnotationsCarryTheRoleAnswer pins the wire projection: one entry
// per annotated id, in the role's order, with the role's own struct as the
// element — which is what makes this body and `bd list`'s decoration one
// compatibility domain.
func TestBlockingAnnotationsCarryTheRoleAnswer(t *testing.T) {
	annotator := &roleBlockingAnnotator{result: issueops.BlockingResult{Items: []issueops.IssueBlocking{
		{ID: "bd-1", BlockedBy: []string{"bd-2", "external:ticket-9"}, Blocks: []string{}, Parent: "bd-9"},
		{ID: "bd-3", BlockedBy: []string{}, Blocks: []string{"bd-1"}},
	}}}
	ts := newBlockingServer(t, annotator)

	body := decodeBody(t, ts.get(t, "/v0/beads/dependencies/blocking?issue_id=bd-1&issue_id=bd-3"))
	items, _ := body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("items = %v, want one entry per annotated id", body["items"])
	}
	first, _ := items[0].(map[string]any)
	if first["id"] != "bd-1" || first["parent"] != "bd-9" {
		t.Errorf("items[0] = %v, want bd-1 with parent bd-9", first)
	}
	blockedBy, _ := first["blocked_by"].([]any)
	if len(blockedBy) != 2 || blockedBy[0] != "bd-2" || blockedBy[1] != "external:ticket-9" {
		t.Errorf("items[0].blocked_by = %v, want the role's list in the role's order", first["blocked_by"])
	}
	second, _ := items[1].(map[string]any)
	// An entry with no parent omits the member rather than shipping "": the
	// document types it as optional and a client reads absence as "no parent".
	if _, present := second["parent"]; present {
		t.Errorf("items[1] carries a `parent` %v; an id with none omits the member", second["parent"])
	}
}

// TestBlockingAnnotationsNeverAnswerNull pins both lists and the envelope as
// empty arrays rather than null: a client that ranges over the answer must not
// have to nil-check any of them, which is what "always present" in the document
// means.
func TestBlockingAnnotationsNeverAnswerNull(t *testing.T) {
	annotator := &roleBlockingAnnotator{result: issueops.BlockingResult{Items: []issueops.IssueBlocking{
		{ID: "bd-1", BlockedBy: []string{}, Blocks: []string{}},
	}}}
	ts := newBlockingServer(t, annotator)

	raw := readAll(t, ts.get(t, "/v0/beads/dependencies/blocking?issue_id=bd-1"))
	if strings.Contains(raw, "null") {
		t.Fatalf("body = %s, want empty arrays rather than null", raw)
	}

	// And the envelope itself, for a role that answered with a nil slice: the
	// wire promise does not depend on the role keeping its own.
	bare := newBlockingServer(t, &roleBlockingAnnotator{})
	if got := readAll(t, bare.get(t, "/v0/beads/dependencies/blocking?issue_id=bd-1")); strings.Contains(got, "null") {
		t.Fatalf("body = %s, want an empty items array rather than null", got)
	}
}

// TestBlockingAnnotationsBoundTheQuestion pins the two refusals this operation
// owns. They are on the ID COUNT rather than on the answer, and they share
// maxDependencyAnchors with the stored-edge read: the two operations are asked
// about the same page of ids, and two different numbers would let a client ask
// one of them about a page the other refuses.
func TestBlockingAnnotationsBoundTheQuestion(t *testing.T) {
	ts := newBlockingServer(t, &roleBlockingAnnotator{})

	for _, tc := range []struct {
		name  string
		query string
	}{
		{"no issue_id at all", "/v0/beads/dependencies/blocking"},
		{"more ids than the cap", "/v0/beads/dependencies/blocking?" + repeatedIssueIDs(maxDependencyAnchors+1)},
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

	// The cap is a bound, not a rejection of large requests: exactly the cap is
	// served.
	if resp := ts.get(t, "/v0/beads/dependencies/blocking?"+repeatedIssueIDs(maxDependencyAnchors)); resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d for exactly %d ids, want 200: %s", resp.StatusCode, maxDependencyAnchors, readAll(t, resp))
	}
}

// TestBlockingAnnotationsRefuseAnUnknownParameter keeps this operation under
// the document's uniform rule. It publishes exactly ONE parameter, so a client
// that guessed at a `type` or `direction` filter would otherwise receive the
// unfiltered decoration and believe it narrowed.
func TestBlockingAnnotationsRefuseAnUnknownParameter(t *testing.T) {
	ts := newBlockingServer(t, &roleBlockingAnnotator{})

	resp := ts.get(t, "/v0/beads/dependencies/blocking?issue_id=bd-1&type=blocks")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if body["param"] != "type" || body["reason"] != string(ReasonUnknownParameter) {
		t.Errorf("problem = %v, want unknown_parameter on type", body)
	}
}

// TestABlockingRoleRefusalIsTheDocumentedBadRequest pins the mapping from the
// role's ErrValidation onto the one parameter that can cause it, and that
// nothing else becomes a 400.
func TestABlockingRoleRefusalIsTheDocumentedBadRequest(t *testing.T) {
	ts := newBlockingServer(t, &roleBlockingAnnotator{
		err: fmt.Errorf("annotate blocking: %w", issueops.ErrValidation),
	})
	resp := ts.get(t, "/v0/beads/dependencies/blocking?issue_id=bd-1&issue_id=")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if body["code"] != string(CodeInvalidArgument) || body["param"] != "issue_id" || body["reason"] != string(ReasonInvalidValue) {
		t.Errorf("problem = %v, want invalid_argument on issue_id with invalid_value", body)
	}

	opaque := newBlockingServer(t, &roleBlockingAnnotator{err: errors.New("backend is unreachable")})
	if resp := opaque.get(t, "/v0/beads/dependencies/blocking?issue_id=bd-1"); resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d for an opaque role failure, want 500: %s", resp.StatusCode, readAll(t, resp))
	}
}
