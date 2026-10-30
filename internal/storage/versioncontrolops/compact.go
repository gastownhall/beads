package versioncontrolops

import (
	"context"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/storage"
)

// commitDateArg renders t as a DOLT_COMMIT --date argument (#7294). Dolt's
// supported layouts include 2006-01-02T15:04:05Z07:00 while accepting a
// fractional second the layout omits, and it stores commit dates in
// milliseconds (Datetime3), so the .000 fraction keeps intra-second order
// between same-second commits. UTC is the right zone: go-mysql-server
// renders time.Time columns in UTC regardless of session zone, and the
// dolt_log.date values this round-trips against are read back as UTC
// instants.
func commitDateArg(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

// commitHash8 returns the short display form of a commit hash.
func commitHash8(hash string) string {
	return hash[:min(8, len(hash))]
}

// Compact squashes old Dolt commits into a single base commit while preserving
// recent commits via cherry-pick, keeping each replayed commit's original
// commit date (#7294): DOLT_CHERRY_PICK stamps a replayed commit with the
// time of the replay, which erases every timestamp-dependent read made before
// the compaction (AS OF cursors, dolt_history commit_date, tracker
// last_sync). The recipe:
//  1. Read the log to pin the original dates of the boundary and kept
//     commits, refusing before anything is created if either is missing
//  2. Create temp branch at the boundary commit (last old commit)
//  3. Checkout temp branch
//  4. Soft-reset to initial commit (collapses old history into working set)
//  5. Commit as single squashed base
//  6. Cherry-pick each recent commit on top, then amend it back to its
//     original commit date (DOLT_COMMIT --amend --date; without a message it
//     reuses HEAD's, which the cherry-pick already copied from the original)
//  7. Checkout main, hard-reset to temp branch
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

	// Preflight: pin the kept commits' original dates before anything is
	// created, so the replay can restore them and a hash the log does not
	// know refuses before the temp branch exists.
	originals, err := Log(ctx, conn, 0)
	if err != nil {
		return fmt.Errorf("compact: read commit log: %w", err)
	}
	byHash := make(map[string]storage.CommitInfo, len(originals))
	for _, c := range originals {
		byHash[c.Hash] = c
	}
	if _, ok := byHash[boundaryHash]; !ok {
		return fmt.Errorf("compact: commit %s is not in the log of the current branch", boundaryHash)
	}
	keptDates := make([]time.Time, len(recentHashes))
	for i, hash := range recentHashes {
		c, ok := byHash[hash]
		if !ok {
			return fmt.Errorf("compact: commit %s is not in the log of the current branch", hash)
		}
		keptDates[i] = c.Date
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
	for i, hash := range recentHashes {
		if err := execSQL(fmt.Sprintf("cherry-pick %s", commitHash8(hash)), "CALL DOLT_CHERRY_PICK('--allow-empty', ?)", hash); err != nil {
			return err
		}
		// The amend restores the original commit date without touching the
		// replayed content or message; the explicit --allow-empty documents
		// that the amended commit may itself be empty (#3815).
		if err := execSQL(fmt.Sprintf("restore date %s", commitHash8(hash)),
			"CALL DOLT_COMMIT('--amend', '--allow-empty', '--date', ?)",
			commitDateArg(keptDates[i])); err != nil {
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
