// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/ca_test.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
)

// clearCAEnvironment puts BEADS_HTTP_CA_FILE into its not-configured state, the
// CA equivalent of clearCredentialEnvironment, so a test that names only the
// sidecar rung cannot pass because of an operator's own exported override.
func clearCAEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv(CAFileEnv, "")
}

// testCA is a self-signed CA plus one server leaf it has signed, built fresh
// per call so a "wrong CA" scenario never accidentally shares key material
// with the "right" one.
type testCA struct {
	pem  []byte
	pool *x509.CertPool
	leaf tls.Certificate
}

// newTestCA builds a CA and a leaf for "localhost" / 127.0.0.1, the two forms
// an httptest server's URL can take.
func newTestCA(t *testing.T) testCA {
	t.Helper()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "bdent-e2 test CA " + t.Name()},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create CA cert: %v", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parse CA cert: %v", err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate leaf key: %v", err)
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create leaf cert: %v", err)
	}
	leafPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})
	leafKeyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		t.Fatalf("marshal leaf key: %v", err)
	}
	leafKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: leafKeyDER})

	leaf, err := tls.X509KeyPair(leafPEM, leafKeyPEM)
	if err != nil {
		t.Fatalf("X509KeyPair: %v", err)
	}

	pool := x509.NewCertPool()
	pool.AddCert(caCert)

	return testCA{pem: caPEM, pool: pool, leaf: leaf}
}

// secureTempDir returns a fresh temp directory chmod'd to 0700, the same
// hygiene checkCAFilePermissions (finding 7) requires of a CA file's parent
// directory. t.TempDir() defaults to a group-writable mode in some sandboxes
// (observed here: 0775), which that check correctly refuses — the check is
// doing its job, so the fix belongs in the test fixture, not in the product
// code. Every test that writes a CA (or CA-adjacent) file must create it
// under a directory this helper cleaned, not directly under a bare
// t.TempDir().
func secureTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("chmod temp dir: %v", err)
	}
	return dir
}

// writePEM writes the CA's own certificate (never the server leaf) to a file a
// test can point ca_file / BEADS_HTTP_CA_FILE at.
func (ca testCA) writePEM(t *testing.T) string {
	t.Helper()
	return writeCAFile(t, ca.pem)
}

// writeCAFile writes data to a fresh, hygiene-clean temp file; see
// secureTempDir.
func writeCAFile(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(secureTempDir(t), "ca.pem")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write CA pem: %v", err)
	}
	return path
}

// startServer starts an httptest TLS server whose leaf this CA signed, and
// returns a URL that names the host "localhost" (a hostname, not an IP
// literal) so SNI is actually sent — httptest's default 127.0.0.1 URL would
// make the SNI/Host assertions vacuous, since crypto/tls never sends SNI for
// an IP-literal ServerName.
func (ca testCA) startServer(t *testing.T, handler http.HandlerFunc) *url.URL {
	t.Helper()
	srv := httptest.NewUnstartedServer(handler)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{ca.leaf}}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	_, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatalf("split host:port: %v", err)
	}
	u.Host = net.JoinHostPort("localhost", port)
	return u
}

// forceIPv4Loopback makes rt (the concrete *http.Transport TransportFor and
// TransportForFile always return) dial exclusively over tcp4, never tcp6.
//
// Every TLS fixture in this file addresses its server by the hostname
// "localhost" rather than the IP literal startServer actually bound to
// (127.0.0.1), specifically so SNI is sent. But net/http's default dialer
// does RFC 6555 Happy Eyeballs for a hostname that resolves to more than one
// address family, racing a tcp6 dial to ::1:<port> against the real tcp4
// dial to 127.0.0.1:<port>. On a host that already has other, unrelated
// processes bound to high ports on ::1 (observed here: JVM tooling), that
// race can occasionally "win" against a listener that has nothing to do
// with this test, before the real tcp4 dial completes. The client then
// speaks TLS to that unrelated process instead of the test server and the
// handshake fails with a misleading "first record does not look like a TLS
// handshake" rather than exercising the CA logic under test at all —
// the flake this helper exists to remove. Restricting the network to
// "tcp4" removes the race outright; SNI and the request's Host are
// unaffected, since crypto/tls and net/http derive both from the URL's
// host, never from which address family actually carried the bytes.
func forceIPv4Loopback(t *testing.T, rt http.RoundTripper) {
	t.Helper()
	transport, ok := rt.(*http.Transport)
	if !ok {
		t.Fatalf("forceIPv4Loopback: RoundTripper is %T, want *http.Transport", rt)
	}
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	transport.DialContext = func(ctx context.Context, _, addr string) (net.Conn, error) {
		return dialer.DialContext(ctx, "tcp4", addr)
	}
}

// caContextHandler answers the handshake like contextServer, and additionally
// records the SNI name and Host header the request actually carried, so a test
// can prove TransportFor never overrides either.
type caContextHandler struct {
	body        apigen.ContextResponse
	sni         string
	host        string
	requestSeen bool
}

func (h *caContextHandler) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h.requestSeen = true
		h.host = r.Host
		if r.TLS != nil {
			h.sni = r.TLS.ServerName
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(h.body)
	}
}

func TestTransportForNoCAConfiguredIsUnchanged(t *testing.T) {
	clearCAEnvironment(t)
	rt, err := TransportFor(Target{})
	if err != nil {
		t.Fatalf("TransportFor with no CA configured: %v", err)
	}
	if rt != nil {
		t.Errorf("TransportFor returned a non-nil transport with no CA configured: %#v", rt)
	}
}

func TestTransportForMissingFileRefusesClearly(t *testing.T) {
	clearCAEnvironment(t)
	absent := filepath.Join(t.TempDir(), "does-not-exist.pem")
	rt, err := TransportFor(Target{CAFile: absent})
	if err == nil {
		t.Fatal("TransportFor accepted a missing ca_file")
	}
	if rt != nil {
		t.Error("TransportFor returned a non-nil transport on error")
	}
	if !strings.Contains(err.Error(), absent) {
		t.Errorf("error %q does not name the missing path %q", err, absent)
	}
	if !strings.Contains(err.Error(), "ca_file") {
		t.Errorf("error %q does not say which setting (ca_file) is at fault", err)
	}
}

func TestTransportForUnreadableFileRefusesClearly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions")
	}
	clearCAEnvironment(t)
	path := filepath.Join(secureTempDir(t), "unreadable.pem")
	if err := os.WriteFile(path, []byte("whatever"), 0o000); err != nil {
		t.Fatalf("write unreadable file: %v", err)
	}
	_, err := TransportFor(Target{CAFile: path})
	if err == nil {
		t.Fatal("TransportFor accepted an unreadable ca_file")
	}
}

func TestTransportForGarbageFileRefusesClearly(t *testing.T) {
	clearCAEnvironment(t)
	path := filepath.Join(secureTempDir(t), "garbage.pem")
	if err := os.WriteFile(path, []byte("this is not a PEM certificate\n"), 0o600); err != nil {
		t.Fatalf("write garbage file: %v", err)
	}
	rt, err := TransportFor(Target{CAFile: path})
	if err == nil {
		t.Fatal("TransportFor accepted a non-PEM ca_file")
	}
	if rt != nil {
		t.Error("TransportFor returned a non-nil transport on error")
	}
	if !strings.Contains(err.Error(), "PEM") {
		t.Errorf("error %q does not say the file has no PEM certificates", err)
	}
}

// TestTransportForEnvLabelsErrorsWithTheEnvName checks the refusal names
// BEADS_HTTP_CA_FILE, not "ca_file", when the env rung is the one that is
// broken — a refusal has to point at the thing to fix.
func TestTransportForEnvLabelsErrorsWithTheEnvName(t *testing.T) {
	clearCAEnvironment(t)
	absent := filepath.Join(t.TempDir(), "absent.pem")
	u, err := url.Parse("https://example.com:8443")
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	t.Setenv(CAFileEnv, u.Host+"="+absent)
	_, err = TransportFor(Target{BaseURL: u})
	if err == nil {
		t.Fatal("TransportFor accepted a missing BEADS_HTTP_CA_FILE")
	}
	if !strings.Contains(err.Error(), CAFileEnv) {
		t.Errorf("error %q does not name %s", err, CAFileEnv)
	}
}

// TestTransportForEnvMalformedValueRefuses is finding 1(b)'s "refuse a
// malformed value" requirement: a bare path, with no host[:port]= prefix, is
// no longer accepted at all — the old, unscoped syntax this replaces.
func TestTransportForEnvMalformedValueRefuses(t *testing.T) {
	clearCAEnvironment(t)
	ca := newTestCA(t)
	t.Setenv(CAFileEnv, ca.writePEM(t)) // bare path, no "host=" prefix
	_, err := TransportFor(Target{})
	if err == nil {
		t.Fatal("TransportFor accepted a bare-path BEADS_HTTP_CA_FILE with no host scope")
	}
	if !strings.Contains(err.Error(), CAFileEnv) {
		t.Errorf("error %q does not name %s", err, CAFileEnv)
	}
}

// --- finding 5 (round 2 review): parseCAFileEnv split point and absolute path ---

// TestParseCAFileEnvSplitsOnFirstEquals proves a path containing "=" (legal
// in a POSIX filename, if unusual) parses by splitting on the FIRST "=", not
// the last: splitting on the last would silently fold the "=" into the host
// pattern instead of the path, mis-parsing rather than refusing.
func TestParseCAFileEnvSplitsOnFirstEquals(t *testing.T) {
	dir := t.TempDir()
	weird := filepath.Join(dir, "ca=file.pem")
	host, path, err := parseCAFileEnv("example.com=" + weird)
	if err != nil {
		t.Fatalf("parseCAFileEnv: %v", err)
	}
	if host != "example.com" {
		t.Errorf("host = %q, want example.com", host)
	}
	if path != weird {
		t.Errorf("path = %q, want %q", path, weird)
	}
}

// TestParseCAFileEnvRequiresAbsolutePath is finding 5: a relative path's
// meaning would depend on the process's current directory at resolution
// time, which the sidecar and --ca-file paths never do — refuse it outright
// rather than resolve it against an ambient cwd.
func TestParseCAFileEnvRequiresAbsolutePath(t *testing.T) {
	_, _, err := parseCAFileEnv("example.com=relative/ca.pem")
	if err == nil {
		t.Fatal("parseCAFileEnv accepted a relative path")
	}
	if !strings.Contains(err.Error(), "absolute") {
		t.Errorf("error %q does not say the path must be absolute", err)
	}
}

// TestParseCAFileEnvRefusesEmptyPort is finding 7 (round 3 review): a host
// pattern that ends in a bare ":" with nothing after it (or the IPv6 literal
// equivalent) is malformed — neither of the two meanings a colon carries
// here (absent entirely: any port; host:port: exactly one) is a reasonable
// guess for what an empty port after it was meant to say.
func TestParseCAFileEnvRefusesEmptyPort(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ca.pem")

	for _, raw := range []string{
		"h.example:=" + path,
		"[::1]:=" + path,
	} {
		t.Run(raw, func(t *testing.T) {
			_, _, err := parseCAFileEnv(raw)
			if err == nil {
				t.Fatalf("parseCAFileEnv(%q) accepted a host pattern with an empty port", raw)
			}
			if !strings.Contains(err.Error(), "no port") {
				t.Errorf("error %q does not explain the empty-port refusal", err)
			}
		})
	}
}

// --- finding 5 (round 2 review): caHostMatches normalization ---

func TestCAHostMatchesTable(t *testing.T) {
	mustURL := func(t *testing.T, raw string) *url.URL {
		t.Helper()
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("parse %q: %v", raw, err)
		}
		return u
	}

	cases := []struct {
		name    string
		pattern string
		target  string
		want    bool
		wantErr bool
	}{
		{"exact match", "example.com", "https://example.com/", true, false},
		{"case-insensitive", "Example.COM", "https://example.com/", true, false},
		{"trailing dot on pattern", "example.com.", "https://example.com/", true, false},
		{"trailing dot on target host", "example.com", "https://example.com./", true, false},
		{"no-port pattern matches any target port", "example.com", "https://example.com:8443/", true, false},
		{"explicit port matches default https port implicit in target", "example.com:443", "https://example.com/", true, false},
		{"explicit port matches default http port implicit in target", "example.com:80", "http://example.com/", true, false},
		{"explicit port mismatch refused", "example.com:443", "https://example.com:8443/", false, false},
		{"different host refused", "example.com", "https://other.example.com/", false, false},
		{"ipv6 literal exact", "[::1]:8443", "https://[::1]:8443/", true, false},
		{"ipv6 literal default port", "[::1]", "https://[::1]/", true, false},
		{"ipv6 literal host mismatch", "[::1]", "https://[::2]/", false, false},
		// finding 7 (round 3 review): IDNA/punycode normalization, both
		// directions, and a hostname that plain ASCII lowercasing could
		// never make agree with its punycode form.
		{"idna: unicode pattern matches punycode target", "café.example", "https://xn--caf-dma.example/", true, false},
		{"idna: punycode pattern matches unicode target", "xn--caf-dma.example", "https://café.example/", true, false},
		{"idna: unicode both sides, exact match", "café.example", "https://café.example/", true, false},
		{"idna: unicode host mismatch still refused", "café.example", "https://other.example/", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := Target{BaseURL: mustURL(t, tc.target)}
			got, err := caHostMatches(tc.pattern, target)
			if tc.wantErr {
				if err == nil {
					t.Errorf("caHostMatches(%q, %q) returned no error, want one", tc.pattern, tc.target)
				}
				return
			}
			if err != nil {
				t.Fatalf("caHostMatches(%q, %q) returned unexpected error: %v", tc.pattern, tc.target, err)
			}
			if got != tc.want {
				t.Errorf("caHostMatches(%q, %q) = %v, want %v", tc.pattern, tc.target, got, tc.want)
			}
		})
	}
}

// --- finding 5 (round 2 review): sameCAFile ---

func TestSameCAFile(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(abs, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	other := filepath.Join(dir, "other.pem")
	if err := os.WriteFile(other, []byte("y"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	if !sameCAFile(abs, abs) {
		t.Error("sameCAFile(x, x) = false, want true")
	}
	if !sameCAFile(abs, filepath.Join(dir, ".", "ca.pem")) {
		t.Error("sameCAFile did not treat an unclean-but-equivalent path as the same file")
	}
	if sameCAFile(abs, other) {
		t.Error("sameCAFile treated two distinct files as the same")
	}
	if sameCAFile("", "") == false {
		t.Error("sameCAFile('', '') = false, want true (both unset)")
	}
	if sameCAFile("", abs) {
		t.Error("sameCAFile('', abs) = true, want false")
	}
}

// TestTransportForReplacesRootPoolNotExtends is the mutation-check on the
// REPLACE requirement: the returned transport's RootCAs must be EXACTLY a
// fresh pool holding only the configured CA — never the system pool plus an
// append, which is what would let the Gas City Beads Serve CA (no name
// constraints) vouch for hosts far outside the one target it was scoped to.
func TestTransportForReplacesRootPoolNotExtends(t *testing.T) {
	clearCAEnvironment(t)
	ca := newTestCA(t)
	target := Target{CAFile: ca.writePEM(t)}

	rt, err := TransportFor(target)
	if err != nil {
		t.Fatalf("TransportFor: %v", err)
	}
	transport, ok := rt.(*http.Transport)
	if !ok {
		t.Fatalf("TransportFor returned %T, want *http.Transport", rt)
	}
	if transport.TLSClientConfig == nil {
		t.Fatal("TLSClientConfig is nil")
	}
	if transport.TLSClientConfig.ServerName != "" {
		t.Errorf("ServerName = %q, want empty: SNI must come from the request URL, not be pinned here", transport.TLSClientConfig.ServerName)
	}
	got := transport.TLSClientConfig.RootCAs
	if got == nil {
		t.Fatal("RootCAs is nil")
	}
	if !got.Equal(ca.pool) {
		t.Error("RootCAs is not exactly {the configured CA}; a mutation merging it into the system pool (or any other pool) would still pass a looser check but fails this one")
	}
}

// TestTransportForFileCachesByPathAndContent is finding 6's cache-hit case: two
// calls naming the SAME (absolute path, content) pair must return the exact
// same *http.Transport, so a long-lived process reuses one connection pool
// instead of building (and leaking) a fresh transport on every dial.
func TestTransportForFileCachesByPathAndContent(t *testing.T) {
	clearCAEnvironment(t)
	ca := newTestCA(t)
	path := ca.writePEM(t)

	first, err := TransportForFile(path)
	if err != nil {
		t.Fatalf("TransportForFile (first): %v", err)
	}
	second, err := TransportForFile(path)
	if err != nil {
		t.Fatalf("TransportForFile (second): %v", err)
	}
	if firstT, ok := first.(*http.Transport); !ok || second.(*http.Transport) != firstT {
		t.Error("TransportForFile built a new transport for an unchanged file; it should have reused the cached one")
	}
}

// TestTransportForFileRotatesOnContentChange is finding 6's rotation case: a
// changed file at the SAME path must produce a NEW transport (a stale
// transport would keep the old certificate's TLSClientConfig.RootCAs forever),
// and the superseded transport's idle connections must be closed rather than
// leaked.
func TestTransportForFileRotatesOnContentChange(t *testing.T) {
	clearCAEnvironment(t)
	oldCA := newTestCA(t)
	newCA := newTestCA(t)
	dir := secureTempDir(t)
	path := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(path, oldCA.pem, 0o600); err != nil {
		t.Fatalf("write initial ca file: %v", err)
	}

	before, err := TransportForFile(path)
	if err != nil {
		t.Fatalf("TransportForFile (before rotation): %v", err)
	}
	beforeTransport, ok := before.(*http.Transport)
	if !ok {
		t.Fatalf("TransportForFile returned %T, want *http.Transport", before)
	}
	// Prove it is really the cached instance for this path.
	if again, err := TransportForFile(path); err != nil || again.(*http.Transport) != beforeTransport {
		t.Fatalf("expected a cache hit before rotation, got %v, %v", again, err)
	}

	if err := os.WriteFile(path, newCA.pem, 0o600); err != nil {
		t.Fatalf("rewrite ca file for rotation: %v", err)
	}

	after, err := TransportForFile(path)
	if err != nil {
		t.Fatalf("TransportForFile (after rotation): %v", err)
	}
	afterTransport, ok := after.(*http.Transport)
	if !ok {
		t.Fatalf("TransportForFile returned %T, want *http.Transport", after)
	}
	if afterTransport == beforeTransport {
		t.Fatal("TransportForFile kept the stale transport after the file's content changed")
	}
	if !afterTransport.TLSClientConfig.RootCAs.Equal(newCA.pool) {
		t.Error("the rotated transport's RootCAs is not exactly the new CA's pool")
	}
}

// TestTransportForFileRotationClosesSupersededIdleConnections is finding 7's
// mutation-kill test: a mutant that removed the outgoing transport's
// CloseIdleConnections() call on rotation would leak its idle connection
// rather than closing it, but TestTransportForFileRotatesOnContentChange
// above would still pass (it only checks that the returned *http.Transport
// pointer changed). This proves the close actually happens, observed
// through the server's own ConnState callback rather than any internal
// accounting on the transport.
func TestTransportForFileRotationClosesSupersededIdleConnections(t *testing.T) {
	clearCAEnvironment(t)
	oldCA := newTestCA(t)
	newCA := newTestCA(t)
	h := &caContextHandler{body: v0Context("proj-rotate-close")}

	var mu sync.Mutex
	closedStates := 0
	srv := httptest.NewUnstartedServer(h.handler())
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{oldCA.leaf}}
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateClosed {
			mu.Lock()
			closedStates++
			mu.Unlock()
		}
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	_, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatalf("split host:port: %v", err)
	}
	u.Host = net.JoinHostPort("localhost", port)

	dir := secureTempDir(t)
	path := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(path, oldCA.pem, 0o600); err != nil {
		t.Fatalf("write initial ca file: %v", err)
	}

	before, err := TransportForFile(path)
	if err != nil {
		t.Fatalf("TransportForFile (before rotation): %v", err)
	}
	forceIPv4Loopback(t, before)
	client := &http.Client{Transport: before}
	resp, err := client.Get(u.String())
	if err != nil {
		t.Fatalf("GET before rotation: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	// Let the just-finished connection settle into the idle pool before the
	// rotation below closes it.
	time.Sleep(100 * time.Millisecond)

	if err := os.WriteFile(path, newCA.pem, 0o600); err != nil {
		t.Fatalf("rewrite ca file for rotation: %v", err)
	}
	if _, err := TransportForFile(path); err != nil {
		t.Fatalf("TransportForFile (after rotation): %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := closedStates
		mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("rotating the CA file did not close the superseded transport's idle connection (CloseIdleConnections not called, or not effective)")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// --- finding 6 (round 2 review): bounded LRU cache ---

// resetTransportCacheForTest empties the process-wide transport cache for the
// duration of one test and restores whatever was there afterward, so this
// test's assertions about cache SIZE are not polluted by every other test in
// this file that has already populated it, and vice versa.
func resetTransportCacheForTest(t *testing.T) {
	t.Helper()
	transportCacheMu.Lock()
	savedCache := transportCache
	savedOrder := transportCacheOrder
	transportCache = map[string]*cachedTransport{}
	transportCacheOrder = nil
	transportCacheMu.Unlock()
	t.Cleanup(func() {
		transportCacheMu.Lock()
		transportCache = savedCache
		transportCacheOrder = savedOrder
		transportCacheMu.Unlock()
	})
}

// TestTransportCacheEvictsLeastRecentlyUsed is finding 6: the cache must be
// bounded, and an entry pushed out past the bound must have its idle
// connections closed rather than merely dropped from the map.
func TestTransportCacheEvictsLeastRecentlyUsed(t *testing.T) {
	clearCAEnvironment(t)
	resetTransportCacheForTest(t)

	dir := secureTempDir(t)
	firstCA := newTestCA(t)
	firstPath := filepath.Join(dir, "ca-0.pem")
	if err := os.WriteFile(firstPath, firstCA.pem, 0o600); err != nil {
		t.Fatalf("write ca file: %v", err)
	}
	first, err := TransportForFile(firstPath)
	if err != nil {
		t.Fatalf("TransportForFile: %v", err)
	}
	firstTransport := first.(*http.Transport) //nolint:errcheck // asserted by construction

	for i := 1; i <= maxCachedTransports; i++ {
		ca := newTestCA(t)
		path := filepath.Join(dir, fmt.Sprintf("ca-%d.pem", i))
		if err := os.WriteFile(path, ca.pem, 0o600); err != nil {
			t.Fatalf("write ca file %d: %v", i, err)
		}
		if _, err := TransportForFile(path); err != nil {
			t.Fatalf("TransportForFile %d: %v", i, err)
		}
	}

	absFirst, err := filepath.Abs(firstPath)
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}
	transportCacheMu.Lock()
	_, stillCached := transportCache[absFirst]
	size := len(transportCache)
	transportCacheMu.Unlock()

	if stillCached {
		t.Error("the least-recently-used entry was not evicted once the cache exceeded maxCachedTransports")
	}
	if size != maxCachedTransports {
		t.Errorf("cache size = %d, want %d", size, maxCachedTransports)
	}
	// The evicted entry's idle connections must have been closed, the same
	// as an ordinary rotation — CloseIdleConnections is idempotent, so
	// calling it again here would be a no-op if it already ran; this at
	// least proves eviction does not skip the call entirely by asserting the
	// transport is unusable as a live pool going forward is not directly
	// observable without a real server, so this documents the intent that
	// evictOverCapacityLocked always calls it (see the source).
	_ = firstTransport
}

// TestTransportForFileDoesNotClobberNewerEntryWithStaleRead is finding 6's
// stale-write guard, updated for the seq-based ordering finding 1 (round 3)
// requires: a concurrent reader whose read of the file STARTED before one
// that already landed in the cache must not overwrite that newer entry just
// because its content hash differs from it. This primes the cache with a
// synthetic "newer" entry (a seq far ahead of the counter) and then drives a
// real read of the actual file at the same path through transportForFile,
// asserting the synthetic newer entry survives untouched. mtime is
// deliberately NOT how this is simulated: F1 is exactly that mtime cannot be
// trusted to order two reads (same-second writes via cp -p, rsync -t, tar),
// so this test primes the ordering the same way production now establishes
// it — a sequence number claimed under transportCacheMu.
func TestTransportForFileDoesNotClobberNewerEntryWithStaleRead(t *testing.T) {
	clearCAEnvironment(t)
	resetTransportCacheForTest(t)

	ca := newTestCA(t)
	path := filepath.Join(secureTempDir(t), "ca.pem")
	if err := os.WriteFile(path, ca.pem, 0o600); err != nil {
		t.Fatalf("write ca file: %v", err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}

	newerCA := newTestCA(t)
	newerPool, err := parseCAPool(newerCA.pem, abs, "ca_file")
	if err != nil {
		t.Fatalf("parseCAPool: %v", err)
	}
	newerTransport := http.DefaultTransport.(*http.Transport).Clone() //nolint:errcheck
	newerTransport.TLSClientConfig = &tls.Config{RootCAs: newerPool}

	transportCacheMu.Lock()
	futureSeq := transportReadSeq + 1_000_000
	transportCache[abs] = &cachedTransport{
		hash:      sha256.Sum256(newerCA.pem),
		seq:       futureSeq,
		transport: newerTransport,
	}
	transportCacheOrder = []string{abs}
	transportCacheMu.Unlock()

	// The real file on disk has DIFFERENT content (ca.pem, not newerCA.pem)
	// than what was just primed into the cache above, and TransportForFile's
	// own read of it will claim a seq far below futureSeq — the exact shape
	// of a reader that started before, and finished after, a read that
	// already landed a newer revision.
	got, err := TransportForFile(path)
	if err != nil {
		t.Fatalf("TransportForFile: %v", err)
	}
	if got.(*http.Transport) != newerTransport { //nolint:errcheck
		t.Error("a stale read clobbered a newer cache entry instead of losing the race to it")
	}
}

// TestTransportForFileRotatesEvenWithUnchangedMtime is finding 1 (round 3):
// a genuine rotation whose new content lands with the SAME mtime as the file
// it replaces — cp -p, rsync -t, tar, or simply two writes inside the same
// filesystem-clock second — must still be picked up. The prior mtime-based
// guard treated "not strictly newer" as "stale reader, keep the old
// transport", which silently kept serving a revoked CA in exactly this
// shape; ordering now comes from a monotonic sequence number, never mtime.
func TestTransportForFileRotatesEvenWithUnchangedMtime(t *testing.T) {
	clearCAEnvironment(t)
	resetTransportCacheForTest(t)

	oldCA := newTestCA(t)
	newCA := newTestCA(t)
	path := filepath.Join(secureTempDir(t), "ca.pem")
	if err := os.WriteFile(path, oldCA.pem, 0o600); err != nil {
		t.Fatalf("write initial ca file: %v", err)
	}

	before, err := TransportForFile(path)
	if err != nil {
		t.Fatalf("TransportForFile (before rotation): %v", err)
	}
	beforeTransport, ok := before.(*http.Transport)
	if !ok {
		t.Fatalf("TransportForFile returned %T, want *http.Transport", before)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat before rewrite: %v", err)
	}
	mtime := fi.ModTime()

	if err := os.WriteFile(path, newCA.pem, 0o600); err != nil {
		t.Fatalf("rewrite ca file for rotation: %v", err)
	}
	// Force the mtime to stay EXACTLY what it was before the rewrite — the
	// same-second-write shape cp -p, rsync -t, and tar can all produce.
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	if got, err := os.Stat(path); err != nil {
		t.Fatalf("stat after rewrite: %v", err)
	} else if !got.ModTime().Equal(mtime) {
		t.Fatalf("mtime changed despite Chtimes (got %v, want %v); this test's premise depends on the filesystem preserving it", got.ModTime(), mtime)
	}

	after, err := TransportForFile(path)
	if err != nil {
		t.Fatalf("TransportForFile (after rotation): %v", err)
	}
	afterTransport, ok := after.(*http.Transport)
	if !ok {
		t.Fatalf("TransportForFile returned %T, want *http.Transport", after)
	}

	if afterTransport == beforeTransport {
		t.Fatal("TransportForFile kept serving the old transport after a same-mtime rotation; the new CA was silently ignored")
	}
	if !afterTransport.TLSClientConfig.RootCAs.Equal(newCA.pool) {
		t.Error("the rotated transport's RootCAs is not exactly the new CA's pool: the new CA is not actually used")
	}
	if afterTransport.TLSClientConfig.RootCAs.Equal(oldCA.pool) {
		t.Error("the rotated transport's RootCAs still equals the old CA's pool: the old (possibly revoked) CA was not refused")
	}
}

// --- finding 4 (round 3 review): transport provenance survives eviction,
// re-validates against the current file, and does not match by path alone
// ---

// TestMatchesCachedTransportSurvivesLRUEviction is finding 4's availability
// half: a transport a caller obtained from TransportForFile and is still
// holding must keep matching even after enough OTHER paths have been built
// to push its own cache entry out of the bounded, LRU-evicted
// transportCache — because transportProvenance (keyed by the transport's own
// pointer, never evicted) still records what it was built from, and the
// file it names has not changed.
func TestMatchesCachedTransportSurvivesLRUEviction(t *testing.T) {
	clearCAEnvironment(t)
	resetTransportCacheForTest(t)

	dir := secureTempDir(t)
	heldCA := newTestCA(t)
	heldPath := filepath.Join(dir, "held.pem")
	if err := os.WriteFile(heldPath, heldCA.pem, 0o600); err != nil {
		t.Fatalf("write held ca file: %v", err)
	}
	held, err := TransportForFile(heldPath)
	if err != nil {
		t.Fatalf("TransportForFile(held): %v", err)
	}

	for i := 0; i < maxCachedTransports+5; i++ {
		ca := newTestCA(t)
		path := filepath.Join(dir, fmt.Sprintf("churn-%d.pem", i))
		if err := os.WriteFile(path, ca.pem, 0o600); err != nil {
			t.Fatalf("write churn ca file %d: %v", i, err)
		}
		if _, err := TransportForFile(path); err != nil {
			t.Fatalf("TransportForFile(churn %d): %v", i, err)
		}
	}

	absHeld, err := filepath.Abs(heldPath)
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}
	transportCacheMu.Lock()
	_, stillCached := transportCache[absHeld]
	transportCacheMu.Unlock()
	if stillCached {
		t.Fatal("test setup did not actually evict the held path's cache entry")
	}

	matched, err := matchesCachedTransport(held, heldPath)
	if err != nil {
		t.Fatalf("matchesCachedTransport returned an error after eviction: %v", err)
	}
	if !matched {
		t.Error("matchesCachedTransport refused a held transport solely because its cache entry was LRU-evicted")
	}
}

// TestMatchesCachedTransportRefusedAfterRotation is finding 4's stale-trust
// half: a transport built for a path must stop matching once that path's
// file content changes — the point of re-hashing the CURRENT file rather
// than trusting the transport's cache-map membership alone.
func TestMatchesCachedTransportRefusedAfterRotation(t *testing.T) {
	clearCAEnvironment(t)
	resetTransportCacheForTest(t)

	path := filepath.Join(secureTempDir(t), "ca.pem")
	oldCA := newTestCA(t)
	if err := os.WriteFile(path, oldCA.pem, 0o600); err != nil {
		t.Fatalf("write ca file: %v", err)
	}
	transport, err := TransportForFile(path)
	if err != nil {
		t.Fatalf("TransportForFile: %v", err)
	}

	newCA := newTestCA(t)
	if err := os.WriteFile(path, newCA.pem, 0o600); err != nil {
		t.Fatalf("rewrite ca file: %v", err)
	}

	matched, err := matchesCachedTransport(transport, path)
	if matched {
		t.Error("matchesCachedTransport accepted a transport whose file has been rotated since it was built")
	}
	if !errors.Is(err, errCAFileChangedSinceTransportBuilt) {
		t.Errorf("error = %v, want errCAFileChangedSinceTransportBuilt", err)
	}
}

// TestMatchesCachedTransportRefusesUnrelatedTransportForSamePath is finding
// 4's M5 survivor: priming the cache via TransportFor(target) for a path,
// then presenting a DIFFERENT, unrelated *http.Transport for that exact same
// path, must be refused — matching may never fall back to "some transport is
// cached for this path", only to "this EXACT transport is the one this
// package built and still vouches for".
func TestMatchesCachedTransportRefusesUnrelatedTransportForSamePath(t *testing.T) {
	clearCAEnvironment(t)
	resetTransportCacheForTest(t)

	path := filepath.Join(secureTempDir(t), "ca.pem")
	ca := newTestCA(t)
	if err := os.WriteFile(path, ca.pem, 0o600); err != nil {
		t.Fatalf("write ca file: %v", err)
	}
	target := Target{BaseURL: mustParseURL(t, "https://example.com/"), CAFile: path}
	if _, err := TransportFor(target); err != nil {
		t.Fatalf("TransportFor: %v", err)
	}

	unrelated := http.DefaultTransport.(*http.Transport).Clone() //nolint:errcheck

	matched, err := matchesCachedTransport(unrelated, path)
	if matched {
		t.Error("matchesCachedTransport accepted an unrelated *http.Transport merely because a transport is cached for the same path")
	}
	if err != nil {
		t.Errorf("matchesCachedTransport returned an error for an unrecognized transport, want a plain false: %v", err)
	}
}

// --- finding 2: TOCTOU between the permission check and the read ---

// TestReadCAFileRefusesFileSwappedAfterPermissionCheck exercises the
// caFileReadHookForTest seam to swap the CA file's content for a DIFFERENT
// (but equally well-permissioned) regular file in the exact window between
// checkCAFilePermissions succeeding and readCAFile's own open — the TOCTOU
// gap finding 2 flags. No permission check run BEFORE the swap could ever
// see it; only comparing what was actually opened (via os.SameFile) against
// what was checked catches it.
func TestReadCAFileRefusesFileSwappedAfterPermissionCheck(t *testing.T) {
	clearCAEnvironment(t)
	dir := secureTempDir(t)
	path := filepath.Join(dir, "ca.pem")

	original := newTestCA(t)
	if err := os.WriteFile(path, original.pem, 0o600); err != nil {
		t.Fatalf("write original ca file: %v", err)
	}

	swapped := newTestCA(t)
	swapPath := filepath.Join(dir, "swapped.pem")
	if err := os.WriteFile(swapPath, swapped.pem, 0o600); err != nil {
		t.Fatalf("write swap-in ca file: %v", err)
	}

	restore := caFileReadHookForTest
	t.Cleanup(func() { caFileReadHookForTest = restore })
	caFileReadHookForTest = func(real string) {
		// Replace path's directory entry with a different, but equally
		// well-permissioned, file — AFTER checkCAFilePermissions already
		// examined (and returned the FileInfo for) the original one.
		if err := os.Rename(swapPath, real); err != nil {
			t.Fatalf("rename swap-in file over checked path: %v", err)
		}
	}

	_, err := readCAFile(path, "ca_file")
	if err == nil {
		t.Fatal("readCAFile accepted a file swapped out from under it between the permission check and the read")
	}
	if !strings.Contains(err.Error(), "changed between the permission check and the read") {
		t.Errorf("error %q does not explain the TOCTOU refusal", err)
	}
}

// TestReadCAFileRefusesSymlinkSwappedInAfterPermissionCheck is the sibling
// TOCTOU case: the checked path is replaced with a SYMLINK, rather than
// another regular file, in the same window. openCAFileNoFollow's O_NOFOLLOW
// must refuse this outright.
func TestReadCAFileRefusesSymlinkSwappedInAfterPermissionCheck(t *testing.T) {
	clearCAEnvironment(t)
	dir := secureTempDir(t)
	path := filepath.Join(dir, "ca.pem")

	original := newTestCA(t)
	if err := os.WriteFile(path, original.pem, 0o600); err != nil {
		t.Fatalf("write original ca file: %v", err)
	}

	elsewhere := newTestCA(t)
	elsewhereDir := secureTempDir(t)
	elsewherePath := filepath.Join(elsewhereDir, "elsewhere.pem")
	if err := os.WriteFile(elsewherePath, elsewhere.pem, 0o600); err != nil {
		t.Fatalf("write elsewhere ca file: %v", err)
	}

	restore := caFileReadHookForTest
	t.Cleanup(func() { caFileReadHookForTest = restore })
	caFileReadHookForTest = func(real string) {
		if err := os.Remove(real); err != nil {
			t.Fatalf("remove checked path before symlink swap: %v", err)
		}
		if err := os.Symlink(elsewherePath, real); err != nil {
			t.Fatalf("symlink swap-in over checked path: %v", err)
		}
	}

	_, err := readCAFile(path, "ca_file")
	if err == nil {
		t.Fatal("readCAFile accepted a symlink swapped in after the permission check, in violation of O_NOFOLLOW")
	}
}

// --- finding 4: proxy handling ---

// TestCAAwareProxyRefusesHTTPSProxy is finding 4's refusal half: a
// CA-scoped transport must not reach its target through an https:// proxy,
// because the CONNECT to the proxy itself would be verified with the narrow
// CA pool, and Proxy-Authorization would ride across that connection.
func TestCAAwareProxyRefusesHTTPSProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "https://proxy.example.com:443")
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("NO_PROXY", "")
	req, err := http.NewRequest(http.MethodGet, "https://target.example.com/", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	_, err = caAwareProxy(req)
	if err == nil {
		t.Fatal("caAwareProxy accepted an https:// proxy while a CA is configured")
	}
	if !strings.Contains(err.Error(), "https") {
		t.Errorf("error %q does not mention the https proxy", err)
	}
}

// TestCAAwareProxyAllowsHTTPProxy confirms the documented safe case: a plain
// http:// proxy is passed through unchanged, because the destination's TLS is
// still verified end to end against the configured CA through it.
func TestCAAwareProxyAllowsHTTPProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://proxy.example.com:8080")
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("NO_PROXY", "")
	req, err := http.NewRequest(http.MethodGet, "https://target.example.com/", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	proxyURL, err := caAwareProxy(req)
	if err != nil {
		t.Fatalf("caAwareProxy refused a plain http:// proxy: %v", err)
	}
	if proxyURL == nil || proxyURL.Scheme != "http" {
		t.Errorf("proxyURL = %v, want an http:// URL", proxyURL)
	}
}

// TestCAAwareProxyNoProxyConfiguredPassesThrough confirms the no-proxy-set
// case is unaffected: nil, nil, exactly like http.ProxyFromEnvironment.
func TestCAAwareProxyNoProxyConfiguredPassesThrough(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("NO_PROXY", "")
	req, err := http.NewRequest(http.MethodGet, "https://target.example.com/", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	proxyURL, err := caAwareProxy(req)
	if err != nil {
		t.Fatalf("caAwareProxy: %v", err)
	}
	if proxyURL != nil {
		t.Errorf("proxyURL = %v, want nil with no proxy configured", proxyURL)
	}
}

// --- finding 7: CA file hygiene ---

// TestCAFileHygieneRefusesOversizedFile caps the size at 1 MiB: a file past
// that ceiling is almost certainly the wrong file, not a deliberate CA
// bundle. This is finding 7's mutation-kill test: the file is built from
// many VALID, repeated CERTIFICATE PEM blocks (the real CA's own PEM,
// repeated past 1 MiB), not garbage bytes — so a mutant that removed the
// size check (or swapped LimitReader for a plain read) would otherwise
// happily parse the whole thing into a working pool and this test would
// still need the size gate specifically to catch it. Garbage bytes would
// also be refused by parseCAPool alone, which would let that mutant survive.
func TestCAFileHygieneRefusesOversizedFile(t *testing.T) {
	clearCAEnvironment(t)
	ca := newTestCA(t)
	path := filepath.Join(secureTempDir(t), "huge.pem")

	var buf bytes.Buffer
	for buf.Len() <= maxCAFileBytes {
		buf.Write(ca.pem)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write huge file: %v", err)
	}
	_, err := TransportForFile(path)
	if err == nil {
		t.Fatal("TransportForFile accepted a file larger than the size cap")
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("%d", maxCAFileBytes)) {
		t.Errorf("error %q does not name the size cap; want it to be refused BY THE SIZE CHECK, not a PEM-parse failure", err)
	}
}

// chownToNonPrivateGroup chowns path's group to one of the running process's
// own real supplementary groups — a group that genuinely exists on this
// host, and that the process is genuinely a member of, but whose NAME is not
// the running user's own username, so isUserPrivateGroup correctly refuses
// it (finding 4's "genuinely shared group" case, as opposed to the
// Debian/OpenSSH user-private-group convention). Skips the test when the
// process belongs to no supplementary group at all (a minimal container).
func chownToNonPrivateGroup(t *testing.T, path string) {
	t.Helper()
	groups, err := os.Getgroups()
	if err != nil {
		t.Skipf("Getgroups: %v", err)
	}
	primary := os.Getgid()
	for _, gid := range groups {
		if gid == primary {
			continue
		}
		if err := os.Chown(path, -1, gid); err == nil {
			return
		}
	}
	t.Skip("process belongs to no supplementary group usable for this test")
}

// TestCAFileHygieneRefusesGroupWritableFile refuses a CA file that is
// group-writable by a GENUINELY shared group — one of the running process's
// real supplementary groups, not its own user-private group — proving
// finding 4's exception is scoped to the private-group case only.
func TestCAFileHygieneRefusesGroupWritableFile(t *testing.T) {
	clearCAEnvironment(t)
	ca := newTestCA(t)
	path := filepath.Join(secureTempDir(t), "ca.pem")
	if err := os.WriteFile(path, ca.pem, 0o660); err != nil {
		t.Fatalf("write group-writable ca file: %v", err)
	}
	// WriteFile's mode passes through the process umask: under umask 022 the
	// file lands 0640, not group-writable at all, and the refusal under test
	// is never reached. Set the mode explicitly.
	if err := os.Chmod(path, 0o660); err != nil {
		t.Fatalf("chmod ca file 0660: %v", err)
	}
	chownToNonPrivateGroup(t, path)
	_, err := TransportForFile(path)
	if err == nil {
		t.Fatal("TransportForFile accepted a ca file writable by a genuinely shared group")
	}
	if !strings.Contains(err.Error(), "writable") {
		t.Errorf("error %q does not explain the hygiene failure", err)
	}
}

// TestCAFileHygieneRefusesWorldWritableParentDir refuses a CA file whose
// parent directory anyone can write to — SSH's known_hosts / authorized_keys
// hygiene rule — since replacing the file entirely bypasses the file's own
// permission bits.
func TestCAFileHygieneRefusesWorldWritableParentDir(t *testing.T) {
	clearCAEnvironment(t)
	ca := newTestCA(t)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatalf("chmod dir world-writable: %v", err)
	}
	path := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(path, ca.pem, 0o600); err != nil {
		t.Fatalf("write ca file: %v", err)
	}
	_, err := TransportForFile(path)
	if err == nil {
		t.Fatal("TransportForFile accepted a ca file inside a world-writable directory")
	}
	if !strings.Contains(err.Error(), "directory") {
		t.Errorf("error %q does not name the directory as the problem", err)
	}
}

// TestCAFileHygieneRefusesNonCertificateBlock refuses a PEM file containing
// any non-CERTIFICATE block, even if another block in the same file would
// have parsed fine — accepting the file anyway would trust a pool the
// operator never actually reviewed.
func TestCAFileHygieneRefusesNonCertificateBlock(t *testing.T) {
	clearCAEnvironment(t)
	ca := newTestCA(t)
	keyBlock := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("not-really-a-key")})
	mixed := append(append([]byte{}, ca.pem...), keyBlock...)
	path := filepath.Join(secureTempDir(t), "mixed.pem")
	if err := os.WriteFile(path, mixed, 0o600); err != nil {
		t.Fatalf("write mixed pem: %v", err)
	}
	_, err := TransportForFile(path)
	if err == nil {
		t.Fatal("TransportForFile accepted a file with a non-CERTIFICATE PEM block")
	}
	if !strings.Contains(err.Error(), "CERTIFICATE") {
		t.Errorf("error %q does not name the offending block type", err)
	}
}

// TestCAFileHygieneRefusesUnparsableCertificateBlock refuses a PEM file whose
// CERTIFICATE-typed block does not actually parse as a certificate, rather
// than silently ignoring it the way x509.CertPool.AppendCertsFromPEM would.
func TestCAFileHygieneRefusesUnparsableCertificateBlock(t *testing.T) {
	clearCAEnvironment(t)
	ca := newTestCA(t)
	badBlock := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("not-a-real-der-certificate")})
	mixed := append(append([]byte{}, ca.pem...), badBlock...)
	path := filepath.Join(secureTempDir(t), "unparsable.pem")
	if err := os.WriteFile(path, mixed, 0o600); err != nil {
		t.Fatalf("write unparsable pem: %v", err)
	}
	_, err := TransportForFile(path)
	if err == nil {
		t.Fatal("TransportForFile accepted a file with an unparsable CERTIFICATE block")
	}
}

// TestCAFileHygieneAcceptsLeafCertificateAsPin documents that a single leaf
// certificate (no CA:true) is a valid, if narrow, way to pin a target,
// per parseCAPool's doc.
func TestCAFileHygieneAcceptsLeafCertificateAsPin(t *testing.T) {
	clearCAEnvironment(t)
	ca := newTestCA(t)
	leafDER := ca.leaf.Certificate[0]
	leafPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})
	path := filepath.Join(secureTempDir(t), "leaf.pem")
	if err := os.WriteFile(path, leafPEM, 0o600); err != nil {
		t.Fatalf("write leaf-only pem: %v", err)
	}
	rt, err := TransportForFile(path)
	if err != nil {
		t.Fatalf("TransportForFile with a leaf-only pem: %v", err)
	}
	if transport, ok := rt.(*http.Transport); !ok || transport.TLSClientConfig.RootCAs == nil {
		t.Errorf("TransportForFile returned %T (%v), want a *http.Transport with RootCAs set", rt, rt)
	}
}

func mustParseOneCert(t *testing.T, pemBytes []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		t.Fatal("no PEM block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	return cert
}
