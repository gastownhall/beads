package versioncontrolops

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/steveyegge/beads/internal/storage/issueops"
)

// FilterConn supports branch filtering and transactional blocked-state repair.
type FilterConn interface {
	BlockedRecomputeConn
}

// FederationWarnWriter receives the one-time warning for excluded issue history.
var FederationWarnWriter io.Writer = os.Stderr

var excludedHistoryWarningOnce sync.Once

// StageFilteredBranch creates and filters a staging branch, then restores the source branch.
func StageFilteredBranch(ctx context.Context, conn FilterConn, sourceBranch, stagingBranch string, excludeTypes []string) (int64, error) {
	if _, err := conn.ExecContext(ctx, "CALL DOLT_BRANCH(?, ?)", stagingBranch, sourceBranch); err != nil {
		return 0, fmt.Errorf("federation filter: create staging branch: %w", err)
	}
	if err := CheckoutBranch(ctx, conn, stagingBranch); err != nil {
		return 0, fmt.Errorf("federation filter: checkout staging: %w", err)
	}
	var removed int64
	for _, excludeType := range excludeTypes {
		query := "DELETE FROM issues WHERE issue_type = ?"
		args := []any{excludeType}
		if excludeType == "wisp" {
			query, args = "DELETE FROM issues WHERE ephemeral = 1", nil
		}
		result, err := conn.ExecContext(ctx, query, args...)
		if err == nil {
			if n, _ := result.RowsAffected(); n > 0 {
				removed += n
			}
		}
	}
	if removed > 0 {
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return removed, fmt.Errorf("federation filter: begin staged is_blocked recompute: %w", err)
		}
		if _, err := issueops.RecomputeAllIsBlockedInTx(ctx, tx); err != nil {
			_ = tx.Rollback()
			return removed, fmt.Errorf("federation filter: recompute staged is_blocked: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return removed, fmt.Errorf("federation filter: commit staged is_blocked recompute: %w", err)
		}
		if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', ?)", "federation: exclude private issue types"); err != nil {
			return removed, fmt.Errorf("federation filter: commit filtered state: %w", err)
		}
	}
	if err := CheckoutBranch(ctx, conn, sourceBranch); err != nil {
		return removed, fmt.Errorf("federation filter: restore branch %s: %w", sourceBranch, err)
	}
	return removed, nil
}

// WarnExcludedHistory warns once when the push tip was filtered but history remains readable.
func WarnExcludedHistory(removed int64) {
	if removed <= 0 {
		return
	}
	excludedHistoryWarningOnce.Do(func() {
		_, _ = fmt.Fprintf(FederationWarnWriter, "Warning: federation.exclude_types removed %d issue(s) from this push, but earlier commits in the pushed history still contain them; a peer that reads history can recover them. See https://beads.gascity.com/multi-agent/federation\n", removed)
	})
}
