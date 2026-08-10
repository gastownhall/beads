//go:build cgo && unix

package main

import (
	"fmt"
	"net/http"
	"testing"
)

// The events journal over HTTP, end to end through a real `bd serve`.
//
// The handler tests in internal/httpapi drive fakes, which is the right shape
// for the refusal vocabulary and cannot answer the question this file exists
// for: does a mutation that arrives over the HTTP surface actually reach the
// journal the same surface then serves? Every layer between those two facts is
// real here — the server-mode provider serve builds for itself, the activation
// it resolves from the workspace, the journal writes inside the mutation's own
// transaction, and the read plumbing that pages them back out.
//
// It is the composition that has failed before (see the note on
// TestServeActivatesTheEventsJournal): each half looked correct in isolation
// while the server committed with an empty journal and every response looked
// normal.

// TestServeReadsTheJournalItJustWrote: mutate over HTTP, then read the journal
// over HTTP, and require the records to be there, gapless, and paced by a head
// that matches.
func TestServeReadsTheJournalItJustWrote(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newServerModeProject(t, bd, "hje")
	p.env = append(p.env, "BD_EVENTS_JOURNAL=1")

	sp := startServe(t, bd, p.dir, p.env)

	// Nothing has been mutated yet: an ENABLED but untouched journal is a 200
	// with an empty page and a head of zero, which is the answer a disabled
	// workspace must never give.
	status, body, _ := sp.get(t, "/v0/beads/events?since=0")
	if status != http.StatusOK {
		t.Fatalf("GET /v0/beads/events on a fresh workspace = %d: %v\nstderr:\n%s", status, body, sp.stderr.String())
	}
	if head, _ := body["head"].(float64); head != 0 {
		t.Errorf("head on a fresh workspace = %v, want 0", body["head"])
	}
	if records, _ := body["records"].([]any); len(records) != 0 {
		t.Errorf("records on a fresh workspace = %v, want none", records)
	}

	const created = 4
	for i := range created {
		status, body := sp.postJSON(t, "/v0/beads/issues:batchCreate",
			fmt.Sprintf(`{"actor":"tester","items":[{"title":"served mutation %d","issue_type":"task"}]}`, i))
		if status != http.StatusOK {
			t.Fatalf("POST batchCreate %d = %d: %v\nstderr:\n%s", i, status, body, sp.stderr.String())
		}
	}

	status, body, _ = sp.get(t, "/v0/beads/events?since=0")
	if status != http.StatusOK {
		t.Fatalf("GET /v0/beads/events = %d: %v\nstderr:\n%s", status, body, sp.stderr.String())
	}
	records, _ := body["records"].([]any)
	if len(records) != created {
		t.Fatalf("records = %d, want %d — the HTTP mutations did not reach the journal this surface serves: %v",
			len(records), created, body)
	}

	// GAPLESS AND IN ORDER, from 1. A consumer's whole contract is that it can
	// resume from the last seq it processed, which is only true if the sequence
	// it sees has no holes.
	for i, raw := range records {
		rec, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("records[%d] is not an object: %v", i, raw)
		}
		if seq, _ := rec["seq"].(float64); int(seq) != i+1 {
			t.Fatalf("records[%d].seq = %v, want %d — the journal has a gap", i, rec["seq"], i+1)
		}
		if op, _ := rec["op"].(string); op != "create" {
			t.Errorf("records[%d].op = %v, want create", i, rec["op"])
		}
		// The published envelope, not a shape this surface invented: `issue` is
		// always present, and a create carries no dependency half.
		if _, ok := rec["issue"]; !ok {
			t.Errorf("records[%d] has no `issue` member", i)
		}
		if _, ok := rec["dep"]; ok {
			t.Errorf("records[%d] carries `dep` on a create", i)
		}
	}

	// The head paces the consumer: caught up means last seq == head.
	head, _ := body["head"].(float64)
	if int(head) != created {
		t.Fatalf("head = %v, want %d", body["head"], created)
	}

	// Resuming from the head is the steady state of a poller, and it must be an
	// ordinary empty 200 rather than any kind of miss.
	status, body, _ = sp.get(t, fmt.Sprintf("/v0/beads/events?since=%d", int(head)))
	if status != http.StatusOK {
		t.Fatalf("caught-up GET = %d: %v", status, body)
	}
	if records, _ := body["records"].([]any); len(records) != 0 {
		t.Errorf("caught-up records = %v, want none", records)
	}

	// A bounded page still reports the JOURNAL's head, not the page's — that is
	// how the consumer knows to keep reading.
	status, body, _ = sp.get(t, "/v0/beads/events?since=0&limit=2")
	if status != http.StatusOK {
		t.Fatalf("bounded GET = %d: %v", status, body)
	}
	if records, _ := body["records"].([]any); len(records) != 2 {
		t.Fatalf("bounded records = %d, want 2", len(records))
	}
	if h, _ := body["head"].(float64); int(h) != created {
		t.Errorf("bounded page head = %v, want %d — the head must describe the journal", body["head"], created)
	}

	sp.shutdown(t)

	// The same records, through the CLI, on the same workspace. This is the
	// claim the shared record projection makes and the reason it is shared: an
	// operator debugging a consumer must be able to reach for `bd events export`
	// and see what the consumer saw.
	exported := decodeEventRecords(t, p.run(t, bd, "events", "export"))
	if len(exported) != created {
		t.Fatalf("bd events export returned %d records, want %d — the CLI and HTTP reads disagree", len(exported), created)
	}
	for i, rec := range exported {
		if rec.Seq != int64(i+1) {
			t.Errorf("exported[%d].seq = %d, want %d", i, rec.Seq, i+1)
		}
	}
}

// TestServeRefusesAStaleCheckpointWithGone is the retention boundary over HTTP.
// A consumer whose checkpoint was pruned past must be TOLD, with the window the
// server can still serve — not handed the surviving suffix as though it were a
// complete history, and not handed an empty success it would read as "caught
// up" and stall on forever.
//
// Both retention floors are zeroed so the prune can actually cut: the shipped
// defaults (7 days / 100k rows) protect everything a test could create, which
// is the point of them and the reason this has to say so explicitly.
func TestServeRefusesAStaleCheckpointWithGone(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newServerModeProject(t, bd, "hjt")
	p.env = append(p.env,
		"BD_EVENTS_JOURNAL=1",
		"BD_EVENTS_JOURNAL_RETAIN_DAYS=0",
		"BD_EVENTS_JOURNAL_RETAIN_ROWS=0",
		// The prune below is the one this test performs. A maintenance ticker
		// cutting on its own schedule would make the window nondeterministic.
		"BD_EVENTS_JOURNAL_AUTO_PRUNE=0",
	)

	sp := startServe(t, bd, p.dir, p.env)

	const created = 5
	for i := range created {
		status, body := sp.postJSON(t, "/v0/beads/issues:batchCreate",
			fmt.Sprintf(`{"actor":"tester","items":[{"title":"pruned mutation %d","issue_type":"task"}]}`, i))
		if status != http.StatusOK {
			t.Fatalf("POST batchCreate %d = %d: %v\nstderr:\n%s", i, status, body, sp.stderr.String())
		}
	}

	// Cut the first three records out from under a consumer sitting at 0.
	p.run(t, bd, "events", "prune", "--before", "4")

	status, body, _ := sp.get(t, "/v0/beads/events?since=0")
	if status != http.StatusGone {
		t.Fatalf("GET /v0/beads/events?since=0 after a prune = %d, want 410: %v\nstderr:\n%s",
			status, body, sp.stderr.String())
	}
	if code, _ := body["code"].(string); code != "events_journal_truncated" {
		t.Errorf("code = %v, want events_journal_truncated", body["code"])
	}
	// The window, which is the whole payload of this refusal: the consumer's two
	// recoveries are both computed from these numbers.
	for _, tc := range []struct {
		member string
		want   int
	}{{"since", 0}, {"floor", 4}, {"head", created}} {
		got, _ := body[tc.member].(float64)
		if int(got) != tc.want {
			t.Errorf("%s = %v, want %d", tc.member, body[tc.member], tc.want)
		}
	}

	// The documented recovery actually works: resume from floor-1 and the read
	// succeeds with the surviving suffix, gap explicitly accepted.
	floor, _ := body["floor"].(float64)
	status, body, _ = sp.get(t, fmt.Sprintf("/v0/beads/events?since=%d", int(floor)-1))
	if status != http.StatusOK {
		t.Fatalf("resume from floor-1 = %d, want 200: %v", status, body)
	}
	records, _ := body["records"].([]any)
	if len(records) != created-int(floor)+1 {
		t.Fatalf("resumed records = %d, want %d", len(records), created-int(floor)+1)
	}
	first, _ := records[0].(map[string]any)
	if seq, _ := first["seq"].(float64); int(seq) != int(floor) {
		t.Errorf("resumed records[0].seq = %v, want the floor %d", first["seq"], int(floor))
	}

	sp.shutdown(t)
}

// TestServeRefusesTheJournalWhenItIsDisabled is the distinction the data cannot
// make. This workspace never enabled the journal, so it records nothing and
// never will — and the read has to say so rather than answer the empty page an
// enabled-but-untouched workspace answers, which a consumer would poll against
// forever.
func TestServeRefusesTheJournalWhenItIsDisabled(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newServerModeProject(t, bd, "hjd")
	// Deliberately NOT enabling the journal. The default is off.

	sp := startServe(t, bd, p.dir, p.env)

	// A mutation lands, and is not journaled, because nothing is journaling.
	status, postBody := sp.postJSON(t, "/v0/beads/issues:batchCreate",
		`{"actor":"tester","items":[{"title":"unjournaled","issue_type":"task"}]}`)
	if status != http.StatusOK {
		t.Fatalf("POST batchCreate = %d: %v\nstderr:\n%s", status, postBody, sp.stderr.String())
	}

	status, body, _ := sp.get(t, "/v0/beads/events?since=0")
	if status != http.StatusConflict {
		t.Fatalf("GET /v0/beads/events on a disabled workspace = %d, want 409: %v\nstderr:\n%s",
			status, body, sp.stderr.String())
	}
	if code, _ := body["code"].(string); code != "events_journal_disabled" {
		t.Errorf("code = %v, want events_journal_disabled", body["code"])
	}
	if detail, _ := body["detail"].(string); detail == "" {
		t.Error("no detail on the disabled refusal")
	}

	sp.shutdown(t)
}
