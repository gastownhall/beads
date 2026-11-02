package versioncontrolops

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// FederationStagingBranchPrefix is the branch-name prefix of the temporary
// staging branches a federation filtered push builds its filtered tip on.
// Both storage backends use it, so the branch shape — and any tooling that
// sweeps leftover staging branches — stays identical across modes.
const FederationStagingBranchPrefix = "__federation_push_staging_"

// FederationKeepBranchPrefix is the branch-name prefix of the temporary keep
// branches a federation filtered push reverts its filtered commit on before
// merging the result back into the source branch (KeepFilteredPushLocal).
const FederationKeepBranchPrefix = "__federation_push_keep_"

// KeepFilteredPushLocal creates keepBranch at stagingBranch's tip (the filtered
// commit F), checks it out, reverts F with Dolt's supported revert, and
// restores sourceBranch. The revert yields R with parent F and the pre-filter
// tree, cascaded related rows included; the caller then merges keepBranch into
// the source branch through the store's normal merge path before publishing F
// (GH#7890). Once that merge lands, the published filtered tip F is an
// ancestor of the source branch, so the next sync's merge cannot fast-forward
// the filtered tip over the local excluded rows. Branch state is session-scoped,
// so every step stays on conn.
func KeepFilteredPushLocal(ctx context.Context, conn DBConn, sourceBranch, stagingBranch, keepBranch string) error {
	if _, err := conn.ExecContext(ctx, "CALL DOLT_BRANCH(?, ?)", keepBranch, stagingBranch); err != nil {
		return fmt.Errorf("federation filter: create keep branch: %w", err)
	}
	if err := CheckoutBranch(ctx, conn, keepBranch); err != nil {
		return fmt.Errorf("federation filter: checkout keep: %w", err)
	}
	// Revert the staged tip itself. keepBranch was minted at stagingBranch's
	// tip, so its HEAD is exactly the filtered commit F — reverting it applies
	// F's inverse onto F's tree, yielding R with parent F and the tree the
	// source branch had before filtering. Dolt resolves 'HEAD' against the
	// session's checkout, which the checkout above pinned to keepBranch.
	if err := schema.DrainCall(ctx, conn, "CALL DOLT_REVERT('HEAD')"); err != nil {
		return fmt.Errorf("federation filter: revert filtered commit: %w", err)
	}
	if err := CheckoutBranch(ctx, conn, sourceBranch); err != nil {
		return fmt.Errorf("federation filter: restore branch %s: %w", sourceBranch, err)
	}
	return nil
}

// DeleteExcludedIssues removes issues matching excludeTypes from the committed
// issues table on conn's current (staging) branch and reports whether any rows
// were deleted. The special type "wisp" matches ephemeral=true rows: wisps
// normally live in dolt_ignore'd tables and are not pushed, so this acts as a
// defense-in-depth safety net (GH#7890).
//
// Fail-closed: a DELETE error aborts the filtered push with that error —
// continuing would publish the unfiltered tip, which is the exact leak the
// filtering exists to prevent. The caller only recomputes and commits when
// something was actually removed.
func DeleteExcludedIssues(ctx context.Context, conn DBConn, excludeTypes []string) (bool, error) {
	deleted := false
	for _, excludeType := range excludeTypes {
		var result interface{ RowsAffected() (int64, error) }
		var execErr error
		if excludeType == "wisp" {
			result, execErr = conn.ExecContext(ctx, "DELETE FROM issues WHERE ephemeral = 1")
		} else {
			result, execErr = conn.ExecContext(ctx, "DELETE FROM issues WHERE issue_type = ?", excludeType)
		}
		if execErr != nil {
			return deleted, fmt.Errorf("federation filter: delete excluded issues (%s): %w", excludeType, execErr)
		}
		if n, _ := result.RowsAffected(); n > 0 {
			deleted = true
		}
	}
	return deleted, nil
}
