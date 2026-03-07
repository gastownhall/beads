//go:build embeddeddolt

package main

import (
	"context"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// newDoltStore creates an embedded Dolt storage backend.
// The dolt.Config is used only for BeadsDir and Database; server fields are ignored.
func newDoltStore(ctx context.Context, cfg *dolt.Config) (storage.DoltStorage, error) {
	return embeddeddolt.New(ctx, cfg.BeadsDir, cfg.Database, "main")
}

// newDoltStoreFromConfig creates an embedded Dolt storage backend from the
// beads directory's persisted metadata.json configuration.
func newDoltStoreFromConfig(ctx context.Context, beadsDir string) (storage.DoltStorage, error) {
	database := ""
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil {
		database = cfg.GetDoltDatabase()
	}
	return embeddeddolt.New(ctx, beadsDir, database, "main")
}
