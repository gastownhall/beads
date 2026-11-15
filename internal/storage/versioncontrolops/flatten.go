package versioncontrolops

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// Flatten squashes all Dolt commit history into a single commit using
// the Tim Sehn recipe:
//  1. Create a temp branch from current state
//  2. Checkout temp branch
//  3. Soft-reset to the initial (oldest) commit, collapsing all history
//  4. Stage all + commit as a single snapshot
//  5. Checkout main
//  6. Soft-reset main to the flattened branch (keeps main's working set)
//  7. Commit main's staged set, if any: a commit that reached main while
//     the snapshot was built, or rows already staged
//  8. Delete temp branch
//
// Callers should run PruneRemoteRefs and then DoltGC afterward to reclaim disk
// space from orphaned history — remote-tracking refs still anchor the
// pre-flatten chain, and GC alone reclaims nothing while they exist (bd-agctw).
//
// If a step fails, or ctx is canceled, after Flatten created flatten-tmp,
// it tries to return to main and delete the branch (abandonFlatten), so the
// next flatten is not blocked by it. A cleanup that cannot finish keeps the
// branch and names it in the error, and the next flatten's branch create
// then says how to delete it.
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

	execSQL := func(name, query string, args ...interface{}) error {
		if _, err := conn.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("flatten step %q: %w", name, err)
		}
		return nil
	}

	// A flatten-tmp that already exists is not this invocation's: creating
	// it fails here, before cleanup is armed, so it is never removed. It may
	// be left by a flatten whose cleanup could not run, so the error says how
	// to delete it.
	if err := execSQL("create temp branch", "CALL DOLT_BRANCH('flatten-tmp')"); err != nil {
		if strings.Contains(err.Error(), "already exists") {
			return fmt.Errorf("%w; if no flatten is running, an earlier one that did not finish left it: "+
				"delete it from main with CALL DOLT_BRANCH('-D', 'flatten-tmp'), then flatten again", err)
		}
		return err
	}
	// Armed until the final branch delete succeeds, so a failure or
	// cancellation at any later step, that delete included, is cleaned up.
	armed := true
	defer func() {
		if armed {
			retErr = abandonFlatten(ctx, conn, retErr)
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
		// --soft moves main's HEAD and keeps its staged and working sets.
		// --hard replaced the working set with the snapshot's tree, which
		// dropped every foreign key on the dolt_ignored (clone-local) tables
		// that live only there (bd-7bpkd).
		{"reset main to flattened", "CALL DOLT_RESET('--soft', 'flatten-tmp')", nil},
	}

	for _, s := range steps {
		if err := execSQL(s.name, s.query, s.args...); err != nil {
			return err
		}
	}

	// A commit that reached main after flatten-tmp was built, and rows that
	// were already staged, are now staged on main, outside HEAD. Commit the
	// staged set only: rows that are just in the working set stay pending, as
	// they were. The landed commit does not keep its own hash or message:
	// flattening rewrites that history anyway.
	//
	// main has already moved when either step below fails, so the error says
	// so and what to do with what main kept. The move is not rolled back: a
	// hard reset here would discard the writes the soft reset kept. The
	// caller stops before pruning and garbage collection.
	const afterHeadMove = "main already points at the flattened history; inspect its pending changes " +
		"with dolt_status and commit them with `bd dolt commit` when ready"
	staged, err := issueops.HasStagedChanges(ctx, conn)
	if err != nil {
		return fmt.Errorf("flatten step %q failed (%s): %w", "check main for staged changes", afterHeadMove, err)
	}
	if staged {
		err = schema.DrainCall(ctx, conn, "CALL DOLT_COMMIT('-m', ?)",
			"flatten: commit changes that reached main during flatten")
		// Another writer committing main's staged set first leaves nothing
		// to commit, which is the outcome this step wants.
		if err != nil && !issueops.IsNothingToCommitError(err) {
			return fmt.Errorf("flatten step %q failed (%s): %w", "commit changes that reached main", afterHeadMove, err)
		}
	}

	if err := execSQL("delete temp branch", "CALL DOLT_BRANCH('-D', 'flatten-tmp')"); err != nil {
		return err
	}
	armed = false
	return nil
}

// flattenCleanupTimeout bounds abandonFlatten's statements.
const flattenCleanupTimeout = 30 * time.Second

// abandonFlatten is Flatten's cleanup once a step fails after flatten-tmp
// exists: back to main, then the branch deleted. It runs on a context the
// caller's cancellation does not reach, bounded by flattenCleanupTimeout,
// so a flatten canceled between statements still removes the branch that
// would block the next one. It needs conn's session, though: a cancellation
// that interrupts a statement can make the driver close the connection
// (go-sql-driver/mysql does, on a dolt sql-server), and then the cleanup
// fails and the branch stays. The checkout is not forced: if the session
// cannot return to main, flatten-tmp is kept, since it may still be the
// session's branch. It returns cause, joined with any cleanup failure.
func abandonFlatten(ctx context.Context, conn DBConn, cause error) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), flattenCleanupTimeout)
	defer cancel()
	if _, err := conn.ExecContext(cleanupCtx, "CALL DOLT_CHECKOUT('main')"); err != nil {
		return errors.Join(cause, fmt.Errorf(
			"flatten cleanup: return to main: %w; kept branch flatten-tmp, which blocks the next flatten until it is deleted from main with CALL DOLT_BRANCH('-D', 'flatten-tmp')", err))
	}
	if _, err := conn.ExecContext(cleanupCtx, "CALL DOLT_BRANCH('-D', 'flatten-tmp')"); err != nil {
		return errors.Join(cause, fmt.Errorf(
			"flatten cleanup: delete branch flatten-tmp: %w; it remains and blocks the next flatten until deleted with CALL DOLT_BRANCH('-D', 'flatten-tmp')", err))
	}
	return cause
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
