package doctor

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/cmd/bd/doctor/fix"
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
//
// It reads the server that recompute (fix.RecomputeBlocked) writes
// (fix.OpenScanDB), not the shared store's: beside a live
// BEADS_DOLT_SERVER_SOCKET and a resolved port the store reads the socket's
// server, so the check reported its flags while --fix recomputed the server at
// the port and reported them fixed (#7874 review). The shared store tells no
// database at all ("No database yet") from a database whose repair server the
// check cannot read, which it reports.
func CheckBlockedConsistencyWithStore(path string, ss *SharedStore) DoctorCheck {
	db, err := fix.OpenScanDB(path)
	if err != nil {
		if ss.Store() == nil {
			return DoctorCheck{
				Name:    BlockedConsistencyCheckName,
				Status:  StatusOK,
				Message: "No database yet",
			}
		}
		return DoctorCheck{
			Name:    BlockedConsistencyCheckName,
			Status:  StatusWarning,
			Message: "Unable to check is_blocked consistency",
			Detail:  err.Error(),
		}
	}
	defer func() { _ = db.Close() }()
	return checkBlockedConsistencyDB(context.Background(), db)
}

func checkBlockedConsistencyDB(ctx context.Context, db *sql.DB) DoctorCheck {
	stale, err := issueops.CountIsBlockedInconsistenciesInTx(ctx, db)
	if err != nil {
		return DoctorCheck{
			Name:    BlockedConsistencyCheckName,
			Status:  StatusWarning,
			Message: "Unable to check is_blocked consistency",
			Detail:  err.Error(),
		}
	}
	held, heldErr := issueops.CountIsBlockedHeldInTx(ctx, db)
	return blockedConsistencyCheck(stale, held, heldErr)
}

// blockedConsistencyCheck renders the check from its two counts: stale rows a
// full recompute would flip, and held rows it would leave as found because a
// dependency row names a wisp this database does not have (bd-muhnf0). Pure,
// so the render is testable without a database.
//
// A database with nothing dangling never reaches the held read's machinery
// (the probe answers 0 first), so its message is byte for byte the pre-hold
// one; a database that does hold dangling edges keeps the OK status — nothing
// a recompute here could do would be right — and says so. A held count that
// could not be read is never read as 0.
func blockedConsistencyCheck(stale, held int64, heldErr error) DoctorCheck {
	if stale == 0 && heldErr == nil && held == 0 {
		return DoctorCheck{
			Name:    BlockedConsistencyCheckName,
			Status:  StatusOK,
			Message: "is_blocked flags consistent with dependency graph",
		}
	}
	if stale == 0 && heldErr == nil {
		return DoctorCheck{
			Name:    BlockedConsistencyCheckName,
			Status:  StatusOK,
			Message: fmt.Sprintf("is_blocked flags consistent with dependency graph; %d open row(s) depend on a wisp this database does not have — is_blocked left as found", held),
		}
	}
	if stale == 0 {
		return DoctorCheck{
			Name:    BlockedConsistencyCheckName,
			Status:  StatusOK,
			Message: "is_blocked flags consistent with dependency graph",
			Detail:  fmt.Sprintf("could not count rows left as found: %v", heldErr),
		}
	}
	warning := DoctorCheck{
		Name:    BlockedConsistencyCheckName,
		Status:  StatusWarning,
		Message: fmt.Sprintf("%d issue/wisp row(s) have a stale is_blocked flag — 'bd ready' may hide ready work or show blocked work", stale),
		Detail:  "is_blocked is derived from the dependency graph; a skipped post-pull recompute can leave it stale",
		Fix:     "Run: bd doctor --fix (or 'bd recompute-blocked', which also works in embedded mode)",
	}
	if heldErr == nil && held > 0 {
		warning.Detail = fmt.Sprintf("is_blocked is derived from the dependency graph; a skipped post-pull recompute can leave it stale; %d open row(s) depend on a wisp this database does not have — is_blocked left as found", held)
	}
	return warning
}
