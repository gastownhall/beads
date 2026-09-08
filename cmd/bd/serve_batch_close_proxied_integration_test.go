//go:build cgo

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// End-to-end for the batch close, against real Dolt through a real `bd serve`
// subprocess.
//
// The pure tests in internal/httpapi cover the wire edge and the per-item
// outcome projection against a fake role. What only this level can prove is the
// claim the operation's whole shape rests on: that a batch carrying a BAD ID
// still COMMITS its survivors. A fake reports whatever outcome list the case
// handed it, so a handler that answered per-item outcomes over a transaction
// that had rolled back would look identical there.
//
// The role-level contract against a real store is
// backend/conformance's batch-close legs, run by internal/storage/uow. This
// test owns the wire-to-role seam.

// batchClose posts a batch close and returns the status and decoded body.
func (sp *serveProcess) batchClose(t *testing.T, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, sp.url("/v0/beads/issues:batchClose"), strings.NewReader(body))
	if err != nil {
		t.Fatalf("new batchClose request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := sp.client.Do(req)
	if err != nil {
		t.Fatalf("POST batchClose: %v\nstderr:\n%s", err, sp.stderr.String())
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read batchClose body: %v", err)
	}
	var m map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("decode batchClose body %q: %v", raw, err)
		}
	}
	return resp.StatusCode, m
}

// batchCloseOutcomes reads the per-item array, failing if it is not one.
func batchCloseOutcomes(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	raw, ok := body["outcomes"].([]any)
	if !ok {
		t.Fatalf("outcomes = %#v, want an array: %v", body["outcomes"], body)
	}
	out := make([]map[string]any, 0, len(raw))
	for i, entry := range raw {
		m, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("outcomes[%d] = %#v, want an object", i, entry)
		}
		out = append(out, m)
	}
	return out
}

func TestProxiedServerServeBatchClose(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "srvbcl")
	sp := startServe(t, bd, p.dir, bdProxiedEnv(p.dir))

	// THE PROPERTY THE OPERATION'S SHAPE RESTS ON: a typo in the middle of a
	// batch must not roll back the work either side of it. A fake cannot state
	// this, because it holds no rows to roll back.
	t.Run("a bad id is skipped and the survivors commit", func(t *testing.T) {
		first := bdProxiedCreate(t, bd, p.dir, "survivor one", "-p", "1")
		second := bdProxiedCreate(t, bd, p.dir, "survivor two", "-p", "1")

		status, body := sp.batchClose(t, `{"actor":"http-agent","session":"session-a","items":[`+
			`{"id":"`+first.ID+`","reason":"first reason"},`+
			`{"id":"bd-does-not-exist"},`+
			`{"id":"`+second.ID+`","reason":"second reason"}]}`)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200: a per-item refusal must not take the request down: %v", status, body)
		}

		outcomes := batchCloseOutcomes(t, body)
		if len(outcomes) != 3 {
			t.Fatalf("outcomes = %v, want one per requested item", outcomes)
		}
		if outcomes[1]["code"] != "not_found" {
			t.Errorf("outcomes[1].code = %v, want not_found", outcomes[1]["code"])
		}
		for _, i := range []int{0, 2} {
			if _, refused := outcomes[i]["code"]; refused {
				t.Errorf("outcomes[%d] refused beside the bad id: %v", i, outcomes[i])
			}
		}

		// THE ASSERTION THIS FILE EXISTS FOR, read through the CLI's own path:
		// both survivors are closed, with their OWN reasons, so the commit
		// happened and the reasons really are per item.
		for _, want := range []struct {
			id     string
			reason string
		}{{first.ID, "first reason"}, {second.ID, "second reason"}} {
			shown := bdProxiedShow(t, bd, p.dir, want.id)
			if string(shown.Status) != "closed" {
				t.Errorf("%s has status %q; a refused sibling rolled back a landed close", want.id, shown.Status)
			}
			if shown.CloseReason != want.reason {
				t.Errorf("%s carries reason %q, want %q; reasons are per item, not per request",
					want.id, shown.CloseReason, want.reason)
			}
			if shown.ClosedBySession != "session-a" {
				t.Errorf("%s carries session %q; the request-wide session did not land",
					want.id, shown.ClosedBySession)
			}
		}
	})

	// The duplicate is a typo, not a failure: the second occurrence reports an
	// idempotent re-close at its own index, and the FIRST occurrence's reason is
	// what stays on the row.
	t.Run("a duplicated id reports a re-close at its own index", func(t *testing.T) {
		issue := bdProxiedCreate(t, bd, p.dir, "closed twice in one batch", "-p", "1")

		status, body := sp.batchClose(t, `{"actor":"http-agent","items":[`+
			`{"id":"`+issue.ID+`","reason":"the first one"},`+
			`{"id":"`+issue.ID+`","reason":"REWRITTEN"}]}`)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200: %v", status, body)
		}
		outcomes := batchCloseOutcomes(t, body)
		if len(outcomes) != 2 {
			t.Fatalf("outcomes = %v, want one per requested item including the duplicate", outcomes)
		}
		if outcomes[0]["already_closed"] != false || outcomes[1]["already_closed"] != true {
			t.Errorf("already_closed = %v/%v, want false then true",
				outcomes[0]["already_closed"], outcomes[1]["already_closed"])
		}
		if shown := bdProxiedShow(t, bd, p.dir, issue.ID); shown.CloseReason != "the first one" {
			t.Errorf("close_reason = %q; the second occurrence mutated nothing and must have written nothing",
				shown.CloseReason)
		}
	})

	// Close POLICY, over a real graph — the one refusal a fake cannot construct,
	// since it needs a parent with a live open child.
	t.Run("close policy refuses an item, and force bypasses it", func(t *testing.T) {
		parent := bdProxiedCreate(t, bd, p.dir, "a parent", "-p", "1")
		bdProxiedCreate(t, bd, p.dir, "an open child", "-p", "1", "--parent", parent.ID)

		status, body := sp.batchClose(t, `{"actor":"http-agent","items":[{"id":"`+parent.ID+`"}]}`)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200: %v", status, body)
		}
		outcome := batchCloseOutcomes(t, body)[0]
		if outcome["code"] != "not_closable" {
			t.Fatalf("code = %v, want not_closable: %v", outcome["code"], outcome)
		}
		// The count comes from the refusing transaction, and its PRESENCE is
		// what separates this refusal from the live-blocker one.
		if outcome["open_children"] != float64(1) {
			t.Errorf("open_children = %v, want 1", outcome["open_children"])
		}
		if shown := bdProxiedShow(t, bd, p.dir, parent.ID); string(shown.Status) == "closed" {
			t.Fatal("the refused item closed anyway")
		}

		status, body = sp.batchClose(t, `{"actor":"http-agent","force":true,"items":[{"id":"`+parent.ID+`"}]}`)
		if status != http.StatusOK {
			t.Fatalf("forced: status = %d, want 200: %v", status, body)
		}
		outcome = batchCloseOutcomes(t, body)[0]
		if _, refused := outcome["code"]; refused {
			t.Fatalf("force did not bypass close policy: %v", outcome)
		}
		// Reported by a forced close, because the caller that bypassed the
		// guard is exactly the caller that wants the number.
		if outcome["open_children"] != float64(1) {
			t.Errorf("open_children = %v, want the count the forced close bypassed", outcome["open_children"])
		}
		if shown := bdProxiedShow(t, bd, p.dir, parent.ID); string(shown.Status) != "closed" {
			t.Errorf("the forced close did not land: status %q", shown.Status)
		}
	})

	// A refused BODY means the batch never ran, which is the other half of the
	// contract: nothing may be written by a request that earned a problem
	// document.
	t.Run("a refused body writes nothing", func(t *testing.T) {
		issue := bdProxiedCreate(t, bd, p.dir, "untouched by a bad body", "-p", "1")

		status, body := sp.batchClose(t, `{"actor":"http-agent","items":[{"id":"`+issue.ID+`"},{"id":""}]}`)
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400: %v", status, body)
		}
		if body["param"] != "items[1].id" {
			t.Errorf("param = %v, want items[1].id", body["param"])
		}
		if shown := bdProxiedShow(t, bd, p.dir, issue.ID); string(shown.Status) != "open" {
			t.Errorf("a 400 closed an issue: status %q; the batch must never have run", shown.Status)
		}
	})
}
