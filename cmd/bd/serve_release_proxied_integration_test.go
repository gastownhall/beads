//go:build cgo

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// End-to-end for the release, against real Dolt through a real `bd serve`
// subprocess. The pure tests in internal/httpapi cover the wire edge on a fake
// role; what only this level can prove is the TRANSACTION, and here that is a
// sharper claim than usual: a REFUSED release must leave the claim STANDING.
//
// A fake role cannot show that. It reports whatever error the case handed it
// and holds no row, so a handler that refused a wrong-holder release AFTER the
// row was already emptied would look identical. This test reads the row back
// through the CLI's own path after every refusal, so "nothing was written" is
// asserted against the stored row rather than against a fake's bookkeeping.
//
// The role-level transition — what the row looks like afterwards, the dropped
// lease, the recorded event — is owned against a real store by
// internal/storage/uow/releaser_contract_test.go. This test owns the
// wire-to-role seam and cites that one rather than duplicating it.

// releaseIssue posts a release for id and returns the status and decoded body.
func (sp *serveProcess) releaseIssue(t *testing.T, id, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, sp.url("/v0/beads/issues/"+id+":release"), strings.NewReader(body))
	if err != nil {
		t.Fatalf("new release request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := sp.client.Do(req)
	if err != nil {
		t.Fatalf("POST release %s: %v\nstderr:\n%s", id, err, sp.stderr.String())
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read release body: %v", err)
	}
	var m map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("decode release body %q: %v", raw, err)
		}
	}
	return resp.StatusCode, m
}

func TestProxiedServerServeRelease(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "srvrel")
	sp := startServe(t, bd, p.dir, bdProxiedEnv(p.dir))

	// The loop this operation completes: claim it, give it back, and see the
	// row available to the next taker. The post-state is the ANONYMOUS one the
	// document describes, read back through the CLI rather than off the
	// response the release itself composed.
	t.Run("a holder releases its own claim", func(t *testing.T) {
		issue := bdProxiedCreate(t, bd, p.dir, "release over http", "-p", "1")

		if status, body := sp.claim(t, issue.ID, "http-agent"); status != http.StatusOK {
			t.Fatalf("claim: status = %d, want 200: %v", status, body)
		}

		status, body := sp.releaseIssue(t, issue.ID, `{"actor":"http-agent"}`)
		if status != http.StatusOK {
			t.Fatalf("release: status = %d, want 200: %v", status, body)
		}
		if body["changed"] != true {
			t.Errorf("changed = %v, want true; the role reports it true on every answer it returns without an error", body["changed"])
		}
		if body["revision"] == nil {
			t.Errorf("the response carries no revision; a caller composing its next expected_version has nothing to read")
		}

		shown := bdProxiedShow(t, bd, p.dir, issue.ID)
		if string(shown.Status) != "open" || shown.Assignee != "" {
			t.Fatalf("bd show reports status %q assignee %q; the HTTP release did not land",
				shown.Status, shown.Assignee)
		}
	})

	// THE GUARD SATISFIED. A supervisor that can NAME the holder releases it
	// without being the holder — the fence replaced rather than bypassed.
	t.Run("a satisfied expected_assignee releases another actor's claim", func(t *testing.T) {
		issue := bdProxiedCreate(t, bd, p.dir, "release-if-current over http", "-p", "1")

		if status, body := sp.claim(t, issue.ID, "worker-a"); status != http.StatusOK {
			t.Fatalf("claim: status = %d, want 200: %v", status, body)
		}

		status, body := sp.releaseIssue(t, issue.ID, `{"actor":"supervisor","expected_assignee":"worker-a"}`)
		if status != http.StatusOK {
			t.Fatalf("guarded release: status = %d, want 200: %v", status, body)
		}
		shown := bdProxiedShow(t, bd, p.dir, issue.ID)
		if string(shown.Status) != "open" || shown.Assignee != "" {
			t.Fatalf("bd show reports status %q assignee %q; the guarded release did not land",
				shown.Status, shown.Assignee)
		}
	})

	// THE PROOF THE FAKE CANNOT GIVE. Each refusal is followed by a read of the
	// stored row, so "nothing was written" is asserted rather than assumed —
	// and the claim SURVIVING a wrong-holder refusal is the property the whole
	// ownership fence exists for.
	t.Run("a refused release leaves the claim standing", func(t *testing.T) {
		issue := bdProxiedCreate(t, bd, p.dir, "refused releases over http", "-p", "1")

		if status, body := sp.claim(t, issue.ID, "worker-a"); status != http.StatusOK {
			t.Fatalf("claim: status = %d, want 200: %v", status, body)
		}

		// The fence: an unforced, unguarded release by somebody else.
		status, body := sp.releaseIssue(t, issue.ID, `{"actor":"mallory"}`)
		if status != http.StatusConflict {
			t.Fatalf("foreign release: status = %d, want 409: %v", status, body)
		}
		if body["code"] != "already_claimed" {
			t.Errorf("code = %v, want already_claimed", body["code"])
		}
		assertStillHeldBy(t, bd, p.dir, issue.ID, "worker-a", "the ownership fence")

		// The guard, missed. It is a precondition_failed rather than the
		// fence's code, and it echoes the REQUEST's expectation with no
		// observation beside it.
		status, body = sp.releaseIssue(t, issue.ID, `{"actor":"supervisor","expected_assignee":"worker-b"}`)
		if status != http.StatusConflict {
			t.Fatalf("stale guard: status = %d, want 409: %v", status, body)
		}
		if body["code"] != "precondition_failed" {
			t.Errorf("code = %v, want precondition_failed", body["code"])
		}
		if body["expected_assignee"] != "worker-b" {
			t.Errorf("expected_assignee = %v, want the value the request sent", body["expected_assignee"])
		}
		if _, present := body["actual_assignee"]; present {
			t.Errorf("actual_assignee = %v; the role reports the holder in prose only, so this member cannot be honestly filled",
				body["actual_assignee"])
		}
		assertStillHeldBy(t, bd, p.dir, issue.ID, "worker-a", "a missed guard")
	})

	// NOT IDEMPOTENT, proved against a real row: the second release of a row
	// the first one emptied is a refusal, not a 200. This is the case a client
	// mapping the code to "already released" is written against.
	t.Run("releasing an unheld row is a refusal", func(t *testing.T) {
		issue := bdProxiedCreate(t, bd, p.dir, "unheld release over http", "-p", "1")

		if status, body := sp.claim(t, issue.ID, "worker-a"); status != http.StatusOK {
			t.Fatalf("claim: status = %d, want 200: %v", status, body)
		}
		if status, body := sp.releaseIssue(t, issue.ID, `{"actor":"worker-a"}`); status != http.StatusOK {
			t.Fatalf("first release: status = %d, want 200: %v", status, body)
		}

		status, body := sp.releaseIssue(t, issue.ID, `{"actor":"worker-a"}`)
		if status != http.StatusConflict {
			t.Fatalf("second release: status = %d, want 409: %v", status, body)
		}
		if body["code"] != "not_releasable" {
			t.Errorf("code = %v, want not_releasable", body["code"])
		}
		// A row that was never claimed at all answers the SAME way, which is
		// the anonymity the non-idempotence argument rests on: there is nothing
		// left on either row to tell the two situations apart.
		fresh := bdProxiedCreate(t, bd, p.dir, "never claimed", "-p", "1")
		status, body = sp.releaseIssue(t, fresh.ID, `{"actor":"worker-a"}`)
		if status != http.StatusConflict || body["code"] != "not_releasable" {
			t.Errorf("a never-claimed row answers %d/%v; it must be indistinguishable from a re-release",
				status, body["code"])
		}
	})

	// The status half of the same code, over a row the CLI closed. `force` does
	// not reach it — it answers "may I release someone else's claim", and this
	// is not a question about ownership.
	t.Run("a closed row refuses a release, forced or not", func(t *testing.T) {
		issue := bdProxiedCreate(t, bd, p.dir, "closed release over http", "-p", "1")

		if status, body := sp.claim(t, issue.ID, "worker-a"); status != http.StatusOK {
			t.Fatalf("claim: status = %d, want 200: %v", status, body)
		}
		if status, body := sp.closeIssue(t, issue.ID, `{"actor":"worker-a","force":true}`); status != http.StatusOK {
			t.Fatalf("close: status = %d, want 200: %v", status, body)
		}

		for _, body := range []string{`{"actor":"worker-a"}`, `{"actor":"worker-a","force":true}`} {
			status, decoded := sp.releaseIssue(t, issue.ID, body)
			if status != http.StatusConflict {
				t.Fatalf("release of a closed row with %s: status = %d, want 409: %v", body, status, decoded)
			}
			if decoded["code"] != "not_releasable" {
				t.Errorf("release of a closed row with %s: code = %v, want not_releasable", body, decoded["code"])
			}
		}
	})

	t.Run("an id naming nothing is a 404", func(t *testing.T) {
		status, body := sp.releaseIssue(t, "bd-does-not-exist", `{"actor":"worker-a"}`)
		if status != http.StatusNotFound {
			t.Fatalf("status = %d, want 404: %v", status, body)
		}
		if body["code"] != "not_found" {
			t.Errorf("code = %v, want not_found", body["code"])
		}
	})
}

// assertStillHeldBy reads the row through the CLI and fails unless the claim is
// intact. It is the assertion this file exists for: every refusal above is
// followed by one, because a handler that emptied the row and THEN refused
// would be indistinguishable from a correct one at the wire.
func assertStillHeldBy(t *testing.T, bd, dir, id, holder, what string) {
	t.Helper()
	shown := bdProxiedShow(t, bd, dir, id)
	if shown.Assignee != holder {
		t.Fatalf("after %s refused the release, %s is held by %q, want %q: the refusal wrote to the row",
			what, id, shown.Assignee, holder)
	}
	if string(shown.Status) != "in_progress" {
		t.Fatalf("after %s refused the release, %s has status %q, want in_progress: the refusal wrote to the row",
			what, id, shown.Status)
	}
}
