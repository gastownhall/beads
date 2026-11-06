//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/storagecontract"
	"github.com/steveyegge/beads/internal/types"
)

// TestExternalRefHistoryBatchContract runs the external-ref history batch
// contract against the embedded store — the DEFAULT open path, whose raw SQL
// surface is a per-call OpenSQL handle rather than a pooled *sql.DB, which
// is what this wiring's WithTx, ExecMain, QueryRowMain and branch-scoped
// ExecSide hooks exercise.
func TestExternalRefHistoryBatchContract(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "erhb")
	ctx := t.Context()

	openMain := func(ctx context.Context) (*sql.DB, func() error, error) {
		return embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
	}

	fixture := storagecontract.ExternalRefHistoryBatchFixture{
		Prefix: "erhb",
		WithTx: func(ctx context.Context, fn func(tx *sql.Tx) error) error {
			db, cleanup, err := openMain(ctx)
			if err != nil {
				return err
			}
			defer cleanup()
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			defer tx.Rollback()
			return fn(tx)
		},
		ExecMain: func(ctx context.Context, query string, args ...any) error {
			db, cleanup, err := openMain(ctx)
			if err != nil {
				return err
			}
			defer cleanup()
			_, err = db.ExecContext(ctx, query, args...)
			return err
		},
		QueryRowMain: func(ctx context.Context, dest any, query string, args ...any) error {
			db, cleanup, err := openMain(ctx)
			if err != nil {
				return err
			}
			defer cleanup()
			return db.QueryRowContext(ctx, query, args...).Scan(dest)
		},
		// OpenSQL takes the branch directly, so a side-branch statement is
		// one handle scoped to that branch — no checkout whose session
		// state could outlive the statement.
		ExecSide: func(ctx context.Context, branch, query string, args ...any) error {
			db, cleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, branch)
			if err != nil {
				return err
			}
			defer cleanup()
			_, err = db.ExecContext(ctx, query, args...)
			return err
		},
		CreateIssue: func(ctx context.Context, id, title string) error {
			return te.store.CreateIssue(ctx, &types.Issue{
				ID:        id,
				Title:     title,
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: types.TypeTask,
			}, "tester")
		},
		CurrentCommit: te.store.GetCurrentCommit,
		// The same one-transaction settlement the dolt leg runs: under
		// autocommit DOLT_MERGE rolls back on conflicts, so set
		// dolt_allow_commit_conflicts, merge, resolve --theirs, stage, and
		// date the merge commit inside one transaction.
		MergeSide: func(ctx context.Context, branch string, at time.Time) error {
			db, cleanup, err := openMain(ctx)
			if err != nil {
				return err
			}
			defer cleanup()
			tx, err := db.BeginTx(ctx, nil)
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
				// The embedded settlement can conclude the merge on its own
				// during the resolve, leaving nothing for the explicit
				// commit to version. Accept that outcome here: the
				// contract's preconditions (issues AS OF the snapshot
				// commit, and the single-id answer at the cutoff) fail
				// loudly if the merge did not actually land.
				if !strings.Contains(err.Error(), "nothing to commit") {
					return fmt.Errorf("merge commit: %w", err)
				}
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
