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
	// Rows the recompute cannot decide are reported beside the verdict and do
	// not change it: they are not stale, and --fix leaves them as found. A
	// failed read of them is not a reason to hide the stale count.
	undecided, err := issueops.CountIsBlockedUndecidedInTx(ctx, db)
	if err != nil {
		undecided = 0
	}
	if stale == 0 {
		check := DoctorCheck{
			Name:    BlockedConsistencyCheckName,
			Status:  StatusOK,
			Message: "is_blocked flags consistent with dependency graph",
		}
		if undecided > 0 {
			check.Message += fmt.Sprintf(" (%s)", undecidedBlockedNote(undecided))
			check.Detail = undecidedBlockedDetail
		}
		return check
	}
	check := DoctorCheck{
		Name:    BlockedConsistencyCheckName,
		Status:  StatusWarning,
		Message: fmt.Sprintf("%d issue/wisp row(s) have a stale is_blocked flag — 'bd ready' may hide ready work or show blocked work", stale),
		Detail:  "is_blocked is derived from the dependency graph; a skipped post-pull recompute can leave it stale",
		Fix:     "Run: bd doctor --fix (or 'bd recompute-blocked', which also works in embedded mode)",
	}
	if undecided > 0 {
		check.Detail += "; " + undecidedBlockedNote(undecided) + ". " + undecidedBlockedDetail
	}
	return check
}

// undecidedBlockedNote is the count half of the Blocked State line for rows
// that depend on a wisp this database has no row for.
func undecidedBlockedNote(n int64) string {
	return fmt.Sprintf("%d row(s) depend on a wisp this database does not have; is_blocked left as found", n)
}

// undecidedBlockedDetail says why such a row is not a finding on a clone, and
// when it is one.
const undecidedBlockedDetail = "Wisps are local to the database that created them, so a clone has the dependency and not the wisp, and keeps the flag it last merged. " +
	"On the database that created the wisp this names a dependency left behind by a delete that bypassed bd"
