package issueops

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strconv"
)

const (
	dependencyCoordinationKeyPrefix     = "dependency-coordination/"
	dependencyCoordinationKeyVersion    = "v1"
	dependencyCoordinationDurableTier   = "dependencies"
	dependencyCoordinationEphemeralTier = "wisp_dependencies"
	dependencyCoordinationShardCount    = 1 << 12
)

// touchDependencyCoordinationInTx rewrites the coordination cells for both
// dependency tables in a fixed order. Writers that need a stable view of an
// issue's incoming parent-child edges use these cells to make concurrent Dolt
// transactions conflict rather than cell-merge.
func touchDependencyCoordinationInTx(ctx context.Context, tx DBTX, parentID string) error {
	for _, tier := range [2]string{dependencyCoordinationDurableTier, dependencyCoordinationEphemeralTier} {
		if err := TouchDependencyCoordinationTableInTx(ctx, tx, parentID, tier); err != nil {
			return err
		}
	}
	return nil
}

// TouchDependencyCoordinationTableInTx rewrites the coordination cell for one
// dependency table. table must be dependencies or wisp_dependencies.
func TouchDependencyCoordinationTableInTx(ctx context.Context, tx DBTX, parentID, table string) error {
	if parentID == "" {
		return fmt.Errorf("touch dependency coordination: parent ID must not be empty")
	}
	if table != dependencyCoordinationDurableTier && table != dependencyCoordinationEphemeralTier {
		return fmt.Errorf("touch dependency coordination: unsupported table %q", table)
	}
	key := dependencyCoordinationKey(parentID, table)
	if _, err := tx.ExecContext(ctx,
		"REPLACE INTO local_metadata (`key`, value) VALUES (?, ?)", key, strconv.FormatInt(FreshRowLock(), 10)); err != nil {
		return fmt.Errorf("touch dependency coordination for %s: %w", table, err)
	}
	return nil
}

func dependencyCoordinationKey(parentID, table string) string {
	shard := dependencyCoordinationShard(parentID)
	// A tier has 4096 shard rows: enough to keep unrelated writes apart while
	// bounding the clone-local coordination state at 8192 rows. Same-parent
	// operations always resolve to the same shard; a hash collision only adds a
	// safe serialization conflict.
	return fmt.Sprintf("%s%s/%s/%03x", dependencyCoordinationKeyPrefix, dependencyCoordinationKeyVersion, table, shard)
}

func dependencyCoordinationShard(parentID string) uint16 {
	sum := sha256.Sum256([]byte(parentID))
	return uint16(sum[0])<<4 | uint16(sum[1])>>4
}
