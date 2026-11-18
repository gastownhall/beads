package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/steveyegge/beads/internal/storage/dberrors"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

func NewChildCounterSQLRepository(runner Runner) domain.ChildCounterSQLRepository {
	return &childCounterSQLRepositoryImpl{runner: runner}
}

type childCounterSQLRepositoryImpl struct {
	runner Runner
}

var _ domain.ChildCounterSQLRepository = (*childCounterSQLRepositoryImpl)(nil)

func (r *childCounterSQLRepositoryImpl) NextChildID(ctx context.Context, parentID string, _ domain.ChildCounterOpts) (string, error) {
	if parentID == "" {
		return "", errors.New("db: ChildCounterSQLRepository.NextChildID: parentID must not be empty")
	}

	counterTable, issueTable := "child_counters", "issues"
	parentIsWisp, err := r.parentIsActiveWisp(ctx, parentID)
	if err != nil {
		return "", fmt.Errorf("db: ChildCounterSQLRepository.NextChildID: probe parent table for %s: %w", parentID, err)
	}
	if parentIsWisp {
		counterTable, issueTable = "wisp_child_counters", "wisps"
	}

	var lastChild int
	err = r.runner.QueryRowContext(ctx,
		//nolint:gosec // G201: counterTable is one of two hardcoded constants
		fmt.Sprintf("SELECT last_child FROM %s WHERE parent_id = ?", counterTable),
		parentID,
	).Scan(&lastChild)
	switch {
	case err == nil:
	case errors.Is(err, sql.ErrNoRows):
		lastChild = 0
	default:
		return "", fmt.Errorf("db: ChildCounterSQLRepository.NextChildID: read counter for %s: %w", parentID, err)
	}

	// The live children AND the retired ones: a retired child suffix is taken
	// (issueops.UsedChildIDsInTx says why the counter alone is not enough).
	usedIDs, err := issueops.UsedChildIDsInTx(ctx, r.runner, issueTable, parentID)
	if err != nil {
		return "", fmt.Errorf("db: ChildCounterSQLRepository.NextChildID: scan existing children of %s: %w", parentID, err)
	}
	for _, id := range usedIDs {
		if n, ok := parseChildSuffix(id); ok && n > lastChild {
			lastChild = n
		}
	}

	next := lastChild + 1
	//nolint:gosec // G201: counterTable is one of two hardcoded constants
	if _, err := r.runner.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (parent_id, last_child) VALUES (?, ?)
		ON DUPLICATE KEY UPDATE last_child = ?
	`, counterTable), parentID, next, next); err != nil {
		return "", fmt.Errorf("db: ChildCounterSQLRepository.NextChildID: upsert counter for %s: %w", parentID, err)
	}

	return fmt.Sprintf("%s.%d", parentID, next), nil
}

// AdvanceForChild reconciles one caller-named child into its parent's child
// counter by reusing issueops' ReconcileChildCounters (the #5122 fix for the
// issueops create path, GH#4750), so both stacks advance counters with one
// body: the reconcile reads the parent's plane for its counter table, never
// lowers a counter, skips a parent that does not exist (the counter's foreign
// key), and issues no SQL at all for a flat id. Runs on the runner the caller
// shares with the insert, so the advance commits or rolls back with the row.
func (r *childCounterSQLRepositoryImpl) AdvanceForChild(ctx context.Context, childID string) error {
	if childID == "" {
		return errors.New("db: ChildCounterSQLRepository.AdvanceForChild: childID must not be empty")
	}
	if _, err := issueops.ReconcileChildCounters(ctx, r.runner, []*types.Issue{{ID: childID}}); err != nil {
		return fmt.Errorf("db: ChildCounterSQLRepository.AdvanceForChild: advance counter for %s: %w", childID, err)
	}
	return nil
}

func (r *childCounterSQLRepositoryImpl) parentIsActiveWisp(ctx context.Context, parentID string) (bool, error) {
	var probe int
	err := r.runner.QueryRowContext(ctx, "SELECT 1 FROM wisps WHERE id = ? LIMIT 1", parentID).Scan(&probe)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case dberrors.IsTableNotExist(err):
		return false, nil
	default:
		return false, err
	}
}

func parseChildSuffix(id string) (int, bool) {
	dot := strings.LastIndex(id, ".")
	if dot < 0 || dot == len(id)-1 {
		return 0, false
	}
	n, err := strconv.Atoi(id[dot+1:])
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}
