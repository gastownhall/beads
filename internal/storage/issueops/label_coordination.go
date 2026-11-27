package issueops

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strconv"

	publicops "github.com/steveyegge/beads/issueops"
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
// rows, so two writers that both replaced {a} committed {x, y}. The edits that
// lose an update this way are the ones that decide which labels to delete from
// the set they read: a whole-set replacement (LabelPatch.Replace on the direct
// route, the label use case's SetLabels on the uow route) and a prefix removal
// (LabelPatch.RemovePrefixes, which `bd set-state` sends). Such an edit
// rewrites this cell once, with a fresh value, in the SAME transaction, when
// it changes the set. A second such edit overlapping it on the same issue then
// fails its commit with a serialization error, and its update path replays it
// against the committed set: withRetryTx on the direct route (Lifecycle.Update
// and ApplyBatch), RunTx on the uow route.
//
// An add or remove of named labels (LabelPatch.Add and Remove, AddLabelInTx and
// RemoveLabelInTx, the uow label repository's Insert and Delete) reads nothing
// it acts on, so it leaves the cell alone and never conflicts through it. That
// keeps the transactions that write labels this way without a replay (`bd
// label propagate`, `bd cook --persist`, the tracker's direct-route label
// sync) exactly as conflict-free as before. Such an edit overlapping a
// replacement still merges row by row: a single named label lands as if it ran
// before or after the replacement, while an edit of several named labels can
// interleave with it, as any two label edits could before the cell.
//
// The bulk label writers do not take part either: a rename across issues
// (renameLabelInPlane), the create/import label upsert and a promote or demote
// between planes write label rows without touching the cell, so one of them
// overlapping a replacement can still commit the union.
//
// table is the label table the edit writes (labels or wisp_labels), and tx
// must be the transaction that writes the label rows: a cell written on
// another session would not conflict with anything. local_metadata is ignored
// for Dolt publication, so the cell never reaches a commit or a remote;
// ignored tables still take part in SQL transaction conflicts.
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
// issue always resolves to the same shard. A hash collision between two issues
// only makes two set-reading edits on them conflict, which their update paths
// replay; it never touches a named add or remove.
func labelCoordinationKey(issueID, table string) string {
	return fmt.Sprintf("%s%s/%s/%03x", labelCoordinationKeyPrefix, labelCoordinationKeyVersion, table, labelCoordinationShard(issueID))
}

func labelCoordinationShard(issueID string) uint16 {
	sum := sha256.Sum256([]byte(issueID))
	return uint16(sum[0])<<4 | uint16(sum[1])>>4
}

// labelPatchReadsTheSet reports whether patch decides which labels to delete
// from the set it read: a whole-set replacement or a non-empty prefix removal
// (an empty prefix matches nothing). Only such a patch rewrites the
// coordination cell; see TouchLabelCoordinationInTx.
func labelPatchReadsTheSet(patch publicops.LabelPatch) bool {
	if patch.Replace.Set {
		return true
	}
	for _, prefix := range patch.RemovePrefixes {
		if prefix != "" {
			return true
		}
	}
	return false
}
