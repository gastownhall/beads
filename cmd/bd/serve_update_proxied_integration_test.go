//go:build cgo

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// End-to-end for the field update, against real Dolt through a real `bd serve`
// subprocess. The pure tests in internal/httpapi cover the wire edge on a fake
// role and assert the projection onto issueops.IssuePatch field by field; what
// only this level can prove is that the projection LANDS — that a set member, a
// null-cleared member and an untouched member all end up in the row the way the
// document says, and that a same-value resend really is a no-op against a store
// rather than against a fake's bookkeeping.

// updateIssueRaw patches id and returns the status and the UNDECODED body, so a
// caller can choose how to read a member rather than inheriting
// encoding/json's default for `any`.
func (sp *serveProcess) updateIssueRaw(t *testing.T, id, body string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPatch, sp.url("/v0/beads/issues/"+id), strings.NewReader(body))
	if err != nil {
		t.Fatalf("new update request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := sp.client.Do(req)
	if err != nil {
		t.Fatalf("PATCH %s: %v\nstderr:\n%s", id, err, sp.stderr.String())
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read update body: %v", err)
	}
	return resp.StatusCode, raw
}

// updateIssue patches id and returns the status and decoded body.
func (sp *serveProcess) updateIssue(t *testing.T, id, body string) (int, map[string]any) {
	t.Helper()
	status, raw := sp.updateIssueRaw(t, id, body)
	var m map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("decode update body %q: %v", raw, err)
		}
	}
	return status, m
}

// revisionOf decodes the `revision` token as the 64-BIT INTEGER the document
// declares it to be, which is the whole reason this reads the raw bytes.
//
// Decoding it into an `any` yields a float64, and live tokens run past 5e17
// where a float64's ulp is already 64 — so the value that comes back is NEAR
// the token and is not it, and a guard composed from it is refused against a
// row nothing else touched. That is not hypothetical: it is how the first
// version of the case below failed.
//
// NEVER ORDER IT AND NEVER COMPUTE ONE. The token is opaque and compared for
// EQUALITY alone, so "the previous revision" is not `revision - 1`; it is the
// value an earlier write answered with, which is what the case below uses.
func revisionOf(t *testing.T, raw []byte) int64 {
	t.Helper()
	var body struct {
		Revision *int64 `json:"revision"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode revision from %q: %v", raw, err)
	}
	if body.Revision == nil {
		t.Fatalf("the response carries no `revision`: %s", raw)
	}
	return *body.Revision
}

func TestProxiedServerServeUpdate(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "srvupd")
	sp := startServe(t, bd, p.dir, bdProxiedEnv(p.dir))

	// THE THREE-WAY PROOF the spec names: one issue carrying a SET member, a
	// NULL-CLEARED member and an UNTOUCHED member, read back after the PATCH.
	// A fake role can report what it was handed; only a real row can show that
	// "absent means untouched" is a property of the write rather than of the
	// handler's bookkeeping.
	t.Run("a set member, a cleared member and an untouched member", func(t *testing.T) {
		issue := bdProxiedCreate(t, bd, p.dir, "before", "-p", "3",
			"-d", "the original description", "--acceptance", "the original criteria")

		// Give it something to clear and something to leave alone.
		if _, err := bdProxiedRun(t, bd, p.dir, "update", issue.ID, "--external-ref", "JIRA-1"); err != nil {
			t.Fatalf("bd update --external-ref: %v", err)
		}
		seeded := bdProxiedShow(t, bd, p.dir, issue.ID)
		if seeded.ExternalRef == nil || *seeded.ExternalRef != "JIRA-1" {
			t.Fatalf("external_ref did not seed: %v", seeded.ExternalRef)
		}

		status, body := sp.updateIssue(t, issue.ID, `{"actor":"http-agent","patch":{
			"title":"after",
			"priority":1,
			"external_ref":null
		}}`)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200: %v", status, body)
		}
		if body["changed"] != true {
			t.Errorf("changed = %v, want true", body["changed"])
		}

		after := bdProxiedShow(t, bd, p.dir, issue.ID)
		// SET: the two members the patch carried.
		if after.Title != "after" {
			t.Errorf("title = %q, want the patched value", after.Title)
		}
		if after.Priority != 1 {
			t.Errorf("priority = %d, want the patched 1", after.Priority)
		}
		// NULL-CLEARED: the member the patch sent as null.
		if after.ExternalRef != nil && *after.ExternalRef != "" {
			t.Errorf("external_ref = %q, want it cleared by the explicit null", *after.ExternalRef)
		}
		// UNTOUCHED: the members the patch never mentioned. This is the half a
		// handler that filled the whole struct from a decoded body would blank.
		if after.Description != "the original description" {
			t.Errorf("description = %q; an absent member must leave the field untouched", after.Description)
		}
		if after.AcceptanceCriteria != "the original criteria" {
			t.Errorf("acceptance_criteria = %q; an absent member must leave the field untouched", after.AcceptanceCriteria)
		}
		if string(after.Status) != "open" {
			t.Errorf("status = %q; this operation cannot edit status at all", after.Status)
		}
	})

	// The idempotency half: a same-value resend is a 200 with changed:false, and
	// the row is unchanged.
	t.Run("a same-value resend reports changed false", func(t *testing.T) {
		issue := bdProxiedCreate(t, bd, p.dir, "idempotent", "-p", "2")

		status, body := sp.updateIssue(t, issue.ID, `{"actor":"http-agent","patch":{"title":"renamed"}}`)
		if status != http.StatusOK {
			t.Fatalf("first update: status = %d, want 200: %v", status, body)
		}
		if body["changed"] != true {
			t.Errorf("changed = %v, want true on the first write", body["changed"])
		}

		status, body = sp.updateIssue(t, issue.ID, `{"actor":"http-agent","patch":{"title":"renamed"}}`)
		if status != http.StatusOK {
			t.Fatalf("resend: status = %d, want 200: %v", status, body)
		}
		if body["changed"] != false {
			t.Errorf("changed = %v, want false for a same-value patch", body["changed"])
		}
		if shown := bdProxiedShow(t, bd, p.dir, issue.ID); shown.Title != "renamed" {
			t.Errorf("title = %q after the resend", shown.Title)
		}
	})

	// Labels are COMPLETE REPLACEMENT, which is the one member whose semantics a
	// client cannot infer from the field name.
	t.Run("labels are replaced wholesale", func(t *testing.T) {
		issue := bdProxiedCreate(t, bd, p.dir, "labelled", "-p", "2", "--label", "keep", "--label", "drop")

		status, body := sp.updateIssue(t, issue.ID, `{"actor":"http-agent","patch":{"labels":["keep","added"]}}`)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200: %v", status, body)
		}
		after := bdProxiedShow(t, bd, p.dir, issue.ID)
		got := map[string]bool{}
		for _, l := range after.Labels {
			got[l] = true
		}
		if !got["keep"] || !got["added"] {
			t.Errorf("labels = %v, want the replacement set", after.Labels)
		}
		if got["drop"] {
			t.Errorf("labels = %v; replacement must remove a label the request omitted", after.Labels)
		}
	})

	// THE CASE THE INCREMENTAL MEMBERS EXIST FOR, and the one a fake cannot
	// state: two writers each adding a label to one row, neither of which knows
	// what the other added. Composed out of replacements — which is all this
	// operation published before — the second write silently drops the first
	// writer's label, because a replacement can only be composed safely by a
	// writer that knows it is alone.
	t.Run("concurrent additions do not drop each other's labels", func(t *testing.T) {
		issue := bdProxiedCreate(t, bd, p.dir, "incrementally labelled", "-p", "2", "--label", "original")

		// Each writer read the row BEFORE the other wrote, so each knows only
		// {original} plus its own label. Under a replacement that is a lost
		// update; under an addition it is not.
		for _, label := range []string{"from-a", "from-b"} {
			status, body := sp.updateIssue(t, issue.ID,
				`{"actor":"http-agent","patch":{"add_labels":["`+label+`"]}}`)
			if status != http.StatusOK {
				t.Fatalf("add %s: status = %d, want 200: %v", label, status, body)
			}
		}

		after := bdProxiedShow(t, bd, p.dir, issue.ID)
		got := map[string]bool{}
		for _, l := range after.Labels {
			got[l] = true
		}
		for _, want := range []string{"original", "from-a", "from-b"} {
			if !got[want] {
				t.Errorf("labels = %v, want %q kept; an addition must not rewrite the set", after.Labels, want)
			}
		}

		// And the removal half, applied to a row neither writer fully knows.
		status, body := sp.updateIssue(t, issue.ID,
			`{"actor":"http-agent","patch":{"remove_labels":["from-a"]}}`)
		if status != http.StatusOK {
			t.Fatalf("remove: status = %d, want 200: %v", status, body)
		}
		after = bdProxiedShow(t, bd, p.dir, issue.ID)
		got = map[string]bool{}
		for _, l := range after.Labels {
			got[l] = true
		}
		if got["from-a"] {
			t.Errorf("labels = %v; the removal did not land", after.Labels)
		}
		if !got["original"] || !got["from-b"] {
			t.Errorf("labels = %v; a removal must touch only the labels it names", after.Labels)
		}

		// Removing a label the row does not carry is a no-op, not a refusal.
		status, body = sp.updateIssue(t, issue.ID,
			`{"actor":"http-agent","patch":{"remove_labels":["never-there"]}}`)
		if status != http.StatusOK {
			t.Fatalf("removing an absent label: status = %d, want 200: %v", status, body)
		}
		if body["changed"] != false {
			t.Errorf("changed = %v, want false; removing a label the row does not carry writes nothing", body["changed"])
		}

		// All three in one request, in the role's order: replace, then add,
		// then REMOVE WINS.
		status, body = sp.updateIssue(t, issue.ID,
			`{"actor":"http-agent","patch":{"labels":["base"],"add_labels":["extra","doomed"],"remove_labels":["doomed"]}}`)
		if status != http.StatusOK {
			t.Fatalf("ordered edit: status = %d, want 200: %v", status, body)
		}
		after = bdProxiedShow(t, bd, p.dir, issue.ID)
		got = map[string]bool{}
		for _, l := range after.Labels {
			got[l] = true
		}
		if !got["base"] || !got["extra"] {
			t.Errorf("labels = %v, want the replacement plus the addition", after.Labels)
		}
		if got["doomed"] {
			t.Errorf("labels = %v; removal must win over an addition of the same label", after.Labels)
		}
		if got["original"] || got["from-b"] {
			t.Errorf("labels = %v; the replacement must still clear what it omitted", after.Labels)
		}
	})

	// The rule-8 oracle against `bd update --json`[0], the claim's device: both
	// surfaces marshal the same canonical struct, so the allowlist is empty.
	t.Run("the updated issue matches bd update --json element 0", func(t *testing.T) {
		issue := bdProxiedCreate(t, bd, p.dir, "parity oracle", "-p", "1",
			"-d", "described", "--acceptance", "accepted", "--label", "oracle")

		status, body := sp.updateIssue(t, issue.ID, `{"actor":"parity-agent","patch":{"title":"patched by http"}}`)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200: %v", status, body)
		}
		fromHTTP, ok := body["issue"].(map[string]any)
		if !ok {
			t.Fatalf("issue = %#v, want an object", body["issue"])
		}

		// A same-value update through the CLI writes nothing and reports the
		// same row, so both sides describe one post-state snapshot.
		fromCLI := bdProxiedUpdateOneRaw(t, bd, p.dir, issue.ID, "--title", "patched by http")
		if diff := diffJSONObjects(fromHTTP, fromCLI, nil); diff != "" {
			t.Errorf("UpdateIssueResponse.issue and `bd update --json`[0] disagree:\n%s", diff)
		}
	})

	t.Run("unknown ids are 404 and refused bodies write nothing", func(t *testing.T) {
		status, body := sp.updateIssue(t, "bd-nosuchissue", `{"actor":"http-agent","patch":{"title":"t"}}`)
		if status != http.StatusNotFound {
			t.Fatalf("unknown id: status = %d, want 404: %v", status, body)
		}
		if body["code"] != "not_found" {
			t.Errorf("code = %v, want not_found", body["code"])
		}

		issue := bdProxiedCreate(t, bd, p.dir, "never patched", "-p", "2", "-d", "untouched")
		// `status` and `assignee` used to be here, refused by name. They are
		// published now, so what stands in their place is the refusal each of
		// the new members BROUGHT: a self-parent, and the two spellings of the
		// assignee guards that contradict each other.
		for _, refused := range []string{
			`{"actor":"   ","patch":{"title":"t"}}`,
			`{"actor":"agent","patch":{}}`,
			`{"actor":"agent","patch":{"parent_id":"` + issue.ID + `"}}`,
			`{"actor":"agent","patch":{"metadata":{"replace":{},"merge":{"a":1}}}}`,
			`{"actor":"agent","patch":{"title":"t"},"force_assignee_transfer":true}`,
			`{"actor":"agent","patch":{"assignee":"x"},"force_assignee_transfer":true,"expected_assignee":"y"}`,
			`{"actor":"agent","patch":{"title":null}}`,
			`{"actor":"agent","patch":{"priority":9}}`,
			`{"actor":"agent","patch":{"notes":"a","append_notes":"b"}}`,
		} {
			status, problem := sp.updateIssue(t, issue.ID, refused)
			if status != http.StatusBadRequest {
				t.Fatalf("body %.50q: status = %d, want 400: %v", refused, status, problem)
			}
			if problem["code"] != "invalid_argument" {
				t.Errorf("body %.50q: code = %v, want invalid_argument", refused, problem["code"])
			}
		}

		// The refusals are wire-edge refusals: nothing reached the database.
		after := bdProxiedShow(t, bd, p.dir, issue.ID)
		if after.Title != "never patched" || after.Description != "untouched" ||
			string(after.Status) != "open" || after.Assignee != "" {
			t.Errorf("a refused update wrote to the row: %+v", after)
		}
	})

	// THE MEMBERS THAT CARRY POLICY, against a real row. The pure tests assert
	// the projection onto issueops.UpdateRequest; what only this level can show
	// is that a status, an assignee and a metadata edit sent in ONE patch all
	// land in one transaction — the capability two calls cannot give a caller —
	// and that the guard token this write mints really guards the next one.
	//
	// `parent_id` is deliberately NOT read back here. `bd show --json` answers a
	// detail view whose `dependencies` are IssueWithDependencyMetadata rather
	// than the edge rows types.Issue carries, so a read-back through this helper
	// could not fail on a reparent that never landed. Its projection is pinned
	// in the pure tests, its refusals in TestUpdateAnswersThePolicyRefusalsIts
	// MembersEarned, and its one edge-decided-here rule — a self-parent — in the
	// refused-bodies table above.
	t.Run("status, assignee and metadata land together", func(t *testing.T) {
		issue := bdProxiedCreate(t, bd, p.dir, "the child", "-p", "2")

		status, raw := sp.updateIssueRaw(t, issue.ID, `{"actor":"http-agent","patch":{
			"status":"in_progress",
			"assignee":"http-agent",
			"metadata":{"set":{"lane":"wire"}}
		}}`)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", status, raw)
		}
		var landed struct {
			Changed bool `json:"changed"`
		}
		if err := json.Unmarshal(raw, &landed); err != nil {
			t.Fatalf("decode the update body %q: %v", raw, err)
		}
		if !landed.Changed {
			t.Errorf("changed = false, want true: %s", raw)
		}
		// The guard's token, minted by this write. A caller cannot compose a
		// guarded follow-up without it, which is why the member exists.
		first := revisionOf(t, raw)

		after := bdProxiedShow(t, bd, p.dir, issue.ID)
		if string(after.Status) != "in_progress" || after.Assignee != "http-agent" {
			t.Errorf("status/assignee did not land: %q/%q", after.Status, after.Assignee)
		}
		if !strings.Contains(string(after.Metadata), `"lane"`) {
			t.Errorf("metadata did not land: %s", after.Metadata)
		}

		// THE READ-MODIFY-WRITE LOOP, with a real concurrent writer rather than
		// an arithmetic one. A second unguarded write moves the row, so the
		// token the first write answered with is now genuinely stale — which is
		// the only way to make one, since the token is opaque and compared for
		// equality alone.
		concurrentStatus, concurrent := sp.updateIssueRaw(t, issue.ID,
			`{"actor":"other-agent","patch":{"notes":"a concurrent edit"}}`)
		if concurrentStatus != http.StatusOK {
			t.Fatalf("the concurrent write: status = %d, want 200: %s", concurrentStatus, concurrent)
		}
		second := revisionOf(t, concurrent)
		if second == first {
			t.Fatalf("revision = %d after a second write; a write that does not move the token makes every guard vacuous", second)
		}

		conflictStatus, problem := sp.updateIssue(t, issue.ID,
			`{"actor":"http-agent","expected_version":`+strconv.FormatInt(first, 10)+`,"patch":{"title":"never"}}`)
		if conflictStatus != http.StatusConflict {
			t.Fatalf("stale guard: status = %d, want 409: %v", conflictStatus, problem)
		}
		if problem["code"] != "precondition_failed" {
			t.Errorf("code = %v, want precondition_failed", problem["code"])
		}
		if problem["param"] != "expected_version" {
			t.Errorf("param = %v, want expected_version", problem["param"])
		}
		if shown := bdProxiedShow(t, bd, p.dir, issue.ID); shown.Title == "never" {
			t.Error("a refused guard wrote to the row")
		}

		// The loop closes: re-read the token the last write answered with and
		// the guarded write lands.
		freshStatus, fresh := sp.updateIssue(t, issue.ID,
			`{"actor":"http-agent","expected_version":`+strconv.FormatInt(second, 10)+`,"patch":{"title":"guarded"}}`)
		if freshStatus != http.StatusOK {
			t.Fatalf("guarded update: status = %d, want 200: %v", freshStatus, fresh)
		}
		if shown := bdProxiedShow(t, bd, p.dir, issue.ID); shown.Title != "guarded" {
			t.Errorf("title = %q, want the guarded write to have landed", shown.Title)
		}
	})

	// THE REPARENT, over the wire, against the graph the two legs really walk.
	// It needs no edge read-back: a cycle can only be observed by asking for one,
	// and the refusal IS the observation. A under B is legal; B under A then
	// closes a parent-child cycle, which CheckDependencyCycleInTx refuses on the
	// store legs and depRepo.Insert's HasCycle refuses on the unit-of-work leg.
	t.Run("a reparent that closes a cycle is a 409 over the wire", func(t *testing.T) {
		a := bdProxiedCreate(t, bd, p.dir, "cycle a", "-p", "2")
		b := bdProxiedCreate(t, bd, p.dir, "cycle b", "-p", "2")

		status, body := sp.updateIssue(t, a.ID, `{"actor":"http-agent","patch":{"parent_id":"`+b.ID+`"}}`)
		if status != http.StatusOK {
			t.Fatalf("the legal reparent: status = %d, want 200: %v", status, body)
		}

		cycleStatus, problem := sp.updateIssue(t, b.ID, `{"actor":"http-agent","patch":{"parent_id":"`+a.ID+`"}}`)
		if cycleStatus != http.StatusConflict {
			t.Fatalf("the cycling reparent: status = %d, want 409: %v", cycleStatus, problem)
		}
		if problem["code"] != "dependency_cycle" {
			t.Errorf("code = %v, want dependency_cycle", problem["code"])
		}
		if problem["param"] != "patch.parent_id" {
			t.Errorf("param = %v, want patch.parent_id", problem["param"])
		}
		// THE HIERARCHY MEMBERS ARE ABSENT, and the document says so for this
		// operation: CheckBlockingHierarchyInTx probes blocks and
		// conditional-blocks only, so a parent-child write can never raise the
		// hierarchy refusal. A client must not dispatch on their presence here.
		for _, member := range []string{"issue_id", "blocker_id", "blocker_is_ancestor"} {
			if _, present := problem[member]; present {
				t.Errorf("%s is present on a reparent refusal; this operation promises the plain cycle only: %v", member, problem)
			}
		}
	})

	// The workspace's own vocabulary is the role's question, and the document
	// promises its refusal as a 400 rather than letting it fall through to a 500.
	t.Run("a value this workspace refuses is a 400", func(t *testing.T) {
		issue := bdProxiedCreate(t, bd, p.dir, "typed", "-p", "2")

		status, body := sp.updateIssue(t, issue.ID,
			`{"actor":"http-agent","patch":{"issue_type":"not-a-configured-type"}}`)
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400: %v", status, body)
		}
		if body["code"] != "invalid_argument" {
			t.Errorf("code = %v, want invalid_argument", body["code"])
		}
		if shown := bdProxiedShow(t, bd, p.dir, issue.ID); string(shown.IssueType) == "not-a-configured-type" {
			t.Error("the refused issue_type was written anyway")
		}
	})

	// The lifecycle operations and the patch share one path under different
	// methods. A PATCH must never execute as one of them.
	t.Run("a patch is never a lifecycle verb", func(t *testing.T) {
		issue := bdProxiedCreate(t, bd, p.dir, "not a close", "-p", "2")

		// The custom-method segment under PATCH addresses an issue whose id is
		// literally "<id>:close", which no row holds.
		status, body := sp.updateIssue(t, issue.ID+":close", `{"actor":"http-agent","patch":{"title":"t"}}`)
		if status != http.StatusNotFound {
			t.Fatalf("PATCH a custom-method segment: status = %d, want 404: %v", status, body)
		}
		if shown := bdProxiedShow(t, bd, p.dir, issue.ID); string(shown.Status) != "open" {
			t.Errorf("a PATCH executed as a close: status %q", shown.Status)
		}
	})

	sp.shutdown(t)
}
