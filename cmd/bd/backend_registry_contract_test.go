package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

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

// registerContractBlockingBackend registers a backend whose opens block until
// ctx is done and then fail with an error wrapping ctx.Err() — the shape of a
// dial that never completes — so a test can see whether a store-open timeout
// survives the CLI's error framing.
func registerContractBlockingBackend(t *testing.T, name string, remote bool) {
	t.Helper()
	open := func(ctx context.Context, _ string) (storage.DoltStorage, error) {
		<-ctx.Done()
		return nil, fmt.Errorf("dial contract backend: %w", ctx.Err())
	}
	backends.Register(name, backends.Backend{
		Open:                open,
		OpenReadOnly:        open,
		WorkspaceIsBeadsDir: true,
		Remote:              remote,
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

// TestOpenStoreErrorFramesRemoteBackends is the M1 fix: a registered Remote
// backend's open failure must not be announced as "failed to open database" —
// there is no database, only a network client that could not reach or was
// refused by its remote server — while every other backend (Dolt, or a
// registered backend that is not Remote) keeps the original wording so this
// is additive. Every framing must keep the underlying error in the chain.
func TestOpenStoreErrorFramesRemoteBackends(t *testing.T) {
	const remoteName = "contract-remote-error"
	const localName = "contract-local-error"
	registerContractRemoteBackend(t, remoteName)
	registerContractBackend(t, localName)

	underlying := errors.New("dial tcp 127.0.0.1:1: connect: connection refused")

	got := openStoreError(remoteName, underlying)
	msg := got.Error()
	if !strings.Contains(msg, "remote backend") || !strings.Contains(msg, remoteName) {
		t.Errorf("remote backend message = %q, want it to name the remote backend", msg)
	}
	if strings.Contains(msg, "failed to open database") {
		t.Errorf("remote backend message = %q, want no Dolt-shaped \"failed to open database\" framing", msg)
	}
	if !strings.Contains(msg, underlying.Error()) {
		t.Errorf("remote backend message = %q, want it to include the underlying error %v", msg, underlying)
	}
	if !errors.Is(got, underlying) {
		t.Errorf("remote backend error = %v, want it to wrap the underlying error %v", got, underlying)
	}

	for _, name := range []string{localName, configfile.BackendDolt, "unregistered"} {
		got := openStoreError(name, underlying)
		if want := "failed to open database: " + underlying.Error(); got.Error() != want {
			t.Errorf("backend %q message = %q, want the original framing %q", name, got, want)
		}
		if !errors.Is(got, underlying) {
			t.Errorf("backend %q error = %v, want it to wrap the underlying error %v", name, got, underlying)
		}
	}
}

// TestEnsureStoreActiveKeepsOpenErrorChain drives the real
// ensureStoreActiveWithContext (prime's stubbed timeout test cannot see its
// framing) against a registered backend whose open outlives its context. The
// remote-aware framing must still wrap the open error: prime tells a
// store-open timeout apart from a generic "storage unavailable" only through
// errors.Is(err, context.DeadlineExceeded), so a framing that flattens the
// chain into text silently downgrades that diagnostic for every backend.
func TestEnsureStoreActiveKeepsOpenErrorChain(t *testing.T) {
	tests := []struct {
		name       string
		backend    string
		remote     bool
		wantPrefix string
	}{
		{
			name:       "local",
			backend:    "contract-open-chain-local",
			wantPrefix: "failed to open database: ",
		},
		{
			name:       "remote",
			backend:    "contract-open-chain-remote",
			remote:     true,
			wantPrefix: `failed to reach remote backend "contract-open-chain-remote": `,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registerContractBlockingBackend(t, tt.backend, tt.remote)
			t.Setenv("BEADS_DIR", writeContractBackendConfig(t, tt.backend))
			oldStore, oldStoreActive := store, storeActive
			store, storeActive = nil, false
			t.Cleanup(func() { store, storeActive = oldStore, oldStoreActive })

			ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
			defer cancel()
			err := ensureStoreActiveWithContext(ctx)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("ensureStoreActiveWithContext() error = %v, want it to wrap context.DeadlineExceeded", err)
			}
			if msg := err.Error(); !strings.HasPrefix(msg, tt.wantPrefix) || !strings.Contains(msg, "\nHint: ") {
				t.Errorf("ensureStoreActiveWithContext() error = %q, want prefix %q and a Hint line", msg, tt.wantPrefix)
			}
		})
	}
}

// TestBackendNameForErrorFramingReadsMetadata covers direct mode's path to
// openStoreError: it has no cfg in scope at the open call site, so it must
// load metadata.json itself to learn the configured backend name.
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
