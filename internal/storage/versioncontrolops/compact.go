package versioncontrolops

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// Compact squashes old Dolt commits into a single base commit while preserving
// recent commits via cherry-pick. The recipe:
//  1. Create temp branch at the boundary commit (last old commit)
//  2. Checkout temp branch
//  3. Soft-reset to initial commit (collapses old history into working set)
//  4. Commit as single squashed base
//  5. Cherry-pick each recent commit on top
//  6. Checkout main, soft-reset to temp branch (a change that reached main
//     while the rebuild ran — an uncommitted working-set row, or a commit
//     landing in the window before the reset — survives in main's staged
//     and working roots instead of being destroyed)
//  7. Commit whatever the reset left staged, so main is never left dirty.
//     A commit that landed during the rebuild is folded in: its rows
//     survive, its own hash and message do not. Already-staged changes can
//     fold in too; unstaged working-set changes stay pending.
//  8. Delete temp branch
//
// Callers should run PruneRemoteRefs and then DoltGC afterward to reclaim disk
// space — remote-tracking refs still anchor the pre-compact chain, and GC
// alone reclaims nothing while they exist (bd-agctw).
//
// conn must be a single database connection (not a pooled *sql.DB) since the
// stored procedures rely on session-scoped state (current branch, working set).
func Compact(ctx context.Context, conn DBConn, initialHash, boundaryHash string, oldCommits int, recentHashes []string) (retErr error) {
	branchCreated := false

	// Best-effort cleanup: if any step fails after creating the temp branch,
	// try to return to main and delete the temp branch so future compactions
	// aren't blocked by a leftover branch.
	defer func() {
		if retErr != nil && branchCreated {
			_, _ = conn.ExecContext(ctx, "CALL DOLT_CHECKOUT('main')")
			_, _ = conn.ExecContext(ctx, "CALL DOLT_BRANCH('-D', 'compact-tmp')")
		}
	}()

	execSQL := func(name, query string, args ...interface{}) error {
		if _, err := conn.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("compact step %q: %w", name, err)
		}
		return nil
	}

	if err := execSQL("create temp branch", "CALL DOLT_BRANCH('compact-tmp', ?)", boundaryHash); err != nil {
		return err
	}
	branchCreated = true

	if err := execSQL("checkout temp", "CALL DOLT_CHECKOUT('compact-tmp')"); err != nil {
		return err
	}
	if err := execSQL("soft reset to initial", "CALL DOLT_RESET('--soft', ?)", initialHash); err != nil {
		return err
	}
	msg := fmt.Sprintf("compact: squash %d commits into base snapshot", oldCommits)
	if err := execSQL("commit squashed base", "CALL DOLT_COMMIT('-Am', ?)", msg); err != nil {
		return err
	}

	// --allow-empty: the preserved window can contain empty commits (a Dolt
	// auto-commit with no table change, or a bd create double-commit whose
	// leading member has 0 changed tables). Without this flag DOLT_CHERRY_PICK
	// aborts the entire replay at the first empty commit with Error 1105
	// ("The previous cherry-pick commit is empty. Use --allow-empty ..."),
	// leaving compaction permanently blocked on active databases. See #3815.
	for _, hash := range recentHashes {
		if err := execSQL(fmt.Sprintf("cherry-pick %s", hash[:min(8, len(hash))]), "CALL DOLT_CHERRY_PICK('--allow-empty', ?)", hash); err != nil {
			return err
		}
	}

	if err := execSQL("checkout main", "CALL DOLT_CHECKOUT('main')"); err != nil {
		return err
	}
	// Soft, not hard: main's staged and working roots must survive the
	// reset. The rebuilt tree equals the planned head's tree, so everything
	// dolt_status reports after the reset is exactly what reached main
	// outside the compaction plan — a row main holds between its SQL commit
	// and its DOLT_COMMIT (or a whole batch under batched auto-commit), or a
	// commit another session landed during the rebuild. A hard reset here
	// destroyed those writes after their writers had been told they
	// succeeded (GH#7789).
	if err := execSQL("reset main to compacted", "CALL DOLT_RESET('--soft', 'compact-tmp')"); err != nil {
		return err
	}

	// The soft reset kept main's staged root, so whatever dolt_status now
	// reports as staged is exactly what reached main outside the compaction
	// plan: commit it, so main is never left dirty and a landed write is
	// reachable from HEAD. No DOLT_ADD: staging nothing extra keeps
	// unstaged working-set changes (a batch waiting for its DOLT_COMMIT,
	// ignored tables) pending, as their writers left them. Another writer
	// can consume the staged set between the check and the commit; the
	// nothing-to-commit that follows is benign and tolerated.
	staged, err := issueops.HasStagedChanges(ctx, conn)
	if err != nil {
		return fmt.Errorf("compact step %q: %w", "check staged changes after reset", err)
	}
	if staged {
		if err := schema.DrainCall(ctx, conn, "CALL DOLT_COMMIT('-m', ?)", "compact: commit changes that reached main during compaction"); err != nil && !issueops.IsNothingToCommitError(err) {
			return fmt.Errorf("compact step %q: %w", "commit changes that reached main during compaction", err)
		}
	}

	if err := execSQL("delete temp branch", "CALL DOLT_BRANCH('-D', 'compact-tmp')"); err != nil {
		return err
	}

	return nil
}
