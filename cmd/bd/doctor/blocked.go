package doctor

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/issueops"
)

// BlockedConsistencyCheckName is the doctor check name; applyFixList dispatches
// the repair (fix.RecomputeBlocked) on this exact string.
const BlockedConsistencyCheckName = "Blocked State"

// CheckBlockedConsistencyWithStore reports issues and wisps whose denormalized
// is_blocked flag disagrees with the dependency graph (bd-6dnrw.37). is_blocked
// is derived state maintained by the local write paths and by a post-pull
// recompute scoped to the merge diff; a recompute that failed after its merge
// committed, or a conflicted pull resolved by hand, can leave it stale, and a
// re-pull that merges nothing will not refresh it. `bd ready` trusts the
// column, so stale values silently hide ready work or surface blocked work. The
// repair is 'bd doctor --fix', which runs a full recompute.
func CheckBlockedConsistencyWithStore(ss *SharedStore) DoctorCheck {
	store := ss.Store()
	if store == nil {
		return DoctorCheck{
			Name:    BlockedConsistencyCheckName,
			Status:  StatusOK,
			Message: "No database yet",
		}
	}
	return checkBlockedConsistencyWithStore(context.Background(), store)
}

func checkBlockedConsistencyWithStore(ctx context.Context, store *dolt.DoltStore) DoctorCheck {
	stale, err := issueops.CountIsBlockedInconsistenciesInTx(ctx, store.UnderlyingDB())
	if err != nil {
		return DoctorCheck{
			Name:    BlockedConsistencyCheckName,
			Status:  StatusWarning,
			Message: "Unable to check is_blocked consistency",
			Detail:  err.Error(),
		}
	}
	if stale == 0 {
		return DoctorCheck{
			Name:    BlockedConsistencyCheckName,
			Status:  StatusOK,
			Message: "is_blocked flags consistent with dependency graph",
		}
	}
	return DoctorCheck{
		Name:    BlockedConsistencyCheckName,
		Status:  StatusWarning,
		Message: fmt.Sprintf("%d issue/wisp row(s) have a stale is_blocked flag — 'bd ready' may hide ready work or show blocked work", stale),
		Detail:  "is_blocked is derived from the dependency graph; a skipped post-pull recompute can leave it stale",
		Fix:     "Run: bd doctor --fix (or 'bd recompute-blocked', which also works in embedded mode)",
	}
}
