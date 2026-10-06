// Written fresh for OSS beads S6 (no bd-enterprise source copied). Tests
// guardInsecureCredential (insecure_credential_guard.go), the S6 review's
// MED-4 fix: Open/OpenReadOnly/OpenWith's shared choke point for refusing a
// credential bound for a plain-http, non-loopback target.
package httpclient

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

const nonLoopbackTestHost = "198.51.100.1" // RFC 5737 TEST-NET-2: reserved, globally unroutable.

func newTestRequest(t *testing.T, rawURL string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	return req
}

// TestGuardInsecureCredentialPassesThroughNilCredential pins the loopback-
// trust posture: an unauthenticated open never had anything to guard, and
// must not be turned into a refusal by this fix.
func TestGuardInsecureCredentialPassesThroughNilCredential(t *testing.T) {
	target := Target{BaseURL: mustParseURL(t, "http://"+nonLoopbackTestHost+"/")}
	if got := guardInsecureCredential(target, nil, false); got != nil {
		t.Fatalf("guardInsecureCredential(nil) = %v, want nil", got)
	}
}

// TestGuardInsecureCredentialPassesThroughForLoopback: the finding is about
// a NON-loopback target; a loopback one (what the tip OSS server ships with)
// must be unaffected regardless of opts.
func TestGuardInsecureCredentialPassesThroughForLoopback(t *testing.T) {
	target := Target{BaseURL: mustParseURL(t, "http://127.0.0.1:8080/")}
	inner := stubProvider{tag: "loopback"}
	got := guardInsecureCredential(target, inner, false)
	if got != CredentialProvider(inner) {
		t.Fatalf("guardInsecureCredential returned %T, want the inner provider unwrapped for a loopback target", got)
	}
}

// TestGuardInsecureCredentialPassesThroughForHTTPS: the finding is about
// PLAIN http; a non-loopback https target is exactly what a credential should
// cross the network over, so it must be unaffected.
func TestGuardInsecureCredentialPassesThroughForHTTPS(t *testing.T) {
	target := Target{BaseURL: mustParseURL(t, "https://"+nonLoopbackTestHost+"/")}
	inner := stubProvider{tag: "https"}
	got := guardInsecureCredential(target, inner, false)
	if got != CredentialProvider(inner) {
		t.Fatalf("guardInsecureCredential returned %T, want the inner provider unwrapped for an https target", got)
	}
}

// TestGuardInsecureCredentialRefusesNonLoopbackPlainHTTP is MED-4's core
// case: a credential provider that DOES attach a header to a non-loopback
// plain-http target must be refused, by default, with no opt-in given.
func TestGuardInsecureCredentialRefusesNonLoopbackPlainHTTP(t *testing.T) {
	target := Target{BaseURL: mustParseURL(t, "http://"+nonLoopbackTestHost+"/")}
	inner := stubProvider{tag: "will-attach"}
	guarded := guardInsecureCredential(target, inner, false)
	if guarded == CredentialProvider(inner) {
		t.Fatal("guardInsecureCredential returned the inner provider unwrapped for a non-loopback plain-http target")
	}

	req := newTestRequest(t, "http://"+nonLoopbackTestHost+"/v0/beads/context")
	err := guarded.Authorize(context.Background(), req)
	if err == nil {
		t.Fatal("Authorize succeeded; want a refusal (plain http, non-loopback, no opt-in)")
	}
	if !strings.Contains(err.Error(), "refusing to send a credential") || !strings.Contains(err.Error(), AllowInsecureCredentialEnv) {
		t.Errorf("Authorize error = %q, want it to name the refusal and %s", err.Error(), AllowInsecureCredentialEnv)
	}
}

// TestGuardInsecureCredentialPassesThroughWhenNothingIsAttached: a provider
// that is consulted but attaches NOTHING (the ambient ladder with no token
// configured, the tip server's loopback-trust posture extended to a
// non-loopback target with no credential at all) must not be refused — the
// finding is specifically about a credential crossing the network, not about
// plain http to a non-loopback host in general.
func TestGuardInsecureCredentialPassesThroughWhenNothingIsAttached(t *testing.T) {
	target := Target{BaseURL: mustParseURL(t, "http://"+nonLoopbackTestHost+"/")}
	inner := noopProvider{}
	guarded := guardInsecureCredential(target, inner, false)

	req := newTestRequest(t, "http://"+nonLoopbackTestHost+"/v0/beads/context")
	if err := guarded.Authorize(context.Background(), req); err != nil {
		t.Fatalf("Authorize: %v, want no refusal since no credential was attached", err)
	}
	if req.Header.Get("Authorization") != "" {
		t.Fatalf("Authorization header = %q, want none", req.Header.Get("Authorization"))
	}
}

// TestGuardInsecureCredentialAllowedByOptIn proves DialOptions'
// AllowInsecureCredential=true (what connect.go sets from --allow-plaintext)
// lets the SAME combination through that the default test above refuses.
func TestGuardInsecureCredentialAllowedByOptIn(t *testing.T) {
	target := Target{BaseURL: mustParseURL(t, "http://"+nonLoopbackTestHost+"/")}
	inner := stubProvider{tag: "opted-in"}
	guarded := guardInsecureCredential(target, inner, true)
	if guarded != CredentialProvider(inner) {
		t.Fatalf("guardInsecureCredential(allowed=true) returned %T, want the inner provider unwrapped", guarded)
	}
}

// TestGuardInsecureCredentialAllowedByEnv proves the BEADS_HTTP_ALLOW_INSECURE
// escape hatch Open/OpenReadOnly/OpenWith's callers rely on (they have no
// flag of their own to set opts.AllowInsecureCredential through).
func TestGuardInsecureCredentialAllowedByEnv(t *testing.T) {
	t.Setenv(AllowInsecureCredentialEnv, "1")
	target := Target{BaseURL: mustParseURL(t, "http://"+nonLoopbackTestHost+"/")}
	inner := stubProvider{tag: "env-opted-in"}
	guarded := guardInsecureCredential(target, inner, false)
	if guarded != CredentialProvider(inner) {
		t.Fatalf("guardInsecureCredential(env opt-in) returned %T, want the inner provider unwrapped", guarded)
	}
}

// TestGuardInsecureCredentialDelegatesRefreshAndSource proves the refusal
// wrapper stays transparent to the two capabilities a CredentialProvider may
// also offer, so a 401's rotation retry and its source-naming both still work
// across an ALLOWED insecure dial.
func TestGuardInsecureCredentialDelegatesRefreshAndSource(t *testing.T) {
	target := Target{BaseURL: mustParseURL(t, "http://"+nonLoopbackTestHost+"/")}
	inner := &reportingProvider{source: "test-rung"}
	guarded := guardInsecureCredential(target, inner, true)
	if guarded != CredentialProvider(inner) {
		t.Fatalf("guardInsecureCredential(allowed=true) returned %T, want the inner provider unwrapped", guarded)
	}

	// With allowed=false the guard wraps, and must still forward both calls.
	wrapped := guardInsecureCredential(target, inner, false)
	retry, err := wrapped.Refresh(context.Background())
	if err != nil || !retry {
		t.Fatalf("Refresh = (%v, %v), want (true, nil)", retry, err)
	}
	reporter, ok := wrapped.(interface{ Source() string })
	if !ok {
		t.Fatal("wrapped provider does not implement Source() string")
	}
	if got := reporter.Source(); got != "test-rung" {
		t.Fatalf("Source() = %q, want %q", got, "test-rung")
	}
}

// TestDialWithWiresAllowInsecureCredentialThroughToTheGuard is the
// integration proof that DialWith actually applies guardInsecureCredential
// (not just that the helper is correct in isolation): a real ambient
// BearerProvider (BEADS_HTTP_TOKEN set) dialing a non-loopback plain-http
// target must be refused before the server ever sees the request, and the
// SAME dial must go through once AllowInsecureCredential is set — proving
// connect.go's --allow-plaintext -> DialOptions.AllowInsecureCredential wire
// actually reaches this guard.
func TestDialWithWiresAllowInsecureCredentialThroughToTheGuard(t *testing.T) {
	clearCredentialEnvironment(t)
	t.Setenv(TokenEnv, "integration-token")
	server := &contextServer{body: v0Context("proj-insecure-wiring")}
	srv := server.start(t)

	target := Target{BaseURL: mustParseURL(t, "http://"+nonLoopbackTestHost+"/")}
	redirecting := &http.Client{Transport: &redirectToAddrTransport{addr: strings.TrimPrefix(srv.URL, "http://")}}

	_, err := Handshake(context.Background(), target, DialOptions{HTTPClient: redirecting})
	if err == nil {
		t.Fatal("Handshake succeeded; want DialWith's insecure-credential guard to refuse")
	}
	if !strings.Contains(err.Error(), "refusing to send a credential") {
		t.Errorf("Handshake error = %v, want the insecure-credential refusal", err)
	}
	if len(server.seen) != 0 {
		t.Errorf("server saw %d request(s); want the guard to refuse before ever dialing", len(server.seen))
	}

	snap, err := Handshake(context.Background(), target, DialOptions{HTTPClient: redirecting, AllowInsecureCredential: true})
	if err != nil {
		t.Fatalf("Handshake with AllowInsecureCredential=true: %v", err)
	}
	if snap.ProjectId != "proj-insecure-wiring" {
		t.Errorf("project_id = %q, want proj-insecure-wiring", snap.ProjectId)
	}
}

// TestDialWithWiresTargetAllowInsecureCredentialThroughToTheGuard is
// TestDialWithWiresAllowInsecureCredentialThroughToTheGuard's companion for
// the PERSISTED grant (bee-ghosttrack CHANGES_REQUESTED on #7288,
// should-fix 2): `bd connect --allow-plaintext` now saves the opt-in onto
// the sidecar (Target.AllowInsecureCredential, round-tripped through
// SaveTarget/LoadTarget), so an ORDINARY command's dial — which builds
// DialOptions fresh with AllowInsecureCredential left false, unlike
// connect's own Handshake probe — must still get through on target's say
// alone. Without DialWith also consulting target.AllowInsecureCredential, a
// workspace connected with the flag would need BEADS_HTTP_ALLOW_INSECURE=1
// set for every later command too, which is exactly the gap this closes.
func TestDialWithWiresTargetAllowInsecureCredentialThroughToTheGuard(t *testing.T) {
	clearCredentialEnvironment(t)
	t.Setenv(TokenEnv, "integration-token")
	server := &contextServer{body: v0Context("proj-insecure-wiring-target")}
	srv := server.start(t)

	redirecting := &http.Client{Transport: &redirectToAddrTransport{addr: strings.TrimPrefix(srv.URL, "http://")}}

	plainTarget := Target{BaseURL: mustParseURL(t, "http://"+nonLoopbackTestHost+"/")}
	if _, err := Handshake(context.Background(), plainTarget, DialOptions{HTTPClient: redirecting}); err == nil {
		t.Fatal("Handshake with no grant anywhere succeeded; want the guard to refuse")
	}

	grantedTarget := Target{BaseURL: mustParseURL(t, "http://"+nonLoopbackTestHost+"/"), AllowInsecureCredential: true}
	snap, err := Handshake(context.Background(), grantedTarget, DialOptions{HTTPClient: redirecting})
	if err != nil {
		t.Fatalf("Handshake with target.AllowInsecureCredential=true (opts left false): %v", err)
	}
	if snap.ProjectId != "proj-insecure-wiring-target" {
		t.Errorf("project_id = %q, want proj-insecure-wiring-target", snap.ProjectId)
	}
}

// redirectToAddrTransport sends every request to addr instead of req.URL's
// own host, so a test can dial a real (loopback) httptest.Server while
// target.BaseURL carries a different, non-loopback-looking host — the shape
// needed to exercise the insecure-credential guard against a server that
// actually answers once the guard lets a request through.
type redirectToAddrTransport struct{ addr string }

func (r *redirectToAddrTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Host = r.addr
	clone.Host = r.addr
	return http.DefaultTransport.RoundTrip(clone)
}

// noopProvider never attaches anything — the ambient ladder's unconfigured
// state, or a third-party provider that only sometimes authorizes.
type noopProvider struct{}

func (noopProvider) Authorize(context.Context, *http.Request) error { return nil }
func (noopProvider) Refresh(context.Context) (bool, error)          { return false, nil }

// reportingProvider is a minimal CredentialProvider that also implements the
// wire package's unexported credentialSourceReporter shape structurally, to
// prove insecureCredentialGuard forwards both Refresh and Source.
type reportingProvider struct{ source string }

func (p *reportingProvider) Authorize(context.Context, *http.Request) error { return nil }
func (p *reportingProvider) Refresh(context.Context) (bool, error)          { return true, nil }
func (p *reportingProvider) Source() string                                 { return p.source }
