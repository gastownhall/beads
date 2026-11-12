package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The purity pins for the five plain /v0 writes: create, update, close, reopen
// and delete. Each handler's job is to decode the body into the role's request
// and hand that request over UNCHANGED — every other decision belongs to
// issueops. What these pins add over each operation's own forwarding cases is
// the WHOLE request: the role must receive exactly the struct the wire spelled,
// so a field the body never named cannot arrive set.
//
// That is the shape the next change to these handlers takes. A guard that a
// second protocol computes and threads in beside the decoded request — a
// row-check hook read off the request context, a refusal flag, a timestamp
// mode — must stay nil or zero on a plain /v0 request, and reflect.DeepEqual
// holds it to that the moment the field exists: a func field is equal only
// when both sides are nil, and a pointer field compares what it points at, so
// any non-nil value against this table's nil fails.

// plainV0WriteCase is one plain /v0 write and the role request it must arrive
// as, field for field.
type plainV0WriteCase struct {
	name string
	// send issues the write through the real server.
	send func(t *testing.T, ts *testServer) *http.Response
	// want is the role request the body decodes to: a value of the role's own
	// request type, every field the body did not name left zero.
	want any
}

// plainV0DeliberatelyZero names the request fields no plain /v0 write fills,
// keyed "issueops.<Struct>.<Field>", each with the reason.
// TestPlainV0WriteCasesDecideEveryRequestField holds every other field of the
// five request types to a case that fills it, so a field added to one of them
// gets a decision: a wire member a case sends, or an entry here.
var plainV0DeliberatelyZero = map[string]string{
	"issueops.CreateRequest.IDPrefix":       "a local front door's config.yaml prefix; a remote caller's would override the served workspace's rule (create.go)",
	"issueops.UpdateRequest.IssuePlaneOnly": "updateIssue resolves across both planes (update.go)",
}

// plainV0WriteCases is the table: for each operation, the smallest body it
// accepts and the fullest, with update split three ways because its body
// refuses some members beside others (`claim` beside the compare-and-set
// guards, `force_assignee_transfer` beside `expected_assignee`, `notes`
// beside `append_notes`).
func plainV0WriteCases() []plainV0WriteCase {
	version := int64(41)
	estimate := 30
	ref := "gh-1"
	due := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	deferUntil := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	openStatus := issueops.Status("open")
	nobody := ""
	return []plainV0WriteCase{
		{
			name: "create/required members only",
			send: func(t *testing.T, ts *testServer) *http.Response {
				return ts.createIssue(t, `{"actor":"alice","title":"one"}`)
			},
			want: issueops.CreateRequest{
				Actor:           "alice",
				Issue:           &types.Issue{Title: "one"},
				DefaultPriority: true,
			},
		},
		{
			name: "create/every member",
			send: func(t *testing.T, ts *testServer) *http.Response {
				return ts.createIssue(t, `{
					"actor": "  alice  ",
					"id": "bd-new",
					"title": "Full create",
					"description": "desc",
					"design": "design",
					"acceptance_criteria": "ac",
					"notes": "notes",
					"status": "open",
					"issue_type": "task",
					"assignee": "bob",
					"owner": "carol",
					"estimated_minutes": 30,
					"external_ref": "gh-1",
					"due_at": "2026-12-01T00:00:00Z",
					"defer_until": "2026-11-15T00:00:00Z",
					"sender": "dave",
					"ephemeral": true,
					"no_history": false,
					"priority": 1,
					"labels": ["a", "b"],
					"metadata": {"k":"v"},
					"await_type": "gh:pr",
					"await_id": "123",
					"timeout": 60000000000,
					"waiters": ["w1"],
					"parent_id": "bd-parent",
					"inherit_labels_from_parent": true,
					"force_id_prefix": true,
					"dependencies": [{"target_id":"bd-2","type":"blocks","reverse":true,"metadata":{"m":1}}],
					"waits_for": {"spawner_id":"bd-3","gate":"any-children"}
				}`)
			},
			want: issueops.CreateRequest{
				Actor: "alice",
				Issue: &types.Issue{
					ID:                 "bd-new",
					Title:              "Full create",
					Description:        "desc",
					Design:             "design",
					AcceptanceCriteria: "ac",
					Notes:              "notes",
					Status:             types.StatusOpen,
					IssueType:          types.TypeTask,
					Assignee:           "bob",
					Owner:              "carol",
					EstimatedMinutes:   &estimate,
					ExternalRef:        &ref,
					DueAt:              &due,
					DeferUntil:         &deferUntil,
					Sender:             "dave",
					Ephemeral:          true,
					Priority:           1,
					Labels:             []string{"a", "b"},
					Metadata:           json.RawMessage(`{"k":"v"}`),
					AwaitType:          "gh:pr",
					AwaitID:            "123",
					Timeout:            time.Minute,
					Waiters:            []string{"w1"},
				},
				ParentID:                "bd-parent",
				InheritLabelsFromParent: true,
				ForceIDPrefix:           true,
				Dependencies: []issueops.CreateDependency{
					{TargetID: "bd-2", Type: types.DepBlocks, Reverse: true, Metadata: `{"m":1}`},
				},
				WaitsFor: &issueops.WaitsFor{SpawnerID: "bd-3", Gate: "any-children"},
			},
		},
		{
			name: "update/one patch member",
			send: func(t *testing.T, ts *testServer) *http.Response {
				return ts.updateIssue(t, updatePath, `{"actor":"alice","patch":{"title":"t"}}`)
			},
			want: issueops.UpdateRequest{
				Actor:      "alice",
				IssueID:    "bd-1",
				Patch:      issueops.IssuePatch{Title: issueops.Field[string]{Set: true, Value: "t"}},
				Provenance: updateProvenance,
			},
		},
		{
			name: "update/claim beside every patch member it admits",
			send: func(t *testing.T, ts *testServer) *http.Response {
				return ts.updateIssue(t, updatePath, `{
					"actor": "alice",
					"claim": true,
					"expected_version": "41",
					"force_close_policy": true,
					"force_notes_overwrite": true,
					"allow_template": true,
					"patch": {
						"title": "t",
						"description": "d",
						"design": "g",
						"acceptance_criteria": "ac",
						"notes": "n",
						"priority": 1,
						"issue_type": "bug",
						"status": "in_progress",
						"parent_id": "bd-9",
						"labels": ["x"],
						"add_labels": ["y"],
						"remove_labels": ["z"],
						"metadata": {"set":{"k":"v"},"unset":["gone"]},
						"estimated_minutes": null,
						"external_ref": "gh-2",
						"due_at": "2026-12-01T00:00:00Z",
						"defer_until": null,
						"persistence": "persistent",
						"await_id": "a1",
						"waiters": ["w"]
					}
				}`)
			},
			want: issueops.UpdateRequest{
				Actor:   "alice",
				IssueID: "bd-1",
				Patch: issueops.IssuePatch{
					Title:              issueops.Field[string]{Set: true, Value: "t"},
					Description:        issueops.Field[string]{Set: true, Value: "d"},
					Design:             issueops.Field[string]{Set: true, Value: "g"},
					AcceptanceCriteria: issueops.Field[string]{Set: true, Value: "ac"},
					Notes:              issueops.Field[string]{Set: true, Value: "n"},
					Priority:           issueops.Field[int]{Set: true, Value: 1},
					IssueType:          issueops.Field[issueops.IssueType]{Set: true, Value: "bug"},
					Status:             issueops.Field[issueops.Status]{Set: true, Value: "in_progress"},
					ParentID:           issueops.Field[string]{Set: true, Value: "bd-9"},
					Labels: issueops.LabelPatch{
						Replace: issueops.Field[[]string]{Set: true, Value: []string{"x"}},
						Add:     []string{"y"},
						Remove:  []string{"z"},
					},
					Metadata: issueops.MetadataPatch{
						Set:   map[string]json.RawMessage{"k": json.RawMessage(`"v"`)},
						Unset: []string{"gone"},
					},
					EstimatedMinutes: issueops.Field[*int]{Set: true},
					ExternalRef:      issueops.Field[*string]{Set: true, Value: strPtr("gh-2")},
					DueAt:            issueops.Field[*time.Time]{Set: true, Value: &due},
					DeferUntil:       issueops.Field[*time.Time]{Set: true},
					Persistence:      issueops.Field[issueops.PersistenceMode]{Set: true, Value: issueops.PersistenceModePersistent},
					AwaitID:          issueops.Field[string]{Set: true, Value: "a1"},
					Waiters:          issueops.Field[[]string]{Set: true, Value: []string{"w"}},
				},
				Claim:               true,
				ExpectedVersion:     &version,
				ForceClosePolicy:    true,
				ForceNotesOverwrite: true,
				AllowTemplate:       true,
				Provenance:          updateProvenance,
			},
		},
		{
			name: "update/the compare-and-set guards",
			send: func(t *testing.T, ts *testServer) *http.Response {
				return ts.updateIssue(t, updatePath, `{"actor":"alice","expected_status":"open","expected_assignee":"","patch":{"append_notes":"more"}}`)
			},
			want: issueops.UpdateRequest{
				Actor:            "alice",
				IssueID:          "bd-1",
				Patch:            issueops.IssuePatch{AppendNotes: issueops.Field[string]{Set: true, Value: "more"}},
				ExpectedStatus:   &openStatus,
				ExpectedAssignee: &nobody,
				Provenance:       updateProvenance,
			},
		},
		{
			name: "update/a forced assignee transfer",
			send: func(t *testing.T, ts *testServer) *http.Response {
				return ts.updateIssue(t, updatePath, `{"actor":"alice","force_assignee_transfer":true,"patch":{"assignee":"bob"}}`)
			},
			want: issueops.UpdateRequest{
				Actor:                 "alice",
				IssueID:               "bd-1",
				Patch:                 issueops.IssuePatch{Assignee: issueops.Field[string]{Set: true, Value: "bob"}},
				ForceAssigneeTransfer: true,
				Provenance:            updateProvenance,
			},
		},
		{
			name: "close/actor only",
			send: func(t *testing.T, ts *testServer) *http.Response {
				return ts.closeIssue(t, closePath, `{"actor":"alice"}`)
			},
			want: issueops.CloseRequest{Actor: "alice", IssueID: "bd-1"},
		},
		{
			name: "close/every member",
			send: func(t *testing.T, ts *testServer) *http.Response {
				return ts.closeIssue(t, closePath, `{"actor":"alice","reason":"done","session":"s-1","force":true,"expected_version":"41","auto_close_molecule":true}`)
			},
			want: issueops.CloseRequest{
				Actor: "alice", IssueID: "bd-1", Reason: "done", Session: "s-1",
				Force: true, ExpectedVersion: &version, AutoCloseMolecule: true,
			},
		},
		{
			name: "reopen/actor only",
			send: func(t *testing.T, ts *testServer) *http.Response {
				return ts.reopenIssue(t, reopenPath, `{"actor":"alice"}`)
			},
			want: issueops.ReopenRequest{Actor: "alice", IssueID: "bd-1", Provenance: reopenProvenance},
		},
		{
			name: "reopen/every member",
			send: func(t *testing.T, ts *testServer) *http.Response {
				return ts.reopenIssue(t, reopenPath, `{"actor":"alice","reason":"again","expected_version":"41"}`)
			},
			want: issueops.ReopenRequest{
				Actor: "alice", IssueID: "bd-1", Reason: "again",
				ExpectedVersion: &version, Provenance: reopenProvenance,
			},
		},
		{
			name: "delete/ids only",
			send: func(t *testing.T, ts *testServer) *http.Response {
				return ts.delete(t, `{"ids":["bd-1"]}`)
			},
			want: issueops.DeleteRequest{IDs: []string{"bd-1"}},
		},
		{
			name: "delete/every member beside a guard",
			send: func(t *testing.T, ts *testServer) *http.Response {
				return ts.delete(t, `{"ids":["bd-1"],"actor":"alice","cascade":true,"force":true,"dry_run":true,"expected_version":"41"}`)
			},
			want: issueops.DeleteRequest{
				IDs: []string{"bd-1"}, Actor: "alice", Cascade: true, Force: true,
				DryRun: true, ExpectedVersion: &version,
			},
		},
	}
}

// TestPlainV0WritesForwardExactlyTheDecodedRequest drives each case through
// recording roles and requires exactly one role call, carrying exactly the
// decoded request.
//
// Watched to fail before landing: with `IssuePlaneOnly: true` added to
// updateRequest's result, every update case names IssuePlaneOnly; with a
// scratch `InTx` func field on issueops.UpdateRequest that the handler fills
// unconditionally, every update case names InTx — and filling it from a
// request-context value that a plain /v0 request never carries stays green.
func TestPlainV0WritesForwardExactlyTheDecodedRequest(t *testing.T) {
	for _, tc := range plainV0WriteCases() {
		t.Run(tc.name, func(t *testing.T) {
			lifecycle := &roleLifecycle{
				createResult: issueops.CreateResult{Issue: createdIssue("bd-1")},
				updateResult: issueops.UpdateResult{Issue: seededIssue("bd-1", "alice", types.StatusOpen), Changed: true},
				closeResult:  issueops.CloseResult{Issue: closedIssue("bd-1"), Changed: true},
				reopenResult: issueops.ReopenResult{Issue: reopenedIssue("bd-1"), Changed: true},
			}
			deleter := &roleDeleter{}
			ts := newTestServer(t, rolesConfig(Config{Lifecycle: lifecycle, Deleter: deleter}))

			resp := tc.send(t, ts)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
			}
			got := recordedPlainV0Writes(lifecycle, deleter)
			if len(got) != 1 {
				t.Fatalf("the write reached the roles %d times (%v), want exactly once", len(got), got)
			}
			if fields := requestFieldsThatDiffer(got[0], tc.want); len(fields) > 0 {
				t.Errorf("%T differs from the decoded wire request in %s:\n got %+v\nwant %+v",
					got[0], strings.Join(fields, ", "), got[0], tc.want)
			}
		})
	}
}

// recordedPlainV0Writes is every request the two write roles received, in a
// fixed order, so a write that reached the wrong method — or two — shows.
func recordedPlainV0Writes(lifecycle *roleLifecycle, deleter *roleDeleter) []any {
	var all []any
	for _, r := range lifecycle.createRequests() {
		all = append(all, r)
	}
	for _, r := range lifecycle.updateRequests() {
		all = append(all, r)
	}
	for _, r := range lifecycle.closeRequests() {
		all = append(all, r)
	}
	for _, r := range lifecycle.reopenRequests() {
		all = append(all, r)
	}
	for _, r := range deleter.requests() {
		all = append(all, r)
	}
	return all
}

// requestFieldsThatDiffer names the top-level fields where got and want are not
// reflect.DeepEqual, so a failure says which field leaked rather than printing
// two structs to compare by eye.
func requestFieldsThatDiffer(got, want any) []string {
	gv, wv := reflect.ValueOf(got), reflect.ValueOf(want)
	if gv.Type() != wv.Type() {
		return []string{fmt.Sprintf("its type (got %s, want %s)", gv.Type(), wv.Type())}
	}
	if gv.Kind() != reflect.Struct {
		if reflect.DeepEqual(got, want) {
			return nil
		}
		return []string{"its value"}
	}
	var fields []string
	for i := range gv.NumField() {
		if !reflect.DeepEqual(gv.Field(i).Interface(), wv.Field(i).Interface()) {
			fields = append(fields, gv.Type().Field(i).Name)
		}
	}
	return fields
}

// TestPlainV0WriteCasesDecideEveryRequestField keeps the table honest as the
// requests grow: every exported field of the five request types is filled by
// some case's want, or listed in plainV0DeliberatelyZero with a reason — never
// both, and never neither.
func TestPlainV0WriteCasesDecideEveryRequestField(t *testing.T) {
	filled := map[string]bool{}
	seen := map[reflect.Type]bool{}
	for _, tc := range plainV0WriteCases() {
		v := reflect.ValueOf(tc.want)
		seen[v.Type()] = true
		for i := range v.NumField() {
			if !v.Field(i).IsZero() {
				filled[qualifiedRequestField(v.Type(), v.Type().Field(i).Name)] = true
			}
		}
	}
	for _, typ := range []reflect.Type{
		reflect.TypeOf(issueops.CreateRequest{}), reflect.TypeOf(issueops.UpdateRequest{}),
		reflect.TypeOf(issueops.CloseRequest{}), reflect.TypeOf(issueops.ReopenRequest{}),
		reflect.TypeOf(issueops.DeleteRequest{}),
	} {
		if !seen[typ] {
			t.Errorf("no plain /v0 write case arrives as %s", typ)
		}
		for i := range typ.NumField() {
			name := qualifiedRequestField(typ, typ.Field(i).Name)
			_, zero := plainV0DeliberatelyZero[name]
			switch {
			case filled[name] && zero:
				t.Errorf("%s is listed in plainV0DeliberatelyZero but a case fills it", name)
			case !filled[name] && !zero:
				t.Errorf("%s is filled by no plain /v0 write case: add the wire member that feeds it to a case, or list it in plainV0DeliberatelyZero with the reason plain /v0 leaves it zero", name)
			}
		}
	}
	names := make([]string, 0, len(plainV0DeliberatelyZero))
	for name := range plainV0DeliberatelyZero {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if strings.TrimSpace(plainV0DeliberatelyZero[name]) == "" {
			t.Errorf("plainV0DeliberatelyZero lists %s with no reason", name)
		}
	}
}

// qualifiedRequestField spells a field the way plainV0DeliberatelyZero keys it.
func qualifiedRequestField(typ reflect.Type, field string) string {
	return "issueops." + typ.Name() + "." + field
}

// TestRequestFieldsThatDifferNamesAStrayGuard is the comparison's own
// falsification on a fabricated request carrying the two shapes a guard takes,
// a func field and a pointer to a struct: set against nil, each is named;
// nil against nil, neither is.
func TestRequestFieldsThatDifferNamesAStrayGuard(t *testing.T) {
	type rowCheck struct{ Run func() error }
	type request struct {
		Actor string
		InTx  func() error
		Check *rowCheck
	}
	if got := requestFieldsThatDiffer(request{Actor: "a"}, request{Actor: "a"}); len(got) != 0 {
		t.Errorf("equal requests reported %v", got)
	}
	if got := requestFieldsThatDiffer(request{Actor: "a", InTx: func() error { return nil }}, request{Actor: "a"}); len(got) != 1 || got[0] != "InTx" {
		t.Errorf("a set func field reported %v, want [InTx]", got)
	}
	if got := requestFieldsThatDiffer(request{Actor: "a", Check: &rowCheck{}}, request{Actor: "a"}); len(got) != 1 || got[0] != "Check" {
		t.Errorf("a set pointer field reported %v, want [Check]", got)
	}
	if got := requestFieldsThatDiffer(issueops.CloseRequest{}, issueops.ReopenRequest{}); len(got) != 1 || !strings.Contains(got[0], "type") {
		t.Errorf("requests of different types reported %v, want the type named", got)
	}
}
