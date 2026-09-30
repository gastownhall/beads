package versioncontrolops

import (
	"context"
	"fmt"
	"os"
)

// Flatten squashes all Dolt commit history into a single commit using
// the Tim Sehn recipe:
//  1. Create a temp branch from current state
//  2. Checkout temp branch
//  3. Soft-reset to the initial (oldest) commit, collapsing all history
//  4. Stage all + commit as a single snapshot
//  5. Checkout main
//  6. Hard-reset main to the flattened branch
//  7. Delete temp branch
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

	// Once flatten-tmp exists, every failure returns to main and deletes it
	// again: a leftover flatten-tmp blocks every later flatten at "create temp
	// branch". The cleanup survives a canceled ctx, and any cleanup failure
	// (checkout or delete) is appended to the error so the operator knows the
	// session or the branch was left behind.
	branchCreated := false
	finalDelete := false
	defer func() {
		if retErr == nil || !branchCreated {
			return
		}
		if err := cleanupTempBranch(ctx, conn, "flatten-tmp"); err != nil {
			retErr = fmt.Errorf("%w (%v)", retErr, err)
		} else if finalDelete {
			// Only the final delete had failed, and the retry just deleted
			// flatten-tmp: the flatten fully succeeded. Report the retried
			// delete as a warning, the way this package reports other
			// non-fatal events, and return nil (upstream review, #6772).
			fmt.Fprintf(os.Stderr, "Warning: %v; the cleanup retry deleted flatten-tmp\n", retErr)
			retErr = nil
		}
	}()

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

	for i, s := range steps {
		if err := execSQL(s.name, s.query, s.args...); err != nil {
			return err
		}
		if i == 0 {
			branchCreated = true
		}
	}

	// bd-7bpkd / ga-28co77: the hard reset drops every clone-local FK; the
	// helper re-links the ones it dropped, on this same session. Any failure
	// here — a probe failure before the reset, or a re-link failure after it
	// — deletes flatten-tmp through the deferred cleanup above.
	if err := resetHardPreservingCloneLocalFKs(ctx, conn, "flatten-tmp"); err != nil {
		return fmt.Errorf("flatten step %q: %w", "reset main to flattened", err)
	}

	// The flatten itself has succeeded; only deleting flatten-tmp remains. The
	// flag stays set until that delete succeeds, so a failed delete (e.g. the
	// caller was canceled here) is retried by the deferred cleanup on its own
	// context instead of stranding the branch (Astra r3). If the retry deletes
	// it, Flatten returns nil with a warning; if the retry fails too, the error
	// says the flatten succeeded and appends the cleanup failure.
	finalDelete = true
	if _, err := conn.ExecContext(ctx, "CALL DOLT_BRANCH('-D', 'flatten-tmp')"); err != nil {
		return fmt.Errorf("flatten succeeded, but deleting temp branch flatten-tmp failed: %w", err)
	}
	branchCreated = false
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
