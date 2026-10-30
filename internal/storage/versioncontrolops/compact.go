package versioncontrolops

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"sort"
	"time"
)

// commitDateArg renders t as a Dolt commit date (#7294), the form both
// DOLT_COMMIT's --date and the @@dolt_committer_date session variable
// parse. Dolt's supported layouts include 2006-01-02T15:04:05Z07:00 while
// accepting a fractional second the layout omits, and it stores commit
// dates in milliseconds (Datetime3), so the .000 fraction keeps
// intra-second order between same-second commits. UTC is the right zone:
// go-mysql-server renders time.Time columns in UTC regardless of session
// zone, and the dolt_log dates this round-trips against are read back as
// UTC instants.
func commitDateArg(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

// commitHash8 returns the short display form of a commit hash.
func commitHash8(hash string) string {
	return hash[:min(8, len(hash))]
}

// compactCommit is one dolt_log row as Compact reads it. A Dolt commit
// carries two identities, each with its own date. The committer and its
// date are what dolt_log.date, AS OF and dolt_history read; the replay
// restores them. The author, author date and message are what
// DOLT_CHERRY_PICK copies from the original; the read-back checks that it
// did (#7294).
type compactCommit struct {
	Hash           string
	Committer      string
	CommitterEmail string
	CommitterDate  time.Time
	Author         string
	AuthorEmail    string
	AuthorDate     time.Time
	Message        string
	CommitOrder    int64
}

// readCompactLog reads every commit of the current branch with both
// identities (Log reads only the committer).
func readCompactLog(ctx context.Context, conn DBConn) ([]compactCommit, error) {
	rows, err := conn.QueryContext(ctx,
		"SELECT commit_hash, committer, email, date, author, author_email, author_date, message, commit_order FROM dolt_log")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var commits []compactCommit
	for rows.Next() {
		var c compactCommit
		if err := rows.Scan(&c.Hash, &c.Committer, &c.CommitterEmail, &c.CommitterDate,
			&c.Author, &c.AuthorEmail, &c.AuthorDate, &c.Message, &c.CommitOrder); err != nil {
			return nil, err
		}
		commits = append(commits, c)
	}
	return commits, rows.Err()
}

// restorable refuses a commit the replay cannot write back as it was. Dolt
// refuses to write a commit whose author or committer has an empty name or
// email, and an empty @@dolt_committer_* value falls back to the session's
// own identity, so either would fail the replay or its read-back after the
// temp branch exists.
func (c compactCommit) restorable() error {
	for _, f := range [...]struct{ field, value string }{
		{"committer name", c.Committer}, {"committer email", c.CommitterEmail},
		{"author name", c.Author}, {"author email", c.AuthorEmail},
	} {
		if f.value == "" {
			return fmt.Errorf("%s is empty", f.field)
		}
	}
	return nil
}

// sameMetadata reports whether a rebuilt commit carries everything its
// original did: both identities, both dates and the message. The hash and
// the commit order differ by construction.
func (c compactCommit) sameMetadata(o compactCommit) bool {
	return c.Committer == o.Committer && c.CommitterEmail == o.CommitterEmail && c.CommitterDate.Equal(o.CommitterDate) &&
		c.Author == o.Author && c.AuthorEmail == o.AuthorEmail && c.AuthorDate.Equal(o.AuthorDate) &&
		c.Message == o.Message
}

// metadata renders what sameMetadata compares, for a refusal.
func (c compactCommit) metadata() string {
	return fmt.Sprintf("committer %s <%s> %s, author %s <%s> %s, message %q",
		c.Committer, c.CommitterEmail, c.CommitterDate.UTC(), c.Author, c.AuthorEmail, c.AuthorDate.UTC(), c.Message)
}

// compactOriginals pins what the replay must preserve before anything is
// created: the boundary commit, whose date the squashed base takes, and
// each kept commit, in recentHashes order. A hash the log does not know, or
// a kept commit the replay cannot restore, refuses here, before the temp
// branch exists.
func compactOriginals(ctx context.Context, conn DBConn, boundaryHash string, recentHashes []string) (compactCommit, []compactCommit, error) {
	commits, err := readCompactLog(ctx, conn)
	if err != nil {
		return compactCommit{}, nil, fmt.Errorf("compact: read commit log: %w", err)
	}
	byHash := make(map[string]compactCommit, len(commits))
	for _, c := range commits {
		byHash[c.Hash] = c
	}
	boundary, ok := byHash[boundaryHash]
	if !ok {
		return compactCommit{}, nil, fmt.Errorf("compact: commit %s is not in the log of the current branch", boundaryHash)
	}
	kept := make([]compactCommit, len(recentHashes))
	for i, hash := range recentHashes {
		c, ok := byHash[hash]
		if !ok {
			return compactCommit{}, nil, fmt.Errorf("compact: commit %s is not in the log of the current branch", hash)
		}
		if err := c.restorable(); err != nil {
			return compactCommit{}, nil, fmt.Errorf("compact: commit %s: %w; compact a smaller window with --days", commitHash8(hash), err)
		}
		kept[i] = c
	}
	return boundary, kept, nil
}

// replayKept cherry-picks each kept commit, in order, onto the current
// branch. DOLT_CHERRY_PICK copies the original's author, author date and
// message, but takes the committer from the session and dates it at the
// time of the replay, and the committer date is the one dolt_log.date, AS
// OF and dolt_history read (#7294). So the session's committer is set to
// the original's before each pick, and cleared once the replay ends,
// however it ends.
//
// --allow-empty: the preserved window can contain empty commits (a Dolt
// auto-commit with no table change, or a bd create double-commit whose
// leading member has 0 changed tables). Without this flag DOLT_CHERRY_PICK
// aborts the entire replay at the first empty commit with Error 1105
// ("The previous cherry-pick commit is empty. Use --allow-empty ..."),
// leaving compaction permanently blocked on active databases. See #3815.
func replayKept(ctx context.Context, conn DBConn, kept []compactCommit) (retErr error) {
	defer func() { retErr = errors.Join(retErr, clearCommitter(ctx, conn)) }()
	for _, c := range kept {
		if _, err := conn.ExecContext(ctx,
			"SET @@dolt_committer_name = ?, @@dolt_committer_email = ?, @@dolt_committer_date = ?",
			c.Committer, c.CommitterEmail, commitDateArg(c.CommitterDate)); err != nil {
			return fmt.Errorf("compact step %q: %w", "set committer "+commitHash8(c.Hash), err)
		}
		if _, err := conn.ExecContext(ctx, "CALL DOLT_CHERRY_PICK('--allow-empty', ?)", c.Hash); err != nil {
			return fmt.Errorf("compact step %q: %w", "cherry-pick "+commitHash8(c.Hash), err)
		}
	}
	return nil
}

// errCommitterNotCleared is the failed step that leaves the session's
// committer set to the last replayed one.
var errCommitterNotCleared = errors.New(`compact step "clear committer"`)

// clearCommitter resets the session's committer variables to their unset
// default, so later commits on the connection are stamped with the
// session's own identity and the time of writing again. A failed reset
// wraps errCommitterNotCleared, for abandonCompaction.
func clearCommitter(ctx context.Context, conn DBConn) error {
	if _, err := conn.ExecContext(ctx,
		"SET @@dolt_committer_name = '', @@dolt_committer_email = '', @@dolt_committer_date = ''"); err != nil {
		return fmt.Errorf("%w: %w", errCommitterNotCleared, err)
	}
	return nil
}

// abandonCompaction is Compact's best-effort cleanup once a step fails
// after the temp branch exists: back to main, and the temp branch deleted.
// A connection whose committer could not be cleared would stamp later
// commits with the last replayed committer and date once it is back in its
// pool, so a pinned *sql.Conn is then discarded, but only after the
// cleanup: discarding it ends the session, and a cleanup that never
// reached the server would leave compact-tmp to block every later
// compaction.
func abandonCompaction(ctx context.Context, conn DBConn, cause error) {
	_, _ = conn.ExecContext(ctx, "CALL DOLT_CHECKOUT('main')")
	_, _ = conn.ExecContext(ctx, "CALL DOLT_BRANCH('-D', 'compact-tmp')")
	if !errors.Is(cause, errCommitterNotCleared) {
		return
	}
	if pinned, ok := conn.(interface{ Raw(func(any) error) error }); ok {
		_ = pinned.Raw(func(any) error { return driver.ErrBadConn })
	}
}

// verifyRebuilt reads the rebuilt chain back before main moves onto it.
// Sorted by commit_order, the newest len(kept) rows must carry each kept
// commit's identities, dates and message, and the row below them is the
// squashed base, dated as the boundary. kept arrives in ascending
// commit_order (planCompaction's contract), the order the replay created
// the rebuilt commits in.
func verifyRebuilt(ctx context.Context, conn DBConn, boundary compactCommit, kept []compactCommit) error {
	rebuilt, err := readCompactLog(ctx, conn)
	if err != nil {
		return fmt.Errorf("compact: read rebuilt log: %w", err)
	}
	sort.Slice(rebuilt, func(i, j int) bool { return rebuilt[i].CommitOrder < rebuilt[j].CommitOrder })
	if len(rebuilt) < len(kept)+1 {
		return fmt.Errorf("compact: rebuilt log holds %d commits, want at least the squashed base plus %d replayed", len(rebuilt), len(kept))
	}
	replayed := rebuilt[len(rebuilt)-len(kept):]
	for i, want := range kept {
		if !replayed[i].sameMetadata(want) {
			return fmt.Errorf("compact: rebuilt commit for %s does not preserve the original: rebuilt %s; original %s",
				commitHash8(want.Hash), replayed[i].metadata(), want.metadata())
		}
	}
	if base := rebuilt[len(rebuilt)-len(kept)-1]; !base.CommitterDate.Equal(boundary.CommitterDate) {
		return fmt.Errorf("compact: rebuilt squashed base is dated %s, want boundary %s's date %s",
			base.CommitterDate.UTC(), commitHash8(boundary.Hash), boundary.CommitterDate.UTC())
	}
	return nil
}

// Compact squashes old Dolt commits into a single base commit while preserving
// recent commits via cherry-pick, keeping each replayed commit's original
// committer and commit date (#7294): DOLT_CHERRY_PICK stamps a replayed
// commit with the session's identity and the time of the replay, which
// erases every timestamp-dependent read made before the compaction (AS OF
// cursors, dolt_history commit_date, tracker last_sync). The recipe:
//  1. Read the log to pin what the replay must preserve: the boundary
//     commit's date, and each kept commit's identities, dates and message;
//     a missing commit, or one Dolt cannot write back, refuses before
//     anything is created
//  2. Create temp branch at the boundary commit (last old commit)
//  3. Checkout temp branch
//  4. Soft-reset to initial commit (collapses old history into working set)
//  5. Commit as single squashed base, dated as the boundary commit so AS OF
//     reads between the boundary and the first kept commit still resolve to
//     the pre-compaction state
//  6. Cherry-pick each recent commit on top, with the session's committer
//     set to the original's; the cherry-pick itself keeps the original
//     author, author date and message
//  7. Read back the rebuilt log and verify it: sorted by commit_order, the
//     newest len(recent) rows must match each kept commit's identities,
//     dates and message, and the row below them is the base dated as the
//     boundary; any mismatch refuses before main moves
//  8. Checkout main, hard-reset to temp branch
//  9. Delete temp branch
//
// Callers should run PruneRemoteRefs and then DoltGC afterward to reclaim disk
// space — remote-tracking refs still anchor the pre-compact chain, and GC
// alone reclaims nothing while they exist (bd-agctw).
//
// conn must be a single database connection (not a pooled *sql.DB) since the
// stored procedures rely on session-scoped state (current branch, working
// set, committer).
func Compact(ctx context.Context, conn DBConn, initialHash, boundaryHash string, oldCommits int, recentHashes []string) (retErr error) {
	execSQL := func(name, query string, args ...interface{}) error {
		if _, err := conn.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("compact step %q: %w", name, err)
		}
		return nil
	}

	boundary, kept, err := compactOriginals(ctx, conn, boundaryHash, recentHashes)
	if err != nil {
		return err
	}

	if err := execSQL("create temp branch", "CALL DOLT_BRANCH('compact-tmp', ?)", boundaryHash); err != nil {
		return err
	}

	// Best-effort cleanup: if any step fails after creating the temp branch,
	// try to return to main and delete the temp branch so future compactions
	// aren't blocked by a leftover branch.
	defer func() {
		if retErr != nil {
			abandonCompaction(ctx, conn, retErr)
		}
	}()

	if err := execSQL("checkout temp", "CALL DOLT_CHECKOUT('compact-tmp')"); err != nil {
		return err
	}
	if err := execSQL("soft reset to initial", "CALL DOLT_RESET('--soft', ?)", initialHash); err != nil {
		return err
	}
	msg := fmt.Sprintf("compact: squash %d commits into base snapshot", oldCommits)
	// --date: the squashed base inherits the boundary commit's date, so AS OF
	// reads between the boundary and the first kept commit still resolve to
	// the pre-compaction state (#7294).
	if err := execSQL("commit squashed base", "CALL DOLT_COMMIT('-Am', ?, '--date', ?)",
		msg, commitDateArg(boundary.CommitterDate)); err != nil {
		return err
	}

	if err := replayKept(ctx, conn, kept); err != nil {
		return err
	}
	// A rebuilt chain that does not read back as its original refuses here,
	// while main has not moved; the deferred cleanup deletes the temp branch.
	if err := verifyRebuilt(ctx, conn, boundary, kept); err != nil {
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
