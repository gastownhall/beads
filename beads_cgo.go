//go:build cgo

package beads

import (
	"context"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// OpenBestAvailable opens a beads database using the best available backend
// for the given .beads directory. It reads metadata.json to determine the
// configured mode:
//
//   - Embedded mode (default): Opens via the CGo embedded Dolt engine.
//   - Server mode: Connects to an external dolt sql-server via OpenFromConfig.
//
// The returned Storage must be closed when no longer needed.
//
// beadsDir is the path to the .beads directory.
func OpenBestAvailable(ctx context.Context, beadsDir string) (Storage, error) {
	cfg, err := configfile.Load(beadsDir)
	if err == nil && cfg != nil && cfg.IsDoltServerMode() {
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
