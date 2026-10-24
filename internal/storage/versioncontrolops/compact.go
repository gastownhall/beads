package versioncontrolops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
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
// The plan (boundaryHash, recentHashes) is read from main's log before
// Compact runs. Compact refuses before step 1 when main's head is not the
// head the plan was built from, and checks again just before the hard reset
// in step 6: a commit that reached main after the plan was read is in no
// cherry-pick, so resetting main past it would delete a write that had
// already reported success. On a refusal main is left untouched and the temp
// branch removed.
//
// Compact takes the planned head to be the newest commit in the plan, which
// is main's head unless the log's date order disagrees with its parent
// order; then Compact refuses although nothing moved. A caller that can read
// main's head (MainHead) BEFORE it reads the log should call CompactAtHead
// with that head instead, so each refusal names the true head and its cause.
//
// Callers should run PruneRemoteRefs and then DoltGC afterward to reclaim disk
// space — remote-tracking refs still anchor the pre-compact chain, and GC
// alone reclaims nothing while they exist (bd-agctw).
//
// conn must be a single database connection (not a pooled *sql.DB) since the
// stored procedures rely on session-scoped state (current branch, working set).
func Compact(ctx context.Context, conn DBConn, initialHash, boundaryHash string, oldCommits int, recentHashes []string) error {
	return CompactAtHead(ctx, conn, planNewest(boundaryHash, recentHashes), initialHash, boundaryHash, oldCommits, recentHashes)
}

// CompactAtHead is Compact with the head of main the plan was built from read
// by the caller. Read it with MainHead before reading the log: a commit that
// lands between the two reads is then in the plan but not at plannedHead, so
// CompactAtHead refuses rather than resetting main past it. It also refuses
// when main is still at plannedHead but the plan's newest commit is another
// commit, because a plan built from a log whose date order disagrees with
// its parent order does not cover main.
func CompactAtHead(ctx context.Context, conn DBConn, plannedHead, initialHash, boundaryHash string, oldCommits int, recentHashes []string) (retErr error) {
	branchCreated := false

	if err := requireMainAt(ctx, conn, plannedHead, "before building the squashed branch"); err != nil {
		return err
	}
	if newest := planNewest(boundaryHash, recentHashes); newest != plannedHead {
		return fmt.Errorf("%w: main is at %s, but the newest commit in the plan is %s; "+
			"the log's date order disagrees with its parent order, so main was left unchanged",
			ErrPlanNotAtHead, plannedHead, newest)
	}

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
	if err := requireMainAt(ctx, conn, plannedHead, "before resetting main"); err != nil {
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

// ErrMainMoved reports that main's head is no longer the commit a compaction
// plan was built from, so the compaction stopped without changing main.
var ErrMainMoved = errors.New("main moved during compaction")

// ErrPlanNotAtHead reports that main is at the head read before the plan,
// but the plan's newest commit is a different commit, so the compaction
// stopped without changing main.
var ErrPlanNotAtHead = errors.New("compaction plan does not end at main's head")

// planNewest is the newest commit a plan names: the last commit it replays
// or, when it replays none, the boundary commit itself.
func planNewest(boundaryHash string, recentHashes []string) string {
	if len(recentHashes) > 0 {
		return recentHashes[len(recentHashes)-1]
	}
	return boundaryHash
}

// MainHead returns the commit at the head of main.
func MainHead(ctx context.Context, conn DBConn) (string, error) {
	var head string
	if err := conn.QueryRowContext(ctx, "SELECT DOLT_HASHOF('main')").Scan(&head); err != nil {
		return "", err
	}
	return head, nil
}

// TablesChangedBetween names the tables whose rows or schema differ between
// the commits from and to, read from DOLT_DIFF_SUMMARY. Called with the head a
// compaction was planned from and the head it left on main, it answers whether
// the squash and replay changed any data: an empty answer means they did not.
// Call it before GC, which may remove the planned head's chain. On success the
// result is never nil, so a JSON encoder writes [] rather than null.
func TablesChangedBetween(ctx context.Context, conn DBConn, from, to string) ([]string, error) {
	rows, err := conn.QueryContext(ctx,
		"SELECT from_table_name, to_table_name FROM DOLT_DIFF_SUMMARY(?, ?)", from, to)
	if err != nil {
		return nil, fmt.Errorf("diff summary %s..%s: %w", from, to, err)
	}
	defer rows.Close()
	changed := []string{}
	for rows.Next() {
		var fromName, toName sql.NullString
		if err := rows.Scan(&fromName, &toName); err != nil {
			return nil, fmt.Errorf("diff summary %s..%s: %w", from, to, err)
		}
		name := toName.String
		if name == "" {
			name = fromName.String
		}
		changed = append(changed, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("diff summary %s..%s: %w", from, to, err)
	}
	sort.Strings(changed)
	return changed, nil
}

// requireMainAt returns ErrMainMoved, in words, unless main's head is want.
func requireMainAt(ctx context.Context, conn DBConn, want, when string) error {
	head, err := MainHead(ctx, conn)
	if err != nil {
		return fmt.Errorf("compact: reading the head of main %s: %w", when, err)
	}
	if head != want {
		return fmt.Errorf("%w: the plan was built from main at %s, but main is at %s (checked %s), "+
			"so main was left unchanged; run compact again",
			ErrMainMoved, want, head, when)
	}
	return nil
}
