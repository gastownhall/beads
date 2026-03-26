package versioncontrolops

import (
	"context"
	"fmt"
)

// DoltClone clones a Dolt database from a remote URL.
// conn must be a non-transactional database connection.
// The database parameter specifies the local database name for the clone.
func DoltClone(ctx context.Context, conn DBConn, url, database string) error {
	if _, err := conn.ExecContext(ctx, "CALL DOLT_CLONE(?, ?)", url, database); err != nil {
		return fmt.Errorf("dolt clone %s: %w", url, err)
	}
	return nil
}
