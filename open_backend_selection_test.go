package beads_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/backends"
)

var errRegistryBackendOpen = errors.New("registry backend open sentinel")

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
