package wire

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http/httptrace"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// DefaultResponseHeaderTimeout bounds how long the first attempt of a
// replayable request waits for the server's response HEADERS once the
// request has been written, separately from DefaultTimeout's bound on the
// whole exchange.
//
// It exists for the stuck connection the maintainer-city shadow run found: a
// single HTTP/1.1 connection that went silent while other connections to the
// same serve kept answering, so each request on it sat out the full 60s
// overall timeout. The timer cannot tell that connection from a serve that is
// still working: serve writes a read's headers only once the handler has its
// answer, after waiting up to 10s for a database slot (a wait that ends in a
// 503, which is headers). 15s sits above that wait, but a large read can take
// longer and still be healthy. That is why the retry gets no header timer of
// its own and runs on what is left of the overall timeout (see
// Client.roundTrip): such a read still finishes, later and at the cost of one
// abandoned request, unless the retry's headers and body together take longer
// than what is left, about DefaultTimeout minus this.
//
// Only the headers are bounded: once they arrive the timer is stopped, and a
// slow body that keeps streaming is governed by DefaultTimeout (and the
// caller's context) alone.
//
// The timer applies to replayable requests (see Request.Replayable). Any other
// write is bounded by the client's overall timeout instead, because bd serve
// answers a write only after it commits; see roundTrip.
const DefaultResponseHeaderTimeout = 15 * time.Second

// ErrResponseHeaderTimeout is the sentinel a *ResponseHeaderTimeoutError
// unwraps to.
var ErrResponseHeaderTimeout = errors.New("bd serve sent no response headers in time")

// ResponseHeaderTimeoutError is a request that was written to the server and
// got no response headers in time: its first attempt hit the header timer, or
// the overall timeout ran out while it waited. A request that never left the
// machine (a dial or TLS handshake that timed out) is not one; it stays a
// plain *ConnectError, because the server cannot have seen it. It is returned
// inside a *ConnectError (the server said nothing), so callers that classify
// an unreachable server keep working; errors.As reaches this type for the
// detail.
type ResponseHeaderTimeoutError struct {
	Op     string
	Method string
	// Host is the server's host[:port], named so the operator knows which
	// server to look at.
	Host string
	// After is how long the request waited: the header timeout when its timer
	// fired, else the measured wait (from when the first attempt was sent, for
	// a retried read).
	After time.Duration
	// Attempts is how many requests went out: 1, or 2 for a read whose retry
	// on another connection got no headers either.
	Attempts int
	// Retried reports whether a retry on another connection was made.
	Retried bool
	// Reason is why the first attempt was abandoned, when Retried:
	// StallHeaderTimeout or StallConnectionReset.
	Reason string
	// Replayable reports whether the request was idempotent. One that is not
	// was never retried and may or may not have been applied.
	Replayable bool
	// Err is the transport's own error, when the overall timeout (not the
	// header timer) is what fired.
	Err error
}

func (e *ResponseHeaderTimeoutError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: no response headers from %s within %s", e.Op, e.Host, e.After.Round(10*time.Millisecond))
	if e.Retried {
		first := "got no headers within the header timeout"
		if e.Reason == StallConnectionReset {
			first = "had its connection reset"
		}
		fmt.Fprintf(&b, " on %d attempts (the first %s; the retry used another connection)", e.Attempts, first)
	}
	if e.Err != nil {
		fmt.Fprintf(&b, " (%v)", e.Err)
	}
	if !e.Replayable {
		fmt.Fprintf(&b, "; the %s was not retried because it is not idempotent (it may or may not have been applied)", e.Method)
	}
	fmt.Fprintf(&b, "; check the server at %s (is it at its connection limit?)", e.Host)
	return b.String()
}

func (e *ResponseHeaderTimeoutError) Unwrap() []error {
	if e.Err != nil {
		return []error{ErrResponseHeaderTimeout, e.Err}
	}
	return []error{ErrResponseHeaderTimeout}
}

// Timeout reports true, so net.Error-style timeout checks classify it.
func (e *ResponseHeaderTimeoutError) Timeout() bool { return true }

// Temporary is part of the net.Error interface. It is false for a request
// that is not replayable, so a caller that retries whatever reports itself
// temporary never resends a write that may already have been applied.
func (e *ResponseHeaderTimeoutError) Temporary() bool { return e.Replayable }

// isTimeout reports whether err is a timeout (net.Error, or the client's own
// Timeout surfacing through *url.Error).
func isTimeout(err error) bool {
	var te interface{ Timeout() bool }
	return errors.As(err, &te) && te.Timeout()
}

// Reasons a StallRetry names.
const (
	StallHeaderTimeout   = "response_header_timeout"
	StallConnectionReset = "connection_reset"
)

// StallRetry describes one retry of an idempotent request on another
// connection after the first attempt stalled (no headers within the header
// timeout) or had its connection reset. It is reported, never swallowed: a retry that
// hides a sick server is a stall nobody sees.
type StallRetry struct {
	Op     string
	Method string
	Host   string
	Reason string
	// Retry is the 1-based retry number for this request (always 1 today:
	// one retry per request).
	Retry int
	// Total is the process-wide count of stall retries, this one included.
	Total uint64
	// Err is the first attempt's failure.
	Err error
}

func (e StallRetry) String() string {
	return fmt.Sprintf("bd: http %s %s to %s stalled (%s); retry %d on another connection (%d stall retries in this process): %v",
		e.Op, e.Method, e.Host, e.Reason, e.Retry, e.Total, e.Err)
}

var stallRetries atomic.Uint64

// StallRetryCount is the number of stall retries this process has made.
func StallRetryCount() uint64 { return stallRetries.Load() }

// stallReason classifies a failed attempt as retryable on another
// connection, returning "" when it is not.
func stallReason(err error) string {
	var ce *ConnectError
	if !errors.As(err, &ce) {
		// Only a failure to get an answer is a stall; a credential source's
		// own error that happens to wrap io.EOF is not.
		return ""
	}
	switch {
	case errors.Is(err, ErrResponseHeaderTimeout):
		return StallHeaderTimeout
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		return StallConnectionReset
	}
	return ""
}

// headerTimer bounds one attempt's wait for response headers. It starts once
// the request has been written (httptrace's WroteRequest, where
// http.Transport.ResponseHeaderTimeout starts its own clock), so a slow dial,
// TLS handshake or credential source is never counted as a server that went
// silent. When it fires it cancels the attempt's context, which makes
// net/http close that connection rather than return it to the idle pool, so
// a retry can never ride it. It also records when the request was written,
// which is what tells a write that timed out awaiting its headers from one
// that never left the machine.
type headerTimer struct {
	d      time.Duration
	cancel context.CancelCauseFunc

	mu      sync.Mutex
	state   int       // headerWaiting, headerSettled or headerTimedOut
	wroteAt time.Time // zero until the request has been written
	timer   *time.Timer
}

const (
	headerWaiting  = iota
	headerSettled  // Do returned before the timer fired
	headerTimedOut // the timer fired first
)

// startHeaderTimer returns the attempt's context, which carries the trace hook
// that arms a timer of d (none when d <= 0) once the request is written.
func startHeaderTimer(ctx context.Context, d time.Duration) (context.Context, *headerTimer) {
	attemptCtx, cancel := context.WithCancelCause(ctx)
	h := &headerTimer{d: d, cancel: cancel}
	return httptrace.WithClientTrace(attemptCtx, &httptrace.ClientTrace{WroteRequest: h.wroteRequest}), h
}

// wroteRequest runs on the transport's writing goroutine. A failed write
// records nothing (the attempt fails on its own), and only the first
// successful one counts: the transport may resend a request it got no answer
// to on a connection it found dead, and the wait is measured from the first.
func (h *headerTimer) wroteRequest(info httptrace.WroteRequestInfo) {
	if info.Err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.wroteAt.IsZero() {
		return
	}
	h.wroteAt = time.Now()
	if h.d > 0 && h.state == headerWaiting {
		h.timer = time.AfterFunc(h.d, h.fire)
	}
}

func (h *headerTimer) fire() {
	h.mu.Lock()
	fired := h.state == headerWaiting
	if fired {
		h.state = headerTimedOut
	}
	h.mu.Unlock()
	if fired {
		h.cancel(ErrResponseHeaderTimeout)
	}
}

// stop must be called as soon as Do returns. It disarms the timer and reports
// whether it had already fired (the headers lost the race).
func (h *headerTimer) stop() (timedOut bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.timer != nil {
		h.timer.Stop()
	}
	if h.state == headerWaiting {
		h.state = headerSettled
	}
	return h.state == headerTimedOut
}

// written reports when the request was written, if it was.
func (h *headerTimer) written() (time.Time, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.wroteAt, !h.wroteAt.IsZero()
}

// done releases the attempt's context. Call it once the body has been read.
func (h *headerTimer) done() { h.cancel(nil) }
