//go:build cgo

package backend

import (
	"context"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

func openDolt(ctx context.Context, beadsDir string, cfg *configfile.Config, readOnly bool) (storage.DoltStorage, error) {
	if cfg.IsDoltServerMode() {
		return dolt.NewFromConfigWithOptions(ctx, beadsDir, &dolt.Config{ReadOnly: readOnly})
	}
	if readOnly {
		return embeddeddolt.OpenReadOnly(ctx, beadsDir, cfg.GetDoltDatabase(), "main")
	}
	return embeddeddolt.Open(ctx, beadsDir, cfg.GetDoltDatabase(), "main")
}
