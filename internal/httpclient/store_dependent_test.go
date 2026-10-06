// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/ca_test.go, credential_test.go, target_test.go (store-dependent tests)@49d1df2f6)
// to OSS beads under the MIT license.

package httpclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/httpclient/wire"
)

// warmIPv4OnlyCATransport pre-builds and caches target's CA-scoped transport
// — the exact (abs path, content hash) cache entry transportForFile would
// build and cache for it on Handshake's own first dial, per
// matchesCachedTransport's doc — and forces that cached *http.Transport to
// dial over tcp4 only.
//
// This closes the same Happy-Eyeballs ::1-vs-127.0.0.1 race
// forceIPv4Loopback documents and fixes for TransportFor's direct callers
// (ca_wrong_ca_test.go, ca_test.go): every server these tests dial is
// addressed by the hostname "localhost" (ca.startServer rewrites it there
// deliberately, so SNI is actually sent), which Go's default dialer races a
// tcp6 dial to ::1:<port> against the real tcp4 dial to 127.0.0.1:<port>
// for. An unrelated local process already bound to a colliding ephemeral
// port on ::1 can occasionally "win" that race before the real dial
// completes, and the client then speaks TLS to that unrelated process
// instead of the test server: handshake fails with a misleading "tls: first
// record does not look like a TLS handshake" instead of exercising anything
// the test is actually about (observed flake:
// TestTransportForSucceedsWithEnvCAFile).
//
// Unlike forceIPv4Loopback, this test never gets its hands on the
// *http.Transport Handshake's own DialWith builds internally (DialOptions{}
// leaves HTTPClient nil deliberately, so DialWith's own nil-HTTPClient
// branch is what's under test) — so instead of mutating that transport
// after the fact, this warms the SAME path-and-content-keyed cache entry
// DialWith's transportForFile call will hit, by calling the public
// TransportFor with the identical target first. DialWith then finds (and
// reuses, never rebuilds) the exact pointer already forced to tcp4-only,
// without changing which of DialWith's branches actually ran.
//
// It is a deliberate no-op when target has no CA configured (TransportFor
// returns nil, nil): that case resolves through the process-wide
// baselineTransport singleton instead, which this package exposes no cache
// to pre-warm from outside. The call sites that matter here all configure a
// CA, so this never silently skips the case it exists for.
func warmIPv4OnlyCATransport(t *testing.T, target Target) {
	t.Helper()
	rt, err := TransportFor(target)
	if err != nil {
		t.Fatalf("warmIPv4OnlyCATransport: TransportFor: %v", err)
	}
	if rt == nil {
		return
	}
	forceIPv4Loopback(t, rt)
}

// TestTransportForSucceedsWithSidecarCAFile is the "succeeds with ca_file
// (sidecar)" case: Target.CAFile, as `bd connect --ca-file` writes it.
func TestTransportForSucceedsWithSidecarCAFile(t *testing.T) {
	clearCAEnvironment(t)
	ca := newTestCA(t)
	h := &caContextHandler{body: v0Context("proj-ca")}
	u := ca.startServer(t, h.handler())

	target := Target{BaseURL: u, CAFile: ca.writePEM(t)}
	warmIPv4OnlyCATransport(t, target)
	snap, err := Handshake(context.Background(), target, DialOptions{})
	if err != nil {
		t.Fatalf("Handshake with sidecar ca_file: %v", err)
	}
	if snap.ProjectId != "proj-ca" {
		t.Errorf("project_id = %q, want proj-ca", snap.ProjectId)
	}
	if !h.sawRequest() {
		t.Fatal("server never saw a request")
	}
	if h.seenSNI() != "localhost" {
		t.Errorf("SNI = %q, want %q; TransportFor must not override ServerName", h.seenSNI(), "localhost")
	}
	if wantHost := u.Host; h.seenHost() != wantHost {
		t.Errorf("Host header = %q, want %q", h.seenHost(), wantHost)
	}
}

// TestTransportForSucceedsWithEnvCAFile is the "succeeds with ca_file (env)"
// case: BEADS_HTTP_CA_FILE, host-scoped to exactly this target's host:port,
// with no sidecar CAFile at all.
func TestTransportForSucceedsWithEnvCAFile(t *testing.T) {
	clearCAEnvironment(t)
	ca := newTestCA(t)
	h := &caContextHandler{body: v0Context("proj-env-ca")}
	u := ca.startServer(t, h.handler())
	t.Setenv(CAFileEnv, u.Host+"="+ca.writePEM(t))

	target := Target{BaseURL: u}
	warmIPv4OnlyCATransport(t, target)
	snap, err := Handshake(context.Background(), target, DialOptions{})
	if err != nil {
		t.Fatalf("Handshake with %s: %v", CAFileEnv, err)
	}
	if snap.ProjectId != "proj-env-ca" {
		t.Errorf("project_id = %q, want proj-env-ca", snap.ProjectId)
	}
}

// TestCAFileEnvAgreeingWithSidecarSucceeds pins the documented precedence when
// the two rungs agree: the env var's host-scoped pattern matches the target
// and names the SAME file the sidecar does, so there is nothing to disagree
// about and the dial succeeds.
func TestCAFileEnvAgreeingWithSidecarSucceeds(t *testing.T) {
	clearCAEnvironment(t)
	ca := newTestCA(t)
	h := &caContextHandler{body: v0Context("proj-agree")}
	u := ca.startServer(t, h.handler())

	path := ca.writePEM(t)
	t.Setenv(CAFileEnv, u.Host+"="+path)
	target := Target{BaseURL: u, CAFile: path}
	warmIPv4OnlyCATransport(t, target)

	if _, err := Handshake(context.Background(), target, DialOptions{}); err != nil {
		t.Fatalf("env and sidecar naming the same CA for the same target should succeed: %v", err)
	}
}

// TestCAFileEnvDisagreeingWithSidecarRefuses is finding 1(c): the env var's
// host-scoped pattern matches the target, but names a DIFFERENT file than the
// target's own sidecar ca_file. That is a hard refusal, not a silent
// override in either direction — the two disagreeing is very likely a stale
// sidecar or a misdirected env var, and picking one silently would trust
// whichever file the operator did not intend.
func TestCAFileEnvDisagreeingWithSidecarRefuses(t *testing.T) {
	clearCAEnvironment(t)
	right := newTestCA(t)
	wrong := newTestCA(t)
	h := &caContextHandler{body: v0Context("proj-disagree")}
	u := right.startServer(t, h.handler())

	t.Setenv(CAFileEnv, u.Host+"="+right.writePEM(t))
	target := Target{BaseURL: u, CAFile: wrong.writePEM(t)}

	_, err := Handshake(context.Background(), target, DialOptions{})
	if err == nil {
		t.Fatal("Handshake succeeded despite the env CA and the sidecar ca_file disagreeing for the same target")
	}
	if !strings.Contains(err.Error(), CAFileEnv) {
		t.Errorf("error %q does not name %s", err, CAFileEnv)
	}
	if h.sawRequest() {
		t.Error("server saw a request; a disagreement must be refused before dialing")
	}
}

// TestCAFileEnvNonMatchingHostKeepsSystemRoots is the mandated test for
// finding 1: a target whose host the env var's pattern does NOT match must
// keep using system roots (or its own sidecar), never the env's private CA —
// otherwise BEADS_HTTP_CA_FILE set for one server would let its
// no-name-constraints CA impersonate every other host this process dials,
// e.g. api.github.com.
func TestCAFileEnvNonMatchingHostKeepsSystemRoots(t *testing.T) {
	clearCAEnvironment(t)
	ca := newTestCA(t)
	h := &caContextHandler{body: v0Context("proj-other-host")}
	u := ca.startServer(t, h.handler())

	// The env var is well-formed and points at a valid CA, but scoped to a
	// host that is NOT the target being dialed.
	t.Setenv(CAFileEnv, "unrelated.example.com:9999="+ca.writePEM(t))

	target := Target{BaseURL: u}
	_, err := Handshake(context.Background(), target, DialOptions{})
	if err == nil {
		t.Fatal("Handshake succeeded against a private-CA server using only system roots; the non-matching env var must not have applied")
	}
	if h.sawRequest() {
		t.Error("server saw a request; TLS verification against system roots should have failed before any HTTP request")
	}
}

// TestTransportForWrongCAFails is the "fails with the wrong CA" case.
func TestTransportForWrongCAFails(t *testing.T) {
	clearCAEnvironment(t)
	serverCA := newTestCA(t)
	otherCA := newTestCA(t)
	h := &caContextHandler{body: v0Context("proj-wrong-ca")}
	u := serverCA.startServer(t, h.handler())

	target := Target{BaseURL: u, CAFile: otherCA.writePEM(t)}
	_, err := Handshake(context.Background(), target, DialOptions{})
	if err == nil {
		t.Fatal("Handshake succeeded against a server signed by a CA other than the configured one")
	}
	if h.sawRequest() {
		t.Error("server saw a request; the TLS handshake should have failed before any HTTP request")
	}
}

// TestNoCAConfiguredOnlySystemRootsFails is the "fails with the right hostname
// but only system roots" case: a workspace that never configured ca_file or
// BEADS_HTTP_CA_FILE must not trust a private CA just because it dialed the
// right host.
func TestNoCAConfiguredOnlySystemRootsFails(t *testing.T) {
	clearCAEnvironment(t)
	ca := newTestCA(t)
	h := &caContextHandler{body: v0Context("proj-no-ca")}
	u := ca.startServer(t, h.handler())

	target := Target{BaseURL: u}
	_, err := Handshake(context.Background(), target, DialOptions{})
	if err == nil {
		t.Fatal("Handshake succeeded with no CA configured against a server the system roots do not trust")
	}
}

// TestDialWithNilTransportHTTPClientGetsCAInjectedOnACopy is finding 5's
// first half: a caller that supplies an *http.Client with a nil Transport
// (Timeout, CheckRedirect, Jar set for its own reasons, but no opinion on the
// transport) gets the resolved CA transport injected automatically — DialWith
// must not silently drop ca_file just because HTTPClient was non-nil — and it
// must happen on a COPY, leaving the caller's own *http.Client untouched.
func TestDialWithNilTransportHTTPClientGetsCAInjectedOnACopy(t *testing.T) {
	clearCAEnvironment(t)
	ca := newTestCA(t)
	h := &caContextHandler{body: v0Context("proj-nil-transport-client")}
	u := ca.startServer(t, h.handler())

	callerClient := &http.Client{Timeout: 7 * time.Second}
	target := Target{BaseURL: u, CAFile: ca.writePEM(t)}
	warmIPv4OnlyCATransport(t, target)

	snap, err := Handshake(context.Background(), target, DialOptions{HTTPClient: callerClient})
	if err != nil {
		t.Fatalf("Handshake with a nil-Transport HTTPClient and a configured CA: %v", err)
	}
	if snap.ProjectId != "proj-nil-transport-client" {
		t.Errorf("project_id = %q, want proj-nil-transport-client", snap.ProjectId)
	}
	if callerClient.Transport != nil {
		t.Error("DialWith mutated the caller's own *http.Client; it must inject on a copy")
	}
}

// TestDialWithOwnTransportHTTPClientAndConfiguredCARefuses is finding 5's
// second half: a caller that supplies an *http.Client with its OWN non-nil
// Transport has taken the transport over completely. If the target also has a
// CA configured, DialWith must refuse rather than silently dialing through a
// transport that may not trust it.
func TestDialWithOwnTransportHTTPClientAndConfiguredCARefuses(t *testing.T) {
	clearCAEnvironment(t)
	ca := newTestCA(t)
	h := &caContextHandler{body: v0Context("proj-own-transport")}
	u := ca.startServer(t, h.handler())

	pool := x509.NewCertPool()
	pool.AddCert(mustParseOneCert(t, ca.pem))
	// This transport actually trusts the CA — the refusal must fire on the
	// mere presence of a caller-supplied Transport plus a configured CA, not
	// on whether that transport happens to be wrong.
	explicit := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}

	target := Target{BaseURL: u, CAFile: ca.writePEM(t)}
	_, err := Handshake(context.Background(), target, DialOptions{HTTPClient: explicit})
	if err == nil {
		t.Fatal("Handshake succeeded with a caller-supplied Transport and a configured CA; DialWith should refuse rather than silently trust the caller's own transport")
	}
	if !strings.Contains(err.Error(), "ca_file") && !strings.Contains(err.Error(), CAFileEnv) {
		t.Errorf("error %q does not name the CA setting at fault", err)
	}
	if h.sawRequest() {
		t.Error("server saw a request; the refusal must fire before dialing")
	}
}

// TestDialWithOwnTransportHTTPClientAndNoCAIsUsedVerbatim confirms the other
// branch of finding 5: when the target has NO CA configured, a
// caller-supplied Transport is used exactly as given — DialWith has nothing
// to add and nothing to refuse.
func TestDialWithOwnTransportHTTPClientAndNoCAIsUsedVerbatim(t *testing.T) {
	clearCAEnvironment(t)
	h := &caContextHandler{body: v0Context("proj-own-transport-no-ca")}
	srv := httptest.NewServer(h.handler())
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}

	explicit := &http.Client{Transport: http.DefaultTransport}
	target := Target{BaseURL: u}
	snap, err := Handshake(context.Background(), target, DialOptions{HTTPClient: explicit})
	if err != nil {
		t.Fatalf("Handshake with a caller Transport and no CA configured: %v", err)
	}
	if snap.ProjectId != "proj-own-transport-no-ca" {
		t.Errorf("project_id = %q, want proj-own-transport-no-ca", snap.ProjectId)
	}
}

// countBurstConnections fires n concurrent GETs at target through client and
// reports how many of them did NOT reuse an existing connection
// (httptrace.GotConnInfo.Reused == false), i.e. how many paid for a fresh
// dial (and, for this transport, a fresh TLS handshake).
func countBurstConnections(t *testing.T, client *http.Client, target *url.URL, n int) int64 {
	t.Helper()
	var wg sync.WaitGroup
	var newConns int64
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reused := make(chan bool, 1)
			trace := &httptrace.ClientTrace{
				GotConn: func(info httptrace.GotConnInfo) { reused <- info.Reused },
			}
			ctx := httptrace.WithClientTrace(context.Background(), trace)
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
			if err != nil {
				t.Errorf("NewRequest: %v", err)
				return
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Errorf("Do: %v", err)
				return
			}
			// A connection only goes back into the idle pool once its body is
			// drained to EOF: Close() alone does not reuse it (net/http's
			// documented behavior), so an un-drained Close here would make
			// every request in the burst look like a fresh dial regardless of
			// MaxIdleConnsPerHost.
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if !<-reused {
				atomic.AddInt64(&newConns, 1)
			}
		}()
	}
	wg.Wait()
	return atomic.LoadInt64(&newConns)
}

// TestTransportForRaisesMaxIdleConnsPerHostForBursts is the perf-hardening
// half of TransportFor: gc's ready-veto fan-out dials one CA-scoped target at
// concurrency 8, and http.DefaultTransport's MaxIdleConnsPerHost of 2 would
// force a fresh TLS handshake (~120ms at a cross-region round-trip time this CA exists
// for) on 6 of every 8 requests even once the pool is warm. This asserts the
// ceiling TransportFor sets, and that a burst run twice — the second after the
// first has gone idle — reuses every connection the second time, rather than
// re-dialing past whatever the ceiling is.
func TestTransportForRaisesMaxIdleConnsPerHostForBursts(t *testing.T) {
	clearCAEnvironment(t)
	ca := newTestCA(t)
	h := &caContextHandler{body: v0Context("proj-pool")}
	u := ca.startServer(t, h.handler())

	rt, err := TransportFor(Target{BaseURL: u, CAFile: ca.writePEM(t)})
	if err != nil {
		t.Fatalf("TransportFor: %v", err)
	}
	transport, ok := rt.(*http.Transport)
	if !ok {
		t.Fatalf("TransportFor returned %T, want *http.Transport", rt)
	}
	if transport.MaxIdleConnsPerHost != maxIdleConnsPerHost {
		t.Errorf("MaxIdleConnsPerHost = %d, want %d", transport.MaxIdleConnsPerHost, maxIdleConnsPerHost)
	}
	t.Cleanup(transport.CloseIdleConnections)

	const burst = 8
	client := &http.Client{Transport: transport}

	if warm := countBurstConnections(t, client, u, burst); warm == 0 {
		t.Fatal("first burst reused connections that could not have existed yet")
	}

	// Let the just-finished burst's connections settle into the idle pool
	// before firing the repeat — reuse only happens once a connection is
	// idle, not merely finished.
	var steady int64
	for attempt := 0; attempt < 5; attempt++ {
		time.Sleep(50 * time.Millisecond)
		steady = countBurstConnections(t, client, u, burst)
		if steady == 0 {
			break
		}
	}
	if steady != 0 {
		t.Errorf("repeat burst dialed %d/%d fresh connections after warm-up; MaxIdleConnsPerHost=%d is not keeping enough connections idle", steady, burst, transport.MaxIdleConnsPerHost)
	}
}

// --- finding 6: transport caching and root rotation ---

// TestHandshakeGatesIdentityBeforeAnythingIsServed is the wrong-server
// diagnostic connect leans on: the sidecar's pin is compared with the server's
// own project id, and a mismatch names both plus where that server's data lives.
func TestHandshakeGatesIdentityBeforeAnythingIsServed(t *testing.T) {
	clearCredentialEnvironment(t)
	server := &contextServer{body: v0Context("proj-server")}
	srv := server.start(t)

	target := Target{BaseURL: mustParseURL(t, srv.URL), ExpectProjectID: "proj-workspace"}
	_, err := Handshake(context.Background(), target, DialOptions{})
	if err == nil {
		t.Fatal("Handshake accepted a server that owns a different project")
	}
	var mismatch *wire.ProjectMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("Handshake error = %T (%v), want *wire.ProjectMismatchError", err, err)
	}
	for _, want := range []string{"proj-server", "proj-workspace", "/srv/repo", "bd connect"} {
		if !strings.Contains(mismatch.Error(), want) {
			t.Errorf("mismatch text is missing %q:\n%s", want, mismatch.Error())
		}
	}
}

func TestHandshakeAcceptsTheMatchingServer(t *testing.T) {
	clearCredentialEnvironment(t)
	server := &contextServer{body: v0Context("proj-1")}
	srv := server.start(t)

	got, err := Handshake(context.Background(), Target{BaseURL: mustParseURL(t, srv.URL), ExpectProjectID: "proj-1"}, DialOptions{})
	if err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	if got.ProjectId != "proj-1" {
		t.Errorf("project_id = %q, want proj-1", got.ProjectId)
	}
}

// TestOpenThroughTheDefaultDialerReachesTheServer walks the whole activation
// path with nothing stubbed: the sidecar the connect path writes, the registered
// default dialer, and the lazy handshake behind the workspace-identity probe.
func TestOpenThroughTheDefaultDialerReachesTheServer(t *testing.T) {
	clearCredentialEnvironment(t)
	server := &contextServer{body: v0Context("proj-1")}
	srv := server.start(t)

	dir := t.TempDir()
	if err := SaveTarget(dir, Target{BaseURL: mustParseURL(t, srv.URL), ExpectProjectID: "proj-1"}); err != nil {
		t.Fatalf("SaveTarget: %v", err)
	}

	prev := dialer
	RegisterDefaultDialer(DialOptions{UserAgent: "bd/test " + WireUserAgentSuffix})
	t.Cleanup(func() { dialer = prev })

	opened, err := NewFromConfig(context.Background(), dir)
	if err != nil {
		t.Fatalf("NewFromConfig: %v", err)
	}
	t.Cleanup(func() { _ = opened.Close() })

	// Nothing has dispatched yet: design D6 makes the context fetch lazy, so a
	// command that only needs the baseline pays for one round trip, not two.
	if len(server.seen) != 0 {
		t.Errorf("Open dialed %d times; the handshake is lazy", len(server.seen))
	}

	got, err := opened.GetMetadata(context.Background(), "_project_id")
	if err != nil {
		t.Fatalf("GetMetadata(_project_id): %v", err)
	}
	if got != "proj-1" {
		t.Errorf("_project_id = %q, want the server's project", got)
	}
	if len(server.seen) != 1 {
		t.Errorf("handshake ran %d times, want once and cached", len(server.seen))
	}
}

// TestDialSendsTheBearerAndRetriesOnceAfterRotation composes the provider with
// the wire client's one-retry window: a client rolled mid-flight 401s once and
// succeeds on the retry, and only because Refresh re-read the source.
func TestDialSendsTheBearerAndRetriesOnceAfterRotation(t *testing.T) {
	clearCredentialEnvironment(t)
	path := filepath.Join(t.TempDir(), "credentials")
	t.Setenv("BEADS_CREDENTIALS_FILE", path)

	server := &contextServer{body: v0Context("proj-1"), require: "rolled"}
	srv := server.start(t)
	base := mustParseURL(t, srv.URL)

	// The section key carries the server's ephemeral port, which only exists
	// once httptest has bound.
	writeCredential := func(token string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(fmt.Sprintf("[%s]\npassword=%s\n", base.Host, token)), 0o600); err != nil {
			t.Fatalf("write credentials file: %v", err)
		}
	}
	writeCredential("stale")
	server.onUnauthorized = func() { writeCredential("rolled") }

	conn, err := Dial(Target{BaseURL: base}, DialOptions{})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if _, err := conn.ServerContext(context.Background()); err != nil {
		t.Fatalf("ServerContext across a rotation: %v", err)
	}

	if len(server.seen) != 2 {
		t.Fatalf("server saw %d requests, want the 401 and its single retry: %v", len(server.seen), server.seen)
	}
	if server.seen[0] != "Bearer stale" || server.seen[1] != "Bearer rolled" {
		t.Errorf("request headers = %v, want the stale token then the rolled one", server.seen)
	}
}

// TestDialDoesNotRetryACredentialThatDidNotRoll: one retry is the whole
// rotation window. A second attempt with the same refused token would turn a
// clear 401 into two.
func TestDialDoesNotRetryACredentialThatDidNotRoll(t *testing.T) {
	clearCredentialEnvironment(t)
	t.Setenv(TokenEnv, "wrong")

	server := &contextServer{body: v0Context("proj-1"), require: "right"}
	srv := server.start(t)

	conn, err := Dial(Target{BaseURL: mustParseURL(t, srv.URL)}, DialOptions{})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if _, err := conn.ServerContext(context.Background()); err == nil {
		t.Fatal("ServerContext succeeded against a server that refuses this token")
	}
	if len(server.seen) != 1 {
		t.Errorf("server saw %d requests, want exactly one: %v", len(server.seen), server.seen)
	}
}
