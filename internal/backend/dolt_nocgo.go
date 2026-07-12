//go:build !cgo

package backend

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

func openDolt(ctx context.Context, beadsDir string, cfg *configfile.Config, readOnly bool) (storage.DoltStorage, error) {
	if !cfg.IsDoltServerMode() {
		return nil, fmt.Errorf("embedded Dolt requires CGO; use server mode (bd init --server)")
	}
	return dolt.NewFromConfigWithOptions(ctx, beadsDir, &dolt.Config{ReadOnly: readOnly})
}
