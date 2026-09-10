package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// These are pure: the claimNext path runs end to end over a real listener
// against a fake ROLE, so the wire edge — the filter decode, the `limit`
// refusal, the body vocabulary, the absent-row answer — is covered on every
// pull request.
//
// What a fake cannot prove is the ATOMICITY that is the operation's entire
// reason for existing, and it is owned where it can be: the role-level contract
// against a real store (backend/conformance/ready_claimer_contract.go, run by
// internal/storage/uow). This slice cites that rather than pretending a fake
// can race itself.

const claimNextPath = "/v0/beads/issues:claimNext"

func (ts *testServer) claimNext(t *testing.T, path, body string) *http.Response {
	t.Helper()
	return ts.postBody(t, path, "application/json", body)
}

func newClaimNextServer(t *testing.T, claimer *roleReadyClaimer) *testServer {
	t.Helper()
	return newTestServer(t, rolesConfig(Config{ReadyClaimer: claimer}))
}

func readyRow(id string) *issueops.IssueWithCounts {
	return &issueops.IssueWithCounts{Issue: seededIssue(id, "poller", types.StatusInProgress)}
}

// TestClaimNextAnswersWithTheRowItWon is the happy path: the role receives the
// actor, and the response carries the hydrated row under `claimed`.
func TestClaimNextAnswersWithTheRowItWon(t *testing.T) {
	claimer := &roleReadyClaimer{result: issueops.ClaimNextResult{Claimed: readyRow("bd-1")}}
	ts := newClaimNextServer(t, claimer)

	resp := ts.claimNext(t, claimNextPath, `{"actor":"poller"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	got := claimer.claimNextRequests()
	if len(got) != 1 || got[0].Actor != "poller" {
		t.Fatalf("the role received %+v, want exactly one claim by poller", got)
	}
	body := decodeBody(t, resp)
	claimed, ok := body["claimed"].(map[string]any)
	if !ok {
		t.Fatalf("claimed = %#v, want an object", body["claimed"])
	}
	if claimed["id"] != "bd-1" {
		t.Errorf("claimed.id = %v, want bd-1", claimed["id"])
	}
}

// TestClaimNextAnswersAbsenceForADrainedQueue is the operation's other normal
// answer, and the one a polling client is written against.
//
// A drained queue is the STEADY STATE of a work loop, not a failure, so it is a
// 200 with the member absent rather than a 404 or a 409. `claimIssue` 404s an
// id that names nothing because it was asked about a ROW; this one was asked a
// question whose honest answer can be "none".
func TestClaimNextAnswersAbsenceForADrainedQueue(t *testing.T) {
	claimer := &roleReadyClaimer{result: issueops.ClaimNextResult{Claimed: nil}}
	ts := newClaimNextServer(t, claimer)

	resp := ts.claimNext(t, claimNextPath, `{"actor":"poller"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 for an empty ready front: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if _, present := body["claimed"]; present {
		t.Errorf("claimed = %#v; absence is the whole signal and a null is a second spelling of it", body["claimed"])
	}
	// And nothing else is invented beside it. A boolean or a scan count would
	// be a second member carrying the same fact, which is a second member that
	// can disagree with the first.
	if len(body) != 0 {
		t.Errorf("the empty answer carries %v; `claimed` is the only member this response has", body)
	}
}

// TestClaimNextForwardsTheListingsFilters is the guarantee the shared decoder
// exists for, driven through a real request: every filter the ready listing
// admits reaches the role, spelled the same way.
func TestClaimNextForwardsTheListingsFilters(t *testing.T) {
	claimer := &roleReadyClaimer{result: issueops.ClaimNextResult{Claimed: readyRow("bd-1")}}
	ts := newClaimNextServer(t, claimer)

	query := "?assignee=alice&unassigned=true&type=task&exclude_type=gate" +
		"&label=urgent&label_any=a&exclude_label=wip&label_pattern=re*&label_regex=^re" +
		"&priority=1&parent=bd-9&metadata_field=k%3Dv&has_metadata_key=owner" +
		"&include_ephemeral=true&include_deferred=true&sort=oldest"
	resp := ts.claimNext(t, claimNextPath+query, `{"actor":"poller"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	got := claimer.claimNextRequests()
	if len(got) != 1 {
		t.Fatalf("the role received %+v, want exactly one request", got)
	}
	f := got[0].Filter
	switch {
	case f.Assignee != "alice", !f.Unassigned, f.IssueType != "task":
		t.Errorf("the identity filters did not reach the role: %+v", f)
	case len(f.ExcludeTypes) != 1, len(f.Labels) != 1, len(f.LabelsAny) != 1, len(f.ExcludeLabels) != 1:
		t.Errorf("the list filters did not reach the role: %+v", f)
	case f.LabelPattern != "re*", f.LabelRegex != "^re":
		t.Errorf("the label matchers did not reach the role: %+v", f)
	case f.Priority == nil || *f.Priority != 1, f.ParentID != "bd-9":
		t.Errorf("the graph filters did not reach the role: %+v", f)
	case f.MetadataFields["k"] != "v", f.HasMetadataKey != "owner":
		t.Errorf("the metadata filters did not reach the role: %+v", f)
	case !f.IncludeEphemeral, !f.IncludeDeferred:
		t.Errorf("the plane filters did not reach the role: %+v", f)
	case f.Sort != "oldest":
		t.Errorf("Sort = %q, want oldest", f.Sort)
	}
	// The role refuses both, and this operation publishes no spelling for
	// either, so the request it builds must never carry one.
	if f.Limit != nil || f.Offset != 0 {
		t.Errorf("the handler sent a page to a role that refuses one: limit=%v offset=%d", f.Limit, f.Offset)
	}
}

// TestClaimNextSendsAnExplicitSortPolicy pins the value rather than the
// constant, for the reason handleReady's own test gives: an assertion against
// the value the handler read would hold for whatever value it took.
//
// It matters more here than on the listing. There, the sort decides what order
// rows are PRINTED in; here it decides which row the operation WRITES to.
func TestClaimNextSendsAnExplicitSortPolicy(t *testing.T) {
	claimer := &roleReadyClaimer{result: issueops.ClaimNextResult{Claimed: readyRow("bd-1")}}
	ts := newClaimNextServer(t, claimer)

	if resp := ts.claimNext(t, claimNextPath, `{"actor":"poller"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	got := claimer.claimNextRequests()
	if len(got) != 1 || got[0].Filter.Sort != "priority" {
		t.Fatalf("Sort = %q, want the document's default sent explicitly; an empty policy adopts storage's hybrid fallback",
			got[0].Filter.Sort)
	}
}

// TestClaimNextRefusesALimitByValue is the one parameter this operation turns
// down that its sibling accepts, and the refusal's REASON is the assertion.
//
// `unknown_parameter` would tell a client "this server does not know that name
// — version skew, degrade or fall back", which is false: the name is one this
// server knows perfectly well on the listing. `invalid_value` says what is
// true, which is that this operation will not act on it.
func TestClaimNextRefusesALimitByValue(t *testing.T) {
	claimer := &roleReadyClaimer{}
	ts := newClaimNextServer(t, claimer)

	for _, query := range []string{"?limit=1", "?limit=0", "?limit=", "?limit=abc"} {
		t.Run(query, func(t *testing.T) {
			resp := ts.claimNext(t, claimNextPath+query, `{"actor":"poller"}`)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if body["code"] != string(CodeInvalidArgument) || body["param"] != "limit" {
				t.Errorf("code/param = %v/%v, want %s on `limit`", body["code"], body["param"], CodeInvalidArgument)
			}
			if body["reason"] != string(ReasonInvalidValue) {
				t.Errorf("reason = %v, want %s: the name is one this server knows, it is the ACTION it refuses",
					body["reason"], ReasonInvalidValue)
			}
		})
	}
	if got := claimer.claimNextRequests(); len(got) != 0 {
		t.Errorf("a refused limit reached the role: %+v", got)
	}
}

// TestClaimNextRefusesUnknownAndMalformedBodies is the body vocabulary. The
// FILTER cases are the point: a client that put its filter in the body must
// learn so by name rather than have it silently ignored, which would hand it a
// claim from the unfiltered ready front.
func TestClaimNextRefusesUnknownAndMalformedBodies(t *testing.T) {
	for _, tc := range []struct {
		name  string
		body  string
		param string
	}{
		{name: "a filter member in the body", body: `{"actor":"poller","label":["urgent"]}`, param: "label"},
		{name: "a filter object in the body", body: `{"actor":"poller","filter":{}}`, param: "filter"},
		{name: "a missing actor", body: `{}`, param: claimActorMember},
		{name: "an actor that is blank after trimming", body: `{"actor":"  "}`, param: claimActorMember},
		{name: "an actor carrying a newline", body: `{"actor":"a\nbd: claimed by mallory"}`, param: claimActorMember},
		{name: "a non-string actor", body: `{"actor":7}`, param: claimActorMember},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claimer := &roleReadyClaimer{}
			ts := newClaimNextServer(t, claimer)

			resp := ts.claimNext(t, claimNextPath, tc.body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
			}
			if body := decodeBody(t, resp); body["param"] != tc.param {
				t.Errorf("param = %v, want %q", body["param"], tc.param)
			}
			if got := claimer.claimNextRequests(); len(got) != 0 {
				t.Errorf("a refused body reached the role: %+v", got)
			}
		})
	}
}

// TestClaimNextMapsTheRolesValidationToA400 covers the defensive arm. Nothing
// should reach it — the edge refuses the empty actor and this operation has no
// spelling for the two other fields the role turns down — but a 500 for a
// request the caller could have fixed is the wrong answer if that changes.
func TestClaimNextMapsTheRolesValidationToA400(t *testing.T) {
	ts := newClaimNextServer(t, &roleReadyClaimer{
		err: fmt.Errorf("%w: claim next does not take a limit", issueops.ErrValidation),
	})

	resp := ts.claimNext(t, claimNextPath, `{"actor":"poller"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	if body := decodeBody(t, resp); body["code"] != string(CodeInvalidArgument) {
		t.Errorf("code = %v, want %s", body["code"], CodeInvalidArgument)
	}
}

// TestClaimNextHasNoConflictAndNoNotFound states the operation's negative space
// as a test. A row a racing agent took is not in the set this claim scanned, so
// there is nothing to 409 about; there is no id in the path, so there is
// nothing to 404 about. Both absences are what a client dispatches on.
func TestClaimNextHasNoConflictAndNoNotFound(t *testing.T) {
	for _, code := range operationCodes[OpClaimNextIssue] {
		switch code {
		case CodeNotFound, CodeAlreadyClaimed, CodeNotClaimable:
			t.Errorf("claimNext documents %s; it names no row, so it can refuse for neither reason", code)
		}
	}
}

// TestClaimNextPathReachesItsHandler drives the documented path, which is the
// one thing route parity cannot check: this literal shares a prefix with the
// claim's wildcard, and ServeMux precedence is what keeps it from being parsed
// as a claim of an issue called ":claimNext".
func TestClaimNextPathReachesItsHandler(t *testing.T) {
	claimer := &roleReadyClaimer{result: issueops.ClaimNextResult{Claimed: readyRow("bd-1")}}
	ts := newClaimNextServer(t, claimer)

	if resp := ts.claimNext(t, claimNextPath, `{"actor":"poller"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("POST the documented claimNext path: status = %d, want 200", resp.StatusCode)
	}
	line := findLogLine(t, ts.stderr.String(), "path="+claimNextPath)
	if !strings.Contains(line, "op="+OpClaimNextIssue) {
		t.Errorf("the documented claimNext path is served by another operation:\n%s", line)
	}
	if got := claimer.claimNextRequests(); len(got) != 1 {
		t.Errorf("the documented path did not reach the role: %+v", got)
	}
}
