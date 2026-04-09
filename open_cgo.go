//go:build cgo

package beads

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// OpenBestAvailable opens a beads database using the best available backend,
// routing based on the dolt_mode setting in metadata.json.
//
// If dolt_mode is "server" (or environment overrides indicate server mode),
// it delegates to OpenFromConfig which connects to the external Dolt SQL server.
// Otherwise (the default for standalone repos), it uses the embedded Dolt engine
// directly — the same backend that bd itself uses.
//
// beadsDir is the path to the .beads directory.
func OpenBestAvailable(ctx context.Context, beadsDir string) (Storage, error) {
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		return nil, err
	}
	if cfg != nil && cfg.IsDoltServerMode() {
		return OpenFromConfig(ctx, beadsDir)
	}

	database := configfile.DefaultDoltDatabase
	if cfg != nil {
		database = cfg.GetDoltDatabase()
	}

	lock, err := embeddeddolt.TryLock(filepath.Join(beadsDir, "embeddeddolt"))
	if err != nil {
		return nil, fmt.Errorf("beads: cannot acquire embedded Dolt lock (is bd running?): %w", err)
	}
	return embeddeddolt.New(ctx, beadsDir, database, "main", embeddeddolt.WithLock(lock))
}
