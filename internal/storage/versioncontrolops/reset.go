package versioncontrolops

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// resetHardPreservingCloneLocalFKs runs CALL DOLT_RESET('--hard'[, target]) on
// conn through schema.ResetHardPreservingCloneLocalFKs, which re-links every
// clone-local FK the reset drops (bd-7bpkd, ga-28co77). conn must be the same
// session the caller's preceding checkout ran on.
//
// FKs that were already severed before the reset are left untouched and named
// on stderr, like this package's other operator notices, so the fault reaches
// the operator running the command; the heal for those stays
// `bd doctor --fix`. A re-link failure is returned as a
// *schema.CloneLocalFKRelinkError (the reset itself succeeded).
func resetHardPreservingCloneLocalFKs(ctx context.Context, conn DBConn, target string) error {
	result, err := schema.ResetHardPreservingCloneLocalFKs(ctx, conn, target)
	if w := result.Warning(); w != "" {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", w)
	}
	return err
}

// tempBranchCleanupTimeout bounds cleanupTempBranch.
const tempBranchCleanupTimeout = 30 * time.Second

// cleanupTempBranch returns the session to main and deletes branch after a
// failed flatten or compact (bd-7bpkd, ga-28co77 review round 3).
//
// It runs on a context that keeps ctx's values but not its cancellation or
// deadline, bounded by tempBranchCleanupTimeout. A canceled caller is the most
// likely reason the operation failed; cleaning up on that same context would
// fail both statements at once, leave the session on the temp branch, and
// strand the branch so that every later run fails creating it.
//
// Both statements are always attempted. It returns nil, or one single-line
// error naming every cleanup statement that failed.
func cleanupTempBranch(ctx context.Context, conn DBConn, branch string) error {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), tempBranchCleanupTimeout)
	defer cancel()

	var problems []string
	if _, err := conn.ExecContext(cctx, "CALL DOLT_CHECKOUT('main')"); err != nil {
		problems = append(problems, fmt.Sprintf("checkout main: %v", err))
	}
	//nolint:gosec // G201: branch is one of this package's fixed temp-branch names.
	if _, err := conn.ExecContext(cctx, fmt.Sprintf("CALL DOLT_BRANCH('-D', '%s')", branch)); err != nil {
		problems = append(problems, fmt.Sprintf("delete temp branch %s: %v", branch, err))
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("cleanup also failed: %s", strings.Join(problems, "; "))
}
