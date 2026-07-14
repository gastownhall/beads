package mysql

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
)

// NewFromConfig opens the MySQL backend for a workspace. It reads the base server DSN
// and per-workspace database from .beads/metadata.json, resolves the password through
// the credential ladder (command > env > credentials file, fail-closed) and places it into the DSN —
// never persisting it — then provisions and returns the store. It is the factory arm
// cmd/bd dispatches to when metadata.json has backend="mysql".
func NewFromConfig(ctx context.Context, beadsDir string) (storage.DoltStorage, error) {
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		return nil, fmt.Errorf("mysql: load config: %w", err)
	}
	dsn := cfg.GetMySQLDSN()
	if dsn == "" {
		return nil, fmt.Errorf("mysql: no DSN (set mysql_dsn in metadata.json, or BEADS_MYSQL_URL)")
	}
	dsn, err = resolveDSNCredential(ctx, cfg, dsn)
	if err != nil {
		return nil, fmt.Errorf("mysql: resolve credential: %w", err)
	}
	database := cfg.GetMySQLDatabase()
	if database == "" {
		return nil, fmt.Errorf("mysql: no database (set mysql_database in metadata.json)")
	}
	return Provision(ctx, dsn, database)
}

// Provision opens the MySQL backend from an explicit server DSN and workspace
// database: it creates the database if absent and applies the schema (InitSchema —
// idempotent, config seeds on first provision only), then returns the store over the
// per-workspace connection. baseDSN may carry a password; it is used only to connect
// and is never persisted. bd init calls this directly; NewFromConfig calls it after
// resolving DSN/database from metadata.
func Provision(ctx context.Context, baseDSN, database string) (storage.DoltStorage, error) {
	if baseDSN == "" {
		return nil, fmt.Errorf("mysql: empty DSN")
	}
	if database == "" {
		return nil, fmt.Errorf("mysql: empty database")
	}
	if err := InitSchema(ctx, baseDSN, database); err != nil {
		return nil, err
	}
	wsDSN, err := withDatabase(baseDSN, database)
	if err != nil {
		return nil, fmt.Errorf("mysql: parse DSN: %w", err)
	}
	return New(ctx, Config{DSN: wsDSN})
}
