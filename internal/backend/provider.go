package backend

import (
	"context"
	"fmt"
	"sync"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
)

// Capabilities describes optional behavior exposed by a backend provider.
//
// Keep this small: command code should use capabilities to decide whether a
// backend-specific operation is available without knowing which backend is in
// use.
type Capabilities struct {
	Embedded          bool
	Transactions      bool
	RawSQL            bool
	Leases            bool
	Maintenance       bool
	Versioning        bool
	Branching         bool
	DoltRemotes       bool
	ConcurrentWriters bool
}

// OpenOptions is the backend-neutral open request used by configured stores.
type OpenOptions struct {
	BeadsDir        string
	Database        string
	Branch          string
	ServerMode      bool
	ProxiedServer   bool
	ReadOnly        bool
	ReadOnlyCommand bool
	LenientOpen     bool
}

// Descriptor describes the backend selected for a configured open. It exposes
// backend-neutral behavior only; concrete driver and connection types remain
// behind the storage boundary.
type Descriptor struct {
	Name         string
	External     bool
	Capabilities Capabilities
}

// ConfiguredOpenOptions controls behavior that must be chosen by the caller
// rather than inferred from workspace metadata.
type ConfiguredOpenOptions struct {
	ReadOnly        bool
	ReadOnlyCommand bool
	LenientOpen     bool
}

// Provider is the core seam for built-in and future plugin-shaped backends.
type Provider interface {
	Name() string
	Capabilities(*configfile.Config) Capabilities
	Open(context.Context, OpenOptions) (storage.DoltStorage, error)
}

var registry = struct {
	sync.RWMutex
	providers map[string]Provider
}{providers: make(map[string]Provider)}

// Register adds a provider. It panics for invalid duplicate registrations so
// built-in provider mistakes fail during startup.
func Register(provider Provider) {
	if provider == nil {
		panic("backend: nil provider")
	}
	name := provider.Name()
	if name == "" {
		panic("backend: provider with empty name")
	}
	registry.Lock()
	defer registry.Unlock()
	if _, exists := registry.providers[name]; exists {
		panic("backend: duplicate provider " + name)
	}
	registry.providers[name] = provider
}

func Lookup(name string) (Provider, bool) {
	registry.RLock()
	defer registry.RUnlock()
	provider, ok := registry.providers[name]
	return provider, ok
}

func MustLookup(name string) (Provider, error) {
	provider, ok := Lookup(name)
	if !ok {
		return nil, fmt.Errorf("unknown backend %q", name)
	}
	return provider, nil
}
