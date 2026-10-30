package versioncontrolops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage"
)

// Compact squashes old Dolt commits into a single base commit while preserving
// recent commits via cherry-pick. The recipe:
//  1. Read the log; refuse if the boundary or a recent commit is missing, or
//     a recent commit's committer cannot be restored with --author
//  2. Create temp branch at the boundary commit (last old commit)
//  3. Checkout temp branch
//  4. Soft-reset to initial commit (collapses old history into working set)
//  5. Commit as single squashed base, dated with the boundary commit's date
//  6. Cherry-pick each recent commit on top, amending it back to its original
//     date and committer (cherry-pick records the replay time and the session)
//  7. Re-read the log; refuse unless the replay kept every commit's metadata
//  8. Checkout main, hard-reset to temp branch
//  9. Delete temp branch
//
// Keeping the dates keeps every timestamp recorded before the compaction (a
// tracker's last_sync, an AS OF cursor) meaningful against the rewritten
// history (#7294).
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
	authors := make([]string, len(recentHashes))
	for i, hash := range recentHashes {
		c, ok := byHash[hash]
		if !ok {
			return fmt.Errorf("compact: commit %s is not in the log of the current branch", hash)
		}
		author, err := authorArg(c.Author, c.Email)
		if err != nil {
			return fmt.Errorf("compact: commit %s: %w; a smaller --days squashes it into the base", hash, err)
		}
		originals[i] = c
		authors[i] = author
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
			commitDateArg(originals[i].Date), authors[i]); err != nil {
			return err
		}
	}

	// Main has not moved yet: refuse here if the server applied the dates or
	// identities differently, and the deferred cleanup drops the temp branch.
	if err := verifyCompactReplay(ctx, conn, boundary, originals); err != nil {
		return err
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

// authorArg renders a committer identity for DOLT_COMMIT --author. Dolt parses
// --author with the unanchored regex `([^)]+) \<([^)]+)` and then strips every
// '>' from the email, so an identity that would not round-trip is refused
// rather than restored altered. An empty email ("name <>") does round-trip.
func authorArg(name, email string) (string, error) {
	if name == "" || strings.Contains(name, ")") || strings.ContainsAny(email, ")<>") {
		return "", fmt.Errorf("committer %q <%s> cannot be restored with DOLT_COMMIT --author", name, email)
	}
	return fmt.Sprintf("%s <%s>", name, email), nil
}

// verifyCompactReplay re-reads the log of the rebuilt branch and checks that
// the replayed commits carry the originals' committer, email, date and
// message, in order, and that the squashed base carries the boundary's date.
func verifyCompactReplay(ctx context.Context, conn DBConn, boundary storage.CommitInfo, originals []storage.CommitInfo) error {
	rebuilt, err := Log(ctx, conn, 0)
	if err != nil {
		return fmt.Errorf("compact: read back: %w", err)
	}
	sort.SliceStable(rebuilt, func(i, j int) bool { return rebuilt[i].CommitOrder < rebuilt[j].CommitOrder })
	if len(rebuilt) < len(originals)+1 {
		return fmt.Errorf("compact: rebuilt branch has %d commits, want at least %d", len(rebuilt), len(originals)+1)
	}
	replayed := rebuilt[len(rebuilt)-len(originals):]
	for i, want := range originals {
		got := replayed[i]
		short := want.Hash[:min(8, len(want.Hash))]
		switch {
		case got.Author != want.Author:
			return fmt.Errorf("compact: replayed %s has committer %q, want %q", short, got.Author, want.Author)
		case got.Email != want.Email:
			return fmt.Errorf("compact: replayed %s has email %q, want %q", short, got.Email, want.Email)
		case !got.Date.Equal(want.Date):
			return fmt.Errorf("compact: replayed %s has date %s, want %s", short, commitDateArg(got.Date), commitDateArg(want.Date))
		case got.Message != want.Message:
			return fmt.Errorf("compact: replayed %s has message %q, want %q", short, got.Message, want.Message)
		}
	}
	if base := rebuilt[len(rebuilt)-len(originals)-1]; !base.Date.Equal(boundary.Date) {
		return fmt.Errorf("compact: squashed base has date %s, want the boundary's %s", commitDateArg(base.Date), commitDateArg(boundary.Date))
	}
	return nil
}
