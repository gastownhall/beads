package versioncontrolops

import (
	"context"
	"fmt"
)

// DoltGC runs Dolt garbage collection to reclaim disk space. Archive level 0
// writes classic Snappy table files instead of zstd archives.
// conn must be a non-transactional database connection since
// DOLT_GC cannot run inside an explicit transaction.
func DoltGC(ctx context.Context, conn DBConn) error {
	if _, err := conn.ExecContext(ctx, "CALL DOLT_GC('--archive-level', '0')"); err != nil {
		return fmt.Errorf("dolt gc: %w", err)
	}
	return nil
}
