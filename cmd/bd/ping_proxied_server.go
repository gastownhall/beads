package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/ui"
)

func runPingProxiedServer(ctx context.Context) error {
	evt := metrics.NewCommandEvent("ping")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	start := time.Now()

	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return pingFail(start, "no .beads directory found")
	}
	resolveMs := time.Since(start).Milliseconds()

	if uowProvider == nil {
		return pingFail(start, "proxied-server UOW provider not initialized")
	}
	storeMs := time.Since(start).Milliseconds()

	_, err := uow.RunTxRead(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (*domain.RawSQLResult, error) {
		return uw.RawSQLUseCase().Query(ctx, "SELECT 1")
	})
	if err != nil {
		return pingFail(start, fmt.Sprintf("query failed: %v", err))
	}
	totalMs := time.Since(start).Milliseconds()
	queryMs := totalMs - storeMs

	if jsonOutput {
		return outputJSON(map[string]interface{}{
			"status":     "ok",
			"resolve_ms": resolveMs,
			"store_ms":   storeMs - resolveMs,
			"query_ms":   queryMs,
			"total_ms":   totalMs,
		})
	}

	fmt.Fprintf(os.Stdout, "%s bd ping: ok (%dms)\n", ui.RenderPass("✓"), totalMs)
	return nil
}
