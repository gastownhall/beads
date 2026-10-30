package wire

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/netutil"
)

// stallServer answers every request normally except those that arrive on the
// FIRST connection it ever sees: those get no response headers at all, the
// shape of the shadow run's stuck HTTP/1.1 connection (a silent connection
// while other connections to the same serve kept answering). The hung handler
// returns when the client gives up on the connection.
type stallServer struct {
	srv *httptest.Server

	mu      sync.Mutex
	first   string   // RemoteAddr of the first connection
	remotes []string // RemoteAddr of every request, in order
	methods []string
	hang    func(w http.ResponseWriter, r *http.Request)
}

func newStallServer(t *testing.T, answer http.HandlerFunc) *stallServer {
	t.Helper()
	s := &stallServer{}
	s.hang = func(_ http.ResponseWriter, r *http.Request) {
		// Drain the body first: net/http only notices the client closing the
		// connection (and cancels r.Context) once the body has been read.
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		if s.first == "" {
			s.first = r.RemoteAddr
		}
		stuck := r.RemoteAddr == s.first
		s.remotes = append(s.remotes, r.RemoteAddr)
		s.methods = append(s.methods, r.Method)
		hang := s.hang
		s.mu.Unlock()
		if stuck {
			hang(w, r)
			return
		}
		answer(w, r)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *stallServer) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.remotes...)
}

func (s *stallServer) client(t *testing.T, opts Options) *Client {
	t.Helper()
	base, err := url.Parse(s.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if opts.HTTPClient == nil {
		// The production shape: a pooled transport with the package's own 60s
		// overall ceiling, which the header timeout must undercut.
		opts.HTTPClient = &http.Client{Transport: s.srv.Client().Transport, Timeout: DefaultTimeout}
	}
	c, err := New(base, nil, opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

const testHeaderTimeout = 300 * time.Millisecond

func TestAStalledIdempotentReadRetriesOnceOnAnotherConnection(t *testing.T) {
	s := newStallServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	var events []StallRetry
	c := s.client(t, Options{
		ResponseHeaderTimeout: testHeaderTimeout,
		OnStallRetry:          func(e StallRetry) { events = append(events, e) },
	})
	before := StallRetryCount()

	start := time.Now()
	var out map[string]any
	if err := c.Do(ctx(t), Request{Op: OpGetContext, Method: http.MethodGet, Path: PathContext}, &out); err != nil {
		t.Fatalf("Do: %v", err)
	}
	elapsed := time.Since(start)

	if out["ok"] != true {
		t.Fatalf("body = %v, want the second connection's answer", out)
	}
	// One header timeout, then one prompt retry: well under two timeouts, and
	// nowhere near the 60s overall ceiling the shadow run hit.
	if elapsed < testHeaderTimeout || elapsed > 2*testHeaderTimeout+time.Second {
		t.Errorf("elapsed %v, want about one header timeout (%v)", elapsed, testHeaderTimeout)
	}
	seen := s.seen()
	if len(seen) != 2 {
		t.Fatalf("server saw %d requests, want 2 (the stalled one and one retry): %v", len(seen), seen)
	}
	if seen[0] == seen[1] {
		t.Errorf("the retry rode the stuck connection %s; it must use another one", seen[0])
	}
	if len(events) != 1 {
		t.Fatalf("OnStallRetry fired %d times, want 1: %+v", len(events), events)
	}
	e := events[0]
	if e.Op != OpGetContext || e.Method != http.MethodGet || e.Retry != 1 || e.Reason != StallHeaderTimeout {
		t.Errorf("event = %+v, want op %s, GET, retry 1, reason %q", e, OpGetContext, StallHeaderTimeout)
	}
	if !strings.Contains(e.String(), "retry 1") || !strings.Contains(e.String(), e.Host) {
		t.Errorf("event line %q should carry the retry count and the host", e.String())
	}
	if got := StallRetryCount() - before; got != 1 {
		t.Errorf("StallRetryCount advanced by %d, want 1", got)
	}
}

func TestAnExplicitlyIdempotentPostReadRetriesToo(t *testing.T) {
	s := newStallServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	})
	c := s.client(t, Options{ResponseHeaderTimeout: testHeaderTimeout})
	err := c.Do(ctx(t), Request{Op: OpBatchGetIssues, Method: http.MethodPost, Path: PathIssuesBatchGet,
		Body: map[string]any{"ids": []string{"x"}}, Idempotent: true}, nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if n := len(s.seen()); n != 2 {
		t.Fatalf("server saw %d requests, want 2", n)
	}
}

func TestAStalledWriteIsNeverRetriedAndSurfacesATypedTimeout(t *testing.T) {
	s := newStallServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	})
	fired := 0
	// A write is bounded by the client's overall timeout, not the header
	// timer; shrink the overall one so the stall surfaces quickly.
	c := s.client(t, Options{
		HTTPClient:            &http.Client{Transport: s.srv.Client().Transport, Timeout: 2 * testHeaderTimeout},
		ResponseHeaderTimeout: testHeaderTimeout,
		OnStallRetry:          func(StallRetry) { fired++ },
	})

	start := time.Now()
	err := c.Do(ctx(t), Request{Op: OpCreateIssue, Method: http.MethodPost, Path: PathIssues,
		Body: map[string]any{"title": "t"}}, nil)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("Do succeeded; want the header timeout")
	}
	var hte *ResponseHeaderTimeoutError
	if !errors.As(err, &hte) {
		t.Fatalf("err = %T %v, want *ResponseHeaderTimeoutError", err, err)
	}
	if !errors.Is(err, ErrResponseHeaderTimeout) {
		t.Errorf("err does not unwrap to ErrResponseHeaderTimeout: %v", err)
	}
	var ce *ConnectError
	if !errors.As(err, &ce) {
		t.Errorf("err should still be a *ConnectError for callers that classify unreachable servers: %v", err)
	}
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Errorf("err should report Timeout() = true: %v", err)
	}
	if hte.Retried || hte.Attempts != 1 || hte.Method != http.MethodPost {
		t.Errorf("hte = %+v, want one unretried POST attempt", hte)
	}
	if hte.Temporary() {
		t.Error("a write's timeout reports Temporary() = true, so a generic net.Error retry would resend it")
	}
	// The measured wait since the request was written, not a configured
	// number, and the transport's own error rather than nothing.
	if hte.After < testHeaderTimeout || hte.After > elapsed || hte.Err == nil {
		t.Errorf("hte.After = %v, hte.Err = %v; want the measured wait (at most %v) and the transport's error", hte.After, hte.Err, elapsed)
	}
	host := s.srv.Listener.Addr().String()
	msg := err.Error()
	for _, want := range []string{host, "not retried", "may or may not have been applied", "Client.Timeout exceeded", "check the server"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q should contain %q", msg, want)
		}
	}
	if n := len(s.seen()); n != 1 {
		t.Fatalf("server saw %d requests, want exactly 1 (a write is never replayed)", n)
	}
	if fired != 0 {
		t.Errorf("OnStallRetry fired %d times for a write", fired)
	}
	if elapsed < 2*testHeaderTimeout || elapsed > 2*testHeaderTimeout+time.Second {
		t.Errorf("elapsed %v; the write should fail at the overall timeout (%v)", elapsed, 2*testHeaderTimeout)
	}
}

// TestASlowWriteIsNotCutOffByTheHeaderTimeout: bd serve answers a write only
// after it commits, so a write whose headers take longer than the header
// timeout is a slow but healthy write, and must complete.
func TestASlowWriteIsNotCutOffByTheHeaderTimeout(t *testing.T) {
	c, rec := newTestClient(t, Options{ResponseHeaderTimeout: testHeaderTimeout}, nil, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		time.Sleep(3 * testHeaderTimeout)
		_, _ = w.Write([]byte(`{}`))
	})
	err := c.Do(ctx(t), Request{Op: OpCreateIssue, Method: http.MethodPost, Path: PathIssues,
		Body: map[string]any{"title": "t"}}, nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if n := rec.count(); n != 1 {
		t.Errorf("server saw %d requests, want 1", n)
	}
}

// TestAReadThatStallsTwiceGivesUpAfterOneRetry: the retry has no header timer
// of its own, so a read whose retry gets no headers either fails once the
// overall timeout is spent, counted from the first attempt, and not later.
func TestAReadThatStallsTwiceGivesUpAfterOneRetry(t *testing.T) {
	var mu sync.Mutex
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n++
		mu.Unlock()
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL)
	budget := 4 * testHeaderTimeout
	c, err := New(base, nil, Options{
		HTTPClient:            &http.Client{Transport: srv.Client().Transport, Timeout: budget},
		ResponseHeaderTimeout: testHeaderTimeout,
	})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err = c.Do(ctx(t), Request{Op: OpGetContext, Method: http.MethodGet, Path: PathContext}, nil)
	elapsed := time.Since(start)
	var hte *ResponseHeaderTimeoutError
	if !errors.As(err, &hte) {
		t.Fatalf("err = %v, want *ResponseHeaderTimeoutError", err)
	}
	if !hte.Retried || hte.Attempts != 2 || hte.Reason != StallHeaderTimeout || !hte.Temporary() {
		t.Errorf("hte = %+v, want 2 attempts with the retry and its reason recorded", hte)
	}
	if elapsed < budget || elapsed > budget+time.Second {
		t.Errorf("elapsed %v, want the overall timeout (%v) and no more", elapsed, budget)
	}
	if hte.After < budget || hte.After > elapsed {
		t.Errorf("hte.After = %v, want the whole wait (between %v and %v)", hte.After, budget, elapsed)
	}
	if msg := err.Error(); !strings.Contains(msg, "on 2 attempts (the first got no headers within the header timeout") {
		t.Errorf("error %q should say both attempts went unanswered", msg)
	}
	mu.Lock()
	defer mu.Unlock()
	if n != 2 {
		t.Errorf("server saw %d requests, want 2", n)
	}
}

func TestAResetConnectionOnAReadRetriesOnAnotherConnection(t *testing.T) {
	s := newStallServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	// The first connection is reset (RST, not FIN) instead of hanging.
	s.hang = resetConnection
	var events []StallRetry
	c := s.client(t, Options{ResponseHeaderTimeout: testHeaderTimeout, OnStallRetry: func(e StallRetry) { events = append(events, e) }})
	var out map[string]any
	if err := c.Do(ctx(t), Request{Op: OpGetContext, Method: http.MethodGet, Path: PathContext}, &out); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if out["ok"] != true {
		t.Fatalf("body = %v", out)
	}
	if len(events) != 1 || events[0].Reason != StallConnectionReset {
		t.Fatalf("events = %+v, want one %q retry", events, StallConnectionReset)
	}
}

// resetConnection is a hang that resets the connection (RST, not FIN).
func resetConnection(w http.ResponseWriter, _ *http.Request) {
	conn, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		return
	}
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetLinger(0)
	}
	_ = conn.Close()
}

func TestARetryAfterAResetNamesTheReset(t *testing.T) {
	s := newStallServer(t, func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // the retry's connection never answers either
	})
	s.hang = resetConnection
	c := s.client(t, Options{
		HTTPClient:            &http.Client{Transport: s.srv.Client().Transport, Timeout: 2 * testHeaderTimeout},
		ResponseHeaderTimeout: testHeaderTimeout,
	})
	err := c.Do(ctx(t), Request{Op: OpGetContext, Method: http.MethodGet, Path: PathContext}, nil)
	var hte *ResponseHeaderTimeoutError
	if !errors.As(err, &hte) {
		t.Fatalf("err = %v, want *ResponseHeaderTimeoutError", err)
	}
	if !hte.Retried || hte.Reason != StallConnectionReset {
		t.Errorf("hte = %+v, want the retry recorded with reason %q", hte, StallConnectionReset)
	}
	if msg := err.Error(); !strings.Contains(msg, "the first had its connection reset") {
		t.Errorf("error %q should name the first attempt's reset", msg)
	}
}

// TestASlowReadFinishesOnItsRetry: the timer cannot tell a silent connection
// from a serve still working on a slow read. A read slower than the timer on
// every connection is retried once and, with no timer on the retry, still
// finishes, as it did before there was a timer.
func TestASlowReadFinishesOnItsRetry(t *testing.T) {
	var mu sync.Mutex
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n++
		mu.Unlock()
		select {
		case <-time.After(2 * testHeaderTimeout):
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL)
	var events []StallRetry
	c, err := New(base, nil, Options{
		HTTPClient:            &http.Client{Transport: srv.Client().Transport, Timeout: DefaultTimeout},
		ResponseHeaderTimeout: testHeaderTimeout,
		OnStallRetry:          func(e StallRetry) { events = append(events, e) },
	})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	var out map[string]any
	if err := c.Do(ctx(t), Request{Op: OpGetContext, Method: http.MethodGet, Path: PathContext}, &out); err != nil {
		t.Fatalf("Do: %v; a read slower than the header timeout must still finish", err)
	}
	if out["ok"] != true {
		t.Fatalf("body = %v", out)
	}
	if elapsed := time.Since(start); elapsed < 3*testHeaderTimeout {
		t.Errorf("elapsed %v, want one header timeout plus the slow answer (%v)", elapsed, 3*testHeaderTimeout)
	}
	if len(events) != 1 || events[0].Reason != StallHeaderTimeout {
		t.Errorf("events = %+v, want one %q retry", events, StallHeaderTimeout)
	}
	mu.Lock()
	defer mu.Unlock()
	if n != 2 {
		t.Errorf("server saw %d requests, want 2 (the abandoned attempt and the retry)", n)
	}
}

// TestAReadQueuedBehindTheConnectionLimitFinishesOnceASlotFrees is serve at its
// connection limit (a netutil.LimitListener, as serve's own): the kernel
// completes the handshake and takes the request, but serve accepts the
// connection only once a slot frees. The retry waits for that within the
// overall timeout instead of failing a read serve was about to answer.
func TestAReadQueuedBehindTheConnectionLimitFinishesOnceASlotFrees(t *testing.T) {
	occupied := make(chan struct{})
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/occupy" {
			close(occupied)
			time.Sleep(3 * testHeaderTimeout)
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	srv.Listener = netutil.LimitListener(srv.Listener, 1)
	// Every answer closes its connection, so the slot frees once it is sent.
	srv.Config.SetKeepAlivesEnabled(false)
	srv.Start()
	t.Cleanup(srv.Close)

	occupier := make(chan error, 1)
	go func() {
		resp, err := srv.Client().Get(srv.URL + "/occupy")
		if err == nil {
			_ = resp.Body.Close()
		}
		occupier <- err
	}()
	<-occupied

	base, _ := url.Parse(srv.URL)
	var events []StallRetry
	c, err := New(base, nil, Options{
		HTTPClient:            &http.Client{Transport: srv.Client().Transport, Timeout: DefaultTimeout},
		ResponseHeaderTimeout: testHeaderTimeout,
		OnStallRetry:          func(e StallRetry) { events = append(events, e) },
	})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := c.Do(ctx(t), Request{Op: OpGetContext, Method: http.MethodGet, Path: PathContext}, &out); err != nil {
		t.Fatalf("Do: %v; a read queued behind the connection limit must finish once a slot frees", err)
	}
	if out["ok"] != true {
		t.Fatalf("body = %v", out)
	}
	if len(events) != 1 || events[0].Reason != StallHeaderTimeout {
		t.Errorf("events = %+v, want one %q retry", events, StallHeaderTimeout)
	}
	if err := <-occupier; err != nil {
		t.Fatalf("the request holding the slot failed: %v", err)
	}
}

// slowCredentials is a credential source slower than the header timer, such as
// a cold BEADS_HTTP_TOKEN_COMMAND.
type slowCredentials struct{ d time.Duration }

func (s slowCredentials) Authorize(ctx context.Context, _ *http.Request) error {
	select {
	case <-time.After(s.d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (slowCredentials) Refresh(context.Context) (bool, error) { return false, nil }

// TestASlowCredentialSourceIsNotAStall: the timer starts once the request has
// been written, so the time a credential source takes is not counted as a
// server that went silent.
func TestASlowCredentialSourceIsNotAStall(t *testing.T) {
	fired := 0
	c, rec := newTestClient(t, Options{ResponseHeaderTimeout: testHeaderTimeout, OnStallRetry: func(StallRetry) { fired++ }},
		slowCredentials{d: 2 * testHeaderTimeout}, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{}`))
		})
	if err := c.Do(ctx(t), Request{Op: OpGetContext, Method: http.MethodGet, Path: PathContext}, nil); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if fired != 0 {
		t.Errorf("OnStallRetry fired %d times for a slow credential source", fired)
	}
	if n := rec.count(); n != 1 {
		t.Errorf("server saw %d requests, want 1", n)
	}
}

// TestASlowCredentialSourceDoesNotShortenTheRetry: both attempts share the
// client's Timeout counted, as that Timeout is, from when the first attempt is
// sent, so a credential source that takes the whole Timeout to resolve (a cold
// token command) leaves the retry of a slow but healthy read all of what the
// first attempt did not use. The retry replays the stamped request rather than
// consulting the source again, so a source this slow on every call does not
// cost the retry its budget either.
func TestASlowCredentialSourceDoesNotShortenTheRetry(t *testing.T) {
	var mu sync.Mutex
	var arrived []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		arrived = append(arrived, time.Now())
		mu.Unlock()
		select {
		case <-time.After(2 * testHeaderTimeout):
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL)
	budget := 5 * testHeaderTimeout
	var events []StallRetry
	c, err := New(base, slowCredentials{d: budget}, Options{
		HTTPClient:            &http.Client{Transport: srv.Client().Transport, Timeout: budget},
		ResponseHeaderTimeout: testHeaderTimeout,
		OnStallRetry:          func(e StallRetry) { events = append(events, e) },
	})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := c.Do(ctx(t), Request{Op: OpGetContext, Method: http.MethodGet, Path: PathContext}, &out); err != nil {
		t.Fatalf("Do: %v; the credential source's time is not the server's, and must not cut the retry short", err)
	}
	done := time.Now()
	if out["ok"] != true {
		t.Fatalf("body = %v", out)
	}
	if len(events) != 1 || events[0].Reason != StallHeaderTimeout {
		t.Errorf("events = %+v, want one %q retry", events, StallHeaderTimeout)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(arrived) != 2 {
		t.Fatalf("server saw %d requests, want 2 (the abandoned attempt and the retry)", len(arrived))
	}
	if took := done.Sub(arrived[0]); took > budget {
		t.Errorf("the read finished %v after its first attempt arrived, want within one Timeout (%v)", took, budget)
	}
}

// TestAWriteWhoseDialTimesOutIsAPlainConnectError: a timeout before the request
// was written (here the dial) cannot have reached the server, so it is not
// reported as a header timeout that may or may not have been applied.
func TestAWriteWhoseDialTimesOutIsAPlainConnectError(t *testing.T) {
	base, _ := url.Parse("http://127.0.0.1:1")
	transport := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: os.ErrDeadlineExceeded}
	}}
	c, err := New(base, nil, Options{
		HTTPClient:            &http.Client{Transport: transport, Timeout: DefaultTimeout},
		ResponseHeaderTimeout: testHeaderTimeout,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = c.Do(ctx(t), Request{Op: OpCreateIssue, Method: http.MethodPost, Path: PathIssues,
		Body: map[string]any{"title": "t"}}, nil)
	var ce *ConnectError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %T %v, want *ConnectError", err, err)
	}
	var hte *ResponseHeaderTimeoutError
	if errors.As(err, &hte) {
		t.Fatalf("a write whose dial timed out was reported as a header timeout: %v", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "i/o timeout") || strings.Contains(msg, "may or may not") {
		t.Errorf("error %q should be the dial timeout, not a maybe-applied write", msg)
	}
}

// TestAStallLeavesOtherServersIdleConnectionsAlone: a transport may be shared by
// every client in the process (DialWith's baseline transport), so a stall
// retry must not flush its idle pool: another server's client keeps its
// kept-alive connection.
func TestAStallLeavesOtherServersIdleConnectionsAlone(t *testing.T) {
	stalled := newStallServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	})
	var mu sync.Mutex
	var remotes []string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		remotes = append(remotes, r.RemoteAddr)
		mu.Unlock()
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(other.Close)

	shared := &http.Transport{}
	t.Cleanup(shared.CloseIdleConnections)
	hc := &http.Client{Transport: shared, Timeout: DefaultTimeout}
	stalledClient := stalled.client(t, Options{HTTPClient: hc, ResponseHeaderTimeout: testHeaderTimeout})
	otherBase, _ := url.Parse(other.URL)
	otherClient, err := New(otherBase, nil, Options{HTTPClient: hc, ResponseHeaderTimeout: testHeaderTimeout})
	if err != nil {
		t.Fatal(err)
	}

	read := Request{Op: OpGetContext, Method: http.MethodGet, Path: PathContext}
	if err := otherClient.Do(ctx(t), read, nil); err != nil {
		t.Fatalf("other server: %v", err)
	}
	if err := stalledClient.Do(ctx(t), read, nil); err != nil {
		t.Fatalf("stalled server: %v", err)
	}
	if n := len(stalled.seen()); n != 2 {
		t.Fatalf("stalled server saw %d requests, want the stall and its retry", n)
	}
	if err := otherClient.Do(ctx(t), read, nil); err != nil {
		t.Fatalf("other server again: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(remotes) != 2 || remotes[0] != remotes[1] {
		t.Errorf("other server saw connections %v; its kept-alive connection should have survived the stall retry", remotes)
	}
}

func TestASlowStreamingBodyIsNotCutOffByTheHeaderTimeout(t *testing.T) {
	c, rec := newTestClient(t, Options{ResponseHeaderTimeout: testHeaderTimeout}, nil, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"ok":true`)
		w.(http.Flusher).Flush()
		// Keep streaming for well past the header timeout.
		for range 8 {
			time.Sleep(testHeaderTimeout / 2)
			_, _ = io.WriteString(w, " ")
			w.(http.Flusher).Flush()
		}
		_, _ = io.WriteString(w, `}`)
	})
	start := time.Now()
	var out map[string]any
	if err := c.Do(ctx(t), Request{Op: OpGetContext, Method: http.MethodGet, Path: PathContext}, &out); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if out["ok"] != true {
		t.Fatalf("body = %v", out)
	}
	if elapsed := time.Since(start); elapsed < 3*testHeaderTimeout {
		t.Errorf("elapsed %v: the body should have streamed past the header timeout", elapsed)
	}
	if n := rec.count(); n != 1 {
		t.Errorf("server saw %d requests, want 1 (no retry for a slow body)", n)
	}
}

func TestTheDefaultHeaderTimeoutUndercutsTheOverallTimeout(t *testing.T) {
	if DefaultResponseHeaderTimeout <= 0 || DefaultResponseHeaderTimeout >= DefaultTimeout {
		t.Fatalf("DefaultResponseHeaderTimeout = %v, want it positive and below DefaultTimeout (%v)",
			DefaultResponseHeaderTimeout, DefaultTimeout)
	}
}

func TestANegativeHeaderTimeoutDisablesIt(t *testing.T) {
	c, _ := newTestClient(t, Options{ResponseHeaderTimeout: -1}, nil, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * testHeaderTimeout)
		_, _ = w.Write([]byte(`{}`))
	})
	if c.headerTimeout != 0 {
		t.Fatalf("headerTimeout = %v, want disabled", c.headerTimeout)
	}
	if err := c.Do(ctx(t), Request{Op: OpGetContext, Method: http.MethodGet, Path: PathContext}, nil); err != nil {
		t.Fatalf("Do: %v", err)
	}
}

// TestAReadThatSpentTheWholeTimeoutIsNotRetried: the retry runs on what the
// first attempt left of the overall timeout, so a read whose first attempt used
// all of it (here with the header timer off, as
// BEADS_HTTP_RESPONSE_HEADER_TIMEOUT=off leaves it) fails with its typed
// timeout, and reports no retry it has no time left to make.
func TestAReadThatSpentTheWholeTimeoutIsNotRetried(t *testing.T) {
	var mu sync.Mutex
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n++
		mu.Unlock()
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL)
	budget := testHeaderTimeout
	fired := 0
	c, err := New(base, nil, Options{
		HTTPClient:            &http.Client{Transport: srv.Client().Transport, Timeout: budget},
		ResponseHeaderTimeout: -1,
		OnStallRetry:          func(StallRetry) { fired++ },
	})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err = c.Do(ctx(t), Request{Op: OpGetContext, Method: http.MethodGet, Path: PathContext}, nil)
	elapsed := time.Since(start)
	var hte *ResponseHeaderTimeoutError
	if !errors.As(err, &hte) {
		t.Fatalf("err = %T %v, want *ResponseHeaderTimeoutError", err, err)
	}
	if hte.Retried || hte.Attempts != 1 || hte.Err == nil {
		t.Errorf("hte = %+v, want one unretried attempt and the transport's timeout", hte)
	}
	if fired != 0 {
		t.Errorf("OnStallRetry fired %d times with none of the overall timeout left", fired)
	}
	if elapsed < budget || elapsed > budget+time.Second {
		t.Errorf("elapsed %v, want the overall timeout (%v) and no more", elapsed, budget)
	}
	mu.Lock()
	defer mu.Unlock()
	if n != 1 {
		t.Errorf("server saw %d requests, want 1", n)
	}
}

// TestARaisedRequestTimeoutAlsoBoundsTheStallRetry: the retry runs on what the
// first attempt left of the REQUEST's overall timeout, so a read that raises
// its ceiling (Request.Timeout) keeps the raised ceiling on its retry rather
// than falling back to the client's own. The server answers every request
// after slowRead, and the retry is sent once the header timer fires, so under
// the client's own ceiling (slowRead, counted from the first send) the retry
// runs out before its answer; under the raised one it gets it.
func TestARaisedRequestTimeoutAlsoBoundsTheStallRetry(t *testing.T) {
	const slowRead = 2 * testHeaderTimeout
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(slowRead):
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	base, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	var retries int
	c, err := New(base, nil, Options{
		HTTPClient:            &http.Client{Transport: srv.Client().Transport, Timeout: slowRead},
		ResponseHeaderTimeout: testHeaderTimeout,
		OnStallRetry:          func(StallRetry) { retries++ },
	})
	if err != nil {
		t.Fatal(err)
	}
	read := Request{Op: OpGetContext, Method: http.MethodGet, Path: PathContext}

	// The control: the client's own ceiling ends before the retry's answer.
	var hte *ResponseHeaderTimeoutError
	if err := c.Do(ctx(t), read, nil); !errors.As(err, &hte) || !hte.Retried {
		t.Fatalf("Do under the client's own %v ceiling: err = %v, want a retried *ResponseHeaderTimeoutError", slowRead, err)
	}

	read.Timeout = DefaultTimeout
	var out map[string]any
	if err := c.Do(ctx(t), read, &out); err != nil {
		t.Fatalf("Do with Request.Timeout %v: %v; the retry must run on the raised ceiling, not the client's %v",
			read.Timeout, err, slowRead)
	}
	if out["ok"] != true {
		t.Fatalf("body = %v, want the retry's answer", out)
	}
	if retries != 2 {
		t.Errorf("OnStallRetry fired %d times, want 2 (one per Do)", retries)
	}
}

// countingToken is a staticToken that counts its Authorize calls.
type countingToken struct {
	staticToken
	authorized int
}

func (c *countingToken) Authorize(ctx context.Context, req *http.Request) error {
	c.authorized++
	return c.staticToken.Authorize(ctx, req)
}

// TestAStallRetryReplaysTheCredentialAndA401StillRefreshes: a read whose first
// attempt stalls is resent with the headers Authorize set for it, not
// authorized again, so a credential that went stale meanwhile draws a 401 on
// the retry. That 401 reaches Refresh like any other, and the request Refresh
// re-issues is authorized afresh.
func TestAStallRetryReplaysTheCredentialAndA401StillRefreshes(t *testing.T) {
	var mu sync.Mutex
	var answered []string
	s := newStallServer(t, func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		mu.Lock()
		answered = append(answered, auth)
		mu.Unlock()
		if auth != "Bearer rotated" {
			unauthenticated(w)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	creds := &countingToken{staticToken: staticToken{tokens: []string{"stale", "rotated"}, retry: true}}
	base, _ := url.Parse(s.srv.URL)
	var events []StallRetry
	c, err := New(base, creds, Options{
		HTTPClient:            &http.Client{Transport: s.srv.Client().Transport, Timeout: DefaultTimeout},
		ResponseHeaderTimeout: testHeaderTimeout,
		OnStallRetry:          func(e StallRetry) { events = append(events, e) },
	})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := c.Do(ctx(t), Request{Op: OpGetContext, Method: http.MethodGet, Path: PathContext}, &out); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if out["ok"] != true {
		t.Fatalf("body = %v, want the answer to the refreshed credential", out)
	}
	if len(events) != 1 || events[0].Reason != StallHeaderTimeout {
		t.Errorf("events = %+v, want one %q retry", events, StallHeaderTimeout)
	}
	if creds.authorized != 2 {
		t.Errorf("Authorize called %d times, want 2 (once per request built, not once per attempt)", creds.authorized)
	}
	if creds.refreshN != 1 {
		t.Errorf("Refresh called %d times, want 1", creds.refreshN)
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"Bearer stale", "Bearer rotated"}; !slices.Equal(answered, want) {
		t.Errorf("answered requests carried %q, want %q (the stall retry, then the refreshed request)", answered, want)
	}
	if n := len(s.seen()); n != 3 {
		t.Errorf("server saw %d requests, want 3 (the stall, its retry, and the refreshed request)", n)
	}
}
