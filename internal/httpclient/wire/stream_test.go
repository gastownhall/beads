// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/wire/stream_test.go@49d1df2f6)
// to OSS beads under the MIT license.
package wire

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The streaming door's branch matrix, in the same httptest idiom the rest of
// this package uses: canned SSE bytes, no bd serve, no Dolt. The connect half
// reuses roundTrip's own machinery, so what is genuinely new here is the SSE
// parser and the idle deadline — and both are exercised against frames a real
// server writes (internal/httpapi/events_watch.go) and against the malformed
// ones only a doctored server can send.

// writeSSEHeader opens a text/event-stream response and returns its flusher.
func writeSSEHeader(t *testing.T, w http.ResponseWriter) http.Flusher {
	t.Helper()
	fl, ok := w.(http.Flusher)
	if !ok {
		t.Fatal("the test server's ResponseWriter cannot flush; a stream cannot be tested")
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fl.Flush()
	return fl
}

func writeSSE(t *testing.T, w http.ResponseWriter, body string) {
	t.Helper()
	fl := writeSSEHeader(t, w)
	_, _ = io.WriteString(w, body)
	fl.Flush()
}

func TestWatchParsesRecordsMultiLineDataAndComments(t *testing.T) {
	// One connection carrying every frame shape the server emits: the opening
	// retry advisory, a heartbeat comment, a record, and a record whose payload
	// arrives across two data lines. The parser has to skip the first two, honor
	// the retry, and join the multi-line data with a single newline.
	c, _ := newTestClient(t, Options{}, nil, func(w http.ResponseWriter, _ *http.Request) {
		writeSSE(t, w,
			"retry: 3000\n\n"+
				": heartbeat\n\n"+
				"id: 5\ndata: {\"seq\":5}\n\n"+
				"id: 6\ndata: line1\ndata: line2\n\n")
	})

	stream, err := c.WatchEvents(ctx(t), 0)
	if err != nil {
		t.Fatalf("WatchEvents: %v", err)
	}
	defer stream.Close()

	first, err := stream.Next()
	if err != nil {
		t.Fatalf("first Next: %v", err)
	}
	if first.Seq != 5 || string(first.Data) != `{"seq":5}` {
		t.Errorf("first event = {seq:%d data:%q}, want {5 {\"seq\":5}}", first.Seq, first.Data)
	}
	if first.Retry != 3*time.Second {
		t.Errorf("retry = %s, want the advertised 3s: a `retry:` frame carries the reconnection delay", first.Retry)
	}

	second, err := stream.Next()
	if err != nil {
		t.Fatalf("second Next: %v", err)
	}
	if second.Seq != 6 || string(second.Data) != "line1\nline2" {
		t.Errorf("second event = {seq:%d data:%q}, want {6 line1\\nline2}: multi-line data joins with a newline", second.Seq, second.Data)
	}

	if _, err := stream.Next(); err == nil {
		t.Error("a closed stream's Next returned no error")
	}
}

func TestWatchIdleTimeoutFiresAfterThreeHeartbeatsOfSilence(t *testing.T) {
	restore := watchIdleTimeout
	watchIdleTimeout = 50 * time.Millisecond
	t.Cleanup(func() { watchIdleTimeout = restore })

	c, _ := newTestClient(t, Options{}, nil, func(w http.ResponseWriter, r *http.Request) {
		writeSSE(t, w, "retry: 3000\n\n")
		// Go silent: no records, no heartbeats. A half-dead NAT looks exactly like
		// this. Hold the connection until the client's idle timer cancels it.
		<-r.Context().Done()
	})

	stream, err := c.WatchEvents(ctx(t), 0)
	if err != nil {
		t.Fatalf("WatchEvents: %v", err)
	}
	defer stream.Close()

	done := make(chan error, 1)
	go func() {
		_, nerr := stream.Next()
		done <- nerr
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrStreamIdle) {
			t.Errorf("a silent stream's Next = %v, want ErrStreamIdle", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Next did not return on a silent stream; the idle deadline never fired")
	}
}

func TestWatchIdleDeadlineIsInterByteNotPerLine(t *testing.T) {
	restore := watchIdleTimeout
	watchIdleTimeout = 300 * time.Millisecond
	t.Cleanup(func() { watchIdleTimeout = restore })

	// One large single data line, delivered in fragments each well within the idle
	// window but whose TOTAL transfer exceeds it. A per-line transfer deadline
	// would kill this actively-moving stream at 300ms; an inter-byte idle deadline,
	// re-armed on every fragment that made byte progress, must let it complete.
	// The chunk is larger than the parser's read buffer so each one forces an
	// ErrBufferFull fragment — the exact point the deadline has to re-arm.
	const chunks = 8
	const chunkSize = 8192
	c, _ := newTestClient(t, Options{}, nil, func(w http.ResponseWriter, _ *http.Request) {
		fl := writeSSEHeader(t, w)
		_, _ = io.WriteString(w, "id: 1\ndata: ")
		fl.Flush()
		payload := strings.Repeat("x", chunkSize)
		for i := 0; i < chunks; i++ {
			time.Sleep(50 * time.Millisecond)
			_, _ = io.WriteString(w, payload)
			fl.Flush()
		}
		_, _ = io.WriteString(w, "\n\n")
		fl.Flush()
	})

	stream, err := c.WatchEvents(ctx(t), 0)
	if err != nil {
		t.Fatalf("WatchEvents: %v", err)
	}
	defer stream.Close()

	type result struct {
		ev  StreamEvent
		err error
	}
	done := make(chan result, 1)
	go func() {
		ev, nerr := stream.Next()
		done <- result{ev, nerr}
	}()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("a slow-but-progressing large line = %v, want it delivered: the idle deadline must be inter-byte, not a per-line transfer budget", got.err)
		}
		if want := chunks * chunkSize; len(got.ev.Data) != want {
			t.Errorf("delivered %d data bytes, want %d: the whole progressing line", len(got.ev.Data), want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Next never returned on a progressing stream")
	}
}

func TestStreamCloseIsIdempotentUnderConcurrency(t *testing.T) {
	c, _ := newTestClient(t, Options{}, nil, func(w http.ResponseWriter, r *http.Request) {
		writeSSE(t, w, "id: 1\ndata: {\"seq\":1}\n\n")
		// Hold the connection open so Close has a live body to close and a parked
		// request to cancel; it unblocks when the client's Close cancels the request.
		<-r.Context().Done()
	})
	stream, err := c.WatchEvents(ctx(t), 0)
	if err != nil {
		t.Fatalf("WatchEvents: %v", err)
	}

	// Race many concurrent Close calls. The CAS one-shot must let exactly one win
	// the cancel-and-close and no-op the rest, with no data race on the closed flag
	// — the whole point of moving it off a plain bool. Run under -race to catch a
	// regression.
	const n = 16
	var wg sync.WaitGroup
	wg.Add(n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			<-start
			stream.Close()
		}()
	}
	close(start)
	wg.Wait()

	// A further Close after they all return is still safe and still a no-op.
	stream.Close()
}

func TestWatchRefreshesTheCredentialOnceOnA401(t *testing.T) {
	creds := &staticToken{tokens: []string{"stale", "fresh"}, retry: true}
	var connects atomic.Int64
	c, _ := newTestClient(t, Options{}, creds, func(w http.ResponseWriter, r *http.Request) {
		n := connects.Add(1)
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if n == 1 {
			if token != "stale" {
				t.Errorf("first connect carried token %q, want the stale one", token)
			}
			problemJSON(w, http.StatusUnauthorized, `{"status":401,"code":"unauthenticated"}`)
			return
		}
		if token != "fresh" {
			t.Errorf("the retry carried token %q, want the refreshed one", token)
		}
		writeSSE(t, w, "id: 1\ndata: {\"seq\":1}\n\n")
	})

	stream, err := c.WatchEvents(ctx(t), 0)
	if err != nil {
		t.Fatalf("WatchEvents after a 401 refresh: %v", err)
	}
	defer stream.Close()

	ev, err := stream.Next()
	if err != nil {
		t.Fatalf("Next after the refreshed reconnect: %v", err)
	}
	if ev.Seq != 1 {
		t.Errorf("delivered seq %d, want 1", ev.Seq)
	}
	if creds.refreshN != 1 {
		t.Errorf("refreshed %d times, want exactly one", creds.refreshN)
	}
	if got := connects.Load(); got != 2 {
		t.Errorf("made %d connects, want 2 (the 401 and the refreshed retry)", got)
	}
}

func TestWatchDoesNotRefreshTwiceOnAPersistent401(t *testing.T) {
	creds := &staticToken{tokens: []string{"a", "b"}, retry: true}
	var connects atomic.Int64
	c, _ := newTestClient(t, Options{}, creds, func(w http.ResponseWriter, _ *http.Request) {
		connects.Add(1)
		problemJSON(w, http.StatusUnauthorized, `{"status":401,"code":"unauthenticated"}`)
	})

	_, err := c.WatchEvents(ctx(t), 0)
	if !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("a persistent 401 = %v, want ErrUnauthenticated", err)
	}
	if got := connects.Load(); got != 2 {
		t.Errorf("made %d connects, want 2: the refresh is once and only once", got)
	}
}

func TestWatchRefusesARedirect(t *testing.T) {
	c, _ := newTestClient(t, Options{}, nil, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "https://elsewhere.example/login")
		w.WriteHeader(http.StatusFound)
	})
	_, err := c.WatchEvents(ctx(t), 0)
	if !errors.Is(err, ErrRedirected) {
		t.Fatalf("a redirected watch = %v, want ErrRedirected: a bd serve does not redirect", err)
	}
}

func TestWatchStampsTheProjectIDOnTheStreamRequest(t *testing.T) {
	c, rec := newTestClient(t, Options{ExpectProjectID: "proj-1"}, nil, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get(ProjectIDHeader); got != "proj-1" {
			t.Errorf("stream request carried %s=%q, want proj-1: events:watch is project-stamp-ENFORCED", ProjectIDHeader, got)
		}
		writeSSE(t, w, "id: 1\ndata: {}\n\n")
	})
	stream, err := c.WatchEvents(ctx(t), 0)
	if err != nil {
		t.Fatalf("WatchEvents: %v", err)
	}
	defer stream.Close()
	if got := rec.at(t, 0).header.Get(ProjectIDHeader); got != "proj-1" {
		t.Errorf("the recorded stream request carried %s=%q, want proj-1", ProjectIDHeader, got)
	}
	// And the required since parameter rides every connect.
	if q := rec.at(t, 0).rawQuery; !strings.Contains(q, "since=0") {
		t.Errorf("the stream request query was %q, want it to carry since=0", q)
	}
}

func TestWatchConnectRefusalsMapToTheThreeSentinels(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"disabled", http.StatusConflict, `{"status":409,"code":"events_journal_disabled"}`, ErrEventsJournalDisabled},
		{"truncated", http.StatusGone, `{"status":410,"code":"events_journal_truncated","since":40,"floor":51,"head":99}`, ErrEventsJournalTruncated},
		{"saturated", http.StatusServiceUnavailable, `{"status":503,"code":"events_watch_saturated"}`, ErrEventsWatchSaturated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newTestClient(t, Options{}, nil, func(w http.ResponseWriter, _ *http.Request) {
				problemJSON(w, tc.status, tc.body)
			})
			_, err := c.WatchEvents(ctx(t), 0)
			if !errors.Is(err, tc.want) {
				t.Fatalf("connect %d = %v, want %v", tc.status, err, tc.want)
			}
			if tc.want == ErrEventsJournalTruncated {
				var prob *ProblemError
				if !errors.As(err, &prob) {
					t.Fatalf("a 410 = %T, want *ProblemError carrying the window", err)
				}
				if prob.Since == nil || prob.Floor == nil || prob.Head == nil {
					t.Errorf("the truncation carried window {since:%v floor:%v head:%v}, want all three", prob.Since, prob.Floor, prob.Head)
				}
			}
		})
	}
}

func TestWatchTruncatedEventSurfacesItsNameAndProblem(t *testing.T) {
	problem := `{"code":"events_journal_truncated","since":40,"floor":51,"head":99}`
	c, _ := newTestClient(t, Options{}, nil, func(w http.ResponseWriter, _ *http.Request) {
		writeSSE(t, w, "retry: 60000\n\nevent: truncated\ndata: "+problem+"\n\n")
	})
	stream, err := c.WatchEvents(ctx(t), 0)
	if err != nil {
		t.Fatalf("WatchEvents: %v", err)
	}
	defer stream.Close()

	ev, err := stream.Next()
	if err != nil {
		t.Fatalf("Next for the truncated event: %v", err)
	}
	if ev.Name != truncatedEventName {
		t.Errorf("event name = %q, want %q: the one named event on this stream", ev.Name, truncatedEventName)
	}
	if ev.Retry != 60*time.Second {
		t.Errorf("retry = %s, want the 60s the server raises in front of a truncation", ev.Retry)
	}
	var decoded struct {
		Since, Floor, Head int64
	}
	if err := json.Unmarshal(ev.Data, &decoded); err != nil {
		t.Fatalf("the truncated event's data did not parse as a problem: %v", err)
	}
	if decoded.Since != 40 || decoded.Floor != 51 || decoded.Head != 99 {
		t.Errorf("window = {%d %d %d}, want {40 51 99}", decoded.Since, decoded.Floor, decoded.Head)
	}
}

func TestWatchRefusesAnOversizedEvent(t *testing.T) {
	c, _ := newTestClient(t, Options{MaxResponseBytes: 64}, nil, func(w http.ResponseWriter, _ *http.Request) {
		writeSSE(t, w, "id: 1\ndata: "+strings.Repeat("x", 200)+"\n\n")
	})
	stream, err := c.WatchEvents(ctx(t), 0)
	if err != nil {
		t.Fatalf("WatchEvents: %v", err)
	}
	defer stream.Close()
	if _, err := stream.Next(); !errors.Is(err, ErrResponseTooLarge) {
		t.Errorf("an event past the per-event cap = %v, want ErrResponseTooLarge", err)
	}
}

func TestWatchRefusesANonEventStreamAnswer(t *testing.T) {
	c, _ := newTestClient(t, Options{}, nil, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	})
	_, err := c.WatchEvents(ctx(t), 0)
	if !errors.Is(err, ErrNotEventStream) {
		t.Fatalf("a 2xx that is not a stream = %v, want ErrNotEventStream", err)
	}
}
