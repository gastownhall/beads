//go:build embeddeddolt

package main

import (
	"context"
	"path/filepath"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

const isEmbeddedDolt = true

// newDoltStore creates an embedded Dolt storage backend.
// The dolt.Config is used only for BeadsDir and Database; server fields are ignored.
func newDoltStore(ctx context.Context, cfg *dolt.Config) (storage.DoltStorage, error) {
	return embeddeddolt.New(ctx, cfg.BeadsDir, cfg.Database, "main")
}

// acquireEmbeddedLock acquires an exclusive flock on the embeddeddolt data
// directory derived from beadsDir. The caller must defer lock.Unlock().
// Used by commands that require single-writer access (e.g., bd init).
func acquireEmbeddedLock(beadsDir string) (*embeddeddolt.Lock, error) {
	dataDir := filepath.Join(beadsDir, "embeddeddolt")
	return embeddeddolt.TryLock(dataDir)
}

// newDoltStoreCreateIfMissing creates an embedded Dolt storage backend,
// writing a minimal metadata.json if the beads directory has no configuration.
// database is used as both the Dolt database name and the fallback for metadata.json.
func newDoltStoreCreateIfMissing(ctx context.Context, beadsDir, database string) (storage.DoltStorage, error) {
	return embeddeddolt.NewWithOptions(ctx, beadsDir, database, "main", &embeddeddolt.Options{
		CreateIfMissing: true,
	})
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
