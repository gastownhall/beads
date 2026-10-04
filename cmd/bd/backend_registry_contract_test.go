package main

import (
	"context"
	"errors"
	"strings"
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

// registerContractRemoteBackend is registerContractBackend plus Remote: true,
// for tests of the IsRemote-aware store-open error framing.
func registerContractRemoteBackend(t *testing.T, name string) {
	t.Helper()
	backends.Register(name, backends.Backend{
		Open: func(context.Context, string) (storage.DoltStorage, error) {
			return nil, errRegistryReadWrite
		},
		OpenReadOnly: func(context.Context, string) (storage.DoltStorage, error) {
			return nil, errRegistryReadOnly
		},
		WorkspaceIsBeadsDir: true,
		Remote:              true,
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

	if err := validateConfiguredBackend(&configfile.Config{Backend: name}, beadsDir); err != nil {
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
		if err := validateConfiguredBackend(&configfile.Config{Backend: name}, t.TempDir()); err == nil {
			t.Errorf("OSS unexpectedly accepted removed backend %q", name)
		}
	}
}

// TestOpenStoreErrorMessageFramesRemoteBackends is the M1 fix: a registered
// Remote backend's open failure must not be announced as "failed to open
// database" — there is no database, only a network client that could not
// reach or was refused by its remote server — while every other backend
// (Dolt, or a registered backend that is not Remote) keeps the original
// wording so this is additive.
func TestOpenStoreErrorMessageFramesRemoteBackends(t *testing.T) {
	const remoteName = "contract-remote-error"
	const localName = "contract-local-error"
	registerContractRemoteBackend(t, remoteName)
	registerContractBackend(t, localName)

	underlying := errors.New("dial tcp 127.0.0.1:1: connect: connection refused")

	got := openStoreErrorMessage(remoteName, underlying)
	if !strings.Contains(got, "remote backend") || !strings.Contains(got, remoteName) {
		t.Errorf("remote backend message = %q, want it to name the remote backend", got)
	}
	if strings.Contains(got, "failed to open database") {
		t.Errorf("remote backend message = %q, want no Dolt-shaped \"failed to open database\" framing", got)
	}
	if !strings.Contains(got, underlying.Error()) {
		t.Errorf("remote backend message = %q, want it to include the underlying error %v", got, underlying)
	}

	for _, name := range []string{localName, configfile.BackendDolt, "unregistered"} {
		got := openStoreErrorMessage(name, underlying)
		if !strings.Contains(got, "failed to open database") {
			t.Errorf("backend %q message = %q, want the original \"failed to open database\" framing", name, got)
		}
	}
}

// TestBackendNameForErrorFramingReadsMetadata covers direct mode's path to
// openStoreErrorMessage: it has no cfg in scope at the open call site, so it
// must load metadata.json itself to learn the configured backend name.
func TestBackendNameForErrorFramingReadsMetadata(t *testing.T) {
	const name = "contract-error-framing-metadata"
	registerContractRemoteBackend(t, name)
	beadsDir := writeContractBackendConfig(t, name)

	if got := backendNameForErrorFraming(beadsDir); got != name {
		t.Errorf("backendNameForErrorFraming(%q) = %q, want %q", beadsDir, got, name)
	}
	// A directory with no metadata.json at all must degrade to "" (plain
	// Dolt framing) rather than erroring.
	if got := backendNameForErrorFraming(t.TempDir()); got != "" {
		t.Errorf("backendNameForErrorFraming(no metadata.json) = %q, want \"\"", got)
	}
}
