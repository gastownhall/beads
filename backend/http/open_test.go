package bdhttp_test

// Adapted from bd-enterprise backend/http/open_test.go under the MIT
// contribution: TestOpenNeedsNoWorkspaceOnDisk, the ServerSnapshot field-set
// drift guard, TestHandshakeReportsAWrongServer, and the activation sidecar
// round trip carry the same reasoning. Dropped or reshaped for OSS: the
// Credentials-hook tests (OSS takes a per-call Options.Credential instead;
// its tests are in per_open_credential_test.go), ServerInfo / lazy ServerContext (the OSS Store
// does not expose that interface), and ServerSnapshot's field set itself,
// which follows DESIGN.txt exactly (WireRevision in place of the ORM-backed
// field bd-enterprise used). No bd-enterprise-specific wording carries over.

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"reflect"
	"testing"

	bdhttp "github.com/steveyegge/beads/backend/http"
)

// TestOpenNeedsNoWorkspaceOnDisk is the programmatic door's reason to exist.
// An embedder that already knows the server — it read the URL from its own
// config, or minted a per-tenant one — has no .beads directory to point the
// registry at, and creating one just to open a store would be a file written
// for the benefit of a lookup.
func TestOpenNeedsNoWorkspaceOnDisk(t *testing.T) {
	hermeticEnv(t)
	t.Setenv(bdhttp.TokenEnv, "127.0.0.1=direct-token")
	server := newRecordingServer(t)

	store, err := bdhttp.Open(context.Background(), server.target(t), bdhttp.Options{HTTPClient: server.Client()})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	got, err := store.GetMetadata(context.Background(), "_project_id")
	if err != nil {
		t.Fatalf("GetMetadata: %v", err)
	}
	if got != projectID {
		t.Errorf("project id = %q, want %q", got, projectID)
	}
	server.wantAuthorization(t, "Bearer direct-token")
}

// TestOpenWithoutRegistrationStillDials pins the independence of the two
// doors: Open dials from the Options it is handed, with no dependence on
// Register ever having run.
func TestOpenWithoutRegistrationStillDials(t *testing.T) {
	hermeticEnv(t)
	t.Setenv(bdhttp.TokenEnv, "127.0.0.1=unregistered")
	server := newRecordingServer(t)

	store, err := bdhttp.Open(context.Background(), server.target(t), bdhttp.Options{HTTPClient: server.Client()})
	if err != nil {
		t.Fatalf("Open without Register: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if _, err := store.GetMetadata(context.Background(), "_project_id"); err != nil {
		t.Fatalf("GetMetadata: %v", err)
	}
	server.wantAuthorization(t, "Bearer unregistered")
}

// TestHandshakeVerifiesBeforeAnythingIsWritten is the connect path's probe. A
// caller about to record a server has to be able to check it first, and doing
// that by opening a store would mean describing a workspace before knowing
// whether the server behind it is the right one.
func TestHandshakeVerifiesBeforeAnythingIsWritten(t *testing.T) {
	hermeticEnv(t)
	server := newRecordingServer(t)

	snapshot, err := bdhttp.Handshake(context.Background(), server.target(t), bdhttp.Options{HTTPClient: server.Client()})
	if err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	if snapshot.ProjectID != projectID {
		t.Errorf("project id = %q, want %q", snapshot.ProjectID, projectID)
	}
	if snapshot.BdVersion != "9.9.9" {
		t.Errorf("bd_version = %q, want the server's own", snapshot.BdVersion)
	}
	if snapshot.APIVersion != "v0" {
		t.Errorf("api_version = %q, want the major this client speaks", snapshot.APIVersion)
	}
	if snapshot.WireRevision != 1 {
		t.Errorf("wire_revision = %d, want the server's own", snapshot.WireRevision)
	}
	if len(snapshot.Capabilities) != 2 || snapshot.Capabilities[0] != "issues.get" {
		t.Errorf("capabilities = %v, want the server's advertised list", snapshot.Capabilities)
	}
}

// TestServerSnapshotExportsNoHostFacts is the drift guard on the curated
// snapshot, and it is a guard rather than a comment because the thing it
// prevents arrives by accident.
//
// The wire document is GENERATED from the OpenAPI spec. Aliasing it, or
// widening this struct to match it, would put a codegen bump — a renamed
// field, a new member — on a published Go API, and would hand every embedder
// the server's own filesystem paths. A multi-tenant caller holding another
// tenant's repo root is not a field it chose to read; it is a field it could
// not avoid.
func TestServerSnapshotExportsNoHostFacts(t *testing.T) {
	want := map[string]bool{
		"APIVersion":   true,
		"BdVersion":    true,
		"WireRevision": true,
		"ProjectID":    true,
		"Capabilities": true,
	}
	typ := reflect.TypeOf(bdhttp.ServerSnapshot{})
	if typ.PkgPath() != "github.com/steveyegge/beads/backend/http" {
		t.Fatalf("ServerSnapshot is declared in %q: it must be this package's own struct, not an alias of the generated wire type", typ.PkgPath())
	}
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		if !want[name] {
			t.Errorf("ServerSnapshot exports %q, which is not on the curated set: server host paths, storage mode, logical database and the CLI schema version stay behind this door", name)
			continue
		}
		delete(want, name)
	}
	for name := range want {
		t.Errorf("ServerSnapshot no longer carries %q", name)
	}
}

// TestHandshakeReportsAWrongServer pins the gate that makes the probe worth
// running: the pinned identity is checked, and the refusal is typed, so a
// caller can tell "that is the wrong server" from "that server is down"
// without matching text.
func TestHandshakeReportsAWrongServer(t *testing.T) {
	hermeticEnv(t)
	server := newRecordingServer(t)
	target := server.target(t)
	target.ExpectProjectID = "proj-somewhere-else"

	_, err := bdhttp.Handshake(context.Background(), target, bdhttp.Options{HTTPClient: server.Client()})
	if !errors.Is(err, bdhttp.ErrProjectMismatch) {
		t.Fatalf("Handshake against a wrong server = %v, want ErrProjectMismatch", err)
	}
	var mismatch *bdhttp.ProjectMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("the refusal is not a *ProjectMismatchError: %v", err)
	}
}

// TestHandshakeUsesTheSameLadderOpenWouldUse keeps the two doors on one
// credential path: a probe that authorized differently from the store it
// precedes would verify a server the store then cannot reach.
func TestHandshakeUsesTheSameLadderOpenWouldUse(t *testing.T) {
	hermeticEnv(t)
	t.Setenv(bdhttp.TokenEnv, "127.0.0.1=probe-token")
	server := newRecordingServer(t)

	if _, err := bdhttp.Handshake(context.Background(), server.target(t), bdhttp.Options{HTTPClient: server.Client()}); err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	server.wantAuthorization(t, "Bearer probe-token")
}

// TestTheActivationSidecarRoundTrips covers the workspace half of the
// surface: an embedder that writes a connected workspace for a later `bd`
// invocation, or reads the one `bd connect` already wrote, goes through
// these.
func TestTheActivationSidecarRoundTrips(t *testing.T) {
	beadsDir := t.TempDir()

	if _, err := bdhttp.LoadTarget(beadsDir); !errors.Is(err, bdhttp.ErrNotConnected) {
		t.Fatalf("LoadTarget on an unconnected workspace = %v, want ErrNotConnected", err)
	}
	if removed, err := bdhttp.RemoveTarget(beadsDir); err != nil || removed {
		t.Fatalf("RemoveTarget on an unconnected workspace = (%v, %v), want (false, nil)", removed, err)
	}

	base, err := url.Parse("https://beads.example.com")
	if err != nil {
		t.Fatal(err)
	}
	want := bdhttp.Target{BaseURL: base, ExpectProjectID: projectID}
	if err := bdhttp.SaveTarget(beadsDir, want); err != nil {
		t.Fatalf("SaveTarget: %v", err)
	}
	if got := bdhttp.TargetPath(beadsDir); got != filepath.Join(beadsDir, bdhttp.TargetFileName) {
		t.Errorf("TargetPath = %q, want the sidecar beside metadata.json", got)
	}

	got, err := bdhttp.LoadTarget(beadsDir)
	if err != nil {
		t.Fatalf("LoadTarget: %v", err)
	}
	if got.String() != want.String() || got.ExpectProjectID != want.ExpectProjectID {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}

	removed, err := bdhttp.RemoveTarget(beadsDir)
	if err != nil {
		t.Fatalf("RemoveTarget: %v", err)
	}
	if !removed {
		t.Error("RemoveTarget did not report the sidecar it deleted")
	}
	if _, err := bdhttp.LoadTarget(beadsDir); !errors.Is(err, bdhttp.ErrNotConnected) {
		t.Errorf("LoadTarget after RemoveTarget = %v, want ErrNotConnected", err)
	}
}

// TestABearerProviderIsBuildableFromThePublicSurface proves the default
// ladder is nameable, which is what lets a caller wrap or inspect it without
// reimplementing it.
func TestABearerProviderIsBuildableFromThePublicSurface(t *testing.T) {
	hermeticEnv(t)
	t.Setenv(bdhttp.TokenEnv, "127.0.0.1=ladder-token")
	server := newRecordingServer(t)

	var provider bdhttp.CredentialProvider = bdhttp.NewBearerProvider(server.target(t).BaseURL)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.Authorize(context.Background(), req); err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer ladder-token" {
		t.Errorf("Authorization = %q, want the ladder's token", got)
	}
}
