package doctor

import (
	"fmt"
	"strings"

	"github.com/steveyegge/beads/cmd/bd/doctor/fix"
)

// CheckCloneLocalFKs detects clone-local (dolt_ignored) tables whose foreign
// keys into the tracked plane have been severed by a hard reset (bd-7bpkd):
// DOLT_RESET('--hard') swaps the tracked table's backing object and silently
// drops the untracked table's constraint — enforcement stops, the loss
// survives server restarts, and orphan rows accumulate from the first squash
// window onward. The fix deletes the orphans and re-adds each constraint in
// place.
//
// This check does not require CGO: it queries via MySQL wire protocol the
// dolt server its fix writes, or the socket of a server only a unix socket
// names, where the fix does not run (fix.ScanSeveredCloneLocalFKs).
func CheckCloneLocalFKs(path string) DoctorCheck {
	severed, err := fix.ScanSeveredCloneLocalFKs(path)
	if err != nil {
		return DoctorCheck{
			Name:    "Clone-Local FKs",
			Status:  StatusOK,
			Message: "N/A (no database)",
		}
	}
	if len(severed) == 0 {
		return DoctorCheck{
			Name:    "Clone-Local FKs",
			Status:  StatusOK,
			Message: "All clone-local FKs enforcing",
		}
	}

	parts := make([]string, 0, len(severed))
	orphans := 0
	for _, fk := range severed {
		parts = append(parts, fmt.Sprintf("%s.%s (%d orphan(s))", fk.Table, fk.Constraint, fk.Orphans))
		orphans += fk.Orphans
	}
	return DoctorCheck{
		Name:    "Clone-Local FKs",
		Status:  StatusWarning,
		Message: fmt.Sprintf("%d severed FK(s) on clone-local tables: %s", len(severed), strings.Join(parts, ", ")),
		Detail:  cloneLocalFKDetail(orphans),
		Fix:     "Run 'bd doctor --fix' to remove orphaned rows and re-link the constraint(s) in place",
	}
}

// cloneLocalFKDetail explains a severing without blaming a flatten or
// compact that just ran: those soft-reset main now (bd-7bpkd's fix), so a
// severing found today predates the fix or came from another hard-reset
// path — merge abort recovery, the migration-lock error path.
func cloneLocalFKDetail(orphans int) string {
	return fmt.Sprintf("A past hard reset dropped FKs from dolt_ignored tables (bd-7bpkd); %d orphaned row(s) accumulated while enforcement was off. Flatten and compact soft-reset now, so the severing predates that fix or came from merge abort or migration error recovery", orphans)
}
