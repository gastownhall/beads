package issueops

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strconv"

	"github.com/steveyegge/beads/internal/storage"
)

// EnsureIssueIDAvailableInTx serializes same-shard creates and rejects occupied IDs.
func EnsureIssueIDAvailableInTx(ctx context.Context, tx DBTX, id string) error {
	if tx == nil {
		return fmt.Errorf("ensure issue ID available: transaction is nil")
	}
	if id == "" {
		return fmt.Errorf("ensure issue ID available: ID is empty")
	}
	key := issueCreateCoordinationKey(id)
	if _, err := tx.ExecContext(ctx, "REPLACE INTO local_metadata (`key`, value) VALUES (?, ?)", key, strconv.FormatInt(FreshRowLock(), 10)); err != nil {
		return fmt.Errorf("coordinate issue create: %w", err)
	}
	taken, err := issueIDTakenTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if taken {
		return fmt.Errorf("%w: %s", storage.ErrAlreadyExists, id)
	}
	return nil
}

// issueIDTakenTx reports whether either storage plane holds id. issues and
// wisps share one ID space, so this is the one availability question both the
// create guard above and the hash minter (GenerateIssueIDInTable) ask.
func issueIDTakenTx(ctx context.Context, tx DBTX, id string) (bool, error) {
	for _, table := range []string{"issues", "wisps"} {
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE id = ?", id).Scan(&count); err != nil {
			return false, fmt.Errorf("check %s for issue %q: %w", table, id, err)
		}
		if count > 0 {
			return true, nil
		}
	}
	return false, nil
}

func issueCreateCoordinationKey(id string) string {
	sum := sha256.Sum256([]byte(id))
	shard := uint16(sum[0])<<4 | uint16(sum[1])>>4
	return fmt.Sprintf("issue-create/v1/%03x", shard)
}
