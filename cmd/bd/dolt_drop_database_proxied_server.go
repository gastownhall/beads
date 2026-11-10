package main

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage/uow"
)

// runDoltDropDatabaseProxied runs drop-database's one guarded DROP over the
// proxied bd server's connection. The guards and consent run on this side of
// the route (the proxied server is not trusted to re-derive them), exactly as
// runDoltCleanDatabasesProxied does for clean-databases.
func runDoltDropDatabaseProxied(ctx context.Context, beadsDir string, opts dropDatabaseOptions, guardIn dropDatabaseGuardInput) error {
	provider, err := newProxiedServerUOWProviderAdopting(ctx, beadsDir, "")
	if err != nil {
		return HandleError("failed to open uow provider: %v", err)
	}
	defer func() { _ = provider.Close(ctx) }()

	mp, ok := provider.(uow.MaintenanceProvider)
	if !ok {
		return HandleError("proxied-server provider does not support maintenance operations")
	}

	return mp.RunNonTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		return dropDatabase(ctx, conn, opts, guardIn)
	})
}
