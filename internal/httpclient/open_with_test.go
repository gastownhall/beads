// Written fresh for OSS beads S6 (no bd-enterprise source copied).
package httpclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/storage/backends"
)

// literalSecretProvider is a CredentialProvider whose unexported field holds
// the literal secret value, the same shape a real token-bearing provider
// takes. It exists only so a test can grep for that literal in an error
// string: fmt's %+v prints unexported struct fields by value, so if OpenWith
// (or anything it calls) ever formats opts.Credential directly into an error
// — rather than formatting only the target and the wrapped dial error, as
// open_with.go's "dialing %s: %w" does today — this field's value leaks into
// logs and terminals, exactly what wire.CredentialProvider's own doc comment
// on Authorize forbids ("The error must not carry the credential").
type literalSecretProvider struct {
	secret string
}

func (literalSecretProvider) Authorize(context.Context, *http.Request) error { return nil }
func (literalSecretProvider) Refresh(context.Context) (bool, error)          { return false, nil }

// TestOpenWithDialErrorNeverFormatsTheCredential kills the mutation that
// widens open_with.go's dial-failure wrap from
// `fmt.Errorf("dialing %s: %w", target, err)` to one that also interpolates
// opts.Credential (e.g. "dialing %s cred=%+v: %w"). A credential a caller
// supplies through OpenOptions.Credential must never surface in an error this
// package hands back — the error travels into the embedder's own logs and
// terminals, same as the doc comment on CredentialProvider.Authorize says.
//
// The dial target names a CA file that does not exist, which DialWith (via
// dialTransport) refuses before opening any socket: a config-time error, not
// a network one, so the test stays fast and never reaches any real network
// — the standing safety rule for this suite — while still giving OpenWith a
// real, non-nil error from the exact call it wraps.
func TestOpenWithDialErrorNeverFormatsTheCredential(t *testing.T) {
	beadsDir := t.TempDir()
	base, err := url.Parse("http://127.0.0.1:1/")
	if err != nil {
		t.Fatalf("parse base url: %v", err)
	}
	target := Target{BaseURL: base, CAFile: filepath.Join(beadsDir, "no-such-ca.pem")}
	if err := SaveTarget(beadsDir, target); err != nil {
		t.Fatalf("SaveTarget: %v", err)
	}

	const secret = "KILL-ME-SECRET-1234"
	opts := backends.OpenOptions{
		Credential: ProvidedCredential{Provider: literalSecretProvider{secret: secret}},
	}

	_, err = OpenWith(context.Background(), beadsDir, opts, DialOptions{}, false)
	if err == nil {
		t.Fatal("OpenWith against an unreachable loopback port returned no error; cannot assert what its error does, or does not, carry")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("OpenWith's error carries the literal credential secret %q: %v", secret, err)
	}
}

// fixedTokenProvider signs every request with one token. It caches nothing
// beyond its own field, so what a server sees is exactly what the store that
// holds it was handed.
type fixedTokenProvider struct{ token string }

func (p fixedTokenProvider) Authorize(_ context.Context, req *http.Request) error {
	req.Header.Set("Authorization", "Bearer "+p.token)
	return nil
}
func (fixedTokenProvider) Refresh(context.Context) (bool, error) { return false, nil }

// contextOnlyServer answers the v0 handshake for one project and records each
// request's Authorization header.
func contextOnlyServer(t *testing.T, project string) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var auths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auths = append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		if r.URL.Path != "/v0/beads/context" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"api_version":"v0","backend":"dolt","bd_version":"9.9.9",` +
			`"beads_dir":"/srv/.beads","capabilities":["issues.get"],` +
			`"database":"beads","dolt_mode":"server","project_id":"` + project + `","repo_root":"/srv",` +
			`"schema_version":1,"wire_revision":1,"min_client_wire_revision":1}`))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), auths...)
	}
}

// spyGetenv swaps the package's one environment reader for a recorder for the
// life of t. It must not be used from a parallel test: getenv is a package
// variable, and top-level tests that call t.Parallel only resume after every
// sequential test (this one included) has finished and restored it.
func spyGetenv(t *testing.T) func() []string {
	t.Helper()
	var mu sync.Mutex
	seen := map[string]bool{}
	prev := getenv
	getenv = func(name string) string {
		mu.Lock()
		seen[name] = true
		mu.Unlock()
		// Answer "1" for every name: were any of them consulted, the
		// plaintext opt-in would read as granted and a malformed token or
		// CA pattern would fail the dial, so a leak shows as behaviour as
		// well as in the record.
		return "1"
	}
	t.Cleanup(func() { getenv = prev })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := make([]string, 0, len(seen))
		for name := range seen {
			out = append(out, name)
		}
		sort.Strings(out)
		return out
	}
}

// TestExplicitCredentialReadsNoEnvironment pins the explicit-credential door's
// promise: a dial, a handshake and a registry OpenWith handed their own
// credential consult no process environment at all — no token, no token
// command, no CA pattern, no plaintext opt-in. The control dial through the
// ambient ladder proves the spy actually sees reads.
func TestExplicitCredentialReadsNoEnvironment(t *testing.T) {
	srv, auths := contextOnlyServer(t, "proj-env")
	base, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	target := Target{BaseURL: base, ExpectProjectID: "proj-env"}
	creds := fixedTokenProvider{token: "explicit"}
	ctx := context.Background()

	seen := spyGetenv(t)

	conn, err := DialWithCredential(target, creds, DialOptions{})
	if err != nil {
		t.Fatalf("DialWithCredential: %v", err)
	}
	if _, err := conn.ServerContext(ctx); err != nil {
		t.Fatalf("ServerContext: %v", err)
	}
	if _, err := HandshakeWithCredential(ctx, target, creds, DialOptions{}); err != nil {
		t.Fatalf("HandshakeWithCredential: %v", err)
	}

	beadsDir := t.TempDir()
	if err := SaveTarget(beadsDir, target); err != nil {
		t.Fatalf("SaveTarget: %v", err)
	}
	store, err := OpenWith(ctx, beadsDir, backends.OpenOptions{Credential: ProvidedCredential{Provider: creds}}, DialOptions{}, false)
	if err != nil {
		t.Fatalf("OpenWith: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.GetMetadata(ctx, "_project_id"); err != nil {
		t.Fatalf("GetMetadata: %v", err)
	}

	// A plain-http, non-loopback target is where the guard would consult
	// BEADS_HTTP_ALLOW_INSECURE; on this door it must not. Dialing builds the
	// guard without opening a socket.
	remote := Target{BaseURL: mustParseURL(t, "http://"+nonLoopbackTestHost+"/")}
	if _, err := DialWithCredential(remote, creds, DialOptions{}); err != nil {
		t.Fatalf("DialWithCredential(non-loopback): %v", err)
	}

	if got := seen(); len(got) != 0 {
		t.Errorf("the explicit-credential door read the environment: %v", got)
	}
	for i, got := range auths() {
		if got != "Bearer explicit" {
			t.Errorf("request %d carried Authorization %q, want the explicit credential", i, got)
		}
	}

	// Control: the ambient door does read the environment, so an empty
	// record above is evidence rather than a spy that never fires.
	// Its outcome is irrelevant (the spy's "1" is not a valid CA pattern).
	_, _ = Dial(remote, DialOptions{})
	if got := seen(); len(got) == 0 {
		t.Fatal("the spy saw no reads from the ambient Dial either; it is not wired to the package's env reads")
	}
}

// TestDialWithCredentialRefusesNil keeps "explicit" from meaning "nothing":
// a nil provider on this door is ErrCredentialRequired, not an
// unauthenticated dial and not a fall-back to the ambient ladder.
func TestDialWithCredentialRefusesNil(t *testing.T) {
	target := Target{BaseURL: mustParseURL(t, "http://127.0.0.1:1/")}
	if _, err := DialWithCredential(target, nil, DialOptions{}); !errors.Is(err, ErrCredentialRequired) {
		t.Fatalf("DialWithCredential(nil) = %v, want ErrCredentialRequired", err)
	}
}

// TestGuardInsecureCredentialIgnoresEnvWhenNotConsulted is the guard half of
// the explicit door: with consultEnv false, BEADS_HTTP_ALLOW_INSECURE=1 grants
// nothing and the refusal wrapper stays on.
func TestGuardInsecureCredentialIgnoresEnvWhenNotConsulted(t *testing.T) {
	t.Setenv(AllowInsecureCredentialEnv, "1")
	target := Target{BaseURL: mustParseURL(t, "http://"+nonLoopbackTestHost+"/")}
	guarded := guardInsecureCredential(target, stubProvider{tag: "explicit"}, false, false)
	if _, ok := guarded.(*insecureCredentialGuard); !ok {
		t.Fatalf("guardInsecureCredential(consultEnv=false) returned %T, want the refusal wrapper despite %s=1", guarded, AllowInsecureCredentialEnv)
	}
}
