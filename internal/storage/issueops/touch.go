package issueops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TouchIssueResult reports which storage plane owned the touched issue.
// Callers use it to mark the matching issue and audit-event tables dirty.
type TouchIssueResult struct {
	IsWisp bool
}

// TouchIssueInTx publishes a fresh issue revision after a composite mutation
// changed only related tables (labels, dependencies, hierarchy). It advances
// updated_at, replaces row_lock with a distinct non-zero token, and records the
// normally-attributed update audit and journal events without changing any
// user-authored issue field. All writes use tx, so the related mutation and its
// published revision either commit or roll back together.
//
// The issues plane is probed first, matching GetIssueInTx and checked-close
// routing when corrupt/legacy data happens to contain a dual-resident ID.
//
//nolint:gosec // G201: table names come from WispTableRouting (hardcoded constants).
func TouchIssueInTx(ctx context.Context, tx DBTX, id, actor string) (*TouchIssueResult, error) {
	oldIssue, err := getIssueFromTableInTx(ctx, tx, "issues", "labels", id)
	isWisp := false
	if errors.Is(err, storage.ErrNotFound) {
		oldIssue, err = getIssueFromTableInTx(ctx, tx, "wisps", "wisp_labels", id)
		isWisp = true
	}
	if errors.Is(err, storage.ErrNotFound) {
		return nil, fmt.Errorf("%w: issue %s", storage.ErrNotFound, id)
	}
	if err != nil {
		return nil, fmt.Errorf("read issue before touch: %w", err)
	}

	issueTable, _, eventTable, _ := WispTableRouting(isWisp)
	nextRowLock := freshRowLock()
	for nextRowLock == oldIssue.RowVersion {
		nextRowLock = freshRowLock()
	}
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx,
		fmt.Sprintf("UPDATE %s SET updated_at = ?, row_lock = ? WHERE id = ?", issueTable),
		now, nextRowLock, id,
	)
	if err != nil {
		return nil, fmt.Errorf("touch issue row: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("read touched issue row count: %w", err)
	}
	if rows != 1 {
		return nil, fmt.Errorf("touch issue %s affected %d rows, want 1", id, rows)
	}

	oldData, err := json.Marshal(oldIssue)
	if err != nil {
		return nil, fmt.Errorf("encode issue before touch: %w", err)
	}
	if err := RecordFullEventInTable(ctx, tx, eventTable, id, types.EventUpdated, actor, string(oldData), "{}"); err != nil {
		return nil, fmt.Errorf("record touch event: %w", err)
	}
	if err := RecordEventInTx(ctx, tx, EventUpdate, id, actor); err != nil {
		return nil, err
	}
	return &TouchIssueResult{IsWisp: isWisp}, nil
}
