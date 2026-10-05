package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// These cover the transport half of POST /v0/beads/issues:batchGet — the body
// shape, this operation's own bounds, and how the role's result is projected
// onto the wire. Everything below the wire (the cap, deduplication, snapshot
// sharing, labels-only hydration) is issueops.BatchGetter's, pinned by
// backend/conformance/batch_getter_contract.go at all three backends.

const batchGetPath = "/v0/beads/issues:batchGet"

func newBatchGetServer(t *testing.T, getter *roleBatchGetter) *testServer {
	t.Helper()
	return newTestServer(t, rolesConfig(Config{BatchGetter: getter}))
}

func batchGetIssue(id string, rowVersion int64) *types.Issue {
	return &types.Issue{
		ID:         id,
		Title:      "title for " + id,
		Priority:   1,
		IssueType:  types.TypeTask,
		Status:     types.StatusOpen,
		Labels:     []string{"api"},
		RowVersion: rowVersion,
		CreatedAt:  time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC),
		UpdatedAt:  time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC),
	}
}

// TestBatchGetIssuesAnswersWithRevision pins HIGH item 1: each item in
// `issues` carries its own `revision`, projected from the issue's RowVersion
// the same way IssueDetails.Revision is for GET /v0/beads/issues/{id}. The
// fixture uses guardToken, past 2^53, so a response member that had been
// decoded (or encoded) as a float64 anywhere on this path would read back a
// NEARBY number rather than the real one.
func TestBatchGetIssuesAnswersWithRevision(t *testing.T) {
	issue := batchGetIssue("bd-1", guardToken)
	getter := &roleBatchGetter{result: issueops.GetManyResult{
		Issues:  []*types.Issue{issue},
		Missing: []string{},
	}}
	ts := newBatchGetServer(t, getter)

	resp := ts.claim(t, batchGetPath, `{"ids":["bd-1"]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	raw := readAll(t, resp)
	var body struct {
		Issues []struct {
			ID       *string `json:"id"`
			Revision *string `json:"revision"`
		} `json:"issues"`
		Missing []string `json:"missing"`
	}
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	if len(body.Issues) != 1 {
		t.Fatalf("issues = %v, want 1 entry: %s", body.Issues, raw)
	}
	got := body.Issues[0]
	if got.ID == nil || *got.ID != "bd-1" {
		t.Errorf("id = %v, want bd-1", got.ID)
	}
	if got.Revision == nil {
		t.Fatalf("the issue carries no `revision`: %s", raw)
	}
	token, err := types.ParseRevisionToken(*got.Revision)
	if err != nil {
		t.Fatalf("parse revision %q: %v", *got.Revision, err)
	}
	if token != guardToken {
		t.Errorf("revision = %d, want the row's %d", token, guardToken)
	}
	if len(body.Missing) != 0 {
		t.Errorf("missing = %v, want none", body.Missing)
	}

	reqs := getter.getManyRequests()
	if len(reqs) != 1 || len(reqs[0].IDs) != 1 || reqs[0].IDs[0] != "bd-1" {
		t.Fatalf("role calls = %+v, want one call naming bd-1", reqs)
	}
}

// TestBatchGetIssuesReportsMissingIDs pins that an id naming no stored row
// comes back in `missing` rather than vanishing or refusing the request, and
// that a found id and a missing one can ride in the same answer.
func TestBatchGetIssuesReportsMissingIDs(t *testing.T) {
	issue := batchGetIssue("bd-1", 1)
	getter := &roleBatchGetter{result: issueops.GetManyResult{
		Issues:  []*types.Issue{issue},
		Missing: []string{"bd-gone"},
	}}
	ts := newBatchGetServer(t, getter)

	resp := ts.claim(t, batchGetPath, `{"ids":["bd-1","bd-gone"]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	missing, _ := body["missing"].([]any)
	if len(missing) != 1 || missing[0] != "bd-gone" {
		t.Errorf("missing = %v, want [bd-gone]", body["missing"])
	}
	issues, _ := body["issues"].([]any)
	if len(issues) != 1 {
		t.Errorf("issues = %v, want exactly the one found row", body["issues"])
	}
}

// TestBatchGetIssuesAnswersEmptyArraysNotNull pins the one thing a Go nil
// slice gets wrong on the way out, for BOTH members: a client is entitled to
// range over `issues` and `missing` without a nil check even when neither
// carries anything.
func TestBatchGetIssuesAnswersEmptyArraysNotNull(t *testing.T) {
	getter := &roleBatchGetter{result: issueops.GetManyResult{
		Issues:  []*types.Issue{},
		Missing: []string{"bd-gone"},
	}}
	ts := newBatchGetServer(t, getter)

	resp := ts.claim(t, batchGetPath, `{"ids":["bd-gone"]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	raw := readAll(t, resp)
	if !strings.Contains(raw, `"issues":[]`) {
		t.Errorf("body = %s, want an empty `issues` array rather than null", raw)
	}

	allMissing := &roleBatchGetter{result: issueops.GetManyResult{
		Issues:  []*types.Issue{},
		Missing: []string{},
	}}
	ts2 := newBatchGetServer(t, allMissing)
	resp2 := ts2.claim(t, batchGetPath, `{"ids":[]}`)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp2.StatusCode, readAll(t, resp2))
	}
	raw2 := readAll(t, resp2)
	if !strings.Contains(raw2, `"missing":[]`) {
		t.Errorf("body = %s, want an empty `missing` array rather than null", raw2)
	}
}

// TestBatchGetIssuesRefusesAnUnknownMember pins that the body is
// additionalProperties: false and the refusal names the offending member: `ids`
// is the whole vocabulary.
func TestBatchGetIssuesRefusesAnUnknownMember(t *testing.T) {
	getter := &roleBatchGetter{}
	ts := newBatchGetServer(t, getter)

	resp := ts.claim(t, batchGetPath, `{"ids":["bd-1"],"dry_run":true}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if body["code"] != string(CodeInvalidArgument) {
		t.Errorf("code = %v, want %q", body["code"], CodeInvalidArgument)
	}
	if body["param"] != "dry_run" {
		t.Errorf("param = %v, want dry_run", body["param"])
	}
	if calls := getter.getManyRequests(); len(calls) != 0 {
		t.Errorf("the role was called %d times for a refused request", len(calls))
	}
}

// TestBatchGetIssuesRefusesAMissingOrNullIDs pins that `ids` is required and
// must actually be an array: absent and explicit null are both refused, before
// the role is ever reached.
func TestBatchGetIssuesRefusesAMissingOrNullIDs(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"no ids member at all", `{}`},
		{"ids is explicitly null", `{"ids":null}`},
		{"ids is a string, not an array", `{"ids":"bd-1"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			getter := &roleBatchGetter{}
			ts := newBatchGetServer(t, getter)

			resp := ts.claim(t, batchGetPath, tc.body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if body["code"] != string(CodeInvalidArgument) {
				t.Errorf("code = %v, want %q", body["code"], CodeInvalidArgument)
			}
			if body["param"] != "ids" {
				t.Errorf("param = %v, want ids", body["param"])
			}
			if calls := getter.getManyRequests(); len(calls) != 0 {
				t.Errorf("the role was called %d times for a refused request", len(calls))
			}
		})
	}
}

// TestBatchGetIssuesRefusesAQueryParameter pins that the document-level
// unknown-parameter rule reaches this operation too: it declares no
// parameters, so every query key is refused.
func TestBatchGetIssuesRefusesAQueryParameter(t *testing.T) {
	getter := &roleBatchGetter{}
	ts := newBatchGetServer(t, getter)

	resp := ts.claim(t, batchGetPath+"?dry_run=true", `{"ids":["bd-1"]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if body["param"] != "dry_run" || body["reason"] != string(ReasonUnknownParameter) {
		t.Errorf("param/reason = %v/%v, want dry_run/unknown_parameter", body["param"], body["reason"])
	}
	if calls := getter.getManyRequests(); len(calls) != 0 {
		t.Errorf("the role was called %d times for a refused request", len(calls))
	}
}

// TestBatchGetIssuesRefusesOverTheCap pins the wire's half of MaxGetManyIDs:
// the fake stands in for the role's own refusal (as
// TestCountDependencyEdgesNamesTheRefusedParameter does for GraphCounter),
// and the handler still answers 400 with the error envelope the operation's
// only member takes, naming the FULL 1001-entry request as sent.
func TestBatchGetIssuesRefusesOverTheCap(t *testing.T) {
	const over = issueops.MaxGetManyIDs + 1
	getter := &roleBatchGetter{err: &issueops.TooManyIDsError{Requested: over, Cap: issueops.MaxGetManyIDs}}
	ts := newBatchGetServer(t, getter)

	ids := make([]string, over)
	for i := range ids {
		ids[i] = fmt.Sprintf(`"bd-%d"`, i)
	}
	resp := ts.claim(t, batchGetPath, `{"ids":[`+strings.Join(ids, ",")+`]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if body["code"] != string(CodeInvalidArgument) {
		t.Errorf("code = %v, want %q", body["code"], CodeInvalidArgument)
	}
	if body["param"] != "ids" {
		t.Errorf("param = %v, want ids", body["param"])
	}
	if body["reason"] != string(ReasonInvalidValue) {
		t.Errorf("reason = %v, want invalid_value", body["reason"])
	}

	reqs := getter.getManyRequests()
	if len(reqs) != 1 {
		t.Fatalf("role calls = %d, want 1: the role, not the handler, is where this is counted", len(reqs))
	}
	if len(reqs[0].IDs) != over {
		t.Errorf("the role received %d ids, want the full %d sent: the handler must not pre-trim the request", len(reqs[0].IDs), over)
	}
}

// TestBatchGetIssuesAcceptsExactlyTheCap pins the wire's half of the
// boundary MED item 4 names at the role: a request at exactly MaxGetManyIDs
// is not refused by anything above the role, so every id reaches it.
func TestBatchGetIssuesAcceptsExactlyTheCap(t *testing.T) {
	const atCap = issueops.MaxGetManyIDs

	ids := make([]string, atCap)
	issues := make([]*types.Issue, atCap)
	for i := range ids {
		id := fmt.Sprintf("bd-%d", i)
		ids[i] = `"` + id + `"`
		issues[i] = batchGetIssue(id, 1)
	}
	getter := &roleBatchGetter{result: issueops.GetManyResult{
		Issues:  issues,
		Missing: []string{},
	}}
	ts := newBatchGetServer(t, getter)

	resp := ts.claim(t, batchGetPath, `{"ids":[`+strings.Join(ids, ",")+`]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("at the cap: status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	reqs := getter.getManyRequests()
	if len(reqs) != 1 || len(reqs[0].IDs) != atCap {
		t.Fatalf("the role received %d ids, want the full %d at the cap", len(reqs[0].IDs), atCap)
	}
}

var _ issueops.BatchGetter = (*roleBatchGetter)(nil)
