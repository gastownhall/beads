//go:build !embeddeddolt

package main

import (
	"context"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

// newDoltStore creates a storage backend from an explicit config.
// Used by bd init and PersistentPreRun.
func newDoltStore(ctx context.Context, cfg *dolt.Config) (storage.DoltStorage, error) {
	return dolt.New(ctx, cfg)
}

// newDoltStoreFromConfig creates a storage backend from the beads directory's
// persisted metadata.json configuration. Used by direct_mode.go.
func newDoltStoreFromConfig(ctx context.Context, beadsDir string) (storage.DoltStorage, error) {
	return dolt.NewFromConfig(ctx, beadsDir)
}
