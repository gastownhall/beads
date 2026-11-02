package versioncontrolops

import (
	"context"
)

// FederationStagingBranchPrefix is the branch-name prefix of the temporary
// staging branches a federation filtered push builds its filtered tip on.
// Both storage backends use it, so the branch shape — and any tooling that
// sweeps leftover staging branches — stays identical across modes.
const FederationStagingBranchPrefix = "__federation_push_staging_"

// DeleteExcludedIssues removes issues matching excludeTypes from the committed
// issues table on conn's current (staging) branch and reports whether any rows
// were deleted. The special type "wisp" matches ephemeral=true rows: wisps
// normally live in dolt_ignore'd tables and are not pushed, so this acts as a
// defense-in-depth safety net (GH#7890).
//
// A DELETE error is deliberately ignored (deleted stays false for that type),
// matching the server store's pre-existing best-effort filtering behavior;
// the caller only recomputes and commits when something was actually removed.
func DeleteExcludedIssues(ctx context.Context, conn DBConn, excludeTypes []string) bool {
	deleted := false
	for _, excludeType := range excludeTypes {
		var result interface{ RowsAffected() (int64, error) }
		var execErr error
		if excludeType == "wisp" {
			result, execErr = conn.ExecContext(ctx, "DELETE FROM issues WHERE ephemeral = 1")
		} else {
			result, execErr = conn.ExecContext(ctx, "DELETE FROM issues WHERE issue_type = ?", excludeType)
		}
		if execErr == nil {
			if n, _ := result.RowsAffected(); n > 0 {
				deleted = true
			}
		}
	}
	return deleted
}