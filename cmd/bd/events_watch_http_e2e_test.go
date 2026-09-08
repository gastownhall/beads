//go:build cgo && unix

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The journal STREAM, end to end through a real `bd serve`.
//
// The handler tests in internal/httpapi drive a fake journal a case can write
// to, which is the right shape for the framing and the refusal vocabulary and
// cannot answer the question this file exists for: does a mutation arriving on
// one connection reach a stream that was already open on another, without
// anybody polling? Every layer between those two facts is real here — the
// server-mode provider, the journal write inside the mutation's own
// transaction, the read loop, and the socket.

// watchConn is one open text/event-stream response plus the pieces a case needs
// to read frames off it and hang up like a client.
type watchConn struct {
	resp   *http.Response
	frames *bufio.Reader
	cancel context.CancelFunc
}

// openWatchStream connects to GET /v0/beads/events:watch and consumes the
// leading `retry:` frame, so the caller's next read is a record.
func openWatchStream(t *testing.T, sp *serveProcess, path string, lastEventID string) *watchConn {
	t.Helper()

	// Its own client with no whole-response timeout: this response never
	// completes, and the request context is what ends it.
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sp.url(path), nil)
	if err != nil {
		cancel()
		t.Fatalf("new request: %v", err)
	}
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		cancel()
		t.Fatalf("GET %s: %v\nstderr:\n%s", path, err, sp.stderr.String())
	}
	t.Cleanup(func() {
		cancel()
		_ = resp.Body.Close()
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200\nstderr:\n%s", path, resp.StatusCode, sp.stderr.String())
	}
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/event-stream; charset=utf-8", got)
	}

	wc := &watchConn{resp: resp, frames: bufio.NewReader(resp.Body), cancel: cancel}
	if got := wc.next(t); !strings.HasPrefix(got, "retry: ") {
		t.Fatalf("first frame = %q, want the reconnection delay", got)
	}
	return wc
}

// next reads one frame, skipping the heartbeat comments that keep an idle
// connection alive — a case waiting for a record must not care how long it
// waited.
func (wc *watchConn) next(t *testing.T) string {
	t.Helper()
	for {
		var frame []string
		for {
			line, err := wc.frames.ReadString('\n')
			if err != nil {
				t.Fatalf("reading the stream: %v", err)
			}
			if line == "\n" {
				break
			}
			frame = append(frame, strings.TrimSuffix(line, "\n"))
		}
		joined := strings.Join(frame, "\n")
		if strings.HasPrefix(joined, ":") {
			continue
		}
		return joined
	}
}

// record reads the next frame and returns its id and decoded record, failing
// the case if the frame is not one.
func (wc *watchConn) record(t *testing.T) (int64, map[string]any) {
	t.Helper()
	frame := wc.next(t)
	var id int64
	var data string
	for _, line := range strings.Split(frame, "\n") {
		switch {
		case strings.HasPrefix(line, "id: "):
			if _, err := fmt.Sscanf(line, "id: %d", &id); err != nil {
				t.Fatalf("id line %q: %v", line, err)
			}
		case strings.HasPrefix(line, "data: "):
			data = strings.TrimPrefix(line, "data: ")
		default:
			t.Fatalf("unexpected line %q in a record frame:\n%s", line, frame)
		}
	}
	if data == "" {
		t.Fatalf("frame carries no data:\n%s", frame)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(data), &rec); err != nil {
		t.Fatalf("frame data %q is not JSON: %v", data, err)
	}
	return id, rec
}

// TestServeStreamsTheJournalItIsWriting: hold a stream open on one connection,
// mutate on another, and require the records to arrive without anybody asking
// for them — then drop the stream and resume it with Last-Event-ID, which is
// the whole reconnection contract.
func TestServeStreamsTheJournalItIsWriting(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newServerModeProject(t, bd, "hjw")
	p.env = append(p.env, "BD_EVENTS_JOURNAL=1")

	sp := startServe(t, bd, p.dir, p.env)

	create := func(title string) {
		t.Helper()
		status, body := sp.postJSON(t, "/v0/beads/issues:batchCreate",
			fmt.Sprintf(`{"actor":"tester","items":[{"title":%q,"issue_type":"task"}]}`, title))
		if status != http.StatusOK {
			t.Fatalf("POST batchCreate %q = %d: %v\nstderr:\n%s", title, status, body, sp.stderr.String())
		}
	}

	// OPEN FIRST, MUTATE SECOND. The records this asserts on did not exist when
	// the stream connected, which is the difference between this operation and
	// the paged read.
	stream := openWatchStream(t, sp, "/v0/beads/events:watch?since=0", "")
	mutated := time.Now()
	create("streamed mutation 1")
	create("streamed mutation 2")

	for i := range 2 {
		id, rec := stream.record(t)
		if id != int64(i+1) {
			t.Fatalf("record %d arrived with id %d, want %d", i, id, i+1)
		}
		// THE ID IS THE SEQ. A client resumes from the id, and it can only do
		// that if the two are the same number.
		if seq, _ := rec["seq"].(float64); int64(seq) != id {
			t.Errorf("record %d: id %d but seq %v", i, id, rec["seq"])
		}
		if op, _ := rec["op"].(string); op != "create" {
			t.Errorf("record %d op = %v, want create", i, rec["op"])
		}
		if _, ok := rec["issue"]; !ok {
			t.Errorf("record %d has no `issue` member", i)
		}
	}

	// A LOOSE ARRIVAL BOUND, because "it arrived" is not the claim this
	// operation makes — "it arrived without anybody asking" is. The read above
	// blocks for as long as the request context allows, so without a bound a
	// stream that delivered after a minute would pass. Ten seconds is far above
	// the one-second poll and far below any interval a consumer would have
	// chosen for itself.
	if elapsed := time.Since(mutated); elapsed > 10*time.Second {
		t.Errorf("the mutations took %s to reach an open stream; that is not push delivery", elapsed)
	}

	// The paged read answers the same records from the same checkpoint while the
	// stream is open, which is the fallback the 503 for a saturated cap points
	// at — and proof the stream is not holding the server's read capacity.
	status, body, _ := sp.get(t, "/v0/beads/events?since=0")
	if status != http.StatusOK {
		t.Fatalf("paged read alongside an open stream = %d: %v", status, body)
	}
	if records, _ := body["records"].([]any); len(records) != 2 {
		t.Errorf("paged read returned %d records while streaming, want 2", len(records))
	}

	// RECONNECT. The client hangs up and comes back with the id it reached,
	// against the SAME url — carrying the same stale `since=0` a browser's
	// EventSource would re-send. The header has to win, or every reconnect
	// replays the whole journal.
	stream.cancel()
	resumed := openWatchStream(t, sp, "/v0/beads/events:watch?since=0", "1")

	id, _ := resumed.record(t)
	if id != 2 {
		t.Fatalf("first record after a Last-Event-ID: 1 reconnect = %d, want 2 — `since=0` won and the journal replayed", id)
	}

	// And the resumed stream is live, not a replay that then stops.
	create("streamed mutation 3")
	if id, _ := resumed.record(t); id != 3 {
		t.Fatalf("record after the reconnect = %d, want 3", id)
	}

	// SHUTDOWN WITH THE STREAM STILL OPEN. http.Server.Shutdown waits for
	// active requests, so a stream that did not wind itself up would hold the
	// drain for its full timeout and then be killed — a graceful stop that takes
	// twenty seconds and reports itself forced.
	start := time.Now()
	sp.shutdown(t)
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Errorf("shutdown with an open stream took %s; the stream is holding the drain", elapsed)
	}
	if log := sp.stderr.String(); strings.Contains(log, "event=shutdown_forced") {
		t.Errorf("the shutdown was forced with a stream open:\n%s", log)
	}
}

// TestServeRefusesAStreamOnADisabledJournal: the connect-time refusals are
// ordinary problem+json responses on this operation too, and the stream is
// never opened. A consumer that got a 200 here would hold a connection open
// against a workspace that will never emit a record.
func TestServeRefusesAStreamOnADisabledJournal(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newServerModeProject(t, bd, "hjx")
	// Deliberately NOT enabling the journal. The default is off.

	sp := startServe(t, bd, p.dir, p.env)

	status, body, header := sp.get(t, "/v0/beads/events:watch?since=0")
	if status != http.StatusConflict {
		t.Fatalf("GET /v0/beads/events:watch on a disabled workspace = %d, want 409: %v\nstderr:\n%s",
			status, body, sp.stderr.String())
	}
	if got := header.Get("Content-Type"); !strings.HasPrefix(got, "application/problem+json") {
		t.Errorf("Content-Type = %q, want problem+json", got)
	}
	if code, _ := body["code"].(string); code != "events_journal_disabled" {
		t.Errorf("code = %v, want events_journal_disabled", body["code"])
	}

	sp.shutdown(t)
}
