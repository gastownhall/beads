//go:build cgo

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// End-to-end for claimNext, against real Dolt through a real `bd serve`
// subprocess.
//
// This is the operation whose whole justification a fake cannot express. The
// pure tests in internal/httpapi prove the wire edge — the filter decode, the
// `limit` refusal, the body vocabulary, the absent-row answer — against a role
// that answers whatever the case handed it. What only this level can prove is
// that the ATOMICITY is real: that concurrent claimants never win the same row,
// which is the exact failure of the listing-then-claim composition this
// operation exists to retire.
//
// The role-level contract against a real store is
// backend/conformance/ready_claimer_contract.go, run by internal/storage/uow.
// This test owns the wire-to-role seam and the concurrency claim the wire makes
// on the role's behalf.

// claimNext posts a claim and returns the status and decoded body.
func (sp *serveProcess) claimNext(t *testing.T, query, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, sp.url("/v0/beads/issues:claimNext"+query), strings.NewReader(body))
	if err != nil {
		t.Fatalf("new claimNext request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := sp.client.Do(req)
	if err != nil {
		t.Fatalf("POST claimNext: %v\nstderr:\n%s", err, sp.stderr.String())
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read claimNext body: %v", err)
	}
	var m map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("decode claimNext body %q: %v", raw, err)
		}
	}
	return resp.StatusCode, m
}

// claimedID reads the id out of a claimNext body, or "" when nothing was
// eligible. The ABSENCE is the signal, so this reads presence rather than a
// flag.
func claimedID(t *testing.T, body map[string]any) string {
	t.Helper()
	raw, present := body["claimed"]
	if !present {
		return ""
	}
	claimed, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("claimed = %#v, want an object", raw)
	}
	id, _ := claimed["id"].(string)
	if id == "" {
		t.Fatalf("claimed carries no id: %v", claimed)
	}
	return id
}

func TestProxiedServerServeClaimNext(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "srvcnx")
	sp := startServe(t, bd, p.dir, bdProxiedEnv(p.dir))

	// The loop this operation serves, and the read-back that proves the claim
	// is a property of the stored row rather than of the response body.
	t.Run("a claim wins a ready row and leaves it held", func(t *testing.T) {
		issue := bdProxiedCreate(t, bd, p.dir, "claim-next over http", "-p", "0",
			"--label", "cnx-solo")

		status, body := sp.claimNext(t, "?label=cnx-solo", `{"actor":"poller-a"}`)
		if status != http.StatusOK {
			t.Fatalf("claimNext: status = %d, want 200: %v", status, body)
		}
		if got := claimedID(t, body); got != issue.ID {
			t.Fatalf("claimed %q, want the one ready row %q", got, issue.ID)
		}

		shown := bdProxiedShow(t, bd, p.dir, issue.ID)
		if string(shown.Status) != "in_progress" || shown.Assignee != "poller-a" {
			t.Fatalf("bd show reports status %q assignee %q; the HTTP claim did not land",
				shown.Status, shown.Assignee)
		}

		// The queue this filter describes is now drained, which is a 200 with
		// the member ABSENT rather than any kind of refusal.
		status, body = sp.claimNext(t, "?label=cnx-solo", `{"actor":"poller-b"}`)
		if status != http.StatusOK {
			t.Fatalf("drained claimNext: status = %d, want 200: %v", status, body)
		}
		if _, present := body["claimed"]; present {
			t.Errorf("a drained queue answered with %v; absence is the signal", body["claimed"])
		}
	})

	// THE PROPERTY THE OPERATION EXISTS FOR. Concurrent claimants against one
	// ready front must never win the same row — which is exactly what the
	// listing-then-claim composition cannot promise, because the row moves
	// between the two requests.
	//
	// The assertion is on DUPLICATES rather than on timing, so it is
	// deterministic: however the goroutines interleave, a row won twice is a
	// broken guarantee and a row won once is the guarantee kept.
	t.Run("concurrent claimants never win the same row", func(t *testing.T) {
		const rows = 6
		want := map[string]bool{}
		for i := 0; i < rows; i++ {
			issue := bdProxiedCreate(t, bd, p.dir, "contended ready work", "-p", "0",
				"--label", "cnx-race")
			want[issue.ID] = true
		}

		// More claimants than rows, so the surplus must come back empty rather
		// than duplicating a win.
		const claimants = rows + 3
		var (
			mu  sync.Mutex
			won []string
			wg  sync.WaitGroup
		)
		for i := 0; i < claimants; i++ {
			wg.Add(1)
			go func(n int) {
				defer wg.Done()
				status, body := sp.claimNext(t, "?label=cnx-race", `{"actor":"racer"}`)
				if status != http.StatusOK {
					t.Errorf("claimant %d: status = %d, want 200: %v", n, status, body)
					return
				}
				if id := claimedID(t, body); id != "" {
					mu.Lock()
					won = append(won, id)
					mu.Unlock()
				}
			}(i)
		}
		wg.Wait()

		seen := map[string]bool{}
		for _, id := range won {
			if seen[id] {
				t.Errorf("%s was won TWICE; the claim is not atomic, which is the whole reason this operation exists", id)
			}
			seen[id] = true
			if !want[id] {
				t.Errorf("%s was won but is not one of this case's ready rows", id)
			}
		}
		if len(won) != rows {
			t.Errorf("%d claimants won %d of %d rows; every ready row must go to exactly one of them",
				claimants, len(won), rows)
		}
	})

	// The filter is the listing's, and this is the assertion that it NARROWS
	// rather than being decoded and dropped: a claim that ignored the filter
	// would happily hand back the other label's row.
	t.Run("the filter narrows what a claim may win", func(t *testing.T) {
		mine := bdProxiedCreate(t, bd, p.dir, "wanted", "-p", "0", "--label", "cnx-want")
		bdProxiedCreate(t, bd, p.dir, "unwanted", "-p", "0", "--label", "cnx-avoid")

		status, body := sp.claimNext(t, "?label=cnx-want", `{"actor":"picky"}`)
		if status != http.StatusOK {
			t.Fatalf("filtered claimNext: status = %d, want 200: %v", status, body)
		}
		if got := claimedID(t, body); got != mine.ID {
			t.Fatalf("claimed %q, want the labelled row %q; the filter did not narrow the set", got, mine.ID)
		}
	})

	t.Run("a limit is refused rather than dropped", func(t *testing.T) {
		status, body := sp.claimNext(t, "?limit=1", `{"actor":"poller-a"}`)
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400: %v", status, body)
		}
		if body["code"] != "invalid_argument" || body["param"] != "limit" {
			t.Errorf("code/param = %v/%v, want invalid_argument on `limit`", body["code"], body["param"])
		}
		if body["reason"] != "invalid_value" {
			t.Errorf("reason = %v, want invalid_value: this server knows the name, it refuses the action", body["reason"])
		}
	})
}
