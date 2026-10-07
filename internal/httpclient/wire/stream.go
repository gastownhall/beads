// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/wire/stream.go@49d1df2f6)
// to OSS beads under the MIT license.
package wire

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"sync/atomic"
	"time"
)

// The STREAMING door, the one operation on this surface that cannot go through
// Do.
//
// Do reads the whole body under a per-request timeout so the 401 retry can
// replay a []byte, and both are exactly wrong for a stream: GET
// /v0/beads/events:watch holds a text/event-stream open for as long as the
// consumer wants it, so there is no whole body to read and no fixed timeout to
// read it under. This file is the second door — its own http client with no
// timeout, an SSE parser over the live body, and an idle deadline standing in
// for the timeout Do relies on.
//
// THE CONNECT HALF IS Do's, though, and deliberately so. Everything up to the
// 200 and its text/event-stream header — the 400s, the 409, the 410, the 503 —
// is an ordinary problem response the server sends BEFORE a single stream byte
// (internal/httpapi/events_watch.go), so this door reuses roundTrip's own
// pieces: the shared request stamp, redirectRefused, the bounded error-body
// read, mapProblem. Only once the stream is open does the vocabulary become
// SSE.

// watchIdleTimeout is how long a stream may go silent before this client
// declares it dead and fails loudly.
//
// The server heartbeats a comment line every ~20s (internal/httpapi's
// watchHeartbeat) precisely so an idle connection through a NAT or a proxy keeps
// producing bytes; three heartbeats of total silence is a connection that has
// half-died without either end being told — the classic way a stream stops
// delivering while both sides believe it is up. Sixty seconds is long enough
// that one missed heartbeat is not mistaken for a dead peer and short enough
// that a consumer's stall is measured in a minute rather than in hours.
//
// It is a var rather than a const so the stream tests can drive the timeout in
// milliseconds instead of waiting a real minute for it.
var watchIdleTimeout = 60 * time.Second

// truncatedEventName is the ONE named event this stream emits: a prune that
// raced an open stream, carrying the same problem document the connect-time 410
// would have. Every other event is unnamed — the default `message` type — which
// is what lets a consumer read "an event I do not recognize" as "stop".
const truncatedEventName = "truncated"

// StreamEvent is one dispatched SSE event: a journal record (unnamed) or the
// `truncated` event (named), with the record's seq and the server's most recent
// reconnection advisory.
type StreamEvent struct {
	// Seq is the record's sequence number, from the `id:` field. It is the same
	// value the record's own JSON carries; a consumer that resumes uses it as
	// the next `since`.
	Seq int64
	// Name is "" for a record and truncatedEventName for the truncation event.
	Name string
	// Data is the event's `data:` payload, multi-line values joined with '\n'.
	// For a record it is the EventRecord JSON; for the truncation it is the
	// problem document.
	Data []byte
	// Retry is the server's most recent `retry:` advisory, or 0 if none has
	// arrived. A consumer that reconnects honors it as the delay before doing so.
	Retry time.Duration
}

// EventStream is one open text/event-stream response, parsed one dispatched
// event at a time.
//
// It is NOT safe for concurrent use: Next reads and parses on the caller's
// goroutine, which is exactly the single-goroutine delivery the Watcher role
// promises. Close is the only method safe to call from another goroutine, to
// abort a blocked Next.
type EventStream struct {
	op        string
	serverURL string
	body      io.ReadCloser
	reader    *bufio.Reader
	maxBytes  int64

	// parentCtx is the CALLER's context, read only to classify a read failure:
	// a read that failed because the caller canceled is theirs, one that failed
	// because the idle timer canceled the request is this stream's.
	parentCtx context.Context
	// cancel cancels the REQUEST's derived context, which is what the idle timer
	// and Close both pull to unblock a parked read.
	cancel context.CancelFunc

	idledOut atomic.Bool
	retry    time.Duration
	// closed is atomic to honor the documented cross-goroutine idempotency of
	// Close: the flag is read and set from any goroutine racing an abort against
	// the caller's own Close, so it matches idledOut rather than trusting a plain
	// bool the race detector would flag.
	closed atomic.Bool
}

// WatchEvents opens the journal stream from since, in ONE connect.
//
// It builds the request exactly as roundTrip does — the shared stamp, so the
// Bd-Project-Id the workspace pinned rides this request like every other — with
// Accept: text/event-stream and since as a required query parameter on every
// connect. It sends NO Last-Event-ID: this client rebuilds `since` on every
// reconnect from the seq it last delivered, which the server treats as
// equivalent to the header a browser's EventSource would resend.
//
// A 401 is retried once, exactly as Do retries it: one credential refresh and
// one reconnect, because a GET carries no body to replay and the server's token
// re-read is on a ~1s gate. Everything else the server can answer before the
// stream opens comes back as the typed error roundTrip would have produced.
func (c *Client) WatchEvents(ctx context.Context, since int64) (*EventStream, error) {
	q := url.Values{}
	// FormatInt, not Itoa: since is an int64 and a resume can sit anywhere in the
	// range, so a 32-bit conversion would silently wrap a large checkpoint.
	q.Set("since", strconv.FormatInt(since, 10))
	u, err := c.resolve(PathEventsWatch, q)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", OpWatchEvents, err)
	}

	hc := c.streamClient()

	stream, err := c.dialStream(ctx, hc, u)
	if err != nil && c.creds != nil {
		var prob *ProblemError
		if errors.As(err, &prob) && prob.Status == http.StatusUnauthorized {
			retry, rerr := c.creds.Refresh(ctx)
			if rerr != nil {
				// Fail closed, exactly as Do does: a credential source that errored
				// is not a license to send the stale one again.
				return nil, fmt.Errorf("%s: refreshing the credential for bd serve at %s: %w", OpWatchEvents, c.base.Redacted(), rerr)
			}
			if retry {
				stream, err = c.dialStream(ctx, hc, u)
			}
		}
	}
	return stream, err
}

// streamClient clones the client's http.Client with the timeout removed.
//
// A stream has no bounded length, so the 60-second per-request timeout that
// makes Do's whole-body read safe would kill every stream at one minute. The
// clone preserves Transport and the forced CheckRedirect — a redirect on a
// bearer connection is refused, never followed — and swaps only Timeout, so the
// stream's liveness is the idle deadline's job and its cancellation is the
// caller's context.
func (c *Client) streamClient() *http.Client {
	hc := *c.hc
	hc.Timeout = 0
	return &hc
}

// dialStream performs one connect and returns either a live stream or the typed
// error the server answered with before the stream opened.
func (c *Client) dialStream(ctx context.Context, hc *http.Client, u *url.URL) (*EventStream, error) {
	streamCtx, cancel := context.WithCancel(ctx)

	req, err := http.NewRequestWithContext(streamCtx, http.MethodGet, u.String(), nil)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("%s: %w", OpWatchEvents, err)
	}
	req.Header.Set("Accept", "text/event-stream")
	if err := c.stampRequest(streamCtx, OpWatchEvents, req); err != nil {
		cancel()
		return nil, err
	}

	//nolint:gosec // G704: the URL is the workspace's configured server; dialing it IS the feature.
	resp, err := hc.Do(req)
	if err != nil {
		cancel()
		return nil, &ConnectError{Op: OpWatchEvents, ServerURL: c.base.Redacted(), Err: err}
	}

	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		_ = resp.Body.Close()
		cancel()
		return nil, redirectRefused(OpWatchEvents, c.base.Redacted(), resp)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// The connect-time refusals are ordinary problem responses with a bounded
		// body, so this reads and maps them exactly as roundTrip does.
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, c.maxBytes+1))
		_ = resp.Body.Close()
		cancel()
		prob := mapProblem(c.target(Request{Op: OpWatchEvents}), resp.StatusCode, resp.Header, raw, c.maxRetry)
		if resp.StatusCode == http.StatusUnauthorized {
			prob.CredentialSource = credentialSource(c.creds)
		}
		return nil, prob
	}

	// A 2xx that is not text/event-stream is not a bd serve stream — a proxy, a
	// gateway, or a different server answering on the port. Refused rather than
	// parsed, so a consumer does not sit on a connection that will never frame a
	// record.
	if !isEventStream(resp.Header.Get("Content-Type")) {
		_ = resp.Body.Close()
		cancel()
		return nil, &NotEventStreamError{Op: OpWatchEvents, ServerURL: c.base.Redacted(), ContentType: resp.Header.Get("Content-Type")}
	}

	return &EventStream{
		op:        OpWatchEvents,
		serverURL: c.base.Redacted(),
		body:      resp.Body,
		reader:    bufio.NewReader(resp.Body),
		maxBytes:  c.maxBytes,
		parentCtx: ctx,
		cancel:    cancel,
	}, nil
}

// isEventStream reports whether a Content-Type names an SSE body, ignoring the
// charset parameter the server sends with it.
func isEventStream(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	return err == nil && mediaType == "text/event-stream"
}

// Retry is the server's most recent reconnection advisory, or 0. The Watcher
// role reads it after a stream drops to decide how long to wait before
// reconnecting.
func (s *EventStream) Retry() time.Duration { return s.retry }

// Close aborts a blocked Next and releases the connection. It is idempotent and
// safe to call from another goroutine.
//
// The one-shot is a CompareAndSwap, not a read-then-write, so two goroutines that
// race Close — the Watcher role closing a drained stream while an idle timer's
// abort path is unwinding, say — cancel the context and close the body exactly
// once between them, which is what the idempotency contract above promises.
func (s *EventStream) Close() {
	if !s.closed.CompareAndSwap(false, true) {
		return
	}
	s.cancel()
	_ = s.body.Close()
}

// Next reads and returns the next dispatched event.
//
// It loops over SSE lines, accumulating one event's fields until a blank line
// dispatches it. Comment lines (heartbeats) and frames that carry only a
// `retry:` or an `id:` — which SSE dispatches as no message — are absorbed here
// so the caller sees only events that carry data. A read that outlives the idle
// deadline, that the caller cancels, or that the connection drops surfaces as an
// error; a per-event size cap refuses a frame larger than the client's bound.
func (s *EventStream) Next() (StreamEvent, error) {
	var ev sseEvent
	for {
		line, err := s.readLine()
		if err != nil {
			return StreamEvent{}, s.classifyReadErr(err)
		}
		line = trimLineEnd(line)

		if len(line) == 0 {
			// The blank line dispatches. A frame with no data carries only a retry
			// advisory or an id and fires no message in SSE, so it is folded into
			// the next event rather than returned.
			if len(ev.data) == 0 {
				ev = sseEvent{}
				continue
			}
			return StreamEvent{Seq: ev.seq, Name: ev.name, Data: ev.data, Retry: s.retry}, nil
		}

		if line[0] == ':' {
			// A comment — the heartbeat. It carries nothing, and reading it already
			// reset the idle deadline, which is its whole purpose.
			continue
		}

		field, value := splitSSEField(line)
		if err := s.applyField(&ev, field, value); err != nil {
			return StreamEvent{}, err
		}
	}
}

// sseEvent is one event's fields, accumulated by Next until a blank line
// dispatches them.
type sseEvent struct {
	data []byte
	name string
	seq  int64
	// size is every field value read for this event, which is what the
	// per-event cap measures.
	size int64
}

// applyField folds one SSE field line into ev, refusing the event once its
// values pass the per-event cap. Every field's value counts against the cap,
// not just data's. retry is the one field that is not the event's: it is a
// stream-wide advisory, so it lands on s.
func (s *EventStream) applyField(ev *sseEvent, field, value string) error {
	ev.size += int64(len(value))
	if ev.size > s.maxBytes {
		return s.tooLarge()
	}
	switch field {
	case "event":
		ev.name = value
	case "data":
		if len(ev.data) > 0 {
			ev.data = append(ev.data, '\n')
		}
		ev.data = append(ev.data, value...)
		if int64(len(ev.data)) > s.maxBytes {
			return s.tooLarge()
		}
	case "id":
		if n, perr := strconv.ParseInt(value, 10, 64); perr == nil {
			ev.seq = n
		}
	case "retry":
		if ms, perr := strconv.ParseInt(value, 10, 64); perr == nil && ms >= 0 {
			s.retry = time.Duration(ms) * time.Millisecond
		}
	}
	return nil
}

// readLine reads one SSE line, bounded by the per-event cap and armed with the
// idle deadline.
//
// The read is wrapped in a fresh time.AfterFunc so that no bytes for
// watchIdleTimeout cancels the request's derived context, which unblocks the
// parked read; the timer is stopped the instant the line returns, so a stream
// delivering normally never trips it. ReadSlice rather than ReadString keeps a
// pathological line with no newline from growing an unbounded buffer: it is read
// in fragments and refused the moment the accumulated bytes pass the cap.
//
// The deadline is an INTER-BYTE idle, not a per-line transfer budget: it is
// re-armed on every fragment that made byte progress, so a large single line
// that keeps moving — arriving across many ErrBufferFull fragments — never idles
// out while bytes flow, and only a stream that goes genuinely silent for
// watchIdleTimeout between fragments does.
func (s *EventStream) readLine() ([]byte, error) {
	timer := time.AfterFunc(watchIdleTimeout, func() {
		s.idledOut.Store(true)
		s.cancel()
	})
	defer timer.Stop()

	var buf []byte
	for {
		frag, err := s.reader.ReadSlice('\n')
		if len(frag) > 0 {
			// Byte progress: this fragment is not silence, so re-arm the deadline to
			// measure the gap until the NEXT bytes rather than the whole transfer.
			timer.Reset(watchIdleTimeout)
		}
		buf = append(buf, frag...)
		if int64(len(buf)) > s.maxBytes {
			return nil, s.tooLarge()
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			if len(buf) > 0 && errors.Is(err, io.EOF) {
				// A final line with no trailing newline: return it, and let the
				// next read report the EOF.
				return buf, nil
			}
			return buf, err
		}
		return buf, nil
	}
}

// classifyReadErr turns a read failure into the error the caller should see.
//
// The order is deliberate: the caller's own cancellation wins, because a stream
// the caller stopped is not a drop to reconnect from. Then an idle timeout,
// which canceled the request but is this client's decision and not the
// caller's. Everything else — an EOF, a reset — is the connection dropping,
// which the Watcher role reconnects from.
func (s *EventStream) classifyReadErr(err error) error {
	if s.parentCtx.Err() != nil {
		return s.parentCtx.Err()
	}
	if s.idledOut.Load() {
		return &StreamIdleError{Op: s.op, ServerURL: s.serverURL, Idle: watchIdleTimeout}
	}
	return err
}

func (s *EventStream) tooLarge() error {
	return &StreamEventTooLargeError{Op: s.op, ServerURL: s.serverURL, Limit: s.maxBytes}
}

// trimLineEnd strips the trailing newline and an optional preceding carriage
// return, so a server that framed with CRLF parses like one that framed with LF.
func trimLineEnd(line []byte) []byte {
	line = bytes.TrimSuffix(line, []byte("\n"))
	line = bytes.TrimSuffix(line, []byte("\r"))
	return line
}

// splitSSEField splits an SSE line into its field name and value, dropping one
// optional space after the colon. A line with no colon is a field with an empty
// value, per the SSE grammar.
func splitSSEField(line []byte) (string, string) {
	i := bytes.IndexByte(line, ':')
	if i < 0 {
		return string(line), ""
	}
	value := line[i+1:]
	if len(value) > 0 && value[0] == ' ' {
		value = value[1:]
	}
	return string(line[:i]), string(value)
}

// ErrNotEventStream reports that a 2xx answer to the watch connect was not a
// text/event-stream body, which means the URL does not point at a bd serve.
var ErrNotEventStream = errors.New("bd serve did not answer the watch with an event stream")

// NotEventStreamError names the media type that came back instead, because on a
// shared host that is what says WHAT answered — a JSON API, an HTML login page,
// a proxy's own 200.
type NotEventStreamError struct {
	Op          string
	ServerURL   string
	ContentType string
}

func (e *NotEventStreamError) Error() string {
	if e.ContentType == "" {
		return fmt.Sprintf("%s: bd serve at %s answered the watch 2xx with no content type; a stream must be text/event-stream", e.Op, e.ServerURL)
	}
	return fmt.Sprintf("%s: bd serve at %s answered the watch with content type %q, not text/event-stream (the URL does not point at a bd serve)", e.Op, e.ServerURL, e.ContentType)
}

func (e *NotEventStreamError) Unwrap() error { return ErrNotEventStream }

// ErrStreamIdle reports that an open stream went silent past the idle deadline.
var ErrStreamIdle = errors.New("bd serve stream went silent")

// StreamIdleError is a stream that produced no bytes — not even a heartbeat —
// for watchIdleTimeout. The connection is treated as dead: the recovery is a
// reconnect, which the Watcher role performs from the last delivered seq.
type StreamIdleError struct {
	Op        string
	ServerURL string
	Idle      time.Duration
}

func (e *StreamIdleError) Error() string {
	return fmt.Sprintf("%s: bd serve at %s sent no data for %s; the stream is silent past three heartbeats and treated as dead", e.Op, e.ServerURL, e.Idle)
}

func (e *StreamIdleError) Unwrap() error { return ErrStreamIdle }

// ErrStreamPoisoned reports that an open stream carried a payload this client
// cannot follow, discovered MID-STREAM: a record whose JSON will not decode, or
// an event past the per-event bound. It is a PROTOCOL fault, not a transport
// drop — the resume checkpoint would re-fetch the same poisoned record on every
// reconnect — so the Watcher role STOPS on it rather than reconnecting into an
// unbounded loop pinned on the same bad record. A consumer dispatches on it to
// tell a poisoned feed from a transient disconnect it never sees.
var ErrStreamPoisoned = errors.New("bd serve stream carried a record this client cannot follow")

// StreamEventTooLargeError is one SSE event past the client's per-event bound.
// It reuses ErrResponseTooLarge because the recovery is the same — raise the cap,
// or recognize that the far likelier cause is a URL that is not a bd serve — and
// the per-event bound is what the whole-body cap is on the paged reads.
type StreamEventTooLargeError struct {
	Op        string
	ServerURL string
	Limit     int64
}

func (e *StreamEventTooLargeError) Error() string {
	return fmt.Sprintf("%s: a single event from bd serve at %s exceeds the %d-byte limit", e.Op, e.ServerURL, e.Limit)
}

func (e *StreamEventTooLargeError) Unwrap() error { return ErrResponseTooLarge }

// NewEventStreamReader wraps an already-open SSE body for a backend test that
// feeds canned frames without standing up an HTTP server.
//
// Production opens a stream through WatchEvents, which owns the connect and the
// idle-deadline plumbing; this constructor is the seam a stub-tier test uses to
// drive the Watcher role's reconnect and delivery logic against scripted bytes.
// The parent context is Background — a byte reader never blocks, so the idle
// timer never fires — and Close still closes the reader the test handed in.
func NewEventStreamReader(body io.ReadCloser, maxBytes int64) *EventStream {
	_, cancel := context.WithCancel(context.Background())
	if maxBytes <= 0 {
		maxBytes = DefaultMaxResponseBytes
	}
	return &EventStream{
		op:        OpWatchEvents,
		serverURL: "test",
		body:      body,
		reader:    bufio.NewReader(body),
		maxBytes:  maxBytes,
		parentCtx: context.Background(),
		cancel:    cancel,
	}
}
