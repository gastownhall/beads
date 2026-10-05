// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/dial_test.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"net/http"
	"testing"
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
	if h.requestSeen {
		t.Error("server saw a request; TLS verification against the wrong CA should have failed first")
	}
}
