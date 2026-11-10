// Package embedder_test proves the one promise S6 makes to an out-of-tree
// embedder (gc, Gas City is the motivating case): linking nothing but PUBLIC
// doors — github.com/steveyegge/beads, .../backend, and .../backend/http — is
// enough to register the http backend and open a workspace through it with a
// per-call credential, via beads.OpenBestAvailableWith.
//
// Written fresh for OSS beads S6 (no bd-enterprise source copied). This
// package imports ONLY those three import paths (plus the standard library
// and net/http/httptest for the stand-in server): no internal/... package
// appears in this file's imports, which is itself the thing under test — an
// internal import here would compile today and break silently the day
// internal/httpclient's shape changes, defeating the whole point of a public
// door.
package embedder_test

import (
	"context"
	"encoding/json"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	beads "github.com/steveyegge/beads"
	"github.com/steveyegge/beads/backend"
	bdhttp "github.com/steveyegge/beads/backend/http"
	"github.com/steveyegge/beads/issueops"
)

// TestNoInternalImports is the mechanical, enforced form of this file's doc
// comment promise above: nothing in this package may import a
// github.com/steveyegge/beads/internal/... package, directly or otherwise.
// That import would compile fine today and only break silently the day some
// internal package's shape changes — exactly what a public-door embedder
// test exists to catch before it ships. It replaces an earlier, separate
// go.mod for this directory: a second module cannot see internal/... either,
// but it is heavier machinery for the same guarantee, and nothing stops a
// future edit from deleting a go.mod quietly, whereas this test fails loudly
// in the same `go test` run as everything else.
//
// It only inspects this package's own files' import declarations
// (go/parser, ImportsOnly — no type-checking, so it stays fast and adds no
// dependency beyond the standard library) and does not recurse into what
// those imports themselves pull in; that transitive check is exactly what
// go build/go vet already do on every change.
func TestNoInternalImports(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}
	const forbidden = "github.com/steveyegge/beads/internal"
	for _, pkg := range pkgs {
		for filename, file := range pkg.Files {
			for _, imp := range file.Imports {
				path, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					t.Fatalf("%s: unquoting import %s: %v", filename, imp.Path.Value, err)
				}
				if path == forbidden || strings.HasPrefix(path, forbidden+"/") {
					t.Errorf("%s imports %q: test/embedder must link only public doors, never an internal/... package", filename, path)
				}
			}
		}
	}
}

const embedderProjectID = "proj-gc-tenant"

// embedderReadyIssueID is the one issue the stand-in server's /v0/beads/ready
// page carries, so the test has something distinctive to assert on besides
// "the call did not error".
const embedderReadyIssueID = "gc-1"

// backendName is spelled literally, the way a workspace's metadata.json
// carries it, rather than read back from a re-exported constant: bdhttp
// deliberately exports no Backend name constant (see backend/http/http.go),
// so a rename of the registry key here would be caught by every CONNECTED
// workspace left unopenable, which is the fact this spells out explicitly
// instead of hiding behind a symbol that does not exist.
const backendName = "http"

// TestEmbedderOpensTheHTTPBackendThroughThePublicDoorsOnly is the linkage
// proof: Register from the public backend/http door, a workspace described
// only by the public Target/SaveTarget helpers, and
// beads.OpenBestAvailableWith from the public root package — no package under
// internal/... in the call chain this test writes.
func TestEmbedderOpensTheHTTPBackendThroughThePublicDoorsOnly(t *testing.T) {
	server := newStandInServer(t)
	bdhttp.Register(bdhttp.Options{HTTPClient: server.Client()})
	t.Cleanup(func() { backend.Deregister(backendName) })

	beadsDir := filepath.Join(t.TempDir(), ".beads")
	// SaveTarget, like the package's other low-level helpers, writes into an
	// already-existing directory; creating it is the caller's job (cmd/bd's
	// connect command does the same MkdirAll before calling SaveTarget).
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", beadsDir, err)
	}
	base, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse stand-in server URL: %v", err)
	}
	target := bdhttp.Target{BaseURL: base, ExpectProjectID: embedderProjectID}
	// Attach is the one public call that activates a workspace for this
	// backend: it writes the per-user sidecar (what SaveTarget alone used to
	// leave to this test) AND metadata.json's {"backend": "http"} selection —
	// the fact that tells OpenBestAvailableWith to consult the registry at
	// all — in one call, the same pair `bd connect` itself now calls through
	// (cmd/bd/connect.go). Before Attach existed this test hand-wrote
	// metadata.json's JSON shape directly, since there was no public door
	// onto that second file's writer; asserting the on-disk shape below
	// keeps this test proving the SAME canary (a rename of the registry key
	// breaks every connected workspace) without reaching into
	// internal/configfile for it.
	if err := bdhttp.Attach(beadsDir, target); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	metadataBytes, err := os.ReadFile(filepath.Join(beadsDir, "metadata.json"))
	if err != nil {
		t.Fatalf("reading metadata.json after Attach: %v", err)
	}
	var metadata struct {
		Backend string `json:"backend"`
	}
	if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
		t.Fatalf("parsing metadata.json after Attach: %v", err)
	}
	if metadata.Backend != backendName {
		t.Fatalf("metadata.json backend = %q, want %q", metadata.Backend, backendName)
	}

	// A multi-tenant embedder's reason to call OpenBestAvailableWith rather
	// than OpenBestAvailable: this credential is THIS caller's, not whatever
	// the ambient process environment happens to hold, which belongs to no
	// one tenant in a process serving many.
	provider := &tenantCredential{token: "gc-tenant-token"}
	store, err := beads.OpenBestAvailableWith(context.Background(), beadsDir, beads.OpenOptions{
		Credential: bdhttp.ProvidedCredential{Provider: provider},
	})
	if err != nil {
		t.Fatalf("OpenBestAvailableWith: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// The read itself goes through the role accessor, not a backend-specific
	// method: IssueReader().Ready is issueops.Reader's own door, the same one
	// `bd ready` calls against ANY backend. Ready is the simplest operation to
	// stand a fake server in for — a single GET with no pagination contract
	// and no handshake capability gate (it is a baseline v0 operation) — so
	// this proves the public linkage without reimplementing the wire's
	// capability negotiation in a throwaway test server.
	reader, err := store.IssueReader()
	if err != nil {
		t.Fatalf("IssueReader: %v", err)
	}
	page, err := reader.Ready(context.Background(), issueops.ReadyRequest{})
	if err != nil {
		t.Fatalf("Ready: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != embedderReadyIssueID {
		t.Errorf("ready page = %+v, want one item %q", page.Items, embedderReadyIssueID)
	}
	server.wantAuthorization(t, "Bearer gc-tenant-token")
}

// tenantCredential is a bdhttp.CredentialProvider scoped to one embedded
// tenant, built from nothing but the public CredentialProvider interface.
type tenantCredential struct{ token string }

func (c *tenantCredential) Authorize(_ context.Context, req *http.Request) error {
	req.Header.Set("Authorization", "Bearer "+c.token)
	return nil
}

func (c *tenantCredential) Refresh(context.Context) (bool, error) { return false, nil }

// standInServer is a minimal bd serve stand-in: just enough of the v0
// handshake response for Open/GetMetadata to succeed, recording the
// Authorization header every request carried.
type standInServer struct {
	*httptest.Server
	mu    sync.Mutex
	auths []string
}

func newStandInServer(t *testing.T) *standInServer {
	t.Helper()
	s := &standInServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.auths = append(s.auths, r.Header.Get("Authorization"))
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v0/beads/context":
			// Open/OpenWith dial lazily and never fetch this on their own
			// (the handshake is cached per-process on the first POST-BASELINE
			// dispatch — see internal/httpclient.Store.snapshot) — Ready
			// below is baseline and preflights without one. This handler
			// stays in place anyway: it is the one other request a workspace
			// opened through this backend could legitimately make, and a
			// stand-in server that 404s it would be a trap for the next
			// person who extends this test to a non-baseline operation.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"api_version":              "v0",
				"backend":                  "dolt",
				"bd_version":               "9.9.9",
				"beads_dir":                "/srv/.beads",
				"capabilities":             []string{"issues.get", "issues.list"},
				"database":                 "beads",
				"dolt_mode":                "server",
				"project_id":               embedderProjectID,
				"repo_root":                "/srv",
				"schema_version":           1,
				"wire_revision":            1,
				"min_client_wire_revision": 1,
			})
		case "/v0/beads/ready":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"has_more": false,
				"items": []map[string]any{
					{"id": embedderReadyIssueID, "title": "embedder linkage proof", "priority": 2},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Server.Close)
	return s
}

func (s *standInServer) wantAuthorization(t *testing.T, want string) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.auths) == 0 {
		t.Fatal("the stand-in server saw no requests at all")
	}
	for i, got := range s.auths {
		if got != want {
			t.Errorf("request %d carried Authorization %q, want %q", i, got, want)
		}
	}
}
