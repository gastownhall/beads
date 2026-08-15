package backends_test

import (
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/backends"
)

func TestDeregisterRestoresUnknownBackendFailClosed(t *testing.T) {
	const name = "transient-fixture"
	cfg := &configfile.Config{Backend: name}

	backends.Register(name, fixtureBackend())
	t.Cleanup(func() { backends.Deregister(name) })
	if !configfile.IsSupportedBackend(name) {
		t.Fatalf("registered backend %q is not supported", name)
	}
	if got := cfg.GetBackend(); got != name {
		t.Fatalf("registered backend GetBackend() = %q, want %q", got, name)
	}

	if !backends.Deregister(name) {
		t.Fatalf("Deregister(%q) = false, want true", name)
	}
	if configfile.IsSupportedBackend(name) {
		t.Fatalf("deregistered backend %q is still supported", name)
	}
	if got := cfg.GetBackend(); got != configfile.BackendDolt {
		t.Fatalf("deregistered backend GetBackend() = %q, want fail-safe Dolt fallback", got)
	}
}
