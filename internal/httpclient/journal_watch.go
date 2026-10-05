// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/journal_watch.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/journalops"
)

// The DURABLE MUTATION JOURNAL, PUSHED over GET /v0/beads/events:watch.
//
// THERE IS NO ACCESSOR HERE either, and its absence is the same design journal.go
// states for the paged read: journalops.Watcher is not on storage.DoltStorage,
// so a backend publishes the watch by IMPLEMENTING the interface a caller
// type-asserts for. This is the PUSH spelling of the read entitlement journal.go
// already holds — the same exclusive checkpoint, the same records, the same
// typed truncation — and it carries neither the prune (EventsJournalAccessor)
// nor the activation (EventsJournalConfigurer) for the same reason: a client of a
// remote workspace has no workspace and no engine, only a read of one.
// cmd/bd/enterprise_events_journal_refusal_test.go pins that *Store implements
// this watch role and still implements neither operator half.

// StreamWire is the transport surface the WATCH role dispatches through: the one
// streaming operation on the v0 surface.
//
// It is a separate interface from WireClient's read pair rather than a third
// method on it because a stream is neither a buffered Do nor a bare Preflight —
// its result is a live *wire.EventStream held open for as long as the consumer
// wants it. WireClient embeds it, so a store still holds one transport and the
// compiler still proves a build linked one; *wire.Client satisfies it verbatim.
type StreamWire interface {
	// WatchEvents opens the journal stream from since in one connect, returning
	// a live stream or the typed error the server answered before it opened.
	WatchEvents(ctx context.Context, since int64) (*wire.EventStream, error)
}

// watchReconnectDelay is the wait before a reconnect when the server named no
// delay of its own — neither a Retry-After on a 503 nor a `retry:` advisory on
// the stream it just dropped. It matches the server's own default reconnection
// advice (internal/httpapi's eventsWatchRetry, 3s): long enough that a server
// restart does not draw a reconnect storm, short enough that a consumer's lag
// after one is measured in seconds.
//
// It is a var rather than a const so the stub-tier test can drive a reconnect in
// milliseconds instead of waiting whole seconds for one.
var watchReconnectDelay = 3 * time.Second

var _ journalops.Watcher = (*Store)(nil)

// WatchEventsJournal delivers every committed record after since, in seq order,
// gapless, until ctx is done, deliver refuses, or the watch fails typed.
//
// RECONNECTION LIVES IN THIS ROLE, AND THAT IS THE ONE DESIGN DECISION WORTH
// STATING. A dropped stream — a reset, a proxy idle-out, the server restarting —
// is absorbed here by reconnecting from the last DELIVERED seq, so the delivery a
// consumer sees is exactly-once across a reconnect: no record is replayed and
// none is skipped, because `since` is the contract and the resume point is the
// seq this loop last handed to deliver. The alternative — surfacing every
// disconnect to the caller — was rejected because the server calls reconnection
// normal and free (it keeps no per-consumer state; a stream is a sequence of
// `since` reads), so every consumer would otherwise reimplement the identical
// checkpoint bookkeeping this loop does once. The trade is that a caller cannot
// observe a transient disconnect; what it observes instead is uninterrupted,
// duplicate-free delivery, which is what a replay feed is for.
//
// WHAT IS NOT ABSORBED is returned, because none of it is a stream to silently
// re-open: deliver's own error (verbatim), the caller's ctx cancellation, a
// *journalops.TruncatedError when the resume point falls below the retained
// window (the consumer decides between the gap and a rebuild — journalops states
// why this role cannot), a saturation refusal (the paged read is never refused
// for this reason, so the consumer polls journal.go instead), the activation
// refusal of a journal-off workspace, and the capability, identity and
// api-version refusals a wrong or too-old server answers before any stream byte.
func (s *Store) WatchEventsJournal(ctx context.Context, since int64, deliver func(journalops.Row) error) error {
	if s.wire == nil {
		return fmt.Errorf("%w: cannot watch %s", ErrNoTransport, s.target)
	}

	resume := since
	reconnectIn := watchReconnectDelay
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		// The two-speed policy, run before every dial: events.watch is
		// post-baseline, so a server that does not advertise it refuses here from
		// the cached handshake rather than dialing an unrouted path and reading a
		// bare 404 as an entity's not_found. A *wire.CapabilityError returns at
		// once; the cached snapshot makes every later reconnect's preflight free.
		if err := s.wire.Preflight(ctx, wire.OpWatchEvents); err != nil {
			return err
		}

		stream, err := s.wire.WatchEvents(ctx, resume)
		if err != nil {
			wait, retry := watchConnectDisposition(err, reconnectIn)
			if !retry {
				return eventsJournalError(err)
			}
			if sleepErr := sleepWithContext(ctx, wait); sleepErr != nil {
				return sleepErr
			}
			continue
		}

		next, derr := s.drainWatchStream(stream, resume, deliver)
		resume = next
		if hint := stream.Retry(); hint > 0 {
			reconnectIn = hint
		}
		stream.Close()

		// drainWatchStream always ends on an error — the stream's own Next never
		// returns a nil error and a payload together. The classification decides
		// return-versus-reconnect, and a deliver refusal or a mid-stream truncation
		// wins over an incidental ctx cancellation: a consumer that aborted from its
		// own deliver wants that error back, not the ctx it may also have closed.
		switch {
		case isWatchReturn(derr):
			return watchReturnUnwrap(derr)
		case ctx.Err() != nil:
			return ctx.Err()
		default:
			// A transport drop or an idle timeout: reconnect from the resume point.
		}
		if sleepErr := sleepWithContext(ctx, reconnectIn); sleepErr != nil {
			return sleepErr
		}
	}
}

// drainWatchStream reads one open stream to its end, delivering each record and
// advancing the resume point past every record it hands off.
//
// It returns the resume point in every case, so the caller reconnects from the
// last DELIVERED seq. Its error tells the caller what ended the stream: a
// *watchDeliverError when deliver refused, a *journalops.TruncatedError on the
// mid-stream `truncated` event, and otherwise the raw stream error (an EOF, an
// idle timeout, the caller's ctx) the caller classifies.
func (s *Store) drainWatchStream(stream *wire.EventStream, resume int64, deliver func(journalops.Row) error) (int64, error) {
	for {
		ev, err := stream.Next()
		if err != nil {
			// A per-event size overrun is a POISON, not a drop: the record that
			// exceeded the bound sits at the resume checkpoint, so reconnecting from
			// it re-fetches the same oversized frame forever. Return it typed —
			// errors.Is reaches wire.ErrStreamPoisoned, same as the undecodable-record
			// path below — so the role STOPS; every other Next error (an EOF, an idle
			// timeout, a reset) is a transport transient the caller reconnects from.
			if isStreamPoison(err) {
				return resume, &watchReturn{err: fmt.Errorf("%w: %w", wire.ErrStreamPoisoned, err)}
			}
			return resume, err
		}

		if ev.Name == watchTruncatedEventName {
			return resume, watchTruncatedError(ev.Data)
		}
		if ev.Name != "" {
			// An event this client does not name is one it must not read as a
			// record. Only `truncated` is named; anything else is skipped so a
			// future named event cannot be mis-decoded as a mutation.
			continue
		}

		var record apigen.EventRecord
		if err := json.Unmarshal(ev.Data, &record); err != nil {
			// A record that will not decode is a POISON, not a drop: it sits at the
			// resume checkpoint, so reconnecting from it re-fetches the same
			// undecodable frame and pins the watcher on an unbounded reconnect loop.
			// Return it TYPED (errors.Is reaches wire.ErrStreamPoisoned) inside a
			// watchReturn so the role STOPS rather than skipping a mutation or looping.
			return resume, &watchReturn{err: fmt.Errorf("%s: decoding a journal record from bd serve at %s: %w: %w", wire.OpWatchEvents, s.target, wire.ErrStreamPoisoned, err)}
		}

		row := journalRow(record)
		// The monotonicity guard, which is what makes a reconnect exactly-once. A
		// reconnect resumes from `resume`, which the server treats as an exclusive
		// checkpoint, so no record at or below it should arrive — but a guard here
		// makes a duplicate impossible rather than merely unlikely, and a server
		// that echoed the resume row cannot make this loop deliver it twice.
		if row.Seq <= resume {
			continue
		}
		if err := deliver(row); err != nil {
			return resume, &watchDeliverError{err: err}
		}
		resume = row.Seq
	}
}

// watchTruncatedEventName is the wire's name for the one event that carries a
// prune racing an open stream (internal/httpapi's truncatedEventName). It is
// respelled here rather than imported for the reason the operation ids are:
// importing internal/httpapi would drag the storage engine into a client.
const watchTruncatedEventName = "truncated"

// watchConnectDisposition classifies a connect failure into a wait-and-reconnect
// or a return.
//
// A *wire.ConnectError is transport — DNS, dial, a reset before headers — and is
// transient, so it reconnects after the default delay. A 503 that is busy or
// db-unavailable is transient too, honoring the server's Retry-After when it sent
// one. Everything else returns: the saturation refusal (the caller polls the
// paged read), the disabled and truncated refusals, and every capability,
// identity and api-version refusal.
func watchConnectDisposition(err error, dflt time.Duration) (time.Duration, bool) {
	var connErr *wire.ConnectError
	if errors.As(err, &connErr) {
		return dflt, true
	}
	if errors.Is(err, wire.ErrEventsWatchSaturated) ||
		errors.Is(err, wire.ErrEventsJournalDisabled) ||
		errors.Is(err, wire.ErrEventsJournalTruncated) {
		return 0, false
	}
	var prob *wire.ProblemError
	if errors.As(err, &prob) && prob.Retryable() {
		if prob.RetryAfter > 0 {
			return prob.RetryAfter, true
		}
		return dflt, true
	}
	return 0, false
}

// isStreamPoison reports whether a mid-stream Next error is a PROTOCOL poison —
// a frame past the per-event bound — rather than a transport transient. A poison
// re-fetches from the same resume checkpoint on every reconnect, so the role
// STOPS on it; only genuine drops (EOF, idle timeout, reset) keep the reconnect
// path. The undecodable-record poison is caught in drainWatchStream at the decode
// site, where the raw json error is in hand.
func isStreamPoison(err error) bool {
	var tooLarge *wire.StreamEventTooLargeError
	return errors.As(err, &tooLarge)
}

// watchTruncatedError rebuilds the role's typed truncation from the problem
// document the mid-stream `truncated` event carried.
//
// It is the same shape as eventsJournalError on the paged read: the window's
// three members travel together, so a document missing any of them is one this
// client cannot reconstruct a window from, and the wire's sentinel travels
// instead of a window with a fabricated bound. A consumer dispatches on the TYPE
// and reads the window off the fields.
func watchTruncatedError(data []byte) error {
	var p apigen.Problem
	if err := json.Unmarshal(data, &p); err != nil {
		return &watchReturn{err: fmt.Errorf("%s: decoding the truncation from bd serve: %w", wire.OpWatchEvents, err)}
	}
	if p.Since == nil || p.Floor == nil || p.Head == nil {
		return &watchReturn{err: wire.ErrEventsJournalTruncated}
	}
	return &watchReturn{err: &journalops.TruncatedError{Since: *p.Since, Floor: *p.Floor, Head: *p.Head}}
}

// watchReturn wraps a drain error that must be RETURNED rather than reconnected
// from. It exists so drainWatchStream's caller can tell a mid-stream truncation
// from a transport drop without inspecting the wrapped type twice.
type watchReturn struct{ err error }

func (e *watchReturn) Error() string { return e.err.Error() }
func (e *watchReturn) Unwrap() error { return e.err }

// watchDeliverError wraps deliver's own refusal, which the role returns verbatim
// to its caller — the record projection and the transport are out of the way, so
// the error the consumer sees is exactly the one its deliver produced.
type watchDeliverError struct{ err error }

func (e *watchDeliverError) Error() string { return e.err.Error() }
func (e *watchDeliverError) Unwrap() error { return e.err }

// isWatchReturn reports whether a drain error is one to return rather than
// reconnect from.
func isWatchReturn(err error) bool {
	var ret *watchReturn
	var del *watchDeliverError
	return errors.As(err, &ret) || errors.As(err, &del)
}

// watchReturnUnwrap strips the role's own wrappers so the caller sees the error
// the drain actually produced: the typed truncation or deliver's verbatim
// refusal.
func watchReturnUnwrap(err error) error {
	var ret *watchReturn
	if errors.As(err, &ret) {
		return ret.err
	}
	var del *watchDeliverError
	if errors.As(err, &del) {
		return del.err
	}
	return err
}
