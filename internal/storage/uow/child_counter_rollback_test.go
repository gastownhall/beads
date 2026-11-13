package uow

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

// TestCallerNamedChildCounterRollsBack pins the transactional half of the
// caller-named child-counter advance (bd-qp2au6, #4750): the counter row a
// `bd create --id P.8` writes rides the unit of work's transaction with the
// child row itself, so a unit of work that fails after the create leaves
// neither row behind. The counter is asserted INSIDE the transaction, so a
// create that skipped the advance entirely cannot pass this quietly: there
// would be no row to read.
func TestCallerNamedChildCounterRollsBack(t *testing.T) {
	ctx := context.Background()
	provider := newTestUOWProvider(t)

	// The parent is committed separately: only the child's create rides the
	// transaction under test.
	require.NoError(t, RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
		_, err := uw.IssueUseCase().CreateIssue(ctx, domain.CreateIssueParams{
			Issue:      &types.Issue{Title: "parent", IssueType: types.TypeEpic, Priority: 2},
			ExplicitID: "rb-p",
		}, "tester")
		return "bd: create rb-p", err
	}))

	sentinel := errors.New("sentinel: abandon the caller-named child")
	err := RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
		_, err := uw.IssueUseCase().CreateIssue(ctx, domain.CreateIssueParams{
			Issue:      &types.Issue{Title: "named child", IssueType: types.TypeTask, Priority: 2},
			ExplicitID: "rb-p.8",
		}, "tester")
		if err != nil {
			return "", err
		}
		// Same transaction, same pinned connection: the counter the create
		// advanced must be visible before the rollback discards it.
		result, err := uw.RawSQLUseCase().Query(ctx,
			"SELECT last_child FROM child_counters WHERE parent_id = ?", "rb-p")
		if err != nil {
			return "", fmt.Errorf("read counter inside the unit of work: %w", err)
		}
		if len(result.Rows) != 1 {
			return "", fmt.Errorf("child_counters rows for rb-p inside the unit of work = %d, want 1 with last_child = 8", len(result.Rows))
		}
		var lastChild int
		if err := scanRawSQLValue(&lastChild, result.Rows[0][0]); err != nil {
			return "", fmt.Errorf("scan last_child inside the unit of work: %w", err)
		}
		if lastChild != 8 {
			return "", fmt.Errorf("last_child inside the unit of work = %d, want 8", lastChild)
		}
		return "", sentinel
	})
	require.ErrorIs(t, err, sentinel,
		"the unit of work must end with the caller's sentinel once the in-transaction assertions held")

	// A fresh unit of work must see neither the child nor its counter row.
	_, err = RunTxRead(ctx, provider, func(ctx context.Context, uw UnitOfWork) (struct{}, error) {
		assertCount := func(query string, want int) error {
			result, err := uw.RawSQLUseCase().Query(ctx, query)
			if err != nil {
				return err
			}
			if len(result.Rows) != 1 {
				return fmt.Errorf("query %q returned %d rows, want 1", query, len(result.Rows))
			}
			var got int
			if err := scanRawSQLValue(&got, result.Rows[0][0]); err != nil {
				return err
			}
			if got != want {
				return fmt.Errorf("query %q = %d, want %d", query, got, want)
			}
			return nil
		}
		if err := assertCount("SELECT COUNT(*) FROM issues WHERE id = 'rb-p.8'", 0); err != nil {
			return struct{}{}, fmt.Errorf("rolled-back child: %w", err)
		}
		if err := assertCount("SELECT COUNT(*) FROM child_counters WHERE parent_id = 'rb-p'", 0); err != nil {
			return struct{}{}, fmt.Errorf("rolled-back counter: %w", err)
		}
		if err := assertCount("SELECT COUNT(*) FROM issues WHERE id = 'rb-p'", 1); err != nil {
			return struct{}{}, fmt.Errorf("committed parent: %w", err)
		}
		return struct{}{}, nil
	})
	require.NoError(t, err, "the fresh unit of work must see the rollback's result")
}
