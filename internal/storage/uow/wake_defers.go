package uow

import (
	"fmt"
	"os"
	"sync"

	"context"

	"github.com/steveyegge/beads/internal/storage/dberrors"
	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
)

// WakeExpiredDefers runs the lazy defer-wake sweep in its own unit of work,
// committing with a wake message iff any permanent issue woke — the same
// commit-iff-changed contract every RunTxResult writer keeps, so the steady
// state (nothing expired) costs one UPDATE on a transaction that never calls
// DOLT_COMMIT.
//
// It runs in a transaction of its own, not the caller's, because every
// ready-work READ in this stack runs under RunTxRead or a caller-owned UOW
// that rolls back — a sweep inside those spans would be silently discarded.
// Sequencing is what matters: sweep first, then read, and the read sees the
// woken rows.
//
// A WISP-only wake takes a third path: wisp tables are dolt_ignored, so the
// wake-message form (DOLT_COMMIT) would find nothing to commit and the
// transaction would roll back, silently discarding the wisp wakes — every
// subsequent ready read would then redo and re-discard the same writes
// forever. Those wakes persist via uw.Commit(ctx, "") — the ephemeral
// plain-COMMIT form RunTxEphemeral keeps for dolt_ignored state (bd-lrgn1) —
// issued inside the work func; a serialization failure from it surfaces as
// the closure's error and retries against a fresh unit of work, exactly like
// a commit issued by RunTxResult itself.
func WakeExpiredDefers(ctx context.Context, p UnitOfWorkProvider) (int, error) {
	return RunTxResult(ctx, p, func(ctx context.Context, uw UnitOfWork) (int, string, error) {
		issues, wisps, err := uw.IssueUseCase().WakeExpiredDefers(ctx)
		if err != nil {
			return 0, "", err
		}
		if issues > 0 {
			return issues, storageissueops.WakeDefersCommitMessage(issues), nil
		}
		if wisps > 0 {
			if err := uw.Commit(ctx, ""); err != nil {
				return 0, "", err
			}
		}
		return 0, "", nil
	})
}

// advisoryAccessDeniedOnce rate-limits the access-denied advisory to one
// warning per process: a read-only-privileged SQL user hits it on every
// ready-front read, and repeating a configuration fact on each `bd ready`
// is noise, not signal.
var advisoryAccessDeniedOnce sync.Once

// WakeExpiredDefersAdvisory is WakeExpiredDefers under the read paths'
// contract: a ready listing must never fail because the sweep could not run,
// so errors are reduced to a stderr warning (warn-once for access-denied).
func WakeExpiredDefersAdvisory(ctx context.Context, p UnitOfWorkProvider) {
	_, err := WakeExpiredDefers(ctx, p)
	if err == nil {
		return
	}
	if dberrors.IsAccessDenied(err) {
		advisoryAccessDeniedOnce.Do(func() {
			fmt.Fprintf(os.Stderr, "warning: defer-wake sweep skipped (SQL user lacks write privileges; expired defers will not auto-wake from this client): %v\n", err)
		})
		return
	}
	fmt.Fprintf(os.Stderr, "warning: defer-wake sweep skipped: %v\n", err)
}
