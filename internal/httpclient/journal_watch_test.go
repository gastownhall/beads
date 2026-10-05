// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/journal_watch_test.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/eventsjournal"
	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/journalops"
)

// The unit half of the events-journal WATCH: the reconnect-inside-the-role
// bookkeeping a correct server never lets a served test observe.
//
// The served tier runs the whole watch contract against a real bd serve. What is
// left here is the logic that has no server-side witness: that a dropped stream
// resumes from the last DELIVERED seq (exactly-once across a reconnect), that a
// deliver refusal aborts and returns verbatim, that a saturation refusal returns
// without a retry, and that a capability-absent handshake refuses before any
// dial at all.

// watchWire scripts one connect at a time: each call answers the next prepared
// stream (canned SSE bytes) or the next prepared connect error, and records the
// `since` it was dialed with.
type watchWire struct {
	recordingWire

	responses    []watchResponse
	sinceSeen    []int64
	dials        int
	preflightErr error
	preflights   int
}

type watchResponse struct {
	body     string // SSE frames, when err is nil
	err      error  // a connect error, when non-nil
	maxBytes int64  // per-event cap for this stream; 0 means the client default
}

func (w *watchWire) Preflight(_ context.Context, _ string) error {
	w.preflights++
	return w.preflightErr
}

func (w *watchWire) WatchEvents(_ context.Context, since int64) (*wire.EventStream, error) {
	w.sinceSeen = append(w.sinceSeen, since)
	idx := w.dials
	w.dials++
	if idx >= len(w.responses) {
		// The script is exhausted. Return a terminal saturation refusal so a test
		// that reconnects past its scripted streams stops deterministically rather
		// than looping.
		return nil, saturatedProblem()
	}
	r := w.responses[idx]
	if r.err != nil {
		return nil, r.err
	}
	return wire.NewEventStreamReader(io.NopCloser(strings.NewReader(r.body)), r.maxBytes), nil
}

func saturatedProblem() error {
	return &wire.ProblemError{
		Op: wire.OpWatchEvents, Status: http.StatusServiceUnavailable,
		Code: "events_watch_saturated", Err: wire.ErrEventsWatchSaturated,
	}
}

// watcherOver binds the WATCH role to the transport through the TYPE ASSERTION a
// front door makes, never through the concrete method set — the same way
// journalOver binds the paged read.
func watcherOver(t *testing.T, w WireClient) journalops.Watcher {
	t.Helper()
	store := New(testTarget(t), w, &apigen.ContextResponse{})
	watcher, ok := any(store).(storage.EventsJournalWatcher)
	if !ok {
		t.Fatal("*Store does not implement storage.EventsJournalWatcher")
	}
	return watcher
}

// recordFrame builds one SSE record frame the way the server frames it: `id:`
// carrying the seq, one `data:` line carrying the EventRecord JSON.
func recordFrame(seq int64) string {
	rec := eventsjournal.Record{
		Seq: seq, TS: "2026-08-11T00:00:00Z", Op: "create",
		IssueID: fmt.Sprintf("bd-%d", seq),
		Issue:   json.RawMessage(fmt.Sprintf(`{"id":"bd-%d"}`, seq)),
	}
	b, err := json.Marshal(rec)
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf("id: %d\ndata: %s\n\n", seq, b)
}

func recordFrames(seqs ...int64) string {
	var b strings.Builder
	for _, seq := range seqs {
		b.WriteString(recordFrame(seq))
	}
	return b.String()
}

// fastReconnect shrinks the reconnect delay so a test that drops and resumes
// does not wait whole seconds for the reconnect.
func fastReconnect(t *testing.T) {
	t.Helper()
	restore := watchReconnectDelay
	watchReconnectDelay = time.Millisecond
	t.Cleanup(func() { watchReconnectDelay = restore })
}

func TestWatchRefusesWithZeroDialsWhenTheCapabilityIsAbsent(t *testing.T) {
	w := &watchWire{preflightErr: &wire.CapabilityError{
		Op: wire.OpWatchEvents, Capability: "events.watch", ServerURL: "http://serve",
	}}
	err := watcherOver(t, w).WatchEventsJournal(context.Background(), 0, func(journalops.Row) error {
		t.Fatal("delivered a record from a server that does not advertise the capability")
		return nil
	})

	var absent *wire.CapabilityError
	if !errors.As(err, &absent) {
		t.Fatalf("a capability-absent watch = %v (%T), want *wire.CapabilityError", err, err)
	}
	if w.dials != 0 {
		t.Errorf("dialed the stream %d times, want 0: the preflight refuses before any dial", w.dials)
	}
}

func TestWatchResumesFromTheLastDeliveredSeqAcrossAReconnect(t *testing.T) {
	fastReconnect(t)
	// The first stream carries three records and then drops (the reader ends,
	// which the parser surfaces as an EOF). The role must reconnect from seq 3 —
	// the last it DELIVERED — so the second dial's `since` is 3 and no record is
	// replayed or skipped. The scripted second connect refuses with saturation to
	// end the loop.
	w := &watchWire{responses: []watchResponse{
		{body: recordFrames(1, 2, 3)},
		{err: saturatedProblem()},
	}}

	var delivered []int64
	err := watcherOver(t, w).WatchEventsJournal(context.Background(), 0, func(row journalops.Row) error {
		delivered = append(delivered, row.Seq)
		return nil
	})
	if !errors.Is(err, wire.ErrEventsWatchSaturated) {
		t.Fatalf("the resumed watch ended with %v, want the scripted saturation", err)
	}

	if want := []int64{1, 2, 3}; !equalSeqs(delivered, want) {
		t.Errorf("delivered %v, want %v with no dup and no gap across the reconnect", delivered, want)
	}
	if len(w.sinceSeen) != 2 {
		t.Fatalf("made %d dials, want 2 (the first stream and one reconnect)", len(w.sinceSeen))
	}
	if w.sinceSeen[0] != 0 || w.sinceSeen[1] != 3 {
		t.Errorf("dialed since=%v, want [0 3]: the reconnect resumes from the last DELIVERED seq", w.sinceSeen)
	}
}

func TestWatchSkipsARecordAtOrBelowTheResumePoint(t *testing.T) {
	fastReconnect(t)
	// A server that re-sent the resume row after a reconnect must not make the
	// role deliver it twice. The first stream delivers 1,2,3 then drops; the
	// second re-sends 3 (the resume row) before 4,5. The monotonicity guard drops
	// the echoed 3.
	w := &watchWire{responses: []watchResponse{
		{body: recordFrames(1, 2, 3)},
		{body: recordFrames(3, 4, 5)},
		{err: saturatedProblem()},
	}}

	var delivered []int64
	_ = watcherOver(t, w).WatchEventsJournal(context.Background(), 0, func(row journalops.Row) error {
		delivered = append(delivered, row.Seq)
		return nil
	})
	if want := []int64{1, 2, 3, 4, 5}; !equalSeqs(delivered, want) {
		t.Errorf("delivered %v, want %v: an echoed resume row is dropped by the monotonicity guard", delivered, want)
	}
}

func TestWatchReturnsADeliverErrorVerbatimAndStops(t *testing.T) {
	fastReconnect(t)
	sentinel := errors.New("the consumer said stop")
	w := &watchWire{responses: []watchResponse{{body: recordFrames(1, 2, 3)}}}

	var delivered []int64
	err := watcherOver(t, w).WatchEventsJournal(context.Background(), 0, func(row journalops.Row) error {
		delivered = append(delivered, row.Seq)
		if row.Seq == 2 {
			return sentinel
		}
		return nil
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("a deliver refusal = %v, want it returned verbatim", err)
	}
	if want := []int64{1, 2}; !equalSeqs(delivered, want) {
		t.Errorf("delivered %v, want %v: delivery stops at the refusal", delivered, want)
	}
	if w.dials != 1 {
		t.Errorf("dialed %d times, want 1: a deliver refusal aborts rather than reconnecting", w.dials)
	}
}

func TestWatchReturnsCtxErrWhenTheCallerCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := &watchWire{responses: []watchResponse{{body: recordFrames(1, 2, 3)}}}

	err := watcherOver(t, w).WatchEventsJournal(ctx, 0, func(journalops.Row) error { return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled watch = %v, want context.Canceled", err)
	}
	if w.dials != 0 {
		t.Errorf("dialed %d times on a cancelled context, want 0", w.dials)
	}
}

func TestWatchSurfacesSaturationWithoutRetry(t *testing.T) {
	fastReconnect(t)
	w := &watchWire{responses: []watchResponse{{err: saturatedProblem()}}}

	err := watcherOver(t, w).WatchEventsJournal(context.Background(), 0, func(journalops.Row) error { return nil })
	if !errors.Is(err, wire.ErrEventsWatchSaturated) {
		t.Fatalf("a saturated connect = %v, want ErrEventsWatchSaturated", err)
	}
	if w.dials != 1 {
		t.Errorf("dialed %d times, want 1: saturation returns rather than retrying, because the recovery is the paged read", w.dials)
	}
}

func TestWatchReconnectsAfterATransientConnectError(t *testing.T) {
	fastReconnect(t)
	// A ConnectError is transport, not a refusal: the role waits and reconnects.
	// The first dial fails to connect, the second delivers, the third saturates
	// to end the loop.
	w := &watchWire{responses: []watchResponse{
		{err: &wire.ConnectError{Op: wire.OpWatchEvents, ServerURL: "http://serve", Err: errors.New("connection refused")}},
		{body: recordFrames(1, 2)},
		{err: saturatedProblem()},
	}}

	var delivered []int64
	err := watcherOver(t, w).WatchEventsJournal(context.Background(), 0, func(row journalops.Row) error {
		delivered = append(delivered, row.Seq)
		return nil
	})
	if !errors.Is(err, wire.ErrEventsWatchSaturated) {
		t.Fatalf("watch ended with %v, want the scripted saturation after a transient reconnect", err)
	}
	if want := []int64{1, 2}; !equalSeqs(delivered, want) {
		t.Errorf("delivered %v, want %v after reconnecting past a transient connect error", delivered, want)
	}
	if w.dials != 3 {
		t.Errorf("dialed %d times, want 3 (fail, deliver, saturate)", w.dials)
	}
}

func TestWatchStopsOnAnUndecodableRecordWithoutReconnecting(t *testing.T) {
	fastReconnect(t)
	// A record frame whose payload is not valid JSON is a POISON, not a drop:
	// reconnecting from the resume checkpoint re-fetches the same undecodable frame
	// in an unbounded 3s loop. The role must STOP with the typed poison and dial
	// exactly once, never falling into the transient-reconnect path.
	w := &watchWire{responses: []watchResponse{
		{body: "id: 1\ndata: {not-valid-json\n\n"},
	}}

	err := watcherOver(t, w).WatchEventsJournal(context.Background(), 0, func(journalops.Row) error {
		t.Fatal("delivered a record from an undecodable frame")
		return nil
	})
	if !errors.Is(err, wire.ErrStreamPoisoned) {
		t.Fatalf("an undecodable record = %v, want it typed as wire.ErrStreamPoisoned", err)
	}
	if w.dials != 1 {
		t.Errorf("dialed %d times, want 1: a poisoned record STOPS rather than reconnect-looping", w.dials)
	}
}

func TestWatchStopsOnAnOversizedEventWithoutReconnecting(t *testing.T) {
	fastReconnect(t)
	// An event past the per-event bound is a POISON too: the oversized frame sits
	// at the resume checkpoint, so a reconnect re-fetches it forever. The role must
	// STOP with the typed poison — carrying ErrResponseTooLarge as its cause — and
	// dial exactly once.
	big := "id: 1\ndata: " + strings.Repeat("x", 128) + "\n\n"
	w := &watchWire{responses: []watchResponse{
		{body: big, maxBytes: 32},
	}}

	err := watcherOver(t, w).WatchEventsJournal(context.Background(), 0, func(journalops.Row) error {
		t.Fatal("delivered a record from an oversized frame")
		return nil
	})
	if !errors.Is(err, wire.ErrStreamPoisoned) {
		t.Fatalf("an oversized event = %v, want it typed as wire.ErrStreamPoisoned", err)
	}
	if !errors.Is(err, wire.ErrResponseTooLarge) {
		t.Errorf("an oversized event = %v, want it to also carry wire.ErrResponseTooLarge", err)
	}
	if w.dials != 1 {
		t.Errorf("dialed %d times, want 1: an oversized frame STOPS rather than reconnect-looping", w.dials)
	}
}

func TestWatchStopsOnAMidStreamTruncatedEvent(t *testing.T) {
	fastReconnect(t)
	// A mid-stream in-band `truncated` event must arrive as the typed truncation
	// and STOP the watch. The served conformance can only assert this stop under an
	// undocumented retry-hint(60s) > mid-stream-timeout(20s) coupling, so the role's
	// STOP branch is pinned directly here — typed, bounded dials, timing-independent.
	problem := `{"since":40,"floor":51,"head":99}`
	w := &watchWire{responses: []watchResponse{
		{body: "event: truncated\ndata: " + problem + "\n\n"},
	}}

	err := watcherOver(t, w).WatchEventsJournal(context.Background(), 0, func(journalops.Row) error {
		t.Fatal("delivered a record from a truncated stream")
		return nil
	})
	var trunc *journalops.TruncatedError
	if !errors.As(err, &trunc) {
		t.Fatalf("a mid-stream truncation = %v (%T), want *journalops.TruncatedError", err, err)
	}
	if trunc.Since != 40 || trunc.Floor != 51 || trunc.Head != 99 {
		t.Errorf("truncation window = {%d %d %d}, want {40 51 99}", trunc.Since, trunc.Floor, trunc.Head)
	}
	if w.dials != 1 {
		t.Errorf("dialed %d times, want 1: a mid-stream truncation STOPS rather than reconnecting", w.dials)
	}
}

func equalSeqs(got, want []int64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
