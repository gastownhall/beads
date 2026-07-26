package main

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

func runCompactDoltProxiedServer(ctx context.Context) error {
	start := time.Now()

	if !compactDryRun {
		CheckReadonly("compact")
	}

	if compactDryRun {
		if jsonOutput {
			return outputJSON(map[string]interface{}{
				"dry_run": true,
			})
		}
		fmt.Printf("DRY RUN - Dolt garbage collection\n\n")
		fmt.Printf("Run without --dry-run to perform garbage collection.\n")
		return nil
	}

	if !jsonOutput {
		fmt.Printf("Running Dolt garbage collection...\n")
	}

	err := runProxiedNonTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		return versioncontrolops.DoltGC(ctx, conn)
	})
	if err != nil {
		return HandleErrorRespectJSON("dolt gc failed: %v", err)
	}

	elapsed := time.Since(start)

	if jsonOutput {
		return outputJSON(map[string]interface{}{
			"success":    true,
			"elapsed_ms": elapsed.Milliseconds(),
		})
	}

	fmt.Printf("✓ Dolt garbage collection complete\n")
	fmt.Printf("  Time: %v\n", elapsed)
	return nil
}
