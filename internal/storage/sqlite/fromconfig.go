package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
)

// NewFromConfig opens the SQLite backend for a workspace, reading the database file
// path from .beads/metadata.json (default beads.db, relative to the beads dir). SQLite
// is file-based, so there is no DSN password to manage.
func NewFromConfig(ctx context.Context, beadsDir string) (storage.DoltStorage, error) {
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		return nil, fmt.Errorf("sqlite: load config: %w", err)
	}
	path := cfg.GetSQLitePath()
	if path == "" {
		path = "beads.db"
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(beadsDir, path)
	}
	return Provision(ctx, path)
}

// Provision opens the SQLite database file, applies the schema (idempotent; config
// seeds on first provision), and returns the store. bd init calls this directly.
func Provision(ctx context.Context, dbPath string) (storage.DoltStorage, error) {
	if dbPath == "" {
		return nil, fmt.Errorf("sqlite: empty database path")
	}
	d := dsn(dbPath)
	// DDL and seeds are native SQLite; a raw modernc connection (no translation) runs
	// them. The store's own connection goes through the translating dialect.
	raw, err := sql.Open("sqlite", d)
	if err != nil {
		return nil, fmt.Errorf("sqlite: open (raw): %w", err)
	}
	if err := InitSchema(ctx, raw); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("sqlite: init schema: %w", err)
	}
	_ = raw.Close()
	return New(ctx, Config{DSN: d})
}
