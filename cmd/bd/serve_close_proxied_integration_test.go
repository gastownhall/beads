//go:build cgo

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// End-to-end for the close, against real Dolt through a real `bd serve`
// subprocess. The pure tests in internal/httpapi cover the wire edge on a fake
// role; what only this level can prove is the TRANSACTION — that first-close-
// wins is a property of the stored row rather than of a fake's bookkeeping, and
// that the open-children refusal and its force bypass are the real policy the
// CLI enforces, over a graph a fake has no way to hold.
//
// The role-level transition itself is owned against a real store by
// internal/storage/uow/lifecycle_close_reopen_contract_test.go. This test owns
// the wire-to-role seam and cites that one rather than duplicating it.

// lifecycleVerbRaw posts a custom method for id and returns the status and the
// UNDECODED body, so a caller can choose how to read a member rather than
// inheriting encoding/json's default for `any`.
//
// It exists for `revision`, and the update slice paid for the lesson: live
// row_lock tokens run past 5e17, where a float64's ulp is already 64, so a
// token read through `map[string]any` is a value NEAR the token and is not it.
// The decoded helpers below stay as they were — a case comparing prose members
// wants the ordinary decode — and the guarded cases read the raw bytes.
func (sp *serveProcess) lifecycleVerbRaw(t *testing.T, verb, id, body string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, sp.url("/v0/beads/issues/"+id+":"+verb), strings.NewReader(body))
	if err != nil {
		t.Fatalf("new %s request: %v", verb, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := sp.client.Do(req)
	if err != nil {
		t.Fatalf("POST %s %s: %v\nstderr:\n%s", verb, id, err, sp.stderr.String())
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s body: %v", verb, err)
	}
	return resp.StatusCode, raw
}

// decodeVerbBody is the ordinary decode the unguarded cases use.
func decodeVerbBody(t *testing.T, verb string, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("decode %s body %q: %v", verb, raw, err)
		}
	}
	return m
}

// closeIssueRaw posts a close and returns the status and the undecoded body.
func (sp *serveProcess) closeIssueRaw(t *testing.T, id, body string) (int, []byte) {
	t.Helper()
	return sp.lifecycleVerbRaw(t, "close", id, body)
}

// closeIssue posts a close for id and returns the status and decoded body.
func (sp *serveProcess) closeIssue(t *testing.T, id, body string) (int, map[string]any) {
	t.Helper()
	status, raw := sp.closeIssueRaw(t, id, body)
	return status, decodeVerbBody(t, "close", raw)
}

// reopenIssueRaw posts a reopen and returns the status and the undecoded body.
func (sp *serveProcess) reopenIssueRaw(t *testing.T, id, body string) (int, []byte) {
	t.Helper()
	return sp.lifecycleVerbRaw(t, "reopen", id, body)
}

// reopenIssue posts a reopen for id and returns the status and decoded body.
func (sp *serveProcess) reopenIssue(t *testing.T, id, body string) (int, map[string]any) {
	t.Helper()
	status, raw := sp.reopenIssueRaw(t, id, body)
	return status, decodeVerbBody(t, "reopen", raw)
}

func TestProxiedServerServeClose(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "srvclo")
	sp := startServe(t, bd, p.dir, bdProxiedEnv(p.dir))

	// THE FIRST-CLOSE-WINS PROOF, read back through a second close. A fake role
	// can report Changed false; only a real store can show that the second
	// close left the first one's reason and session in the columns.
	t.Run("the first close wins and a re-close does not rewrite it", func(t *testing.T) {
		issue := bdProxiedCreate(t, bd, p.dir, "close over http", "-p", "1")

		status, body := sp.closeIssue(t, issue.ID, `{"actor":"http-agent","reason":"shipped it","session":"session-a"}`)
		if status != http.StatusOK {
			t.Fatalf("first close: status = %d, want 200: %v", status, body)
		}
		if body["already_closed"] != false {
			t.Errorf("already_closed = %v, want false on a fresh close", body["already_closed"])
		}
		closed, ok := body["issue"].(map[string]any)
		if !ok {
			t.Fatalf("issue = %#v, want an object", body["issue"])
		}
		if closed["status"] != "closed" || closed["close_reason"] != "shipped it" {
			t.Errorf("the response does not describe the closed row: %v", closed)
		}

		// The CLI reads the same row through its own path.
		shown := bdProxiedShow(t, bd, p.dir, issue.ID)
		if string(shown.Status) != "closed" || shown.CloseReason != "shipped it" || shown.ClosedBySession != "session-a" {
			t.Fatalf("bd show reports status %q reason %q session %q; the HTTP close did not land",
				shown.Status, shown.CloseReason, shown.ClosedBySession)
		}

		// The replay. It succeeds, reports the idempotent flag, and — the whole
		// point — writes NEITHER value, so the record of why the work ended
		// cannot be rewritten by a client replaying its own recovery.
		status, body = sp.closeIssue(t, issue.ID, `{"actor":"other-agent","reason":"REWRITTEN","session":"session-b"}`)
		if status != http.StatusOK {
			t.Fatalf("re-close: status = %d, want 200: %v", status, body)
		}
		if body["already_closed"] != true {
			t.Errorf("already_closed = %v, want true for a re-close", body["already_closed"])
		}

		after := bdProxiedShow(t, bd, p.dir, issue.ID)
		if after.CloseReason != "shipped it" {
			t.Errorf("close_reason = %q after the replay, want the FIRST close's value", after.CloseReason)
		}
		if after.ClosedBySession != "session-a" {
			t.Errorf("closed_by_session = %q after the replay, want the FIRST close's value", after.ClosedBySession)
		}
	})

	// THE FORCED-CLOSE PROOF. The open-children refusal is the role's, over a
	// real parent-child edge; the count in the problem body comes from the
	// transaction that refused, and the force bypass is what a caller does next.
	t.Run("open children refuse an unforced close and force bypasses them", func(t *testing.T) {
		parent := bdProxiedCreate(t, bd, p.dir, "the parent", "-p", "1")
		for i := range 2 {
			child := bdProxiedCreate(t, bd, p.dir, fmt.Sprintf("child %d", i), "-p", "2")
			if out, err := bdProxiedRun(t, bd, p.dir, "dep", "add", child.ID, parent.ID, "--type", "parent-child"); err != nil {
				t.Fatalf("bd dep add: %v\n%s", err, out)
			}
		}

		status, body := sp.closeIssue(t, parent.ID, `{"actor":"http-agent","reason":"too soon"}`)
		if status != http.StatusConflict {
			t.Fatalf("unforced close over open children: status = %d, want 409: %v", status, body)
		}
		if body["code"] != "not_closable" {
			t.Errorf("code = %v, want not_closable", body["code"])
		}
		// Read inside the refusing transaction, never parsed out of the prose.
		if body["open_children"] != float64(2) {
			t.Errorf("open_children = %v, want 2", body["open_children"])
		}
		if body["request_id"] == nil {
			t.Error("no request_id on the problem body")
		}

		// A refusal writes nothing.
		if shown := bdProxiedShow(t, bd, p.dir, parent.ID); string(shown.Status) == "closed" {
			t.Fatalf("the refused close closed the issue anyway: status %q", shown.Status)
		}

		status, body = sp.closeIssue(t, parent.ID, `{"actor":"http-agent","reason":"closing anyway","force":true}`)
		if status != http.StatusOK {
			t.Fatalf("forced close: status = %d, want 200: %v", status, body)
		}
		// A forced close reports what it bypassed, which is exactly what the
		// caller who bypassed it wants to know.
		if body["open_children"] != float64(2) {
			t.Errorf("open_children = %v on the forced close, want the 2 it bypassed", body["open_children"])
		}
		if shown := bdProxiedShow(t, bd, p.dir, parent.ID); string(shown.Status) != "closed" {
			t.Errorf("bd show reports status %q after a forced close", shown.Status)
		}
	})

	// The rule-8 oracle. Every other operation on this surface is checked
	// against its CLI equivalent by comparing FULL item JSON, and "parity holds
	// by construction" is exactly the reasoning that rule exists to forbid: a
	// later CLI-side enrichment of close output must fail here rather than drift
	// silently against the HTTP body.
	//
	// The CLI side is an idempotent RE-close, the claim oracle's device: it
	// writes nothing under first-close-wins, so both surfaces describe the same
	// row from the same post-state snapshot. `bd show --json` is deliberately
	// not the oracle — it answers IssueDetails, with the counts and the revision
	// this response does not carry, so it would compare two different shapes.
	t.Run("the closed issue matches bd close --json element 0", func(t *testing.T) {
		issue := bdProxiedCreate(t, bd, p.dir, "parity oracle", "-p", "1",
			"-d", "described", "--acceptance", "accepted", "--label", "oracle")

		status, body := sp.closeIssue(t, issue.ID, `{"actor":"parity-agent","reason":"done","session":"oracle-session"}`)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200: %v", status, body)
		}
		fromHTTP, ok := body["issue"].(map[string]any)
		if !ok {
			t.Fatalf("issue = %#v, want an object", body["issue"])
		}

		fromCLI := bdProxiedCloseOneRaw(t, bd, p.dir, issue.ID, "--reason", "ignored under first-close-wins")

		// The allowlist is EMPTY: both surfaces marshal the same canonical
		// struct from the same post-state snapshot, so any difference at all is
		// a real divergence and belongs in review, not in this list.
		if diff := diffJSONObjects(fromHTTP, fromCLI, nil); diff != "" {
			t.Errorf("CloseIssueResponse.issue and `bd close --json`[0] disagree:\n%s", diff)
		}
	})

	// THE ROUND-TRIP PROOF for reopenIssue. A fake role can report Changed; only
	// a real store can show that the reopen CLEARED the close record — which is
	// what makes the close's first-close-wins rule survivable, since the way to
	// write a new reason is to reopen and close again.
	t.Run("a close and reopen round trip clears the close record", func(t *testing.T) {
		issue := bdProxiedCreate(t, bd, p.dir, "round trip", "-p", "1")

		if status, body := sp.closeIssue(t, issue.ID, `{"actor":"http-agent","reason":"shipped","session":"session-x"}`); status != http.StatusOK {
			t.Fatalf("close: status = %d, want 200: %v", status, body)
		}
		closed := bdProxiedShow(t, bd, p.dir, issue.ID)
		if closed.CloseReason != "shipped" || closed.ClosedBySession != "session-x" {
			t.Fatalf("the close did not record its reason/session: %q/%q", closed.CloseReason, closed.ClosedBySession)
		}

		status, body := sp.reopenIssue(t, issue.ID, `{"actor":"http-agent","reason":"the fix regressed"}`)
		if status != http.StatusOK {
			t.Fatalf("reopen: status = %d, want 200: %v", status, body)
		}
		if body["already_open"] != false {
			t.Errorf("already_open = %v, want false on a reopen that changed the row", body["already_open"])
		}
		reopened, ok := body["issue"].(map[string]any)
		if !ok {
			t.Fatalf("issue = %#v, want an object", body["issue"])
		}
		if reopened["status"] != "open" {
			t.Errorf("the response reports status %v, want open", reopened["status"])
		}

		after := bdProxiedShow(t, bd, p.dir, issue.ID)
		if string(after.Status) != "open" {
			t.Fatalf("bd show reports status %q; the HTTP reopen did not land", after.Status)
		}
		if after.CloseReason != "" || after.ClosedBySession != "" {
			t.Errorf("the reopen left close_reason %q and closed_by_session %q; both describe a closure that no longer holds",
				after.CloseReason, after.ClosedBySession)
		}

		// The reason is recorded on the EVENT, which is where the document tells
		// a client to read it back — not on a field of the issue, and not in the
		// response.
		events, err := bdProxiedRun(t, bd, p.dir, "history", issue.ID, "--events")
		if err != nil {
			t.Fatalf("bd history --events: %v\n%s", err, events)
		}
		if !strings.Contains(string(events), "the fix regressed") {
			t.Errorf("the reopen reason is not on the issue's event stream:\n%s", events)
		}

		// And the round trip really is a round trip: the issue is claimable
		// again, which is the recovery flow this pair exists to serve.
		if status, claim := sp.claim(t, issue.ID, "http-agent"); status != http.StatusOK {
			t.Errorf("claim after reopen: status = %d, want 200: %v", status, claim)
		}
	})

	// The idempotent half, against the real store: a reopen of an issue that was
	// never done changes nothing and succeeds.
	t.Run("reopening an issue that was never done is idempotent", func(t *testing.T) {
		issue := bdProxiedCreate(t, bd, p.dir, "never closed", "-p", "2")

		status, body := sp.reopenIssue(t, issue.ID, `{"actor":"http-agent"}`)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200: %v", status, body)
		}
		if body["already_open"] != true {
			t.Errorf("already_open = %v, want true for an issue that was never done", body["already_open"])
		}
		if shown := bdProxiedShow(t, bd, p.dir, issue.ID); string(shown.Status) != "open" {
			t.Errorf("bd show reports status %q after an idempotent reopen", shown.Status)
		}
	})

	t.Run("a closed issue is no longer claimable", func(t *testing.T) {
		issue := bdProxiedCreate(t, bd, p.dir, "closed then claimed", "-p", "2")
		if status, body := sp.closeIssue(t, issue.ID, `{"actor":"http-agent"}`); status != http.StatusOK {
			t.Fatalf("close: status = %d: %v", status, body)
		}

		// The two operations agree about the row they share, which is the whole
		// reason the lifecycle pair belongs on this surface.
		status, body := sp.claim(t, issue.ID, "http-agent")
		if status != http.StatusConflict {
			t.Fatalf("claim of a closed issue: status = %d, want 409: %v", status, body)
		}
		if body["code"] != "not_claimable" || body["issue_status"] != "closed" {
			t.Errorf("claim refusal = %v, want not_claimable/closed", body)
		}
	})

	t.Run("unknown ids are 404 and refused bodies write nothing", func(t *testing.T) {
		status, body := sp.closeIssue(t, "bd-nosuchissue", `{"actor":"http-agent"}`)
		if status != http.StatusNotFound {
			t.Fatalf("unknown id: status = %d, want 404: %v", status, body)
		}
		if body["code"] != "not_found" {
			t.Errorf("code = %v, want not_found", body["code"])
		}

		issue := bdProxiedCreate(t, bd, p.dir, "never closed", "-p", "2")
		for _, refused := range []string{
			`{"actor":"   "}`,
			`{"actor":"agent\nbd serve: close bd-0 by mallory"}`,
			`{"actor":"agent","reason":"` + strings.Repeat("x", 300) + `"}`,
			`{"actor":"agent","cascade":true}`,
			`{"actor":"agent","force":"yes"}`,
		} {
			status, problem := sp.closeIssue(t, issue.ID, refused)
			if status != http.StatusBadRequest {
				t.Fatalf("body %.40q: status = %d, want 400: %v", refused, status, problem)
			}
			if problem["code"] != "invalid_argument" {
				t.Errorf("body %.40q: code = %v, want invalid_argument", refused, problem["code"])
			}
		}

		// The refusals are wire-edge refusals: nothing reached the database.
		if shown := bdProxiedShow(t, bd, p.dir, issue.ID); string(shown.Status) != "open" {
			t.Errorf("a refused close wrote to the row: status %q", shown.Status)
		}
	})

	// THE GUARD, AS A REAL READ-MODIFY-WRITE LOOP. The pure tests assert the
	// projection onto issueops.CloseRequest and the shape of the 409; what only
	// this level can show is that the token a write ANSWERS with is the one the
	// next request must send, that a stale one really refuses against a store,
	// and — the claim a fake has no way to hold — that the guard is evaluated
	// BEFORE the idempotent re-close.
	//
	// The stale token is made by a real concurrent writer rather than by
	// arithmetic. The token is opaque and compared for equality alone, so
	// `revision - 1` is not "the previous revision"; there is no way to
	// construct a stale one except to let something move the row.
	t.Run("a guarded close and reopen compose from the revision the write answered", func(t *testing.T) {
		issue := bdProxiedCreate(t, bd, p.dir, "guarded lifecycle", "-p", "2")

		// The first guarded write has to seed itself from an unguarded one:
		// no READ on this surface publishes a revision yet, which is the gap
		// the document names on every one of these members.
		status, raw := sp.updateIssueRaw(t, issue.ID, `{"actor":"http-agent","patch":{"notes":"seeded"}}`)
		if status != http.StatusOK {
			t.Fatalf("the seeding write: status = %d, want 200: %s", status, raw)
		}
		first := revisionOf(t, raw)

		status, raw = sp.updateIssueRaw(t, issue.ID, `{"actor":"other-agent","patch":{"notes":"moved"}}`)
		if status != http.StatusOK {
			t.Fatalf("the concurrent write: status = %d, want 200: %s", status, raw)
		}
		second := revisionOf(t, raw)
		if second == first {
			t.Fatalf("revision = %d after a second write; a write that does not move the token makes every guard vacuous", second)
		}

		// The stale close. Nothing is written, and the 409 names the member.
		status, problem := sp.closeIssue(t, issue.ID,
			`{"actor":"http-agent","reason":"never","expected_version":`+strconv.FormatInt(first, 10)+`}`)
		if status != http.StatusConflict {
			t.Fatalf("stale close: status = %d, want 409: %v", status, problem)
		}
		if problem["code"] != "precondition_failed" || problem["param"] != "expected_version" {
			t.Errorf("stale close: code = %v param = %v, want precondition_failed / expected_version", problem["code"], problem["param"])
		}
		if shown := bdProxiedShow(t, bd, p.dir, issue.ID); string(shown.Status) != "open" || shown.CloseReason != "" {
			t.Errorf("a refused guard closed the row anyway: status %q reason %q", shown.Status, shown.CloseReason)
		}

		// FORCE DOES NOT BYPASS IT. `force` bypasses close POLICY; the guard is
		// about whether this is still the row the caller read, which force says
		// nothing about.
		status, problem = sp.closeIssue(t, issue.ID,
			`{"actor":"http-agent","force":true,"expected_version":`+strconv.FormatInt(first, 10)+`}`)
		if status != http.StatusConflict {
			t.Fatalf("forced stale close: status = %d, want 409 — force bypasses policy, never a precondition: %v", status, problem)
		}
		if shown := bdProxiedShow(t, bd, p.dir, issue.ID); string(shown.Status) != "open" {
			t.Errorf("a forced stale close closed the row: status %q", shown.Status)
		}

		// The fresh guard lands, and answers with the token the close minted.
		status, raw = sp.closeIssueRaw(t, issue.ID,
			`{"actor":"http-agent","reason":"shipped","expected_version":`+strconv.FormatInt(second, 10)+`}`)
		if status != http.StatusOK {
			t.Fatalf("guarded close: status = %d, want 200: %s", status, raw)
		}
		closed := revisionOf(t, raw)
		if closed == second {
			t.Errorf("revision = %d after a close that wrote; the token did not move", closed)
		}
		if shown := bdProxiedShow(t, bd, p.dir, issue.ID); string(shown.Status) != "closed" {
			t.Fatalf("the guarded close did not land: status %q", shown.Status)
		}

		// THE CLAIM ONLY A REAL STORE CAN SHOW: the guard is checked BEFORE the
		// idempotent re-close. The same body that earns a 200 with
		// `already_closed` unguarded is a 409 when it carries a token the close
		// itself has already invalidated — which is what lets a client read
		// `already_closed` as "and nothing has happened here since".
		status, problem = sp.closeIssue(t, issue.ID,
			`{"actor":"http-agent","expected_version":`+strconv.FormatInt(second, 10)+`}`)
		if status != http.StatusConflict {
			t.Fatalf("guarded re-close with a pre-close token: status = %d, want 409: %v", status, problem)
		}

		// Unguarded, that same replay is the ordinary idempotent answer.
		status, raw = sp.closeIssueRaw(t, issue.ID, `{"actor":"http-agent","reason":"REWRITTEN"}`)
		if status != http.StatusOK {
			t.Fatalf("unguarded re-close: status = %d, want 200: %s", status, raw)
		}
		replay := decodeVerbBody(t, "close", raw)
		if replay["already_closed"] != true {
			t.Errorf("already_closed = %v, want true", replay["already_closed"])
		}
		afterReplay := revisionOf(t, raw)
		if afterReplay != closed {
			t.Errorf("revision = %d after an idempotent re-close, want the unchanged %d: a replay that writes nothing must not move the token",
				afterReplay, closed)
		}

		// The reopen's own guard, on the mirror. Stale first.
		status, problem = sp.reopenIssue(t, issue.ID,
			`{"actor":"http-agent","expected_version":`+strconv.FormatInt(second, 10)+`}`)
		if status != http.StatusConflict {
			t.Fatalf("stale reopen: status = %d, want 409: %v", status, problem)
		}
		if problem["code"] != "precondition_failed" || problem["param"] != "expected_version" {
			t.Errorf("stale reopen: code = %v param = %v", problem["code"], problem["param"])
		}
		if shown := bdProxiedShow(t, bd, p.dir, issue.ID); string(shown.Status) != "closed" {
			t.Errorf("a refused guard reopened the row: status %q", shown.Status)
		}

		// And the loop closes: the token the last successful write answered
		// with is the one that lands.
		status, raw = sp.reopenIssueRaw(t, issue.ID,
			`{"actor":"http-agent","expected_version":`+strconv.FormatInt(afterReplay, 10)+`}`)
		if status != http.StatusOK {
			t.Fatalf("guarded reopen: status = %d, want 200: %s", status, raw)
		}
		if reopened := revisionOf(t, raw); reopened == afterReplay {
			t.Errorf("revision = %d after a reopen that wrote; the token did not move", reopened)
		}
		if shown := bdProxiedShow(t, bd, p.dir, issue.ID); string(shown.Status) != "open" || shown.CloseReason != "" {
			t.Errorf("the guarded reopen did not land: status %q reason %q", shown.Status, shown.CloseReason)
		}
	})

	sp.shutdown(t)
}

// bdProxiedCloseOneRaw runs `bd close --json` and decodes the one item it
// reports as a generic object rather than into types.Issue: the parity oracle
// compares the JSON both surfaces actually emit, and decoding through the
// struct would hide exactly the field-level drift it exists to catch.
func bdProxiedCloseOneRaw(t *testing.T, bd, dir, id string, args ...string) map[string]any {
	t.Helper()
	full := append([]string{"close", id, "--json"}, args...)
	out, err := bdProxiedRun(t, bd, dir, full...)
	if err != nil {
		t.Fatalf("bd close %s --json failed: %v\n%s", id, err, out)
	}
	s := string(out)
	start := strings.Index(s, "[")
	if start < 0 {
		t.Fatalf("no JSON array in show output:\n%s", s)
	}
	var items []map[string]any
	if err := json.Unmarshal([]byte(s[start:]), &items); err != nil {
		t.Fatalf("parse show JSON: %v\nraw: %s", err, s[start:])
	}
	if len(items) != 1 {
		t.Fatalf("show returned %d items, want 1:\n%s", len(items), s[start:])
	}
	return items[0]
}
