package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/steveyegge/beads/issueops"
)

// The close guards — template read-only, the pin, the assignee fence — are the
// ROLE's (issueops.Lifecycle.Close and the batch roles that share its body), so
// these cases drive a fake role that refuses the way the real one does and pin
// what the wire makes of it on each of the three close routes. The real role
// behind a real server is the served leg of the conformance contracts
// (internal/httpclient: TestServedLifecycleCloseEnforcesTheCloseGuards and its
// batch siblings), which also proves the client rebuilds the typed error.

// closeGuardCases are the three refusals as a unit-of-work backend returns
// them: wrapped by the use case and the repository, so the mapping has to find
// them by type rather than by being handed the bare value.
var closeGuardCases = []struct {
	name     string
	err      error
	code     Code
	assignee any
}{
	{
		name: "template",
		err:  &issueops.TemplateReadOnlyError{IssueID: "bd-1"},
		code: CodeTemplateReadOnly,
	},
	{
		name: "pinned",
		err:  &issueops.PinnedError{IssueID: "bd-1"},
		code: CodeIssuePinned,
	},
	{
		name:     "not assignee",
		err:      &issueops.CloseNotAssigneeError{IssueID: "bd-1", Assignee: "bob", Actor: "alice"},
		code:     CodeNotAssignee,
		assignee: "bob",
	},
}

func wrapAsUOW(err error) error {
	return fmt.Errorf("close bd-1: db: IssueSQLRepository.CloseChecked bd-1: %w", err)
}

// assertNoRoleProse checks a detail is this server's own words: the role's
// sentence names the actor the caller sent and the holder, and a refusal's
// detail is not where a client reads either.
func assertNoRoleProse(t *testing.T, where string, detail any) {
	t.Helper()
	text, _ := detail.(string)
	if text == "" {
		t.Errorf("%s detail = %v, want this server's own sentence", where, detail)
	}
	for _, leaked := range []string{"cannot modify", "cannot close", "IssueSQLRepository"} {
		if strings.Contains(text, leaked) {
			t.Errorf("%s detail %q carries the role's prose (%q)", where, text, leaked)
		}
	}
}

func TestCloseGuardsAreTypedConflicts(t *testing.T) {
	for _, tc := range closeGuardCases {
		t.Run(tc.name, func(t *testing.T) {
			lifecycle := &roleLifecycle{closeErr: wrapAsUOW(tc.err)}
			ts := newCloseServer(t, lifecycle)

			resp := ts.closeIssue(t, closePath, `{"actor":"alice"}`)
			if resp.StatusCode != http.StatusConflict {
				t.Fatalf("status = %d, want 409: %s", resp.StatusCode, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if body["code"] != string(tc.code) {
				t.Errorf("code = %v, want %s", body["code"], tc.code)
			}
			if body["assignee"] != tc.assignee {
				t.Errorf("assignee = %v, want %v", body["assignee"], tc.assignee)
			}
			if _, ok := body["open_children"]; ok {
				t.Errorf("a close guard carries open_children, which would read as close policy")
			}
			assertNoRoleProse(t, "close", body["detail"])
		})
	}
}

// TestCloseForwardsForceToTheGuards pins that the handler hands `force` to the
// role and decides nothing itself: whether a guard is waived is the role's call.
func TestCloseForwardsForceToTheGuards(t *testing.T) {
	lifecycle := &roleLifecycle{closeErr: wrapAsUOW(&issueops.TemplateReadOnlyError{IssueID: "bd-1"})}
	ts := newCloseServer(t, lifecycle)

	resp := ts.closeIssue(t, closePath, `{"actor":"alice","force":true}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want the role's 409 under force: %s", resp.StatusCode, readAll(t, resp))
	}
	if got := lifecycle.closeRequests(); len(got) != 1 || !got[0].Force {
		t.Fatalf("the role received %+v, want one request carrying Force", got)
	}
}

func TestBatchCloseItemsCarryTheCloseGuards(t *testing.T) {
	outcomes := []issueops.CloseOutcome{{IssueID: "bd-0", Issue: closedIssue("bd-0"), Changed: true}}
	for _, tc := range closeGuardCases {
		outcomes = append(outcomes, issueops.CloseOutcome{IssueID: "bd-1", Err: wrapAsUOW(tc.err)})
	}
	ts := newBatchCloseServer(t, &roleBatchCloser{result: issueops.CloseBatchResult{Outcomes: outcomes}})

	resp := ts.batchClose(t, `{"actor":"alice","items":[{"id":"bd-0"},{"id":"bd-1"},{"id":"bd-1"},{"id":"bd-1"}]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 carrying per-item refusals: %s", resp.StatusCode, readAll(t, resp))
	}
	got := outcomesOf(t, resp)
	if len(got) != 4 {
		t.Fatalf("outcomes = %d, want 4", len(got))
	}
	if _, refused := got[0]["code"]; refused {
		t.Errorf("outcome 0 = %v, want the plain item's success", got[0])
	}
	for i, tc := range closeGuardCases {
		item := got[i+1]
		if item["code"] != string(tc.code) {
			t.Errorf("%s item code = %v, want %s", tc.name, item["code"], tc.code)
		}
		if item["assignee"] != tc.assignee {
			t.Errorf("%s item assignee = %v, want %v", tc.name, item["assignee"], tc.assignee)
		}
		if _, ok := item["issue"]; ok {
			t.Errorf("%s item carries an issue snapshot beside its refusal", tc.name)
		}
		assertNoRoleProse(t, tc.name+" item", item["detail"])
	}
}

func TestApplyBatchCloseItemGuardsAreTypedConflicts(t *testing.T) {
	for _, tc := range closeGuardCases {
		t.Run(tc.name, func(t *testing.T) {
			applier := &roleBatchApplier{err: itemErr(1, issueops.ItemClose, "", "bd-1", wrapAsUOW(tc.err))}
			ts := newApplyBatchServer(t, applier)

			resp := ts.claim(t, batchApplyPath, `{"actor":"alice","items":[{"kind":"create","create":{"title":"one"}}]}`)
			if resp.StatusCode != http.StatusConflict {
				t.Fatalf("status = %d, want 409: %s", resp.StatusCode, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if body["code"] != string(tc.code) {
				t.Errorf("code = %v, want %s", body["code"], tc.code)
			}
			if body["assignee"] != tc.assignee {
				t.Errorf("assignee = %v, want %v", body["assignee"], tc.assignee)
			}
			if body["item_index"] != float64(1) || body["item_kind"] != "close" || body["item_issue_id"] != "bd-1" {
				t.Errorf("item members = %v/%v/%v, want 1/close/bd-1", body["item_index"], body["item_kind"], body["item_issue_id"])
			}
			assertNoRoleProse(t, "applyBatch", body["detail"])
		})
	}
}
