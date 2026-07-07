package doctor

import (
	"fmt"

	"github.com/steveyegge/beads/cmd/bd/doctor/fix"
)

// CheckCrossTableDuplicates detects IDs that exist in both the issues and
// wisps tables. Such dups arise from the message→wisp dual-write window
// (be-iabdi) and block every id-resolver lookup until removed.
//
// This check does not require CGO: it queries via MySQL wire protocol using
// the same dolt-server connection as the fix package.
func CheckCrossTableDuplicates(path string) DoctorCheck {
	count, err := fix.CountCrossTableDuplicates(path)
	if err != nil {
		return DoctorCheck{
			Name:    "Cross-Table Duplicates",
			Status:  StatusOK,
			Message: "N/A (no database or wisps table absent)",
		}
	}
	if count == 0 {
		return DoctorCheck{
			Name:    "Cross-Table Duplicates",
			Status:  StatusOK,
			Message: "No cross-table duplicates",
		}
	}
	return DoctorCheck{
		Name:    "Cross-Table Duplicates",
		Status:  StatusError,
		Message: fmt.Sprintf("%d id(s) exist in both issues and wisps", count),
		Detail:  "Stale issues-table copies break every lookup for the affected IDs",
		Fix:     "Run 'bd doctor --check=validate --fix' to remove stale issues copies (wisps are canonical)",
	}
}
