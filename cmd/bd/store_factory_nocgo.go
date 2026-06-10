//go:build !cgo

package main

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dbproxy/util"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

func usesSQLServer() bool {
	return true
}

// isEmbeddedMode reports whether the command is using embedded Dolt storage.
func isEmbeddedMode() bool {
	return false
}

func usesProxiedServer() bool {
	if shouldUseGlobals() {
		return proxiedServerMode
	}
	return cmdCtx != nil && cmdCtx.ProxiedServerMode
}

func newDoltStore(ctx context.Context, cfg *dolt.Config) (storage.DoltStorage, error) {
	if cfg.ProxiedServer {
		// TODO: this should not be a store
		// it should be a uow provider
		return nil, fmt.Errorf("proxy server store should be uow provider")
	}
	if !cfg.ServerMode {
		return nil, fmt.Errorf("%s", nocgoEmbeddedErrMsg)
	}
	return dolt.New(ctx, cfg)
}

// acquireEmbeddedLock returns a no-op lock in non-CGO builds.
func acquireEmbeddedLock(_ string, _ bool) (util.Unlocker, error) {
	return util.NoopLock{}, nil
}

// newDoltStoreFromConfig creates a SQL-server-backed storage backend from config.
func newDoltStoreFromConfig(ctx context.Context, beadsDir string) (storage.DoltStorage, error) {
	cfg, err := configfile.Load(beadsDir)
	if err == nil && cfg != nil && cfg.IsDoltProxiedServerMode() {
		// Proxied-server workspaces have no classic store backend; they are
		// served through the UOW provider by commands with a proxied
		// dispatch path.
		return nil, fmt.Errorf("workspace %s uses dolt proxied-server mode, which cannot be opened as a classic store; only commands with proxied-server support can use it", beadsDir)
	}
	if err == nil && cfg != nil && cfg.IsDoltServerMode() {
		return dolt.NewFromConfig(ctx, beadsDir)
	}
	return nil, fmt.Errorf("%s", nocgoEmbeddedErrMsg)
}

// newReadOnlyStoreFromConfig creates a read-only SQL-server-backed storage backend.
func newReadOnlyStoreFromConfig(ctx context.Context, beadsDir string) (storage.DoltStorage, error) {
	cfg, err := configfile.Load(beadsDir)
	if err == nil && cfg != nil && cfg.IsDoltProxiedServerMode() {
		// Proxied-server workspaces have no classic store backend (see
		// newDoltStoreFromConfig); read-only cross-repo opens hit this too.
		return nil, fmt.Errorf("workspace %s uses dolt proxied-server mode, which cannot be opened as a classic store; only commands with proxied-server support can use it", beadsDir)
	}
	if err == nil && cfg != nil && cfg.IsDoltServerMode() {
		return dolt.NewFromConfigWithOptions(ctx, beadsDir, &dolt.Config{ReadOnly: true})
	}
	return nil, fmt.Errorf("%s", nocgoEmbeddedErrMsg)
}

// newWritableRoutedStoreFromConfig opens a writable target store for routed
// writes without running schema migrations. No-CGO variant: server mode only.
func newWritableRoutedStoreFromConfig(ctx context.Context, beadsDir string) (storage.DoltStorage, error) {
	cfg, err := configfile.Load(beadsDir)
	if err == nil && cfg != nil && cfg.IsDoltProxiedServerMode() {
		return nil, fmt.Errorf("proxy server store needs to be uow provider")
	}
	if err == nil && cfg != nil && cfg.IsDoltServerMode() {
		return dolt.NewFromConfigWithOptions(ctx, beadsDir, &dolt.Config{})
	}
	return nil, fmt.Errorf("%s", nocgoEmbeddedErrMsg)
}

const nocgoEmbeddedErrMsg = `embedded Dolt requires a CGO build, but this bd binary was built with CGO_ENABLED=0.

Three options:

  1. Use the proxied dolt sql-server (no external server, no reinstall):
       bd init --proxied-server
     bd spawns a per-workspace proxy + child dolt sql-server under
     .beads/proxieddb/ and manages their lifecycle for you.

  2. Use external server mode (no reinstall needed):
       bd init --server
     Requires a running 'dolt sql-server'. See docs/DOLT.md.

  3. Reinstall with embedded-mode support:
       brew install beads                              # macOS / Linux
       npm install -g @beads/bd                        # any platform with Node
       curl -fsSL https://raw.githubusercontent.com/steveyegge/beads/main/scripts/install.sh | bash

See docs/INSTALLING.md for the full comparison.`
