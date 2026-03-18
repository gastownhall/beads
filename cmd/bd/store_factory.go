//go:build !embeddeddolt

package main

import (
	"context"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

const isEmbeddedDolt = false

// newDoltStore creates a storage backend from an explicit config.
// Used by bd init and PersistentPreRun.
func newDoltStore(ctx context.Context, cfg *dolt.Config) (storage.DoltStorage, error) {
	return dolt.New(ctx, cfg)
}

// acquireEmbeddedLock is a no-op in non-embedded builds. The server-mode dolt
// backend handles its own concurrency.
func acquireEmbeddedLock(_ string) (*embeddeddolt.Lock, error) {
	return &embeddeddolt.Lock{}, nil
}

// newDoltStoreCreateIfMissing creates a server-mode Dolt storage backend,
// creating the database if it doesn't exist.
func newDoltStoreCreateIfMissing(ctx context.Context, beadsDir, _ string) (storage.DoltStorage, error) {
	return dolt.NewFromConfigWithOptions(ctx, beadsDir, &dolt.Config{CreateIfMissing: true})
}

// newDoltStoreFromConfig creates a storage backend from the beads directory's
// persisted metadata.json configuration. Used by direct_mode.go.
func newDoltStoreFromConfig(ctx context.Context, beadsDir string) (storage.DoltStorage, error) {
	return dolt.NewFromConfig(ctx, beadsDir)
}
