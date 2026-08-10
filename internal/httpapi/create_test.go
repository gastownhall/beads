package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The pins for POST /v0/beads/issues. What is asserted here is the WIRE EDGE —
// that the whole create vocabulary reaches the role faithfully, that the edge
// members arrive as typed edges rather than as fields on the issue, that the
// response is the STORED row, and that each of the role's refusals arrives as
// the documented code. Everything below the wire is the role's, and
// TestProxiedServerServeCreate is where a real row proves it.

const createPath = "/v0/beads/issues"

func newCreateServer(t *testing.T, lifecycle *roleLifecycle) *testServer {
	t.Helper()
	return newTestServer(t, rolesConfig(Config{Lifecycle: lifecycle}))
}

// createdIssue is the row a fake role answers with: deliberately unlike the
// request, so a case that asserted on the response could not pass by reflecting
// the body back.
func createdIssue(id string) *types.Issue {
	return &types.Issue{
		ID:        id,
		Title:     "as stored",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		CreatedAt: time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC),
	}
}

func (ts *testServer) createIssue(t *testing.T, body string) *http.Response {
	t.Helper()
	return ts.claim(t, createPath, body)
}

// TestCreatePathReachesItsHandler drives the plain collection POST, which
// shares its path with listIssues' GET and sits beside three literal `:verb`
// siblings and the claim's wide `/v0/beads/issues/{idop}` wildcard. A 404 or a
// batch response here would mean the request reached one of them.
func TestCreatePathReachesItsHandler(t *testing.T) {
	lifecycle := &roleLifecycle{createResult: issueops.CreateResult{Issue: createdIssue("bd-1")}}
	ts := newCreateServer(t, lifecycle)

	resp := ts.createIssue(t, `{"actor":"alice","title":"one"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	if len(lifecycle.createRequests()) != 1 {
		t.Fatalf("the role was called %d times, want 1 — the path reached another handler", len(lifecycle.createRequests()))
	}
	// The batch beside it must still be reachable: ServeMux prefers the literal
	// segment, and a row that broke that would answer batches here.
	if got := ts.claim(t, "/v0/beads/issues:batchCreate", `{"actor":"alice","items":[{"title":"one"}]}`); got.StatusCode != http.StatusOK {
		t.Fatalf("issues:batchCreate status = %d, want 200: %s", got.StatusCode, readAll(t, got))
	}
	if len(lifecycle.createRequests()) != 1 {
		t.Errorf("the batch reached the single-create handler; the literal segment must win over the collection POST")
	}
}

// TestCreateForwardsEveryDocumentedMember is the operation's central pin. This
// body publishes the whole create vocabulary and every member is projected by
// hand, so one request drives all of them and asserts the request the role
// received, field by field.
//
// It is the R1 lesson made mechanical: issues:batchCreate's item publishes nine
// members, and the five its absence made unusable — status, sender, metadata,
// ephemeral and an explicit id — are asserted here alongside the rest.
func TestCreateForwardsEveryDocumentedMember(t *testing.T) {
	lifecycle := &roleLifecycle{createResult: issueops.CreateResult{Issue: createdIssue("bd-7")}}
	ts := newCreateServer(t, lifecycle)

	resp := ts.createIssue(t, `{
		"actor": "  alice  ",
		"id": "bd-7",
		"force_id_prefix": true,
		"title": "the row",
		"description": "body",
		"design": "how",
		"acceptance_criteria": "done when",
		"notes": "scratch",
		"issue_type": "bug",
		"status": "in_progress",
		"priority": 1,
		"assignee": "bob",
		"owner": "carol",
		"labels": ["api", "wire"],
		"estimated_minutes": 30,
		"external_ref": "gh-9",
		"due_at": "2026-01-02T03:04:05Z",
		"defer_until": "2026-01-01T00:00:00Z",
		"sender": "planner",
		"metadata": {"plan": true},
		"ephemeral": true,
		"parent_id": "bd-parent",
		"inherit_labels_from_parent": true,
		"dependencies": [
			{"target_id":"bd-2","type":"blocks"},
			{"target_id":"bd-3","type":"related","reverse":true,"metadata":{"why":"mirror"}}
		],
		"waits_for": {"spawner_id":"bd-4","gate":"any-children"}
	}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}

	got := lifecycle.createRequests()
	if len(got) != 1 {
		t.Fatalf("the role was called %d times, want 1", len(got))
	}
	req := got[0]
	if req.Actor != "alice" {
		t.Errorf("actor = %q, want the trimmed %q", req.Actor, "alice")
	}
	if req.Issue == nil {
		t.Fatal("the role received no issue")
	}
	issue := req.Issue

	for _, tc := range []struct {
		name      string
		got, want any
	}{
		{"id", issue.ID, "bd-7"},
		{"title", issue.Title, "the row"},
		{"description", issue.Description, "body"},
		{"design", issue.Design, "how"},
		{"acceptance_criteria", issue.AcceptanceCriteria, "done when"},
		{"notes", issue.Notes, "scratch"},
		{"issue_type", issue.IssueType, types.IssueType("bug")},
		{"status", issue.Status, types.Status("in_progress")},
		{"priority", issue.Priority, 1},
		{"assignee", issue.Assignee, "bob"},
		{"owner", issue.Owner, "carol"},
		{"sender", issue.Sender, "planner"},
		{"ephemeral", issue.Ephemeral, true},
		{"no_history", issue.NoHistory, false},
		{"parent_id", req.ParentID, "bd-parent"},
		{"inherit_labels_from_parent", req.InheritLabelsFromParent, true},
		{"force_id_prefix", req.ForceIDPrefix, true},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %v, want %v", tc.name, tc.got, tc.want)
		}
	}
	if strings.Join(issue.Labels, ",") != "api,wire" {
		t.Errorf("labels = %v, want the authoritative two-element set", issue.Labels)
	}
	if issue.EstimatedMinutes == nil || *issue.EstimatedMinutes != 30 {
		t.Errorf("estimated_minutes = %v, want 30", issue.EstimatedMinutes)
	}
	if issue.ExternalRef == nil || *issue.ExternalRef != "gh-9" {
		t.Errorf("external_ref = %v, want gh-9", issue.ExternalRef)
	}
	wantDue := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if issue.DueAt == nil || !issue.DueAt.Equal(wantDue) {
		t.Errorf("due_at = %v, want %v", issue.DueAt, wantDue)
	}
	wantDefer := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if issue.DeferUntil == nil || !issue.DeferUntil.Equal(wantDefer) {
		t.Errorf("defer_until = %v, want %v", issue.DeferUntil, wantDefer)
	}
	if got := strings.ReplaceAll(string(issue.Metadata), " ", ""); got != `{"plan":true}` {
		t.Errorf("metadata = %s, want the caller's own bytes", issue.Metadata)
	}

	// IDPrefix is deliberately unpublished: a remote client's config.yaml
	// describes a workspace this server does not serve.
	if req.IDPrefix != "" {
		t.Errorf("the handler published IDPrefix (%q); the served workspace's prefix rule is not the caller's to override", req.IDPrefix)
	}
	// The issue's own edge lists must stay empty — the role refuses a request
	// that carries them, and the edges travel as request fields instead.
	if len(issue.Dependencies) != 0 || len(issue.Comments) != 0 {
		t.Errorf("the handler put edges or comments on the issue (%d/%d); they belong to the request's own members",
			len(issue.Dependencies), len(issue.Comments))
	}
	// created_at/created_by are unpublished, so a fresh row must reach the role
	// with both zero and take the implementation's own values.
	if !issue.CreatedAt.IsZero() || issue.CreatedBy != "" {
		t.Errorf("the handler set created_at/created_by (%v/%q); neither is published on this operation",
			issue.CreatedAt, issue.CreatedBy)
	}

	if len(req.Dependencies) != 2 {
		t.Fatalf("the role received %d edges, want 2", len(req.Dependencies))
	}
	if req.Dependencies[0] != (issueops.CreateDependency{TargetID: "bd-2", Type: types.DependencyType("blocks")}) {
		t.Errorf("dependencies[0] = %+v, want a forward blocks edge", req.Dependencies[0])
	}
	reverse := req.Dependencies[1]
	if reverse.TargetID != "bd-3" || reverse.Type != types.DependencyType("related") || !reverse.Reverse {
		t.Errorf("dependencies[1] = %+v, want a reverse related edge", reverse)
	}
	if got := strings.ReplaceAll(reverse.Metadata, " ", ""); got != `{"why":"mirror"}` {
		t.Errorf("dependencies[1].metadata = %s, want the caller's own bytes", reverse.Metadata)
	}
	if reverse.ThreadID != "" {
		t.Errorf("dependencies[1].thread_id = %q; this document publishes no thread id", reverse.ThreadID)
	}

	if req.WaitsFor == nil {
		t.Fatal("the role received no waits-for gate")
	}
	if *req.WaitsFor != (issueops.WaitsFor{SpawnerID: "bd-4", Gate: "any-children"}) {
		t.Errorf("waits_for = %+v, want the typed spawner and gate", *req.WaitsFor)
	}
}

// TestCreateCarriesTheWaitsForGateAsSentOrDefaulted pins the one member whose
// meaning is split between the wire and the role: an omitted `gate` reaches the
// role EMPTY rather than defaulted here, because the role documents empty as
// all-children and a second default at the edge would be a second definition.
func TestCreateCarriesTheWaitsForGateAsSentOrDefaulted(t *testing.T) {
	for _, test := range []struct {
		name     string
		body     string
		wantGate string
	}{
		{"gate omitted", `{"actor":"alice","title":"one","waits_for":{"spawner_id":"bd-4"}}`, ""},
		{"gate sent", `{"actor":"alice","title":"one","waits_for":{"spawner_id":"bd-4","gate":"all-children"}}`, "all-children"},
	} {
		t.Run(test.name, func(t *testing.T) {
			lifecycle := &roleLifecycle{createResult: issueops.CreateResult{Issue: createdIssue("bd-1")}}
			ts := newCreateServer(t, lifecycle)

			if resp := ts.createIssue(t, test.body); resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
			}
			got := lifecycle.createRequests()
			if len(got) != 1 || got[0].WaitsFor == nil {
				t.Fatalf("the role received no waits-for gate")
			}
			if got[0].WaitsFor.Gate != test.wantGate {
				t.Errorf("gate = %q, want %q", got[0].WaitsFor.Gate, test.wantGate)
			}
			if got[0].WaitsFor.SpawnerID != "bd-4" {
				t.Errorf("spawner_id = %q, want bd-4", got[0].WaitsFor.SpawnerID)
			}
		})
	}
}

// TestCreateAnswersTheStoredRow pins that the response is the role's snapshot
// and not the request reflected back. The minted id, the defaulted status and
// the persisted timestamps exist nowhere else, and a handler that echoed the
// body would look correct on every field the caller happened to send.
func TestCreateAnswersTheStoredRow(t *testing.T) {
	lifecycle := &roleLifecycle{createResult: issueops.CreateResult{Issue: createdIssue("bd-minted")}}
	ts := newCreateServer(t, lifecycle)

	resp := ts.createIssue(t, `{"actor":"alice","title":"as sent","priority":0}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if body["id"] != "bd-minted" {
		t.Errorf("id = %v, want the id the role minted", body["id"])
	}
	if body["title"] != "as stored" {
		t.Errorf("title = %v, want the STORED title; the request's own was %q", body["title"], "as sent")
	}
	if body["created_at"] == nil || body["updated_at"] == nil {
		t.Errorf("the response omitted the persisted timestamps: %v", body)
	}
}

// TestCreateRefusesAnOccupiedExplicitID pins the 409 and its `param`. It is a
// conflict rather than a 400 because the body is well-formed and stays
// well-formed: the identical request succeeded before the id was taken.
func TestCreateRefusesAnOccupiedExplicitID(t *testing.T) {
	lifecycle := &roleLifecycle{createErr: fmt.Errorf("create bd-7: %w", storage.ErrAlreadyExists)}
	ts := newCreateServer(t, lifecycle)

	resp := ts.createIssue(t, `{"actor":"alice","title":"one","id":"bd-7"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if body["code"] != "already_exists" {
		t.Errorf("code = %v, want already_exists", body["code"])
	}
	if body["param"] != "id" {
		t.Errorf("param = %v, want id — the member that earned the refusal", body["param"])
	}
}

// TestCreateRefusesAGraphTheEdgesWouldBreak pins both arms of the one
// dependency_cycle code, and the member PRESENCE that tells them apart. The
// hierarchy refusal carries issue_id/blocker_id/blocker_is_ancestor and the
// plain scheduling cycle carries none, which is the discriminator the document
// promises for POST /v0/beads/dependencies:add and inherits here.
func TestCreateRefusesAGraphTheEdgesWouldBreak(t *testing.T) {
	for _, test := range []struct {
		name          string
		err           error
		wantHierarchy bool
	}{
		{
			name: "scheduling cycle",
			err:  fmt.Errorf("%w: %w", storage.ErrValidation, issueops.ErrDependencyCycle),
		},
		{
			name: "hierarchy conflict",
			err: fmt.Errorf("%w: %w", storage.ErrValidation, &issueops.DependencyHierarchyConflictError{
				IssueID: "bd-7", BlockerID: "bd-parent", BlockerIsAncestor: true,
			}),
			wantHierarchy: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			lifecycle := &roleLifecycle{createErr: test.err}
			ts := newCreateServer(t, lifecycle)

			resp := ts.createIssue(t, `{"actor":"alice","title":"one","parent_id":"bd-parent","dependencies":[{"target_id":"bd-parent","type":"blocks"}]}`)
			if resp.StatusCode != http.StatusConflict {
				t.Fatalf("status = %d, want 409: %s", resp.StatusCode, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if body["code"] != "dependency_cycle" {
				t.Errorf("code = %v, want dependency_cycle", body["code"])
			}
			_, hasIssue := body["issue_id"]
			_, hasBlocker := body["blocker_id"]
			_, hasAncestor := body["blocker_is_ancestor"]
			present := hasIssue && hasBlocker && hasAncestor
			if present != test.wantHierarchy {
				t.Errorf("hierarchy members present = %v, want %v — presence is the discriminator: %v",
					present, test.wantHierarchy, body)
			}
			if test.wantHierarchy && body["blocker_is_ancestor"] != true {
				t.Errorf("blocker_is_ancestor = %v, want true", body["blocker_is_ancestor"])
			}
		})
	}
}

// TestCreateAnswersAMissingEdgeTargetWithA400 is the operation's not-found
// posture, and the case that proves it has none. The role wraps a dangling
// target in BOTH ErrValidation and ErrNotFound; answering 404 would tell a
// client its request went to the wrong place, when this operation names no
// resource in its path at all.
func TestCreateAnswersAMissingEdgeTargetWithA400(t *testing.T) {
	lifecycle := &roleLifecycle{createErr: fmt.Errorf("create: dependency target does not exist: %w: %w",
		storage.ErrValidation, storage.ErrNotFound)}
	ts := newCreateServer(t, lifecycle)

	resp := ts.createIssue(t, `{"actor":"alice","title":"one","dependencies":[{"target_id":"bd-gone","type":"blocks"}]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if body["code"] != "invalid_argument" {
		t.Errorf("code = %v, want invalid_argument", body["code"])
	}
	if body["param"] != "dependencies" {
		t.Errorf("param = %v, want dependencies", body["param"])
	}
	if detail, _ := body["detail"].(string); strings.Contains(detail, "dependency target does not exist") {
		t.Errorf("detail quotes the role's own message: %q", detail)
	}
}

// TestCreateMapsTheRolesOwnRefusals pins the remaining 400s: each one names the
// member a client can act on where there is one, and none of them quotes the
// role's prose.
func TestCreateMapsTheRolesOwnRefusals(t *testing.T) {
	for _, test := range []struct {
		name      string
		err       error
		wantParam any
	}{
		{
			name:      "prefix mismatch names the id",
			err:       fmt.Errorf("%w: %w", storage.ErrValidation, issueops.ErrPrefixMismatch),
			wantParam: "id",
		},
		{
			name:      "self dependency names the edges",
			err:       fmt.Errorf("%w: %w", storage.ErrValidation, issueops.ErrSelfDependency),
			wantParam: "dependencies",
		},
		{
			// The workspace's own vocabularies can refuse a member this request
			// does not carry, so there is nothing honest to name.
			name:      "workspace vocabulary names nothing",
			err:       fmt.Errorf("%w: unknown issue type", storage.ErrValidation),
			wantParam: nil,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			lifecycle := &roleLifecycle{createErr: test.err}
			ts := newCreateServer(t, lifecycle)

			resp := ts.createIssue(t, `{"actor":"alice","title":"one","id":"xx-1"}`)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if body["param"] != test.wantParam {
				t.Errorf("param = %v, want %v", body["param"], test.wantParam)
			}
			if detail, _ := body["detail"].(string); strings.Contains(detail, "unknown issue type") {
				t.Errorf("detail quotes the role's own message: %q", detail)
			}
		})
	}
}

// TestCreateRefusesUnknownMembersAtEveryLevel is the version-skew pin. An
// unknown member is refused BY NAME at each of the three levels, which is what
// makes additionalProperties: false enforceable by a client that has stopped
// parsing prose.
func TestCreateRefusesUnknownMembersAtEveryLevel(t *testing.T) {
	for _, test := range []struct {
		name      string
		body      string
		wantParam string
	}{
		{
			name:      "request level",
			body:      `{"actor":"alice","title":"one","created_at":"2026-01-01T00:00:00Z"}`,
			wantParam: "created_at",
		},
		{
			name:      "edge level",
			body:      `{"actor":"alice","title":"one","dependencies":[{"target_id":"bd-2","type":"blocks","thread_id":"t1"}]}`,
			wantParam: "dependencies[0].thread_id",
		},
		{
			name:      "waits-for level",
			body:      `{"actor":"alice","title":"one","waits_for":{"spawner_id":"bd-4","also_blocks":true}}`,
			wantParam: "waits_for.also_blocks",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			lifecycle := &roleLifecycle{}
			ts := newCreateServer(t, lifecycle)

			resp := ts.createIssue(t, test.body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if body["param"] != test.wantParam {
				t.Errorf("param = %v, want %q: a client learns WHICH level and WHICH member from this alone",
					body["param"], test.wantParam)
			}
			if body["reason"] != "unknown_parameter" {
				t.Errorf("reason = %v, want unknown_parameter", body["reason"])
			}
			if len(lifecycle.createRequests()) != 0 {
				t.Error("the role was called for a refused request; nothing may reach it")
			}
		})
	}
}

// TestCreateRefusesTheShapesTheDocumentRefuses walks the rest of the body
// vocabulary. Every one of these is refused BEFORE any database work, which is
// what lets the detail reflect the caller's own input back.
func TestCreateRefusesTheShapesTheDocumentRefuses(t *testing.T) {
	for _, test := range []struct {
		name      string
		body      string
		wantParam any
	}{
		{"no title", `{"actor":"alice"}`, "title"},
		{"blank title", `{"actor":"alice","title":"   "}`, "title"},
		{"no actor", `{"title":"one"}`, "actor"},
		{"blank actor", `{"actor":"  ","title":"one"}`, "actor"},
		{"actor with a newline", "{\"actor\":\"a\\nb\",\"title\":\"one\"}", "actor"},
		{"priority out of range", `{"actor":"alice","title":"one","priority":9}`, "priority"},
		{"priority is a string", `{"actor":"alice","title":"one","priority":"high"}`, ""},
		{"both retention modes", `{"actor":"alice","title":"one","ephemeral":true,"no_history":true}`, "no_history"},
		{"control character in a stored column", "{\"actor\":\"alice\",\"title\":\"one\",\"sender\":\"a\\u0001b\"}", "sender"},
		{"edge with no target", `{"actor":"alice","title":"one","dependencies":[{"target_id":"","type":"blocks"}]}`, "dependencies[0].target_id"},
		{"edge with an unstorable type", `{"actor":"alice","title":"one","dependencies":[{"target_id":"bd-2","type":""}]}`, "dependencies[0].type"},
		{"waits-for with no spawner", `{"actor":"alice","title":"one","waits_for":{"gate":"all-children"}}`, "waits_for.spawner_id"},
		{"waits-for is not an object", `{"actor":"alice","title":"one","waits_for":"bd-4"}`, "waits_for"},
		{"body is not an object", `["actor"]`, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			lifecycle := &roleLifecycle{}
			ts := newCreateServer(t, lifecycle)

			resp := ts.createIssue(t, test.body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if test.wantParam == "" {
				// A member whose TYPE is wrong is reported against the whole
				// body: the typed decode reports the offender only inside an
				// error string, which this surface does not quote.
				if body["param"] != "" && body["param"] != nil {
					t.Errorf("param = %v, want the body-level refusal", body["param"])
				}
			} else if body["param"] != test.wantParam {
				t.Errorf("param = %v, want %v", body["param"], test.wantParam)
			}
			if len(lifecycle.createRequests()) != 0 {
				t.Error("the role was called for a refused request; nothing may reach it")
			}
		})
	}
}

// TestCreateRefusesAnExplicitNullOnEveryMemberButMetadata pins the rule that
// makes this body's nullable set empty.
//
// A null unmarshals into *T as nil, which is indistinguishable from omission,
// so without the check the value the client asked for would be silently
// replaced by the workspace default. `metadata` is the exception and its bytes
// reach the role verbatim, because the metadata plane is the one place where
// `null` is a value rather than an absence — and the role, not this handler, is
// the definition of what that plane accepts.
func TestCreateRefusesAnExplicitNullOnEveryMemberButMetadata(t *testing.T) {
	for _, member := range []string{"priority", "ephemeral", "labels", "estimated_minutes", "external_ref", "due_at", "parent_id", "dependencies", "waits_for"} {
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

	t.Run("metadata null reaches the role", func(t *testing.T) {
		lifecycle := &roleLifecycle{createResult: issueops.CreateResult{Issue: createdIssue("bd-1")}}
		ts := newCreateServer(t, lifecycle)

		resp := ts.createIssue(t, `{"actor":"alice","title":"one","metadata":null}`)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
		}
		got := lifecycle.createRequests()
		if len(got) != 1 || got[0].Issue == nil {
			t.Fatalf("the role was called %d times, want 1", len(got))
		}
		if string(got[0].Issue.Metadata) != "null" {
			t.Errorf("metadata = %q, want the literal null the caller sent", got[0].Issue.Metadata)
		}
	})

	t.Run("metadata absent leaves the document unset", func(t *testing.T) {
		lifecycle := &roleLifecycle{createResult: issueops.CreateResult{Issue: createdIssue("bd-1")}}
		ts := newCreateServer(t, lifecycle)

		if resp := ts.createIssue(t, `{"actor":"alice","title":"one"}`); resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
		}
		got := lifecycle.createRequests()
		if len(got) != 1 || got[0].Issue == nil {
			t.Fatalf("the role was called %d times, want 1", len(got))
		}
		if got[0].Issue.Metadata != nil {
			t.Errorf("metadata = %q, want nil: an absent member and a present null are different requests", got[0].Issue.Metadata)
		}
	})
}

// TestCreateIssueRequestDecodesAPresentNullMetadata is the client-side half of
// the rule above, and the one nothing else can see.
//
// `metadata` is a MetadataValue, and a *json.RawMessage cannot READ a present
// null: encoding/json sets the pointer to nil before any UnmarshalJSON runs, so
// a generated client would send `{"metadata":null}` and decode it back as an
// omitted member. The wire does not change in either direction, which is why
// only a decode INTO the generated struct catches a regenerated spec that lost
// x-go-type-skip-optional-pointer.
func TestCreateIssueRequestDecodesAPresentNullMetadata(t *testing.T) {
	var present apigen.CreateIssueRequest
	if err := json.Unmarshal([]byte(`{"actor":"alice","title":"one","metadata":null}`), &present); err != nil {
		t.Fatalf("decode a present null: %v", err)
	}
	if string(present.Metadata) != "null" {
		t.Errorf("metadata = %q, want the literal null; the generated member cannot carry a present null anymore", present.Metadata)
	}

	var absent apigen.CreateIssueRequest
	if err := json.Unmarshal([]byte(`{"actor":"alice","title":"one"}`), &absent); err != nil {
		t.Fatalf("decode an absent member: %v", err)
	}
	if absent.Metadata != nil {
		t.Errorf("metadata = %q, want nil for an absent member", absent.Metadata)
	}
}

// TestCreateRefusesARoleThatReportsSuccessWithoutARow pins the fold
// checkedLifecycle.Create exists for. The handler writes *result.Issue straight
// onto the wire, and a provider-supplied role is ordinary caller code, so a nil
// with a nil error must be the generic 500 with the fault in the log.
//
// THE STATUS ALONE CANNOT SEE THE FOLD, which is the whole reason this case is
// written the way the claimer's twin is: a handler that dereferenced the nil
// panics, the server recovers it into the SAME 500 with the same body, and a
// case that asserted only the status would stay green against the exact
// regression it is named for. What distinguishes them is the LOG: the fold's
// own sentence reaches the request_error line, and no panic is recovered.
//
// The substring matched below is the FOLD'S MESSAGE, not an operation field —
// the request line carries the op, the error line carries the error, and this
// case is about the second. Reword the fold and reword this.
func TestCreateRefusesARoleThatReportsSuccessWithoutARow(t *testing.T) {
	lifecycle := &roleLifecycle{createResult: issueops.CreateResult{}}
	ts := newCreateServer(t, lifecycle)

	resp := ts.createIssue(t, `{"actor":"alice","title":"one"}`)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", resp.StatusCode, readAll(t, resp))
	}
	if body := decodeBody(t, resp); body["code"] != string(CodeInternal) {
		t.Errorf("code = %v, want %s", body["code"], CodeInternal)
	}
	assertNoPanic(t, ts)
	if line := findLogLine(t, ts.stderr.String(), "event=request_error"); !strings.Contains(line, "reported success without an issue") {
		t.Errorf("the 500 is logged without the fold's own reason, so an operator cannot tell it from any other internal error:\n%s", line)
	}
}

// TestCreateRefusesTheWrongMediaTypeAndAnyQuery keeps the two uniform rules on
// this operation: a body this server will not read, and a parameter it does not
// know. Neither may reach the role.
func TestCreateRefusesTheWrongMediaTypeAndAnyQuery(t *testing.T) {
	lifecycle := &roleLifecycle{}
	ts := newCreateServer(t, lifecycle)

	if resp := ts.postBody(t, createPath, "text/plain", `{"actor":"alice","title":"one"}`); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("text/plain status = %d, want 400", resp.StatusCode)
	}
	resp := ts.claim(t, createPath+"?dry_run=true", `{"actor":"alice","title":"one"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("query status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	if body := decodeBody(t, resp); body["param"] != "dry_run" || body["reason"] != "unknown_parameter" {
		t.Errorf("param/reason = %v/%v, want dry_run/unknown_parameter", body["param"], body["reason"])
	}
	if len(lifecycle.createRequests()) != 0 {
		t.Error("the role was called for a refused request")
	}
}
