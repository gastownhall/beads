package issueops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

type ReopenResult struct {
	IsWisp      bool
	AlreadyOpen bool
	Changed     bool
	// IssueRowsChanged is the concrete SQL fact that at least one issues row
	// changed in this transaction, either the reopened row or a recomputed row.
	IssueRowsChanged bool
	// CommentTable names the comment table a nonblank reason wrote its
	// structured comment to ("comments" or "wisp_comments"), and is empty
	// when no comment was written. Selective staging routes stage it so the
	// comment reaches the same Dolt commit as the reopened row.
	CommentTable string
}

// ReopenIssueInTx reopens only statuses in the done category. It leaves all
// other statuses unchanged.
func ReopenIssueInTx(ctx context.Context, tx DBTX, id, reason, actor string) (*ReopenResult, error) {
	return reopenIssueInTx(ctx, tx, id, reason, actor, true)
}

//nolint:gosec // G201: table names come from WispTableRouting (hardcoded constants)
func reopenIssueInTx(ctx context.Context, tx DBTX, id, reason, actor string, retryOnConditionalMiss bool) (*ReopenResult, error) {
	isWisp := IsActiveWispInTx(ctx, tx, id)
	issueTable, _, eventTable, _ := WispTableRouting(isWisp)

	var current string
	err := tx.QueryRowContext(ctx, fmt.Sprintf("SELECT status FROM %s WHERE id = ?", issueTable), id).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: issue %s", storage.ErrNotFound, id)
	}
	if err != nil {
		return nil, fmt.Errorf("read reopen status for %s: %w", id, err)
	}

	status := types.Status(current)
	category, err := ReopenCategoryInTx(ctx, tx, status)
	if err != nil {
		return nil, fmt.Errorf("resolve reopen category for %s: %w", id, err)
	}
	if category != types.CategoryDone {
		return &ReopenResult{IsWisp: isWisp, AlreadyOpen: status == types.StatusOpen}, nil
	}

	var affectedIssues, affectedWisps []string
	if isWisp {
		affectedIssues, affectedWisps, err = AffectedByStatusChangeForWispInTx(ctx, tx, id)
	} else {
		affectedIssues, affectedWisps, err = AffectedByStatusChangeInTx(ctx, tx, id)
	}
	if err != nil {
		return nil, fmt.Errorf("affected by reopen for %s: %w", id, err)
	}

	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`
		UPDATE %s
		SET status = ?, closed_at = NULL, close_reason = '', closed_by_session = '', defer_until = NULL,
			updated_at = ?, row_lock = ?
		WHERE id = ? AND status = ?
	`, issueTable), types.StatusOpen, now, freshRowLock(), id, status)
	if err != nil {
		return nil, fmt.Errorf("reopen issue: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("get reopen rows affected: %w", err)
	}
	if rows == 0 {
		var latest string
		err := tx.QueryRowContext(ctx, fmt.Sprintf("SELECT status FROM %s WHERE id = ?", issueTable), id).Scan(&latest)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: issue %s", storage.ErrNotFound, id)
		}
		if err != nil {
			return nil, fmt.Errorf("check reopen status for %s: %w", id, err)
		}
		latestStatus := types.Status(latest)
		latestCategory, err := ReopenCategoryInTx(ctx, tx, latestStatus)
		if err != nil {
			return nil, fmt.Errorf("resolve latest reopen category for %s: %w", id, err)
		}
		if latestCategory != types.CategoryDone {
			return &ReopenResult{IsWisp: isWisp, AlreadyOpen: latestStatus == types.StatusOpen}, nil
		}
		if retryOnConditionalMiss {
			return reopenIssueInTx(ctx, tx, id, reason, actor, false)
		}
		return nil, fmt.Errorf("reopen issue %s: status changed concurrently", id)
	}

	if err := DeleteLeaseInTx(ctx, tx, id); err != nil {
		return nil, err
	}

	if err := RecordEventInTable(ctx, tx, eventTable, id, types.EventReopened, actor, reason); err != nil {
		return nil, fmt.Errorf("record reopen event: %w", err)
	}

	// A nonblank reason lands in the issue's comment thread as a real,
	// versioned comment — the same surface `bd comment` writes — attributed
	// to the reopen actor and routed to the plane the issue lives in. The
	// helper's journal record (CommentSourceStructured) replaces the audit
	// commented event this used to mint; the reopened event above still
	// carries the reason for the local audit trail.
	commentTable := ""
	if strings.TrimSpace(reason) != "" {
		commentTable = "comments"
		if isWisp {
			commentTable = "wisp_comments"
		}
		if _, err := addIssueCommentInTx(ctx, tx, id, actor, reason, time.Now().UTC().Truncate(time.Second), true); err != nil {
			return nil, fmt.Errorf("add reopen comment: %w", err)
		}
	}

	recompute, err := RecomputeIsBlockedInTxWithResult(ctx, tx, affectedIssues, affectedWisps)
	if err != nil {
		return nil, fmt.Errorf("recompute is_blocked after reopen for %s: %w", id, err)
	}

	// Snapshot only after all derived blocked-state maintenance has completed.
	// A reopen is a status change, so it journals as an update and mints a
	// version; the already-open early returns above write nothing and mint
	// nothing.
	if err := RecordEventInTx(ctx, tx, EventUpdate, id, actor); err != nil {
		return nil, err
	}
	if err := RecordVersionInTx(ctx, tx, id, actor); err != nil {
		return nil, err
	}

	return &ReopenResult{
		IsWisp:           isWisp,
		Changed:          true,
		IssueRowsChanged: !isWisp || recompute.IssueRowsChanged,
		CommentTable:     commentTable,
	}, nil
}
