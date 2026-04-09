//go:build !cgo

package beads

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// OpenBestAvailable opens a beads database using the best available backend
// for the given .beads directory. In non-CGO builds, only server mode is
// supported; embedded mode returns an error directing the user to server mode.
//
// beadsDir is the path to the .beads directory.
func OpenBestAvailable(ctx context.Context, beadsDir string) (Storage, embeddeddolt.Unlocker, error) {
	cfg, err := configfile.Load(beadsDir)
	if err == nil && cfg != nil && cfg.IsDoltServerMode() {
		store, err := dolt.NewFromConfig(ctx, beadsDir)
		if err != nil {
			return nil, nil, err
		}
		return store, embeddeddolt.NoopLock{}, nil
	}
	return nil, nil, fmt.Errorf("embedded Dolt requires CGO; use server mode (bd init --server)")
}
