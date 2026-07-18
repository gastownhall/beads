package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
)

// NewFromConfig opens the SQLite backend for a workspace, reading the database file
// path from .beads/metadata.json (default beads.db, relative to the beads dir). SQLite
// is file-based, so there is no DSN password to manage.
//
// Guard (bd-oyvc2.7): when the SQLite file does not exist yet but the workspace
// already contains a Dolt database on disk, this refuses instead of provisioning.
// Silently creating a fresh empty SQLite database next to live Dolt data makes every
// issue vanish from view (false-empty) and divorces new writes from the real store.
// Fresh provisioning outside `bd init` is only legitimate when there is no
// pre-existing Dolt data to shadow (e.g. a fresh clone of a SQLite-backed workspace).
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
	if _, statErr := os.Stat(path); statErr != nil {
		if !os.IsNotExist(statErr) {
			return nil, fmt.Errorf("sqlite: stat %s: %w", path, statErr)
		}
		if doltDir := existingDoltDataDir(beadsDir, cfg); doltDir != "" {
			return nil, fmt.Errorf("sqlite: metadata.json selects the SQLite backend but %s does not exist, "+
				"and this workspace already has a Dolt database at %s; refusing to create a fresh empty "+
				"SQLite database next to existing Dolt data (bd-oyvc2.7).\n"+
				"  To keep using Dolt: remove the \"backend\" and \"sqlite_path\" fields from %s.\n"+
				"  To switch to SQLite: run 'bd init --backend=sqlite --reinit-local' (see 'bd help init-safety')",
				path, doltDir, configfile.ConfigPath(beadsDir))
		}
	}
	return Provision(ctx, path)
}

// existingDoltDataDir returns the path of a Dolt database directory already present
// in the workspace, or "" when none exists. It checks the embedded layout
// (.beads/embeddeddolt/<db>/.dolt) and the server-mode data dir (default .beads/dolt,
// or the configured dolt_data_dir), where databases live either directly
// (<dir>/.dolt) or one level down (<dir>/<db>/.dolt).
func existingDoltDataDir(beadsDir string, cfg *configfile.Config) string {
	roots := []string{filepath.Join(beadsDir, "embeddeddolt")}
	doltDir := filepath.Join(beadsDir, "dolt")
	if custom := cfg.GetDoltDataDir(); custom != "" {
		if filepath.IsAbs(custom) {
			doltDir = custom
		} else {
			doltDir = filepath.Join(beadsDir, custom)
		}
	}
	roots = append(roots, doltDir)

	for _, root := range roots {
		if info, err := os.Stat(filepath.Join(root, ".dolt")); err == nil && info.IsDir() {
			return root
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			if info, err := os.Stat(filepath.Join(root, entry.Name(), ".dolt")); err == nil && info.IsDir() {
				return filepath.Join(root, entry.Name())
			}
		}
	}
	return ""
}

// Provision opens the SQLite database file, applies the schema (idempotent; config
// seeds on first provision), and returns the store. bd init calls this directly —
// init is the one place where creating a fresh database over an existing workspace
// is a deliberate, guarded choice (init has its own existing-data safety checks).
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
