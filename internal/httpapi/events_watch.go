package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/steveyegge/beads/internal/eventsjournal"
	"github.com/steveyegge/beads/internal/storage"
)

// The events journal, PUSHED. GET /v0/beads/events:watch is the poll read's
// sibling, not a mode of it: same cursor, same records, same refusals, held
// open as text/event-stream so a consumer learns about a mutation when it
// happens instead of on its next interval.
//
// THE CURSOR IS STILL THE CONTRACT, and that is the whole design. This server
// keeps no per-consumer state, remembers no subscription and cannot redeliver;
// a stream is a sequence of `since` reads this process performs on the client's
// behalf, and every record carries `id: <seq>` so the client's own checkpoint
// is the same number it would have held while polling. That is what makes
// reconnection free: `Last-Event-ID` — the header a browser's EventSource
// resends by itself — is the same value the `since` parameter takes, so a
// dropped stream resumes exactly where a poller would have.
//
// NOTHING NEW IS READ. The loop below calls the SAME ReadEventsJournalPage the
// poll handler calls, through the same read-only cursor, with the same
// truncation contract arriving as the same typed error. There is no follow mode
// in storage, no notification channel and no new engine surface — a push
// endpoint that invented its own read path would be a second retention contract
// with the same failure mode this feature exists to prevent.
//
// WHEN TO USE WHICH is a real decision and the document states it: a consumer
// that can afford its interval should poll, because a poll holds nothing
// between requests. A stream costs a connection and a goroutine for as long as
// it stays open, plus a database slot for the moment of each read — which is
// why there is a hard cap on how many this process will hold at once, and why
// the refusal past that cap points back at the paged read.

const (
	// eventsWatchBatch is how many records one pass of the loop may carry. It
	// is the poll endpoint's DEFAULT rather than its ceiling, and it is not a
	// parameter: a stream paces itself, so the only thing this number changes
	// is how a backlog is chunked on the way out.
	eventsWatchBatch = defaultEventsLimit

	// eventsWatchRetry is the reconnection delay handed to the client in the
	// stream's first line, in milliseconds. EventSource's own default is
	// implementation-defined and browsers have shipped values from 500ms up, so
	// it is stated rather than inherited: three seconds is long enough that a
	// server restart does not produce a reconnect storm, short enough that a
	// consumer's lag after one is measured in seconds.
	eventsWatchRetry = 3000

	// eventsWatchTruncatedRetry is the delay raised in front of the `truncated`
	// event, in milliseconds. A consumer that respects the event stops and
	// re-baselines and never uses this; it is for the one that does not — a bare
	// EventSource will reconnect with the same dead Last-Event-ID and earn a
	// connect-time 410 forever, and a minute between attempts is the difference
	// between a slow loop and a hot one.
	eventsWatchTruncatedRetry = 60000

	// maxWatchStreams bounds concurrent streams, and it sits BELOW the
	// accepted-connection cap on purpose. Every stream holds a connection and a
	// goroutine for its whole life, so without a bound the stream surface would
	// be the one operation on this server with no limit at all — and it is the
	// one whose requests last hours.
	//
	// SIXTEEN CONNECTIONS OF HEADROOM IS WHAT MAKES THE REFUSAL REAL, and this
	// is the arithmetic. netutil.LimitListener returns a connection slot only
	// when the connection CLOSES — after the handler has returned, which is
	// after this counter has already come back down. A cap equal to maxConns
	// would therefore be unreachable: at sixty-four streams every connection
	// slot is held by one of them, the sixty-fifth connect is never accepted,
	// and the 503 this operation documents could not be delivered to anybody.
	// The paged read, every mutation and a fresh-connection /healthz would park
	// silently in the kernel accept backlog instead, which is precisely the
	// failure the cap exists to convert into a status and a log line.
	//
	// So the stream surface saturates FIRST, sixteen connections early — one
	// full complement of the in-flight database requests this server admits —
	// leaving room for the polls, writes and probes that must keep answering.
	// Connection saturation at maxConns is still reachable by other means and is
	// still the silent cliff; the runbook documents it as the worse one.
	// TestTheStreamCapLeavesConnectionHeadroom pins the relationship.
	maxWatchStreams = 48
)

// The stream's two cadences. Both are Server fields at the point of use
// (orDefault), for the reason semTimeout and writeStall are: a test that had to
// wait real seconds for a heartbeat would either be slow or would not exist.
const (
	// watchPollInterval is how often the loop asks for new records. One second
	// is what `bd events tail --follow` uses, and the stream is deliberately no
	// fresher than the CLI's own follow: a tighter loop would multiply database
	// reads across every open stream to shave a delay nothing here promises.
	watchPollInterval = time.Second

	// watchHeartbeat is how long a stream may go silent before it emits a
	// comment line. It is proxy and NAT defense first — an idle connection
	// through an intermediary that times out silently is the classic way a
	// stream stops delivering without either end noticing — and liveness second:
	// a write to a client that has gone away fails, which is how this loop
	// learns about a disconnect no TCP FIN announced.
	watchHeartbeat = 20 * time.Second

	// eventsWatchReadDeadline bounds ONE pass of the loop's read, and it is
	// deliberately much shorter than the requestDeadline this operation is
	// exempt from.
	//
	// It is not really a database budget: a bounded, indexed page of a thousand
	// journal rows needs nothing like fifteen seconds, and a read that takes
	// them is a wedge rather than a slow query. It is a LIVENESS budget, and it
	// bounds two things a stream cannot otherwise bound. A read parked for sixty
	// seconds suspends this stream's heartbeats for sixty seconds, which reads
	// to every intermediary between here and the consumer as a dead connection.
	// And it delays this loop's next look at the shutdown signal by the same
	// amount, which is three times the drain budget — turning a clean stop into
	// a forced one.
	eventsWatchReadDeadline = 15 * time.Second
)

// lastEventIDHeader is the standard SSE resume header. It is spelled once
// because it is both read from the request and named in a refusal.
const lastEventIDHeader = "Last-Event-ID"

// handleWatchEvents answers GET /v0/beads/events:watch.
//
// EVERY REFUSAL IS AN ORDINARY PROBLEM RESPONSE, and the ordering below is what
// makes that true: validation, activation, transport, capacity, and then the
// first read — all of it before a single stream byte. Once the 200 and its
// text/event-stream header are on the wire the status is spent, and a
// truncation discovered after that can only be reported inside the stream (see
// the `truncated` event). The connect-time half of the contract is therefore
// byte-identical to the poll endpoint's: same 400s, same 409, same 410.
func (s *Server) handleWatchEvents(w http.ResponseWriter, r *http.Request) {
	rec := requestInfo(r.Context())
	q := newQuery(r.URL.Query())
	since := q.integer64("since")
	if !s.acceptQuery(w, r, q) {
		return
	}

	// `since` is required here for the reason it is required on the poll read —
	// an omitted checkpoint defaulted to zero replays the whole retained window
	// as a flood of duplicates — and it stays required even when the header
	// below supersedes its value. A browser reconnecting an EventSource re-sends
	// the ORIGINAL URL with its original `since`, so a stream whose parameter
	// was optional on reconnect and required on first connect would be one rule
	// with two spellings.
	if since == nil || *since < 0 {
		rec.refuse("since")
		s.fail(w, r, InvalidArgument("since", ReasonInvalidValue,
			"since is required and must be zero or a positive sequence number; use 0 to read from the beginning"))
		return
	}
	cursor := *since

	// THE HEADER WINS, because the client that sends it did not choose to. An
	// EventSource re-sends the URL it was constructed with — a `since` that is
	// as old as the consumer's process — and attaches Last-Event-ID carrying the
	// seq it actually reached. Honoring the parameter there would re-deliver
	// every record since startup on every reconnect, forever.
	//
	// An unparseable value is refused rather than ignored, and that is the whole
	// argument for validating a header this server also emits: a client that
	// invented its own Last-Event-ID has a broken checkpoint, and silently
	// falling back to `since` would hand it a stream that looks correct and
	// starts in the wrong place.
	if raw := r.Header.Get(lastEventIDHeader); raw != "" {
		resumed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || resumed < 0 {
			rec.refuse(lastEventIDHeader)
			s.fail(w, r, InvalidArgument(lastEventIDHeader, ReasonInvalidValue,
				"Last-Event-ID must be a journal sequence number: zero or a positive 64-bit integer, as this stream emits it"))
			return
		}
		cursor = resumed
	}

	// The activation gate, before any database work, for the reason the poll
	// read states: a disabled journal is indistinguishable from an idle one in
	// the data, so a stream over one would be a connection held open forever
	// against a workspace that will never emit a record.
	if !s.cfg.EventsJournalEnabled {
		s.fail(w, r, EventsJournalDisabled())
		return
	}

	// A stream that cannot flush is not a stream: every record would sit in
	// net/http's buffer until it filled or the handler returned, which for this
	// operation is never. Refused here, before the cap is charged and before the
	// status is spent, so a deployment that wrapped this server in a
	// non-flushing middleware learns about it on the first request instead of
	// producing streams that deliver nothing.
	if !canFlush(w) {
		s.event("events_watch_unflushable", "request_id", rec.id, "writer", fmt.Sprintf("%T", w))
		s.fail(w, r, newResult(CodeInternal, ""))
		return
	}

	release, admitted := s.admitWatchStream(rec)
	if !admitted {
		s.fail(w, r, EventsWatchSaturated())
		return
	}
	defer release()

	// THE READ DEADLINE GOES BEFORE THE FIRST READ, not when the stream opens.
	// http.Server.ReadTimeout is thirty seconds from the start of the request,
	// and the connect read below is the last thing that happens under it; a
	// database wedged at that moment would have the deadline expire mid-read,
	// which net/http reports by canceling the request context — so the wedge
	// would be booked as `client_closed` and the operator would be looking for a
	// client that never left. Clearing it here means the connect read is bounded
	// only by its own deadline (readWatchBatch), which is the one that names the
	// right condition.
	clearRequestReadDeadline(w)

	journal, err := s.eventsJournalCursor(r)
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	// THE FIRST READ HAPPENS BEFORE THE STREAM OPENS. It is what turns a
	// pruned-past checkpoint into the plain 410 the poll read gives — the same
	// body, the same code, the same window — rather than into a 200 that
	// immediately reports its own failure. Every other connect-time error
	// (busy, unreachable, a backend fault) reaches the wire the same way for the
	// same reason.
	page, err := s.readWatchBatch(r.Context(), rec, journal, cursor)
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	s.streamEvents(w, r, journal, cursor, page)
}

// streamEvents writes the stream and owns its life.
//
// It returns when the client goes away, when the server begins shutting down,
// when a write fails, or when the journal is pruned out from under the cursor —
// and on every one of those paths the caller's deferred release gives the
// stream slot back. There is no other exit.
func (s *Server) streamEvents(w http.ResponseWriter, r *http.Request, journal storage.EventsJournalCursor, since int64, page storage.EventsJournalPage) {
	ctx := r.Context()
	rec := requestInfo(ctx)

	// Everything this stream logs carries the request id, so the one line the
	// request log already has for it and any line the stream adds join.
	stream := newEventStream(w, func(name string, kv ...any) {
		s.event(name, append([]any{"request_id", rec.id}, kv...)...)
	})
	stream.open(eventsWatchRetry)

	// The per-pass slot acquisitions are booked against a DETACHED record
	// carrying this request's id: acquire overwrites sem_wait every time, and
	// the number worth having on a stream's one request line is the wait at
	// connect, not the wait on whichever pass happened to be last. Saturation
	// events stay attributable because the id travels.
	passes := &reqInfo{id: rec.id}

	// A TIMER RATHER THAN A TICKER, because the interval is jittered per pass.
	// A ticker would put every stream on the same phase, and the phase is not
	// random: streams reconnect together after a restart and all take the same
	// `retry` delay, so a fixed interval marches them into one read burst per
	// second for the life of the process. watchPollDelay spreads both the first
	// wait and every later one.
	interval := orDefault(s.watchPoll, watchPollInterval)
	poll := time.NewTimer(watchPollDelay(interval))
	defer poll.Stop()
	beat := orDefault(s.watchBeat, watchHeartbeat)
	quiet := time.Now()

	for {
		for _, row := range page.Rows {
			if !stream.record(row) {
				return
			}
			// Advanced only past records this loop has WRITTEN, so a failed
			// write cannot be followed by a read that skips what it dropped.
			since = row.Seq
		}
		switch {
		case len(page.Rows) > 0:
			stream.flush()
			quiet = time.Now()
		case time.Since(quiet) >= beat:
			stream.comment("heartbeat")
			quiet = time.Now()
		}
		if stream.failed() {
			// A broken pipe, or a client that stopped reading long enough to
			// trip the rolled write deadline. Either way this connection is
			// finished; the client's reconnect is the recovery and it already
			// holds the id to resume from.
			return
		}

		// A FULL BATCH MEANS THERE IS MORE, so the backlog drains at read speed
		// instead of one batch per tick. Only a short read waits: that is the
		// caught-up state, and it is the only state in which this loop sleeps.
		//
		// EVERY PASS CHECKS FOR THE EXIT, though, including the draining ones.
		// A consumer resuming from a week-old checkpoint drains hundreds of
		// batches back to back, and a loop that only looked at the client and
		// the shutdown signal on the branch that sleeps would ignore both for
		// the whole backlog — writing records to a client that hung up long ago,
		// and holding a graceful shutdown past its drain budget for a stream
		// nobody is reading. The non-blocking check costs one select per pass.
		if len(page.Rows) == eventsWatchBatch {
			select {
			case <-ctx.Done():
				return
			case <-s.closing:
				return
			default:
			}
		} else {
			select {
			case <-ctx.Done():
				return
			case <-s.closing:
				// Graceful shutdown. Returning here is what lets the drain
				// finish: http.Server.Shutdown waits for active requests, and a
				// stream that waited for the client to leave would hold every
				// shutdown open for the whole drain timeout.
				return
			case <-poll.C:
				poll.Reset(watchPollDelay(interval))
			}
		}

		var err error
		page, err = s.readWatchBatch(ctx, passes, journal, since)
		if err == nil {
			continue
		}

		var truncated *storage.EventsJournalTruncatedError
		switch {
		case errors.Is(err, ErrBusy):
			// Slot pressure, not a fault. The refused read left `page` empty, so
			// the loop simply sleeps and asks again on the next tick. Ending the
			// stream here would turn a busy minute into a reconnect from every
			// consumer at once, which is the load the semaphore just refused.
		case errors.As(err, &truncated):
			stream.truncate(truncatedFrame(rec, truncated))
			return
		case errors.Is(err, context.Canceled):
			// The client hung up mid-read. Not a server fault and not worth an
			// error line — the same judgement failErr makes.
			return
		default:
			s.event("events_watch_failed", "request_id", rec.id, "since", since, "error", err.Error())
			return
		}
	}
}

// readWatchBatch performs one pass of the loop's read, holding a database slot
// for exactly as long as the read takes.
//
// A STREAM MUST NOT HOLD A SLOT WHILE IT WAITS. There are sixteen and this
// server admits sixty-four streams; one slot per open stream would let a
// handful of idle consumers starve every other operation on the server for
// hours, with /healthz green throughout. Taking the slot per read keeps the
// invariant the semaphore exists for — nothing touches the database without one
// — while charging a stream only for the moments it actually does.
//
// The read carries its own deadline for the same reason the route's per-request
// one exists. That deadline does not apply to this operation (the response has
// no bounded length), so without one here a wedged database would park a read,
// and its slot, for the life of a connection nobody is watching. It is a
// shorter budget than the route's, and eventsWatchReadDeadline says why.
func (s *Server) readWatchBatch(ctx context.Context, rec *reqInfo, journal storage.EventsJournalCursor, since int64) (storage.EventsJournalPage, error) {
	release, err := s.acquire(ctx, rec)
	if err != nil {
		return storage.EventsJournalPage{}, err
	}
	defer release()

	readCtx, cancel := context.WithTimeout(ctx, eventsWatchReadDeadline)
	defer cancel()
	return journal.ReadEventsJournalPage(readCtx, since, eventsWatchBatch)
}

// watchPollDelay spreads one stream's next read around the poll interval, by up
// to a tenth of it either way.
//
// Streams synchronize by themselves and there is nothing random to break the
// tie: every consumer reconnecting after a restart is handed the same `retry`
// delay, so they come back together and, on a fixed interval, read together
// forever after — one burst of up to maxWatchStreams reads per second against a
// semaphore that admits sixteen. Jittering every wait rather than only the first
// keeps them spread even after a pass that took longer than its interval.
//
// crypto/rand because it is the one source in this process that needs no seed
// and no justification; the cost is a two-byte read once per stream per second.
// A failed read falls back to the flat interval, which is the behavior this
// function is improving on rather than depending on.
func watchPollDelay(interval time.Duration) time.Duration {
	span := interval / 5
	if span <= 0 {
		return interval
	}
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		return interval
	}
	offset := time.Duration(int64(span) * int64(binary.BigEndian.Uint16(b[:])) / (1 << 16))
	return interval - span/2 + offset
}

// clearRequestReadDeadline drops http.Server.ReadTimeout for this request.
//
// ReadTimeout is a deadline on the WHOLE request, and net/http keeps a
// background read running on the connection while a handler writes — the read
// that notices a disconnect. When that read hits the deadline, net/http treats
// it as a dead connection and CANCELS the request context, so a stream would end
// itself at ReadTimeout with no client involved and nothing in the log to say
// why. Clearing it is per-request: the next request on a reused connection gets
// its deadlines set fresh.
//
// It does not weaken disconnect detection. A client that goes away still
// produces a read error, which still cancels the context; what is lost is only
// the TIME limit, which is the thing SSE requires — a healthy consumer sends
// nothing for the life of its stream. A peer that vanishes without a FIN or RST
// is reaped by TCP keepalive, which Go's listener enables by default (see the
// note in engdocs/SERVE_RUNBOOK.md for the measured window).
//
// The WRITE deadline needs no such handling and must not be cleared:
// statusWriter rolls it forward immediately before every write, so it bounds one
// stalled write rather than the stream, which is exactly right here.
//
// The error is dropped for the reason extendWriteDeadline drops its own: a
// ResponseWriter with no connection under it (httptest's recorder) has no
// deadline to clear and nothing to time out.
func clearRequestReadDeadline(w http.ResponseWriter) {
	_ = http.NewResponseController(w).SetReadDeadline(time.Time{})
}

// truncatedFrame is the body the mid-stream `truncated` event carries: the
// EXACT problem document the connect-time 410 would have carried, request id
// and all.
//
// One shape rather than two. A consumer meets this condition on both surfaces —
// a 410 when it reconnects, this event when a prune races an open stream — and
// a second encoding of the same three numbers is a second contract to keep in
// step. Whatever handles the 410 handles this, unchanged.
func truncatedFrame(rec *reqInfo, err *storage.EventsJournalTruncatedError) []byte {
	res := EventsJournalTruncated(err).WithRequestID(rec.id)
	body, marshalErr := json.Marshal(res.Problem)
	if marshalErr != nil {
		// Unreachable: apigen.Problem is plain data. The stream still has to
		// end, and it ends with something a consumer can parse as this event.
		return []byte(`{"code":"` + string(CodeEventsJournalTruncated) + `"}`)
	}
	return body
}

// admitWatchStream reserves one of the concurrent-stream slots, or reports that
// this server is already holding as many as it will.
//
// The counter is incremented FIRST and rolled back on refusal, rather than
// compared and then incremented: two connects arriving together would both read
// the same count and both be admitted, which is how a cap ends up being
// advisory. The release the caller defers is the only decrement, so it runs on
// every exit path a stream has.
func (s *Server) admitWatchStream(rec *reqInfo) (release func(), admitted bool) {
	limit := int64(maxWatchStreams)
	if s.maxWatchStreams > 0 {
		limit = int64(s.maxWatchStreams)
	}
	live := s.watchStreams.Add(1)
	if live > limit {
		s.watchStreams.Add(-1)
		s.event("events_watch_saturated", "request_id", rec.id, "streams", live-1, "max_streams", limit)
		return nil, false
	}
	// The gauge on ADMISSION, not only on refusal. Streams accumulate over hours
	// and the refusal is the cliff; a line per connect carrying the live count
	// against the limit is what lets an operator watch the approach instead of
	// discovering it from a consumer's 503. One line per stream, not per record.
	s.event("events_watch_admitted", "request_id", rec.id, "streams", live, "max_streams", limit)
	return func() { s.watchStreams.Add(-1) }, true
}

// canFlush reports whether w can push bytes to the client before the handler
// returns, without writing anything to find out.
//
// http.ResponseController answers the same question, but only by attempting a
// Flush — which writes the header and spends the status. This surface has to
// know before it decides between a 200 stream and a 500, so it walks the
// wrapper chain the same way the controller does. The bound is against a writer
// whose Unwrap returns itself; net/http's own walk is unbounded, and a hang in
// a handler is worse than a missed capability.
func canFlush(w http.ResponseWriter) bool {
	for range 8 {
		if _, ok := w.(http.Flusher); ok {
			return true
		}
		unwrapper, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return false
		}
		w = unwrapper.Unwrap()
	}
	return false
}

// eventStream frames one text/event-stream response.
//
// It remembers the FIRST write error and turns every later call into a no-op,
// so the loop above reads like the happy path and still stops at the first
// broken pipe. There is no buffering: each frame is written and flushed on its
// way out, because a record held back is a record the consumer has not seen.
type eventStream struct {
	w  http.ResponseWriter
	rc *http.ResponseController
	// note records a condition the response itself cannot report, because the
	// status is spent. Exactly one thing uses it — see record — and write
	// failures deliberately do not: a client hanging up is ordinary and is not a
	// server event.
	note func(name string, kv ...any)
	err  error
}

func newEventStream(w http.ResponseWriter, note func(name string, kv ...any)) *eventStream {
	return &eventStream{w: w, rc: http.NewResponseController(w), note: note}
}

// open writes the response headers and the reconnection delay, and flushes them
// so a client's connect completes before the first record exists.
func (e *eventStream) open(retryMillis int) {
	h := e.w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	// Buffering is the one intermediary behavior that silently defeats a
	// stream: a reverse proxy that accumulates the response delivers every
	// record at once, late. This is the de-facto header for turning it off.
	//
	// Cache-Control is deliberately NOT set here. withRequestContext already
	// sets `no-store` on every response, which forbids storing the body at all
	// — strictly stronger than the `no-cache` an SSE example usually shows —
	// and a second, weaker value would be the only thing this operation
	// disagreed with the rest of the surface about.
	h.Set("X-Accel-Buffering", "no")

	// The request read deadline is already gone — clearRequestReadDeadline runs
	// before the connect read, so that read is bounded by its own deadline and
	// not by the residue of http.Server.ReadTimeout. Nothing to do here.
	e.w.WriteHeader(http.StatusOK)
	e.write(fmt.Sprintf("retry: %d\n\n", retryMillis))
	e.flush()
}

// record emits one journal record as one SSE event, and reports whether the
// stream is still writable.
//
// `id:` IS THE CURSOR. It carries the record's seq, which is what a client
// sends back as Last-Event-ID and what it would have passed as `since` while
// polling — one checkpoint, three spellings, all the same number.
//
// The event is UNNAMED on purpose: the default `message` type is what a bare
// `es.onmessage` receives, so the ordinary case needs no listener registration
// at all. The one named event on this stream is `truncated`, and naming only
// the exception is what lets a consumer treat "an event I do not recognize" as
// "stop, this is not a record".
//
// ONE `data:` LINE, ALWAYS. An SSE frame is line-oriented and a raw newline
// inside a data line would split one record into two, so the guarantee has to
// come from the encoding rather than from hope: this is encoding/json output,
// and the encoder escapes every control character — including U+000A and
// U+000D — inside every string it writes. The payload members travel as
// json.RawMessage, so they are not re-encoded here; they are themselves the
// output of a marshaler for the same reason.
// TestEventsWatchFramesEveryRecordOnOneLine drives a record whose payloads
// carry literal newlines and carriage returns and pins that the frame stays one
// line.
func (e *eventStream) record(row storage.EventsJournalRow) bool {
	if e.err != nil {
		return false
	}
	encoded, err := json.Marshal(eventsjournal.NewRecord(row))
	if err != nil {
		// Unreachable for a well-formed row, and there is no way to tell the
		// CLIENT: the 200 is long since on the wire. So it goes in the log
		// naming the seq, because the alternative is the worst diagnosis on this
		// surface — a stream that ends silently, and ends again at the same
		// record on every reconnect, with nothing anywhere to say which row is
		// unencodable. Ending the stream is still the right behavior; skipping
		// the record would be silent loss.
		e.note("events_watch_failed", "seq", row.Seq, "error", err.Error())
		e.err = err
		return false
	}
	e.write(fmt.Sprintf("id: %d\ndata: %s\n\n", row.Seq, encoded))
	return e.err == nil
}

// comment emits a line no consumer sees: an SSE comment, which exists to put
// bytes on an idle connection.
func (e *eventStream) comment(text string) {
	e.write(": " + text + "\n\n")
	e.flush()
}

// truncate ends the stream with the one named event it can emit.
//
// The reconnection delay is RAISED FIRST, and the order matters: a client that
// reconnects on this event reaches the connect-time 410 and can do nothing
// about it, so the last instruction this server gets to give is "come back
// slowly". A consumer that handles the event properly stops and re-baselines
// and never spends the delay.
func (e *eventStream) truncate(body []byte) {
	e.write(fmt.Sprintf("retry: %d\n\n", eventsWatchTruncatedRetry))
	e.write(fmt.Sprintf("event: truncated\ndata: %s\n\n", body))
	e.flush()
}

func (e *eventStream) write(frame string) {
	if e.err != nil {
		return
	}
	// gosec's taint analysis flags every direct write to a ResponseWriter as a
	// possible XSS sink because it cannot see a Content-Type. It is
	// text/event-stream here, set in open, which no browser parses as a
	// document: EventSource hands the data to script as a string and renders
	// nothing. The only caller-derived value that reaches these frames is a
	// journal record, and it arrives as encoding/json output.
	//nolint:gosec // G705: text/event-stream body, JSON-encoded content, no HTML sink.
	if _, err := fmt.Fprint(e.w, frame); err != nil {
		e.err = err
	}
}

func (e *eventStream) flush() {
	if e.err != nil {
		return
	}
	if err := e.rc.Flush(); err != nil {
		e.err = err
	}
}

// failed reports whether a write has already failed, which is the loop's signal
// that this connection is over.
func (e *eventStream) failed() bool { return e.err != nil }
