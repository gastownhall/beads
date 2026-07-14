package postgres

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/pgdialect"
)

// NewFromConfig opens the Postgres backend for a workspace. It reads the base DSN
// and per-workspace schema from .beads/metadata.json, resolves the password through
// the credential ladder (command > env > credentials file, fail-closed) and places
// it into the DSN — never persisting it — then applies the schema over a raw
// non-translating connection and returns the store. This is the factory arm cmd/bd
// dispatches to when metadata.json has backend="postgres".
func NewFromConfig(ctx context.Context, beadsDir string) (storage.DoltStorage, error) {
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		return nil, fmt.Errorf("postgres: load config: %w", err)
	}
	dsn := cfg.GetPostgresDSN()
	if dsn == "" {
		return nil, fmt.Errorf("postgres: no DSN (set postgres_dsn in metadata.json, or BEADS_POSTGRES_URL)")
	}
	dsn, err = resolveDSNCredential(ctx, cfg, dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: resolve credential: %w", err)
	}
	schema := cfg.GetPostgresSchema()
	if schema == "" {
		return nil, fmt.Errorf("postgres: no schema (set postgres_schema in metadata.json)")
	}
	return Provision(ctx, dsn, schema)
}

// Provision opens the Postgres backend from an explicit DSN and schema: it applies
// the schema (InitSchema — create-schema, DDL, config seeds, version stamp; all
// idempotent) over a raw non-translating connection, then returns the store over the
// translating driver. dsn may carry a password; it is used only to connect and is
// never persisted. `bd init` calls this directly (before metadata.json exists, using
// the flag-provided DSN); NewFromConfig calls it after resolving DSN/schema from
// metadata. DDL runs raw because the translating driver would mangle the $$-quoted
// function bodies and treat DDL as workload SQL.
func Provision(ctx context.Context, dsn, schema string) (storage.DoltStorage, error) {
	if dsn == "" {
		return nil, fmt.Errorf("postgres: empty DSN")
	}
	if schema == "" {
		return nil, fmt.Errorf("postgres: empty schema")
	}
	raw, err := pgdialect.OpenRaw(dsn, schema)
	if err != nil {
		return nil, fmt.Errorf("postgres: open (raw): %w", err)
	}
	if err := InitSchema(ctx, raw, schema); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("postgres: init schema: %w", err)
	}
	// Byte-order parity with Dolt depends on the database collation (text columns
	// use the DB default; per-column COLLATE "C" breaks the shared recursive CTEs).
	// Warn loudly when it is not code-point ordered, at init and on every open.
	warnOnCollationDivergence(ctx, raw)
	_ = raw.Close()

	return New(ctx, Config{DSN: dsn, Schema: schema})
}
