package main

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// runDoltDropDatabaseProxied runs the named drop on the proxied server's
// maintenance connection, as clean-databases does.
func runDoltDropDatabaseProxied(ctx context.Context, beadsDir string, run func(context.Context, versioncontrolops.DBConn) error) error {
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
		return run(ctx, conn)
	})
}
