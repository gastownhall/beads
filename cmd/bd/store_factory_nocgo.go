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
func newDoltStore(ctx context.Context, cfg *dolt.Config) (storage.DoltStorage, error) {
	if !cfg.ServerMode {
		return nil, fmt.Errorf("%s", nocgoEmbeddedErrMsg)
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
	return nil, fmt.Errorf("%s", nocgoEmbeddedErrMsg)
}

// newReadOnlyStoreFromConfig creates a read-only server-mode storage backend.
func newReadOnlyStoreFromConfig(ctx context.Context, beadsDir string) (storage.DoltStorage, error) {
	cfg, err := configfile.Load(beadsDir)
	if err == nil && cfg != nil && cfg.IsDoltServerMode() {
		return dolt.NewFromConfigWithOptions(ctx, beadsDir, &dolt.Config{ReadOnly: true})
	}
	return nil, fmt.Errorf("%s", nocgoEmbeddedErrMsg)
}

// nocgoEmbeddedErrMsg guides the user either to server mode (no rebuild
// needed) or to an embedded-capable install path. It intentionally enumerates
// the canonical install paths so users don't have to hunt through docs.
const nocgoEmbeddedErrMsg = `embedded Dolt requires a CGO build, but this bd binary was built with CGO_ENABLED=0.

Two options:

  1. Use server mode (no reinstall needed):
       bd init --server
     Requires a running 'dolt sql-server'. See docs/DOLT.md.

  2. Reinstall with embedded-mode support:
       brew install beads                              # macOS / Linux
       npm install -g @beads/bd                        # any platform with Node
       curl -fsSL https://raw.githubusercontent.com/steveyegge/beads/main/scripts/install.sh | bash

See docs/INSTALLING.md for the full comparison.`
