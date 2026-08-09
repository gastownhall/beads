package main

import (
	"context"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/backends"
)

var (
	errRegistryReadWrite = errors.New("registry read-write open")
	errRegistryReadOnly  = errors.New("registry read-only open")
)

func registerContractBackend(t *testing.T, name string) {
	t.Helper()
	backends.Register(name, backends.Backend{
		Open: func(context.Context, string) (storage.DoltStorage, error) {
			return nil, errRegistryReadWrite
		},
		OpenReadOnly: func(context.Context, string) (storage.DoltStorage, error) {
			return nil, errRegistryReadOnly
		},
		WorkspaceIsBeadsDir: true,
	})
	t.Cleanup(func() { backends.Deregister(name) })
}

func writeContractBackendConfig(t *testing.T, backend string) string {
	t.Helper()
	beadsDir := t.TempDir()
	if err := (&configfile.Config{Backend: backend}).Save(beadsDir); err != nil {
		t.Fatalf("save metadata.json: %v", err)
	}
	return beadsDir
}

func TestRegisteredBackendDispatchesReadWriteAndReadOnly(t *testing.T) {
	const name = "contract"
	registerContractBackend(t, name)
	beadsDir := writeContractBackendConfig(t, name)

	if err := validateConfiguredBackend(&configfile.Config{Backend: name}); err != nil {
		t.Fatalf("validateConfiguredBackend() rejected registered backend: %v", err)
	}
	if _, err := newDoltStoreFromConfig(t.Context(), beadsDir); !errors.Is(err, errRegistryReadWrite) {
		t.Fatalf("read-write factory error = %v, want %v", err, errRegistryReadWrite)
	}
	if _, err := newReadOnlyStoreFromConfig(t.Context(), beadsDir); !errors.Is(err, errRegistryReadOnly) {
		t.Fatalf("read-only factory error = %v, want %v", err, errRegistryReadOnly)
	}
}

func TestRegisteredBackendDrivesWorkspaceDiscovery(t *testing.T) {
	const name = "contract-discovery"
	registerContractBackend(t, name)

	if !registeredBackendWorkspaceIsBeadsDir(&configfile.Config{Backend: name}) {
		t.Fatal("registered backend did not expose its .beads workspace")
	}
	if registeredBackendWorkspaceIsBeadsDir(&configfile.Config{Backend: "unregistered"}) {
		t.Fatal("unregistered backend exposed a .beads workspace")
	}
	if registeredBackendWorkspaceIsBeadsDir(&configfile.Config{Backend: configfile.BackendDolt}) {
		t.Fatal("Dolt must retain its existing database discovery path")
	}
}

func TestOSSRegistersNoRemovedBackends(t *testing.T) {
	for _, name := range []string{
		configfile.BackendPostgres,
		configfile.BackendMySQL,
		configfile.BackendSQLite,
	} {
		if backends.Registered(name) {
			t.Errorf("OSS unexpectedly registered removed backend %q", name)
		}
		if err := validateConfiguredBackend(&configfile.Config{Backend: name}); err == nil {
			t.Errorf("OSS unexpectedly accepted removed backend %q", name)
		}
	}
}
