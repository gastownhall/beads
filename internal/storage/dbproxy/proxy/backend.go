package proxy

import (
	"errors"
	"fmt"
	"strings"
)

// Backend identifies which DatabaseServer implementation the proxy fronts.
// The proxy parent passes Backend across the fork+exec boundary as the
// --backend flag value; the child re-types it on the way in and dispatches
// to a concrete server.DatabaseServer.
type Backend string

const (
	BackendExternal          Backend = "external"
	BackendLocalServer       Backend = "local-server"
	BackendLocalSharedServer Backend = "local-shared-server"
)

// knownBackends is the dispatchable set, ordered for stable error messages
// and CLI help text.
var knownBackends = []Backend{
	BackendExternal,
	BackendLocalServer,
	BackendLocalSharedServer,
}

func (b Backend) String() string { return string(b) }

// Valid reports whether b is one of the recognized constants.
func (b Backend) Valid() bool {
	for _, k := range knownBackends {
		if b == k {
			return true
		}
	}
	return false
}

// Validate returns nil if b is non-empty and recognized; otherwise it
// returns a descriptive error listing the supported set.
func (b Backend) Validate() error {
	if b == "" {
		return errors.New("backend must be set")
	}
	if !b.Valid() {
		return fmt.Errorf("unknown backend %q (want one of: %s)", string(b), strings.Join(KnownBackendNames(), ", "))
	}
	return nil
}

// KnownBackendNames returns the recognized backend identifiers as strings,
// in display order. Useful for CLI help text and validation error messages.
func KnownBackendNames() []string {
	out := make([]string, len(knownBackends))
	for i, k := range knownBackends {
		out[i] = string(k)
	}
	return out
}
