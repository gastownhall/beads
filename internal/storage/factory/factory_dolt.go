package factory

import (
	"context"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

func init() {
	RegisterBackend(configfile.BackendDolt, func(ctx context.Context, path string, opts Options) (storage.Storage, error) {
		store, err := dolt.New(ctx, &dolt.Config{
			Path:       path,
			Database:   opts.Database,
			ReadOnly:   opts.ReadOnly,
			ServerHost: opts.ServerHost,
			ServerPort: opts.ServerPort,
			ServerUser: opts.ServerUser,
		})
		if err != nil {
			return nil, err
		}
		return store, nil
	})
}
