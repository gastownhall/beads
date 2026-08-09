//go:build cgo

package main

import (
	"encoding/json"
	"io"
	"net/http"
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

// updateIssue patches id and returns the status and decoded body.
func (sp *serveProcess) updateIssue(t *testing.T, id, body string) (int, map[string]any) {
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
	var m map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("decode update body %q: %v", raw, err)
		}
	}
	return resp.StatusCode, m
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
		for _, refused := range []string{
			`{"actor":"   ","patch":{"title":"t"}}`,
			`{"actor":"agent","patch":{}}`,
			`{"actor":"agent","patch":{"status":"closed"}}`,
			`{"actor":"agent","patch":{"assignee":"mallory"}}`,
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
