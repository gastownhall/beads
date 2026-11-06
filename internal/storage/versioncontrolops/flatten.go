package versioncontrolops

import (
	"context"
	"fmt"

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
func Flatten(ctx context.Context, conn DBConn) error {
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

	steps := []struct {
		name  string
		query string
		args  []interface{}
	}{
		{"create temp branch", "CALL DOLT_BRANCH('flatten-tmp')", nil},
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
