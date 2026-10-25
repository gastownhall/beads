// Written fresh for OSS beads S2 (no bd-enterprise source copied).
package httpclient

import (
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"testing"
)

// TestTransportForWrongCARefusesTheTLSHandshake is task #6's "wrong CA
// refused" case, proved end-to-end rather than by inspecting the transport's
// RootCAs field: a client built from a CA file that did NOT sign the server's
// leaf must fail the TLS handshake itself when it actually dials, the same
// way an operator who misconfigured BEADS_HTTP_CA_FILE would see it fail.
// ca_test.go already proves the RIGHT CA dials successfully over TLS
// (TestTransportForFileRotationClosesSupersededIdleConnections's live GETs,
// both before and after a rotation) and that TransportFor installs exactly
// the configured pool as RootCAs (TestTransportForReplacesRootPoolNotExtends)
// -- what neither proves is this negative: that a MISMATCHED pool is actually
// enforced at the wire, not merely recorded on the struct. ca.startServer
// exists for exactly this (a server whose leaf is independently verifiable
// against the CA that signed it) but had no caller before this test.
func TestTransportForWrongCARefusesTheTLSHandshake(t *testing.T) {
	clearCAEnvironment(t)
	right := newTestCA(t)
	wrong := newTestCA(t)

	u := right.startServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	})

	rt, err := TransportFor(Target{CAFile: wrong.writePEM(t)})
	if err != nil {
		t.Fatalf("TransportFor: %v", err)
	}
	forceIPv4Loopback(t, rt)
	client := &http.Client{Transport: rt}

	resp, err := client.Get(u.String())
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("GET succeeded against a server whose leaf the configured CA did not sign; want the TLS handshake to fail")
	}
	var unknownAuthority x509.UnknownAuthorityError
	if !errors.As(err, &unknownAuthority) {
		t.Fatalf("GET error = %v, want it to wrap x509.UnknownAuthorityError (a different TLS failure would also hide a real misconfiguration behind the wrong message)", err)
	}
}

// TestTransportForWrongCAViaEnvRefusesTheTLSHandshake is the BEADS_HTTP_CA_FILE
// env-driven sibling of TestTransportForWrongCARefusesTheTLSHandshake: the
// same wrong-CA negative, but resolved through resolveCAFile's host-scoped env
// syntax (CAFileEnv, "host[:port]=path") rather than Target.CAFile set
// directly. The direct-CAFile test above and this one together cover both of
// TransportFor's two input paths with the same live-TLS proof; neither alone
// shows the env-resolution path actually reaches the wire enforcement, since
// resolveCAFile could in principle resolve to the wrong path without the TLS
// layer ever being exercised against it.
func TestTransportForWrongCAViaEnvRefusesTheTLSHandshake(t *testing.T) {
	clearCAEnvironment(t)
	right := newTestCA(t)
	wrong := newTestCA(t)

	u := right.startServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	})

	t.Setenv(CAFileEnv, u.Host+"="+wrong.writePEM(t))

	rt, err := TransportFor(Target{BaseURL: u})
	if err != nil {
		t.Fatalf("TransportFor: %v", err)
	}
	forceIPv4Loopback(t, rt)
	client := &http.Client{Transport: rt}

	resp, err := client.Get(u.String())
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("GET succeeded against a server whose leaf the BEADS_HTTP_CA_FILE-named CA did not sign; want the TLS handshake to fail")
	}
	var unknownAuthority x509.UnknownAuthorityError
	if !errors.As(err, &unknownAuthority) {
		t.Fatalf("GET error = %v, want it to wrap x509.UnknownAuthorityError", err)
	}
}

// TestTransportForRightCASucceedsOverTLS is the positive control for the test
// above: it proves ca.startServer plus a CA file the leaf WAS signed by is a
// fixture that actually completes a TLS handshake, so the refusal above is
// this client's own verification doing its job rather than the fixture being
// broken in a way that would fail for any CA at all.
func TestTransportForRightCASucceedsOverTLS(t *testing.T) {
	clearCAEnvironment(t)
	ca := newTestCA(t)

	u := ca.startServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	})

	rt, err := TransportFor(Target{CAFile: ca.writePEM(t)})
	if err != nil {
		t.Fatalf("TransportFor: %v", err)
	}
	forceIPv4Loopback(t, rt)
	client := &http.Client{Transport: rt}

	resp, err := client.Get(u.String())
	if err != nil {
		t.Fatalf("GET against a server whose leaf the configured CA signed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != "ok" {
		t.Errorf("body = %q, want %q", body, "ok")
	}
}
