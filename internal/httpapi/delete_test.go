package httpapi

import (
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/issueops"
)

// The pins for POST /v0/beads/issues:delete. As with the sweep, what is
// asserted here is the WIRE EDGE — that the handler decodes the document's
// members into the role's request faithfully, refuses what the document
// refuses, and does not re-implement anything the role owns.

const deletePath = "/v0/beads/issues:delete"

func (ts *testServer) delete(t *testing.T, body string) *http.Response {
	t.Helper()
	return ts.postBody(t, deletePath, "application/json", body)
}

// TestDeletePathReachesItsHandler is the sweep row's twin: a LITERAL segment
// registered beside the claim's wildcard `/v0/beads/issues/{idop}`, where
// ServeMux precedence is by specificity rather than by registration order. A
// 404 or a claim-shaped refusal here would mean the delete is being parsed as a
// claim of an issue named ":delete".
func TestDeletePathReachesItsHandler(t *testing.T) {
	deleter := &roleDeleter{result: issueops.DeleteResult{Deleted: 1}}
	ts := newTestServer(t, rolesConfig(Config{Deleter: deleter}))

	resp := ts.delete(t, `{"ids":["bd-1"],"force":true}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	if len(deleter.requests()) != 1 {
		t.Fatalf("the delete role was called %d times, want 1 — the path reached another handler",
			len(deleter.requests()))
	}
}

// TestDeleteForwardsEveryDocumentedMember is the operation's central pin: each
// of the body's five UNCONSTRAINED members reaches the role's request unchanged.
// `expected_version` has cases of its own below rather than a column here,
// because it constrains `ids` — a guard beside this case's two ids is refused
// by design.
//
// It is asserted on the REQUEST the role received rather than on the response,
// because a body carrying the right numbers says nothing about which beads went
// — this is the operation where the handler dropping `cascade` deletes a
// subtree the caller did not ask for.
func TestDeleteForwardsEveryDocumentedMember(t *testing.T) {
	deleter := &roleDeleter{}
	ts := newTestServer(t, rolesConfig(Config{Deleter: deleter}))

	resp := ts.delete(t, `{
		"ids": ["bd-1", "bd-2"],
		"actor": "  alice  ",
		"cascade": true,
		"force": true,
		"dry_run": true
	}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}

	reqs := deleter.requests()
	if len(reqs) != 1 {
		t.Fatalf("%d deletes, want 1", len(reqs))
	}
	want := issueops.DeleteRequest{
		IDs: []string{"bd-1", "bd-2"},
		// TRIMMED, by the same rule and the same function the claim's actor
		// goes through: it reaches the same commit-message interpolation.
		Actor:   "alice",
		Cascade: true,
		Force:   true,
		DryRun:  true,
	}
	if !reflect.DeepEqual(reqs[0], want) {
		t.Errorf("request = %+v, want %+v", reqs[0], want)
	}
}

// TestDeleteDefaultsTheOptionalMembers: a body carrying only `ids` reaches the
// role as a GUARDED, non-cascading, real delete.
//
// All three flags default to their zero values, and here — unlike the sweep's
// protect_referenced — false is the GUARDED answer for all three. The zero value
// is the protection, so a handler that "helpfully" defaulted force on is the bug.
func TestDeleteDefaultsTheOptionalMembers(t *testing.T) {
	deleter := &roleDeleter{}
	ts := newTestServer(t, rolesConfig(Config{Deleter: deleter}))

	if resp := ts.delete(t, `{"ids":["bd-1"]}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	reqs := deleter.requests()
	if len(reqs) != 1 {
		t.Fatalf("%d deletes, want 1", len(reqs))
	}
	want := issueops.DeleteRequest{IDs: []string{"bd-1"}}
	if !reflect.DeepEqual(reqs[0], want) {
		t.Errorf("request = %+v, want %+v", reqs[0], want)
	}
}

// TestDeletePublishesTheWholeResult drives every member of the response body:
// the schema is not x-go-type-pinned, so a field added to the role's result and
// forgotten in the projection would be silently absent from the wire.
func TestDeletePublishesTheWholeResult(t *testing.T) {
	deleter := &roleDeleter{result: issueops.DeleteResult{
		DryRun:            true,
		Deleted:           4,
		Dependencies:      7,
		Labels:            2,
		Events:            9,
		ReferencesUpdated: 3,
		Orphaned:          []string{"bd-orphan-a", "bd-orphan-b"},
	}}
	ts := newTestServer(t, rolesConfig(Config{Deleter: deleter}))

	resp := ts.delete(t, `{"ids":["bd-1"],"force":true,"dry_run":true}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	want := map[string]any{
		"dry_run":            true,
		"deleted":            float64(4),
		"dependencies":       float64(7),
		"labels":             float64(2),
		"events":             float64(9),
		"references_updated": float64(3),
		"orphaned":           []any{"bd-orphan-a", "bd-orphan-b"},
	}
	if !reflect.DeepEqual(body, want) {
		t.Errorf("body = %#v, want %#v", body, want)
	}
}

// TestDeleteOmitsAnEmptyOrphanList: `orphaned` is documented absent rather than
// empty when nothing was orphaned, so a client can tell "this mode cannot
// orphan" from "this mode could and did not".
func TestDeleteOmitsAnEmptyOrphanList(t *testing.T) {
	deleter := &roleDeleter{result: issueops.DeleteResult{Deleted: 1}}
	ts := newTestServer(t, rolesConfig(Config{Deleter: deleter}))

	body := decodeBody(t, ts.delete(t, `{"ids":["bd-1"],"cascade":true}`))
	if _, present := body["orphaned"]; present {
		t.Errorf("orphaned = %v, want it absent when nothing was orphaned", body["orphaned"])
	}
}

func TestDeleteRefusesTheDocumentedBodies(t *testing.T) {
	for _, test := range []struct {
		name  string
		body  string
		param string
	}{
		{"no ids", `{"force":true}`, "ids"},
		{"ids is not an array", `{"ids":"bd-1"}`, "ids"},
		{"ids is null", `{"ids":null}`, "ids"},
		{"ids is empty", `{"ids":[]}`, "ids"},
		{"ids carries a non-string", `{"ids":[3]}`, "ids"},
		{"unknown member", `{"ids":["bd-1"],"scope":"all"}`, "scope"},
		{"actor is not a string", `{"ids":["bd-1"],"actor":7}`, "actor"},
		{"actor is blank", `{"ids":["bd-1"],"actor":"  "}`, "actor"},
		{"cascade is not a boolean", `{"ids":["bd-1"],"cascade":"yes"}`, "cascade"},
		{"force is not a boolean", `{"ids":["bd-1"],"force":1}`, "force"},
		{"dry_run is not a boolean", `{"ids":["bd-1"],"dry_run":"yes"}`, "dry_run"},
	} {
		t.Run(test.name, func(t *testing.T) {
			deleter := &roleDeleter{}
			ts := newTestServer(t, rolesConfig(Config{Deleter: deleter}))

			resp := ts.delete(t, test.body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if body["code"] != string(CodeInvalidArgument) {
				t.Errorf("code = %v, want %s", body["code"], CodeInvalidArgument)
			}
			if body["param"] != test.param {
				t.Errorf("param = %v, want %q", body["param"], test.param)
			}
			// A refused body must never reach the role: this is destructive,
			// and "refused after acting" is the one failure shape worth ruling
			// out at every edge.
			if got := len(deleter.requests()); got != 0 {
				t.Errorf("the role was called %d times for a refused body, want 0", got)
			}
		})
	}
}

// TestDeletePublishesTheRolesGuardAsA400 is the reason failDeleteErr exists.
//
// The dependents guard is refused BELOW the wire, by issueops.Deleter — that is
// what makes this endpoint incapable of orphaning a graph by omission. The
// refusal reaching the client as a 500 would tell it the server was broken when
// the request was.
func TestDeletePublishesTheRolesGuardAsA400(t *testing.T) {
	// The typed error, not a lookalike: the handler keys on the sentinel it
	// wraps.
	deleter := &roleDeleter{err: &issueops.DependentsOutsideRequestError{
		IssueID:    "bd-blocker",
		Dependents: []string{"bd-dependent"},
	}}
	ts := newTestServer(t, rolesConfig(Config{Deleter: deleter}))

	resp := ts.delete(t, `{"ids":["bd-blocker"]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if body["code"] != string(CodeInvalidArgument) {
		t.Errorf("code = %v, want %s", body["code"], CodeInvalidArgument)
	}
	if detail, _ := body["detail"].(string); !strings.Contains(detail, "bd-blocker") {
		t.Errorf("detail = %q, want the role's own sentence naming the blocked bead", detail)
	}
	// No `param`: the refusal is about the absence of a CHOICE between two
	// members, and the document declares `param` absent on that case.
	if _, present := body["param"]; present {
		t.Errorf("param = %v, want it absent for a whole-request refusal", body["param"])
	}
}

// TestDeletePublishesAnAbsentIDAsA404 pins the other refusal, and pins that its
// detail does NOT name the ids.
//
// NotFound's fixed sentence is what keeps a handler that decided a miss without
// reading storage indistinguishable from one that read and missed. `bd delete`
// still names them, because it is answering the person who typed them.
func TestDeletePublishesAnAbsentIDAsA404(t *testing.T) {
	deleter := &roleDeleter{err: &issueops.NotFoundError{IDs: []string{"bd-nosuch"}}}
	ts := newTestServer(t, rolesConfig(Config{Deleter: deleter}))

	resp := ts.delete(t, `{"ids":["bd-nosuch"],"force":true}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if body["code"] != string(CodeNotFound) {
		t.Errorf("code = %v, want %s", body["code"], CodeNotFound)
	}
	if detail, _ := body["detail"].(string); strings.Contains(detail, "bd-nosuch") {
		t.Errorf("detail = %q, want this surface's fixed not-found sentence without the id", detail)
	}
}

// TestDeleteRefusesAForeignMediaType: the CSRF control the claim documents, for
// the reason the sweep gives — accepting text/plain would let an attacker's
// page drive a delete from any browser on the host, and this one does not even
// need the beads to be closed.
func TestDeleteRefusesAForeignMediaType(t *testing.T) {
	deleter := &roleDeleter{}
	ts := newTestServer(t, rolesConfig(Config{Deleter: deleter}))

	resp := ts.postBody(t, deletePath, "text/plain", `{"ids":["bd-1"]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	if body := decodeBody(t, resp); body["param"] != "Content-Type" {
		t.Errorf("param = %v, want Content-Type", body["param"])
	}
	if got := len(deleter.requests()); got != 0 {
		t.Errorf("the role was called %d times for a refused media type, want 0", got)
	}
}

// TestDeletePublishesNoQueryParameters: this operation's whole vocabulary is
// its body, so a query key is version skew rather than a bad value.
func TestDeletePublishesNoQueryParameters(t *testing.T) {
	ts := newTestServer(t, rolesConfig(Config{Deleter: &roleDeleter{}}))

	resp := ts.postBody(t, deletePath+"?force=true", "application/json", `{"ids":["bd-1"]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if body["param"] != "force" || body["reason"] != string(ReasonUnknownParameter) {
		t.Errorf("body = %v, want unknown_parameter on param force", body)
	}
}

// TestDeleteResponseCarriesEveryRoleField is the guard the missing x-go-type
// pin costs. deleteResponse is a hand-written projection, so a field added to
// issueops.DeleteResult and forgotten there would vanish from the wire with
// nothing to notice; this counts the fields and fails when the two drift.
func TestDeleteResponseCarriesEveryRoleField(t *testing.T) {
	role := reflect.TypeOf(issueops.DeleteResult{}).NumField()
	wire := reflect.TypeOf(deleteResponse(issueops.DeleteResult{})).NumField()
	if role != wire {
		t.Fatalf("issueops.DeleteResult has %d fields and the wire type has %d: "+
			"a field was added to one and not projected onto the other (see deleteResponse)", role, wire)
	}
}

// TestDeleteForwardsTheVersionGuard: the member reaches the role as the POINTER
// it models, and an absent member stays nil.
//
// The nil half is what stops the handler inventing a guard. On this operation
// that matters more than anywhere else on the surface: a delete that took &0 by
// default would refuse every bead that has ever been written to, and one that
// dropped a real guard would erase a row the caller no longer recognizes.
func TestDeleteForwardsTheVersionGuard(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want *int64
	}{
		{"a guard is forwarded", `{"ids":["bd-1"],"expected_version":9007199254740993}`, guard(guardToken)},
		{"the never-written version is a real guard", `{"ids":["bd-1"],"expected_version":0}`, guard(0)},
		{"an absent guard stays nil", `{"ids":["bd-1"]}`, nil},
		// Cascade and force bypass POLICY, never a precondition. Both travel
		// beside the guard rather than instead of it.
		{"cascade does not displace it", `{"ids":["bd-1"],"cascade":true,"expected_version":41}`, guard(41)},
		{"force does not displace it", `{"ids":["bd-1"],"force":true,"expected_version":41}`, guard(41)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deleter := &roleDeleter{}
			ts := newTestServer(t, rolesConfig(Config{Deleter: deleter}))

			resp := ts.delete(t, tc.body)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
			}
			reqs := deleter.requests()
			if len(reqs) != 1 {
				t.Fatalf("%d deletes, want 1", len(reqs))
			}
			switch {
			case tc.want == nil && reqs[0].ExpectedVersion != nil:
				t.Errorf("ExpectedVersion = %d, want nil: an absent member must not become a guard", *reqs[0].ExpectedVersion)
			case tc.want != nil && reqs[0].ExpectedVersion == nil:
				t.Errorf("ExpectedVersion = nil, want %d", *tc.want)
			case tc.want != nil && *reqs[0].ExpectedVersion != *tc.want:
				t.Errorf("ExpectedVersion = %d, want %d", *reqs[0].ExpectedVersion, *tc.want)
			}
		})
	}
}

// TestDeleteRefusesAGuardBesideSeveralIDs is the arity rule at the edge: one
// token describes one row, so a guard beside more than one DISTINCT id is a 400
// naming the guard, and nothing reaches the role.
//
// It is refused here rather than left to the role — which refuses the same pair
// as ErrValidation — because that route reaches the wire through failDeleteErr
// with NO `param` at all, and the member name is the whole recovery: send one
// guarded request per bead.
func TestDeleteRefusesAGuardBesideSeveralIDs(t *testing.T) {
	deleter := &roleDeleter{}
	ts := newTestServer(t, rolesConfig(Config{Deleter: deleter}))

	resp := ts.delete(t, `{"ids":["bd-1","bd-2"],"expected_version":41}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if body["code"] != string(CodeInvalidArgument) {
		t.Errorf("code = %v, want %s", body["code"], CodeInvalidArgument)
	}
	if body["param"] != expectedVersionMember {
		t.Errorf("param = %v, want %s — the guard is the member that cannot be honored", body["param"], expectedVersionMember)
	}
	// A 400 and not a 409: a token beside two ids is a malformed request, not a
	// statement about state, and a client must not retry it after a re-read.
	if body["status"] != float64(http.StatusBadRequest) {
		t.Errorf("status = %v, want %d", body["status"], http.StatusBadRequest)
	}
	if calls := deleter.requests(); len(calls) != 0 {
		t.Errorf("%d deletes reached the role; the arity rule is refused before anything is read", len(calls))
	}
}

// TestDeleteCountsDistinctIDsForTheGuard is the ANTI-DRIFT pin, and it is the
// case that earns the local counting helper its place.
//
// The role's rule is DUPLICATES COLLAPSE FIRST — `bd delete a a` with a token
// names one bead and is legal — and it collapses after TRIMMING
// (workapi.NormalizeDeleteIDs). The transport boundary keeps that package out
// of the handler, so the rule is respelled there; an edge that counted mentions
// instead, or counted untrimmed, would refuse a request the library calls fine
// and no other test in this file would notice.
func TestDeleteCountsDistinctIDsForTheGuard(t *testing.T) {
	for _, tc := range []struct {
		name string
		ids  string
	}{
		{"a repeated id is one bead", `["bd-1","bd-1"]`},
		{"a repeated id is one bead after trimming", `["bd-1"," bd-1"]`},
		{"three mentions of one bead", `["bd-1","bd-1","bd-1"]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deleter := &roleDeleter{}
			ts := newTestServer(t, rolesConfig(Config{Deleter: deleter}))

			resp := ts.delete(t, `{"ids":`+tc.ids+`,"expected_version":41}`)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
			}
			reqs := deleter.requests()
			if len(reqs) != 1 {
				t.Fatalf("%d deletes, want 1", len(reqs))
			}
			if reqs[0].ExpectedVersion == nil {
				t.Fatal("the guard did not reach the role")
			}
			// The ids reach the role AS SENT. Collapsing them is the role's own
			// normalization, and a handler that did it here would be answering a
			// different request than the one it was given.
			if len(reqs[0].IDs) == 1 && strings.Count(tc.ids, "bd-1") > 1 {
				t.Errorf("the handler collapsed the id list before the role saw it: %v", reqs[0].IDs)
			}
		})
	}
}

// TestDeleteRefusesABlankID keeps the arity rule from stealing a broken id
// list's answer.
//
// A blank entry is the ROLE's refusal too, and ValidateDeleteRequest puts it
// AHEAD of the arity rule. The edge has to agree, because it counts trimmed
// distinct ids: a request of `["", "bd-1"]` with a guard would otherwise be
// told its guard names two beads, when what is actually wrong is that its id
// list came out broken.
func TestDeleteRefusesABlankID(t *testing.T) {
	for _, body := range []string{
		`{"ids":["   ","bd-1"],"expected_version":41}`,
		`{"ids":["bd-1",""]}`,
	} {
		deleter := &roleDeleter{}
		ts := newTestServer(t, rolesConfig(Config{Deleter: deleter}))

		resp := ts.delete(t, body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("body %s: status = %d, want 400: %s", body, resp.StatusCode, readAll(t, resp))
			continue
		}
		if problem := decodeBody(t, resp); problem["param"] != deleteIDsMember {
			t.Errorf("body %s: param = %v, want %s — the fault is the id list, not the guard beside it",
				body, problem["param"], deleteIDsMember)
		}
		if calls := deleter.requests(); len(calls) != 0 {
			t.Errorf("body %s: %d deletes reached the role", body, len(calls))
		}
	}
}

// TestDeleteRefusesAStaleGuard is the 409, and the case whose GREEN is bought by
// the arm's placement ABOVE the ErrValidation line in failDeleteErr.
//
// ErrVersionMismatch wraps neither ErrValidation nor ErrNotFound and
// ClassifyError has no row for it, so below that line it is a generic 500 — for
// the one refusal here that reports an irreversible act being stopped.
// Mutation-checked: deleting the arm makes this case fail with 500.
func TestDeleteRefusesAStaleGuard(t *testing.T) {
	deleter := &roleDeleter{err: fmt.Errorf("delete bd-1: %w", issueops.ErrVersionMismatch)}
	ts := newTestServer(t, rolesConfig(Config{Deleter: deleter}))

	resp := ts.delete(t, `{"ids":["bd-1"],"expected_version":9007199254740993}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if body["code"] != string(CodePreconditionFailed) {
		t.Errorf("code = %v, want %s", body["code"], CodePreconditionFailed)
	}
	if body["param"] != expectedVersionMember {
		t.Errorf("param = %v, want %s", body["param"], expectedVersionMember)
	}
	if _, present := body["expected_version"]; !present {
		t.Errorf("the refusal does not echo the guard the request sent: %v", body)
	}
	if _, present := body["actual_version"]; present {
		t.Errorf("actual_version = %v; the refusing transaction rolled back", body["actual_version"])
	}
}

// TestDeleteRefusesAMalformedGuard: the token is an integer and nothing else.
func TestDeleteRefusesAMalformedGuard(t *testing.T) {
	for _, body := range []string{
		`{"ids":["bd-1"],"expected_version":"41"}`,
		`{"ids":["bd-1"],"expected_version":null}`,
		`{"ids":["bd-1"],"expected_version":{}}`,
	} {
		deleter := &roleDeleter{}
		ts := newTestServer(t, rolesConfig(Config{Deleter: deleter}))

		resp := ts.delete(t, body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("body %s: status = %d, want 400: %s", body, resp.StatusCode, readAll(t, resp))
			continue
		}
		if problem := decodeBody(t, resp); problem["param"] != expectedVersionMember {
			t.Errorf("body %s: param = %v, want %s", body, problem["param"], expectedVersionMember)
		}
		if calls := deleter.requests(); len(calls) != 0 {
			t.Errorf("body %s: %d deletes reached the role", body, len(calls))
		}
	}
}
