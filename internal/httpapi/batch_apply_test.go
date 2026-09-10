package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/issueops"
)

// The pins for POST /v0/beads/issues:batchApply. What is asserted here is the
// WIRE EDGE — that a four-level body reaches the role faithfully, that the
// tagged union's two halves are made to agree before the role sees them, that
// the result goes back LEAN, and that each of the role's typed refusals arrives
// as the documented code carrying the members that name the offending item.
// Everything below the wire is the role's.

const batchApplyPath = "/v0/beads/issues:batchApply"

func newApplyBatchServer(t *testing.T, applier *roleBatchApplier) *testServer {
	t.Helper()
	return newTestServer(t, rolesConfig(Config{BatchApplier: applier}))
}

// itemErr wraps a refusal the way the role does, so a case can drive the
// item-naming members without reaching into a backend.
func itemErr(index int, kind issueops.ItemKind, key, issueID string, err error) error {
	return &issueops.ItemError{Index: index, Kind: kind, Key: key, IssueID: issueID, Err: err}
}

// TestApplyBatchPathReachesItsHandler: a literal collection-level custom method
// registered beside the claim's `/v0/beads/issues/{idop}` wildcard. A 404 here
// would mean the segment was parsed as a claim of an issue called ":batchApply".
func TestApplyBatchPathReachesItsHandler(t *testing.T) {
	applier := &roleBatchApplier{result: issueops.ApplyBatchResult{
		Items: []issueops.ItemResult{{Kind: issueops.ItemCreate, IssueID: "bd-1", Changed: true}},
	}}
	ts := newApplyBatchServer(t, applier)

	resp := ts.claim(t, batchApplyPath, `{"actor":"alice","items":[{"kind":"create","create":{"title":"one"}}]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	if len(applier.requests()) != 1 {
		t.Fatalf("the role was called %d times, want 1 — the path reached another handler", len(applier.requests()))
	}
}

// TestApplyBatchForwardsEveryLevelOfTheDocumentedBody is the operation's central
// pin. The body is four levels deep and each level is projected by hand, so this
// drives one item of every kind at once and asserts the whole request the role
// received.
func TestApplyBatchForwardsEveryLevelOfTheDocumentedBody(t *testing.T) {
	applier := &roleBatchApplier{}
	ts := newApplyBatchServer(t, applier)

	resp := ts.claim(t, batchApplyPath, `{
		"actor": "  alice  ",
		"provenance": "planner",
		"force_id_prefix": true,
		"skip_per_edge_cycle_check": true,
		"items": [
			{"kind":"create","create":{
				"key":"root","title":"the plan","description":"body","design":"how",
				"acceptance_criteria":"done when","notes":"scratch","issue_type":"task",
				"status":"open","priority":1,"assignee":"bob","owner":"carol",
				"labels":["api","wire"],"estimated_minutes":30,"external_ref":"gh-9",
				"due_at":"2026-01-02T03:04:05Z","defer_until":"2026-01-01T00:00:00Z",
				"sender":"planner","metadata":{"plan":true},"ephemeral":true,
				"metadata_refs":{"retry_of":{"key":"root"}}}},
			{"kind":"update","update":{
				"target":{"key":"root"},
				"expected_status":"open","expected_assignee":"bob",
				"force_close_policy":true,"force_assignee_transfer":true,
				"patch":{"title":"renamed","status":"closed","assignee":"dave","owner":"erin",
					"labels":{"replace":["a"],"add":["b"],"remove":["c"]},
					"metadata":{"set":{"k":null},"unset":["old"]},
					"estimated_minutes":null}}},
			{"kind":"close","close":{
				"target":{"id":"bd-7"},"reason":"done","session":"s-1","force":true,
				"expected_version":42}},
			{"kind":"dep_add","dep_add":{
				"source":{"key":"root"},"target":{"id":"bd-7"},"type":"waits-for",
				"metadata":{"gate":"any-children"}}}
		]
	}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	reqs := applier.requests()
	if len(reqs) != 1 {
		t.Fatalf("the role was called %d times, want 1", len(reqs))
	}
	got := reqs[0]

	if got.Actor != "alice" {
		t.Errorf("Actor = %q, want the trimmed value", got.Actor)
	}
	if got.Provenance != "planner" {
		t.Errorf("Provenance = %q, want the body's label", got.Provenance)
	}
	if !got.ForceIDPrefix || !got.SkipPerEdgeCycleCheck {
		t.Errorf("ForceIDPrefix/SkipPerEdgeCycleCheck = %v/%v, want both true",
			got.ForceIDPrefix, got.SkipPerEdgeCycleCheck)
	}
	if len(got.Items) != 4 {
		t.Fatalf("Items = %d, want the four the request declared IN ORDER", len(got.Items))
	}
	for i, want := range []issueops.ItemKind{issueops.ItemCreate, issueops.ItemUpdate, issueops.ItemClose, issueops.ItemDepAdd} {
		if got.Items[i].Kind != want {
			t.Fatalf("Items[%d].Kind = %q, want %q: order is the contract and is never changed", i, got.Items[i].Kind, want)
		}
	}

	create := got.Items[0].Create
	if create == nil || create.Issue == nil {
		t.Fatalf("Items[0].Create = %v, want the create payload", create)
	}
	if create.Key != "root" {
		t.Errorf("Create.Key = %q, want the name the item gave itself", create.Key)
	}
	for _, check := range []struct {
		member    string
		got, want string
	}{
		{"title", create.Issue.Title, "the plan"},
		{"description", create.Issue.Description, "body"},
		{"design", create.Issue.Design, "how"},
		{"acceptance_criteria", create.Issue.AcceptanceCriteria, "done when"},
		{"notes", create.Issue.Notes, "scratch"},
		{"issue_type", string(create.Issue.IssueType), "task"},
		{"status", string(create.Issue.Status), "open"},
		{"assignee", create.Issue.Assignee, "bob"},
		{"owner", create.Issue.Owner, "carol"},
		{"sender", create.Issue.Sender, "planner"},
	} {
		if check.got != check.want {
			t.Errorf("create.%s = %q, want %q", check.member, check.got, check.want)
		}
	}
	if create.Issue.Priority != 1 {
		t.Errorf("create.priority = %d, want 1", create.Issue.Priority)
	}
	if create.Issue.EstimatedMinutes == nil || *create.Issue.EstimatedMinutes != 30 {
		t.Errorf("create.estimated_minutes = %v, want 30", create.Issue.EstimatedMinutes)
	}
	if create.Issue.ExternalRef == nil || *create.Issue.ExternalRef != "gh-9" {
		t.Errorf("create.external_ref = %v, want gh-9", create.Issue.ExternalRef)
	}
	if create.Issue.DueAt == nil || create.Issue.DeferUntil == nil {
		t.Errorf("create.due_at/defer_until = %v/%v, want both parsed", create.Issue.DueAt, create.Issue.DeferUntil)
	}
	if !create.Issue.Ephemeral {
		t.Error("create.ephemeral = false; ephemerality is per item and the request asked for it")
	}
	// The metadata document reaches the role as the caller's own BYTES. A
	// handler that decoded and re-marshaled it would renormalize the caller's
	// numbers, which is the hazard the metadata plane's own operation documents.
	if string(create.Issue.Metadata) != `{"plan":true}` {
		t.Errorf("create.metadata = %s, want the caller's bytes unaltered", create.Issue.Metadata)
	}
	if ref, ok := create.MetadataRefs["retry_of"]; !ok || ref.Key != "root" {
		t.Errorf("create.metadata_refs = %v, want the forward/self reference this member alone may carry", create.MetadataRefs)
	}

	update := got.Items[1].Update
	if update == nil {
		t.Fatal("Items[1].Update is nil")
	}
	if update.Target.Key != "root" || update.Target.ID != "" {
		t.Errorf("update.target = %+v, want the backward key ref", update.Target)
	}
	if update.ExpectedStatus == nil || string(*update.ExpectedStatus) != "open" {
		t.Errorf("update.expected_status = %v, want open", update.ExpectedStatus)
	}
	if update.ExpectedAssignee == nil || *update.ExpectedAssignee != "bob" {
		t.Errorf("update.expected_assignee = %v, want bob", update.ExpectedAssignee)
	}
	if !update.ForceClosePolicy || !update.ForceAssigneeTransfer {
		t.Errorf("update force flags = %v/%v, want both true", update.ForceClosePolicy, update.ForceAssigneeTransfer)
	}
	if !update.Patch.Status.Set || string(update.Patch.Status.Value) != "closed" {
		t.Errorf("update.patch.status = %+v, want the crossing status this operation publishes", update.Patch.Status)
	}
	if !update.Patch.Assignee.Set || update.Patch.Assignee.Value != "dave" {
		t.Errorf("update.patch.assignee = %+v, want dave", update.Patch.Assignee)
	}
	if !update.Patch.Owner.Set || update.Patch.Owner.Value != "erin" {
		t.Errorf("update.patch.owner = %+v, want erin", update.Patch.Owner)
	}
	// The FULL label patch: removal has to be expressible, which is the whole
	// reason this member is not the update operation's plain replacement array.
	labels := update.Patch.Labels
	if !labels.Replace.Set || len(labels.Replace.Value) != 1 || len(labels.Add) != 1 || len(labels.Remove) != 1 {
		t.Errorf("update.patch.labels = %+v, want replace, add and remove all carried", labels)
	}
	if got, ok := update.Patch.Metadata.Set["k"]; !ok || string(got) != "null" {
		t.Errorf("update.patch.metadata.set[k] = %s, want the literal null: a present null is a VALUE", got)
	}
	if len(update.Patch.Metadata.Unset) != 1 || update.Patch.Metadata.Unset[0] != "old" {
		t.Errorf("update.patch.metadata.unset = %v, want [old]", update.Patch.Metadata.Unset)
	}
	// An explicit null on a nullable patch member is a CLEAR, not an omission.
	if !update.Patch.EstimatedMinutes.Set || update.Patch.EstimatedMinutes.Value != nil {
		t.Errorf("update.patch.estimated_minutes = %+v, want set with a nil value (a clear)", update.Patch.EstimatedMinutes)
	}

	closeItem := got.Items[2].Close
	if closeItem == nil {
		t.Fatal("Items[2].Close is nil")
	}
	if closeItem.Target.ID != "bd-7" || closeItem.Reason != "done" || closeItem.Session != "s-1" || !closeItem.Force {
		t.Errorf("close = %+v, want the four members the document publishes", closeItem)
	}
	if closeItem.ExpectedVersion == nil || *closeItem.ExpectedVersion != 42 {
		t.Errorf("close.expected_version = %v, want 42", closeItem.ExpectedVersion)
	}

	depAdd := got.Items[3].DepAdd
	if depAdd == nil {
		t.Fatal("Items[3].DepAdd is nil")
	}
	if depAdd.Source.Key != "root" || depAdd.Target.ID != "bd-7" || string(depAdd.Type) != "waits-for" {
		t.Errorf("dep_add = %+v, want the edge the request spelled", depAdd)
	}
	// The gate blob travels as bytes. Normalizing it is the ROLE's, and a
	// handler that parsed it would be a second definition of what a gate is.
	if depAdd.Metadata != `{"gate":"any-children"}` {
		t.Errorf("dep_add.metadata = %q, want the caller's bytes unaltered", depAdd.Metadata)
	}
}

// TestApplyBatchAnswersWithTheLeanResult pins the response side: ids, a
// revision and nothing else. A hydrated issue here would be a response an order
// of magnitude larger than the request, for a snapshot only the library's hooks
// consume — and hooks never fire on this surface.
func TestApplyBatchAnswersWithTheLeanResult(t *testing.T) {
	applier := &roleBatchApplier{result: issueops.ApplyBatchResult{
		Keys: map[string]string{"root": "bd-1"},
		Items: []issueops.ItemResult{
			{Kind: issueops.ItemCreate, IssueID: "bd-1", Changed: true, RowVersion: 77,
				Issue: seededIssue("bd-1", "alice", "open")},
			{Kind: issueops.ItemDepAdd, IssueID: "bd-1", DependsOnID: "bd-7", Changed: false},
		},
	}}
	ts := newApplyBatchServer(t, applier)

	resp := ts.claim(t, batchApplyPath, `{"actor":"alice","items":[{"kind":"create","create":{"title":"one"}}]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	keys, _ := body["keys"].(map[string]any)
	if keys["root"] != "bd-1" {
		t.Errorf("keys = %v, want the one fact the request cannot carry", body["keys"])
	}
	items, _ := body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("items = %v, want one entry per requested item", body["items"])
	}
	first, _ := items[0].(map[string]any)
	if first["kind"] != "create" || first["issue_id"] != "bd-1" || first["changed"] != true {
		t.Errorf("items[0] = %v, want the item's kind, id and changed flag", first)
	}
	if first["revision"] != float64(77) {
		t.Errorf("items[0].revision = %v, want the row's token under the already-committed spelling", first["revision"])
	}
	if _, present := first["issue"]; present {
		t.Error("items[0] carries a hydrated issue; the wire result is lean and the snapshot stops at the library boundary")
	}
	if _, present := first["depends_on_id"]; present {
		t.Error("items[0] carries depends_on_id; it is a dep_add member and its ABSENCE is how a client tells the kinds apart")
	}
	second, _ := items[1].(map[string]any)
	if second["depends_on_id"] != "bd-7" {
		t.Errorf("items[1].depends_on_id = %v, want the edge's target", second["depends_on_id"])
	}
	// 0 is a real value — a legacy row, and every dep_add — so the member is
	// emitted rather than omitted, or a client could not tell the two apart.
	if second["revision"] != float64(0) {
		t.Errorf("items[1].revision = %v, want 0 emitted rather than omitted", second["revision"])
	}
}

// TestApplyBatchAnswersAnEmptyKeysObjectRatherThanNull: `keys` is a required
// member, and a request whose creates named nothing must not answer with the
// `null` a nil Go map marshals to.
func TestApplyBatchAnswersAnEmptyKeysObjectRatherThanNull(t *testing.T) {
	applier := &roleBatchApplier{result: issueops.ApplyBatchResult{
		Items: []issueops.ItemResult{{Kind: issueops.ItemCreate, IssueID: "bd-1", Changed: true}},
	}}
	ts := newApplyBatchServer(t, applier)

	resp := ts.claim(t, batchApplyPath, `{"actor":"alice","items":[{"kind":"create","create":{"title":"one"}}]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	raw := decodeBody(t, resp)
	keys, ok := raw["keys"].(map[string]any)
	if !ok || len(keys) != 0 {
		t.Errorf("keys = %v, want an empty object for a request whose creates named nothing", raw["keys"])
	}
}

// TestApplyBatchRefusesAnUnknownMemberAtEveryLevel is the gate the four-level
// body needs most. Each schema is additionalProperties: false, and a level whose
// unknown member slid through would be undisclosed write surface — so every
// level is driven, and the refusal must name the offender by its qualified path.
func TestApplyBatchRefusesAnUnknownMemberAtEveryLevel(t *testing.T) {
	for _, test := range []struct {
		name      string
		body      string
		wantParam string
	}{
		{
			name:      "request",
			body:      `{"actor":"alice","nope":1,"items":[{"kind":"create","create":{"title":"one"}}]}`,
			wantParam: "nope",
		},
		{
			name:      "item",
			body:      `{"actor":"alice","items":[{"kind":"create","nope":1,"create":{"title":"one"}}]}`,
			wantParam: "items[0].nope",
		},
		{
			name:      "create payload",
			body:      `{"actor":"alice","items":[{"kind":"create","create":{"title":"one","nope":1}}]}`,
			wantParam: "items[0].create.nope",
		},
		{
			name:      "update payload",
			body:      `{"actor":"alice","items":[{"kind":"update","update":{"target":{"id":"bd-1"},"patch":{"title":"x"},"nope":1}}]}`,
			wantParam: "items[0].update.nope",
		},
		{
			name:      "patch",
			body:      `{"actor":"alice","items":[{"kind":"update","update":{"target":{"id":"bd-1"},"patch":{"nope":1}}}]}`,
			wantParam: "items[0].update.patch.nope",
		},
		{
			name:      "label patch",
			body:      `{"actor":"alice","items":[{"kind":"update","update":{"target":{"id":"bd-1"},"patch":{"labels":{"nope":[]}}}}]}`,
			wantParam: "items[0].update.patch.labels.nope",
		},
		{
			name:      "metadata patch",
			body:      `{"actor":"alice","items":[{"kind":"update","update":{"target":{"id":"bd-1"},"patch":{"metadata":{"nope":1}}}}]}`,
			wantParam: "items[0].update.patch.metadata.nope",
		},
		{
			name:      "close payload",
			body:      `{"actor":"alice","items":[{"kind":"close","close":{"target":{"id":"bd-1"},"nope":1}}]}`,
			wantParam: "items[0].close.nope",
		},
		{
			name:      "dep_add payload",
			body:      `{"actor":"alice","items":[{"kind":"dep_add","dep_add":{"source":{"id":"a"},"target":{"id":"b"},"type":"blocks","nope":1}}]}`,
			wantParam: "items[0].dep_add.nope",
		},
		{
			name:      "ref",
			body:      `{"actor":"alice","items":[{"kind":"close","close":{"target":{"id":"bd-1","nope":1}}}]}`,
			wantParam: "items[0].close.target.nope",
		},
		{
			name:      "metadata ref",
			body:      `{"actor":"alice","items":[{"kind":"create","create":{"title":"one","metadata_refs":{"r":{"key":"k","nope":1}}}}]}`,
			wantParam: "items[0].create.metadata_refs.r.nope",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			applier := &roleBatchApplier{}
			ts := newApplyBatchServer(t, applier)

			resp := ts.claim(t, batchApplyPath, test.body)
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
			if len(applier.requests()) != 0 {
				t.Errorf("the role was called for a refused request; nothing may reach it")
			}
		})
	}
}

// TestApplyBatchRefusesATagThatDisagreesWithItsPayload is the case the document
// cannot express and the server therefore owns. The item is a tagged
// single-shape object because this document uses no composition keyword, so
// nothing in a generated type stops a client from sending a kind and another
// kind's payload — or two payloads, or none.
func TestApplyBatchRefusesATagThatDisagreesWithItsPayload(t *testing.T) {
	for _, test := range []struct {
		name      string
		item      string
		wantParam string
	}{
		{
			name:      "kind says create, payload is update",
			item:      `{"kind":"create","update":{"target":{"id":"bd-1"},"patch":{"title":"x"}}}`,
			wantParam: "items[0].update",
		},
		{
			name:      "kind names no payload",
			item:      `{"kind":"create"}`,
			wantParam: "items[0].create",
		},
		{
			// The offender is the payload the kind does NOT name, chosen in a
			// fixed order so `param` never depends on map iteration.
			name:      "two payloads",
			item:      `{"kind":"create","create":{"title":"one"},"close":{"target":{"id":"bd-1"}}}`,
			wantParam: "items[0].close",
		},
		{
			name:      "kind outside the enum",
			item:      `{"kind":"reopen","create":{"title":"one"}}`,
			wantParam: "items[0].kind",
		},
		{
			name:      "no kind at all",
			item:      `{"create":{"title":"one"}}`,
			wantParam: "items[0].kind",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			applier := &roleBatchApplier{}
			ts := newApplyBatchServer(t, applier)

			resp := ts.claim(t, batchApplyPath, `{"actor":"alice","items":[`+test.item+`]}`)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if body["param"] != test.wantParam {
				t.Errorf("param = %v, want %q: a client must never have to read prose to find the offender",
					body["param"], test.wantParam)
			}
			if len(applier.requests()) != 0 {
				t.Error("the role was called for an item whose halves disagree")
			}
		})
	}
}

// TestApplyBatchRejectsTheShapesTheDocumentRefuses pins the rest of the 400s,
// each of them before the role is reached: a request the handler refuses must
// not become a call the role has to refuse again.
func TestApplyBatchRejectsTheShapesTheDocumentRefuses(t *testing.T) {
	item := func(payload string) string {
		return `{"actor":"alice","items":[` + payload + `]}`
	}
	oversize := make([]byte, 0, 300)
	for range 300 {
		oversize = append(oversize, 'x')
	}
	for _, test := range []struct {
		name string
		body string
	}{
		{"missing actor", `{"items":[{"kind":"create","create":{"title":"one"}}]}`},
		{"null actor", `{"actor":null,"items":[{"kind":"create","create":{"title":"one"}}]}`},
		{"blank actor", `{"actor":"   ","items":[{"kind":"create","create":{"title":"one"}}]}`},
		{"missing items", `{"actor":"alice"}`},
		{"empty items", `{"actor":"alice","items":[]}`},
		{"items is not an array", `{"actor":"alice","items":{}}`},
		{"an item is not an object", `{"actor":"alice","items":[3]}`},
		{"non-object body", `[]`},
		{"unparseable body", `{`},
		{"create with no title", item(`{"kind":"create","create":{}}`)},
		{"create with a blank title", item(`{"kind":"create","create":{"title":"  "}}`)},
		{"create with an oversize title", item(`{"kind":"create","create":{"title":"` + string(oversize) + `"}}`)},
		{"create with a control character in the assignee", item(`{"kind":"create","create":{"title":"t","assignee":"a\u0007b"}}`)},
		{"create priority out of range", item(`{"kind":"create","create":{"title":"t","priority":9}}`)},
		{"create priority of the wrong type", item(`{"kind":"create","create":{"title":"t","priority":"high"}}`)},
		{"create on both planes", item(`{"kind":"create","create":{"title":"t","ephemeral":true,"no_history":true}}`)},
		{"update with no target", item(`{"kind":"update","update":{"patch":{"title":"x"}}}`)},
		{"update with an empty patch", item(`{"kind":"update","update":{"target":{"id":"bd-1"},"patch":{}}}`)},
		{"update with no patch", item(`{"kind":"update","update":{"target":{"id":"bd-1"}}}`)},
		{"a null on a non-nullable patch member", item(`{"kind":"update","update":{"target":{"id":"bd-1"},"patch":{"title":null}}}`)},
		{"notes and append_notes together", item(`{"kind":"update","update":{"target":{"id":"bd-1"},"patch":{"notes":"a","append_notes":"b"}}}`)},
		{"metadata replace beside a merge", item(`{"kind":"update","update":{"target":{"id":"bd-1"},"patch":{"metadata":{"replace":{},"merge":{"a":1}}}}}`)},
		{"a ref naming both a key and an id", item(`{"kind":"close","close":{"target":{"key":"k","id":"bd-1"}}}`)},
		{"a ref naming neither", item(`{"kind":"close","close":{"target":{}}}`)},
		{"a ref that is not an object", item(`{"kind":"close","close":{"target":"bd-1"}}`)},
		{"dep_add with no type", item(`{"kind":"dep_add","dep_add":{"source":{"id":"a"},"target":{"id":"b"}}}`)},
		{"dep_add with an empty type", item(`{"kind":"dep_add","dep_add":{"source":{"id":"a"},"target":{"id":"b"},"type":""}}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			applier := &roleBatchApplier{}
			ts := newApplyBatchServer(t, applier)

			resp := ts.claim(t, batchApplyPath, test.body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
			}
			if len(applier.requests()) != 0 {
				t.Errorf("the role was called %d times for a refused request, want 0", len(applier.requests()))
			}
		})
	}
}

// TestApplyBatchRefusesMoreItemsThanTheRoleAccepts pins the cap at the edge, so
// an over-long plan costs no database work. The number is the role's own
// constant rather than a second copy of it.
func TestApplyBatchRefusesMoreItemsThanTheRoleAccepts(t *testing.T) {
	applier := &roleBatchApplier{}
	ts := newApplyBatchServer(t, applier)

	body := `{"actor":"alice","items":[`
	for i := range issueops.MaxApplyBatchItems + 1 {
		if i > 0 {
			body += ","
		}
		body += fmt.Sprintf(`{"kind":"create","create":{"title":"i%d"}}`, i)
	}
	body += `]}`

	resp := ts.claim(t, batchApplyPath, body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	if decodeBody(t, resp)["param"] != "items" {
		t.Error("the refusal does not name `items`")
	}
	if len(applier.requests()) != 0 {
		t.Error("an over-long plan reached the role")
	}
}

// TestApplyBatchRefusesAQueryParameterAndAForeignMediaType keeps this operation
// on the surface's two shared refusals rather than letting a new handler quietly
// opt out of them.
func TestApplyBatchRefusesAQueryParameterAndAForeignMediaType(t *testing.T) {
	applier := &roleBatchApplier{}
	ts := newApplyBatchServer(t, applier)

	body := `{"actor":"alice","items":[{"kind":"create","create":{"title":"one"}}]}`
	if resp := ts.claim(t, batchApplyPath+"?verbose=1", body); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status with a query parameter = %d, want 400", resp.StatusCode)
	}
	if resp := ts.postBody(t, batchApplyPath, "text/plain", body); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status with a foreign media type = %d, want 400", resp.StatusCode)
	}
	if len(applier.requests()) != 0 {
		t.Errorf("the role was called %d times for refused requests, want 0", len(applier.requests()))
	}
}

// TestApplyBatchReportsAForwardKeyReferenceAsAnOrderingMistake is the pin on
// the one 400 that carries a discriminator. A key declared LATER is an ORDERING
// mistake and a key nothing declares is a typo, and a client that could not tell
// them apart would go hunting for a misspelling in a request that is spelled
// correctly and ordered wrongly.
func TestApplyBatchReportsAForwardKeyReferenceAsAnOrderingMistake(t *testing.T) {
	for _, test := range []struct {
		name              string
		err               error
		wantDeclaredLater bool
		wantParam         string
	}{
		{
			name:              "declared later",
			err:               &issueops.RefError{Index: 1, Member: "target", Key: "late", DeclaredLater: true},
			wantDeclaredLater: true,
			wantParam:         "items[1].update.target",
		},
		{
			name:              "declared nowhere",
			err:               &issueops.RefError{Index: 1, Member: "target", Key: "typo"},
			wantDeclaredLater: false,
			wantParam:         "items[1].update.target",
		},
		{
			name:              "a metadata ref names the free-form member",
			err:               &issueops.RefError{Index: 0, Member: "metadata_ref retry_of", Key: "typo"},
			wantDeclaredLater: false,
			wantParam:         "items[0].create.metadata_refs",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			applier := &roleBatchApplier{err: test.err}
			ts := newApplyBatchServer(t, applier)

			resp := ts.claim(t, batchApplyPath, `{"actor":"alice","items":[
				{"kind":"create","create":{"title":"one","metadata_refs":{"retry_of":{"key":"typo"}}}},
				{"kind":"update","update":{"target":{"key":"late"},"patch":{"title":"x"}}}
			]}`)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if body["code"] != "invalid_argument" {
				t.Errorf("code = %v, want invalid_argument", body["code"])
			}
			declaredLater, present := body["declared_later"]
			if !present {
				t.Fatal("declared_later is absent; both polarities are emitted, so a client can never read absence as false")
			}
			if declaredLater != test.wantDeclaredLater {
				t.Errorf("declared_later = %v, want %v", declaredLater, test.wantDeclaredLater)
			}
			if body["param"] != test.wantParam {
				t.Errorf("param = %v, want %q", body["param"], test.wantParam)
			}
			// RefError.Member is diagnostic prose, so it is mapped onto the
			// document's member names and never published as a value.
			if _, present := body["member"]; present {
				t.Error("the problem carries a `member`; the role's free-form member name is not wire vocabulary")
			}
		})
	}
}

// TestApplyBatchReportsAPreconditionMissAsAConflict is the operation's posture
// pin, and the difference from the metadata compare-and-set stated as a test: a
// guard that missed took the WHOLE request down, so it belongs in the error
// channel carrying the members that name the item.
func TestApplyBatchReportsAPreconditionMissAsAConflict(t *testing.T) {
	for _, test := range []struct {
		name      string
		err       error
		wantParam string
		assert    func(t *testing.T, body map[string]any)
	}{
		{
			name:      "version",
			err:       itemErr(1, issueops.ItemUpdate, "root", "bd-1", issueops.ErrVersionMismatch),
			wantParam: "items[1].update.expected_version",
			assert: func(t *testing.T, body map[string]any) {
				if body["expected_version"] != float64(42) {
					t.Errorf("expected_version = %v, want the guard the request sent", body["expected_version"])
				}
			},
		},
		{
			name:      "status",
			err:       itemErr(1, issueops.ItemUpdate, "root", "bd-1", issueops.ErrStatusMismatch),
			wantParam: "items[1].update.expected_status",
			assert: func(t *testing.T, body map[string]any) {
				if body["expected_status"] != "open" {
					t.Errorf("expected_status = %v, want the guard the request sent", body["expected_status"])
				}
			},
		},
		{
			name:      "assignee",
			err:       itemErr(1, issueops.ItemUpdate, "root", "bd-1", issueops.ErrAssigneeMismatch),
			wantParam: "items[1].update.expected_assignee",
			assert: func(t *testing.T, body map[string]any) {
				if body["expected_assignee"] != "bob" {
					t.Errorf("expected_assignee = %v, want the guard the request sent", body["expected_assignee"])
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			applier := &roleBatchApplier{err: test.err}
			ts := newApplyBatchServer(t, applier)

			resp := ts.claim(t, batchApplyPath, `{"actor":"alice","items":[
				{"kind":"create","create":{"key":"root","title":"one"}},
				{"kind":"update","update":{"target":{"id":"bd-1"},
					"expected_version":42,"expected_status":"open","expected_assignee":"bob",
					"patch":{"title":"x"}}}
			]}`)
			if resp.StatusCode != http.StatusConflict {
				t.Fatalf("status = %d, want 409: a miss refuses the whole request, so it is not an answer here: %s",
					resp.StatusCode, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if body["code"] != "precondition_failed" {
				t.Fatalf("code = %v, want precondition_failed", body["code"])
			}
			if body["item_index"] != float64(1) || body["item_kind"] != "update" {
				t.Errorf("item_index/item_kind = %v/%v, want 1/update: the only place the offender exists",
					body["item_index"], body["item_kind"])
			}
			if body["item_key"] != "root" {
				t.Errorf("item_key = %v, want the key the refused item named", body["item_key"])
			}
			// item_issue_id, NOT issue_id: that name is already the hierarchy
			// refusal's presence discriminator.
			if body["item_issue_id"] != "bd-1" {
				t.Errorf("item_issue_id = %v, want the resolved id", body["item_issue_id"])
			}
			if _, present := body["issue_id"]; present {
				t.Error("the problem carries `issue_id`; that member discriminates the dependency_cycle hierarchy refusal and must not fire here")
			}
			if body["param"] != test.wantParam {
				t.Errorf("param = %v, want %q", body["param"], test.wantParam)
			}
			// The refusal rolled its transaction back, so there is no observed
			// value this operation could honestly report.
			for _, absent := range []string{"actual_version", "actual_status", "actual_assignee"} {
				if _, present := body[absent]; present {
					t.Errorf("the problem carries %s; the refusal saw a row that no longer exists to read", absent)
				}
			}
			test.assert(t, body)
		})
	}
}

// TestApplyBatchMapsTheRolesTypedRefusalsOntoTheDocumentedCodes walks the rest
// of the refusal table. Every branch reads a TYPED error rather than prose,
// which is what lets a client stop substring-matching messages.
func TestApplyBatchMapsTheRolesTypedRefusalsOntoTheDocumentedCodes(t *testing.T) {
	for _, test := range []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
		wantParam  string
	}{
		{
			name:       "a scheduling cycle",
			err:        itemErr(0, issueops.ItemDepAdd, "", "bd-1", issueops.ErrDependencyCycle),
			wantStatus: http.StatusConflict,
			wantCode:   "dependency_cycle",
		},
		{
			name: "a hierarchy conflict",
			err: itemErr(0, issueops.ItemDepAdd, "", "bd-1",
				&issueops.DependencyHierarchyConflictError{IssueID: "bd-1", BlockerID: "bd-2", BlockerIsAncestor: true}),
			wantStatus: http.StatusConflict,
			wantCode:   "dependency_cycle",
		},
		{
			name: "a type conflict",
			err: itemErr(0, issueops.ItemDepAdd, "", "bd-1",
				&issueops.DependencyTypeConflictError{ExistingType: "blocks", RequestedType: "parent-child"}),
			wantStatus: http.StatusConflict,
			wantCode:   "dependency_exists",
		},
		{
			name:       "close policy",
			err:        itemErr(0, issueops.ItemClose, "", "bd-1", &issueops.CloseOpenChildrenError{IssueID: "bd-1", OpenChildren: 3}),
			wantStatus: http.StatusConflict,
			wantCode:   "not_closable",
		},
		{
			name:       "the assignee fence",
			err:        itemErr(0, issueops.ItemUpdate, "", "bd-1", storage.ErrAlreadyClaimed),
			wantStatus: http.StatusConflict,
			wantCode:   "already_claimed",
			wantParam:  "items[0].update.assignee",
		},
		{
			name: "a ghost edge source",
			err: itemErr(0, issueops.ItemDepAdd, "", "", &issueops.DependencyEndpointNotFoundError{
				IssueID: "bd-1", DependsOnID: "bd-2", MissingID: "bd-1", Err: issueops.ErrDependencySourceNotFound,
			}),
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_argument",
			wantParam:  "items[0].dep_add.source",
		},
		{
			name: "a ghost edge target",
			err: itemErr(0, issueops.ItemDepAdd, "", "", &issueops.DependencyEndpointNotFoundError{
				IssueID: "bd-1", DependsOnID: "bd-2", MissingID: "bd-2", Err: issueops.ErrDependencyTargetNotFound,
			}),
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_argument",
			wantParam:  "items[0].dep_add.target",
		},
		{
			// A 409, not a 400: the body is well-formed and stays well-formed —
			// the identical request succeeded before the id was taken — so what
			// refuses it is state the client has to READ, not a request it has
			// to fix. `param` and the item members still name where.
			name:       "an occupied explicit id",
			err:        itemErr(0, issueops.ItemCreate, "root", "", storage.ErrAlreadyExists),
			wantStatus: http.StatusConflict,
			wantCode:   "already_exists",
			wantParam:  "items[0].create.id",
		},
		{
			name:       "a target that names nothing",
			err:        itemErr(0, issueops.ItemUpdate, "", "bd-9", storage.ErrNotFound),
			wantStatus: http.StatusNotFound,
			wantCode:   "not_found",
		},
		{
			name:       "the workspace's own validation",
			err:        itemErr(0, issueops.ItemCreate, "", "", storage.ErrValidation),
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_argument",
		},
		{
			name:       "something the mapping does not know",
			err:        errUnmapped,
			wantStatus: http.StatusInternalServerError,
			wantCode:   "internal",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			applier := &roleBatchApplier{err: test.err}
			ts := newApplyBatchServer(t, applier)

			resp := ts.claim(t, batchApplyPath, `{"actor":"alice","items":[{"kind":"create","create":{"title":"one"}}]}`)
			if resp.StatusCode != test.wantStatus {
				t.Fatalf("status = %d, want %d: %s", resp.StatusCode, test.wantStatus, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if body["code"] != test.wantCode {
				t.Errorf("code = %v, want %q", body["code"], test.wantCode)
			}
			if test.wantParam != "" && body["param"] != test.wantParam {
				t.Errorf("param = %v, want %q", body["param"], test.wantParam)
			}
			if test.wantStatus < 500 && test.wantCode != "internal" {
				if body["item_index"] != float64(0) {
					t.Errorf("item_index = %v, want 0: every refusal that names an item must carry it", body["item_index"])
				}
			}
		})
	}
}

// errUnmapped stands in for a refusal the mapping does not know, so the default
// branch is driven rather than assumed.
var errUnmapped = fmt.Errorf("a failure this mapping has never seen")

// TestApplyBatchCarriesTheHierarchyMembersOnlyOnTheHierarchyRefusal pins the
// discriminator inside `dependency_cycle`: member PRESENCE tells a plain
// scheduling cycle from the hierarchy case, exactly as it does on
// POST /v0/beads/dependencies:add.
func TestApplyBatchCarriesTheHierarchyMembersOnlyOnTheHierarchyRefusal(t *testing.T) {
	hierarchy := &roleBatchApplier{err: itemErr(0, issueops.ItemDepAdd, "", "bd-1",
		&issueops.DependencyHierarchyConflictError{IssueID: "bd-1", BlockerID: "bd-2", BlockerIsAncestor: false})}
	plain := &roleBatchApplier{err: itemErr(0, issueops.ItemDepAdd, "", "bd-1", issueops.ErrDependencyCycle)}
	body := `{"actor":"alice","items":[{"kind":"create","create":{"title":"one"}}]}`

	hierarchyBody := decodeBody(t, newApplyBatchServer(t, hierarchy).claim(t, batchApplyPath, body))
	if hierarchyBody["issue_id"] != "bd-1" || hierarchyBody["blocker_id"] != "bd-2" {
		t.Errorf("hierarchy refusal = %v, want issue_id and blocker_id from the refusing transaction", hierarchyBody)
	}
	if hierarchyBody["blocker_is_ancestor"] != false {
		t.Error("blocker_is_ancestor is not emitted when false; both polarities are reported")
	}

	plainBody := decodeBody(t, newApplyBatchServer(t, plain).claim(t, batchApplyPath, body))
	for _, member := range []string{"issue_id", "blocker_id", "blocker_is_ancestor"} {
		if _, present := plainBody[member]; present {
			t.Errorf("the plain cycle refusal carries %s; their ABSENCE is what tells the two refusals apart", member)
		}
	}
}

// TestApplyBatchMetadataMembersDecodeAPresentNull is the pin on the GENERATED
// CLIENT, and it is the only test here that decodes rather than asserting on
// wire bytes — because the bug it exists to prevent is invisible at the wire.
//
// A metadata member declared as *json.RawMessage cannot READ a present null:
// encoding/json answers a JSON null against a pointer by setting the pointer to
// nil, before any UnmarshalJSON runs, so a member holding `null` and a member
// that was never sent decode to the same value. On the metadata plane those
// mean opposite things — the key holds null, versus the key is absent.
//
// The schema's x-go-type-skip-optional-pointer is what makes the member a bare
// json.RawMessage, which is an Unmarshaler and receives the literal. This test
// is what keeps the next `make api-gen` from regenerating that away.
func TestApplyBatchMetadataMembersDecodeAPresentNull(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		want *string
	}{
		{"a present null is the literal", `{"replace":null}`, applyStrPtr("null")},
		{"an omitted member is nil", `{}`, nil},
		{"an ordinary value round-trips", `{"replace":{"a":1}}`, applyStrPtr(`{"a":1}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			var patch apigen.ApplyMetadataPatch
			if err := json.Unmarshal([]byte(test.body), &patch); err != nil {
				t.Fatalf("decoding %s: %v", test.body, err)
			}
			switch {
			case test.want == nil && patch.Replace != nil:
				t.Fatalf("Replace = %s, want nil: an omitted member never asked for a write", string(patch.Replace))
			case test.want != nil && patch.Replace == nil:
				t.Fatalf("Replace = nil, want %s: a present null is a VALUE, and a client that cannot tell "+
					"it from an absent member cannot express a null-valued document", *test.want)
			case test.want != nil && string(patch.Replace) != *test.want:
				t.Fatalf("Replace = %s, want %s", string(patch.Replace), *test.want)
			}
		})
	}

	// The create item's own metadata member carries the same shape for the same
	// reason, and a pointer there would silently drop a caller's `null`.
	var create apigen.ApplyCreateItem
	if err := json.Unmarshal([]byte(`{"title":"t","metadata":null}`), &create); err != nil {
		t.Fatalf("decoding a create item: %v", err)
	}
	if string(create.Metadata) != "null" {
		t.Fatalf("ApplyCreateItem.Metadata = %s, want the literal null", string(create.Metadata))
	}
}

func applyStrPtr(s string) *string { return &s }
