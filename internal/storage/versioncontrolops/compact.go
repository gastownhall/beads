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
//  6. Checkout main, verify the temp branch's committed data equals main's
//  7. Soft-reset main to the temp branch (keeps main's working set)
//  8. Commit main's staged set, if any: a commit that reached main while
//     the history was rebuilt
//  9. Delete temp branch
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
	// The replay must reproduce main's committed data exactly: a cherry-pick
	// can apply cleanly yet resurrect an edit the squashed base had already
	// undone (#6645). Compare the committed refs and abort before the swap on
	// any difference. This is not atomic with the reset below.
	var changedTables int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_diff_summary('main', 'compact-tmp')").Scan(&changedTables); err != nil {
		return fmt.Errorf("compact step %q: %w", "verify replay", err)
	}
	if changedTables != 0 {
		return fmt.Errorf("compact aborted: replayed history differs from main in %d table(s); main was not reset", changedTables)
	}
	// --soft moves main's HEAD and keeps its staged and working sets. compact-tmp's
	// tree equals main's tree when the plan was read, so whatever main holds
	// beyond it stays on main: rows its working set has that no Dolt commit
	// holds yet (bd's window between the SQL commit and DOLT_COMMIT, or a whole
	// batch under batched auto-commit), and a commit that reached main while
	// the history was rebuilt, whose rows come back staged. --hard discarded
	// both, after their writers had been told they succeeded.
	if err := execSQL("reset main to compacted", "CALL DOLT_RESET('--soft', 'compact-tmp')"); err != nil {
		return err
	}

	// A commit that reached main after the plan was read is now staged on
	// main, outside HEAD and so outside a push of main. Commit the staged set
	// only: rows that are just in the working set stay pending, as they were,
	// so a writer's batch keeps its own commit boundary. Rows that were
	// already staged when Compact ran join this commit too. The landed commit
	// does not keep its own hash or message on main: compaction rewrites that
	// history anyway.
	const followupStep = "commit changes that reached main"
	staged, err := issueops.HasStagedChanges(ctx, conn)
	if err != nil {
		return fmt.Errorf("compact step %q: %w", followupStep, err)
	}
	if staged {
		err = schema.DrainCall(ctx, conn, "CALL DOLT_COMMIT('-m', ?)",
			"compact: commit changes that reached main during compaction")
		// Another writer committing main's staged set first leaves nothing
		// to commit, which is the outcome this step wants.
		if err != nil && !issueops.IsNothingToCommitError(err) {
			return fmt.Errorf("compact step %q: %w", followupStep, err)
		}
	}

	if err := execSQL("delete temp branch", "CALL DOLT_BRANCH('-D', 'compact-tmp')"); err != nil {
		return err
	}

	return nil
}
