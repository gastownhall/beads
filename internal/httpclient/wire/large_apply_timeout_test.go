// Contributed by gascity from bd-enterprise (internal/httpclient/wire/ent_large_apply_timeout_test.go@1ee63dff5)
// to OSS beads under the MIT license.
package wire

import (
	"errors"
	"net"
	"net/http"
	"testing"
	"time"
)

// A batchApply request over the server's large-apply threshold is dialed under
// LargeApplyTimeout, not the client's ordinary per-request ceiling.
//
// The fake server answers every write after slowWrite, which is longer than the
// injected client's own Timeout (shortCeiling) and far shorter than
// LargeApplyTimeout. So a request that stays under the client's ordinary
// ceiling fails with a Client.Timeout, and one the client raised to
// LargeApplyTimeout completes: the same shape as a real 1000-item apply on a
// loaded server, where bd serve grants five minutes and the client used to give
// up at DefaultTimeout (60s) while the server went on to commit.
const (
	shortCeiling = 200 * time.Millisecond
	slowWrite    = 1 * time.Second
)

func slowWrites(contextJSON string) http.HandlerFunc {
	fast := serveEverything(contextJSON)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != PathContext {
			select {
			case <-time.After(slowWrite):
			case <-r.Context().Done():
				return
			}
		}
		fast(w, r)
	}
}

// newSlowWriteClient is a client whose own ceiling is shortCeiling, aimed at a
// server that answers every write after slowWrite. The handshake is primed
// under DefaultTimeout BEFORE the ceiling shrinks, so the only request
// shortCeiling ever bounds is the write under test: a context fetch stalled
// past it on a loaded host would otherwise fail an over-threshold case for the
// wrong reason, or hand an at-threshold control the *ConnectError it looks for.
func newSlowWriteClient(t *testing.T, caps ...string) (*Client, *recorder) {
	t.Helper()
	c, rec := newTestClient(t, Options{HTTPClient: &http.Client{Timeout: DefaultTimeout}}, nil,
		slowWrites(contextBody("v0", "proj-1", caps...)))
	if _, err := c.Handshake(ctx(t)); err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	c.hc.Timeout = shortCeiling
	return c, rec
}

// wantClientTimeout requires err to be the client's own ceiling expiring on
// the op write itself: not the handshake, and not any other failure to connect.
func wantClientTimeout(t *testing.T, what, op string, err error) {
	t.Helper()
	var connErr *ConnectError
	if !errors.As(err, &connErr) {
		t.Fatalf("%s: err = %v (%T), want *ConnectError from the client's own ceiling", what, err, err)
	}
	if connErr.Op != op {
		t.Fatalf("%s: the %s request failed (%v); want the %s write to reach the ceiling", what, connErr.Op, err, op)
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("%s: err = %v, want the client's own timeout", what, err)
	}
}

func TestApplyBatchOverTheLargeApplyThresholdOutlivesTheClientCeiling(t *testing.T) {
	c, rec := newSlowWriteClient(t, withBatchApply(CapBatchApplyLarge)...)
	n := LargeApplyItemThreshold + 1
	if _, err := c.ApplyBatch(ctx(t), applyBatchRequestOfSize(n)); err != nil {
		t.Fatalf("ApplyBatch(%d items) against a server slower than the client's %v ceiling: %v; "+
			"a large apply must be dialed under LargeApplyTimeout", n, shortCeiling, err)
	}
	if got := rec.at(t, rec.count()-1).path; got != PathIssuesBatchApply {
		t.Errorf("last request = %q, want %q", got, PathIssuesBatchApply)
	}
}

func TestApplyBatchAtTheLargeApplyThresholdKeepsTheClientCeiling(t *testing.T) {
	// The control: at (not over) the threshold the server runs the request
	// under its ORDINARY deadline, so the client keeps its ordinary ceiling
	// and the same slow server times it out.
	c, _ := newSlowWriteClient(t, withBatchApply(CapBatchApplyLarge)...)
	_, err := c.ApplyBatch(ctx(t), applyBatchRequestOfSize(LargeApplyItemThreshold))
	wantClientTimeout(t, "ApplyBatch at the threshold", OpApplyBatch, err)
}

func TestRequestTimeoutOnlyRaisesABoundedClient(t *testing.T) {
	transport := &http.Transport{}
	bounded := &http.Client{Timeout: DefaultTimeout, Transport: transport}
	c, _ := newTestClient(t, Options{HTTPClient: bounded}, nil, serveEverything("{}"))

	if got := c.httpClientFor(Request{}); got != c.hc {
		t.Error("a request with no Timeout must use the client's own *http.Client")
	}
	if got := c.httpClientFor(Request{Timeout: time.Second}); got != c.hc {
		t.Error("a Timeout SHORTER than the client's must not lower it (that is the context's job)")
	}
	raised := c.httpClientFor(Request{Timeout: LargeApplyTimeout})
	if raised.Timeout != LargeApplyTimeout {
		t.Errorf("raised Timeout = %v, want %v", raised.Timeout, LargeApplyTimeout)
	}
	if raised.Transport != transport {
		t.Error("the raised client must share the client's Transport (its connection pool and CA scope)")
	}
	if raised.CheckRedirect == nil || !errors.Is(raised.CheckRedirect(nil, nil), http.ErrUseLastResponse) {
		t.Error("the raised client must keep the forced CheckRedirect that refuses every redirect")
	}
	if c.hc.Timeout != DefaultTimeout {
		t.Errorf("raising one request mutated the client's own Timeout to %v", c.hc.Timeout)
	}

	unbounded, _ := newTestClient(t, Options{HTTPClient: &http.Client{}}, nil, serveEverything("{}"))
	if got := unbounded.httpClientFor(Request{Timeout: LargeApplyTimeout}); got.Timeout != 0 {
		t.Errorf("an unbounded client was given a %v ceiling; it must stay unbounded", got.Timeout)
	}
}

func TestLargeApplyTimeoutIsLongerThanTheDefault(t *testing.T) {
	if LargeApplyTimeout <= DefaultTimeout {
		t.Fatalf("LargeApplyTimeout = %v must exceed DefaultTimeout = %v", LargeApplyTimeout, DefaultTimeout)
	}
	if largeApplyTimeout(LargeApplyItemThreshold) != 0 || largeApplyTimeout(LargeApplyItemThreshold+1) != LargeApplyTimeout {
		t.Fatal("largeApplyTimeout must switch exactly when the item count EXCEEDS LargeApplyItemThreshold")
	}
}
