package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/storagecontract"
	"github.com/steveyegge/beads/internal/types"
)

// TestExternalRefHistoryBatchContract runs the external-ref history batch
// contract against the server-backed store. It shares its body with the
// embedded leg (issueops.PreviousExternalRefsInTx) and differs only in how
// the history-read transaction and the side-branch sessions are opened,
// which is what this wiring checks.
func TestExternalRefHistoryBatchContract(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	fixture := storagecontract.ExternalRefHistoryBatchFixture{
		Prefix: "erhb",
		WithTx: func(ctx context.Context, fn func(tx *sql.Tx) error) error {
			tx, err := store.db.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			defer tx.Rollback()
			return fn(tx)
		},
		ExecMain: func(ctx context.Context, query string, args ...any) error {
			_, err := store.db.ExecContext(ctx, query, args...)
			return err
		},
		QueryRowMain: func(ctx context.Context, dest any, query string, args ...any) error {
			return store.db.QueryRowContext(ctx, query, args...).Scan(dest)
		},
		// A throwaway connection per statement: DOLT_CHECKOUT's branch
		// selection sticks to the physical connection it ran on, so
		// checking out a side branch on the store's own pool would leak the
		// side branch into the store's reads — the hazard pinStoreBranch
		// documents. A connection opened and closed per statement carries
		// the checkout to the grave; the checkout and the statement run on
		// one leased connection so they share a session.
		ExecSide: func(ctx context.Context, branch, query string, args ...any) error {
			db, err := sql.Open("mysql", store.connStr)
			if err != nil {
				return err
			}
			defer db.Close()
			conn, err := db.Conn(ctx)
			if err != nil {
				return err
			}
			defer conn.Close()
			if _, err := conn.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", branch); err != nil {
				return err
			}
			_, err = conn.ExecContext(ctx, query, args...)
			return err
		},
		CreateIssue: func(ctx context.Context, id, title string) error {
			return store.CreateIssue(ctx, &types.Issue{
				ID:        id,
				Title:     title,
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: types.TypeTask,
			}, "tester")
		},
		CurrentCommit: store.GetCurrentCommit,
		// One transaction for the whole settlement: under autocommit,
		// DOLT_MERGE rolls itself back the moment it detects conflicts
		// instead of leaving them for dolt_conflicts, which is why the
		// store's own pull path sets dolt_allow_commit_conflicts first
		// (store.go settleMergeInTx). Some dolt versions error on the
		// conflicted merge, others leave it in the working set — the
		// resolve either way, and only fail when there was nothing to
		// resolve.
		MergeSide: func(ctx context.Context, branch string, at time.Time) error {
			tx, err := store.db.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			rollback := true
			defer func() {
				if rollback {
					_ = tx.Rollback()
				}
			}()
			if _, err := tx.ExecContext(ctx, "SET @@dolt_allow_commit_conflicts = 1"); err != nil {
				return fmt.Errorf("allow commit conflicts: %w", err)
			}
			// Some dolt versions error on a conflicted merge, others leave
			// the conflicts in the working set; the resolve decides whether
			// the merge can settle, so its error is the one to surface.
			_, mergeErr := tx.ExecContext(ctx, "CALL DOLT_MERGE(?)", branch)
			if _, err := tx.ExecContext(ctx, "CALL DOLT_CONFLICTS_RESOLVE('--theirs', 'issues')"); err != nil {
				if mergeErr != nil {
					return fmt.Errorf("merge %s failed (%v) and its conflicts could not be resolved: %w", branch, mergeErr, err)
				}
				return fmt.Errorf("resolve conflicts (--theirs issues): %w", err)
			}
			if _, err := tx.ExecContext(ctx, "CALL DOLT_ADD('issues')"); err != nil {
				return fmt.Errorf("stage issues: %w", err)
			}
			if _, err := tx.ExecContext(ctx, "CALL DOLT_COMMIT('--date', ?, '-Am', ?)", at.Format(time.RFC3339), "merge "+branch); err != nil {
				return fmt.Errorf("merge commit: %w", err)
			}
			if err := tx.Commit(); err != nil {
				return fmt.Errorf("commit merge transaction: %w", err)
			}
			rollback = false
			return nil
		},
	}

	storagecontract.RunExternalRefHistoryBatchContract(t, ctx, fixture)
}
