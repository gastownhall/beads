package doctor

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/issueops"
)

// StatusBlockedDriftCheckName is the doctor check name; applyFixList dispatches
// the repair (fix.FixStatusBlockedDrift) on this exact string.
const StatusBlockedDriftCheckName = "Status Blocked Drift"

// CheckStatusBlockedDriftWithStore reports issues and wisps left at the
// manually-set status='blocked' after the last thing blocking them stopped
// blocking them, or that never had a blocker recorded at all (be-ntbxt).
// "Blocking" is the dependency graph's own definition — the same one is_blocked
// is derived from — so a row still held by conditional-blocks, an inherited
// parent-child block or a waits-for gate is not drift. Nothing else clears the
// manual status, so 'bd close --suggest-next' can report an issue as newly
// unblocked while it stays invisible to 'bd ready' forever — this drift
// stranded 29 beads fleet-wide (3 of them P1) before this check shipped. The
// repair is 'bd doctor --fix', which returns every drifted row to
// status='open'.
func CheckStatusBlockedDriftWithStore(ss *SharedStore) DoctorCheck {
	store := ss.Store()
	if store == nil {
		return DoctorCheck{
			Name:    StatusBlockedDriftCheckName,
			Status:  StatusOK,
			Message: "No database yet",
		}
	}
	return checkStatusBlockedDriftWithStore(context.Background(), store)
}

func checkStatusBlockedDriftWithStore(ctx context.Context, store *dolt.DoltStore) DoctorCheck {
	drifted, err := issueops.CountStatusBlockedDriftInTx(ctx, store.UnderlyingDB())
	if err != nil {
		return DoctorCheck{
			Name:    StatusBlockedDriftCheckName,
			Status:  StatusWarning,
			Message: "Unable to check status=blocked drift",
			Detail:  err.Error(),
		}
	}
	if drifted == 0 {
		return DoctorCheck{
			Name:    StatusBlockedDriftCheckName,
			Status:  StatusOK,
			Message: "No status=blocked issue/wisp lacks an open blocker",
		}
	}
	return DoctorCheck{
		Name:    StatusBlockedDriftCheckName,
		Status:  StatusWarning,
		Message: fmt.Sprintf("%d issue/wisp row(s) marked status=blocked with no open blocker — invisible to 'bd ready' until fixed", drifted),
		Detail:  "status='blocked' is a manual field; nothing else clears it once the graph stops holding the row blocked (be-ntbxt)",
		Fix:     "Run: bd doctor --fix (or 'bd recompute-blocked --status --fix', which also works in embedded mode)",
	}
}
