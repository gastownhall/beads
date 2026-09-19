package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
)

// The journal stream. Half of these cases are about the boundary the poll read
// does not have: a response whose status is spent while it is still deciding
// things. Everything before the first byte must be the paged read's answer
// exactly — the same 400s, the same 409, the same 410 — and the one condition
// that can arrive after it has to be reportable in band without a consumer
// mistaking it for a record.

// liveEventsJournal is a journal a case can WRITE to while a stream is open. It
// is the difference between testing a stream and testing a snapshot: the
// records that matter here are the ones that did not exist when the client
// connected.
type liveEventsJournal struct {
	mu   sync.Mutex
	rows []storage.EventsJournalRow
	// floor is the lowest seq still retained. Zero means nothing was pruned;
	// setting it is what a prune racing an open stream looks like from here.
	floor int64
	// head is the highest seq ever assigned, which survives a prune. It is
	// tracked separately from the rows for that reason.
	head  int64
	reads int
}

func (j *liveEventsJournal) ReadEventsJournalPage(_ context.Context, since int64, limit int) (storage.EventsJournalPage, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.reads++

	if j.floor > 0 && since < j.floor-1 {
		return storage.EventsJournalPage{}, &storage.EventsJournalTruncatedError{
			Since: since, Floor: j.floor, Head: j.head,
		}
	}
	page := storage.EventsJournalPage{Head: j.head}
	for _, row := range j.rows {
		if row.Seq <= since {
			continue
		}
		page.Rows = append(page.Rows, row)
		if len(page.Rows) == limit {
			break
		}
	}
	return page, nil
}

// commit appends one record, as a mutation landing on the served workspace
// would. It returns the seq it assigned.
func (j *liveEventsJournal) commit(row storage.EventsJournalRow) int64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.head++
	row.Seq = j.head
	if row.TS == "" {
		row.TS = "2026-01-02T03:04:05Z"
	}
	if row.Op == "" {
		row.Op = "create"
	}
	if row.IssueID == "" {
		row.IssueID = fmt.Sprintf("bd-%d", row.Seq)
	}
	if row.IssueJSON == "" {
		row.IssueJSON = fmt.Sprintf(`{"id":%q}`, row.IssueID)
	}
	j.rows = append(j.rows, row)
	return row.Seq
}

// prune drops everything below floor, exactly as `bd events prune` does — and,
// for these cases, exactly while somebody is reading.
func (j *liveEventsJournal) prune(floor int64) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.floor = floor
	kept := j.rows[:0]
	for _, row := range j.rows {
		if row.Seq >= floor {
			kept = append(kept, row)
		}
	}
	j.rows = kept
}

func (j *liveEventsJournal) readCount() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.reads
}

// endlessEventsJournal always answers a FULL batch, which is the backlog case:
// a consumer resuming from a week-old checkpoint drains hundreds of these back
// to back and the loop never sleeps between them.
//
// It refuses after a bounded number of reads so that an implementation which
// ignores its exits fails a count rather than spinning for the rest of the test
// binary's life.
type endlessEventsJournal struct {
	mu    sync.Mutex
	reads int
}

const endlessJournalReadCap = 5000

func (j *endlessEventsJournal) ReadEventsJournalPage(_ context.Context, since int64, limit int) (storage.EventsJournalPage, error) {
	j.mu.Lock()
	j.reads++
	runaway := j.reads > endlessJournalReadCap
	j.mu.Unlock()
	if runaway {
		return storage.EventsJournalPage{}, errors.New("endless journal read cap reached")
	}

	rows := make([]storage.EventsJournalRow, 0, limit)
	for i := range limit {
		seq := since + int64(i) + 1
		rows = append(rows, storage.EventsJournalRow{
			Seq: seq, TS: "2026-01-02T03:04:05Z", Op: "create",
			IssueID: fmt.Sprintf("bd-%d", seq), IssueJSON: fmt.Sprintf(`{"id":"bd-%d"}`, seq),
		})
	}
	return storage.EventsJournalPage{Rows: rows, Head: since + int64(limit) + 1}, nil
}

func (j *endlessEventsJournal) readCount() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.reads
}

// discardWriter is a flushable ResponseWriter that keeps nothing, for the cases
// that stream a backlog nobody reads.
type discardWriter struct{ header http.Header }

func (d *discardWriter) Header() http.Header {
	if d.header == nil {
		d.header = http.Header{}
	}
	return d.header
}
func (d *discardWriter) Write(b []byte) (int, error) { return len(b), nil }
func (d *discardWriter) WriteHeader(int)             {}
func (d *discardWriter) Flush()                      {}

// watchServer stands up a roles-backed server over one live journal with the
// cadences shrunk to milliseconds, so a case waits for a heartbeat in the time
// it takes to run rather than in the time it takes to matter.
func watchServer(t *testing.T, journal storage.EventsJournalCursor, tune ...func(*Server)) *testServer {
	t.Helper()
	tune = append([]func(*Server){func(s *Server) {
		s.watchPoll = 5 * time.Millisecond
		s.watchBeat = 40 * time.Millisecond
	}}, tune...)
	return newTestServer(t, rolesConfig(Config{EventsJournal: journal, EventsJournalEnabled: true}), tune...)
}

// watchStream is one open stream plus the handle a case uses to hang up.
type watchStream struct {
	resp   *http.Response
	frames *bufio.Reader
	cancel context.CancelFunc
}

// openWatch connects, and fails the case if the connect itself was refused —
// the refusal legs open their streams with rawWatch instead.
func openWatch(t *testing.T, ts *testServer, path string, header http.Header) *watchStream {
	t.Helper()
	ws := rawWatch(t, ts, path, header)
	if ws.resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200: %s", path, ws.resp.StatusCode, readAll(t, ws.resp))
	}
	if got := ws.resp.Header.Get("Content-Type"); got != "text/event-stream; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/event-stream; charset=utf-8", got)
	}
	return ws
}

// rawWatch issues the request and returns whatever came back, streaming.
//
// The request carries its own generous deadline so that a stream this server
// forgets to end fails the case instead of hanging it, and its cancel is what
// stands in for a client hanging up.
func rawWatch(t *testing.T, ts *testServer, path string, header http.Header) *watchStream {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.base+path, nil)
	if err != nil {
		cancel()
		t.Fatalf("new request: %v", err)
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	// A client with no whole-response timeout: the response is the point and it
	// never completes on its own.
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		cancel()
		t.Fatalf("GET %s: %v", path, err)
	}
	t.Cleanup(func() {
		cancel()
		_ = resp.Body.Close()
	})
	return &watchStream{resp: resp, frames: bufio.NewReader(resp.Body), cancel: cancel}
}

// next reads one SSE frame: the lines up to the blank line that terminates it.
// The trailing newlines are stripped so a case asserts on the frame's content.
func (ws *watchStream) next(t *testing.T) string {
	t.Helper()
	var frame []string
	for {
		line, err := ws.frames.ReadString('\n')
		if err != nil {
			t.Fatalf("reading the stream after %q: %v", strings.Join(frame, "\\n"), err)
		}
		if line == "\n" {
			return strings.Join(frame, "\n")
		}
		frame = append(frame, strings.TrimSuffix(line, "\n"))
	}
}

// closed reports that the server ended the stream, which is what the truncated
// event promises and the only clean end this operation has.
func (ws *watchStream) closed(t *testing.T) {
	t.Helper()
	if _, err := ws.frames.ReadString('\n'); err == nil {
		t.Error("the stream is still open; a truncated event must be the last thing on it")
	}
}

// dataOf pulls the `data:` payload out of a frame, which is where every
// assertion about content lands.
func dataOf(t *testing.T, frame string) string {
	t.Helper()
	for _, line := range strings.Split(frame, "\n") {
		if rest, ok := strings.CutPrefix(line, "data: "); ok {
			return rest
		}
	}
	t.Fatalf("frame has no data line: %q", frame)
	return ""
}

// TestEventsWatchDeliversRecordsAsTheyLand is the operation's reason to exist,
// and the mutations happen AFTER the connect deliberately: a stream that only
// replayed what already existed would pass every assertion a poll read passes
// and none of the ones that matter.
func TestEventsWatchDeliversRecordsAsTheyLand(t *testing.T) {
	journal := &liveEventsJournal{}
	ts := watchServer(t, journal)

	ws := openWatch(t, ts, "/v0/beads/events:watch?since=0", nil)

	// The reconnection delay leads, before any record exists, so a client's
	// backoff is set even if the stream carries nothing for an hour.
	if got, want := ws.next(t), fmt.Sprintf("retry: %d", eventsWatchRetry); got != want {
		t.Fatalf("first frame = %q, want %q", got, want)
	}

	for i := range 3 {
		journal.commit(storage.EventsJournalRow{IssueID: fmt.Sprintf("bd-live-%d", i)})
	}

	for i := range 3 {
		frame := ws.next(t)
		wantID := fmt.Sprintf("id: %d", i+1)
		if !strings.HasPrefix(frame, wantID+"\n") {
			t.Fatalf("frame %d = %q, want it to lead with %q", i, frame, wantID)
		}
		// UNNAMED, so a bare onmessage receives it. The one named event on this
		// stream is the failure.
		if strings.Contains(frame, "event: ") {
			t.Errorf("record frame %d names an event type: %q", i, frame)
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(dataOf(t, frame)), &rec); err != nil {
			t.Fatalf("frame %d data is not JSON: %v", i, err)
		}
		// id AND seq, because the whole resume contract is that they are the
		// same number: the id is what a client sends back as Last-Event-ID.
		if seq, _ := rec["seq"].(float64); int(seq) != i+1 {
			t.Errorf("frame %d seq = %v, want %d", i, rec["seq"], i+1)
		}
		if got := rec["issue_id"]; got != fmt.Sprintf("bd-live-%d", i) {
			t.Errorf("frame %d issue_id = %v", i, got)
		}
		if _, ok := rec["issue"]; !ok {
			t.Errorf("frame %d has no `issue` member; the envelope is the paged read's", i)
		}
		if _, ok := rec["dep"]; ok {
			t.Errorf("frame %d carries `dep` on a create", i)
		}
	}
}

// TestEventsWatchFramesEveryRecordOnOneLine is the framing invariant, and it is
// asserted against payloads that would break it if the encoding were not what
// this handler claims.
//
// A raw newline inside a `data:` line splits one record into two frames, and a
// raw carriage return does the same (an SSE line ends at CR, LF or CRLF) — so a
// payload carrying either would either corrupt the stream or, worse, let a
// crafted issue title inject an `event:` line of its own. The defense is that
// records are encoding/json output and the encoder escapes every control
// character; this drives exactly that case rather than trusting it.
func TestEventsWatchFramesEveryRecordOnOneLine(t *testing.T) {
	journal := &liveEventsJournal{}
	ts := watchServer(t, journal)

	ws := openWatch(t, ts, "/v0/beads/events:watch?since=0", nil)
	ws.next(t) // retry

	// The payload members travel as raw JSON, so this is what a mutation on an
	// issue whose title contains a newline actually stores.
	hostile, err := json.Marshal(map[string]string{
		"id":    "bd-1",
		"title": "first\nsecond\rthird",
		"probe": "\n\ndata: {\"seq\":9999}\n\n",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	journal.commit(storage.EventsJournalRow{IssueID: "bd-1", IssueJSON: string(hostile)})
	journal.commit(storage.EventsJournalRow{IssueID: "bd-2"})

	frame := ws.next(t)
	lines := strings.Split(frame, "\n")
	if len(lines) != 2 {
		t.Fatalf("frame is %d lines, want exactly id: and data:\n%q", len(lines), frame)
	}
	var rec struct {
		Issue json.RawMessage `json:"issue"`
	}
	if err := json.Unmarshal([]byte(dataOf(t, frame)), &rec); err != nil {
		t.Fatalf("data is not JSON: %v", err)
	}
	// The payload survives byte for byte, which is the other half: escaping must
	// not renormalize what the mutation stored.
	if string(rec.Issue) != string(hostile) {
		t.Errorf("issue payload = %s, want it unchanged: %s", rec.Issue, hostile)
	}
	// And nothing the payload contained became a frame of its own: the very next
	// frame is the record that was committed after it, not the `data:` line the
	// payload spelled out.
	if got := ws.next(t); !strings.HasPrefix(got, "id: 2\n") {
		t.Errorf("next frame = %q, want the following record; the hostile payload leaked frames into the stream", got)
	}
}

// TestEventsWatchHeartbeatsWhileIdle. A stream with nothing to say still has to
// put bytes on the connection: an intermediary that drops an idle connection
// does it silently, and a client that stopped reading is only discovered by a
// write that fails.
func TestEventsWatchHeartbeatsWhileIdle(t *testing.T) {
	journal := &liveEventsJournal{}
	ts := watchServer(t, journal)

	ws := openWatch(t, ts, "/v0/beads/events:watch?since=0", nil)
	ws.next(t) // retry

	if got := ws.next(t); got != ": heartbeat" {
		t.Fatalf("idle frame = %q, want a heartbeat comment", got)
	}
	// A comment, so a consumer that dispatches on event type never sees it.
	if got := ws.next(t); !strings.HasPrefix(got, ":") {
		t.Errorf("second idle frame = %q, want another comment", got)
	}

	// And a record still arrives on a stream that has been heartbeating.
	journal.commit(storage.EventsJournalRow{IssueID: "bd-after-beat"})
	for {
		frame := ws.next(t)
		if strings.HasPrefix(frame, ":") {
			continue
		}
		if !strings.HasPrefix(frame, "id: 1\n") {
			t.Fatalf("frame after the heartbeats = %q, want the record", frame)
		}
		return
	}
}

// TestEventsWatchLastEventIDOutranksSince is the browser contract. An
// EventSource reconnects to the URL it was constructed with — carrying the
// ORIGINAL `since` — and attaches the id it actually reached. A server that
// preferred the parameter would re-deliver everything since the consumer
// started, on every reconnect, forever.
func TestEventsWatchLastEventIDOutranksSince(t *testing.T) {
	journal := &liveEventsJournal{}
	ts := watchServer(t, journal)
	for range 5 {
		journal.commit(storage.EventsJournalRow{})
	}

	ws := openWatch(t, ts, "/v0/beads/events:watch?since=0",
		http.Header{lastEventIDHeader: []string{"3"}})
	ws.next(t) // retry

	if got := ws.next(t); !strings.HasPrefix(got, "id: 4\n") {
		t.Fatalf("first frame = %q, want the record after the header's id", got)
	}
	if got := ws.next(t); !strings.HasPrefix(got, "id: 5\n") {
		t.Errorf("second frame = %q, want seq 5", got)
	}
}

// TestEventsWatchTreatsAnEmptyLastEventIDAsAbsent is the polarity that must NOT
// be a refusal, and it is a real client shape rather than a hypothetical: an
// intermediary or a hand-rolled client that always sets the header sends it
// empty on the first connect, when there is no id yet. Killing that connect
// would refuse the one request the header exists to make work, so an empty value
// means "no id" and `since` decides — the same thing the header being absent
// means, because it says the same thing.
func TestEventsWatchTreatsAnEmptyLastEventIDAsAbsent(t *testing.T) {
	journal := &liveEventsJournal{}
	ts := watchServer(t, journal)
	for range 3 {
		journal.commit(storage.EventsJournalRow{})
	}

	ws := openWatch(t, ts, "/v0/beads/events:watch?since=1",
		http.Header{lastEventIDHeader: []string{""}})
	ws.next(t) // retry

	if got := ws.next(t); !strings.HasPrefix(got, "id: 2\n") {
		t.Fatalf("first frame = %q, want the record after `since`; an empty header must not override it", got)
	}
}

// TestEventsWatchRefusesAnUnusableLastEventID. A client that invented its own
// id has a broken checkpoint; falling back to `since` would hand it a stream
// that looks correct and starts in the wrong place, and it would never find
// out.
func TestEventsWatchRefusesAnUnusableLastEventID(t *testing.T) {
	for _, tc := range []struct {
		name string
		id   string
	}{
		{name: "not a number", id: "abc"},
		{name: "negative", id: "-1"},
		{name: "fractional", id: "4.5"},
		{name: "a uuid, as a different producer would mint", id: "0f8fad5b-d9cb-469f-a165-70867728950e"},
		{name: "hexadecimal", id: "0x4"},
		// A seq is int64 in the document and in storage; a value past that range
		// is a broken checkpoint, not a very patient consumer.
		{name: "past int64", id: "99999999999999999999"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			journal := &liveEventsJournal{}
			ts := watchServer(t, journal)

			ws := rawWatch(t, ts, "/v0/beads/events:watch?since=0",
				http.Header{lastEventIDHeader: []string{tc.id}})
			if ws.resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", ws.resp.StatusCode)
			}
			body := decodeBody(t, ws.resp)
			if got := body["code"]; got != string(CodeInvalidArgument) {
				t.Errorf("code = %v, want %q", got, CodeInvalidArgument)
			}
			// `param` names the HEADER, because that is what the client has to
			// fix; naming `since` would send it after the wrong input.
			if got := body["param"]; got != lastEventIDHeader {
				t.Errorf("param = %v, want %q", got, lastEventIDHeader)
			}
			if n := journal.readCount(); n != 0 {
				t.Errorf("the journal was read %d times for a refused stream, want 0", n)
			}
		})
	}
}

// TestEventsWatchRefusesABadCheckpoint: `since` is required on every connect,
// including the reconnect that also carries a valid header. One rule with two
// spellings is the kind of thing a client discovers in production.
func TestEventsWatchRefusesABadCheckpoint(t *testing.T) {
	for _, tc := range []struct {
		name   string
		query  string
		header http.Header
	}{
		{name: "absent", query: ""},
		{name: "negative", query: "?since=-1"},
		{name: "not a number", query: "?since=abc"},
		{name: "unknown parameter", query: "?since=0&follow=true"},
		{
			name:   "absent even with a usable Last-Event-ID",
			query:  "",
			header: http.Header{lastEventIDHeader: []string{"7"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			journal := &liveEventsJournal{}
			ts := watchServer(t, journal)

			ws := rawWatch(t, ts, "/v0/beads/events:watch"+tc.query, tc.header)
			if ws.resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", ws.resp.StatusCode, readAll(t, ws.resp))
			}
			if got := decodeBody(t, ws.resp)["code"]; got != string(CodeInvalidArgument) {
				t.Errorf("code = %v, want %q", got, CodeInvalidArgument)
			}
		})
	}
}

// TestEventsWatchRefusesWhenTheJournalIsDisabled. A stream over a workspace
// that records nothing is a connection held open forever against a journal that
// will never emit, which is worse than the paged read's version of the same
// mistake rather than better.
func TestEventsWatchRefusesWhenTheJournalIsDisabled(t *testing.T) {
	journal := &liveEventsJournal{}
	ts := newTestServer(t, rolesConfig(Config{EventsJournal: journal, EventsJournalEnabled: false}))

	ws := rawWatch(t, ts, "/v0/beads/events:watch?since=0", nil)
	if ws.resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", ws.resp.StatusCode, readAll(t, ws.resp))
	}
	if got := decodeBody(t, ws.resp)["code"]; got != string(CodeEventsJournalDisabled) {
		t.Errorf("code = %v, want %q", got, CodeEventsJournalDisabled)
	}
	if n := journal.readCount(); n != 0 {
		t.Errorf("the journal was read %d times behind a disabled gate, want 0", n)
	}
}

// TestEventsWatchRefusesAStaleCheckpointBeforeOpening is the connect-time half
// of the truncation contract, and the point is that it is an ORDINARY 410: the
// status is only spendable before the first byte, so a stale cursor discovered
// here must never become a 200 that immediately reports its own failure.
func TestEventsWatchRefusesAStaleCheckpointBeforeOpening(t *testing.T) {
	journal := &liveEventsJournal{}
	for range 6 {
		journal.commit(storage.EventsJournalRow{})
	}
	journal.prune(4)
	ts := watchServer(t, journal)

	ws := rawWatch(t, ts, "/v0/beads/events:watch?since=0", nil)
	if ws.resp.StatusCode != http.StatusGone {
		t.Fatalf("status = %d, want 410: %s", ws.resp.StatusCode, readAll(t, ws.resp))
	}
	if got := ws.resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/problem+json") {
		t.Errorf("Content-Type = %q, want problem+json — no stream was opened", got)
	}
	body := decodeBody(t, ws.resp)
	if got := body["code"]; got != string(CodeEventsJournalTruncated) {
		t.Errorf("code = %v, want %q", got, CodeEventsJournalTruncated)
	}
	for _, tc := range []struct {
		member string
		want   float64
	}{{"since", 0}, {"floor", 4}, {"head", 6}} {
		if got := body[tc.member]; got != tc.want {
			t.Errorf("%s = %v, want %v", tc.member, got, tc.want)
		}
	}

	// The header's cursor is the one that gets checked, because it is the one
	// that would have been used.
	ws = rawWatch(t, ts, "/v0/beads/events:watch?since=5",
		http.Header{lastEventIDHeader: []string{"0"}})
	if ws.resp.StatusCode != http.StatusGone {
		t.Fatalf("with a stale Last-Event-ID and a live `since`, status = %d, want 410", ws.resp.StatusCode)
	}
}

// TestEventsWatchEndsWithATruncatedEventWhenAPruneRacesIt is the one failure
// this operation can report after its status is spent, and it is the case the
// whole in-band vocabulary exists for.
//
// The stream is already open and 200 when the records it was about to send are
// deleted. There is no status left to send, so a server that had nothing to say
// here would either stall silently or skip ahead to the new floor — the silent
// loss the 410 exists to prevent, wearing a 200.
func TestEventsWatchEndsWithATruncatedEventWhenAPruneRacesIt(t *testing.T) {
	journal := &liveEventsJournal{}
	journal.commit(storage.EventsJournalRow{})
	ts := watchServer(t, journal)

	ws := openWatch(t, ts, "/v0/beads/events:watch?since=0", nil)
	ws.next(t) // retry
	if got := ws.next(t); !strings.HasPrefix(got, "id: 1\n") {
		t.Fatalf("first record = %q", got)
	}

	// The prune lands under the open stream, cutting past where it sits.
	for range 5 {
		journal.commit(storage.EventsJournalRow{})
	}
	journal.prune(5)

	// THE DELAY IS RAISED FIRST. A consumer that ignores the event reconnects
	// into a connect-time 410 it can never satisfy, so the last instruction this
	// server gets to give is "come back slowly".
	if got, want := ws.next(t), fmt.Sprintf("retry: %d", eventsWatchTruncatedRetry); got != want {
		t.Fatalf("frame before the failure = %q, want %q", got, want)
	}

	frame := ws.next(t)
	if !strings.HasPrefix(frame, "event: truncated\n") {
		t.Fatalf("failure frame = %q, want a named truncated event", frame)
	}
	var problem map[string]any
	if err := json.Unmarshal([]byte(dataOf(t, frame)), &problem); err != nil {
		t.Fatalf("truncated data is not JSON: %v", err)
	}
	// THE SAME BODY THE 410 CARRIES. A consumer meets this condition on both
	// surfaces, and a second encoding of the same three numbers would be a
	// second contract to keep in step.
	if got := problem["code"]; got != string(CodeEventsJournalTruncated) {
		t.Errorf("code = %v, want %q", got, CodeEventsJournalTruncated)
	}
	if got := problem["status"]; got != float64(http.StatusGone) {
		t.Errorf("status = %v, want 410 — the event carries the refusal it would have been", got)
	}
	for _, tc := range []struct {
		member string
		want   float64
	}{{"since", 1}, {"floor", 5}, {"head", 6}} {
		if got := problem[tc.member]; got != tc.want {
			t.Errorf("%s = %v, want %v", tc.member, got, tc.want)
		}
	}
	if id, _ := problem["request_id"].(string); id == "" {
		t.Error("no request_id on the truncated event; it is the only handle on this stream's log line")
	}

	ws.closed(t)
}

// TestEventsWatchTruncatedEventCarriesTheGoneBodyExactly makes the sentence
// above a machine fact rather than a set of assertions that happen to agree
// with it: the event's data and the 410's body are one encoding of one Result.
func TestEventsWatchTruncatedEventCarriesTheGoneBodyExactly(t *testing.T) {
	err := &storage.EventsJournalTruncatedError{Since: 2, Floor: 9, Head: 41}
	rec := &reqInfo{id: "req-1"}

	inBand := truncatedFrame(rec, err)

	overHTTP := httptest.NewRecorder()
	Write(overHTTP, EventsJournalTruncated(err).WithRequestID(rec.id))

	// Write's encoder appends the newline that makes a body a line; the SSE
	// frame cannot carry one, and that is the ONLY difference allowed.
	if got, want := string(inBand)+"\n", overHTTP.Body.String(); got != want {
		t.Errorf("the truncated event and the 410 have drifted\n in band: %s\nover HTTP: %s", got, want)
	}
}

// TestEventsWatchReleasesItsSlotOnEveryExit. The cap is only a cap if the
// counter comes back down; a decrement that lived on the happy path would leave
// a server that has been refusing streams since its last disconnect, and the
// only symptom is a 503 nobody can explain.
func TestEventsWatchReleasesItsSlotOnEveryExit(t *testing.T) {
	journal := &liveEventsJournal{}
	ts := watchServer(t, journal)

	live := func() int64 { return ts.Server.watchStreams.Load() }

	// A stream the CLIENT ends.
	ws := openWatch(t, ts, "/v0/beads/events:watch?since=0", nil)
	ws.next(t)
	if got := live(); got != 1 {
		t.Fatalf("open streams = %d, want 1", got)
	}
	ws.cancel()
	waitFor(t, "the disconnected stream to give its slot back", func() bool { return live() == 0 })

	// A stream the SERVER ends, on the truncated path.
	journal.commit(storage.EventsJournalRow{})
	ws = openWatch(t, ts, "/v0/beads/events:watch?since=0", nil)
	ws.next(t)
	ws.next(t)
	journal.prune(9)
	waitFor(t, "the truncated stream to give its slot back", func() bool { return live() == 0 })

	// A stream REFUSED before it opened must not have charged the cap at all.
	rawWatch(t, ts, "/v0/beads/events:watch?since=-1", nil)
	if got := live(); got != 0 {
		t.Errorf("open streams after a refused connect = %d, want 0", got)
	}
}

// TestEventsWatchRefusesBeyondTheStreamCap. Streams are the only requests here
// that last hours, so the limit on how many this process will hold is the one
// bound that cannot be a deadline — and the refusal has to name a recovery,
// because "retry" is the wrong advice for a resource that frees on a human
// timescale.
func TestEventsWatchRefusesBeyondTheStreamCap(t *testing.T) {
	journal := &liveEventsJournal{}
	ts := watchServer(t, journal, func(s *Server) { s.maxWatchStreams = 2 })

	for i := range 2 {
		ws := openWatch(t, ts, "/v0/beads/events:watch?since=0", nil)
		if got := ws.next(t); !strings.HasPrefix(got, "retry: ") {
			t.Fatalf("stream %d did not open: %q", i, got)
		}
	}

	refused := rawWatch(t, ts, "/v0/beads/events:watch?since=0", nil)
	if refused.resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status past the cap = %d, want 503: %s", refused.resp.StatusCode, readAll(t, refused.resp))
	}
	if got := refused.resp.Header.Get("Retry-After"); got == "" {
		t.Error("no Retry-After on a saturated stream cap")
	}
	body := decodeBody(t, refused.resp)
	if got := body["code"]; got != string(CodeEventsWatchSaturated) {
		t.Errorf("code = %v, want %q", got, CodeEventsWatchSaturated)
	}
	// The paged read is the documented way out, and it must keep working while
	// every stream slot is taken.
	if detail, _ := body["detail"].(string); !strings.Contains(detail, "/v0/beads/events") {
		t.Errorf("detail = %q; it must name the read that is not capped this way", detail)
	}
	if resp := ts.get(t, "/v0/beads/events?since=0"); resp.StatusCode != http.StatusOK {
		t.Errorf("the paged read = %d while the stream cap is full, want 200", resp.StatusCode)
	}
}

// TestEventsWatchHoldsNoDatabaseSlotBetweenReads is the design this operation
// could most plausibly have got wrong. There are sixteen slots and sixty-four
// stream places; a stream that held one for its life would let a handful of
// idle consumers starve every other operation on the server for hours, with
// /healthz green throughout.
//
// One slot exists here, and it is held by a stream. Every other operation must
// still answer.
func TestEventsWatchHoldsNoDatabaseSlotBetweenReads(t *testing.T) {
	journal := &liveEventsJournal{}
	ts := watchServer(t, journal, func(s *Server) {
		s.sem = make(chan struct{}, 1)
		s.watchPoll = 20 * time.Millisecond
	})

	ws := openWatch(t, ts, "/v0/beads/events:watch?since=0", nil)
	ws.next(t)
	journal.commit(storage.EventsJournalRow{})
	if got := ws.next(t); !strings.HasPrefix(got, "id: 1\n") {
		t.Fatalf("the stream is not reading: %q", got)
	}

	// The paged read needs the same single slot the stream keeps taking and
	// giving back.
	if resp := ts.get(t, "/v0/beads/events?since=0"); resp.StatusCode != http.StatusOK {
		t.Fatalf("a paged read alongside one open stream = %d, want 200: %s",
			resp.StatusCode, readAll(t, resp))
	}
	// And the stream is still live afterwards, so the two are sharing rather
	// than one having displaced the other.
	journal.commit(storage.EventsJournalRow{})
	if got := ws.next(t); !strings.HasPrefix(got, "id: 2\n") {
		t.Errorf("the stream stopped delivering after a competing read: %q", got)
	}
}

// TestTheStreamCapLeavesConnectionHeadroom is the arithmetic that makes the
// 503 deliverable at all, and it is not obvious from either constant alone.
//
// netutil.LimitListener returns a connection slot only when the connection
// closes — after the handler returns, which is after the stream counter has
// already come down. So a stream cap AT the connection cap can never be
// observed: the connect that would have earned the 503 is never accepted, and
// neither is the paged read the refusal points at, nor a mutation, nor a
// fresh-connection /healthz. They all park in the kernel accept backlog while
// the code, the runbook and the user docs promise a status and a retry hint.
//
// The relationship is what is pinned, not the number.
func TestTheStreamCapLeavesConnectionHeadroom(t *testing.T) {
	if maxWatchStreams >= maxConns {
		t.Fatalf("maxWatchStreams = %d with maxConns = %d: the stream cap is unreachable, so its 503 can never be delivered and the poll fallback it names cannot be connected to either",
			maxWatchStreams, maxConns)
	}
	if headroom := maxConns - maxWatchStreams; headroom < maxInflight {
		t.Errorf("stream cap leaves %d connections of headroom, want at least maxInflight (%d) — enough for a full complement of ordinary requests to keep answering while every stream slot is taken",
			headroom, maxInflight)
	}
}

// TestEventsWatchStopsDrainingABacklogWhenAskedTo covers the exit that the
// sleeping branch does not: a stream draining full batches never waits, so a
// loop that consulted the client and the shutdown signal only where it sleeps
// would ignore both for the whole backlog — writing records to a client that
// hung up long ago, and holding a graceful shutdown past its drain budget.
//
// Both stops are asserted the same way and it is deliberately not "the stream
// ended": it is "the stream ended WITHIN A BATCH OR TWO of being told to". A
// backlog ends on its own eventually, so only the count separates a loop that
// listened from one that ran out of records.
func TestEventsWatchStopsDrainingABacklogWhenAskedTo(t *testing.T) {
	for _, tc := range []struct {
		name string
		stop func(s *Server, hangUp context.CancelFunc)
	}{
		{name: "the client hangs up", stop: func(_ *Server, hangUp context.CancelFunc) { hangUp() }},
		{name: "the server shuts down", stop: func(s *Server, _ context.CancelFunc) { s.closeStreams() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			journal := &endlessEventsJournal{}
			s := &Server{
				sem:        make(chan struct{}, 1),
				semTimeout: 5 * time.Second,
				closing:    make(chan struct{}),
				log:        newTestLogger(&lockedBuffer{}),
			}

			ctx, hangUp := context.WithCancel(context.Background())
			defer hangUp()
			req := httptest.NewRequest(http.MethodGet, "/v0/beads/events:watch?since=0", nil).WithContext(ctx)
			first, err := s.readWatchBatch(ctx, &reqInfo{}, journal, 0)
			if err != nil {
				t.Fatalf("connect read: %v", err)
			}

			done := make(chan struct{})
			go func() {
				defer close(done)
				s.streamEvents(&discardWriter{}, req, journal, 0, first)
			}()

			waitFor(t, "the stream to start draining the backlog", func() bool { return journal.readCount() >= 3 })
			before := journal.readCount()
			tc.stop(s, hangUp)

			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("the stream is still draining; it never looked at the exit")
			}
			if drained := journal.readCount() - before; drained > 2 {
				t.Errorf("the stream drained %d more batches after being told to stop, want at most 2 — it is only checking the exit where it sleeps", drained)
			}
		})
	}
}

// TestWatchPollDelaySpreadsTheInterval. Streams synchronize by themselves and
// nothing random breaks the tie: consumers reconnecting after a restart are all
// handed the same `retry`, so they come back together and, on a fixed interval,
// read together for the life of the process — one burst of up to
// maxWatchStreams reads per second against a semaphore that admits sixteen.
func TestWatchPollDelaySpreadsTheInterval(t *testing.T) {
	const interval = time.Second
	lo, hi := interval-interval/10, interval+interval/10

	seen := map[time.Duration]bool{}
	for range 200 {
		got := watchPollDelay(interval)
		if got < lo || got > hi {
			t.Fatalf("watchPollDelay(%s) = %s, want within ±10%%", interval, got)
		}
		seen[got] = true
	}
	if len(seen) < 50 {
		t.Errorf("200 draws produced %d distinct delays; streams would still read in lockstep", len(seen))
	}

	// A degenerate interval must not come back as a zero wait: that would turn
	// the loop into a spin against the database rather than a poll.
	for _, interval := range []time.Duration{time.Nanosecond, time.Millisecond, 5 * time.Millisecond} {
		if got := watchPollDelay(interval); got <= 0 {
			t.Errorf("watchPollDelay(%s) = %s; a non-positive wait is a spin", interval, got)
		}
	}
}

// TestEventsWatchClosesOnServerShutdown. http.Server.Shutdown waits for active
// requests without canceling their contexts, so a stream that watched only for
// its client would hold every graceful shutdown open for the whole drain
// timeout and then be killed anyway — turning "clean exit" into a twenty-second
// pause and a forced-close line in the log.
func TestEventsWatchClosesOnServerShutdown(t *testing.T) {
	journal := &liveEventsJournal{}
	stdout := &lockedBuffer{}
	stderr := &lockedBuffer{}
	srv, err := Listen(rolesConfig(Config{
		Addr: "127.0.0.1:0", EventsJournal: journal, EventsJournalEnabled: true,
		Stdout: stdout, Stderr: stderr,
	}))
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	srv.watchPoll = 5 * time.Millisecond
	srv.watchBeat = time.Hour

	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ctx) }()

	base := "http://" + srv.Addr()
	req, err := http.NewRequest(http.MethodGet, base+"/v0/beads/events:watch?since=0", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	frames := bufio.NewReader(resp.Body)
	if _, err := frames.ReadString('\n'); err != nil {
		t.Fatalf("stream did not open: %v", err)
	}

	cancel()
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return; the open stream is holding the drain")
	}

	// The drain completed rather than timing out, which is the whole assertion:
	// drainTimeout is twenty seconds and this returned in under ten.
	if log := stderr.String(); strings.Contains(log, "event=shutdown_forced") {
		t.Errorf("the shutdown was forced, so the stream did not wind up on its own:\n%s", log)
	}
	if log := stderr.String(); !strings.Contains(log, "event=shutdown_complete") {
		t.Errorf("no clean shutdown recorded:\n%s", log)
	}
}

// unflushableWriter is a ResponseWriter with no way to push bytes before the
// handler returns, and no Unwrap to a writer that has one.
type unflushableWriter struct{ rec *httptest.ResponseRecorder }

func (u unflushableWriter) Header() http.Header         { return u.rec.Header() }
func (u unflushableWriter) Write(b []byte) (int, error) { return u.rec.Write(b) }
func (u unflushableWriter) WriteHeader(status int)      { u.rec.WriteHeader(status) }

// TestEventsWatchRefusesAWriterItCannotFlush. Without a flush every record sits
// in net/http's buffer until it fills or the handler returns, and for this
// operation the handler never returns — so the stream would look healthy from
// both ends and deliver nothing. Refusing before the status is spent is what
// makes that a startup-shaped failure instead of a silent one.
func TestEventsWatchRefusesAWriterItCannotFlush(t *testing.T) {
	journal := &liveEventsJournal{}
	ts := watchServer(t, journal)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v0/beads/events:watch?since=0", nil)
	ts.Server.handleWatchEvents(unflushableWriter{rec: rec}, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/problem+json") {
		t.Errorf("Content-Type = %q, want problem+json", got)
	}
	if n := journal.readCount(); n != 0 {
		t.Errorf("the journal was read %d times for a stream that could not be written, want 0", n)
	}
	if got := ts.Server.watchStreams.Load(); got != 0 {
		t.Errorf("open streams = %d after an unwritable connect, want 0", got)
	}
	// httptest.ResponseRecorder DOES flush, so the check has to be able to see
	// the difference — otherwise this case would pass against a handler that
	// never looked.
	if !canFlush(httptest.NewRecorder()) {
		t.Error("canFlush rejects a recorder; the refusal above proves nothing")
	}
}

// deadlineWriter records the read deadlines a handler sets, and flushes, so the
// stream runs normally around it.
type deadlineWriter struct {
	*httptest.ResponseRecorder
	mu        sync.Mutex
	deadlines []time.Time
}

func (d *deadlineWriter) SetReadDeadline(t time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.deadlines = append(d.deadlines, t)
	return nil
}

func (d *deadlineWriter) cleared() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, deadline := range d.deadlines {
		if deadline.IsZero() {
			return true
		}
	}
	return false
}

// deadlineProbeJournal records, for every read it serves, whether the request's
// read deadline had already been cleared by then. It is how the ORDER of two
// things in one handler becomes an assertion.
type deadlineProbeJournal struct {
	w *deadlineWriter

	mu      sync.Mutex
	cleared []bool
}

func (j *deadlineProbeJournal) ReadEventsJournalPage(_ context.Context, _ int64, _ int) (storage.EventsJournalPage, error) {
	j.mu.Lock()
	j.cleared = append(j.cleared, j.w.cleared())
	j.mu.Unlock()
	return storage.EventsJournalPage{}, nil
}

func (j *deadlineProbeJournal) clearedAtFirstRead() (bool, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if len(j.cleared) == 0 {
		return false, false
	}
	return j.cleared[0], true
}

// TestEventsWatchClearsTheRequestReadDeadline is the trap that kills SSE on this
// server, and it is not the one people look for.
//
// There is no WriteTimeout here — statusWriter rolls a per-write deadline
// instead, which bounds one stalled write rather than a stream. But
// http.Server.ReadTimeout is a deadline on the WHOLE request, and net/http keeps
// a background read running on the connection while the handler writes: the read
// that notices a disconnect. When that read hits the deadline, net/http treats
// it as a dead connection and cancels the request context — so without this an
// idle stream would end itself after readTimeout with no client involved and
// nothing in the log to say why.
//
// A wall-clock test would have to wait thirty seconds to see it, so this pins
// the mechanism: the handler clears the deadline for its own request.
//
// WHEN it clears it is the second half, and it is not cosmetic. The connect read
// happens before any stream byte, and it is the last thing that runs under the
// inherited ReadTimeout: a database wedged at that moment would have the
// deadline expire mid-read, which net/http reports by canceling the request
// context — so the wedge would be booked as `client_closed` and an operator
// would go looking for a client that never left. The probe journal reports
// whether the deadline was already gone when the first read reached it.
func TestEventsWatchClearsTheRequestReadDeadline(t *testing.T) {
	w := &deadlineWriter{ResponseRecorder: httptest.NewRecorder()}
	journal := &deadlineProbeJournal{w: w}
	ts := watchServer(t, journal)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/v0/beads/events:watch?since=0", nil).WithContext(ctx)

	done := make(chan struct{})
	go func() {
		defer close(done)
		ts.Server.handleWatchEvents(w, req)
	}()

	waitFor(t, "the stream to clear its read deadline", w.cleared)
	waitFor(t, "the stream to read the journal", func() bool { _, read := journal.clearedAtFirstRead(); return read })
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler did not return after the client went away")
	}

	cleared, _ := journal.clearedAtFirstRead()
	if !cleared {
		t.Error("the connect read ran while the request still carried http.Server.ReadTimeout; a database wedge there is reported as a client disconnect")
	}
	if readTimeout <= 0 {
		t.Fatal("readTimeout is unset, so this handler no longer needs to clear it — delete the clear and this case together")
	}
}

// TestStreamingRoutesCarryNoRequestDeadline is the other half of the timeout
// story, and the half no behavioral test can reach: requestDeadline is sixty
// seconds, so a stream cut off by it would need a sixty-second case to notice.
//
// It drives the lifecycle wrapper directly, with both polarities, because the
// absence of a deadline is only meaningful against a row that has one.
func TestStreamingRoutesCarryNoRequestDeadline(t *testing.T) {
	s := &Server{sem: make(chan struct{}, 1), log: newTestLogger(&lockedBuffer{})}

	deadlineFor := func(rt route) bool {
		var seen bool
		rt.handler = func(_ *Server, _ http.ResponseWriter, r *http.Request) {
			_, seen = r.Context().Deadline()
		}
		s.route(rt).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
		return seen
	}

	if deadlineFor(route{op: "stream", streaming: true, bypassSemaphore: true}) {
		t.Error("a streaming row's handler runs under the 60s request deadline; the stream would be severed by it")
	}
	if !deadlineFor(route{op: "plain"}) {
		t.Error("an ordinary row's handler runs with no deadline; the backstop is gone for every operation, not just the stream")
	}
}

// waitFor polls cond until it holds, and fails the case rather than hanging
// when it does not. Everything it waits on here happens in milliseconds; the
// budget is for a loaded machine, not for the behavior.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
