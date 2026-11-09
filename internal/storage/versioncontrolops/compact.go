package versioncontrolops

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/schema"
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

// compactVerifySQL counts the table differences between a commit and the
// temp branch: the replay check runs it against the planned head, and the
// catch-up check against main's landed head.
const compactVerifySQL = "SELECT COUNT(*) FROM dolt_diff_summary(?, 'compact-tmp')"

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

// catchUpHeadQuery reads main's current head: the commit any writer that
// reached main since the plan was read left it at. HASHOF resolves a branch
// name on every engine bd serves (issueops documents the DOLT_HASHOF/HASHOF
// pair), and 'main' is branch-stable, so the read names main however the
// session's current branch moves during the compaction.
const catchUpHeadQuery = "SELECT HASHOF('main')"

// catchUpLogQuery reads the commits of the landed range — everything
// reachable from main's current head but not from the planned head — with
// the metadata the replay preserves and each commit's parents, which only
// the --parents option populates. The range is bound as one argument, so the
// commit set is fixed even if main moves again mid-read; dolt_log's deferred
// argument parsing (dtablefunctions/dolt_log.go) evaluates the bind variable
// at execution.
const catchUpLogQuery = "SELECT commit_hash, committer, email, date, author, author_email, author_date, message, commit_order, parents FROM dolt_log(?, '--parents')"

// catchUpChain reads the commits that reached main between the planned head
// and main's current head and returns them oldest first when they form a
// chain the replay can carry: main's head walks single parents down to the
// planned head, every row visited exactly once, no cycles, gaps, duplicates,
// roots or merges, and every identity one Dolt can write back. Any other
// shape returns ok=false, and the caller leaves the range to the soft
// reset's staged follow-up instead of refusing: those commits are another
// writer's work, and the follow-up keeps their rows without replaying them.
func catchUpChain(ctx context.Context, conn DBConn, headHash, landed string) (chain []compactCommit, ok bool, err error) {
	rows, err := conn.QueryContext(ctx, catchUpLogQuery, headHash+".."+landed)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	byHash := make(map[string]compactCommit)
	parents := make(map[string][]string)
	for rows.Next() {
		var c compactCommit
		var parentList sql.NullString
		if err := rows.Scan(&c.Hash, &c.Committer, &c.CommitterEmail, &c.CommitterDate,
			&c.Author, &c.AuthorEmail, &c.AuthorDate, &c.Message, &c.CommitOrder, &parentList); err != nil {
			return nil, false, err
		}
		if _, duplicate := byHash[c.Hash]; duplicate {
			return nil, false, nil
		}
		byHash[c.Hash] = c
		if parentList.Valid {
			parents[c.Hash] = strings.Split(parentList.String, ", ")
		}
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}

	// Walk single parents from main's head down to the planned head. The
	// planned head is outside the range, so reaching it is the walk's success;
	// a hash the range does not hold is a gap, a revisit is a cycle, an empty
	// parent list a root the replay would strand, and two parents a merge the
	// cherry-pick replay cannot carry.
	var walked []compactCommit
	visited := make(map[string]bool)
	for current := landed; ; {
		if visited[current] {
			return nil, false, nil
		}
		visited[current] = true
		c, inRange := byHash[current]
		if !inRange {
			return nil, false, nil
		}
		ps := parents[current]
		if len(ps) != 1 {
			return nil, false, nil
		}
		walked = append(walked, c)
		if ps[0] == headHash {
			break
		}
		current = ps[0]
	}
	if len(visited) != len(byHash) {
		return nil, false, nil // rows outside the walked chain
	}
	for _, c := range walked {
		if err := c.restorable(); err != nil {
			return nil, false, nil
		}
	}
	// Oldest first: the replay cherry-picks every parent before its children.
	chain = make([]compactCommit, len(walked))
	for i, c := range walked {
		chain[len(walked)-1-i] = c
	}
	return chain, true, nil
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
//  8. Checkout main, verify the temp branch's committed data equals headHash's
//  9. Catch up: read main's head, and when it moved off headHash and the
//     commits in between form a linear chain the replay can carry, replay
//     them onto the temp branch the same way (message, identities and dates
//     preserved), then verify the rebuilt chain and that it reproduces
//     main's landed tree; a range the replay cannot carry (a merge, a gap, an
//     identity Dolt cannot write back) skips catch-up and falls back to the
//     staged follow-up of step 10; a commit that lands after the snapshot
//     read is outside catch-up by contract
//  10. Soft-reset main to the temp branch (keeps main's working set)
//  11. Commit main's staged set, if any: a commit that reached main while
//     the history was rebuilt or after catch-up's snapshot
//  12. Delete temp branch
//
// headHash is the commit main pointed at when the caller read the log it
// planned from (the log's highest commit_order). The replay must reproduce
// that commit's data, not main's current HEAD's: a commit that lands on main
// after the plan is read is a concurrent write, which steps 9 and 10 keep,
// not a divergent replay.
//
// Callers should run PruneRemoteRefs and then DoltGC afterward to reclaim disk
// space — remote-tracking refs still anchor the pre-compact chain, and GC
// alone reclaims nothing while they exist (bd-agctw).
//
// conn must be a single database connection (not a pooled *sql.DB) since the
// stored procedures rely on session-scoped state (current branch, working
// set, committer).
func Compact(ctx context.Context, conn DBConn, initialHash, boundaryHash, headHash string, oldCommits int, recentHashes []string) (retErr error) {
	if headHash == "" {
		return fmt.Errorf("compact: the planned head commit is required to verify the replay")
	}
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
	// The replay must reproduce the planned head's committed data exactly: a
	// cherry-pick can apply cleanly yet resurrect an edit the squashed base had
	// already undone (#6645). Compare it with headHash, the commit the plan was
	// read from, and abort before main moves on any difference. Comparing with
	// main's current HEAD instead would refuse every compaction during which
	// another writer committed, reporting that write as a divergent replay.
	// Neither side of this comparison is main: headHash is a fixed commit and
	// compact-tmp is this compaction's own branch. So a write that lands on
	// main between this check and the reset below does not invalidate it; the
	// soft reset keeps that write.
	var changedTables int
	if err := conn.QueryRowContext(ctx, compactVerifySQL, headHash).Scan(&changedTables); err != nil {
		return fmt.Errorf("compact step %q: %w", "verify replay", err)
	}
	if changedTables != 0 {
		return fmt.Errorf("compact aborted: replayed history differs from main's planned head %s in %d table(s); main was not reset",
			headHash[:min(8, len(headHash))], changedTables)
	}

	// Catch-up (#7502): a commit that reached main after the plan was read is
	// still in main's tree when the reset runs, but only outside HEAD: the
	// staged follow-up below re-writes its rows under a compaction commit that
	// keeps neither its message nor its identity, and a writer whose SQL
	// transaction began on that landed head and finishes after the reset can
	// demote it out of HEAD entirely (Dolt merges the moved HEAD against the
	// transaction-start head, so the head move reads as the writer reverting
	// the landed commit). When the landed commits form a linear chain the
	// replay can carry, replay them onto compact-tmp first — each keeping its
	// message, identities and dates — so the reset moves main to the tree its
	// writers already saw. A range the replay cannot carry (a merge, a gap, an
	// identity Dolt cannot write back) is left to the staged follow-up, as
	// before. A commit that lands after this snapshot is outside the catch-up
	// by contract: the follow-up keeps it, and a writer straddling the reset
	// can leave its rows only in the working set, recoverable with
	// `bd dolt commit` (the docs' residual contract; full atomicity needs a
	// conditional head move Dolt does not offer).
	var landed string
	if err := conn.QueryRowContext(ctx, catchUpHeadQuery).Scan(&landed); err != nil {
		return fmt.Errorf("compact step %q: %w", "read main's head", err)
	}
	if landed != headHash {
		caught, replayable, err := catchUpChain(ctx, conn, headHash, landed)
		if err != nil {
			return fmt.Errorf("compact step %q: %w", "read commits that reached main", err)
		}
		if replayable {
			if err := execSQL("checkout temp for catch-up", "CALL DOLT_CHECKOUT('compact-tmp')"); err != nil {
				return err
			}
			if err := replayKept(ctx, conn, caught); err != nil {
				return err
			}
			// The caught-up commits join the kept ones in one read-back: the
			// rebuilt chain must carry every original's identities, dates and
			// message, and the row below the whole replay stays the squashed
			// base dated as the boundary.
			all := make([]compactCommit, 0, len(kept)+len(caught))
			all = append(all, kept...)
			all = append(all, caught...)
			if err := verifyRebuilt(ctx, conn, boundary, all); err != nil {
				return err
			}
			if err := execSQL("checkout main after catch-up", "CALL DOLT_CHECKOUT('main')"); err != nil {
				return err
			}
			// The caught-up chain must reproduce main's landed tree exactly,
			// the same guarantee the kept replay carries against the planned
			// head. landed is an immutable commit, so a write landing on main
			// between this check and the reset below does not invalidate it.
			var caughtTables int
			if err := conn.QueryRowContext(ctx, compactVerifySQL, landed).Scan(&caughtTables); err != nil {
				return fmt.Errorf("compact step %q: %w", "verify catch-up", err)
			}
			if caughtTables != 0 {
				return fmt.Errorf("compact aborted: caught-up history differs from main's head %s in %d table(s); main was not reset",
					commitHash8(landed), caughtTables)
			}
		}
	}
	// --soft moves main's HEAD and keeps its staged and working sets.
	// compact-tmp's tree equals main's head at the catch-up check above
	// (headHash's when nothing landed, the landed head's once caught up), so
	// whatever main holds beyond it stays on main: rows its working set has
	// that no Dolt commit holds yet (bd's window between the SQL commit and
	// DOLT_COMMIT, or a whole batch under batched auto-commit), and a commit
	// that reached main after catch-up's snapshot, whose rows come back
	// staged. --hard discarded both, after their writers had been told they
	// succeeded.
	if err := execSQL("reset main to compacted", "CALL DOLT_RESET('--soft', 'compact-tmp')"); err != nil {
		return err
	}

	// A commit that reached main after catch-up's snapshot is now staged on
	// main, outside HEAD and so outside a push of main. Commit the staged set
	// only: rows that are just in the working set stay pending, as they were,
	// so a writer's batch keeps its own commit boundary. Rows that were
	// already staged when Compact ran join this commit too. A commit the
	// catch-up did not replay — it landed after the snapshot, or its range
	// was not replayable — does not keep its own hash or message on main:
	// the catch-up already replayed everything before the snapshot with both.
	//
	// main has already moved when either step below fails, so the error says
	// so and what to do with what main kept. The move is not rolled back: a
	// hard reset here would discard the writes the soft reset kept. The
	// caller stops before garbage collection.
	const afterHeadMove = "main already points at the compacted history; inspect its pending changes " +
		"with dolt_status and commit them with `bd dolt commit` when ready"
	staged, err := issueops.HasStagedChanges(ctx, conn)
	if err != nil {
		return fmt.Errorf("compact step %q failed (%s): %w", "check main for staged changes", afterHeadMove, err)
	}
	if staged {
		err = schema.DrainCall(ctx, conn, "CALL DOLT_COMMIT('-m', ?)",
			"compact: commit changes that reached main during compaction")
		// Another writer committing main's staged set first leaves nothing
		// to commit, which is the outcome this step wants.
		if err != nil && !issueops.IsNothingToCommitError(err) {
			return fmt.Errorf("compact step %q failed (%s): %w", "commit changes that reached main", afterHeadMove, err)
		}
	}

	if err := execSQL("delete temp branch", "CALL DOLT_BRANCH('-D', 'compact-tmp')"); err != nil {
		return err
	}

	return nil
}
