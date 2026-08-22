package httpapi

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// Pure, on a fake ROLE, like the close's and the reopen's. What a fake cannot
// prove is that a set member, a null-cleared member and an untouched member all
// land the way this handler says they do in a real row; that is
// TestProxiedServerServeUpdate against real Dolt.

const updatePath = "/v0/beads/issues/bd-1"

func (ts *testServer) updateIssue(t *testing.T, path, body string) *http.Response {
	t.Helper()
	return ts.patchBody(t, path, "application/json", body)
}

// patchBody is postBody's PATCH twin: this is the first operation on the
// surface that uses the method, so the helper is new rather than shared.
func (ts *testServer) patchBody(t *testing.T, path, contentType, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPatch, ts.base+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := ts.client.Do(req)
	if err != nil {
		t.Fatalf("PATCH %s: %v", path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func newUpdateServer(t *testing.T, lifecycle *roleLifecycle) *testServer {
	t.Helper()
	return newTestServer(t, rolesConfig(Config{Lifecycle: lifecycle}))
}

func updatedIssue(id string) *types.Issue {
	return seededIssue(id, "alice", types.StatusOpen)
}

// TestUpdateForwardsEveryDocumentedMember walks the whole patch vocabulary in
// one request and asserts the projection onto the role's IssuePatch field by
// field. It is the widest body on this surface, and the mapping is where a
// silent mis-wiring would live.
func TestUpdateForwardsEveryDocumentedMember(t *testing.T) {
	lifecycle := &roleLifecycle{updateResult: issueops.UpdateResult{Issue: updatedIssue("bd-1"), Changed: true}}
	ts := newUpdateServer(t, lifecycle)

	resp := ts.updateIssue(t, updatePath, `{"actor":"alice","patch":{
		"title":"new title",
		"description":"new description",
		"design":"new design",
		"acceptance_criteria":"new criteria",
		"notes":"new notes",
		"priority":3,
		"issue_type":"bug",
		"labels":["one","two"],
		"estimated_minutes":45,
		"external_ref":"JIRA-7",
		"due_at":"2026-09-01T12:00:00Z",
		"defer_until":"2026-08-20T09:30:00Z"
	}}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}

	got := lifecycle.updateRequests()
	if len(got) != 1 {
		t.Fatalf("the role was called %d times, want 1", len(got))
	}
	req := got[0]
	if req.Actor != "alice" || req.IssueID != "bd-1" {
		t.Errorf("the role received actor %q id %q", req.Actor, req.IssueID)
	}
	// Every precondition and force flag stays zero: unpublished on this surface.
	if req.Claim || req.ForceAssigneeTransfer || req.ForceClosePolicy ||
		req.ExpectedVersion != nil || req.ExpectedAssignee != nil || req.ExpectedStatus != nil {
		t.Errorf("the handler published a precondition or force flag: %+v", req)
	}
	if req.IssuePlaneOnly {
		t.Error("the update narrowed itself to the issue plane; this operation resolves across both")
	}

	p := req.Patch
	for _, tc := range []struct {
		name string
		set  bool
		got  any
		want any
	}{
		{"title", p.Title.Set, p.Title.Value, "new title"},
		{"description", p.Description.Set, p.Description.Value, "new description"},
		{"design", p.Design.Set, p.Design.Value, "new design"},
		{"acceptance_criteria", p.AcceptanceCriteria.Set, p.AcceptanceCriteria.Value, "new criteria"},
		{"notes", p.Notes.Set, p.Notes.Value, "new notes"},
		{"priority", p.Priority.Set, p.Priority.Value, 3},
		{"issue_type", p.IssueType.Set, p.IssueType.Value, issueops.IssueType("bug")},
	} {
		if !tc.set {
			t.Errorf("%s: Set is false though the member was present", tc.name)
			continue
		}
		if tc.got != tc.want {
			t.Errorf("%s = %v, want %v", tc.name, tc.got, tc.want)
		}
	}

	// labels is COMPLETE REPLACEMENT, so it must reach Replace and never Add.
	if !p.Labels.Replace.Set || len(p.Labels.Replace.Value) != 2 {
		t.Errorf("labels reached Replace as %+v, want the two-element set", p.Labels.Replace)
	}
	if len(p.Labels.Add) != 0 || len(p.Labels.Remove) != 0 {
		t.Errorf("labels reached the incremental edits (%v/%v); the document publishes replacement only", p.Labels.Add, p.Labels.Remove)
	}

	if !p.EstimatedMinutes.Set || p.EstimatedMinutes.Value == nil || *p.EstimatedMinutes.Value != 45 {
		t.Errorf("estimated_minutes = %+v, want a set 45", p.EstimatedMinutes)
	}
	if !p.ExternalRef.Set || p.ExternalRef.Value == nil || *p.ExternalRef.Value != "JIRA-7" {
		t.Errorf("external_ref = %+v, want a set JIRA-7", p.ExternalRef)
	}
	wantDue := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	if !p.DueAt.Set || p.DueAt.Value == nil || !p.DueAt.Value.Equal(wantDue) {
		t.Errorf("due_at = %+v, want %v", p.DueAt, wantDue)
	}
	wantDefer := time.Date(2026, 8, 20, 9, 30, 0, 0, time.UTC)
	if !p.DeferUntil.Set || p.DeferUntil.Value == nil || !p.DeferUntil.Value.Equal(wantDefer) {
		t.Errorf("defer_until = %+v, want %v", p.DeferUntil, wantDefer)
	}
}

// TestUpdateLeavesAbsentMembersUntouched is the partial-update rule itself:
// PRESENCE is the signal, so every member the body did not carry must arrive
// with Set false. A handler that filled the whole struct from a decoded body
// would pass every other test in this file and silently blank twelve fields.
func TestUpdateLeavesAbsentMembersUntouched(t *testing.T) {
	lifecycle := &roleLifecycle{updateResult: issueops.UpdateResult{Issue: updatedIssue("bd-1"), Changed: true}}
	ts := newUpdateServer(t, lifecycle)

	resp := ts.updateIssue(t, updatePath, `{"actor":"alice","patch":{"title":"only this"}}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	p := lifecycle.updateRequests()[0].Patch
	if !p.Title.Set {
		t.Fatal("title was not set")
	}
	for _, tc := range []struct {
		name string
		set  bool
	}{
		{"description", p.Description.Set},
		{"design", p.Design.Set},
		{"acceptance_criteria", p.AcceptanceCriteria.Set},
		{"notes", p.Notes.Set},
		{"append_notes", p.AppendNotes.Set},
		{"priority", p.Priority.Set},
		{"issue_type", p.IssueType.Set},
		{"labels", p.Labels.Replace.Set},
		{"estimated_minutes", p.EstimatedMinutes.Set},
		{"external_ref", p.ExternalRef.Set},
		{"due_at", p.DueAt.Set},
		{"defer_until", p.DeferUntil.Set},
	} {
		if tc.set {
			t.Errorf("%s: Set is true though the member was absent; an absent member must leave the field untouched", tc.name)
		}
	}
	// And the fields this operation does not publish at all stay zero, whatever
	// the body said — they are not in the accepted set, so they cannot be
	// reached even by name.
	if p.Status.Set || p.Assignee.Set || p.Owner.Set || p.ParentID.Set ||
		p.Persistence.Set || p.ClosedBySession.Set || p.SpecID.Set || p.AwaitID.Set {
		t.Errorf("an unpublished patch member was set: %+v", p)
	}
	if p.Metadata.Replace.Set || p.Metadata.Merge.Set || len(p.Metadata.Set) > 0 || len(p.Metadata.Unset) > 0 {
		t.Errorf("metadata was reached: %+v", p.Metadata)
	}
}

// TestUpdateClearsTheNullableMembers is the other half of the presence rule:
// explicit null on the four Field[*T] members CLEARS, which on the wire is a
// SET field carrying a nil pointer. Set false would mean "untouched" and the
// clear would be silently dropped.
func TestUpdateClearsTheNullableMembers(t *testing.T) {
	lifecycle := &roleLifecycle{updateResult: issueops.UpdateResult{Issue: updatedIssue("bd-1"), Changed: true}}
	ts := newUpdateServer(t, lifecycle)

	resp := ts.updateIssue(t, updatePath, `{"actor":"alice","patch":{
		"estimated_minutes":null,"external_ref":null,"due_at":null,"defer_until":null
	}}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	p := lifecycle.updateRequests()[0].Patch
	if !p.EstimatedMinutes.Set || p.EstimatedMinutes.Value != nil {
		t.Errorf("estimated_minutes = %+v, want set with a nil value (a clear)", p.EstimatedMinutes)
	}
	if !p.ExternalRef.Set || p.ExternalRef.Value != nil {
		t.Errorf("external_ref = %+v, want set with a nil value (a clear)", p.ExternalRef)
	}
	if !p.DueAt.Set || p.DueAt.Value != nil {
		t.Errorf("due_at = %+v, want set with a nil value (a clear)", p.DueAt)
	}
	if !p.DeferUntil.Set || p.DeferUntil.Value != nil {
		t.Errorf("defer_until = %+v, want set with a nil value (a clear)", p.DeferUntil)
	}
}

// TestUpdateRefusesNullOnEveryOtherMember is the claim's null-through-a-pointer
// rule generalized. Without it, `"title":null` unmarshals to a nil pointer that
// is indistinguishable from an absent member — a write the client asked for and
// the server silently dropped.
func TestUpdateRefusesNullOnEveryOtherMember(t *testing.T) {
	for _, member := range []string{
		"title", "description", "design", "acceptance_criteria",
		"notes", "append_notes", "priority", "issue_type", "labels",
	} {
		t.Run(member, func(t *testing.T) {
			lifecycle := &roleLifecycle{updateResult: issueops.UpdateResult{Issue: updatedIssue("bd-1")}}
			ts := newUpdateServer(t, lifecycle)

			resp := ts.updateIssue(t, updatePath, fmt.Sprintf(`{"actor":"alice","patch":{%q:null}}`, member))
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if body["param"] != "patch."+member {
				t.Errorf("param = %v, want patch.%s", body["param"], member)
			}
			if got := lifecycle.updateRequests(); len(got) != 0 {
				t.Errorf("a null reached the role: %+v", got)
			}
		})
	}
}

// TestUpdateIsIdempotentForASameValuePatch: the role's Changed, verbatim. A
// same-value patch is a 200 with changed:false, not an error.
func TestUpdateIsIdempotentForASameValuePatch(t *testing.T) {
	lifecycle := &roleLifecycle{updateResult: issueops.UpdateResult{Issue: updatedIssue("bd-1"), Changed: false}}
	ts := newUpdateServer(t, lifecycle)

	resp := ts.updateIssue(t, updatePath, `{"actor":"alice","patch":{"title":"claim me"}}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	if body := decodeBody(t, resp); body["changed"] != false {
		t.Errorf("changed = %v, want false for a same-value patch", body["changed"])
	}
}

// TestUpdateNamesItsHistoryEntry: the reopen's rule, for the same reason.
func TestUpdateNamesItsHistoryEntry(t *testing.T) {
	lifecycle := &roleLifecycle{updateResult: issueops.UpdateResult{Issue: updatedIssue("bd-1"), Changed: true}}
	ts := newUpdateServer(t, lifecycle)

	if resp := ts.updateIssue(t, updatePath, `{"actor":"alice","patch":{"title":"t"}}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	got := lifecycle.updateRequests()[0]
	if got.Provenance == "" {
		t.Fatal("the update carries no provenance, so the history entry reads differently per backend")
	}
	if !strings.Contains(got.Provenance, "serve") {
		t.Errorf("provenance = %q; it is meant to name THIS surface", got.Provenance)
	}
}

// TestUpdateRejectsTheShapesTheDocumentRefuses is the body vocabulary.
//
// The four cases that used to live here — status, assignee, metadata and
// parent_id refused BY NAME — are gone, because those members are published
// now. What replaces them is the refusal each one BROUGHT: a self-parent, and
// the two combinations of the assignee guards that contradict each other. An
// unknown member is still refused by name at both levels, which is what a
// client dispatches on to tell version skew from a bad value.
func TestUpdateRejectsTheShapesTheDocumentRefuses(t *testing.T) {
	for _, tc := range []struct {
		name  string
		body  string
		param string
	}{
		{"no actor", `{"patch":{"title":"t"}}`, "actor"},
		{"blank actor", `{"actor":"  ","patch":{"title":"t"}}`, "actor"},
		{"actor with a newline", "{\"actor\":\"a\\nbd: update\",\"patch\":{\"title\":\"t\"}}", "actor"},
		{"null actor", `{"actor":null,"patch":{"title":"t"}}`, "actor"},
		{"no patch", `{"actor":"alice"}`, "patch"},
		{"null patch", `{"actor":"alice","patch":null}`, "patch"},
		{"patch is not an object", `{"actor":"alice","patch":["title"]}`, "patch"},
		{"empty patch", `{"actor":"alice","patch":{}}`, "patch"},
		{"unknown request member", `{"actor":"alice","patch":{"title":"t"},"force":true}`, "force"},
		{"unknown patch member", `{"actor":"alice","patch":{"title":"t","persistence":"ephemeral"}}`, "patch.persistence"},
		// The refusals the three policy members brought with them, all decided
		// at the edge because none of them needs to read a row.
		{"self parent", `{"actor":"alice","patch":{"parent_id":"bd-1"}}`, "patch.parent_id"},
		{"oversize parent_id", `{"actor":"alice","patch":{"parent_id":"` + strings.Repeat("x", 300) + `"}}`, "patch.parent_id"},
		{"oversize status", `{"actor":"alice","patch":{"status":"` + strings.Repeat("x", 300) + `"}}`, "patch.status"},
		{"oversize assignee", `{"actor":"alice","patch":{"assignee":"` + strings.Repeat("x", 300) + `"}}`, "patch.assignee"},
		{"metadata replace beside merge", `{"actor":"alice","patch":{"metadata":{"replace":{},"merge":{"a":1}}}}`, "patch.metadata.replace"},
		// A wrong JSON TYPE on a nested member is reported against the whole
		// patch, the way `labels` already is: the typed decode fails as one
		// unit and this surface does not quote its error string.
		{"metadata is not an object", `{"actor":"alice","patch":{"metadata":["a"]}}`, "patch"},
		{"unknown metadata member", `{"actor":"alice","patch":{"metadata":{"clear":true}}}`, "patch.metadata.clear"},
		{"force_assignee_transfer without an assignee edit", `{"actor":"alice","patch":{"title":"t"},"force_assignee_transfer":true}`, "force_assignee_transfer"},
		{"force_assignee_transfer beside expected_assignee", `{"actor":"alice","patch":{"assignee":"bob"},"force_assignee_transfer":true,"expected_assignee":"carol"}`, "force_assignee_transfer"},
		{"expected_version is not a number", `{"actor":"alice","patch":{"title":"t"},"expected_version":"3"}`, "expected_version"},
		{"null expected_status", `{"actor":"alice","patch":{"title":"t"},"expected_status":null}`, "expected_status"},
		{"null force_close_policy", `{"actor":"alice","patch":{"title":"t"},"force_close_policy":null}`, "force_close_policy"},
		{"blank title", `{"actor":"alice","patch":{"title":"   "}}`, "patch.title"},
		{"oversize title", `{"actor":"alice","patch":{"title":"` + strings.Repeat("x", 300) + `"}}`, "patch.title"},
		{"title is not a string", `{"actor":"alice","patch":{"title":7}}`, "patch"},
		{"priority below the range", `{"actor":"alice","patch":{"priority":-1}}`, "patch.priority"},
		{"priority above the range", `{"actor":"alice","patch":{"priority":5}}`, "patch.priority"},
		{"priority is not a number", `{"actor":"alice","patch":{"priority":"high"}}`, "patch"},
		{"oversize issue_type", `{"actor":"alice","patch":{"issue_type":"` + strings.Repeat("x", 300) + `"}}`, "patch.issue_type"},
		{"oversize label", `{"actor":"alice","patch":{"labels":["` + strings.Repeat("x", 300) + `"]}}`, "patch.labels"},
		{"labels is not an array", `{"actor":"alice","patch":{"labels":"one"}}`, "patch"},
		{"oversize external_ref", `{"actor":"alice","patch":{"external_ref":"` + strings.Repeat("x", 300) + `"}}`, "patch.external_ref"},
		{"malformed due_at", `{"actor":"alice","patch":{"due_at":"not a time"}}`, "patch"},
		{"notes and append_notes together", `{"actor":"alice","patch":{"notes":"a","append_notes":"b"}}`, "patch.append_notes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lifecycle := &roleLifecycle{updateResult: issueops.UpdateResult{Issue: updatedIssue("bd-1")}}
			ts := newUpdateServer(t, lifecycle)

			resp := ts.updateIssue(t, updatePath, tc.body)
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
			if got := lifecycle.updateRequests(); len(got) != 0 {
				t.Errorf("a refused body reached the role: %+v", got)
			}
		})
	}
}

// TestUpdateAcceptsAnEmptyLabelArrayAsAClear: an empty array is not an empty
// patch. It is a complete replacement with nothing in it, which is how a client
// removes every label — so it must reach the role rather than being refused
// alongside the empty-object case.
func TestUpdateAcceptsAnEmptyLabelArrayAsAClear(t *testing.T) {
	lifecycle := &roleLifecycle{updateResult: issueops.UpdateResult{Issue: updatedIssue("bd-1"), Changed: true}}
	ts := newUpdateServer(t, lifecycle)

	resp := ts.updateIssue(t, updatePath, `{"actor":"alice","patch":{"labels":[]}}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	p := lifecycle.updateRequests()[0].Patch
	if !p.Labels.Replace.Set || len(p.Labels.Replace.Value) != 0 {
		t.Errorf("labels = %+v, want a set replacement carrying nothing", p.Labels.Replace)
	}
}

// TestUpdateMapsARoleValidationRefusalToTheDocumented400 is failUpdate's
// reason for existing: the workspace's configured vocabulary is a question this
// server cannot ask without a transaction, so the role answers it and the 400
// the document promises has to come from the mapping.
func TestUpdateMapsARoleValidationRefusalToTheDocumented400(t *testing.T) {
	lifecycle := &roleLifecycle{updateErr: fmt.Errorf(
		"%w: issue_type \"epic\" is not configured for this workspace", storage.ErrValidation)}
	ts := newUpdateServer(t, lifecycle)

	resp := ts.updateIssue(t, updatePath, `{"actor":"alice","patch":{"issue_type":"epic"}}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if body["code"] != string(CodeInvalidArgument) {
		t.Errorf("code = %v, want %s", body["code"], CodeInvalidArgument)
	}
	// The role's own prose never reaches the client — 4xx details reflect the
	// caller's input back, not server internals — but the operator gets it.
	if detail, _ := body["detail"].(string); strings.Contains(detail, "not configured for this workspace") {
		t.Errorf("the role's message was quoted into the response: %q", detail)
	}
	if !strings.Contains(ts.stderr.String(), "request_refused") {
		t.Errorf("the real refusal was not logged for the operator:\n%s", ts.stderr.String())
	}
}

// TestUpdateOfAnAbsentIssueIs404 is failUpdate's other branch, and the
// divergence from failBatchCreate: this operation DOES address a resource by
// path, so an id that names nothing is a genuine 404 rather than a statement
// about the body.
func TestUpdateOfAnAbsentIssueIs404(t *testing.T) {
	lifecycle := &roleLifecycle{updateErr: fmt.Errorf("update bd-9: %w", issueops.ErrNotFound)}
	ts := newUpdateServer(t, lifecycle)

	resp := ts.updateIssue(t, "/v0/beads/issues/bd-9", `{"actor":"alice","patch":{"title":"t"}}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", resp.StatusCode, readAll(t, resp))
	}
	if body := decodeBody(t, resp); body["code"] != string(CodeNotFound) {
		t.Errorf("code = %v, want %s", body["code"], CodeNotFound)
	}
}

// TestUpdateRefusesAForeignMediaType and TestUpdateRefusesAQueryParameter: the
// two document-level rules hold on a PATCH exactly as on a POST.
func TestUpdateRefusesAForeignMediaType(t *testing.T) {
	lifecycle := &roleLifecycle{}
	ts := newUpdateServer(t, lifecycle)

	resp := ts.patchBody(t, updatePath, "text/plain", `{"actor":"alice","patch":{"title":"t"}}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	if body := decodeBody(t, resp); body["param"] != "Content-Type" {
		t.Errorf("param = %v, want Content-Type", body["param"])
	}
	if got := lifecycle.updateRequests(); len(got) != 0 {
		t.Errorf("a foreign media type reached the role: %+v", got)
	}
}

func TestUpdateRefusesAQueryParameter(t *testing.T) {
	lifecycle := &roleLifecycle{}
	ts := newUpdateServer(t, lifecycle)

	resp := ts.updateIssue(t, updatePath+"?force=true", `{"actor":"alice","patch":{"title":"t"}}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	if body := decodeBody(t, resp); body["reason"] != string(ReasonUnknownParameter) {
		t.Errorf("reason = %v, want %s", body["reason"], ReasonUnknownParameter)
	}
	if got := lifecycle.updateRequests(); len(got) != 0 {
		t.Errorf("a query string reached the role: %+v", got)
	}
}

// TestUpdateRefusesUnrowableIDsBeforeAnyDatabaseWork: the dispatcher's id bound
// applied here, because this route is NOT on the dispatcher's pattern — it is
// the one write that takes an issue id from the PATH without going through
// customMethodTarget, so it would otherwise have no id bound at all.
func TestUpdateRefusesUnrowableIDsBeforeAnyDatabaseWork(t *testing.T) {
	for _, tc := range []struct{ name, id string }{
		{"longer than the column", strings.Repeat("b", types.MaxFieldLen+1)},
		{"carrying a control character", "bd-1%00"},
		{"carrying a C1 introducer", "bd-1%C2%9B"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lifecycle := &roleLifecycle{}
			ts := newUpdateServer(t, lifecycle)

			resp := ts.updateIssue(t, "/v0/beads/issues/"+tc.id, `{"actor":"alice","patch":{"title":"t"}}`)
			if resp.StatusCode != http.StatusNotFound {
				t.Fatalf("status = %d, want 404: %s", resp.StatusCode, readAll(t, resp))
			}
			if got := lifecycle.updateRequests(); len(got) != 0 {
				t.Errorf("an unrowable id reached the role: %+v", got)
			}
		})
	}
}

// TestPatchOnACustomMethodSegmentIsNeverTheCustomMethod is the routing-safety
// property this operation introduces.
//
// `PATCH /v0/beads/issues/{id}` is a single-segment wildcard, so it MATCHES
// /v0/beads/issues/bd-1:close — which the POST dispatcher would read as a
// close. The two live on one path under different methods, and what must never
// happen is a PATCH executing as a lifecycle verb. It is an update of an issue
// whose id is literally "bd-1:close", which no row holds.
func TestPatchOnACustomMethodSegmentIsNeverTheCustomMethod(t *testing.T) {
	for _, segment := range []string{"bd-1:close", "bd-1:reopen", "bd-1:claim"} {
		t.Run(segment, func(t *testing.T) {
			lifecycle := &roleLifecycle{updateResult: issueops.UpdateResult{Issue: updatedIssue(segment), Changed: true}}
			ts := newUpdateServer(t, lifecycle)

			resp := ts.updateIssue(t, "/v0/beads/issues/"+segment, `{"actor":"alice","patch":{"title":"t"}}`)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
			}
			// It reached the UPDATE verb, with the whole segment as the id.
			if got := lifecycle.updateRequests(); len(got) != 1 || got[0].IssueID != segment {
				t.Fatalf("the role received %+v, want an update of %q", got, segment)
			}
			if got := lifecycle.closeRequests(); len(got) != 0 {
				t.Errorf("a PATCH executed as a close: %+v", got)
			}
			if got := lifecycle.reopenRequests(); len(got) != 0 {
				t.Errorf("a PATCH executed as a reopen: %+v", got)
			}
		})
	}
}

// TestUpdateRefusesALifecycleThatAnswersWithNothing is
// checkedLifecycle.Update's reason for existing.
func TestUpdateRefusesALifecycleThatAnswersWithNothing(t *testing.T) {
	lifecycle := &roleLifecycle{updateResult: issueops.UpdateResult{Changed: true}}
	ts := newUpdateServer(t, lifecycle)

	resp := ts.updateIssue(t, updatePath, `{"actor":"alice","patch":{"title":"t"}}`)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", resp.StatusCode, readAll(t, resp))
	}
	if body := decodeBody(t, resp); body["code"] != string(CodeInternal) {
		t.Errorf("code = %v, want %s", body["code"], CodeInternal)
	}
}

// TestUpdatePathReachesItsHandler drives the documented path and method. It
// shares its pattern with getIssue and differs only in method, which is the
// whole argument for the method.
func TestUpdatePathReachesItsHandler(t *testing.T) {
	lifecycle := &roleLifecycle{updateResult: issueops.UpdateResult{Issue: updatedIssue("bd-1"), Changed: true}}
	ts := newUpdateServer(t, lifecycle)

	if resp := ts.updateIssue(t, updatePath, `{"actor":"alice","patch":{"title":"t"}}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH the documented path: status = %d, want 200", resp.StatusCode)
	}
	line := findLogLine(t, ts.stderr.String(), "method=PATCH")
	if !strings.Contains(line, "op="+OpUpdateIssue) {
		t.Errorf("the documented update path is served by another operation:\n%s", line)
	}
}

// TestUpdateKeepsItsPatternEqualToItsSpecPath is the argument for PATCH stated
// as a test: a plain method needs no routing exception, so this row must NOT
// declare a specPath or a customMethod. A future edit that turned it into a
// custom method would be a design change, not a refactor.
func TestUpdateKeepsItsPatternEqualToItsSpecPath(t *testing.T) {
	for _, rt := range routeTable {
		if rt.op != OpUpdateIssue {
			continue
		}
		if rt.method != http.MethodPatch {
			t.Errorf("the update route registers %s, want PATCH", rt.method)
		}
		if rt.specPath != "" || rt.customMethod != "" {
			t.Errorf("the update route declares specPath %q / customMethod %q; a plain method needs neither",
				rt.specPath, rt.customMethod)
		}
		if rt.pattern != "/v0/beads/issues/{id}" {
			t.Errorf("the update route's pattern is %q, want the issue-detail path", rt.pattern)
		}
		if rt.bypassSemaphore {
			t.Error("the update route bypasses the database semaphore; only handlers that touch no database may")
		}
		return
	}
	t.Fatalf("no %s row in the route table", OpUpdateIssue)
}

// TestUpdateResolvesAcrossBothPlanes, as close and reopen do.
func TestUpdateResolvesAcrossBothPlanes(t *testing.T) {
	wisp := seededIssue("bd-w1", "alice", types.StatusOpen)
	lifecycle := &roleLifecycle{updateResult: issueops.UpdateResult{Issue: wisp, Changed: true}}
	ts := newUpdateServer(t, lifecycle)

	resp := ts.updateIssue(t, "/v0/beads/issues/bd-w1", `{"actor":"alice","patch":{"title":"t"}}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("updating a wisp id: status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	if got := lifecycle.updateRequests(); len(got) != 1 || got[0].IssuePlaneOnly {
		t.Fatalf("the role received %+v, want a both-plane resolve", got)
	}
}

// The pins for the members this operation grew: the three that carry POLICY —
// `status`, `assignee`, `parent_id` — the `metadata` algebra, and the guard and
// force flags beside `patch`. What is asserted is the WIRE EDGE: that each
// reaches the role's own field, and that each refusal the role can now raise
// arrives as the documented code naming the member that earned it.

// revisionedIssue is the row a guarded case reads its token off. The revision
// is the only thing that distinguishes it from updatedIssue, and it is what a
// case must never invent for itself.
func revisionedIssue(id string, revision int64) *types.Issue {
	issue := updatedIssue(id)
	issue.RowVersion = revision
	return issue
}

// TestUpdateForwardsTheGuardedMembers walks the members added beside the
// original patch vocabulary in one request and asserts the projection onto the
// role's UpdateRequest field by field. It is TestUpdateForwardsEveryDocumented
// Member's sibling for the half that carries policy.
func TestUpdateForwardsTheGuardedMembers(t *testing.T) {
	lifecycle := &roleLifecycle{updateResult: issueops.UpdateResult{Issue: revisionedIssue("bd-1", 42), Changed: true}}
	ts := newUpdateServer(t, lifecycle)

	resp := ts.updateIssue(t, updatePath, `{
		"actor":"alice",
		"expected_version": 41,
		"expected_status": "open",
		"force_close_policy": true,
		"patch": {
			"status": "closed",
			"assignee": "bob",
			"parent_id": "bd-parent",
			"metadata": {"merge":{"a":1},"set":{"b":null},"unset":["c"]}
		}
	}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}

	got := lifecycle.updateRequests()
	if len(got) != 1 {
		t.Fatalf("the role was called %d times, want 1", len(got))
	}
	req := got[0]
	if req.ExpectedVersion == nil || *req.ExpectedVersion != 41 {
		t.Errorf("expected_version = %v, want 41", req.ExpectedVersion)
	}
	if req.ExpectedStatus == nil || *req.ExpectedStatus != issueops.Status("open") {
		t.Errorf("expected_status = %v, want open", req.ExpectedStatus)
	}
	if req.ExpectedAssignee != nil {
		t.Errorf("expected_assignee = %v, want nil for an absent guard", *req.ExpectedAssignee)
	}
	if !req.ForceClosePolicy || req.ForceAssigneeTransfer {
		t.Errorf("force flags = %v/%v, want close-policy only", req.ForceClosePolicy, req.ForceAssigneeTransfer)
	}
	// Claim stays zero: acquiring work is `{id}:claim`, which has its own
	// eligibility rules and its own conflict vocabulary.
	if req.Claim {
		t.Error("the update claimed the issue; that operation is `{id}:claim`")
	}
	if req.IssuePlaneOnly {
		t.Error("the update narrowed itself to the issue plane; this operation resolves across both")
	}

	p := req.Patch
	if !p.Status.Set || p.Status.Value != issueops.Status("closed") {
		t.Errorf("status = %+v, want a set closed", p.Status)
	}
	if !p.Assignee.Set || p.Assignee.Value != "bob" {
		t.Errorf("assignee = %+v, want a set bob", p.Assignee)
	}
	if !p.ParentID.Set || p.ParentID.Value != "bd-parent" {
		t.Errorf("parent_id = %+v, want a set bd-parent", p.ParentID)
	}
	m := p.Metadata
	if m.Replace.Set {
		t.Errorf("metadata.replace is set though the request sent none: %+v", m.Replace)
	}
	if !m.Merge.Set || strings.ReplaceAll(string(m.Merge.Value), " ", "") != `{"a":1}` {
		t.Errorf("metadata.merge = %+v, want the caller's own bytes", m.Merge)
	}
	if len(m.Set) != 1 || string(m.Set["b"]) != "null" {
		t.Errorf("metadata.set = %v; a key written to JSON null must survive as the literal", m.Set)
	}
	if len(m.Unset) != 1 || m.Unset[0] != "c" {
		t.Errorf("metadata.unset = %v, want [c]", m.Unset)
	}
}

// TestUpdateDistinguishesTheThreeMetadataStates is the F12 pin, and it is the
// case the generated type cannot express: on the metadata plane an absent
// member, a member holding JSON null and a member holding the empty string are
// three different requests, and only the raw bytes carry the difference.
func TestUpdateDistinguishesTheThreeMetadataStates(t *testing.T) {
	for _, test := range []struct {
		name string
		set  string
		want string
	}{
		{"a key written to null", `{"set":{"k":null}}`, "null"},
		{"a key written to the empty string", `{"set":{"k":""}}`, `""`},
		{"a key written to an empty object", `{"set":{"k":{}}}`, "{}"},
	} {
		t.Run(test.name, func(t *testing.T) {
			lifecycle := &roleLifecycle{updateResult: issueops.UpdateResult{Issue: updatedIssue("bd-1"), Changed: true}}
			ts := newUpdateServer(t, lifecycle)

			resp := ts.updateIssue(t, updatePath, `{"actor":"alice","patch":{"metadata":`+test.set+`}}`)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
			}
			got := lifecycle.updateRequests()
			if len(got) != 1 {
				t.Fatalf("the role was called %d times, want 1", len(got))
			}
			if value := string(got[0].Patch.Metadata.Set["k"]); value != test.want {
				t.Errorf("metadata.set[k] = %q, want %q", value, test.want)
			}
		})
	}

	t.Run("an absent key is not written at all", func(t *testing.T) {
		lifecycle := &roleLifecycle{updateResult: issueops.UpdateResult{Issue: updatedIssue("bd-1"), Changed: true}}
		ts := newUpdateServer(t, lifecycle)

		resp := ts.updateIssue(t, updatePath, `{"actor":"alice","patch":{"metadata":{"unset":["k"]}}}`)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
		}
		got := lifecycle.updateRequests()
		if len(got) != 1 {
			t.Fatalf("the role was called %d times, want 1", len(got))
		}
		if _, present := got[0].Patch.Metadata.Set["k"]; present {
			t.Error("an unset key arrived as a set one; removing a key is `unset`, never a null in `set`")
		}
	})

	// `metadata` itself is NOT nullable: a null on it would be a clear the
	// document never promised, and the algebra spells one as `replace`.
	t.Run("metadata itself refuses an explicit null", func(t *testing.T) {
		lifecycle := &roleLifecycle{}
		ts := newUpdateServer(t, lifecycle)

		resp := ts.updateIssue(t, updatePath, `{"actor":"alice","patch":{"metadata":null}}`)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
		}
		if body := decodeBody(t, resp); body["param"] != "patch.metadata" {
			t.Errorf("param = %v, want patch.metadata", body["param"])
		}
		if len(lifecycle.updateRequests()) != 0 {
			t.Error("a null reached the role")
		}
	})
}

// TestUpdateGuardsComposeFromTheRevisionTheWriteAnswered is the precondition's
// happy path, and it is written as a LOOP rather than with a literal token on
// purpose: `expected_version` guards an opaque value the store mints, so a case
// that invented one would pass against a server that answered a different
// number than it stores. The second request's guard comes from the first
// response's `revision` and from nowhere else.
func TestUpdateGuardsComposeFromTheRevisionTheWriteAnswered(t *testing.T) {
	const stored int64 = 7
	lifecycle := &roleLifecycle{updateResult: issueops.UpdateResult{Issue: revisionedIssue("bd-1", stored), Changed: true}}
	ts := newUpdateServer(t, lifecycle)

	first := ts.updateIssue(t, updatePath, `{"actor":"alice","patch":{"title":"first"}}`)
	if first.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", first.StatusCode, readAll(t, first))
	}
	revision, ok := decodeBody(t, first)["revision"].(float64)
	if !ok {
		t.Fatalf("the response carries no `revision`; a guard whose token no response carries cannot be filled")
	}
	// The token must be the ROW's, read off the snapshot the role answered
	// with. Without this the loop below round-trips whatever the handler put
	// there — including a constant zero — and could not fail against a
	// `revision` that describes no row.
	if int64(revision) != stored {
		t.Fatalf("revision = %d, want the %d the role's row carries", int64(revision), stored)
	}

	second := ts.updateIssue(t, updatePath,
		fmt.Sprintf(`{"actor":"alice","expected_version":%d,"patch":{"title":"second"}}`, int64(revision)))
	if second.StatusCode != http.StatusOK {
		t.Fatalf("guarded status = %d, want 200: %s", second.StatusCode, readAll(t, second))
	}
	got := lifecycle.updateRequests()
	if len(got) != 2 {
		t.Fatalf("the role was called %d times, want 2", len(got))
	}
	if got[1].ExpectedVersion == nil || *got[1].ExpectedVersion != int64(revision) {
		t.Errorf("expected_version = %v, want the %d the write answered with", got[1].ExpectedVersion, int64(revision))
	}
}

// TestUpdateRefusesAStaleGuard pins the 409 for each member of the trio: the
// code, the member `param` names, and the expectation echoed back. The observed
// value is deliberately absent — the refusal rolled its transaction back, so a
// read afterwards would describe a row the refusal never saw.
func TestUpdateRefusesAStaleGuard(t *testing.T) {
	for _, test := range []struct {
		name      string
		body      string
		err       error
		wantParam string
		wantEcho  string
		wantValue any
	}{
		{
			name:      "version",
			body:      `{"actor":"alice","expected_version":7,"patch":{"title":"t"}}`,
			err:       fmt.Errorf("update bd-1: %w", issueops.ErrVersionMismatch),
			wantParam: "expected_version",
			wantEcho:  "expected_version",
			wantValue: float64(7),
		},
		{
			name:      "status",
			body:      `{"actor":"alice","expected_status":"open","patch":{"title":"t"}}`,
			err:       fmt.Errorf("update bd-1: %w", issueops.ErrStatusMismatch),
			wantParam: "expected_status",
			wantEcho:  "expected_status",
			wantValue: "open",
		},
		{
			name:      "assignee",
			body:      `{"actor":"alice","expected_assignee":"bob","patch":{"assignee":"carol"}}`,
			err:       fmt.Errorf("update bd-1: %w", issueops.ErrAssigneeMismatch),
			wantParam: "expected_assignee",
			wantEcho:  "expected_assignee",
			wantValue: "bob",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			lifecycle := &roleLifecycle{updateErr: test.err}
			ts := newUpdateServer(t, lifecycle)

			resp := ts.updateIssue(t, updatePath, test.body)
			if resp.StatusCode != http.StatusConflict {
				t.Fatalf("status = %d, want 409: %s", resp.StatusCode, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if body["code"] != string(CodePreconditionFailed) {
				t.Errorf("code = %v, want %s", body["code"], CodePreconditionFailed)
			}
			if body["param"] != test.wantParam {
				t.Errorf("param = %v, want %q", body["param"], test.wantParam)
			}
			if body[test.wantEcho] != test.wantValue {
				t.Errorf("%s = %v, want %v echoed from the request", test.wantEcho, body[test.wantEcho], test.wantValue)
			}
			for _, absent := range []string{"actual_version", "actual_status", "actual_assignee"} {
				if _, present := body[absent]; present {
					t.Errorf("%s is present; this role reports no observed value and inventing one from a later read would be worse than omitting it", absent)
				}
			}
		})
	}
}

// TestUpdateAnswersThePolicyRefusalsItsMembersEarned walks every 409 the three
// policy members brought, and the extension members that discriminate within a
// shared code.
func TestUpdateAnswersThePolicyRefusalsItsMembersEarned(t *testing.T) {
	for _, test := range []struct {
		name       string
		body       string
		err        error
		wantStatus int
		wantCode   Code
		wantParam  string
		check      func(*testing.T, map[string]any)
	}{
		{
			name:       "status crossing with open children",
			body:       `{"actor":"alice","patch":{"status":"closed"}}`,
			err:        fmt.Errorf("close: %w", &issueops.CloseOpenChildrenError{OpenChildren: 3}),
			wantStatus: http.StatusConflict,
			wantCode:   CodeNotClosable,
			wantParam:  "patch.status",
			check: func(t *testing.T, body map[string]any) {
				if body["open_children"] != float64(3) {
					t.Errorf("open_children = %v, want 3 read from the refusing transaction", body["open_children"])
				}
			},
		},
		{
			name:       "status crossing with a live blocker",
			body:       `{"actor":"alice","patch":{"status":"closed"}}`,
			err:        fmt.Errorf("close: %w", issueops.ErrCloseBlocked),
			wantStatus: http.StatusConflict,
			wantCode:   CodeNotClosable,
			wantParam:  "patch.status",
			check: func(t *testing.T, body map[string]any) {
				if _, present := body["open_children"]; present {
					t.Error("open_children is present on the blocker refusal; its ABSENCE is what tells the two apart")
				}
			},
		},
		{
			name:       "assignee transfer off a live owner",
			body:       `{"actor":"alice","patch":{"assignee":"carol"}}`,
			err:        fmt.Errorf("update: %w: issue bd-1 is assigned to %q", storage.ErrAlreadyClaimed, "bob"),
			wantStatus: http.StatusConflict,
			wantCode:   CodeAlreadyClaimed,
			wantParam:  "patch.assignee",
			check: func(t *testing.T, body map[string]any) {
				// The fence refuses without a typed holder, so the member is
				// absent — and it must NOT be parsed out of the message text,
				// which is exactly what a client adopting this endpoint deletes.
				if got, present := body["assignee"]; present {
					t.Errorf("assignee = %v; the fence reports no typed holder, so this must not be scraped from the message", got)
				}
			},
		},
		{
			// THE DEFENSIVE ARM, driven directly because no reparent can reach
			// it: CheckBlockingHierarchyInTx probes `blocks` and
			// `conditional-blocks` only, and patch.parent_id writes
			// `parent-child`. It is NOT a promise this operation makes — the
			// document tells clients the hierarchy members never arrive here —
			// and this case pins only that IF the refusal ever reaches this
			// handler it keeps its members instead of collapsing into the plain
			// cycle below, which is the one shape a client cannot tell apart.
			name:       "the defensive hierarchy arm keeps its members",
			body:       `{"actor":"alice","patch":{"parent_id":"bd-child"}}`,
			err:        fmt.Errorf("reparent: %w", &issueops.DependencyHierarchyConflictError{IssueID: "bd-1", BlockerID: "bd-child", BlockerIsAncestor: false}),
			wantStatus: http.StatusConflict,
			wantCode:   CodeDependencyCycle,
			wantParam:  "patch.parent_id",
			check: func(t *testing.T, body map[string]any) {
				if body["issue_id"] != "bd-1" || body["blocker_id"] != "bd-child" {
					t.Errorf("hierarchy members = %v/%v, want the refused pair", body["issue_id"], body["blocker_id"])
				}
				if body["blocker_is_ancestor"] != false {
					t.Errorf("blocker_is_ancestor = %v; it travels in BOTH polarities", body["blocker_is_ancestor"])
				}
			},
		},
		{
			// THE ARM EVERY REAL REPARENT CYCLE TAKES, and the one the proxied
			// integration case drives end to end. Its lack of hierarchy members
			// is what this operation documents.
			name:       "reparent that closes a cycle",
			body:       `{"actor":"alice","patch":{"parent_id":"bd-2"}}`,
			err:        fmt.Errorf("reparent: %w", issueops.ErrDependencyCycle),
			wantStatus: http.StatusConflict,
			wantCode:   CodeDependencyCycle,
			wantParam:  "patch.parent_id",
			check: func(t *testing.T, body map[string]any) {
				for _, member := range []string{"issue_id", "blocker_id", "blocker_is_ancestor"} {
					if _, present := body[member]; present {
						t.Errorf("%s is present; this operation's cycle never carries the hierarchy members", member)
					}
				}
			},
		},
		{
			name:       "reparent onto a pair that already carries another edge",
			body:       `{"actor":"alice","patch":{"parent_id":"bd-2"}}`,
			err:        fmt.Errorf("reparent: %w", &issueops.DependencyTypeConflictError{IssueID: "bd-1", DependsOnID: "bd-2", ExistingType: "blocks", RequestedType: "parent-child"}),
			wantStatus: http.StatusConflict,
			wantCode:   CodeDependencyExists,
			wantParam:  "patch.parent_id",
			check: func(t *testing.T, body map[string]any) {
				if body["existing_type"] != "blocks" || body["requested_type"] != "parent-child" {
					t.Errorf("types = %v/%v, want both read from the typed error", body["existing_type"], body["requested_type"])
				}
			},
		},
		{
			// An edge ENDPOINT, not the resource this request addresses. A 404
			// here would send a client looking for the wrong missing row.
			name:       "reparent onto a parent that names nothing",
			body:       `{"actor":"alice","patch":{"parent_id":"bd-gone"}}`,
			err:        fmt.Errorf("reparent: %w", &issueops.DependencyEndpointNotFoundError{IssueID: "bd-1", DependsOnID: "bd-gone", MissingID: "bd-gone", Err: issueops.ErrDependencyTargetNotFound}),
			wantStatus: http.StatusBadRequest,
			wantCode:   CodeInvalidArgument,
			wantParam:  "patch.parent_id",
			check:      func(*testing.T, map[string]any) {},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			lifecycle := &roleLifecycle{updateErr: test.err}
			ts := newUpdateServer(t, lifecycle)

			resp := ts.updateIssue(t, updatePath, test.body)
			if resp.StatusCode != test.wantStatus {
				t.Fatalf("status = %d, want %d: %s", resp.StatusCode, test.wantStatus, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if body["code"] != string(test.wantCode) {
				t.Errorf("code = %v, want %s", body["code"], test.wantCode)
			}
			if body["param"] != test.wantParam {
				t.Errorf("param = %v, want %q", body["param"], test.wantParam)
			}
			if detail, _ := body["detail"].(string); strings.Contains(detail, "reparent:") || strings.Contains(detail, "is assigned to") {
				t.Errorf("detail quotes the role's own message: %q", detail)
			}
			test.check(t, body)
		})
	}
}

// TestUpdateStillAnswers404ForThePathID keeps the one miss this operation does
// own. Publishing `parent_id` added a second not-found shape to the same
// handler, so the two have to stay distinguishable: the path id is a 404 and an
// edge endpoint is a 400.
func TestUpdateStillAnswers404ForThePathID(t *testing.T) {
	lifecycle := &roleLifecycle{updateErr: fmt.Errorf("update bd-1: %w", storage.ErrNotFound)}
	ts := newUpdateServer(t, lifecycle)

	resp := ts.updateIssue(t, updatePath, `{"actor":"alice","patch":{"parent_id":"bd-2"}}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", resp.StatusCode, readAll(t, resp))
	}
	if body := decodeBody(t, resp); body["code"] != string(CodeNotFound) {
		t.Errorf("code = %v, want %s", body["code"], CodeNotFound)
	}
}

// TestUpdateEmptiesTheParentSetOnAnEmptyString pins the one value of
// `parent_id` whose meaning is not "this is the new parent": the role reads a
// set empty value as "remove every parent-child edge", so it must reach the
// role SET rather than being dropped as an absent member.
func TestUpdateEmptiesTheParentSetOnAnEmptyString(t *testing.T) {
	lifecycle := &roleLifecycle{updateResult: issueops.UpdateResult{Issue: updatedIssue("bd-1"), Changed: true}}
	ts := newUpdateServer(t, lifecycle)

	resp := ts.updateIssue(t, updatePath, `{"actor":"alice","patch":{"parent_id":""}}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	got := lifecycle.updateRequests()
	if len(got) != 1 {
		t.Fatalf("the role was called %d times, want 1", len(got))
	}
	if !got[0].Patch.ParentID.Set || got[0].Patch.ParentID.Value != "" {
		t.Errorf("parent_id = %+v, want a SET empty value — the spelling that unparents", got[0].Patch.ParentID)
	}
}

// TestUpdateGuardsDistinguishAbsentFromEmpty pins the pointer the role models
// the two string guards as. A guard on the EMPTY assignee is how a caller says
// "only if nobody holds it", so an absent member and one holding "" must not
// collapse into each other.
func TestUpdateGuardsDistinguishAbsentFromEmpty(t *testing.T) {
	lifecycle := &roleLifecycle{updateResult: issueops.UpdateResult{Issue: updatedIssue("bd-1"), Changed: true}}
	ts := newUpdateServer(t, lifecycle)

	if resp := ts.updateIssue(t, updatePath, `{"actor":"alice","expected_assignee":"","expected_status":"","patch":{"assignee":"bob"}}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	got := lifecycle.updateRequests()
	if len(got) != 1 {
		t.Fatalf("the role was called %d times, want 1", len(got))
	}
	if got[0].ExpectedAssignee == nil || *got[0].ExpectedAssignee != "" {
		t.Errorf("expected_assignee = %v, want a pointer to the empty string", got[0].ExpectedAssignee)
	}
	if got[0].ExpectedStatus == nil || *got[0].ExpectedStatus != "" {
		t.Errorf("expected_status = %v, want a pointer to the empty status", got[0].ExpectedStatus)
	}
}

// TestUpdateAssemblesTheThreeLabelMembersAsOnePatch is the whole of the
// incremental-label slice, and the assertion is that they are ONE edit rather
// than three members racing to write the same field.
//
// The role applies Replace, then Add, then Remove, so removal wins. A handler
// that built a LabelPatch per member would have the last one decoded overwrite
// the others in silence, which is the failure this shape exists to prevent —
// and the reason the three are assembled in one loop rather than three `if`s.
func TestUpdateAssemblesTheThreeLabelMembersAsOnePatch(t *testing.T) {
	for _, tc := range []struct {
		name    string
		patch   string
		replace *[]string
		add     []string
		remove  []string
	}{
		{
			name:    "replacement alone still sets Replace",
			patch:   `{"labels":["a","b"]}`,
			replace: &[]string{"a", "b"},
		},
		{
			// The case the pair exists for: an addition that does NOT have to
			// name the labels already on the row, so a concurrent writer's
			// label is not silently dropped.
			name:  "an addition alone leaves Replace unset",
			patch: `{"add_labels":["c"]}`,
			add:   []string{"c"},
		},
		{
			name:   "a removal alone leaves Replace unset",
			patch:  `{"remove_labels":["c"]}`,
			remove: []string{"c"},
		},
		{
			// NOT mutually exclusive, unlike `notes`/`append_notes`: the role
			// defines an order over all three, so this request has a defined
			// result and must reach the role whole.
			name:    "all three together reach the role together",
			patch:   `{"labels":["a"],"add_labels":["b"],"remove_labels":["a"]}`,
			replace: &[]string{"a"},
			add:     []string{"b"},
			remove:  []string{"a"},
		},
		{
			name:    "an empty replacement clears, and is not read as absent",
			patch:   `{"labels":[]}`,
			replace: &[]string{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lifecycle := &roleLifecycle{updateResult: issueops.UpdateResult{Issue: updatedIssue("bd-1"), Changed: true}}
			ts := newUpdateServer(t, lifecycle)

			resp := ts.updateIssue(t, updatePath, `{"actor":"alice","patch":`+tc.patch+`}`)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
			}
			got := lifecycle.updateRequests()
			if len(got) != 1 {
				t.Fatalf("the role received %+v, want exactly one request", got)
			}
			labels := got[0].Patch.Labels
			if (tc.replace != nil) != labels.Replace.Set {
				t.Fatalf("Replace.Set = %v, want %v: presence of `labels` is what selects a replacement",
					labels.Replace.Set, tc.replace != nil)
			}
			if tc.replace != nil && !slices.Equal(labels.Replace.Value, *tc.replace) {
				t.Errorf("Replace.Value = %v, want %v", labels.Replace.Value, *tc.replace)
			}
			if !slices.Equal(labels.Add, tc.add) {
				t.Errorf("Add = %v, want %v", labels.Add, tc.add)
			}
			if !slices.Equal(labels.Remove, tc.remove) {
				t.Errorf("Remove = %v, want %v", labels.Remove, tc.remove)
			}
		})
	}
}

// TestUpdateBoundsEveryLabelMember: the length rule is about what a label may
// BE, so it holds on all three members — including `remove_labels`, where a
// value the column could not hold could never have been stored. The role
// refuses it there too and writes nothing, so the edge refuses it where the
// 400 can name the member and the index.
func TestUpdateBoundsEveryLabelMember(t *testing.T) {
	long := strings.Repeat("x", types.MaxFieldLen+1)
	for _, member := range []string{"labels", "add_labels", "remove_labels"} {
		t.Run(member, func(t *testing.T) {
			lifecycle := &roleLifecycle{}
			ts := newUpdateServer(t, lifecycle)

			resp := ts.updateIssue(t, updatePath,
				fmt.Sprintf(`{"actor":"alice","patch":{%q:["ok",%q]}}`, member, long))
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if body["param"] != patchParam(member) {
				t.Errorf("param = %v, want %q", body["param"], patchParam(member))
			}
			// The index is in the detail, so a caller fixing a long list knows
			// which entry to fix without a binary search.
			if detail, _ := body["detail"].(string); !strings.Contains(detail, member+"[1]") {
				t.Errorf("detail does not name the offending entry: %q", detail)
			}
			if got := lifecycle.updateRequests(); len(got) != 0 {
				t.Errorf("a refused label reached the role: %+v", got)
			}
		})
	}
}
