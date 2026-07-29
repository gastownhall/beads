//go:build cgo

package beads

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// OpenBestAvailable opens a beads database using the best available backend
// for the given .beads directory. It reads metadata.json to determine the
// configured mode:
//
//   - Embedded Dolt (default): Opens via the CGo embedded Dolt engine.
//   - Dolt server: Connects to a dolt sql-server via OpenFromConfig.
//
// The returned Storage must be closed when no longer needed.
//
// beadsDir is the path to the .beads directory.
func OpenBestAvailable(ctx context.Context, beadsDir string) (Storage, error) {
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		return nil, fmt.Errorf("loading storage metadata: %w", err)
	}
	if cfg == nil {
		cfg = configfile.DefaultConfig()
	}
	if !configfile.IsSupportedBackend(cfg.Backend) {
		return nil, configuredBackendUnavailable(cfg.Backend)
	}

	if cfg.IsDoltServerMode() {
		store, err := dolt.NewFromConfig(ctx, beadsDir)
		if err != nil {
			return nil, err
		}
		return store, nil
	}

	database := configfile.DefaultDoltDatabase
	if cfg != nil {
		database = cfg.GetDoltDatabase()
	}
	store, err := embeddeddolt.Open(ctx, beadsDir, database, "main")
	if err != nil {
		return nil, err
	}
	return store, nil
}
