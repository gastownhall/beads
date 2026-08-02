// Package backends provides the extension seam for storage backends that
// return storage.DoltStorage directly.
//
// OSS Beads registers no alternate backend. A downstream distribution adds a
// registrant package and blank-imports it from cmd/bd; shared validation,
// discovery, and store dispatch then recognize the name without modification.
// The Dolt family remains hand-wired because proxied mode returns a unit of
// work provider rather than a store.
//
// The seam covers opening and discovering an existing registered workspace, not
// provisioning one: bd init and bd bootstrap create or import Dolt only and
// reject registered names, so a downstream registrant supplies its own
// workspace-creation path.
//
// Registration is init-time wiring. A registrant calls Register once during
// process initialization, before any concurrent store access; OSS never
// deregisters, and Deregister exists only for isolated single-threaded contract
// tests. This registry and the backendnames set that config validation reads are
// updated together under this package's lock, so as long as callers honor the
// init-only rule the two never diverge. Do not Register or Deregister
// concurrently with store dispatch.
package backends

import (
	"context"
	"fmt"
	"sync"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/backendnames"
)

// Backend describes a registered storage backend.
type Backend struct {
	// Open opens the workspace store read-write.
	Open func(ctx context.Context, beadsDir string) (storage.DoltStorage, error)

	// OpenReadOnly opens the workspace for a read-only command.
	OpenReadOnly func(ctx context.Context, beadsDir string) (storage.DoltStorage, error)

	// WorkspaceIsBeadsDir means metadata.json and the remote store are enough
	// to identify the workspace; no separately discoverable local DB exists.
	WorkspaceIsBeadsDir bool
}

var (
	mu       sync.RWMutex
	registry = make(map[string]Backend)
)

// Register adds a backend. Invalid or duplicate registrations panic because
// they are process-start wiring errors.
func Register(name string, backend Backend) {
	switch {
	case name == "":
		panic("backends: Register called with empty backend name")
	case name == "dolt":
		panic(`backends: backend name "dolt" is reserved`)
	case backend.Open == nil || backend.OpenReadOnly == nil:
		panic(fmt.Sprintf("backends: backend %q requires Open and OpenReadOnly", name))
	}

	mu.Lock()
	defer mu.Unlock()
	if _, exists := registry[name]; exists {
		panic(fmt.Sprintf("backends: backend %q registered twice", name))
	}
	registry[name] = backend
	backendnames.Add(name)
}

// Deregister removes a backend. It exists for isolated contract tests;
// production registrants register once during process initialization.
func Deregister(name string) bool {
	mu.Lock()
	defer mu.Unlock()
	if _, exists := registry[name]; !exists {
		return false
	}
	delete(registry, name)
	backendnames.Remove(name)
	return true
}

// Lookup returns the backend registered under name.
func Lookup(name string) (Backend, bool) {
	mu.RLock()
	defer mu.RUnlock()
	backend, ok := registry[name]
	return backend, ok
}

// Registered reports whether name has a backend implementation.
func Registered(name string) bool {
	mu.RLock()
	defer mu.RUnlock()
	_, ok := registry[name]
	return ok
}

// WorkspaceIsBeadsDir reports whether name is a registered backend whose
// workspace is identified by the .beads directory alone — metadata.json plus a
// remote store — with no separately discoverable local database. It is the
// single authority for that classification so CLI and library discovery route
// such workspaces to the .beads directory instead of returning "no database".
func WorkspaceIsBeadsDir(name string) bool {
	mu.RLock()
	defer mu.RUnlock()
	backend, ok := registry[name]
	return ok && backend.WorkspaceIsBeadsDir
}
