package main

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage/uow"
)

func runDoltCleanDatabasesProxied(ctx context.Context, beadsDir string, opts cleanDatabasesOptions) error {
	// Server-wide maintenance: every statement cleanDatabases issues (SHOW
	// DATABASES, DROP DATABASE, PURGE) is server-scoped, so the open neither
	// binds to nor creates the workspace's configured database. This is
	// deliberate (#5087 review): the configured database being dropped
	// server-side is precisely the broken state this command exists to clean
	// up from, so its open must not fail on (or recreate) that database.
	provider, err := newProxiedServerUOWProviderAdopting(ctx, beadsDir, "", uow.WithNoDatabaseBind())
	if err != nil {
		return HandleError("failed to open uow provider: %v", err)
	}
	defer func() { _ = provider.Close(ctx) }()

	mp, ok := provider.(uow.MaintenanceProvider)
	if !ok {
		return HandleError("proxied-server provider does not support maintenance operations")
	}

	return mp.RunNonTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		return cleanDatabases(ctx, conn, opts)
	})
}
