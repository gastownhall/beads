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

// TouchLabelCoordinationInTx rewrites the clone-local coordination cell that
// guards one issue's label set (GH#5657).
//
// Label rows alone do not make two overlapping label edits conflict: Dolt
// merges two transactions that each delete the same row and insert different
// rows, so two writers that both replaced {a} committed {x, y}. Every label
// write that actually changes a row rewrites this cell with a fresh value in
// the SAME transaction, so a second overlapping writer on the same issue fails
// its commit with a serialization error and the existing retry replays it
// against the committed set.
//
// table is the label table the write went to (labels or wisp_labels), and tx
// must be the transaction that wrote the label row: a cell written on another
// session would not conflict with anything. local_metadata is ignored for Dolt
// publication, so the cell never reaches a commit or a remote; ignored tables
// still take part in SQL transaction conflicts.
func TouchLabelCoordinationInTx(ctx context.Context, tx DBTX, issueID, table string) error {
	if issueID == "" {
		return fmt.Errorf("touch label coordination: issue ID must not be empty")
	}
	if table != labelCoordinationDurableTier && table != labelCoordinationEphemeralTier {
		return fmt.Errorf("touch label coordination: unsupported table %q", table)
	}
	key := labelCoordinationKey(issueID, table)
	if _, err := tx.ExecContext(ctx,
		"REPLACE INTO local_metadata (`key`, value) VALUES (?, ?)", key, strconv.FormatInt(FreshRowLock(), 10)); err != nil {
		return fmt.Errorf("touch label coordination for %s: %w", table, err)
	}
	return nil
}

// labelCoordinationKey names the cell for issueID's labels in table. A tier
// has 4096 shard rows, bounding the clone-local state at 8192 rows; the same
// issue always resolves to the same shard, and a hash collision between two
// issues only adds a safe serialization conflict.
func labelCoordinationKey(issueID, table string) string {
	return fmt.Sprintf("%s%s/%s/%03x", labelCoordinationKeyPrefix, labelCoordinationKeyVersion, table, labelCoordinationShard(issueID))
}

func labelCoordinationShard(issueID string) uint16 {
	sum := sha256.Sum256([]byte(issueID))
	return uint16(sum[0])<<4 | uint16(sum[1])>>4
}
