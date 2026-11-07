package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/steveyegge/beads/issueops"
)

// The template guard is the ROLE's (issueops.Lifecycle.Update, and the batch
// applier's update items, which share its body), so these cases drive a fake
// role that refuses the way the real one does and pin what the wire makes of
// it. The real role behind a real server is the served leg of the conformance
// contracts (internal/httpclient: TestServedLifecycleUpdateRefusesATemplate and
// TestServedBatchApplyUpdateItemsRefuseATemplate), which also proves the client
// rebuilds the typed error.

// templateRefusal is the refusal as a unit-of-work backend returns it: wrapped,
// so the mapping has to find it by type rather than by being handed the value.
func templateRefusal() error {
	return fmt.Errorf("update bd-1: %w", &issueops.TemplateReadOnlyError{IssueID: "bd-1"})
}

func assertTemplateDetailIsOurs(t *testing.T, detail any) {
	t.Helper()
	text, _ := detail.(string)
	if text == "" || strings.Contains(text, "cannot modify template") {
		t.Errorf("detail = %q, want this server's own sentence rather than the role's", text)
	}
}

func TestUpdateOfATemplateIsATypedConflict(t *testing.T) {
	lifecycle := &roleLifecycle{updateErr: templateRefusal()}
	ts := newUpdateServer(t, lifecycle)

	resp := ts.updateIssue(t, updatePath, `{"actor":"alice","patch":{"title":"edited"},"force_close_policy":true}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if body["code"] != string(CodeTemplateReadOnly) {
		t.Errorf("code = %v, want %s", body["code"], CodeTemplateReadOnly)
	}
	assertTemplateDetailIsOurs(t, body["detail"])
	// The handler decides nothing: the request reached the role whole, force
	// flag included, and the refusal is the role's.
	if got := lifecycle.updateRequests(); len(got) != 1 || !got[0].ForceClosePolicy {
		t.Fatalf("the role received %+v, want one request carrying ForceClosePolicy", got)
	}
}

func TestApplyBatchUpdateItemOnATemplateIsATypedConflict(t *testing.T) {
	applier := &roleBatchApplier{err: itemErr(1, issueops.ItemUpdate, "", "bd-1", templateRefusal())}
	ts := newApplyBatchServer(t, applier)

	resp := ts.claim(t, batchApplyPath, `{"actor":"alice","items":[{"kind":"create","create":{"title":"one"}}]}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if body["code"] != string(CodeTemplateReadOnly) {
		t.Errorf("code = %v, want %s", body["code"], CodeTemplateReadOnly)
	}
	if body["item_index"] != float64(1) || body["item_kind"] != "update" || body["item_issue_id"] != "bd-1" {
		t.Errorf("item members = %v/%v/%v, want 1/update/bd-1", body["item_index"], body["item_kind"], body["item_issue_id"])
	}
	assertTemplateDetailIsOurs(t, body["detail"])
}

// TestUpdateCarriesAllowTemplateToTheRole pins that `allow_template` is decoded
// onto the role request and nothing else: the role owns what it waives.
func TestUpdateCarriesAllowTemplateToTheRole(t *testing.T) {
	for _, tc := range []struct {
		body string
		want bool
	}{
		{`{"actor":"alice","patch":{"title":"edited"},"allow_template":true}`, true},
		{`{"actor":"alice","patch":{"title":"edited"},"allow_template":false}`, false},
		{`{"actor":"alice","patch":{"title":"edited"}}`, false},
	} {
		lifecycle := &roleLifecycle{updateResult: issueops.UpdateResult{Issue: updatedIssue("bd-1"), Changed: true}}
		ts := newUpdateServer(t, lifecycle)
		resp := ts.updateIssue(t, updatePath, tc.body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200: %s", tc.body, resp.StatusCode, readAll(t, resp))
		}
		_ = resp.Body.Close()
		got := lifecycle.updateRequests()
		if len(got) != 1 || got[0].AllowTemplate != tc.want {
			t.Errorf("%s: the role received %+v, want AllowTemplate=%v", tc.body, got, tc.want)
		}
	}
}

func TestUpdateRefusesANonBooleanAllowTemplate(t *testing.T) {
	lifecycle := &roleLifecycle{}
	ts := newUpdateServer(t, lifecycle)
	resp := ts.updateIssue(t, updatePath, `{"actor":"alice","patch":{"title":"edited"},"allow_template":"yes"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	if got := lifecycle.updateRequests(); len(got) != 0 {
		t.Errorf("the role was reached with %+v", got)
	}
}
