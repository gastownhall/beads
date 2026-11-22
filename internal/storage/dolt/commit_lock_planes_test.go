package dolt

// Commit-lock tests (GH#8590, dolthub/dolt#12258): every server-mode commit
// plane holds the per-database named lock (versioncontrolops.CommitLockName)
// across its DOLT_ADD/DOLT_COMMIT pair, takes it after the pending fast path
// so a no-op write pays no lock round trips, and releases it on every return
// path. Each test holds the lock from a second session and asserts the commit
// does not land until the holder releases it — the shape that, before the
// lock existed, produced Dolt commits carrying merge conflict artifacts under
// concurrent writers.
import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// TestCommitLockSerializesStorePlanes is AC-2: every other server-mode
// DoltStore commit plane — the publication funnel (AddLabel), the in-tx
// helper (UpdateIssue, ClaimIssue), the sweeper's in-transaction commit, and
// CommitAll — must hold its Dolt commit until the lock is released.
func TestCommitLockSerializesStorePlanes(t *testing.T) {
	skipIfNoDolt(t)
	t.Parallel()

	t.Run("PublicationFunnelAddLabel", func(t *testing.T) {
		store, probe, lockName := commitLockStore(t)
		ctx := context.Background()
		id := lockTestIssue(t, ctx, store)
		assertCommitBlockedWhileHeld(t, store, probe, lockName, func(ctx context.Context) error {
			return store.AddLabel(ctx, id, "bd-lock-commit", "tester")
		}, 500*time.Millisecond)
	})

	t.Run("InTxHelperUpdateIssue", func(t *testing.T) {
		store, probe, lockName := commitLockStore(t)
		ctx := context.Background()
		id := lockTestIssue(t, ctx, store)
		assertCommitBlockedWhileHeld(t, store, probe, lockName, func(ctx context.Context) error {
			return store.UpdateIssue(ctx, id, map[string]interface{}{"title": "bd-lock-commit"}, "tester")
		}, 500*time.Millisecond)
	})

	t.Run("InTxHelperClaimIssue", func(t *testing.T) {
		store, probe, lockName := commitLockStore(t)
		ctx := context.Background()
		id := lockTestIssue(t, ctx, store)
		assertCommitBlockedWhileHeld(t, store, probe, lockName, func(ctx context.Context) error {
			return store.ClaimIssue(ctx, id, "tester")
		}, 500*time.Millisecond)
	})

	t.Run("Sweeper", func(t *testing.T) {
		store, probe, lockName := commitLockStore(t)
		ctx := context.Background()
		id := "bd-lock-1"
		if err := store.CreateIssue(ctx, &types.Issue{ID: id, Title: "sweep me", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}, "tester"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}
		if err := store.UpdateIssue(ctx, id, map[string]interface{}{"status": string(types.StatusClosed)}, "tester"); err != nil {
			t.Fatalf("close issue: %v", err)
		}
		sweeper, err := store.Sweeper()
		if err != nil {
			t.Fatalf("Sweeper(): %v", err)
		}
		cutoff := time.Now().Add(time.Hour)
		assertCommitBlockedWhileHeld(t, store, probe, lockName, func(ctx context.Context) error {
			res, err := sweeper.Sweep(ctx, issueops.SweepRequest{
				Actor:        "tester",
				Tier:         issueops.SweepDurable,
				ClosedBefore: &cutoff,
				IDPattern:    "*",
			})
			if err != nil {
				return err
			}
			if res.Swept == 0 {
				return errors.New("sweep matched no rows; the plane under test did not run")
			}
			return nil
		}, 500*time.Millisecond)
	})

	t.Run("CommitAll", func(t *testing.T) {
		store, probe, lockName := commitLockStore(t)
		ctx := context.Background()
		id := lockTestIssue(t, ctx, store)
		assertCommitBlockedWhileHeld(t, store, probe, lockName, func(ctx context.Context) error {
			// An out-of-band autocommit write with a real value change
			// dirties the working set, so CommitAll has a real commit to
			// make and cannot report nothing-committed.
			if _, err := store.db.ExecContext(ctx, "UPDATE issues SET title = 'commit-all dirty' WHERE id = ?", id); err != nil {
				return err
			}
			committed, err := store.CommitAll(ctx, "bd-lock-commit")
			if err != nil {
				return err
			}
			if !committed {
				return errors.New("CommitAll reported nothing committed, so the plane under test did not run")
			}
			return nil
		}, 500*time.Millisecond)
	})

}
