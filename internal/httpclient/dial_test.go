// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/dial_test.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"net/http"
	"net/http/httptrace"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/wire"
)

// TestBaselineTransportRaisesMaxIdleConnsPerHost is finding 9: the ceiling
// TransportFor already applies to a CA-scoped target must ALSO apply to an
// ordinary target with no CA configured, since the RTT cost of an
// under-pooled host is the same either way.
func TestBaselineTransportRaisesMaxIdleConnsPerHost(t *testing.T) {
	rt := baselineTransport()
	transport, ok := rt.(*http.Transport)
	if !ok {
		t.Fatalf("baselineTransport returned %T, want *http.Transport", rt)
	}
	if transport.MaxIdleConnsPerHost != maxIdleConnsPerHost {
		t.Errorf("MaxIdleConnsPerHost = %d, want %d", transport.MaxIdleConnsPerHost, maxIdleConnsPerHost)
	}
}

// TestBaselineTransportIsSharedAcrossCalls confirms baselineTransport is built
// once and reused (sync.Once), not cloned per dial: a long-lived process must
// not leak a fresh transport, and its connection pool, on every dial of a
// target that never configured a CA.
func TestBaselineTransportIsSharedAcrossCalls(t *testing.T) {
	first := baselineTransport()
	second := baselineTransport()
	if first != second {
		t.Error("baselineTransport returned a different instance on a second call; it should be built once and reused")
	}
}

// TestDialTransportUsesBaselineWhenNoCAConfigured is finding 9's unit test on
// the exact seam DialWith uses when the caller leaves HTTPClient nil: an
// unconfigured resolvedCA must produce the shared baselineTransport, not a
// freshly cloned one, and it must carry the raised ceiling.
func TestDialTransportUsesBaselineWhenNoCAConfigured(t *testing.T) {
	rt, err := dialTransport(resolvedCA{})
	if err != nil {
		t.Fatalf("dialTransport: %v", err)
	}
	if rt != baselineTransport() {
		t.Error("dialTransport did not return the shared baselineTransport for an unconfigured CA")
	}
}

// TestDialWithNoCARaisesMaxIdleConnsPerHost is finding 9's end-to-end case: a
// target with no CA configured, dialed through DialWith with HTTPClient left
// nil, still reaches a real server through the raised-ceiling baseline
// transport.
func TestDialWithNoCARaisesMaxIdleConnsPerHost(t *testing.T) {
	clearCAEnvironment(t)
	server := &contextServer{body: v0Context("proj-baseline")}
	srv := server.start(t)
	target := Target{BaseURL: mustParseURL(t, srv.URL)}

	snap, err := Handshake(context.Background(), target, DialOptions{})
	if err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	if snap.ProjectId != "proj-baseline" {
		t.Errorf("project_id = %q, want proj-baseline", snap.ProjectId)
	}
}

// TestDialWithSuppliedClientNilTransportNoCARaisesMaxIdleConnsPerHost covers
// the OTHER shape DialWith's own doc comment promises the raised ceiling for
// ("Every dial also gets the raised MaxIdleConnsPerHost ceiling... whether or
// not a CA is configured"): a caller that supplies its own *http.Client with
// a nil Transport, dialed against a target with no CA configured. The
// opts.HTTPClient == nil branch above (TestDialWithNoCARaisesMaxIdleConnsPerHost)
// does not exercise this branch at all — a caller-supplied client with a nil
// Transport used to fall through to http.DefaultTransport's own unraised
// MaxIdleConnsPerHost of 2 instead, silently exempting this one shape from
// the doc's "every dial" promise, and forcing a fresh connection (a fresh TLS
// handshake, over a real network) on most of a concurrent burst even once the
// pool is warm.
//
// This asserts it end to end, through wire.Client.Do rather than by reaching
// into the transport this package's *Conn hands wire.New (wire.Client keeps
// it unexported, and rightly so): a burst of concurrent requests through the
// resulting Conn, traced with httptrace the way
// TestTransportForRaisesMaxIdleConnsPerHostForBursts traces a raw
// *http.Transport, must settle into reusing every connection once the pool
// has gone idle — which the default ceiling of 2 cannot do at this burst
// size.
func TestDialWithSuppliedClientNilTransportNoCARaisesMaxIdleConnsPerHost(t *testing.T) {
	clearCAEnvironment(t)
	server := &contextServer{body: v0Context("proj-supplied-client")}
	srv := server.start(t)
	target := Target{BaseURL: mustParseURL(t, srv.URL)}

	supplied := &http.Client{}
	conn, err := DialWith(target, nil, DialOptions{HTTPClient: supplied})
	if err != nil {
		t.Fatalf("DialWith: %v", err)
	}
	if supplied.Transport != nil {
		t.Error("DialWith mutated the caller's own *http.Client in place; it must inject onto a copy")
	}

	const burst = 8
	req := wire.Request{Op: "test.burst", Method: http.MethodGet, Path: "/v0/test-burst"}

	runBurst := func() int64 {
		var wg sync.WaitGroup
		var newConns int64
		for i := 0; i < burst; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				reused := make(chan bool, 1)
				trace := &httptrace.ClientTrace{
					GotConn: func(info httptrace.GotConnInfo) { reused <- info.Reused },
				}
				ctx := httptrace.WithClientTrace(context.Background(), trace)
				if err := conn.Do(ctx, req, &apigen.ContextResponse{}); err != nil {
					t.Errorf("Do: %v", err)
					return
				}
				if !<-reused {
					atomic.AddInt64(&newConns, 1)
				}
			}()
		}
		wg.Wait()
		return newConns
	}

	if warm := runBurst(); warm == 0 {
		t.Fatal("first burst reused connections that could not have existed yet")
	}

	var steady int64
	for attempt := 0; attempt < 5; attempt++ {
		time.Sleep(50 * time.Millisecond)
		steady = runBurst()
		if steady == 0 {
			break
		}
	}
	if steady != 0 {
		t.Errorf("repeat burst dialed %d/%d fresh connections after warm-up; the supplied-client-nil-Transport branch is not getting the raised MaxIdleConnsPerHost ceiling", steady, burst)
	}
}

// --- finding 2: DialOptionsForFile ---

// TestDialOptionsForFileBypassesEnvEntirely is finding 2's core unit: a
// DialOptions built from an explicit file must verify against EXACTLY that
// file, never BEADS_HTTP_CA_FILE, even when the env is set to a DIFFERENT,
// otherwise-valid CA for the same host. This is what lets `bd connect
// --ca-file X` check X for itself before writing it, regardless of what the
// env currently resolves to.
func TestDialOptionsForFileBypassesEnvEntirely(t *testing.T) {
	clearCAEnvironment(t)
	right := newTestCA(t)
	wrong := newTestCA(t)
	h := &caContextHandler{body: v0Context("proj-connect-check")}
	u := wrong.startServer(t, h.handler())

	// The env is set to a CA that would NOT verify this server; if
	// DialOptionsForFile consulted it, the handshake below would fail for the
	// wrong reason, or a matching-host env could mask the flag's own file
	// entirely.
	t.Setenv(CAFileEnv, u.Host+"="+right.writePEM(t))

	opts, err := DialOptionsForFile(wrong.writePEM(t), DialOptions{})
	if err != nil {
		t.Fatalf("DialOptionsForFile: %v", err)
	}
	// u names the server as "localhost" (so SNI is sent) even though
	// startServer actually bound 127.0.0.1; without pinning the dial to
	// tcp4, the default dual-stack dialer can race an unrelated ::1 listener
	// and flake. See forceIPv4Loopback's doc for the full mechanism.
	forceIPv4Loopback(t, opts.HTTPClient.Transport)
	// Target.CAFile is deliberately left empty, mirroring cmd/bd's connect
	// verification call.
	target := Target{BaseURL: u}
	snap, err := Handshake(context.Background(), target, opts)
	if err != nil {
		t.Fatalf("Handshake verifying exactly the flag's CA file: %v", err)
	}
	if snap.ProjectId != "proj-connect-check" {
		t.Errorf("project_id = %q, want proj-connect-check", snap.ProjectId)
	}
}

// TestDialOptionsForFileRefusesTheWrongCAEvenWithAGoodEnv is finding 2's exact
// regression scenario: "With the env var set to the good CA and --ca-file set
// to a wrong CA, connect succeeded" (the bug). DialOptionsForFile must make
// the handshake fail here, since --ca-file's own file is the wrong one.
func TestDialOptionsForFileRefusesTheWrongCAEvenWithAGoodEnv(t *testing.T) {
	clearCAEnvironment(t)
	right := newTestCA(t)
	wrong := newTestCA(t)
	h := &caContextHandler{body: v0Context("proj-should-not-verify")}
	u := right.startServer(t, h.handler())

	t.Setenv(CAFileEnv, u.Host+"="+right.writePEM(t))

	opts, err := DialOptionsForFile(wrong.writePEM(t), DialOptions{})
	if err != nil {
		t.Fatalf("DialOptionsForFile: %v", err)
	}
	target := Target{BaseURL: u}
	_, err = Handshake(context.Background(), target, opts)
	if err == nil {
		t.Fatal("Handshake succeeded with --ca-file's own wrong CA, despite a good env CA for the same host; DialOptionsForFile must check exactly the flag's file")
	}
	if h.sawRequest() {
		t.Error("server saw a request; TLS verification against the wrong CA should have failed first")
	}
}
