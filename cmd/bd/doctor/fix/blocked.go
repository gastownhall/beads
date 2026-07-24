package fix

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/storage/issueops"
)

// RecomputeBlocked repairs stale is_blocked flags (bd-6dnrw.37) by running a
// full is_blocked recompute over every issue and wisp, then committing the
// issues table so the corrected flags sync. is_blocked is derived state that a
// skipped post-pull recompute can leave stale; a re-pull that merges nothing
// will not refresh it, so this full pass is the repair.
//
// Mirrors DependencyKeys: opens its own writable store, repairs in a
// transaction, and stages only the table it touched so an unrelated dirty
// working set is not swept under this commit.
func RecomputeBlocked(path string) error {
	beadsDir, err := resolvedWorkspaceBeadsDir(path)
	if err != nil {
		return err
	}

	db, err := openDoltDB(beadsDir)
	if err != nil {
		fmt.Printf("  Blocked-state fix skipped (%v)\n", err)
		return nil
	}
	defer db.Close()

	return repairBlockedState(context.Background(), db)
}

// repairBlockedState recomputes is_blocked on an open connection. Split from
// RecomputeBlocked so the repair is testable against an existing store handle.
func repairBlockedState(ctx context.Context, db *sql.DB) error {
	// Explicit transaction so writes persist when @@autocommit is OFF (e.g. a
	// Dolt server started with --no-auto-commit).
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	// Refuse to derive and commit is_blocked from a dirty graph: like the store
	// paths, the recompute reads the working set and stages only `issues`, so a
	// dirty issues/dependencies tree would taint the repair commit (bd-6dnrw.37).
	// In a `bd doctor --fix` run the dependency-graph fixes commit ahead of this
	// one, so the tree is normally clean here; when it is not, surface it as an
	// actionable error rather than committing tainted state.
	if err := issueops.GuardBlockedRecomputeWorkingSet(ctx, tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	changed, err := issueops.RecomputeAllIsBlockedInTx(ctx, tx)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("failed to recompute is_blocked: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit is_blocked repairs: %w", err)
	}

	if changed == 0 {
		fmt.Println("  is_blocked already consistent — nothing to fix")
		return nil
	}

	// Persist the corrected flags as a Dolt commit, staging only issues — the
	// synced table is_blocked lives on (wisps are dolt_ignore'd). This path keeps
	// its own fresh-DB lifecycle rather than the shared store helper, but it must
	// not report success on a failed commit: a swallowed DOLT_COMMIT error would
	// leave the repair in the working set only, silently undone by the next pull.
	// bd doctor is server-mode only, so the server supplies the commit identity.
	if _, err := db.ExecContext(ctx, "CALL DOLT_ADD(?)", "issues"); err != nil {
		return fmt.Errorf("failed to stage is_blocked repairs: %w", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-m', 'doctor: recompute is_blocked for all issues')"); err != nil && !issueops.IsNothingToCommitError(err) {
		return fmt.Errorf("failed to commit is_blocked repairs to Dolt: %w", err)
	}

	fmt.Printf("  Recomputed is_blocked: %d row(s) corrected\n", changed)
	return nil
}
