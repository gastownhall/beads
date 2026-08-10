package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// Pure, on a fake ROLE. The wire edge — the body vocabulary at both levels, the
// item bounds, and above all the PER-ITEM OUTCOME PROJECTION — is covered here.
//
// The projection is why this file is the longest of the three in this slice.
// It is the one place on the surface where a role result and a 200 body can
// disagree with no status saying so: an outcome mapped to the wrong shape ships
// a success that never happened, and no gate above it would notice.
//
// What a fake cannot prove is the TRANSACTION — that the survivors of a batch
// with a bad id really did commit — which is TestProxiedServerServeBatchClose
// against real Dolt.

const batchClosePath = "/v0/beads/issues:batchClose"

func (ts *testServer) batchClose(t *testing.T, body string) *http.Response {
	t.Helper()
	return ts.postBody(t, batchClosePath, "application/json", body)
}

func newBatchCloseServer(t *testing.T, closer *roleBatchCloser) *testServer {
	t.Helper()
	return newTestServer(t, rolesConfig(Config{BatchCloser: closer}))
}

func outcomesOf(t *testing.T, resp *http.Response) []map[string]any {
	t.Helper()
	body := decodeBody(t, resp)
	raw, ok := body["outcomes"].([]any)
	if !ok {
		t.Fatalf("outcomes = %#v, want an array", body["outcomes"])
	}
	out := make([]map[string]any, 0, len(raw))
	for i, entry := range raw {
		m, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("outcomes[%d] = %#v, want an object", i, entry)
		}
		out = append(out, m)
	}
	return out
}

// TestBatchCloseForwardsTheRequestAndAnswersEveryItem is the happy path and the
// contract a client walks: one outcome per requested item, in REQUEST ORDER,
// with the id echoed so the array can be read without indexing back.
func TestBatchCloseForwardsTheRequestAndAnswersEveryItem(t *testing.T) {
	closer := &roleBatchCloser{result: issueops.CloseBatchResult{Outcomes: []issueops.CloseOutcome{
		{IssueID: "bd-1", Issue: closedIssue("bd-1"), Changed: true},
		{IssueID: "bd-2", Issue: closedIssue("bd-2"), Changed: false, OpenChildren: 3},
	}}}
	ts := newBatchCloseServer(t, closer)

	resp := ts.batchClose(t, `{"actor":"alice","session":"session-7","force":true,`+
		`"items":[{"id":"bd-1","reason":"shipped"},{"id":"bd-2"}]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}

	got := closer.closeBatchRequests()
	if len(got) != 1 {
		t.Fatalf("the role received %+v, want exactly one request", got)
	}
	req := got[0]
	if req.Actor != "alice" || req.Session != "session-7" || !req.Force {
		t.Errorf("the request-wide members did not reach the role: %+v", req)
	}
	want := []issueops.BatchCloseItem{{IssueID: "bd-1", Reason: "shipped"}, {IssueID: "bd-2"}}
	if len(req.Items) != len(want) || req.Items[0] != want[0] || req.Items[1] != want[1] {
		t.Errorf("Items = %+v, want %+v in request order", req.Items, want)
	}
	// The composed claim is deliberately unpublished, so the handler must never
	// fill it: a claim this operation asked for is one the caller never sent.
	if req.ClaimNext != nil {
		t.Errorf("the handler requested a composed claim the document does not publish: %+v", req.ClaimNext)
	}

	outcomes := outcomesOf(t, resp)
	if len(outcomes) != 2 {
		t.Fatalf("outcomes = %v, want one per requested item", outcomes)
	}
	if outcomes[0]["issue_id"] != "bd-1" || outcomes[1]["issue_id"] != "bd-2" {
		t.Fatalf("outcomes are not in request order: %v", outcomes)
	}
	if outcomes[0]["already_closed"] != false || outcomes[1]["already_closed"] != true {
		t.Errorf("already_closed is not the role's !Changed: %v", outcomes)
	}
	// Reported even for an idempotent re-close, which is the single close's
	// rule and the reason a caller that forced past the guard asked at all.
	if outcomes[1]["open_children"] != float64(3) {
		t.Errorf("open_children = %v, want 3 on the forced re-close", outcomes[1]["open_children"])
	}
	if outcomes[0]["open_children"] != float64(0) {
		t.Errorf("open_children = %v, want 0 present rather than absent on a successful item",
			outcomes[0]["open_children"])
	}
	for i, outcome := range outcomes {
		if _, present := outcome["code"]; present {
			t.Errorf("outcomes[%d] carries a code on a success: %v", i, outcome)
		}
		if _, present := outcome["issue"]; !present {
			t.Errorf("outcomes[%d] carries no issue on a success: %v", i, outcome)
		}
	}
}

// TestBatchCloseReportsItemRefusalsInsideTheBody is the operation's defining
// shape, and the one this surface has nowhere else.
//
// A refused id must NOT take the request down: the survivors commit, so the
// refusal is a RESULT of the batch rather than an error of it, and it travels
// as a member of that item's outcome inside a 200. A client reads `code` to
// find it — the same vocabulary a problem document publishes, one scope down.
func TestBatchCloseReportsItemRefusalsInsideTheBody(t *testing.T) {
	closer := &roleBatchCloser{result: issueops.CloseBatchResult{Outcomes: []issueops.CloseOutcome{
		{IssueID: "bd-1", Issue: closedIssue("bd-1"), Changed: true},
		{IssueID: "bd-missing", Err: fmt.Errorf("get: %w", issueops.ErrNotFound)},
		{IssueID: "bd-parent", Err: &issueops.CloseOpenChildrenError{IssueID: "bd-parent", OpenChildren: 2}},
		{IssueID: "bd-blocked", Err: fmt.Errorf("%w: bd-blocked", issueops.ErrCloseBlocked)},
	}}}
	ts := newBatchCloseServer(t, closer)

	resp := ts.batchClose(t, `{"actor":"alice","items":[{"id":"bd-1"},{"id":"bd-missing"},`+
		`{"id":"bd-parent"},{"id":"bd-blocked"}]}`)
	// THE STATUS IS THE ASSERTION. Three of four items refused and the answer
	// is still a 200, because the fourth landed.
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: a per-item refusal must not take the request down: %s",
			resp.StatusCode, readAll(t, resp))
	}

	outcomes := outcomesOf(t, resp)
	if len(outcomes) != 4 {
		t.Fatalf("outcomes = %v, want one per requested item including the refusals", outcomes)
	}
	for _, tc := range []struct {
		index        int
		code         any
		openChildren any
	}{
		{index: 0, code: nil},
		{index: 1, code: string(CodeNotFound)},
		// PRESENCE is the discriminator between the two not_closable refusals,
		// exactly as it is on a problem document.
		{index: 2, code: string(CodeNotClosable), openChildren: float64(2)},
		{index: 3, code: string(CodeNotClosable)},
	} {
		outcome := outcomes[tc.index]
		if tc.code == nil {
			if _, present := outcome["code"]; present {
				t.Errorf("outcomes[%d] carries a code on a success: %v", tc.index, outcome)
			}
			continue
		}
		if outcome["code"] != tc.code {
			t.Errorf("outcomes[%d].code = %v, want %v", tc.index, outcome["code"], tc.code)
		}
		// A refused item has no row, and the absence is what makes `code` a
		// safe discriminator: a client that branched on `issue` would read the
		// same absence for both.
		if _, present := outcome["issue"]; present {
			t.Errorf("outcomes[%d] carries an issue on a refusal: %v", tc.index, outcome)
		}
		if _, present := outcome["already_closed"]; present {
			t.Errorf("outcomes[%d] carries already_closed on a refusal: %v", tc.index, outcome)
		}
		got, present := outcome["open_children"]
		switch {
		case tc.openChildren == nil && present:
			t.Errorf("outcomes[%d] carries open_children=%v; its absence is how a client reads the blocker refusal",
				tc.index, got)
		case tc.openChildren != nil && got != tc.openChildren:
			t.Errorf("outcomes[%d].open_children = %v, want %v", tc.index, got, tc.openChildren)
		}
		if detail, _ := outcome["detail"].(string); detail == "" {
			t.Errorf("outcomes[%d] carries no detail beside its code: %v", tc.index, outcome)
		}
	}
}

// TestBatchCloseDoesNotReportAnUnknownItemErrorAsSuccess is the projection's
// fail-closed arm. The role documents three item refusals; anything else must
// still arrive as a REFUSAL, because the alternative shape — a success carrying
// no row — is one a client cannot tell from a real close.
func TestBatchCloseDoesNotReportAnUnknownItemErrorAsSuccess(t *testing.T) {
	closer := &roleBatchCloser{result: issueops.CloseBatchResult{Outcomes: []issueops.CloseOutcome{
		{IssueID: "bd-1", Err: fmt.Errorf("a refusal no vocabulary here names")},
	}}}
	ts := newBatchCloseServer(t, closer)

	resp := ts.batchClose(t, `{"actor":"alice","items":[{"id":"bd-1"}]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	outcome := outcomesOf(t, resp)[0]
	if outcome["code"] != string(CodeInternal) {
		t.Errorf("code = %v, want %s for an unclassified item refusal", outcome["code"], CodeInternal)
	}
	if _, present := outcome["already_closed"]; present {
		t.Errorf("an unclassified refusal was reported as a success: %v", outcome)
	}
	// The role's own message must not travel: it is the same disclosure rule
	// every 5xx detail on this surface follows.
	if detail, _ := outcome["detail"].(string); strings.Contains(detail, "no vocabulary here names") {
		t.Errorf("detail quotes the role's message: %q", detail)
	}
}

// TestBatchCloseRefusesTheRequestBeforeItRuns walks everything that means the
// batch NEVER RAN. Each case is a 400 with a `param` that names the offender —
// indexed at the item level, so an offender in a hundred-item request is found
// without a search — and none of them reaches the role.
func TestBatchCloseRefusesTheRequestBeforeItRuns(t *testing.T) {
	long := strings.Repeat("x", types.MaxFieldLen+1)
	for _, tc := range []struct {
		name  string
		body  string
		param string
	}{
		{name: "an unknown request member", body: `{"actor":"a","items":[{"id":"bd-1"}],"claim_next":{}}`, param: "claim_next"},
		{name: "an unknown item member", body: `{"actor":"a","items":[{"id":"bd-1","force":true}]}`, param: "items[0].force"},
		{name: "a missing actor", body: `{"items":[{"id":"bd-1"}]}`, param: claimActorMember},
		{name: "a missing items", body: `{"actor":"a"}`, param: "items"},
		{name: "an empty items", body: `{"actor":"a","items":[]}`, param: "items"},
		{name: "items that is not an array", body: `{"actor":"a","items":{}}`, param: "items"},
		{name: "an item that is not an object", body: `{"actor":"a","items":["bd-1"]}`, param: "items"},
		{name: "an item with no id", body: `{"actor":"a","items":[{"reason":"r"}]}`, param: "items[0].id"},
		{name: "an item with a blank id", body: `{"actor":"a","items":[{"id":""}]}`, param: "items[0].id"},
		// U+0085 is NEL, a line break on a VT-conformant terminal, and the
		// dispatcher refuses it on the single close's path for that reason. This
		// operation has no dispatcher, so the check lives on the item.
		{name: "an id carrying a control character", body: "{\"actor\":\"a\",\"items\":[{\"id\":\"bd-1\\u0085x\"}]}", param: "items[0].id"},
		{name: "an over-long id", body: `{"actor":"a","items":[{"id":"` + long + `"}]}`, param: "items[0].id"},
		{name: "an over-long reason", body: `{"actor":"a","items":[{"id":"bd-1","reason":"` + long + `"}]}`, param: "items[0].reason"},
		{name: "a reason carrying a newline", body: `{"actor":"a","items":[{"id":"bd-1","reason":"a\nbd: closed by mallory"}]}`, param: "items[0].reason"},
		{name: "the offending item is named by index", body: `{"actor":"a","items":[{"id":"bd-1"},{"id":""}]}`, param: "items[1].id"},
		{name: "an over-long session", body: `{"actor":"a","items":[{"id":"bd-1"}],"session":"` + long + `"}`, param: "session"},
		{name: "a non-boolean force", body: `{"actor":"a","items":[{"id":"bd-1"}],"force":"yes"}`, param: "force"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			closer := &roleBatchCloser{}
			ts := newBatchCloseServer(t, closer)

			resp := ts.batchClose(t, tc.body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if body["code"] != string(CodeInvalidArgument) {
				t.Errorf("code = %v, want %s", body["code"], CodeInvalidArgument)
			}
			if body["param"] != tc.param {
				t.Errorf("param = %v, want %q", body["param"], tc.param)
			}
			// THE BATCH NEVER RAN, which is what a non-2xx from this operation
			// promises. A refusal that had already reached the role would make
			// that promise false.
			if got := closer.closeBatchRequests(); len(got) != 0 {
				t.Errorf("a refused request reached the role: %+v", got)
			}
		})
	}
}

// TestBatchCloseCapsTheItemCount bounds how long one request may hold a write
// transaction, the batch create's rule and its reason.
func TestBatchCloseCapsTheItemCount(t *testing.T) {
	items := make([]string, maxBatchCloseItems+1)
	for i := range items {
		items[i] = fmt.Sprintf(`{"id":"bd-%d"}`, i)
	}
	closer := &roleBatchCloser{}
	ts := newBatchCloseServer(t, closer)

	resp := ts.batchClose(t, `{"actor":"alice","items":[`+strings.Join(items, ",")+`]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	if body := decodeBody(t, resp); body["param"] != "items" {
		t.Errorf("param = %v, want items", body["param"])
	}
	if got := closer.closeBatchRequests(); len(got) != 0 {
		t.Errorf("an over-long batch reached the role: %+v", got)
	}
}

// TestBatchCloseRefusesACloserThatMiscountsItsOutcomes pins checkedBatchCloser's
// whole-batch half, and it is the one refusal on this operation that is NOT a
// per-item outcome.
//
// The array is positional: a client walks it against its own argument list, so
// an outcome count that does not match the request cannot be attributed to any
// item at all. Projecting it anyway would report item N's answer under item
// N+1's id for the rest of the array — silently, inside a 200. So it is
// checkedBatchCreator's whole-batch refusal exactly: the generic 500, and never
// a partial projection.
func TestBatchCloseRefusesACloserThatMiscountsItsOutcomes(t *testing.T) {
	for _, test := range []struct {
		name     string
		outcomes []issueops.CloseOutcome
	}{
		{"fewer outcomes than items", []issueops.CloseOutcome{
			{IssueID: "bd-1", Issue: closedIssue("bd-1"), Changed: true},
		}},
		{"more outcomes than items", []issueops.CloseOutcome{
			{IssueID: "bd-1", Issue: closedIssue("bd-1"), Changed: true},
			{IssueID: "bd-2", Issue: closedIssue("bd-2"), Changed: true},
			{IssueID: "bd-3", Issue: closedIssue("bd-3"), Changed: true},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ts := newBatchCloseServer(t, &roleBatchCloser{
				result: issueops.CloseBatchResult{Outcomes: test.outcomes},
			})

			resp := ts.batchClose(t, `{"actor":"alice","items":[{"id":"bd-1"},{"id":"bd-2"}]}`)
			if resp.StatusCode != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500: %s", resp.StatusCode, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if body["code"] != string(CodeInternal) {
				t.Errorf("code = %v, want %s", body["code"], CodeInternal)
			}
			// The body is a problem document rather than a short or long array:
			// a client that read `outcomes` positionally would misattribute
			// every entry after the first mismatch.
			if _, present := body["outcomes"]; present {
				t.Errorf("a miscounted result was projected onto the wire anyway: %v", body)
			}
			// The fault has to arrive as an ERROR NAMING ITSELF, which is what
			// makes this different from the same status reached by a recovered
			// panic — checkedReleaser's rule, and the counts say which way the
			// role was wrong.
			line := findLogLine(t, ts.stderr.String(), "the closer reported")
			if !strings.Contains(line, "for 2 items") {
				t.Errorf("the logged fault does not name the request it answered:\n%s", line)
			}
		})
	}
}

// TestBatchCloseFoldsAnOutcomeThatIsNeitherAnIssueNorARefusal pins
// checkedBatchCloser's per-item half.
//
// The document says `code`'s absence means the item succeeded AND that `issue`
// is present, so an outcome carrying neither a row nor a refusal has no honest
// wire shape: projected as it stands it is a success with no row, which is the
// exact shape closeOutcome's `default` branch already refuses to produce for an
// unrecognized item error.
//
// It is folded into THAT ITEM rather than failing the request, and the batch's
// own contract is why: the survivors have already committed, so a whole-batch
// 500 would hide durable closes from a caller that cannot re-read them. The
// count above has no such reading — a miscounted array says nothing trustworthy
// about any item — which is what puts the two halves at different scopes.
func TestBatchCloseFoldsAnOutcomeThatIsNeitherAnIssueNorARefusal(t *testing.T) {
	ts := newBatchCloseServer(t, &roleBatchCloser{result: issueops.CloseBatchResult{
		Outcomes: []issueops.CloseOutcome{
			{IssueID: "bd-1", Issue: closedIssue("bd-1"), Changed: true},
			{IssueID: "bd-2", Changed: true},
		},
	}})

	resp := ts.batchClose(t, `{"actor":"alice","items":[{"id":"bd-1"},{"id":"bd-2"}]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: a post-commit fault on one item must not hide the others: %s",
			resp.StatusCode, readAll(t, resp))
	}
	outcomes := outcomesOf(t, resp)
	if len(outcomes) != 2 {
		t.Fatalf("outcomes = %v, want one per requested item", outcomes)
	}
	// The item that landed is untouched, which is the whole reason the fold is
	// per-item.
	if _, present := outcomes[0]["code"]; present {
		t.Errorf("the item that landed was reported as a refusal: %v", outcomes[0])
	}
	if _, present := outcomes[0]["issue"]; !present {
		t.Errorf("the item that landed lost its row: %v", outcomes[0])
	}
	if outcomes[1]["code"] != string(CodeInternal) {
		t.Errorf("code = %v, want %s for an outcome with neither an issue nor a refusal",
			outcomes[1]["code"], CodeInternal)
	}
	if _, present := outcomes[1]["already_closed"]; present {
		t.Errorf("an outcome with no row was reported as a success: %v", outcomes[1])
	}
}

// TestBatchCloseLeavesTheRolesOwnResultAlone is the fold's other half. The
// outcomes slice belongs to the role, so correcting it in place would carry
// this server's correction into whatever the role hands back next — which a
// role that answers from one prepared result does on its very next request.
func TestBatchCloseLeavesTheRolesOwnResultAlone(t *testing.T) {
	closer := &roleBatchCloser{result: issueops.CloseBatchResult{
		Outcomes: []issueops.CloseOutcome{{IssueID: "bd-1", Changed: true}},
	}}
	ts := newBatchCloseServer(t, closer)

	const body = `{"actor":"alice","items":[{"id":"bd-1"}]}`
	if resp := ts.batchClose(t, body); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	if err := closer.result.Outcomes[0].Err; err != nil {
		t.Fatalf("the wrapper wrote its correction into the role's own result: %v", err)
	}
	// The second request is the one that would read a poisoned fixture, and it
	// must reach the same answer as the first.
	resp := ts.batchClose(t, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second request: status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	if outcome := outcomesOf(t, resp)[0]; outcome["code"] != string(CodeInternal) {
		t.Errorf("second request: code = %v, want %s", outcome["code"], CodeInternal)
	}
}

// TestBatchCloseAdmitsADuplicateID: `bd close a b a` is a plausible typo, not a
// failure. The role closes in order and reports the second occurrence as an
// idempotent re-close at its OWN index, so the handler's job is to forward the
// duplicate rather than deduplicate it — which would silently shorten the
// outcomes array a client indexes against its own arguments.
func TestBatchCloseAdmitsADuplicateID(t *testing.T) {
	closer := &roleBatchCloser{result: issueops.CloseBatchResult{Outcomes: []issueops.CloseOutcome{
		{IssueID: "bd-1", Issue: closedIssue("bd-1"), Changed: true},
		{IssueID: "bd-1", Issue: closedIssue("bd-1"), Changed: false},
	}}}
	ts := newBatchCloseServer(t, closer)

	resp := ts.batchClose(t, `{"actor":"alice","items":[{"id":"bd-1"},{"id":"bd-1"}]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	got := closer.closeBatchRequests()
	if len(got) != 1 || len(got[0].Items) != 2 {
		t.Fatalf("the role received %+v, want both occurrences", got)
	}
	if outcomes := outcomesOf(t, resp); len(outcomes) != 2 || outcomes[1]["already_closed"] != true {
		t.Errorf("outcomes = %v, want the second occurrence reported as an idempotent re-close", outcomes)
	}
}

// TestBatchCloseMapsTheRolesValidationToA400 covers the defensive arm: an empty
// actor, an empty item list and a blank id are all refused at the edge, so
// nothing should reach it — but a 500 for a request the caller could have fixed
// is the wrong answer if that changes.
func TestBatchCloseMapsTheRolesValidationToA400(t *testing.T) {
	ts := newBatchCloseServer(t, &roleBatchCloser{
		err: fmt.Errorf("%w: close batch requires an actor", issueops.ErrValidation),
	})

	resp := ts.batchClose(t, `{"actor":"alice","items":[{"id":"bd-1"}]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	if body := decodeBody(t, resp); body["code"] != string(CodeInvalidArgument) {
		t.Errorf("code = %v, want %s", body["code"], CodeInvalidArgument)
	}
}

// TestBatchClosePublishesNoItemRefusalAsAProblemCode states the division as a
// test: everything an ITEM can earn lives in that item's outcome, so this
// operation's problem vocabulary must carry neither of the item codes. A 404
// here would say the operation went to the wrong place and a 409 would say the
// whole batch was refused, and neither is ever true of a per-item refusal.
func TestBatchClosePublishesNoItemRefusalAsAProblemCode(t *testing.T) {
	for _, code := range operationCodes[OpBatchCloseIssues] {
		if code == CodeNotFound || code == CodeNotClosable {
			t.Errorf("batchCloseIssues documents %s as a PROBLEM code; item refusals travel in the 200", code)
		}
	}
}

// TestBatchClosePathReachesItsHandler drives the documented path. The literal
// shares a prefix with the claim's wildcard, and ServeMux precedence is what
// keeps it from being parsed as a claim of an issue called ":batchClose".
func TestBatchClosePathReachesItsHandler(t *testing.T) {
	closer := &roleBatchCloser{result: issueops.CloseBatchResult{Outcomes: []issueops.CloseOutcome{
		{IssueID: "bd-1", Issue: closedIssue("bd-1"), Changed: true},
	}}}
	ts := newBatchCloseServer(t, closer)

	if resp := ts.batchClose(t, `{"actor":"alice","items":[{"id":"bd-1"}]}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("POST the documented batchClose path: status = %d, want 200", resp.StatusCode)
	}
	line := findLogLine(t, ts.stderr.String(), "path="+batchClosePath)
	if !strings.Contains(line, "op="+OpBatchCloseIssues) {
		t.Errorf("the documented batchClose path is served by another operation:\n%s", line)
	}
}
