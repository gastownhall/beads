package beads_test

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/beads"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/backends"
)

var errRegistryBackendOpen = errors.New("registry backend open sentinel")

var errRegistryBackendOpenWith = errors.New("registry backend OpenWith sentinel")

// fixtureCredential is a minimal beads.Credential implementation for tests:
// a bare marker, since OpenOptions.Credential is deliberately opaque.
type fixtureCredential struct{}

func (fixtureCredential) BackendCredential() {}

// registerOpenWithCapturingBackend registers a backend whose OpenWith
// records the beadsDir and opts it received (via the returned pointers), and
// whose Open errors if dispatch ever falls back to it — proving
// OpenBestAvailableWith reaches OpenWith, not Open, when the backend has one.
func registerOpenWithCapturingBackend(t *testing.T, name string, gotBeadsDir *string, gotOpts *beads.OpenOptions, calls *int) {
	t.Helper()
	open := func(context.Context, string) (storage.DoltStorage, error) {
		return nil, errors.New("fixture: Open called despite backend having OpenWith")
	}
	backends.Register(name, backends.Backend{
		Open:         open,
		OpenReadOnly: open,
		OpenWith: func(_ context.Context, beadsDir string, opts backends.OpenOptions) (storage.DoltStorage, error) {
			*calls++
			*gotBeadsDir = beadsDir
			*gotOpts = opts
			return nil, errRegistryBackendOpenWith
		},
	})
	t.Cleanup(func() { backends.Deregister(name) })
}

// registerWorkspaceBackend registers a fake WorkspaceIsBeadsDir backend whose
// Open/OpenReadOnly report a sentinel instead of touching a store, so tests can
// assert that discovery and open dispatch to the registry without provisioning
// Dolt. Register requires both hooks to be non-nil.
func registerWorkspaceBackend(t *testing.T, name string) {
	t.Helper()
	backends.Register(name, backends.Backend{
		Open: func(context.Context, string) (storage.DoltStorage, error) {
			return nil, errRegistryBackendOpen
		},
		OpenReadOnly: func(context.Context, string) (storage.DoltStorage, error) {
			return nil, errRegistryBackendOpen
		},
		WorkspaceIsBeadsDir: true,
	})
	t.Cleanup(func() { backends.Deregister(name) })
}

// plainDoltServerModeBeadsDir writes metadata.json naming the default "dolt"
// backend in explicit server mode, and forces the dial target to a port
// nobody listens on via env vars that GetDoltServerPort/GetDoltServerHost
// check BEFORE metadata.json — TestMain's shared test Dolt container already
// sets BEADS_DOLT_PORT ambiently for the whole process, which would
// otherwise silently out-rank a port pinned only in metadata.json and make
// this open attempt succeed against the wrong server. t.Setenv scopes the
// override to the calling (sub)test and its cleanup.
func plainDoltServerModeBeadsDir(t *testing.T) string {
	t.Helper()
	t.Setenv("BEADS_DOLT_SERVER_HOST", "127.0.0.1")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "1")
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatalf("create .beads: %v", err)
	}
	metadata := `{"backend":"dolt","dolt_mode":"server"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(metadata), 0o600); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
	return beadsDir
}

func writeBackendMetadata(t *testing.T, backend string) string {
	t.Helper()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatalf("create .beads: %v", err)
	}
	metadata := `{"backend":"` + backend + `"}`
	if backend == "sqlite" {
		// Workspaces created by the removed SQLite backend carry an explicit
		// path marker; a bare backend:"sqlite" can also be stale metadata from
		// the earlier SQLite era (see PR #4740). Both must hit the tombstone.
		metadata = `{"backend":"sqlite","sqlite_path":"beads.db"}`
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(metadata), 0o600); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
	return beadsDir
}

func TestOpenBestAvailableRejectsSQLite(t *testing.T) {
	beadsDir := writeBackendMetadata(t, "sqlite")
	store, err := beads.OpenBestAvailable(context.Background(), beadsDir)
	if store != nil {
		_ = store.Close()
		t.Fatal("removed SQLite backend returned a store")
	}
	if err == nil || !strings.Contains(err.Error(), "no longer supported") {
		t.Fatalf("SQLite backend error = %v, want rollback explanation", err)
	}
	if !strings.Contains(err.Error(), "single engine") || !strings.Contains(err.Error(), "export") {
		t.Fatalf("SQLite backend error lacks rationale or migration guidance: %v", err)
	}
	// The fail-closed guarantee includes never provisioning the SQLite file the
	// removed backend would have created.
	for _, name := range []string{"embeddeddolt", "dolt", "beads.db"} {
		if _, statErr := os.Stat(filepath.Join(beadsDir, name)); !os.IsNotExist(statErr) {
			t.Fatalf("removed SQLite backend created %s (stat error: %v)", name, statErr)
		}
	}
}

func TestOpenBestAvailableRejectsRemovedBackends(t *testing.T) {
	for _, backend := range []string{"postgres", "mysql", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			beadsDir := writeBackendMetadata(t, backend)
			store, err := beads.OpenBestAvailable(context.Background(), beadsDir)
			if store != nil {
				_ = store.Close()
				t.Fatalf("removed backend %q returned a store", backend)
			}
			if err == nil || !strings.Contains(err.Error(), "no longer supported") {
				t.Fatalf("removed backend error = %v, want rollback explanation", err)
			}
			rationale := "resource-light"
			if backend == "sqlite" {
				rationale = "single engine"
			}
			if !strings.Contains(err.Error(), rationale) || !strings.Contains(err.Error(), "export") {
				t.Fatalf("removed backend error lacks rationale or migration guidance: %v", err)
			}
			for _, name := range []string{"embeddeddolt", "dolt", "beads.db"} {
				if _, statErr := os.Stat(filepath.Join(beadsDir, name)); !os.IsNotExist(statErr) {
					t.Fatalf("removed backend created %s (stat error: %v)", name, statErr)
				}
			}
		})
	}
}

// TestOpenBestAvailableOffersHealWhenDoltDataExists covers the public open path
// for the case D-8 exists for: metadata names a removed backend, but the
// workspace holds a Dolt database that bd v1.2.x opened happily. The library
// error must carry the same metadata heal the CLI gives — proving beadsDir and
// the config reach configuredBackendUnavailable in both the cgo and non-cgo
// builds — instead of the export/reinitialize path that destroys the database.
func TestOpenBestAvailableOffersHealWhenDoltDataExists(t *testing.T) {
	for _, backend := range []string{"postgres", "mysql", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			beadsDir := writeBackendMetadata(t, backend)
			doltDir := filepath.Join(beadsDir, "embeddeddolt", "beads")
			if err := os.MkdirAll(filepath.Join(doltDir, ".dolt"), 0o750); err != nil {
				t.Fatalf("plant dolt database: %v", err)
			}

			store, err := beads.OpenBestAvailable(context.Background(), beadsDir)
			if store != nil {
				_ = store.Close()
				t.Fatalf("removed backend %q returned a store", backend)
			}
			if err == nil {
				t.Fatal("removed backend with live Dolt data unexpectedly opened")
			}
			for _, want := range []string{
				"no longer supported",
				filepath.Join(beadsDir, "metadata.json"),
				`"backend": "dolt"`,
				doltDir,
				"no storage database was opened or modified",
			} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("public open error missing %q: %v", want, err)
				}
			}
			if strings.Contains(strings.ToLower(err.Error()), "export") {
				t.Errorf("public open error offered the destructive export path despite live Dolt data: %v", err)
			}
		})
	}
}

func TestOpenBestAvailableRejectsUnknownBackend(t *testing.T) {
	beadsDir := writeBackendMetadata(t, "mystery")
	store, err := beads.OpenBestAvailable(context.Background(), beadsDir)
	if store != nil {
		_ = store.Close()
		t.Fatal("unknown backend returned a store")
	}
	if err == nil || !strings.Contains(err.Error(), "not recognized") {
		t.Fatalf("unknown backend error = %v, want fail-closed metadata guidance", err)
	}
	if !strings.Contains(err.Error(), "no storage database was opened or modified") {
		t.Fatalf("unknown backend error lacks data-safety guarantee: %v", err)
	}
	for _, name := range []string{"embeddeddolt", "dolt", "beads.db"} {
		if _, statErr := os.Stat(filepath.Join(beadsDir, name)); !os.IsNotExist(statErr) {
			t.Fatalf("unknown backend created %s (stat error: %v)", name, statErr)
		}
	}
}

func TestOpenBestAvailableRejectsCorruptMetadata(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatalf("create .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte("{"), 0o600); err != nil {
		t.Fatalf("write metadata: %v", err)
	}

	store, err := beads.OpenBestAvailable(context.Background(), beadsDir)
	if store != nil {
		_ = store.Close()
		t.Fatal("corrupt metadata unexpectedly returned a store")
	}
	if err == nil || !strings.Contains(err.Error(), "metadata") {
		t.Fatalf("corrupt metadata error = %v, want metadata load failure", err)
	}
	if _, statErr := os.Stat(filepath.Join(beadsDir, "embeddeddolt")); !os.IsNotExist(statErr) {
		t.Fatalf("corrupt metadata created embedded Dolt storage (stat error: %v)", statErr)
	}
}

// TestOpenBestAvailableDispatchesRegisteredBackend covers the public library
// open path for a registered extension backend: OpenBestAvailable must call the
// backend rather than opening Dolt (CGO) or returning the embedded-Dolt error
// (non-CGO), mirroring the CLI store factories.
func TestOpenBestAvailableDispatchesRegisteredBackend(t *testing.T) {
	const name = "registry-open"
	registerWorkspaceBackend(t, name)

	beadsDir := writeBackendMetadata(t, name)
	store, err := beads.OpenBestAvailable(context.Background(), beadsDir)
	if store != nil {
		_ = store.Close()
		t.Fatal("registered backend dispatch returned a store instead of the backend's own result")
	}
	if !errors.Is(err, errRegistryBackendOpen) {
		t.Fatalf("OpenBestAvailable error = %v, want registered backend Open result", err)
	}
	// Dispatch must not fall through and provision an embedded Dolt store.
	for _, artifact := range []string{"embeddeddolt", "dolt"} {
		if _, statErr := os.Stat(filepath.Join(beadsDir, artifact)); !os.IsNotExist(statErr) {
			t.Fatalf("registered backend dispatch created %s (stat error: %v)", artifact, statErr)
		}
	}
}

// TestFindDatabasePathDiscoversRegisteredWorkspace covers public discovery
// parity: a registered WorkspaceIsBeadsDir backend has no local Dolt database,
// so FindDatabasePath must return the .beads directory itself instead of the
// empty "no database" result the Dolt-only search would give.
func TestFindDatabasePathDiscoversRegisteredWorkspace(t *testing.T) {
	const name = "registry-discovery"
	registerWorkspaceBackend(t, name)

	beadsDir := writeBackendMetadata(t, name)
	t.Setenv("BEADS_DIR", beadsDir)

	got := beads.FindDatabasePath()
	if got == "" {
		t.Fatal("FindDatabasePath returned empty for a WorkspaceIsBeadsDir backend")
	}
	// Path canonicalization (symlinked temp dirs) can rewrite the string, so
	// compare the workspace by identity rather than raw path equality.
	gotInfo, err := os.Stat(got)
	if err != nil {
		t.Fatalf("stat discovered path %q: %v", got, err)
	}
	wantInfo, err := os.Stat(beadsDir)
	if err != nil {
		t.Fatalf("stat beads dir %q: %v", beadsDir, err)
	}
	if !os.SameFile(gotInfo, wantInfo) {
		t.Fatalf("FindDatabasePath = %q, want the .beads workspace dir %q", got, beadsDir)
	}
	// The registry-only workspace carries no local Dolt database and discovery
	// must not create one.
	for _, artifact := range []string{"embeddeddolt", "dolt"} {
		if _, statErr := os.Stat(filepath.Join(beadsDir, artifact)); !os.IsNotExist(statErr) {
			t.Fatalf("registry workspace discovery created %s (stat error: %v)", artifact, statErr)
		}
	}
}

// TestOpenBestAvailableWithDispatchesRegisteredBackendAndPassesOptions proves
// the two S1 guarantees together: OpenBestAvailableWith dispatches to a
// registered backend's OpenWith (not Open), and the OpenOptions the caller
// supplied reach that OpenWith unchanged.
func TestOpenBestAvailableWithDispatchesRegisteredBackendAndPassesOptions(t *testing.T) {
	const name = "registry-openwith"
	var gotBeadsDir string
	var gotOpts beads.OpenOptions
	var calls int
	registerOpenWithCapturingBackend(t, name, &gotBeadsDir, &gotOpts, &calls)

	beadsDir := writeBackendMetadata(t, name)
	cred := fixtureCredential{}
	client := &http.Client{}
	wantOpts := beads.OpenOptions{Credential: cred, HTTPClient: client, UserAgent: "test-agent/1.0"}

	store, err := beads.OpenBestAvailableWith(context.Background(), beadsDir, wantOpts)
	if store != nil {
		_ = store.Close()
		t.Fatal("OpenBestAvailableWith returned a store instead of the backend's OpenWith result")
	}
	if !errors.Is(err, errRegistryBackendOpenWith) {
		t.Fatalf("OpenBestAvailableWith error = %v, want registered backend OpenWith result", err)
	}
	if calls != 1 {
		t.Fatalf("OpenWith called %d times, want 1", calls)
	}
	if gotOpts.Credential != cred {
		t.Fatalf("OpenWith opts.Credential = %v, want %v", gotOpts.Credential, cred)
	}
	if gotOpts.HTTPClient != client {
		t.Fatalf("OpenWith opts.HTTPClient = %v, want %v", gotOpts.HTTPClient, client)
	}
	if gotOpts.UserAgent != "test-agent/1.0" {
		t.Fatalf("OpenWith opts.UserAgent = %q, want test-agent/1.0", gotOpts.UserAgent)
	}
}

// TestOpenBestAvailableWithZeroOptionsMatchesOpenBestAvailable is the S1
// back-compat requirement: OpenBestAvailableWith with a zero OpenOptions must
// behave identically to OpenBestAvailable for every existing path —
// registered backend without OpenWith, registered backend with OpenWith, and
// plain Dolt (no registered backend at all).
//
// OpenBestAvailable now simply delegates to OpenBestAvailableWith with a zero
// OpenOptions (see beads_cgo.go and beads_nocgo.go), so a subtest that calls
// both and compares their results would be vacuous: it can never fail
// regardless of what either returns, because they are the same code path.
// Each subtest instead pins a fixed, independently-meaningful expected result
// — a sentinel identity, a concrete error message, or a concrete
// store-presence/nilness — so that a future change which breaks the
// delegation (e.g. a stray early return, or dropped opts threading) still has
// something to fail against.
func TestOpenBestAvailableWithZeroOptionsMatchesOpenBestAvailable(t *testing.T) {
	t.Run("registered backend without OpenWith", func(t *testing.T) {
		const name = "registry-backcompat-open-only"
		registerWorkspaceBackend(t, name)
		beadsDir := writeBackendMetadata(t, name)

		store, err := beads.OpenBestAvailableWith(context.Background(), beadsDir, beads.OpenOptions{})
		if store != nil {
			_ = store.Close()
			t.Fatal("OpenBestAvailableWith(zero opts) returned a non-nil store, want nil: the fixture's Open always fails")
		}
		if !errors.Is(err, errRegistryBackendOpen) {
			t.Fatalf("OpenBestAvailableWith(zero opts) error = %v, want errRegistryBackendOpen", err)
		}
	})

	t.Run("registered backend with OpenWith", func(t *testing.T) {
		const name = "registry-backcompat-openwith"
		var gotBeadsDir string
		var gotOpts beads.OpenOptions
		var calls int
		registerOpenWithCapturingBackend(t, name, &gotBeadsDir, &gotOpts, &calls)
		beadsDir := writeBackendMetadata(t, name)

		_, errNew := beads.OpenBestAvailableWith(context.Background(), beadsDir, beads.OpenOptions{})
		if !errors.Is(errNew, errRegistryBackendOpenWith) {
			t.Fatalf("OpenBestAvailableWith(zero opts) error = %v, want OpenWith sentinel", errNew)
		}
		if calls != 1 {
			t.Fatalf("OpenWith called %d times, want 1", calls)
		}
		if gotOpts != (beads.OpenOptions{}) {
			t.Fatalf("OpenWith opts = %+v, want the zero value (OpenBestAvailable never set any)", gotOpts)
		}
		if gotBeadsDir != beadsDir {
			t.Fatalf("OpenWith beadsDir = %q, want %q", gotBeadsDir, beadsDir)
		}
	})

	t.Run("no registered backend (plain Dolt, server mode)", func(t *testing.T) {
		// dolt_mode:"server" plus an unreachable port (127.0.0.1:1) makes
		// the dial fail fast with a fixed, well-known error rather than
		// spinning up embedded Dolt (slow, and exercises a path this slice
		// does not touch).
		beadsDir := plainDoltServerModeBeadsDir(t)
		store, err := beads.OpenBestAvailableWith(context.Background(), beadsDir, beads.OpenOptions{})
		if store != nil {
			_ = store.Close()
			t.Fatal("OpenBestAvailableWith(zero opts) returned a non-nil store, want nil: 127.0.0.1:1 is unreachable")
		}
		if err == nil {
			t.Fatal("plain Dolt server mode against an unreachable port unexpectedly opened")
		}
		const wantSubstring = "Dolt server unreachable at 127.0.0.1:1"
		if !strings.Contains(err.Error(), wantSubstring) {
			t.Fatalf("OpenBestAvailableWith(zero opts) error = %q, want it to contain %q", err.Error(), wantSubstring)
		}
	})
}

// TestOpenBestAvailableWithRefusesCredentialWithoutOpenWithOnRegisteredBackend
// covers the registered-backend half of the design's refusal requirement:
// a backend with no OpenWith must never silently drop a caller's Credential.
func TestOpenBestAvailableWithRefusesCredentialWithoutOpenWithOnRegisteredBackend(t *testing.T) {
	const name = "registry-credential-refusal"
	registerWorkspaceBackend(t, name) // Open/OpenReadOnly only, no OpenWith
	beadsDir := writeBackendMetadata(t, name)

	store, err := beads.OpenBestAvailableWith(context.Background(), beadsDir, beads.OpenOptions{
		Credential: fixtureCredential{},
	})
	if store != nil {
		_ = store.Close()
		t.Fatal("OpenBestAvailableWith returned a store alongside the Credential refusal")
	}
	if !errors.Is(err, beads.ErrCredentialWithoutOpenWith) {
		t.Fatalf("OpenBestAvailableWith error = %v, want %v", err, beads.ErrCredentialWithoutOpenWith)
	}
}

// TestOpenBestAvailableWithRefusesPerOpenOptionsOnPlainDolt covers the other
// half: no registered backend at all (plain Dolt, embedded or server) has no
// per-open seam either, so the same refusal applies to each of Credential,
// HTTPClient, and UserAgent. Those refusal arms are duplicated in
// beads_cgo.go and beads_nocgo.go, so this runs against whichever copy the
// build's CGO setting compiles.
func TestOpenBestAvailableWithRefusesPerOpenOptionsOnPlainDolt(t *testing.T) {
	tests := []struct {
		name string
		opts beads.OpenOptions
		want error
	}{
		{name: "Credential", opts: beads.OpenOptions{Credential: fixtureCredential{}}, want: beads.ErrCredentialWithoutOpenWith},
		{name: "HTTPClient", opts: beads.OpenOptions{HTTPClient: &http.Client{}}, want: beads.ErrHTTPClientWithoutOpenWith},
		{name: "UserAgent", opts: beads.OpenOptions{UserAgent: "test-agent/1.0"}, want: beads.ErrUserAgentWithoutOpenWith},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			beadsDir := filepath.Join(t.TempDir(), ".beads")
			if err := os.MkdirAll(beadsDir, 0o750); err != nil {
				t.Fatalf("create .beads: %v", err)
			}

			store, err := beads.OpenBestAvailableWith(context.Background(), beadsDir, tt.opts)
			if store != nil {
				_ = store.Close()
				t.Fatalf("OpenBestAvailableWith returned a store alongside the %s refusal", tt.name)
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("OpenBestAvailableWith error = %v, want %v", err, tt.want)
			}
			// The fail-closed guarantee includes never provisioning a database
			// while refusing to honor the option.
			for _, artifact := range []string{"embeddeddolt", "dolt"} {
				if _, statErr := os.Stat(filepath.Join(beadsDir, artifact)); !os.IsNotExist(statErr) {
					t.Fatalf("%s refusal on plain Dolt created %s (stat error: %v)", tt.name, artifact, statErr)
				}
			}
		})
	}
}

// graphWorkspaceMetadata is the metadata.json a graph-preview build of bd
// writes for a graph_mode "link" workspace: the marker and its binding beside
// an ordinary embedded Dolt backend.
const graphWorkspaceMetadata = `{
  "database": "",
  "backend": "dolt",
  "graph_mode": "link",
  "graph_scope_url": "https://example.test/scope",
  "graph_authority_id": "0123456789abcdef0123456789abcdef",
  "graph_workspace": "/work/repo/.beads",
  "graph_schema_version": 7,
  "graph_ready": true,
  "dolt_mode": "embedded",
  "dolt_database": "beads_graph_0123456789abcdef0123456789abcdef",
  "project_id": "0123456789abcdef0123456789abcdef"
}`

// graphWorkspaceMetadataStripped is graphWorkspaceMetadata as a bd that
// predates graph_mode rewrites it: the graph fields are gone, but the database
// name still points at the graph database.
const graphWorkspaceMetadataStripped = `{
  "database": "",
  "backend": "dolt",
  "dolt_mode": "embedded",
  "dolt_database": "beads_graph_0123456789abcdef0123456789abcdef",
  "project_id": "0123456789abcdef0123456789abcdef"
}`

// publicOpens are the public entry points that open a workspace from its
// metadata.
var publicOpens = map[string]func(context.Context, string) (beads.Storage, error){
	"OpenBestAvailable": beads.OpenBestAvailable,
	"OpenBestAvailableWith": func(ctx context.Context, beadsDir string) (beads.Storage, error) {
		return beads.OpenBestAvailableWith(ctx, beadsDir, beads.OpenOptions{})
	},
	"OpenFromConfig": beads.OpenFromConfig,
	"OpenGated": func(ctx context.Context, beadsDir string) (beads.Storage, error) {
		return beads.OpenGated(ctx, beadsDir, 0)
	},
}

// workspaceTreeDigest records every path under root with its kind and, for
// files, its content, so a test can prove an open left a workspace untouched.
func workspaceTreeDigest(t *testing.T, root string) map[string]string {
	t.Helper()
	digest := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			digest[rel] = "dir"
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		digest[rel] = "file:" + string(content)
		return nil
	})
	if err != nil {
		t.Fatalf("digest %s: %v", root, err)
	}
	return digest
}

// TestPublicOpensRefuseGraphWorkspace is the library half of the graph_mode
// fence the CLI applies at admission: every public open of a workspace whose
// metadata or graph-preview-format marker declares another data model refuses
// it with the typed error before it opens, creates or modifies anything, in
// the workspace or beside it (the workspace gate file OpenGated takes lives
// beside .beads).
func TestPublicOpensRefuseGraphWorkspace(t *testing.T) {
	// If the fence were missing, a server-mode open must fail fast rather
	// than reach TestMain's shared test server.
	t.Setenv("BEADS_DOLT_SERVER_HOST", "127.0.0.1")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "1")
	layouts := []struct {
		name     string
		filename string // "" writes no metadata file
		metadata string
		mode     string
		embedded bool
		// marker writes the graph-preview-format marker graph-preview builds
		// leave in .beads; the refusal then names the marker, not a mode.
		marker bool
	}{
		{name: "link embedded", filename: "metadata.json", metadata: graphWorkspaceMetadata, mode: "link", embedded: true},
		{name: "link server", filename: "metadata.json", metadata: `{"backend":"dolt","dolt_mode":"server","dolt_database":"beads_graph_0123","graph_mode":"link"}`, mode: "link"},
		{name: "unknown marker", filename: "metadata.json", metadata: `{"backend":"dolt","graph_mode":"future-format"}`, mode: "future-format"},
		// A refused open must not migrate a legacy config.json either.
		{name: "link legacy config.json", filename: "config.json", metadata: graphWorkspaceMetadata, mode: "link", embedded: true},
		// A graph workspace whose metadata was never written, was lost, or
		// was rewritten without graph_mode is still declared by the marker.
		{name: "marker only", marker: true},
		{name: "marker beside stripped metadata", filename: "metadata.json", metadata: graphWorkspaceMetadataStripped, embedded: true, marker: true},
		{name: "marker beside stripped legacy config.json", filename: "config.json", metadata: graphWorkspaceMetadataStripped, embedded: true, marker: true},
		{name: "marker beside server metadata", filename: "metadata.json", metadata: `{"backend":"dolt","dolt_mode":"server","dolt_database":"beads_graph_0123"}`, marker: true},
		{name: "marker beside dependency metadata", filename: "metadata.json", metadata: `{"backend":"dolt","dolt_mode":"server","graph_mode":"dependency"}`, marker: true},
		{name: "marker beside unparseable metadata", filename: "metadata.json", metadata: `{"backend":"dolt",`, marker: true},
	}
	for _, layout := range layouts {
		for name, open := range publicOpens {
			t.Run(layout.name+"/"+name, func(t *testing.T) {
				root := t.TempDir()
				beadsDir := filepath.Join(root, ".beads")
				if err := os.MkdirAll(beadsDir, 0o750); err != nil {
					t.Fatal(err)
				}
				if layout.filename != "" {
					if err := os.WriteFile(filepath.Join(beadsDir, layout.filename), []byte(layout.metadata), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if layout.marker {
					if err := os.WriteFile(filepath.Join(beadsDir, "graph-preview-format"), []byte("link-preview-v9\n"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if layout.embedded {
					graphDB := filepath.Join(beadsDir, "embeddeddolt", "beads_graph_0123456789abcdef0123456789abcdef", ".dolt")
					if err := os.MkdirAll(graphDB, 0o750); err != nil {
						t.Fatal(err)
					}
				}
				before := workspaceTreeDigest(t, root)

				store, err := open(t.Context(), beadsDir)
				if err == nil {
					_ = store.Close()
					t.Fatalf("%s opened a graph_mode %q workspace", name, layout.mode)
				}
				if store != nil {
					t.Fatalf("%s returned a non-nil %T with its refusal; an embedder's `if store != nil { store.Close() }` would call Close on it", name, store)
				}
				var formatErr *beads.UnsupportedWorkspaceFormatError
				if !errors.As(err, &formatErr) {
					t.Fatalf("%s error = %v, want *beads.UnsupportedWorkspaceFormatError", name, err)
				}
				if formatErr.BeadsDir != beadsDir || formatErr.GraphMode != layout.mode || formatErr.FormatMarker != layout.marker {
					t.Fatalf("refusal = %+v, want BeadsDir %q, GraphMode %q and FormatMarker %v", formatErr, beadsDir, layout.mode, layout.marker)
				}
				if layout.marker && !strings.Contains(err.Error(), "graph-preview-format marker") {
					t.Fatalf("refusal does not name the format marker: %v", err)
				}
				if !strings.Contains(err.Error(), "no storage database was opened or modified") {
					t.Fatalf("refusal lacks the data-safety guarantee: %v", err)
				}
				if after := workspaceTreeDigest(t, root); !maps.Equal(after, before) {
					t.Fatalf("%s changed the workspace: before %v, after %v", name, slices.Sorted(maps.Keys(before)), slices.Sorted(maps.Keys(after)))
				}
			})
		}
	}
}

// TestPublicOpensAdmitDependencyWorkspace pins the other side of the fence: a
// "dependency" marker is the ordinary Issue workspace, so the open proceeds to
// its backend (here, a server nothing listens on) instead of being refused.
func TestPublicOpensAdmitDependencyWorkspace(t *testing.T) {
	t.Setenv("BEADS_DOLT_SERVER_HOST", "127.0.0.1")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "1")
	for name, open := range publicOpens {
		t.Run(name, func(t *testing.T) {
			beadsDir := filepath.Join(t.TempDir(), ".beads")
			if err := os.MkdirAll(beadsDir, 0o750); err != nil {
				t.Fatal(err)
			}
			metadata := `{"backend":"dolt","dolt_mode":"server","graph_mode":"dependency"}`
			if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(metadata), 0o600); err != nil {
				t.Fatal(err)
			}
			store, err := open(t.Context(), beadsDir)
			if err == nil {
				_ = store.Close()
				t.Fatalf("%s opened a store on an unreachable server", name)
			}
			var formatErr *beads.UnsupportedWorkspaceFormatError
			if errors.As(err, &formatErr) || !strings.Contains(err.Error(), "unreachable") {
				t.Fatalf("%s error = %v, want the backend's unreachable-server error", name, err)
			}
		})
	}
}

// TestPublicOpensRefuseUnsupportedBackendBeforeWriting pins that every public
// open refuses metadata naming a removed or unrecognized backend before it
// writes anything, whichever file holds it. For a legacy config.json,
// configfile.Load would migrate that file into metadata.json, dropping the
// keys Config does not know; for either file, OpenGated's gates create files
// in and beside .beads. The workspace holds a Dolt database, so the refusal
// offers the metadata heal, and it must name the file that actually holds the
// backend rather than one that does not exist.
func TestPublicOpensRefuseUnsupportedBackendBeforeWriting(t *testing.T) {
	// If the refusal were missing, a server-mode open must fail fast rather
	// than reach TestMain's shared test server.
	t.Setenv("BEADS_DOLT_SERVER_HOST", "127.0.0.1")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "1")
	layouts := []struct{ name, file, other string }{
		{"legacy", "config.json", "metadata.json"},
		{"metadata", "metadata.json", "config.json"},
	}
	for _, layout := range layouts {
		for _, backend := range []string{"mystery", "postgres", "mysql", "sqlite"} {
			for _, doltMode := range []string{"embedded", "server"} {
				for name, open := range publicOpens {
					t.Run(layout.name+"/"+backend+"/"+doltMode+"/"+name, func(t *testing.T) {
						root := t.TempDir()
						beadsDir := filepath.Join(root, ".beads")
						if err := os.MkdirAll(filepath.Join(beadsDir, "dolt", ".dolt"), 0o750); err != nil {
							t.Fatal(err)
						}
						metadata := `{"backend":"` + backend + `","dolt_mode":"` + doltMode + `","dolt_database":"beads_legacy","future_binding":"keep-me"}`
						if err := os.WriteFile(filepath.Join(beadsDir, layout.file), []byte(metadata), 0o600); err != nil {
							t.Fatal(err)
						}
						before := workspaceTreeDigest(t, root)

						store, err := open(t.Context(), beadsDir)
						if err == nil {
							_ = store.Close()
							t.Fatalf("%s opened a workspace whose %s names backend %q", name, layout.file, backend)
						}
						// The tree first: a regression that migrates config.json
						// must report the rewrite, not just the file it names.
						if after := workspaceTreeDigest(t, root); !maps.Equal(after, before) {
							t.Fatalf("%s changed the workspace: before %v, after %v", name, before, after)
						}
						if store != nil {
							t.Fatalf("%s returned a non-nil %T with its refusal", name, store)
						}
						if !strings.Contains(err.Error(), strconv.Quote(backend)) || !strings.Contains(err.Error(), "no storage database was opened or modified") {
							t.Fatalf("%s error = %v, want the refusal of backend %q with the data-safety guarantee", name, err, backend)
						}
						if !strings.Contains(err.Error(), layout.file) || strings.Contains(err.Error(), layout.other) {
							t.Fatalf("%s error = %v, want it to name %s, the file holding the backend, and not %s", name, err, layout.file, layout.other)
						}
					})
				}
			}
		}
	}
}

// TestDoltOnlyOpensRefuseRegisteredBackendBeforeWriting pins that the public
// opens that only open Dolt (OpenFromConfig and OpenGated) refuse a
// registered non-Dolt backend before they write anything, whichever file
// names it: Load would migrate a legacy config.json, and OpenGated's gates
// create files in and beside .beads. OpenBestAvailable* dispatch such a
// backend to the registry instead
// (TestOpenBestAvailableWithDispatchesRegisteredBackendAndPassesOptions).
func TestDoltOnlyOpensRefuseRegisteredBackendBeforeWriting(t *testing.T) {
	t.Setenv("BEADS_DOLT_SERVER_HOST", "127.0.0.1")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "1")
	const backend = "fake-registered-dolt-only"
	registerWorkspaceBackend(t, backend)
	opens := map[string]func(context.Context, string) (beads.Storage, error){
		"OpenFromConfig": publicOpens["OpenFromConfig"],
		"OpenGated":      publicOpens["OpenGated"],
	}
	for _, file := range []string{"config.json", "metadata.json"} {
		for name, open := range opens {
			t.Run(file+"/"+name, func(t *testing.T) {
				root := t.TempDir()
				beadsDir := filepath.Join(root, ".beads")
				if err := os.MkdirAll(filepath.Join(beadsDir, "dolt", ".dolt"), 0o750); err != nil {
					t.Fatal(err)
				}
				metadata := `{"backend":"` + backend + `","dolt_mode":"server","dolt_database":"beads_legacy","future_binding":"keep-me"}`
				if err := os.WriteFile(filepath.Join(beadsDir, file), []byte(metadata), 0o600); err != nil {
					t.Fatal(err)
				}
				before := workspaceTreeDigest(t, root)

				store, err := open(t.Context(), beadsDir)
				if err == nil {
					_ = store.Close()
					t.Fatalf("%s opened a workspace whose %s names registered backend %q", name, file, backend)
				}
				if after := workspaceTreeDigest(t, root); !maps.Equal(after, before) {
					t.Fatalf("%s changed the workspace: before %v, after %v", name, before, after)
				}
				if store != nil {
					t.Fatalf("%s returned a non-nil %T with its refusal", name, store)
				}
				if want := strconv.Quote(backend) + " cannot be opened as Dolt"; !strings.Contains(err.Error(), want) {
					t.Fatalf("%s error = %v, want %q", name, err, want)
				}
			})
		}
	}
}

// TestPublicOpensAdmitSupportedBackendInLegacyConfig pins the admitted side
// of the pre-Load backend refusal: a legacy config.json naming Dolt, or no
// backend, passes it, is migrated into metadata.json as before, and the open
// proceeds to the backend's own unreachable-server error.
func TestPublicOpensAdmitSupportedBackendInLegacyConfig(t *testing.T) {
	t.Setenv("BEADS_DOLT_SERVER_HOST", "127.0.0.1")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "1")
	for _, backend := range []string{"dolt", ""} {
		for name, open := range publicOpens {
			t.Run("backend="+backend+"/"+name, func(t *testing.T) {
				beadsDir := filepath.Join(t.TempDir(), ".beads")
				if err := os.MkdirAll(beadsDir, 0o750); err != nil {
					t.Fatal(err)
				}
				legacy := `{"backend":"` + backend + `","dolt_mode":"server"}`
				if err := os.WriteFile(filepath.Join(beadsDir, "config.json"), []byte(legacy), 0o600); err != nil {
					t.Fatal(err)
				}
				store, err := open(t.Context(), beadsDir)
				if err == nil {
					_ = store.Close()
					t.Fatalf("%s opened a store on an unreachable server", name)
				}
				if !strings.Contains(err.Error(), "unreachable") {
					t.Fatalf("%s error = %v, want the backend's unreachable-server error", name, err)
				}
				if _, statErr := os.Stat(filepath.Join(beadsDir, "metadata.json")); statErr != nil {
					t.Fatalf("%s did not migrate the admitted legacy config.json: %v", name, statErr)
				}
			})
		}
	}
}
