package main

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage/uow"
)

func runProxiedNonTx(ctx context.Context, fn func(ctx context.Context, conn *sql.Conn) error) error {
	if uowProvider == nil {
		return HandleErrorRespectJSON("proxied-server UOW provider not initialized")
	}
	mp, ok := uowProvider.(uow.MaintenanceProvider)
	if !ok {
		// The %T is the whole diagnostic: every wrapper in the proxied chain
		// forwards RunNonTx with a compile-time assertion, so reaching this
		// means a new decorator dropped it, and its type names the culprit.
		return HandleErrorRespectJSON("proxied-server provider %T does not support maintenance operations", uowProvider)
	}
	return mp.RunNonTx(ctx, fn)
}
