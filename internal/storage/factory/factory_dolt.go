//go:build cgo

package factory

import (
	"context"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

func init() {
	RegisterBackend(configfile.BackendDolt, func(ctx context.Context, path string, opts Options) (storage.Storage, error) {
		return dolt.New(ctx, &dolt.Config{Path: path, ReadOnly: opts.ReadOnly})
	})
}
