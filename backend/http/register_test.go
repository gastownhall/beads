package bdhttp_test

// Test helpers in this file are adapted from bd-enterprise
// backend/http/register_test.go under the MIT contribution: the registry
// double, the recording server/provider, and hermeticEnv's reasoning for
// taking the process environment out of credential decisions carry over
// directly. The Credentials-hook tests do not: OSS threads a per-open
// credential through backends.OpenOptions.Credential / httpclient.OpenWith
// instead of a bdhttp.Options.Credentials hook (no such hook exists on this
// path; see design D-OpenWith), so those tests are written fresh for this
// seam.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/steveyegge/beads/backend"
	bdhttp "github.com/steveyegge/beads/backend/http"
	"github.com/steveyegge/beads/internal/storage/backends"
)

// The registry key is spelled literally, the way a workspace's metadata.json
// carries it: a test that read it from the code under test would stay green
// through a rename that orphans every existing connected workspace.
const backendName = "http"

// projectID is the workspace identity the stand-in server owns. Every target
// below pins it, so the handshake's identity gate is armed rather than
// skipped.
const projectID = "proj-embedder"

// TestRegisterPutsHTTPInTheRegistryAnEmbedderSees drives the reason this
// package exists: code holding nothing but the public surface can get "http"
// into the registry that store dispatch reads.
func TestRegisterPutsHTTPInTheRegistryAnEmbedderSees(t *testing.T) {
	hermeticEnv(t)
	if backend.Registered(backendName) {
		t.Fatalf("%q was already registered before Register was called: linking this package must not register", backendName)
	}

	registerForTest(t, bdhttp.Options{})

	registered, ok := backend.Lookup(backendName)
	if !ok {
		t.Fatalf("Lookup(%q) found nothing after Register", backendName)
	}
	if registered.Open == nil {
		t.Error("registered http backend has no read-write Open")
	}
	if registered.OpenReadOnly == nil {
		t.Error("registered http backend has no read-only Open")
	}
	if !registered.WorkspaceIsBeadsDir {
		t.Error("an http workspace is the .beads directory alone; there is no local database to discover")
	}
	if !registered.Remote {
		t.Error("the http backend is a pure network client with no local database; Remote must be set")
	}
	if registered.OpenWith == nil {
		t.Error("registered http backend has no per-open OpenWith seam")
	}
	if !backend.WorkspaceIsBeadsDir(backendName) {
		t.Error("backend.WorkspaceIsBeadsDir disagrees with the registration")
	}
	if !backend.IsRemote(backendName) {
		t.Error("backend.IsRemote disagrees with the registration")
	}
}

// TestRegisterTwicePanics pins the decision not to make this idempotent. A
// second call is a double-wired process start, and the registry's panic is
// the only place that ever says so.
func TestRegisterTwicePanics(t *testing.T) {
	hermeticEnv(t)
	registerForTest(t, bdhttp.Options{})

	if !panics(func() { bdhttp.Register(bdhttp.Options{}) }) {
		t.Fatal("a second Register returned instead of panicking: duplicate process-start wiring must not be absorbed")
	}
	if !backend.Registered(backendName) {
		t.Fatalf("the refused duplicate took %q out of the registry", backendName)
	}
}

// TestRegisterInstallsTheTransport is the whole reason Register takes
// Options: registration alone leaves the store's package-level dialer nil,
// and every open then fails with httpclient.ErrNoTransport — a two-step an
// embedder cannot complete, because the second step is an internal function.
func TestRegisterInstallsTheTransport(t *testing.T) {
	hermeticEnv(t)
	server := newRecordingServer(t)
	registerForTest(t, bdhttp.Options{HTTPClient: server.Client()})

	store := openWorkspace(t, connectedWorkspace(t, server))
	got, err := store.GetMetadata(context.Background(), "_project_id")
	if err != nil {
		t.Fatalf("GetMetadata over the registered backend: %v", err)
	}
	if got != projectID {
		t.Errorf("project id = %q, want %q: the store never reached the server", got, projectID)
	}
}

// TestOpenWithHonorsAProvidedCredential pins the per-tenant seam Register
// installs: a caller that supplies backends.OpenOptions.Credential (wrapped
// in bdhttp.ProvidedCredential) reaches the server with THAT credential, not
// the ambient ladder, even though the registered backend was built with no
// credential of its own.
func TestOpenWithHonorsAProvidedCredential(t *testing.T) {
	hermeticEnv(t)
	server := newRecordingServer(t)
	registerForTest(t, bdhttp.Options{HTTPClient: server.Client()})
	beadsDir := connectedWorkspace(t, server)

	registered, ok := backend.Lookup(backendName)
	if !ok {
		t.Fatal("the http backend is not registered")
	}
	provider := &recordingProvider{token: "per-tenant-token"}
	store, err := registered.OpenWith(context.Background(), beadsDir, backends.OpenOptions{
		Credential: bdhttp.ProvidedCredential{Provider: provider},
	})
	if err != nil {
		t.Fatalf("OpenWith: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if _, err := store.GetMetadata(context.Background(), "_project_id"); err != nil {
		t.Fatalf("GetMetadata: %v", err)
	}
	server.wantAuthorization(t, "Bearer per-tenant-token")
}

// TestOpenWithFallsBackToTheAmbientLadderByDefault pins the single-tenant
// default: a registered open with no Credential still authorizes, through
// the same built-in bearer ladder Open/OpenReadOnly use — RequireCredential
// is what opts OUT of this, not the zero value.
func TestOpenWithFallsBackToTheAmbientLadderByDefault(t *testing.T) {
	hermeticEnv(t)
	t.Setenv(bdhttp.TokenEnv, "ambient-token")
	server := newRecordingServer(t)
	registerForTest(t, bdhttp.Options{HTTPClient: server.Client()})
	beadsDir := connectedWorkspace(t, server)

	store := openWorkspace(t, beadsDir)
	if _, err := store.GetMetadata(context.Background(), "_project_id"); err != nil {
		t.Fatalf("GetMetadata: %v", err)
	}
	server.wantAuthorization(t, "Bearer ambient-token")
}

// TestRequireCredentialRefusesAnOpenWithNone is the fail-closed half: a
// multi-tenant embedder that sets RequireCredential must never have a
// workspace silently fall back to whatever the ambient process environment
// happens to hold, because that environment belongs to no one tenant.
func TestRequireCredentialRefusesAnOpenWithNone(t *testing.T) {
	hermeticEnv(t)
	server := newRecordingServer(t)
	registerForTest(t, bdhttp.Options{HTTPClient: server.Client(), RequireCredential: true})
	beadsDir := connectedWorkspace(t, server)

	registered, ok := backend.Lookup(backendName)
	if !ok {
		t.Fatal("the http backend is not registered")
	}
	_, err := registered.OpenWith(context.Background(), beadsDir, backends.OpenOptions{})
	if err == nil {
		t.Fatal("OpenWith with RequireCredential and no Credential succeeded; want a refusal")
	}
	server.wantNoRequests(t)
}

func panics(fn func()) (panicked bool) {
	defer func() { panicked = recover() != nil }()
	fn()
	return false
}

// registerForTest wires the backend for one test and gives the process-global
// registry back afterwards, the isolation seam backend.Deregister exists for.
func registerForTest(t *testing.T, opts bdhttp.Options) {
	t.Helper()
	bdhttp.Register(opts)
	t.Cleanup(func() { backend.Deregister(backendName) })
}

// connectedWorkspace is a .beads directory attached to server, as `bd
// connect` would leave it.
func connectedWorkspace(t *testing.T, server *recordingServer) string {
	t.Helper()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := bdhttp.SaveTarget(beadsDir, server.target(t)); err != nil {
		t.Fatalf("save the activation sidecar: %v", err)
	}
	return beadsDir
}

// openWorkspace opens beadsDir through the REGISTERED backend rather than
// through this package's own Open, so what it exercises is the dialer
// Register installed.
func openWorkspace(t *testing.T, beadsDir string) backend.DoltStorage {
	t.Helper()
	registered, ok := backend.Lookup(backendName)
	if !ok {
		t.Fatal("the http backend is not registered")
	}
	store, err := registered.Open(context.Background(), beadsDir)
	if err != nil {
		t.Fatalf("open the connected workspace: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// hermeticEnv takes the process environment out of the credential decision.
// Every rung of the built-in ladder is read from the environment at dial
// time, so a developer machine that exports one would otherwise change which
// source a test exercises.
func hermeticEnv(t *testing.T) {
	t.Helper()
	t.Setenv(bdhttp.TokenEnv, "")
	t.Setenv(bdhttp.TokenCommandEnv, "")
}

// recordingProvider is a CredentialProvider that signs with a fixed token.
type recordingProvider struct {
	token string

	mu    sync.Mutex
	calls int
}

func (p *recordingProvider) Authorize(_ context.Context, req *http.Request) error {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	req.Header.Set("Authorization", "Bearer "+p.token)
	return nil
}

func (p *recordingProvider) Refresh(context.Context) (bool, error) { return false, nil }

// recordingServer is a bd serve stand-in that answers the v0 handshake and
// records what each request carried in its Authorization header.
type recordingServer struct {
	*httptest.Server

	mu    sync.Mutex
	auths []string
}

func newRecordingServer(t *testing.T) *recordingServer {
	t.Helper()
	s := &recordingServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.auths = append(s.auths, r.Header.Get("Authorization"))
		s.mu.Unlock()
		if r.URL.Path != "/v0/beads/context" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"api_version":"v0","backend":"dolt","bd_version":"9.9.9",` +
			`"beads_dir":"/srv/.beads","capabilities":["issues.get","issues.list"],` +
			`"database":"beads","dolt_mode":"server","project_id":"` + projectID + `","repo_root":"/srv",` +
			`"schema_version":1,"wire_revision":1,"min_client_wire_revision":1}`))
	}))
	t.Cleanup(s.Server.Close)
	return s
}

func (s *recordingServer) target(t *testing.T) bdhttp.Target {
	t.Helper()
	base, err := url.Parse(s.URL)
	if err != nil {
		t.Fatalf("parse the test server URL: %v", err)
	}
	return bdhttp.Target{BaseURL: base, ExpectProjectID: projectID}
}

// wantAuthorization asserts that every request the server saw carried want.
// It is every request rather than any request because a credential that
// reaches only some of them is the same defect as one that reaches none.
func (s *recordingServer) wantAuthorization(t *testing.T, want string) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.auths) == 0 {
		t.Fatal("the server saw no requests at all")
	}
	for i, got := range s.auths {
		if got != want {
			t.Errorf("request %d carried Authorization %q, want %q", i, got, want)
		}
	}
}

// wantNoRequests is the fail-closed half: a refusal that still dialed would
// have sent whatever credential it fell back to, which is the outcome being
// refused.
func (s *recordingServer) wantNoRequests(t *testing.T) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.auths) != 0 {
		t.Errorf("the server saw %d requests, want none: the dial was supposed to fail closed", len(s.auths))
	}
}
