//go:build cgo

package main

import (
	"net/http"
	"testing"
)

// End-to-end for PATCH /v0/beads/issues/{id} with `claim: true`, against real
// Dolt through a real `bd serve` subprocess. The pure tests in internal/httpapi
// pin the projection onto issueops.UpdateRequest.Claim and the refusal mapping
// on a fake role; what only this level can prove is the SEMANTICS the member
// promises by passing through to the role `bd update --claim` uses: the claim
// lands, a same-actor re-claim is idempotent, a foreign holder and an
// unclaimable status refuse, and a refused claim writes none of the patch it
// rode beside.
func TestProxiedServerServeUpdateClaim(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "srvupdcl")
	sp := startServe(t, bd, p.dir, bdProxiedEnv(p.dir))

	t.Run("a claim alone lands, and the same actor's re-claim is idempotent", func(t *testing.T) {
		issue := bdProxiedCreate(t, bd, p.dir, "claim via update", "-p", "2")

		status, body := sp.updateIssue(t, issue.ID, `{"actor":"agent-a","claim":true,"patch":{}}`)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200: %v", status, body)
		}
		if body["changed"] != true {
			t.Errorf("changed = %v, want true on a fresh claim", body["changed"])
		}
		claimed, _ := body["issue"].(map[string]any)
		if claimed["assignee"] != "agent-a" || claimed["status"] != "in_progress" {
			t.Errorf("answered issue = %v, want it held by agent-a and in progress", claimed)
		}
		shown := bdProxiedShow(t, bd, p.dir, issue.ID)
		if shown.Assignee != "agent-a" || string(shown.Status) != "in_progress" {
			t.Fatalf("row = %q/%q, want agent-a/in_progress", shown.Assignee, shown.Status)
		}

		// The direct route's idempotence: held by the same actor and in
		// progress already is a success that changes nothing.
		status, body = sp.updateIssue(t, issue.ID, `{"actor":"agent-a","claim":true,"patch":{}}`)
		if status != http.StatusOK {
			t.Fatalf("re-claim: status = %d, want 200: %v", status, body)
		}
		if body["changed"] != false {
			t.Errorf("re-claim: changed = %v, want false", body["changed"])
		}

		// And a re-claim that rides a field edit applies the edit.
		status, body = sp.updateIssue(t, issue.ID, `{"actor":"agent-a","claim":true,"patch":{"title":"still mine"}}`)
		if status != http.StatusOK {
			t.Fatalf("re-claim with a title: status = %d, want 200: %v", status, body)
		}
		if shown := bdProxiedShow(t, bd, p.dir, issue.ID); shown.Title != "still mine" || shown.Assignee != "agent-a" {
			t.Errorf("row = %q held by %q, want the retitle under agent-a's claim", shown.Title, shown.Assignee)
		}
	})

	t.Run("a claim and a patch land in one write", func(t *testing.T) {
		issue := bdProxiedCreate(t, bd, p.dir, "claim and edit", "-p", "3")

		status, body := sp.updateIssue(t, issue.ID,
			`{"actor":"agent-a","claim":true,"patch":{"priority":1,"notes":"picked up"}}`)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200: %v", status, body)
		}
		shown := bdProxiedShow(t, bd, p.dir, issue.ID)
		if shown.Assignee != "agent-a" || string(shown.Status) != "in_progress" {
			t.Errorf("row = %q/%q, want agent-a/in_progress", shown.Assignee, shown.Status)
		}
		if shown.Priority != 1 || shown.Notes != "picked up" {
			t.Errorf("row priority %d notes %q, want the patch beside the claim", shown.Priority, shown.Notes)
		}
	})

	t.Run("a foreign holder refuses the claim and the patch", func(t *testing.T) {
		issue := bdProxiedCreate(t, bd, p.dir, "held", "-p", "2")
		if status, body := sp.updateIssue(t, issue.ID, `{"actor":"agent-a","claim":true,"patch":{}}`); status != http.StatusOK {
			t.Fatalf("seed claim: status = %d: %v", status, body)
		}

		status, problem := sp.updateIssue(t, issue.ID, `{"actor":"agent-b","claim":true,"patch":{"title":"stolen"}}`)
		if status != http.StatusConflict {
			t.Fatalf("status = %d, want 409: %v", status, problem)
		}
		if problem["code"] != "already_claimed" || problem["param"] != "claim" {
			t.Errorf("code/param = %v/%v, want already_claimed/claim", problem["code"], problem["param"])
		}
		if problem["assignee"] != "agent-a" {
			t.Errorf("assignee = %v, want the holder read by the refusing transaction", problem["assignee"])
		}
		shown := bdProxiedShow(t, bd, p.dir, issue.ID)
		if shown.Title != "held" || shown.Assignee != "agent-a" {
			t.Errorf("row = %q held by %q; a refused claim wrote its patch or moved the claim", shown.Title, shown.Assignee)
		}
	})

	t.Run("an unclaimable status refuses the claim and the patch", func(t *testing.T) {
		issue := bdProxiedCreate(t, bd, p.dir, "done already", "-p", "2")
		if out, err := bdProxiedRun(t, bd, p.dir, "close", issue.ID); err != nil {
			t.Fatalf("bd close: %v\n%s", err, out)
		}

		status, problem := sp.updateIssue(t, issue.ID, `{"actor":"agent-a","claim":true,"patch":{"priority":0}}`)
		if status != http.StatusConflict {
			t.Fatalf("status = %d, want 409: %v", status, problem)
		}
		if problem["code"] != "not_claimable" || problem["param"] != "claim" {
			t.Errorf("code/param = %v/%v, want not_claimable/claim", problem["code"], problem["param"])
		}
		if problem["issue_status"] != "closed" {
			t.Errorf("issue_status = %v, want closed", problem["issue_status"])
		}
		shown := bdProxiedShow(t, bd, p.dir, issue.ID)
		if shown.Priority != 2 || shown.Assignee != "" || string(shown.Status) != "closed" {
			t.Errorf("row priority %d assignee %q status %q; a refused claim wrote", shown.Priority, shown.Assignee, shown.Status)
		}
	})

	// The other direction of atomicity: a patch the role refuses leaves no
	// claim behind.
	t.Run("a refused patch leaves no claim behind", func(t *testing.T) {
		issue := bdProxiedCreate(t, bd, p.dir, "bad patch", "-p", "2")

		status, body := sp.updateIssue(t, issue.ID,
			`{"actor":"agent-a","claim":true,"patch":{"issue_type":"not-a-configured-type"}}`)
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400: %v", status, body)
		}
		shown := bdProxiedShow(t, bd, p.dir, issue.ID)
		if shown.Assignee != "" || string(shown.Status) != "open" {
			t.Errorf("row = %q/%q; the refused update left a claim", shown.Assignee, shown.Status)
		}
	})

	sp.shutdown(t)
}
