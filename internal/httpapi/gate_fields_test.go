package httpapi

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/issueops"
)

// The pins for the gate members: await_type, await_id,
// timeout and waiters on createIssue and on issues:batchApply's create item
// (issues.create.gates), and await_id and waiters on updateIssue's patch and
// on the batchApply update item's patch (issues.update.gates).
//
// The wire is NOT stricter than the role. The handlers decode and project;
// every bound is the role's *issueops.GateFieldError, answered as the
// existing 400 invalid_argument with `param` naming the member at its own
// nesting level. So these cases inject the role's refusal and assert the
// mapping, and separately prove a value the role would accept (a blank waiter,
// a duplicate, an unknown await_type) reaches it untouched.

// gateFieldErr is the role's refusal for one member, spelled as
// internal/storage/issueops.ValidateGateFields spells it.
func gateFieldErr(field string) error {
	return &issueops.GateFieldError{Field: field, Detail: fmt.Sprintf("validation failed: %s is past its bound", field)}
}

func TestCreateForwardsTheGateMembers(t *testing.T) {
	lifecycle := &roleLifecycle{createResult: issueops.CreateResult{Issue: createdIssue("bd-g")}}
	ts := newCreateServer(t, lifecycle)

	// An await_type no allowlist knows, a blank and a repeated waiter: the
	// role stores all of them, so the wire must not refuse any.
	resp := ts.createIssue(t, `{
		"actor": "alice",
		"title": "a gate",
		"issue_type": "gate",
		"await_type": "mail",
		"await_id": "run-42",
		"timeout": 7200000000000,
		"waiters": ["rig/a", "rig/a", ""]
	}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	got := lifecycle.createRequests()
	if len(got) != 1 || got[0].Issue == nil {
		t.Fatalf("the role was called %d times, want 1 with an issue", len(got))
	}
	issue := got[0].Issue
	if issue.AwaitType != "mail" || issue.AwaitID != "run-42" {
		t.Errorf("await_type/await_id = %q/%q, want mail/run-42", issue.AwaitType, issue.AwaitID)
	}
	if issue.Timeout != 2*time.Hour {
		t.Errorf("timeout = %s, want 2h (the member is nanoseconds)", issue.Timeout)
	}
	if !slices.Equal(issue.Waiters, []string{"rig/a", "rig/a", ""}) {
		t.Errorf("waiters = %q, want the list as sent, duplicates and blanks kept", issue.Waiters)
	}

	t.Run("absent members leave the issue's gate fields zero", func(t *testing.T) {
		lifecycle := &roleLifecycle{createResult: issueops.CreateResult{Issue: createdIssue("bd-1")}}
		ts := newCreateServer(t, lifecycle)
		if resp := ts.createIssue(t, `{"actor":"alice","title":"plain"}`); resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
		}
		issue := lifecycle.createRequests()[0].Issue
		if issue.AwaitType != "" || issue.AwaitID != "" || issue.Timeout != 0 || issue.Waiters != nil {
			t.Errorf("gate fields = (%q, %q, %s, %q), want all zero", issue.AwaitType, issue.AwaitID, issue.Timeout, issue.Waiters)
		}
	})
}

func TestCreateRefusesANullGateMember(t *testing.T) {
	for _, member := range []string{"await_type", "await_id", "timeout", "waiters"} {
		t.Run(member, func(t *testing.T) {
			lifecycle := &roleLifecycle{}
			ts := newCreateServer(t, lifecycle)
			resp := ts.createIssue(t, fmt.Sprintf(`{"actor":"alice","title":"one","%s":null}`, member))
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
			}
			if body := decodeBody(t, resp); body["param"] != member {
				t.Errorf("param = %v, want %q", body["param"], member)
			}
			if len(lifecycle.createRequests()) != 0 {
				t.Error("the role was called for a refused request")
			}
		})
	}
}

// TestCreateNamesTheGateMemberTheRoleRefused: the bound is the role's, and the
// 400 names the member its *GateFieldError is about.
func TestCreateNamesTheGateMemberTheRoleRefused(t *testing.T) {
	for _, field := range []string{
		issueops.GateFieldAwaitType, issueops.GateFieldAwaitID, issueops.GateFieldTimeout, issueops.GateFieldWaiters,
	} {
		t.Run(field, func(t *testing.T) {
			lifecycle := &roleLifecycle{createErr: fmt.Errorf("create: %w", gateFieldErr(field))}
			ts := newCreateServer(t, lifecycle)
			resp := ts.createIssue(t, `{"actor":"alice","title":"one","await_type":"timer","await_id":"x","timeout":-1,"waiters":["a"]}`)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if body["code"] != string(CodeInvalidArgument) {
				t.Errorf("code = %v, want %s", body["code"], CodeInvalidArgument)
			}
			if body["param"] != field {
				t.Errorf("param = %v, want %q", body["param"], field)
			}
			if detail, _ := body["detail"].(string); !strings.Contains(detail, field) {
				t.Errorf("detail = %q, want it to name %s", detail, field)
			}
		})
	}
}

func TestUpdateForwardsTheGateMembers(t *testing.T) {
	for _, test := range []struct {
		name        string
		patch       string
		wantAwaitID issueops.Field[string]
		wantWaiters issueops.Field[[]string]
	}{
		{
			name:        "both set",
			patch:       `{"await_id":"bead:bd-9","waiters":["x","x","y"]}`,
			wantAwaitID: issueops.Field[string]{Set: true, Value: "bead:bd-9"},
			wantWaiters: issueops.Field[[]string]{Set: true, Value: []string{"x", "x", "y"}},
		},
		{
			name:        "both cleared",
			patch:       `{"await_id":"","waiters":[]}`,
			wantAwaitID: issueops.Field[string]{Set: true, Value: ""},
			wantWaiters: issueops.Field[[]string]{Set: true, Value: []string{}},
		},
		{
			name:        "waiters alone",
			patch:       `{"waiters":["rig/a"]}`,
			wantWaiters: issueops.Field[[]string]{Set: true, Value: []string{"rig/a"}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			lifecycle := &roleLifecycle{updateResult: issueops.UpdateResult{Issue: updatedIssue("bd-1"), Changed: true}}
			ts := newUpdateServer(t, lifecycle)
			resp := ts.updateIssue(t, updatePath, `{"actor":"alice","patch":`+test.patch+`}`)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
			}
			got := lifecycle.updateRequests()
			if len(got) != 1 {
				t.Fatalf("the role was called %d times, want 1", len(got))
			}
			p := got[0].Patch
			if p.AwaitID != test.wantAwaitID {
				t.Errorf("await_id = %+v, want %+v", p.AwaitID, test.wantAwaitID)
			}
			if p.Waiters.Set != test.wantWaiters.Set || !slices.Equal(p.Waiters.Value, test.wantWaiters.Value) {
				t.Errorf("waiters = %+v, want %+v", p.Waiters, test.wantWaiters)
			}
			if p.Title.Set || p.Status.Set || p.Assignee.Set {
				t.Errorf("the handler set a member the body did not carry: %+v", p)
			}
		})
	}
}

// TestUpdateRefusesTheGateMembersThePatchDoesNotPublish: no local route can
// change await_type or timeout, so neither patch body publishes them, and a
// null on the two it does publish is refused as on every non-nullable member.
func TestUpdateRefusesTheGateMembersThePatchDoesNotPublish(t *testing.T) {
	for _, test := range []struct {
		name, patch, wantParam string
	}{
		{"await_type is unknown", `{"await_type":"timer"}`, "patch.await_type"},
		{"timeout is unknown", `{"timeout":5}`, "patch.timeout"},
		{"await_id is not nullable", `{"await_id":null}`, "patch.await_id"},
		{"waiters is not nullable", `{"waiters":null}`, "patch.waiters"},
	} {
		t.Run(test.name, func(t *testing.T) {
			lifecycle := &roleLifecycle{}
			ts := newUpdateServer(t, lifecycle)
			resp := ts.updateIssue(t, updatePath, `{"actor":"alice","patch":`+test.patch+`}`)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
			}
			if body := decodeBody(t, resp); body["param"] != test.wantParam {
				t.Errorf("param = %v, want %q", body["param"], test.wantParam)
			}
			if len(lifecycle.updateRequests()) != 0 {
				t.Error("the role was called for a refused request")
			}
		})
	}
}

func TestUpdateNamesTheGateMemberTheRoleRefused(t *testing.T) {
	for _, field := range []string{issueops.GateFieldAwaitID, issueops.GateFieldWaiters} {
		t.Run(field, func(t *testing.T) {
			lifecycle := &roleLifecycle{updateErr: gateFieldErr(field)}
			ts := newUpdateServer(t, lifecycle)
			resp := ts.updateIssue(t, updatePath, `{"actor":"alice","patch":{"await_id":"x","waiters":["a"]}}`)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if body["code"] != string(CodeInvalidArgument) {
				t.Errorf("code = %v, want %s", body["code"], CodeInvalidArgument)
			}
			if want := "patch." + field; body["param"] != want {
				t.Errorf("param = %v, want %q", body["param"], want)
			}
		})
	}
}

func TestApplyBatchForwardsTheGateMembers(t *testing.T) {
	applier := &roleBatchApplier{}
	ts := newApplyBatchServer(t, applier)

	resp := ts.claim(t, batchApplyPath, `{"actor":"alice","items":[
		{"kind":"create","create":{"title":"a gate","await_type":"gh:run","await_id":"77","timeout":1,"waiters":["w","w"]}},
		{"kind":"update","update":{"target":{"id":"bd-1"},"patch":{"await_id":"","waiters":[]}}}
	]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	got := applier.requests()
	if len(got) != 1 || len(got[0].Items) != 2 {
		t.Fatalf("the role received %v, want one request of two items", got)
	}
	create := got[0].Items[0].Create
	if create == nil || create.Issue == nil {
		t.Fatal("the create item reached the role without an issue")
	}
	issue := create.Issue
	if issue.AwaitType != "gh:run" || issue.AwaitID != "77" || issue.Timeout != time.Nanosecond ||
		!slices.Equal(issue.Waiters, []string{"w", "w"}) {
		t.Errorf("create gate fields = (%q, %q, %s, %q), want (gh:run, 77, 1ns, [w w])",
			issue.AwaitType, issue.AwaitID, issue.Timeout, issue.Waiters)
	}
	update := got[0].Items[1].Update
	if update == nil {
		t.Fatal("the update item reached the role empty")
	}
	if update.Patch.AwaitID != (issueops.Field[string]{Set: true, Value: ""}) {
		t.Errorf("update await_id = %+v, want a set clear", update.Patch.AwaitID)
	}
	if !update.Patch.Waiters.Set || len(update.Patch.Waiters.Value) != 0 {
		t.Errorf("update waiters = %+v, want a set clear", update.Patch.Waiters)
	}
}

func TestApplyBatchRefusesTheGateShapesTheDocumentRefuses(t *testing.T) {
	for _, test := range []struct {
		name, item, wantParam string
	}{
		{"await_type on a patch", `{"kind":"update","update":{"target":{"id":"bd-1"},"patch":{"await_type":"timer"}}}`, "items[0].update.patch.await_type"},
		{"timeout on a patch", `{"kind":"update","update":{"target":{"id":"bd-1"},"patch":{"timeout":1}}}`, "items[0].update.patch.timeout"},
		{"a null waiters on a patch", `{"kind":"update","update":{"target":{"id":"bd-1"},"patch":{"waiters":null}}}`, "items[0].update.patch.waiters"},
		{"a null await_id on a patch", `{"kind":"update","update":{"target":{"id":"bd-1"},"patch":{"await_id":null}}}`, "items[0].update.patch.await_id"},
	} {
		t.Run(test.name, func(t *testing.T) {
			applier := &roleBatchApplier{}
			ts := newApplyBatchServer(t, applier)
			resp := ts.claim(t, batchApplyPath, `{"actor":"alice","items":[`+test.item+`]}`)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
			}
			if body := decodeBody(t, resp); body["param"] != test.wantParam {
				t.Errorf("param = %v, want %q", body["param"], test.wantParam)
			}
			if len(applier.requests()) != 0 {
				t.Error("the role was called for a refused request")
			}
		})
	}
}

func TestApplyBatchNamesTheGateMemberTheRoleRefused(t *testing.T) {
	for _, test := range []struct {
		name      string
		err       error
		wantParam string
	}{
		{"a create item's await_type", itemErr(0, issueops.ItemCreate, "g", "", gateFieldErr(issueops.GateFieldAwaitType)), "items[0].create.await_type"},
		{"a create item's timeout", itemErr(0, issueops.ItemCreate, "g", "", gateFieldErr(issueops.GateFieldTimeout)), "items[0].create.timeout"},
		{"an update item's waiters", itemErr(0, issueops.ItemUpdate, "", "bd-1", gateFieldErr(issueops.GateFieldWaiters)), "items[0].update.patch.waiters"},
		{"an update item's await_id", itemErr(0, issueops.ItemUpdate, "", "bd-1", gateFieldErr(issueops.GateFieldAwaitID)), "items[0].update.patch.await_id"},
		// A refusal that lost its item still names the member carrying them all.
		{"no item", gateFieldErr(issueops.GateFieldWaiters), "items"},
	} {
		t.Run(test.name, func(t *testing.T) {
			applier := &roleBatchApplier{err: test.err}
			ts := newApplyBatchServer(t, applier)
			resp := ts.claim(t, batchApplyPath, `{"actor":"alice","items":[{"kind":"create","create":{"title":"one"}}]}`)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if body["code"] != string(CodeInvalidArgument) {
				t.Errorf("code = %v, want %s", body["code"], CodeInvalidArgument)
			}
			if body["param"] != test.wantParam {
				t.Errorf("param = %v, want %q", body["param"], test.wantParam)
			}
		})
	}
}

func TestTheGateTokensAreAdvertised(t *testing.T) {
	got := Capabilities()
	for _, token := range []string{CapIssuesCreateGates, CapIssuesUpdateGates} {
		if !slices.Contains(got, token) {
			t.Errorf("Capabilities() = %v, missing %q", got, token)
		}
	}
	if CapIssuesCreateGates != "issues.create.gates" || CapIssuesUpdateGates != "issues.update.gates" {
		t.Errorf("tokens = %q, %q; want issues.create.gates, issues.update.gates", CapIssuesCreateGates, CapIssuesUpdateGates)
	}
}
