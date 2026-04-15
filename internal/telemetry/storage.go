package telemetry

import "github.com/steveyegge/beads/internal/storage"

// WrapStorage is an identity pass-through now that telemetry has been removed.
// It is kept so callers in cmd/bd need no changes.
func WrapStorage(s storage.Storage) storage.Storage {
	return s
}
