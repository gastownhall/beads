package fix

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/storage/issueops"
)

// FixStatusBlockedDrift repairs status='blocked' rows the dependency graph no
// longer holds blocked — every blocker closed, or none was ever recorded
// (be-ntbxt): nothing else clears that manual status, so 'bd ready' never
// sees them again without this fix. Rows the graph DOES still hold blocked,
// by any edge type, are left alone.
//
// Mirrors RecomputeBlocked: opens its own writable store, repairs in a
// transaction, and stages only the table it touched so an unrelated dirty
// working set is not swept under this commit.
func FixStatusBlockedDrift(path string) error {
	beadsDir, err := resolvedWorkspaceBeadsDir(path)
	if err != nil {
		return err
	}

	db, cfg, err := openDoltDB(beadsDir)
	if err != nil {
		fmt.Printf("  Status-blocked-drift fix skipped (%v)\n", err)
		return nil
	}
	defer db.Close()

	if skip, err := guardFixTarget("Status-blocked-drift fix", db, beadsDir, cfg); skip {
		return err
	}

	return repairStatusBlockedDrift(context.Background(), db)
}

// repairStatusBlockedDrift fixes status=blocked drift on an open connection.
// Split from FixStatusBlockedDrift so the repair is testable against an
// existing store handle.
func repairStatusBlockedDrift(ctx context.Context, db *sql.DB) error {
	// Explicit transaction so writes persist when @@autocommit is OFF (e.g. a
	// Dolt server started with --no-auto-commit).
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	// Refuse to derive and commit status=open from a dirty graph: this reads
	// dependencies/issues and stages only issues, so a dirty issues/dependencies
	// tree would taint the repair commit — the same reasoning
	// repairBlockedState applies to its is_blocked recompute (bd-6dnrw.37).
	if err := issueops.GuardBlockedRecomputeWorkingSet(ctx, tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	changed, err := issueops.FixStatusBlockedDriftInTx(ctx, tx)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("failed to fix status=blocked drift: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit status=blocked drift repairs: %w", err)
	}

	if changed == 0 {
		fmt.Println("  No status=blocked drift — nothing to fix")
		return nil
	}

	// Persist the corrected statuses as a Dolt commit, staging only issues —
	// wisps are dolt_ignore'd, so a wisp-only fix needs no version commit here.
	// bd doctor is server-mode only, so the server supplies the commit identity.
	if _, err := db.ExecContext(ctx, "CALL DOLT_ADD(?)", "issues"); err != nil {
		return fmt.Errorf("failed to stage status=blocked drift repairs: %w", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-m', 'doctor: fix status=blocked drift')"); err != nil && !issueops.IsNothingToCommitError(err) {
		return fmt.Errorf("failed to commit status=blocked drift repairs to Dolt: %w", err)
	}

	fmt.Printf("  Fixed status=blocked drift: %d row(s) returned to status=open\n", changed)
	return nil
}
