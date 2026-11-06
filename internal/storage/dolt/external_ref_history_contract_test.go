package dolt

import (
	"context"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/storagecontract"
	"github.com/steveyegge/beads/internal/types"
)

func TestExternalRefHistoryBatchContract(t *testing.T) {
	store, cleanup := setupTestStore(t)
	t.Cleanup(cleanup)
	ctx, cancel := testContext(t)
	defer cancel()
	storagecontract.RunExternalRefHistoryBatchContract(t, ctx, externalRefHistoryFixture(t, store))
}

func BenchmarkExternalRefHistoryBatch(b *testing.B) {
	store, cleanup := setupBenchStore(b)
	b.Cleanup(cleanup)
	storagecontract.RunExternalRefHistoryBatchBenchmark(b, b.Context(), externalRefHistoryFixture(b, store))
}

// DOLT_CHECKOUT is per connection: branch work needs a one-connection pool.
func externalRefHistoryFixture(tb testing.TB, store *DoltStore) storagecontract.ExternalRefHistoryFixture {
	var branch string
	if err := store.db.QueryRowContext(context.Background(), "SELECT active_branch()").Scan(&branch); err != nil {
		tb.Fatalf("active_branch: %v", err)
	}
	exec := func(ctx context.Context, stmt string, args ...any) error {
		_, err := store.db.ExecContext(ctx, stmt, args...)
		return err
	}
	return storagecontract.ExternalRefHistoryFixture{
		Store:  store,
		Branch: branch,
		CreateIssues: func(ctx context.Context, ids []string) error {
			for _, id := range ids {
				issue := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
				if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
					return err
				}
			}
			return nil
		},
		DeleteIssue: store.DeleteIssue,
		CreateBranch: func(ctx context.Context, name string) error {
			tb.Cleanup(func() { _ = exec(context.Background(), "CALL DOLT_BRANCH('-D', ?)", name) })
			return exec(ctx, "CALL DOLT_BRANCH(?)", name)
		},
		CommitOn: func(ctx context.Context, onto string, when time.Time, stmts ...string) (err error) {
			if err := exec(ctx, "CALL DOLT_CHECKOUT(?)", onto); err != nil {
				return err
			}
			defer func() {
				if cerr := exec(ctx, "CALL DOLT_CHECKOUT(?)", branch); err == nil {
					err = cerr
				}
			}()
			for _, stmt := range stmts {
				if err := exec(ctx, stmt); err != nil {
					return err
				}
			}
			return exec(ctx, "CALL DOLT_COMMIT('-Am', 'step', '--date', ?)", when.Format("2006-01-02T15:04:05.000Z"))
		},
		Merge: func(ctx context.Context, from string) error {
			_, err := store.Merge(ctx, from)
			return err
		},
	}
}
