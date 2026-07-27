package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

type cleanDatabasesOptions struct {
	dryRun       bool
	purgeDropped bool
}

const (
	cleanDatabasesListTimeout  = 30 * time.Second
	cleanDatabasesDropTimeout  = 30 * time.Second
	cleanDatabasesBatchSize    = 5
	cleanDatabasesBatchPause   = 2 * time.Second
	cleanDatabasesBackoffPause = 10 * time.Second
	cleanDatabasesTimeoutTrip  = 3
	cleanDatabasesMaxFailures  = 10
)

func listStaleDatabases(ctx context.Context, conn versioncontrolops.DBConn) ([]string, error) {
	listCtx, cancel := context.WithTimeout(ctx, cleanDatabasesListTimeout)
	defer cancel()

	rows, err := conn.QueryContext(listCtx, "SHOW DATABASES")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var stale []string
	for rows.Next() {
		var dbName string
		if err := rows.Scan(&dbName); err != nil {
			return nil, err
		}
		for _, prefix := range staleDatabasePrefixes {
			if strings.HasPrefix(dbName, prefix) {
				stale = append(stale, dbName)
				break
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return stale, nil
}

func cleanDatabases(ctx context.Context, conn versioncontrolops.DBConn, opts cleanDatabasesOptions) error {
	stale, err := listStaleDatabases(ctx, conn)
	if err != nil {
		return HandleError("listing databases: %v", err)
	}

	if len(stale) == 0 {
		fmt.Println("No stale databases found.")
	} else {
		fmt.Printf("Found %d stale databases:\n", len(stale))
		for _, name := range stale {
			fmt.Printf("  %s\n", name)
		}
	}

	if opts.dryRun {
		if len(stale) > 0 {
			fmt.Println("\n(dry run — no databases dropped)")
		}
		if opts.purgeDropped {
			fmt.Println("(dry run — --purge-dropped ignored; no purge performed)")
		}
		return nil
	}

	dropped, err := dropStaleDatabases(ctx, conn, stale)
	if err != nil {
		return err
	}

	fmt.Println()
	if shouldPurgeDroppedDatabases(opts.purgeDropped, dropped) {
		if err := purgeDroppedDatabases(ctx, conn); err != nil {
			fmt.Fprintf(os.Stderr, "  WARN: PURGE_DROPPED_DATABASES failed: %v\n", err)
			fmt.Fprintln(os.Stderr, "  Try `dolt sql -q 'CALL DOLT_PURGE_DROPPED_DATABASES()'`.")
		} else {
			fmt.Println("Purged all dropped databases on this server (server-global, irreversible —")
			fmt.Println("CALL DOLT_UNDROP is no longer available for any of them, not just this run's).")
		}
	} else {
		fmt.Println("Dropped databases remain recoverable via `CALL DOLT_UNDROP(name)` until purged.")
		fmt.Println("Pass --purge-dropped to permanently reclaim their disk. This purges ALL dropped")
		fmt.Println("databases on the server (server-global), not just the ones from this run.")
	}
	return nil
}

func dropStaleDatabases(ctx context.Context, conn versioncontrolops.DBConn, stale []string) (int, error) {
	if len(stale) == 0 {
		return 0, nil
	}

	fmt.Println()
	dropped := 0
	failures := 0
	consecutiveTimeouts := 0

	for i, name := range stale {
		if consecutiveTimeouts >= cleanDatabasesTimeoutTrip {
			fmt.Fprintf(os.Stderr, "  ⚠ %d consecutive timeouts — backing off %s\n",
				consecutiveTimeouts, cleanDatabasesBackoffPause)
			time.Sleep(cleanDatabasesBackoffPause)
			consecutiveTimeouts = 0
		}

		if failures >= cleanDatabasesMaxFailures {
			fmt.Fprintf(os.Stderr, "\n✗ Aborting: %d consecutive failures suggest server is unhealthy.\n", failures)
			fmt.Fprintf(os.Stderr, "  Dropped %d/%d before stopping.\n", dropped, len(stale))
			return dropped, SilentExit()
		}

		dropCtx, dropCancel := context.WithTimeout(ctx, cleanDatabasesDropTimeout)
		safeName := strings.ReplaceAll(name, "`", "``")
		_, err := conn.ExecContext(dropCtx, fmt.Sprintf("DROP DATABASE `%s`", safeName)) //nolint:gosec // G201: identifier-escaped
		dropCancel()
		if err != nil {
			fmt.Fprintf(os.Stderr, "  FAIL: %s: %v\n", name, err)
			failures++
			if isTimeoutError(err) {
				consecutiveTimeouts++
			}
		} else {
			fmt.Printf("  Dropped: %s\n", name)
			dropped++
			failures = 0
			consecutiveTimeouts = 0
		}

		if (i+1)%cleanDatabasesBatchSize == 0 && i+1 < len(stale) {
			fmt.Printf("  [%d/%d] pausing %s...\n", i+1, len(stale), cleanDatabasesBatchPause)
			time.Sleep(cleanDatabasesBatchPause)
		}
	}
	fmt.Printf("\nDropped %d/%d stale databases.\n", dropped, len(stale))
	return dropped, nil
}
