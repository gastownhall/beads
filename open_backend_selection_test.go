package beads_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
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
func TestOpenBestAvailableWithZeroOptionsMatchesOpenBestAvailable(t *testing.T) {
	t.Run("registered backend without OpenWith", func(t *testing.T) {
		const name = "registry-backcompat-open-only"
		registerWorkspaceBackend(t, name)
		beadsDir := writeBackendMetadata(t, name)

		storeOld, errOld := beads.OpenBestAvailable(context.Background(), beadsDir)
		storeNew, errNew := beads.OpenBestAvailableWith(context.Background(), beadsDir, beads.OpenOptions{})
		if storeOld != nil {
			_ = storeOld.Close()
		}
		if storeNew != nil {
			_ = storeNew.Close()
		}
		if (storeOld != nil) != (storeNew != nil) {
			t.Fatalf("store presence differs: OpenBestAvailable store=%v, OpenBestAvailableWith store=%v", storeOld, storeNew)
		}
		if !errors.Is(errOld, errRegistryBackendOpen) || !errors.Is(errNew, errRegistryBackendOpen) {
			t.Fatalf("errors differ from the Open sentinel: old=%v new=%v", errOld, errNew)
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
	})

	t.Run("no registered backend (plain Dolt, server mode)", func(t *testing.T) {
		// dolt_mode:"server" plus an unreachable port makes both calls fail
		// fast on a dial error instead of spinning up embedded Dolt (slow,
		// and exercises a path this slice does not touch). What matters here
		// is that OpenBestAvailable and OpenBestAvailableWith(zero opts)
		// reach the identical dispatch branch and fail identically.
		beadsDir := plainDoltServerModeBeadsDir(t)
		storeOld, errOld := beads.OpenBestAvailable(context.Background(), beadsDir)
		if storeOld != nil {
			_ = storeOld.Close()
		}

		beadsDir2 := plainDoltServerModeBeadsDir(t)
		storeNew, errNew := beads.OpenBestAvailableWith(context.Background(), beadsDir2, beads.OpenOptions{})
		if storeNew != nil {
			_ = storeNew.Close()
		}
		if (storeOld != nil) != (storeNew != nil) {
			t.Fatalf("store presence differs: OpenBestAvailable store=%v, OpenBestAvailableWith store=%v", storeOld != nil, storeNew != nil)
		}
		if (errOld == nil) != (errNew == nil) {
			t.Fatalf("error presence differs: OpenBestAvailable err=%v, OpenBestAvailableWith err=%v", errOld, errNew)
		}
		if errOld == nil {
			t.Fatal("plain Dolt server mode against an unreachable port unexpectedly opened")
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

// TestOpenBestAvailableWithRefusesCredentialOnPlainDolt covers the other
// half: no registered backend at all (plain Dolt, embedded or server) has no
// per-open credential seam either, so the same refusal applies.
func TestOpenBestAvailableWithRefusesCredentialOnPlainDolt(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatalf("create .beads: %v", err)
	}

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
	// The fail-closed guarantee includes never provisioning a database while
	// refusing to honor the Credential.
	for _, artifact := range []string{"embeddeddolt", "dolt"} {
		if _, statErr := os.Stat(filepath.Join(beadsDir, artifact)); !os.IsNotExist(statErr) {
			t.Fatalf("Credential refusal on plain Dolt created %s (stat error: %v)", artifact, statErr)
		}
	}
}
