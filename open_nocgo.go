//go:build !cgo

package beads

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/configfile"
)

// OpenBestAvailable opens a beads database using the best available backend.
// In non-CGO builds, the embedded Dolt engine is not available. If dolt_mode
// is "server", it delegates to OpenFromConfig. Otherwise it returns a clear
// error directing the caller to rebuild with CGO_ENABLED=1.
//
// beadsDir is the path to the .beads directory.
func OpenBestAvailable(ctx context.Context, beadsDir string) (Storage, error) {
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		return nil, err
	}
	if cfg != nil && !cfg.IsDoltServerMode() {
		return nil, fmt.Errorf("beads: OpenBestAvailable requires CGO for embedded mode (dolt_mode=%q); rebuild with CGO_ENABLED=1 or set dolt_mode=server", cfg.GetDoltMode())
	}
	// cfg is nil (no metadata.json) — embedded is the default, CGO required.
	if cfg == nil {
		return nil, fmt.Errorf("beads: OpenBestAvailable requires CGO for embedded mode (dolt_mode=%q); rebuild with CGO_ENABLED=1 or set dolt_mode=server", "embedded")
	}
	return OpenFromConfig(ctx, beadsDir)
}
