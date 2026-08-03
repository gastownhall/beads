//go:build cgo

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// End-to-end for the one write in v0, against real Dolt through a real
// `bd serve` subprocess. The pure tests in internal/httpapi cover the wire edge;
// what only this level can prove is the CAS itself — that two surfaces claiming
// the same issue agree, that a race has exactly one winner, and that the JSON
// the endpoint answers with is the JSON the CLI answers with.

// claim posts a claim for id as actor and returns the status and decoded body.
func (sp *serveProcess) claim(t *testing.T, id, actor string) (int, map[string]any) {
	t.Helper()
	status, body, _ := sp.claimWithBody(t, id, fmt.Sprintf(`{"actor":%q}`, actor))
	return status, body
}

func (sp *serveProcess) claimWithBody(t *testing.T, id, body string) (int, map[string]any, http.Header) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, sp.url("/v0/beads/issues/"+id+":claim"), strings.NewReader(body))
	if err != nil {
		t.Fatalf("new claim request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := sp.client.Do(req)
	if err != nil {
		t.Fatalf("POST claim %s: %v\nstderr:\n%s", id, err, sp.stderr.String())
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read claim body: %v", err)
	}
	var m map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("decode claim body %q: %v", raw, err)
		}
	}
	return resp.StatusCode, m, resp.Header
}

func TestProxiedServerServeClaim(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "srvcl")
	sp := startServe(t, bd, p.dir, bdProxiedEnv(p.dir))

	t.Run("a claim over HTTP is the same claim the CLI sees", func(t *testing.T) {
		issue := bdProxiedCreate(t, bd, p.dir, "claim over http", "-p", "1")

		status, body := sp.claim(t, issue.ID, "http-agent")
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200: %v", status, body)
		}
		if body["already_claimed"] != false {
			t.Errorf("already_claimed = %v, want false on a fresh claim", body["already_claimed"])
		}
		claimed, ok := body["issue"].(map[string]any)
		if !ok {
			t.Fatalf("issue = %#v, want an object", body["issue"])
		}
		if claimed["assignee"] != "http-agent" || claimed["status"] != "in_progress" {
			t.Errorf("claimed issue = %v, want it held by http-agent and in progress", claimed)
		}

		// The CLI reads the same row through its own path.
		shown := bdProxiedShow(t, bd, p.dir, issue.ID)
		if shown.Assignee != "http-agent" || string(shown.Status) != "in_progress" {
			t.Errorf("bd show reports assignee %q status %q; the HTTP claim did not land",
				shown.Assignee, shown.Status)
		}

		// And the CLI's own claim of it now loses, with the copy it has always
		// produced — the HTTP endpoint changed nothing about CLI semantics.
		out := bdProxiedUpdateFail(t, bd, p.dir, issue.ID, "--claim", "--actor", "cli-agent")
		if !strings.Contains(out, issue.ID) {
			t.Errorf("CLI claim refusal does not name the issue:\n%s", out)
		}
	})

	t.Run("a foreign holder is a typed 409 carrying the holder", func(t *testing.T) {
		issue := bdProxiedCreate(t, bd, p.dir, "claimed by the cli first", "-p", "1")
		bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--claim", "--actor", "cli-agent")

		status, body := sp.claim(t, issue.ID, "http-agent")
		if status != http.StatusConflict {
			t.Fatalf("status = %d, want 409: %v", status, body)
		}
		// The whole point: a client classifies this from `code`, `assignee` and
		// `issue_status` — never by substring-matching the prose.
		if body["code"] != "already_claimed" {
			t.Errorf("code = %v, want already_claimed", body["code"])
		}
		if body["assignee"] != "cli-agent" {
			t.Errorf("assignee = %v, want the CLI actor that holds it", body["assignee"])
		}
		if body["issue_status"] != "in_progress" {
			t.Errorf("issue_status = %v, want in_progress", body["issue_status"])
		}
		if body["request_id"] == nil {
			t.Error("no request_id on the problem body")
		}
	})

	t.Run("a closed issue is not claimable", func(t *testing.T) {
		issue := bdProxiedCreate(t, bd, p.dir, "already closed", "-p", "2")
		if out, err := bdProxiedRun(t, bd, p.dir, "close", issue.ID, "--reason", "done"); err != nil {
			t.Fatalf("bd close: %v\n%s", err, out)
		}

		status, body := sp.claim(t, issue.ID, "http-agent")
		if status != http.StatusConflict {
			t.Fatalf("status = %d, want 409: %v", status, body)
		}
		if body["code"] != "not_claimable" {
			t.Errorf("code = %v, want not_claimable", body["code"])
		}
		if body["issue_status"] != "closed" {
			t.Errorf("issue_status = %v, want closed", body["issue_status"])
		}
		// `assignee` is documented with already_claimed only: reporting the
		// actor who closed it would say somebody holds work nobody holds.
		if _, present := body["assignee"]; present {
			t.Errorf("a not_claimable refusal reported an assignee: %v", body)
		}
	})

	t.Run("unknown ids and non-claim paths are 404", func(t *testing.T) {
		status, body := sp.claim(t, "bd-nosuchissue", "http-agent")
		if status != http.StatusNotFound {
			t.Fatalf("unknown id: status = %d, want 404: %v", status, body)
		}
		if body["code"] != "not_found" {
			t.Errorf("code = %v, want not_found", body["code"])
		}

		// The route pattern is a whole-segment wildcard, so this POST reaches
		// the claim handler; the detail path is documented GET-only and must
		// not be treated as a claim of the issue it names.
		req, err := http.NewRequest(http.MethodPost, sp.url("/v0/beads/issues/bd-1"), strings.NewReader(`{"actor":"http-agent"}`))
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := sp.client.Do(req)
		if err != nil {
			t.Fatalf("POST the detail path: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("POST the GET-only detail path: status = %d, want 404", resp.StatusCode)
		}
	})

	t.Run("a refused actor writes nothing", func(t *testing.T) {
		issue := bdProxiedCreate(t, bd, p.dir, "never claimed", "-p", "2")

		for _, body := range []string{
			`{"actor":"   "}`,
			`{"actor":"agent\nbd serve: claim bd-0 by mallory"}`,
			`{"actor":"` + strings.Repeat("x", 300) + `"}`,
			`{"actor":"agent","force":true}`,
		} {
			status, problem, _ := sp.claimWithBody(t, issue.ID, body)
			if status != http.StatusBadRequest {
				t.Fatalf("body %.40q: status = %d, want 400: %v", body, status, problem)
			}
			if problem["code"] != "invalid_argument" {
				t.Errorf("body %.40q: code = %v, want invalid_argument", body, problem["code"])
			}
		}

		// The refusals are wire-edge refusals: nothing reached the database.
		shown := bdProxiedShow(t, bd, p.dir, issue.ID)
		if shown.Assignee != "" || string(shown.Status) != "open" {
			t.Errorf("a refused claim wrote to the row: assignee %q status %q", shown.Assignee, shown.Status)
		}
	})

	// The rule-8 oracle. Every other operation on this surface is checked
	// against its CLI equivalent by comparing FULL item JSON, and "parity holds
	// by construction" is exactly the reasoning that rule exists to forbid: a
	// later CLI-side enrichment of update output must fail here rather than
	// drift silently against the HTTP body.
	t.Run("the claimed issue matches bd update --claim --json element 0", func(t *testing.T) {
		issue := bdProxiedCreate(t, bd, p.dir, "parity oracle", "-p", "1",
			"-d", "described", "--acceptance", "accepted", "--label", "oracle")

		status, body := sp.claim(t, issue.ID, "parity-agent")
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200: %v", status, body)
		}
		fromHTTP, ok := body["issue"].(map[string]any)
		if !ok {
			t.Fatalf("issue = %#v, want an object", body["issue"])
		}

		// A same-actor re-claim through the CLI: idempotent under CLI semantics,
		// so it writes nothing and both sides describe the same row. Element [0]
		// of `bd update --claim --json` is the proxied path's own re-fetch.
		fromCLI := bdProxiedUpdateOneRaw(t, bd, p.dir, issue.ID, "--claim", "--actor", "parity-agent")

		// The allowlist is EMPTY: both surfaces marshal the same canonical
		// struct from the same re-read, so any difference at all is a real
		// divergence and belongs in review, not in this list.
		if diff := diffJSONObjects(fromHTTP, fromCLI, nil); diff != "" {
			t.Errorf("ClaimResponse.issue and `bd update --claim --json`[0] disagree:\n%s", diff)
		}
	})

	t.Run("a re-claim by the holder is idempotent", func(t *testing.T) {
		issue := bdProxiedCreate(t, bd, p.dir, "idempotent", "-p", "2")

		if status, body := sp.claim(t, issue.ID, "http-agent"); status != http.StatusOK {
			t.Fatalf("first claim: status = %d: %v", status, body)
		}
		status, body := sp.claim(t, issue.ID, "http-agent")
		if status != http.StatusOK {
			t.Fatalf("re-claim: status = %d, want 200: %v", status, body)
		}
		if body["already_claimed"] != true {
			t.Errorf("already_claimed = %v, want true for the current holder", body["already_claimed"])
		}
	})

	// The concurrency requirement, against the real CAS. Anything less than
	// "exactly one" is a double-claim: two agents doing the same work.
	t.Run("concurrent claimants produce exactly one winner", func(t *testing.T) {
		issue := bdProxiedCreate(t, bd, p.dir, "contested", "-p", "0")

		const claimants = 8
		type outcome struct {
			actor    string
			status   int
			body     map[string]any
			assignee string
		}
		results := make([]outcome, claimants)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := range claimants {
			wg.Add(1)
			go func() {
				defer wg.Done()
				actor := fmt.Sprintf("racer-%d", i)
				<-start
				status, body := sp.claim(t, issue.ID, actor)
				assignee, _ := body["assignee"].(string)
				results[i] = outcome{actor: actor, status: status, body: body, assignee: assignee}
			}()
		}
		close(start)
		wg.Wait()

		var winners []string
		for _, r := range results {
			switch r.status {
			case http.StatusOK:
				winners = append(winners, r.actor)
			case http.StatusConflict:
				if r.body["code"] != "already_claimed" {
					t.Errorf("%s: code = %v, want already_claimed", r.actor, r.body["code"])
				}
				// The typed extensions hold under contention too, which is
				// exactly when a client needs to know who to coordinate with.
				if r.assignee == "" {
					t.Errorf("%s: 409 carries no assignee: %v", r.actor, r.body)
				}
				if r.body["issue_status"] != "in_progress" {
					t.Errorf("%s: issue_status = %v, want in_progress", r.actor, r.body["issue_status"])
				}
			default:
				t.Errorf("%s: status = %d, want 200 or 409: %v", r.actor, r.status, r.body)
			}
		}
		if len(winners) != 1 {
			t.Fatalf("%d claimants won the race (%v); exactly one must", len(winners), winners)
		}
		for _, r := range results {
			if r.status == http.StatusConflict && r.assignee != "" && r.assignee != winners[0] {
				t.Errorf("%s was told %q holds the issue, but %q won", r.actor, r.assignee, winners[0])
			}
		}

		// And the database agrees with the wire.
		if shown := bdProxiedShow(t, bd, p.dir, issue.ID); shown.Assignee != winners[0] {
			t.Errorf("bd show reports %q holds the issue, the winning response said %q",
				shown.Assignee, winners[0])
		}
	})

	// The detached-close verification, end to end. A client that hangs up
	// mid-claim leaves a transaction to roll back; if that rollback were sent on
	// the request's own canceled context it would fail, and the transaction layer
	// would poison the pinned connection rather than return it. The unit half
	// (TestRunTxResult_ClosesWithADetachedContext) pins the close context itself;
	// this half pins the consequence an operator would see — the server keeps
	// serving claims afterwards.
	t.Run("client disconnects mid-claim leave the server serving", func(t *testing.T) {
		issue := bdProxiedCreate(t, bd, p.dir, "abandoned claims", "-p", "2")

		for i := range 20 {
			ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
			req, err := http.NewRequestWithContext(ctx, http.MethodPost,
				sp.url("/v0/beads/issues/"+issue.ID+":claim"),
				strings.NewReader(fmt.Sprintf(`{"actor":"quitter-%d"}`, i)))
			if err != nil {
				cancel()
				t.Fatalf("new request: %v", err)
			}
			req.Header.Set("Content-Type", "application/json")
			resp, err := sp.client.Do(req)
			if err == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			}
			cancel()
		}

		// Whatever those 20 did or did not manage to write, the server answers
		// the next request. A burned session per disconnect shows up here first.
		if status, _, _ := sp.get(t, "/healthz"); status != http.StatusOK {
			t.Fatalf("/healthz = %d after abandoned claims", status)
		}
		fresh := bdProxiedCreate(t, bd, p.dir, "after the disconnects", "-p", "2")
		status, body := sp.claim(t, fresh.ID, "survivor")
		if status != http.StatusOK {
			t.Fatalf("claim after %d abandoned requests: status = %d, want 200: %v", 20, status, body)
		}
	})

	sp.shutdown(t)
}

// bdProxiedUpdateOneRaw is bdProxiedUpdateOne decoded as a generic object
// rather than into types.Issue: the parity oracle compares the JSON both
// surfaces actually emit, and decoding through the struct would hide exactly
// the field-level drift it exists to catch.
func bdProxiedUpdateOneRaw(t *testing.T, bd, dir string, args ...string) map[string]any {
	t.Helper()
	full := append([]string{"update", "--json"}, args...)
	out, err := bdProxiedRun(t, bd, dir, full...)
	if err != nil {
		t.Fatalf("bd update --json %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	s := string(out)
	start := strings.Index(s, "[")
	if start < 0 {
		t.Fatalf("no JSON array in update output:\n%s", s)
	}
	var items []map[string]any
	if err := json.Unmarshal([]byte(s[start:]), &items); err != nil {
		t.Fatalf("parse update JSON: %v\nraw: %s", err, s[start:])
	}
	if len(items) != 1 {
		t.Fatalf("update returned %d items, want 1:\n%s", len(items), s[start:])
	}
	return items[0]
}

// diffJSONObjects reports the field-level differences between two decoded JSON
// objects, ignoring the named keys. It returns "" when they agree.
func diffJSONObjects(got, want map[string]any, ignore []string) string {
	skip := map[string]bool{}
	for _, k := range ignore {
		skip[k] = true
	}
	keys := map[string]bool{}
	for k := range got {
		keys[k] = true
	}
	for k := range want {
		keys[k] = true
	}
	var names []string
	for k := range keys {
		if !skip[k] {
			names = append(names, k)
		}
	}
	sort.Strings(names)

	var b bytes.Buffer
	for _, k := range names {
		g, inGot := got[k]
		w, inWant := want[k]
		switch {
		case inGot && !inWant:
			fmt.Fprintf(&b, "  %s: only the HTTP body has it (%v)\n", k, g)
		case !inGot && inWant:
			fmt.Fprintf(&b, "  %s: only the CLI output has it (%v)\n", k, w)
		case !reflect.DeepEqual(g, w):
			fmt.Fprintf(&b, "  %s: HTTP %v, CLI %v\n", k, g, w)
		}
	}
	return b.String()
}
