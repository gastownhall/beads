//go:build !cgo

package main

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// isEmbeddedMode returns false in non-CGO builds since embedded Dolt
// requires CGO. Only server mode is available.
func isEmbeddedMode() bool {
	return false
}

// newDoltStore creates a server-mode storage backend. Embedded Dolt is not
// available without CGO.
func newDoltStore(ctx context.Context, cfg *dolt.Config, _ ...embeddeddolt.Option) (storage.DoltStorage, error) {
	if !cfg.ServerMode {
		return nil, fmt.Errorf("embedded Dolt requires CGO; use server mode (bd init --server)")
	}
	return dolt.New(ctx, cfg)
}

// acquireEmbeddedLock returns a no-op lock in non-CGO builds.
func acquireEmbeddedLock(_ string, _ bool) (embeddeddolt.Unlocker, error) {
	return embeddeddolt.NoopLock{}, nil
}

// newDoltStoreFromConfig creates a server-mode storage backend from config.
func newDoltStoreFromConfig(ctx context.Context, beadsDir string) (storage.DoltStorage, error) {
	cfg, err := configfile.Load(beadsDir)
	if err == nil && cfg != nil && cfg.IsDoltServerMode() {
		return dolt.NewFromConfig(ctx, beadsDir)
	}
	return nil, fmt.Errorf("embedded Dolt requires CGO; use server mode (bd init --server)")
}

// newReadOnlyStoreFromConfig creates a read-only server-mode storage backend.
func newReadOnlyStoreFromConfig(ctx context.Context, beadsDir string) (storage.DoltStorage, error) {
	cfg, err := configfile.Load(beadsDir)
	if err == nil && cfg != nil && cfg.IsDoltServerMode() {
		return dolt.NewFromConfigWithOptions(ctx, beadsDir, &dolt.Config{ReadOnly: true})
	}
	return nil, fmt.Errorf("embedded Dolt requires CGO; use server mode (bd init --server)")
}
