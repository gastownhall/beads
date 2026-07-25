package main

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage/uow"
)

func runDoltCleanDatabasesProxied(ctx context.Context, beadsDir string, opts cleanDatabasesOptions) error {
	provider, err := newProxiedServerUOWProvider(ctx, beadsDir)
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
