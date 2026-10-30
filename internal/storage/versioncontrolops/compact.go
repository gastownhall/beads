package versioncontrolops

import (
	"context"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/storage"
)

// Compact squashes old Dolt commits into a single base commit while preserving
// recent commits via cherry-pick. The recipe:
//  1. Create temp branch at the boundary commit (last old commit)
//  2. Checkout temp branch
//  3. Soft-reset to initial commit (collapses old history into working set)
//  4. Commit as single squashed base
//  5. Cherry-pick each recent commit on top
//  6. Checkout main, hard-reset to temp branch
//  7. Delete temp branch
//
// Callers should run PruneRemoteRefs and then DoltGC afterward to reclaim disk
// space — remote-tracking refs still anchor the pre-compact chain, and GC
// alone reclaims nothing while they exist (bd-agctw).
//
// conn must be a single database connection (not a pooled *sql.DB) since the
// stored procedures rely on session-scoped state (current branch, working set).
func Compact(ctx context.Context, conn DBConn, initialHash, boundaryHash string, oldCommits int, recentHashes []string) (retErr error) {
	// Read the original metadata before touching history: DOLT_CHERRY_PICK
	// dates each replayed commit at replay time, so the kept commits are
	// amended back to their original dates below (#7294).
	logEntries, err := Log(ctx, conn, 0)
	if err != nil {
		return fmt.Errorf("compact: %w", err)
	}
	byHash := make(map[string]storage.CommitInfo, len(logEntries))
	for _, c := range logEntries {
		byHash[c.Hash] = c
	}
	boundary, ok := byHash[boundaryHash]
	if !ok {
		return fmt.Errorf("compact: commit %s is not in the log of the current branch", boundaryHash)
	}
	originals := make([]storage.CommitInfo, len(recentHashes))
	for i, hash := range recentHashes {
		c, ok := byHash[hash]
		if !ok {
			return fmt.Errorf("compact: commit %s is not in the log of the current branch", hash)
		}
		originals[i] = c
	}

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
	// The base holds the boundary commit's tree, so it takes the boundary's
	// date: AS OF any time up to the first kept commit still resolves to it.
	msg := fmt.Sprintf("compact: squash %d commits into base snapshot", oldCommits)
	if err := execSQL("commit squashed base", "CALL DOLT_COMMIT('-Am', ?, '--date', ?)", msg, commitDateArg(boundary.Date)); err != nil {
		return err
	}

	// --allow-empty: the preserved window can contain empty commits (a Dolt
	// auto-commit with no table change, or a bd create double-commit whose
	// leading member has 0 changed tables). Without this flag DOLT_CHERRY_PICK
	// aborts the entire replay at the first empty commit with Error 1105
	// ("The previous cherry-pick commit is empty. Use --allow-empty ..."),
	// leaving compaction permanently blocked on active databases. See #3815.
	//
	// Each pick is then amended with the original commit date and identity:
	// cherry-pick records the SQL session as committer at replay time, and
	// AS OF, dolt_history and dolt_log all read the committer. No -m: --amend
	// keeps the message cherry-pick copied.
	for i, hash := range recentHashes {
		short := hash[:min(8, len(hash))]
		if err := execSQL(fmt.Sprintf("cherry-pick %s", short), "CALL DOLT_CHERRY_PICK('--allow-empty', ?)", hash); err != nil {
			return err
		}
		if err := execSQL(fmt.Sprintf("restore metadata %s", short),
			"CALL DOLT_COMMIT('--amend', '--allow-empty', '--date', ?, '--author', ?)",
			commitDateArg(originals[i].Date), authorArg(originals[i].Author, originals[i].Email)); err != nil {
			return err
		}
	}

	if err := execSQL("checkout main", "CALL DOLT_CHECKOUT('main')"); err != nil {
		return err
	}
	if err := execSQL("reset main to compacted", "CALL DOLT_RESET('--hard', 'compact-tmp')"); err != nil {
		return err
	}
	if err := execSQL("delete temp branch", "CALL DOLT_BRANCH('-D', 'compact-tmp')"); err != nil {
		return err
	}

	return nil
}

// commitDateArg renders t for DOLT_COMMIT --date. Dolt stores commit dates to
// the millisecond, so the fraction is kept: commits made within the same
// second keep their order.
func commitDateArg(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

// authorArg renders a committer identity for DOLT_COMMIT --author.
func authorArg(name, email string) string {
	return fmt.Sprintf("%s <%s>", name, email)
}
