package httpapi

import (
	"fmt"
	"net/http"
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

// TestUpdateRejectsTheShapesTheDocumentRefuses is the body vocabulary. The
// EXCLUDED members are the important half: status, assignee and metadata are
// refused by NAME, so a client that tries to smuggle lifecycle through a patch
// is told what happened rather than having it ignored.
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
		// The three the spec argues out of the vocabulary, refused by name.
		{"status is not a patch member", `{"actor":"alice","patch":{"status":"closed"}}`, "patch.status"},
		{"assignee is not a patch member", `{"actor":"alice","patch":{"assignee":"bob"}}`, "patch.assignee"},
		{"metadata is not a patch member", `{"actor":"alice","patch":{"metadata":{}}}`, "patch.metadata"},
		{"parent_id is not a patch member", `{"actor":"alice","patch":{"parent_id":"bd-2"}}`, "patch.parent_id"},
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
