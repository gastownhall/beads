package versioncontrolops

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// flattenPreserveMsg is the message of the commit Flatten adds when the soft
// reset leaves anything staged on main.
const flattenPreserveMsg = "flatten: commit changes that reached main during flatten"

// Flatten squashes all Dolt commit history into a single commit using
// the Tim Sehn recipe:
//  1. Create a temp branch from current state
//  2. Checkout temp branch
//  3. Soft-reset to the initial (oldest) commit, collapsing all history
//  4. Stage all + commit as a single snapshot
//  5. Checkout main
//  6. Soft-reset main to the flattened branch
//  7. Commit what the reset staged, if anything
//  8. Delete temp branch
//
// A leftover flatten-tmp from an earlier interrupted run is refused up front
// rather than deleted: this run only ever removes the branch it created.
// Failures after that branch exists trigger a bounded cleanup (back to main,
// branch deleted) on a context detached from the caller's, so an interrupted
// flatten does not block the next one.
//
// The final reset of main is soft, never hard: DOLT_RESET('--hard') swaps the
// tracked tables' backing objects and silently drops every foreign key on
// every dolt_ignored ("clone-local") table — events and the wisp_* aux
// tables — leaving enforcement off for good (bd-7bpkd, fixed for compact by
// the same change of reset). A soft reset only moves main's HEAD, so the
// clone-local tables' rows, constraints and enforcement all survive, and so
// do main's staged and working sets: uncommitted writes stay pending, and a
// commit that reached main after the snapshot was built comes back staged
// and is preserved by step 7's commit.
//
// Callers should run PruneRemoteRefs and then DoltGC afterward to reclaim disk
// space from orphaned history — remote-tracking refs still anchor the
// pre-flatten chain, and GC alone reclaims nothing while they exist (bd-agctw).
//
// conn must be a single database connection (not a pooled *sql.DB) since the
// stored procedures rely on session-scoped state (current branch, working set).
func Flatten(ctx context.Context, conn DBConn) (retErr error) {
	// Find the initial commit hash (oldest ancestor).
	var initialHash string
	if err := conn.QueryRowContext(ctx,
		"SELECT commit_hash FROM dolt_log ORDER BY date ASC LIMIT 1",
	).Scan(&initialHash); err != nil {
		return fmt.Errorf("find initial commit: %w", err)
	}

	// Count commits to check if flatten is needed.
	var commitCount int
	if err := conn.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dolt_log",
	).Scan(&commitCount); err != nil {
		return fmt.Errorf("count commits: %w", err)
	}
	if commitCount <= 1 {
		return nil // already flat
	}

	// A leftover flatten-tmp blocks the whole recipe (the branch create
	// fails), and this run must only ever delete a branch it created:
	// refuse, and let a human look at what is there.
	var leftover int
	if err := conn.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dolt_branches WHERE name = 'flatten-tmp'",
	).Scan(&leftover); err != nil {
		return fmt.Errorf("flatten step %q: %w", "check for leftover flatten-tmp", err)
	}
	if leftover > 0 {
		return fmt.Errorf("flatten refused: branch flatten-tmp already exists; inspect it and delete it with the dolt CLI (dolt branch -D flatten-tmp) before flattening")
	}

	execSQL := func(name, query string, args ...interface{}) error {
		if _, err := conn.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("flatten step %q: %w", name, err)
		}
		return nil
	}

	if err := execSQL("create temp branch", "CALL DOLT_BRANCH('flatten-tmp')"); err != nil {
		return err
	}

	// flatten-tmp now exists and belongs to this invocation: whatever fails
	// from here on, cleanup tries to remove it so the next flatten is not
	// blocked. Disarmed only by the successful delete that ends a clean run,
	// so a failed final delete is retried under the cleanup's own context.
	defer func() {
		if retErr == nil {
			return
		}
		if err := flattenCleanup(ctx, conn); err != nil {
			retErr = errors.Join(retErr, err)
		}
	}()

	steps := []struct {
		name  string
		query string
		args  []interface{}
	}{
		{"checkout temp branch", "CALL DOLT_CHECKOUT('flatten-tmp')", nil},
		{"soft reset to initial", "CALL DOLT_RESET('--soft', ?)", []interface{}{initialHash}},
		{"commit flattened snapshot", "CALL DOLT_COMMIT('-Am', 'flatten: squash all history into single commit')", nil},
		{"checkout main", "CALL DOLT_CHECKOUT('main')", nil},
	}

	for _, s := range steps {
		if err := execSQL(s.name, s.query, s.args...); err != nil {
			return err
		}
	}

	// --soft moves main's HEAD onto the flattened snapshot and keeps main's
	// staged and working sets, so the clone-local (dolt_ignored) tables keep
	// their rows, their foreign keys and their enforcement: a hard reset here
	// severs every one of those FKs (bd-7bpkd).
	if err := execSQL("reset main to flattened", "CALL DOLT_RESET('--soft', 'flatten-tmp')"); err != nil {
		return err
	}

	// A commit that reached main after the snapshot was built is now staged
	// on main, outside HEAD and so outside a push of main. Commit the staged
	// set only: rows that are just in the working set stay pending, as they
	// were, so a writer's batch keeps its own commit boundary. Rows that were
	// already staged when Flatten ran join this commit too. The landed commit
	// does not keep its own hash or message on main: flatten rewrites that
	// history anyway.
	staged, err := issueops.HasStagedChanges(ctx, conn)
	if err != nil {
		return fmt.Errorf("flatten step %q: %w", "check main for staged changes", err)
	}
	if staged {
		err = schema.DrainCall(ctx, conn, "CALL DOLT_COMMIT('-m', ?)", flattenPreserveMsg)
		// Another writer committing main's staged set first leaves nothing
		// to commit, which is the outcome this step wants.
		if err != nil && !issueops.IsNothingToCommitError(err) {
			return fmt.Errorf("flatten step %q: %w", "commit changes that reached main", err)
		}
	}

	if err := execSQL("delete temp branch", "CALL DOLT_BRANCH('-D', 'flatten-tmp')"); err != nil {
		return err
	}

	return nil
}

// flattenCleanup is Flatten's best-effort cleanup once a step fails after
// flatten-tmp exists: back to main, then the temp branch deleted, so a later
// flatten is not blocked. It runs on a context detached from the caller's —
// the primary failure is often the caller's own cancellation, and the
// cleanup must still reach the server — bounded at 30s. A failed checkout
// keeps the branch: the plain checkout never forces conflicting changes off
// flatten-tmp, and a branch this run did not create is never deleted. A
// successful cleanup does not erase the primary failure; Flatten joins the
// two, so the caller still sees what went wrong.
func flattenCleanup(ctx context.Context, conn DBConn) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if _, err := conn.ExecContext(cleanupCtx, "CALL DOLT_CHECKOUT('main')"); err != nil {
		return fmt.Errorf("flatten cleanup: checkout main failed, branch flatten-tmp left in place: %w", err)
	}
	if _, err := conn.ExecContext(cleanupCtx, "CALL DOLT_BRANCH('-D', 'flatten-tmp')"); err != nil {
		return fmt.Errorf("flatten cleanup: delete flatten-tmp failed, branch left in place: %w", err)
	}
	return nil
}

// FlattenDryRun returns the commit count and initial hash without modifying anything.
func FlattenDryRun(ctx context.Context, conn DBConn) (commitCount int, initialHash string, err error) {
	if err = conn.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dolt_log",
	).Scan(&commitCount); err != nil {
		err = fmt.Errorf("count commits: %w", err)
		return
	}
	if err = conn.QueryRowContext(ctx,
		"SELECT commit_hash FROM dolt_log ORDER BY date ASC LIMIT 1",
	).Scan(&initialHash); err != nil {
		err = fmt.Errorf("find initial commit: %w", err)
		return
	}
	return
}
