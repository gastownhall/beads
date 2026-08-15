package backends_test

import (
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/backends"
)

func TestRegisteredCustomNameIsSupportedAndPreserved(t *testing.T) {
	const name = "enterprise-fixture"
	cfg := &configfile.Config{Backend: name}

	if configfile.IsSupportedBackend(name) {
		t.Fatalf("unregistered backend %q unexpectedly supported", name)
	}
	if got := cfg.GetBackend(); got != configfile.BackendDolt {
		t.Fatalf("unregistered backend GetBackend() = %q, want Dolt fallback", got)
	}

	backends.Register(name, fixtureBackend())
	t.Cleanup(func() { backends.Deregister(name) })

	if !configfile.IsSupportedBackend(name) {
		t.Fatalf("registered backend %q is not supported", name)
	}
	if got := cfg.GetBackend(); got != name {
		t.Fatalf("registered backend GetBackend() = %q, want %q", got, name)
	}
}

func TestRegistrationCanActivateRemovedNameWithoutChangingOSSPolicy(t *testing.T) {
	const name = configfile.BackendPostgres
	cfg := &configfile.Config{Backend: name}

	if configfile.IsSupportedBackend(name) {
		t.Fatal("PostgreSQL tombstone must remain unsupported without a registrant")
	}
	if got := cfg.GetBackend(); got != name {
		t.Fatalf("PostgreSQL tombstone GetBackend() = %q, want %q", got, name)
	}

	backends.Register(name, fixtureBackend())
	t.Cleanup(func() { backends.Deregister(name) })

	if !configfile.IsSupportedBackend(name) {
		t.Fatal("registered PostgreSQL backend is not supported")
	}
	if got := cfg.GetBackend(); got != name {
		t.Fatalf("registered PostgreSQL GetBackend() = %q, want %q", got, name)
	}
}
