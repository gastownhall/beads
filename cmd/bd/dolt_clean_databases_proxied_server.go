package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage/uow"
)

func runDoltCleanDatabasesProxied(ctx context.Context, beadsDir string, dryRun bool) error {
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
		listCtx, listCancel := context.WithTimeout(ctx, 30*time.Second)
		defer listCancel()

		rows, err := conn.QueryContext(listCtx, "SHOW DATABASES")
		if err != nil {
			return HandleError("listing databases: %v", err)
		}
		defer rows.Close()

		var stale []string
		for rows.Next() {
			var dbName string
			if err := rows.Scan(&dbName); err != nil {
				return HandleError("scanning database name: %v", err)
			}
			for _, prefix := range staleDatabasePrefixes {
				if strings.HasPrefix(dbName, prefix) {
					stale = append(stale, dbName)
					break
				}
			}
		}
		if err := rows.Err(); err != nil {
			return HandleError("listing databases: %v", err)
		}

		if len(stale) == 0 {
			fmt.Println("No stale databases found.")
			return nil
		}

		fmt.Printf("Found %d stale databases:\n", len(stale))
		for _, name := range stale {
			fmt.Printf("  %s\n", name)
		}

		if dryRun {
			fmt.Println("\n(dry run — no databases dropped)")
			return nil
		}

		fmt.Println()
		dropped := 0
		for _, name := range stale {
			dropCtx, dropCancel := context.WithTimeout(ctx, 30*time.Second)
			safeName := strings.ReplaceAll(name, "`", "``")
			_, err := conn.ExecContext(dropCtx, fmt.Sprintf("DROP DATABASE `%s`", safeName)) //nolint:gosec // G201: identifier-escaped
			dropCancel()
			if err != nil {
				fmt.Fprintf(os.Stderr, "  FAIL: %s: %v\n", name, err)
			} else {
				fmt.Printf("  Dropped: %s\n", name)
				dropped++
			}
		}
		fmt.Printf("\nDropped %d/%d stale databases.\n", dropped, len(stale))
		return nil
	})
}
