package versioncontrolops

import (
	"context"
	"fmt"
)

// DoltGC runs Dolt garbage collection to reclaim disk space.
// conn must be a non-transactional database connection since
// DOLT_GC cannot run inside an explicit transaction.
func DoltGC(ctx context.Context, conn DBConn) error {
	if _, err := conn.ExecContext(ctx, "CALL DOLT_GC()"); err != nil {
		return fmt.Errorf("dolt gc: %w", err)
	}
	return nil
}
