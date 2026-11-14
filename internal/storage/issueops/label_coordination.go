package issueops

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strconv"
)

const (
	labelCoordinationKeyPrefix     = "label-coordination/"
	labelCoordinationKeyVersion    = "v1"
	labelCoordinationDurableTier   = "labels"
	labelCoordinationEphemeralTier = "wisp_labels"
	labelCoordinationShardCount    = 1 << 12
)

// TouchLabelCoordinationInTx rewrites the coordination cell for one issue's
// rows in one label table, on the label write's OWN transaction. Dolt merges
// tables cell-by-cell, so two transactions that each replace an issue's whole
// label set from the same snapshot never conflict on the label rows
// themselves: the shared old label is a delete-delete and the two new labels
// are disjoint inserts, and both commits land — leaving the union of two
// whole-set requests (GH#5657). Rewriting one shared cell with a fresh value
// turns that silent union into a commit-time serialization conflict, which
// the existing retry loops (withRetryTx, RunTxResultWithin) replay against a
// fresh snapshot. labelTable must be labels or wisp_labels, matching the
// table the label write landed in: a wisp's labels live in wisp_labels, and a
// cell on the other tier would serialize nothing.
func TouchLabelCoordinationInTx(ctx context.Context, tx DBTX, issueID, labelTable string) error {
	if issueID == "" {
		return fmt.Errorf("touch label coordination: issue ID must not be empty")
	}
	if labelTable != labelCoordinationDurableTier && labelTable != labelCoordinationEphemeralTier {
		return fmt.Errorf("touch label coordination: unsupported table %q", labelTable)
	}
	key := labelCoordinationKey(issueID, labelTable)
	if _, err := tx.ExecContext(ctx,
		"REPLACE INTO local_metadata (`key`, value) VALUES (?, ?)", key, strconv.FormatInt(FreshRowLock(), 10)); err != nil {
		return fmt.Errorf("touch label coordination for %s: %w", labelTable, err)
	}
	return nil
}

func labelCoordinationKey(issueID, labelTable string) string {
	shard := labelCoordinationShard(issueID)
	// A tier has 4096 shard rows: enough to keep unrelated issues' label
	// writes apart while bounding the clone-local coordination state at 8192
	// rows — the same shape as the dependency coordination cells. One issue's
	// label writes always resolve to the same shard; a hash collision only
	// adds a safe serialization conflict.
	return fmt.Sprintf("%s%s/%s/%03x", labelCoordinationKeyPrefix, labelCoordinationKeyVersion, labelTable, shard)
}

func labelCoordinationShard(issueID string) uint16 {
	sum := sha256.Sum256([]byte(issueID))
	return uint16(sum[0])<<4 | uint16(sum[1])>>4
}
