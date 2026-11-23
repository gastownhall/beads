package wire

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func dialFailingClient(t *testing.T, base string, dial func(context.Context, string, string) (net.Conn, error)) *Client {
	t.Helper()
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{ResponseHeaderTimeout: testHeaderTimeout}
	if dial != nil {
		opts.HTTPClient = &http.Client{Transport: &http.Transport{DialContext: dial}, Timeout: DefaultTimeout}
	}
	c, err := New(u, nil, opts)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func requireDialError(t *testing.T, err error) *DialError {
	t.Helper()
	var ce *ConnectError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %T %v, want *ConnectError", err, err)
	}
	var de *DialError
	if !errors.As(err, &de) {
		t.Fatalf("err = %v, want a *DialError inside the *ConnectError", err)
	}
	var hte *ResponseHeaderTimeoutError
	if errors.As(err, &hte) {
		t.Fatalf("a request that never connected was reported as a header timeout: %v", err)
	}
	return de
}

// TestAConnectThatTimesOutNamesTheAddressThatNeverAnswered is the filtered
// port: the SYN goes unanswered until the dialer gives up. The error says no
// TCP connection was made to which address, and the read is not retried as a
// stall, because nothing was ever sent.
func TestAConnectThatTimesOutNamesTheAddressThatNeverAnswered(t *testing.T) {
	dials := 0
	addr := &net.TCPAddr{IP: net.ParseIP("192.0.2.10"), Port: 30699}
	c := dialFailingClient(t, "https://bd.example:30699", func(context.Context, string, string) (net.Conn, error) {
		dials++
		return nil, &net.OpError{Op: "dial", Net: "tcp", Addr: addr, Err: os.ErrDeadlineExceeded}
	})
	before := StallRetryCount()
	err := c.Do(ctx(t), Request{Op: OpGetContext, Method: http.MethodGet, Path: PathContext}, nil)
	de := requireDialError(t, err)
	if !de.Timeout() {
		t.Errorf("DialError.Timeout() = false for a connect that timed out")
	}
	msg := err.Error()
	for _, want := range []string{"cannot reach bd serve at https://bd.example:30699", "getContext: no TCP connection to 192.0.2.10:30699", "nothing answered", "i/o timeout"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not contain %q", msg, want)
		}
	}
	if strings.Contains(msg, "response headers") {
		t.Errorf("error %q blames response headers for a connect that never completed", msg)
	}
	if dials != 1 || StallRetryCount() != before {
		t.Errorf("dialed %d times with %d stall retries, want one dial and no retry", dials, StallRetryCount()-before)
	}
}

// TestARefusedConnectSaysNothingIsListening dials a real closed port.
func TestARefusedConnectSaysNothingIsListening(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a Windows refusal is WSAECONNREFUSED, which syscall.ECONNREFUSED does not match; it gets the generic connect wording")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	c := dialFailingClient(t, "http://"+addr, nil)
	err = c.Do(ctx(t), Request{Op: OpListReadyWork, Method: http.MethodGet, Path: PathReady}, nil)
	requireDialError(t, err)
	if !errors.Is(err, syscall.ECONNREFUSED) {
		t.Errorf("errors.Is(err, ECONNREFUSED) = false; the transport's error must stay reachable: %v", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "TCP connection to "+addr+" refused: nothing is listening on that port") {
		t.Errorf("error %q does not name the refused address", msg)
	}
}

func TestAHostThatDoesNotResolveIsNamed(t *testing.T) {
	c := dialFailingClient(t, "https://nowhere.invalid", func(context.Context, string, string) (net.Conn, error) {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "no such host", Name: "nowhere.invalid", IsNotFound: true}}
	})
	err := c.Do(ctx(t), Request{Op: OpListReadyWork, Method: http.MethodGet, Path: PathReady}, nil)
	de := requireDialError(t, err)
	if !de.Resolve {
		t.Errorf("DialError.Resolve = false for a name that did not resolve")
	}
	if msg := err.Error(); !strings.Contains(msg, "cannot resolve nowhere.invalid: no such host") {
		t.Errorf("error %q does not name the unresolved host", msg)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestACanceledDialIsTheCallersOwn: the caller giving up mid-connect is not a
// connect failure to explain, even when what comes back is a genuine dial error
// (here a refusal) rather than the cancellation. The round tripper hands the
// dial error back directly, as net/http does when the dial loses the race with
// the cancel, so the ctx.Err() guard is what keeps it from becoming a DialError.
func TestACanceledDialIsTheCallersOwn(t *testing.T) {
	c, cancel := context.WithCancel(context.Background())
	addr := &net.TCPAddr{IP: net.ParseIP("192.0.2.10"), Port: 30699}
	u, err := url.Parse("https://bd.example")
	if err != nil {
		t.Fatal(err)
	}
	client, err := New(u, nil, Options{
		ResponseHeaderTimeout: testHeaderTimeout,
		HTTPClient: &http.Client{Timeout: DefaultTimeout, Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			cancel()
			return nil, &net.OpError{Op: "dial", Net: "tcp", Addr: addr, Err: syscall.ECONNREFUSED}
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = client.Do(c, Request{Op: OpListReadyWork, Method: http.MethodGet, Path: PathReady}, nil)
	var de *DialError
	if errors.As(err, &de) {
		t.Fatalf("a dial the caller canceled was reported as a connect failure: %v", err)
	}
}

// TestADialSlowerThanTheHeaderTimeoutIsStillAConnectFailure: the header timer
// starts once the request is written, so a connect that takes longer than it
// and then fails is reported as the connect failure, once, not as missing
// headers retried on another connection.
func TestADialSlowerThanTheHeaderTimeoutIsStillAConnectFailure(t *testing.T) {
	dials := 0
	addr := &net.TCPAddr{IP: net.ParseIP("192.0.2.10"), Port: 30699}
	c := dialFailingClient(t, "https://bd.example:30699", func(ctx context.Context, _, _ string) (net.Conn, error) {
		dials++
		select {
		case <-time.After(2 * testHeaderTimeout):
		case <-ctx.Done():
			return nil, &net.OpError{Op: "dial", Net: "tcp", Addr: addr, Err: ctx.Err()}
		}
		return nil, &net.OpError{Op: "dial", Net: "tcp", Addr: addr, Err: os.ErrDeadlineExceeded}
	})
	before := StallRetryCount()
	err := c.Do(ctx(t), Request{Op: OpListReadyWork, Method: http.MethodGet, Path: PathReady}, nil)
	requireDialError(t, err)
	if dials != 1 || StallRetryCount() != before {
		t.Errorf("dialed %d times with %d stall retries, want one dial and no retry", dials, StallRetryCount()-before)
	}
}

// TestADialErrorKeepsTheTransportsWords: naming the failed step must not drop
// the transport's own text, which callers classify an unreachable server by.
// gc's availability tier (internal/dispatch transientNeedles) and gc hook's
// store-unavailable markers match "connection refused", "dial tcp" and
// "i/o timeout" in bd's error; a refused connect that lost those words would
// quarantine a control bead on the first refusal instead of retrying.
func TestADialErrorKeepsTheTransportsWords(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a Windows refusal is worded by WSAECONNREFUSED")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	refused := dialFailingClient(t, "http://"+addr, nil).Do(ctx(t), Request{Op: OpListReadyWork, Method: http.MethodGet, Path: PathReady}, nil)

	timeoutAddr := &net.TCPAddr{IP: net.ParseIP("192.0.2.10"), Port: 30699}
	timedOut := dialFailingClient(t, "https://bd.example:30699", func(context.Context, string, string) (net.Conn, error) {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Addr: timeoutAddr, Err: os.ErrDeadlineExceeded}
	}).Do(ctx(t), Request{Op: OpListReadyWork, Method: http.MethodGet, Path: PathReady}, nil)

	unresolved := dialFailingClient(t, "https://nowhere.invalid", func(context.Context, string, string) (net.Conn, error) {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "no such host", Name: "nowhere.invalid", IsNotFound: true}}
	}).Do(ctx(t), Request{Op: OpListReadyWork, Method: http.MethodGet, Path: PathReady}, nil)

	for _, tc := range []struct {
		name string
		err  error
		want []string
	}{
		{"refused", refused, []string{"refused: nothing is listening on that port", "dial tcp " + addr, "connection refused"}},
		{"timed out", timedOut, []string{"nothing answered", "dial tcp 192.0.2.10:30699", "i/o timeout"}},
		{"unresolved", unresolved, []string{"cannot resolve nowhere.invalid", "dial tcp", "no such host"}},
	} {
		requireDialError(t, tc.err)
		msg := tc.err.Error()
		for _, want := range tc.want {
			if !strings.Contains(msg, want) {
				t.Errorf("%s: error %q does not contain %q", tc.name, msg, want)
			}
		}
		if strings.Contains(msg, `Get "`) {
			t.Errorf("%s: error %q still names the request", tc.name, msg)
		}
	}
}
