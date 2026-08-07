package uow

import (
	"fmt"
	"os"

	"context"

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
func WakeExpiredDefers(ctx context.Context, p UnitOfWorkProvider) (int, error) {
	return RunTxResult(ctx, p, func(ctx context.Context, uw UnitOfWork) (int, string, error) {
		n, err := uw.IssueUseCase().WakeExpiredDefers(ctx)
		if err != nil {
			return 0, "", err
		}
		if n == 0 {
			return 0, "", nil
		}
		return n, storageissueops.WakeDefersCommitMessage(n), nil
	})
}

// WakeExpiredDefersAdvisory is WakeExpiredDefers under the read paths'
// contract: a ready listing must never fail because the sweep could not run,
// so errors are reduced to a stderr warning.
func WakeExpiredDefersAdvisory(ctx context.Context, p UnitOfWorkProvider) {
	if _, err := WakeExpiredDefers(ctx, p); err != nil {
		fmt.Fprintf(os.Stderr, "warning: defer-wake sweep skipped: %v\n", err)
	}
}
