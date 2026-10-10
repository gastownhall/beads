package bdhttp_test

// Written fresh for OSS beads S6f (no bd-enterprise source copied): one
// process opening several projects, on the same server, each with its own
// credential (gc's per-city / per-rig credentials).

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/backend"
	bdhttp "github.com/steveyegge/beads/backend/http"
	"github.com/steveyegge/beads/internal/storage/backends"
)

// twoProjectServer is ONE bd serve stand-in (one host:port) mounting two
// projects under their own path roots, recording the Authorization header
// each project saw. The host:port ladder cannot tell the two apart; only a
// per-open credential can.
type twoProjectServer struct {
	*httptest.Server

	mu    sync.Mutex
	auths map[string][]string // mount -> Authorization headers seen
}

func newTwoProjectServer(t *testing.T, mounts ...string) *twoProjectServer {
	t.Helper()
	s := &twoProjectServer{auths: map[string][]string{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, mount := range mounts {
			if r.URL.Path != "/"+mount+"/v0/beads/context" {
				continue
			}
			s.mu.Lock()
			s.auths[mount] = append(s.auths[mount], r.Header.Get("Authorization"))
			s.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"api_version":"v0","backend":"dolt","bd_version":"9.9.9",` +
				`"beads_dir":"/srv/.beads","capabilities":["issues.get","issues.list"],` +
				`"database":"beads","dolt_mode":"server","project_id":"proj-` + mount + `","repo_root":"/srv",` +
				`"schema_version":1,"wire_revision":1,"min_client_wire_revision":1}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(s.Server.Close)
	return s
}

func (s *twoProjectServer) target(t *testing.T, mount string) bdhttp.Target {
	t.Helper()
	base, err := url.Parse(s.URL + "/" + mount)
	if err != nil {
		t.Fatalf("parse the test server URL: %v", err)
	}
	return bdhttp.Target{BaseURL: base, ExpectProjectID: "proj-" + mount}
}

func (s *twoProjectServer) wantAuthorization(t *testing.T, mount, want string) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	got := s.auths[mount]
	if len(got) == 0 {
		t.Fatalf("project %q saw no requests at all", mount)
	}
	for i, auth := range got {
		if auth != want {
			t.Errorf("project %q request %d carried Authorization %q, want %q", mount, i, auth, want)
		}
	}
}

// poisonAmbient arms every ambient credential source for 127.0.0.1 with a
// token that must never reach the wire: the env token, a token command, and a
// credentials file section for the server's exact host:port.
func poisonAmbient(t *testing.T, server *httptest.Server) {
	t.Helper()
	base, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse the test server URL: %v", err)
	}
	t.Setenv(bdhttp.TokenEnv, "127.0.0.1=ambient-env-token")
	t.Setenv(bdhttp.TokenCommandEnv, "127.0.0.1=echo ambient-command-token")
	credFile := filepath.Join(t.TempDir(), "credentials")
	body := "[" + base.Host + "]\npassword = ambient-file-token\n"
	if err := os.WriteFile(credFile, []byte(body), 0o600); err != nil {
		t.Fatalf("write credentials file: %v", err)
	}
	t.Setenv("BEADS_CREDENTIALS_FILE", credFile)
}

// TestTwoStoresOnOneHostEachSendTheirOwnToken is the G7 shape: one process,
// one server (one host:port), two projects, two credentials. Each store must
// send its own token on every request, and neither may pick up the other's
// or the ambient one — through the direct door and the registry's OpenWith
// seam alike.
func TestTwoStoresOnOneHostEachSendTheirOwnToken(t *testing.T) {
	server := newTwoProjectServer(t, "alpha", "beta", "gamma", "delta")
	poisonAmbient(t, server.Server)
	ctx := context.Background()

	// The direct door: bdhttp.Open with Options.Credential.
	alpha, err := bdhttp.Open(ctx, server.target(t, "alpha"), bdhttp.Options{
		HTTPClient: server.Client(), Credential: &recordingProvider{token: "alpha-token"},
	})
	if err != nil {
		t.Fatalf("Open alpha: %v", err)
	}
	t.Cleanup(func() { _ = alpha.Close() })
	beta, err := bdhttp.Open(ctx, server.target(t, "beta"), bdhttp.Options{
		HTTPClient: server.Client(), Credential: &recordingProvider{token: "beta-token"},
	})
	if err != nil {
		t.Fatalf("Open beta: %v", err)
	}
	t.Cleanup(func() { _ = beta.Close() })

	// The registry seam: OpenWith with a ProvidedCredential per workspace.
	registerForTest(t, bdhttp.Options{HTTPClient: server.Client(), RequireCredential: true})
	registered, ok := backend.Lookup(backendName)
	if !ok {
		t.Fatal("the http backend is not registered")
	}
	openRegistered := func(mount, token string) backend.DoltStorage {
		t.Helper()
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		if err := os.MkdirAll(beadsDir, 0o700); err != nil {
			t.Fatalf("create workspace: %v", err)
		}
		if err := bdhttp.SaveTarget(beadsDir, server.target(t, mount)); err != nil {
			t.Fatalf("save the activation sidecar: %v", err)
		}
		store, err := registered.OpenWith(ctx, beadsDir, backends.OpenOptions{
			Credential: bdhttp.ProvidedCredential{Provider: &recordingProvider{token: token}},
		})
		if err != nil {
			t.Fatalf("OpenWith %s: %v", mount, err)
		}
		t.Cleanup(func() { _ = store.Close() })
		return store
	}
	gamma := openRegistered("gamma", "gamma-token")
	delta := openRegistered("delta", "delta-token")

	// Interleave the reads so a credential cached per host by whichever open
	// ran first would show up on the others.
	for round := 0; round < 2; round++ {
		for name, store := range map[string]backend.DoltStorage{"alpha": alpha, "beta": beta, "gamma": gamma, "delta": delta} {
			got, err := store.GetMetadata(ctx, "_project_id")
			if err != nil {
				t.Fatalf("GetMetadata %s: %v", name, err)
			}
			if got != "proj-"+name {
				t.Errorf("store %s reached project %q", name, got)
			}
		}
	}
	for _, mount := range []string{"alpha", "beta", "gamma", "delta"} {
		server.wantAuthorization(t, mount, "Bearer "+mount+"-token")
	}
}

// TestExplicitCredentialOverridesTheAmbientToken: with BEADS_HTTP_TOKEN,
// BEADS_HTTP_TOKEN_COMMAND and the credentials file all naming this server,
// an explicit Options.Credential is still the only thing sent. The control
// open without one proves the poison is live (the ambient ladder does pick
// it up), so the explicit result is not a vacuous pass.
func TestExplicitCredentialOverridesTheAmbientToken(t *testing.T) {
	server := newTwoProjectServer(t, "alpha", "beta")
	poisonAmbient(t, server.Server)
	ctx := context.Background()

	store, err := bdhttp.Open(ctx, server.target(t, "alpha"), bdhttp.Options{
		HTTPClient: server.Client(), Credential: &recordingProvider{token: "explicit-token"},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.GetMetadata(ctx, "_project_id"); err != nil {
		t.Fatalf("GetMetadata: %v", err)
	}
	server.wantAuthorization(t, "alpha", "Bearer explicit-token")

	control, err := bdhttp.Open(ctx, server.target(t, "beta"), bdhttp.Options{HTTPClient: server.Client()})
	if err != nil {
		t.Fatalf("Open (ambient control): %v", err)
	}
	t.Cleanup(func() { _ = control.Close() })
	if _, err := control.GetMetadata(ctx, "_project_id"); err != nil {
		t.Fatalf("GetMetadata (ambient control): %v", err)
	}
	server.wantAuthorization(t, "beta", "Bearer ambient-env-token")
}

// TestExplicitCredentialSurvivesBrokenAmbientEnv is the behavioural proof
// that the explicit door reads no environment: every ambient variable it
// could consult is set to a value the ambient path refuses outright (an
// unscoped token, an unscoped CA pattern), and the explicit open and
// handshake still succeed. The control shows the same env does break an
// ambient handshake.
func TestExplicitCredentialSurvivesBrokenAmbientEnv(t *testing.T) {
	server := newTwoProjectServer(t, "alpha")
	t.Setenv(bdhttp.TokenEnv, "unscoped-token-is-refused")
	t.Setenv(bdhttp.TokenCommandEnv, "unscoped-command-is-refused")
	t.Setenv("BEADS_HTTP_CA_FILE", "/no/such/ca.pem")
	t.Setenv("BEADS_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "absent"))
	ctx := context.Background()
	opts := bdhttp.Options{HTTPClient: server.Client(), Credential: &recordingProvider{token: "explicit-token"}}

	if _, err := bdhttp.Handshake(ctx, server.target(t, "alpha"), opts); err != nil {
		t.Fatalf("Handshake with a Credential consulted the broken ambient env: %v", err)
	}
	store, err := bdhttp.Open(ctx, server.target(t, "alpha"), opts)
	if err != nil {
		t.Fatalf("Open with a Credential: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.GetMetadata(ctx, "_project_id"); err != nil {
		t.Fatalf("GetMetadata with a Credential consulted the broken ambient env: %v", err)
	}
	server.wantAuthorization(t, "alpha", "Bearer explicit-token")

	if _, err := bdhttp.Handshake(ctx, server.target(t, "alpha"), bdhttp.Options{HTTPClient: server.Client()}); err == nil {
		t.Fatal("the ambient handshake succeeded under a malformed unscoped token; the control no longer proves anything")
	}
}

// TestHandshakeWithACredential: the pre-connect probe authorizes with the
// credential it is handed, not the ambient ladder, so it verifies exactly
// what the tenant's store will send.
func TestHandshakeWithACredential(t *testing.T) {
	server := newTwoProjectServer(t, "alpha")
	poisonAmbient(t, server.Server)
	provider := &recordingProvider{token: "probe-token"}

	snapshot, err := bdhttp.Handshake(context.Background(), server.target(t, "alpha"), bdhttp.Options{
		HTTPClient: server.Client(), Credential: provider,
	})
	if err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	if snapshot.ProjectID != "proj-alpha" {
		t.Errorf("snapshot project = %q, want proj-alpha", snapshot.ProjectID)
	}
	server.wantAuthorization(t, "alpha", "Bearer probe-token")
	provider.mu.Lock()
	calls := provider.calls
	provider.mu.Unlock()
	if calls == 0 {
		t.Error("the handshake never asked the supplied provider to authorize")
	}
}

// plaintextRemote stands in for a bd serve reached over plain http at a
// non-loopback address (the TLS-terminating proxy a plaintext grant exists
// for) without opening a socket. It answers the handshake, and not_found for
// anything else, and records the Authorization header of every request that
// actually reached it.
type plaintextRemote struct {
	mu    sync.Mutex
	auths []string
}

func (r *plaintextRemote) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		_ = req.Body.Close()
	}
	r.mu.Lock()
	r.auths = append(r.auths, req.Header.Get("Authorization"))
	r.mu.Unlock()
	status, contentType, body := http.StatusNotFound, "application/problem+json", `{"status":404,"code":"not_found"}`
	if strings.HasSuffix(req.URL.Path, "/v0/beads/context") {
		status, contentType = http.StatusOK, "application/json"
		body = `{"api_version":"v0","backend":"dolt","bd_version":"9.9.9",` +
			`"beads_dir":"/srv/.beads","capabilities":["issues.get"],` +
			`"database":"beads","dolt_mode":"server","project_id":"proj-remote","repo_root":"/srv",` +
			`"schema_version":1,"wire_revision":1,"min_client_wire_revision":1}`
	}
	return &http.Response{
		StatusCode:    status,
		Header:        http.Header{"Content-Type": {contentType}},
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: -1,
		Request:       req,
	}, nil
}

func (r *plaintextRemote) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.auths...)
}

// TestExplicitCredentialIgnoresTheEnvPlaintextGrant pins, at each production
// door, that BEADS_HTTP_ALLOW_INSECURE=1 grants an explicit credential
// nothing. Against a plain-http, non-loopback target, the registry's OpenWith
// (ProvidedCredential), Open and Handshake (Options.Credential) each refuse
// before any request reaches the transport, and the refusal names the grant
// that does work there. Every other explicit-door test dials loopback, where
// the guard returns before it reads the variable, so a door reverted to the
// env-consulting DialWith passed them all.
//
// Each door's control differs from its refused leg only in
// Target.AllowInsecureCredential, and must send the explicit token exactly
// once. The ambient control shows the same environment does grant the
// built-in ladder plaintext, so the refusals are the door's doing and not a
// variable nobody reads.
func TestExplicitCredentialIgnoresTheEnvPlaintextGrant(t *testing.T) {
	hermeticEnv(t)
	t.Setenv("BEADS_HTTP_ALLOW_INSECURE", "1")
	registerForTest(t, bdhttp.Options{})
	registered, ok := backend.Lookup(backendName)
	if !ok {
		t.Fatal("the http backend is not registered")
	}
	ctx := context.Background()
	remote, err := url.Parse("http://198.51.100.1/") // RFC 5737 TEST-NET-2: never dialed.
	if err != nil {
		t.Fatalf("parse the remote URL: %v", err)
	}

	doors := []struct {
		name string
		// call drives one request through the door with a fresh explicit
		// credential and returns that request's error.
		call func(t *testing.T, target bdhttp.Target, client *http.Client) error
	}{
		{"OpenWith", func(t *testing.T, target bdhttp.Target, client *http.Client) error {
			beadsDir := filepath.Join(t.TempDir(), ".beads")
			if err := os.MkdirAll(beadsDir, 0o700); err != nil {
				t.Fatalf("create workspace: %v", err)
			}
			if err := bdhttp.SaveTarget(beadsDir, target); err != nil {
				t.Fatalf("save the activation sidecar: %v", err)
			}
			store, err := registered.OpenWith(ctx, beadsDir, backends.OpenOptions{
				Credential: bdhttp.ProvidedCredential{Provider: &recordingProvider{token: "explicit-token"}},
				HTTPClient: client,
			})
			if err != nil {
				t.Fatalf("OpenWith: %v", err)
			}
			t.Cleanup(func() { _ = store.Close() })
			_, err = store.GetIssue(ctx, "bd-1")
			return err
		}},
		{"Open", func(t *testing.T, target bdhttp.Target, client *http.Client) error {
			store, err := bdhttp.Open(ctx, target, bdhttp.Options{
				HTTPClient: client, Credential: &recordingProvider{token: "explicit-token"},
			})
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			t.Cleanup(func() { _ = store.Close() })
			_, err = store.GetIssue(ctx, "bd-1")
			return err
		}},
		{"Handshake", func(t *testing.T, target bdhttp.Target, client *http.Client) error {
			_, err := bdhttp.Handshake(ctx, target, bdhttp.Options{
				HTTPClient: client, Credential: &recordingProvider{token: "explicit-token"},
			})
			return err
		}},
	}
	for _, door := range doors {
		t.Run(door.name, func(t *testing.T) {
			refused := &plaintextRemote{}
			err := door.call(t, bdhttp.Target{BaseURL: remote}, &http.Client{Transport: refused})
			if err == nil || !strings.Contains(err.Error(), "refusing to send a credential") {
				t.Fatalf("got %v, want the plaintext refusal: BEADS_HTTP_ALLOW_INSECURE=1 must grant an explicit credential nothing", err)
			}
			if msg := err.Error(); strings.Contains(msg, "BEADS_HTTP_ALLOW_INSECURE") || !strings.Contains(msg, "Target.AllowInsecureCredential") {
				t.Errorf("refusal %q must name Target.AllowInsecureCredential, the grant this door honors, and not BEADS_HTTP_ALLOW_INSECURE, which it never reads", msg)
			}
			if got := refused.seen(); len(got) != 0 {
				t.Errorf("the transport saw %d request(s) %q; the refusal must stop the credential before the wire", len(got), got)
			}

			granted := &plaintextRemote{}
			if err := door.call(t, bdhttp.Target{BaseURL: remote, AllowInsecureCredential: true}, &http.Client{Transport: granted}); err != nil {
				t.Fatalf("with Target.AllowInsecureCredential: %v", err)
			}
			if got := granted.seen(); len(got) != 1 || got[0] != "Bearer explicit-token" {
				t.Errorf("with Target.AllowInsecureCredential the transport saw %q, want one request carrying the explicit token", got)
			}
		})
	}

	// Control: the same environment does grant the built-in ladder plaintext.
	t.Setenv(bdhttp.TokenEnv, "198.51.100.1=ambient-token")
	ambient := &plaintextRemote{}
	if _, err := bdhttp.Handshake(ctx, bdhttp.Target{BaseURL: remote}, bdhttp.Options{HTTPClient: &http.Client{Transport: ambient}}); err != nil {
		t.Fatalf("ambient Handshake under BEADS_HTTP_ALLOW_INSECURE=1: %v; the control no longer shows the variable is live", err)
	}
	if got := ambient.seen(); len(got) != 1 || got[0] != "Bearer ambient-token" {
		t.Errorf("the ambient Handshake sent %q, want one request carrying the ambient token", got)
	}
}

// TestRegisterRefusesAProcessWideCredential: the registered dialer serves
// every workspace in the process, so a credential on it would be one
// credential for every tenant — exactly what Options.Credential is for
// avoiding. Register panics before touching the registry.
func TestRegisterRefusesAProcessWideCredential(t *testing.T) {
	hermeticEnv(t)
	defer func() {
		if backend.Registered(backendName) {
			backend.Deregister(backendName)
			t.Error("the refused Register still put http in the registry")
		}
	}()
	var msg string
	func() {
		defer func() {
			if r := recover(); r != nil {
				msg, _ = r.(string)
			}
		}()
		bdhttp.Register(bdhttp.Options{Credential: &recordingProvider{token: "global"}})
	}()
	if !strings.Contains(msg, "per open") {
		t.Fatalf("Register with a Credential did not panic with the per-open refusal (got %q)", msg)
	}
}
