package doctor

import (
	"context"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/cmd/bd/doctor/fix"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

// CheckDependencyKeysWithStore reports dependency rows left behind by the
// #4259 rekey backfill (bd-6dnrw.17): rows whose surrogate id is not the
// deterministic depid.New(issue_id, target) — per-clone-random keys that
// break bd dolt pull — and rows with no target at all, which the backfill
// deliberately skips and which otherwise stay randomly keyed forever.
func CheckDependencyKeysWithStore(ss *SharedStore) DoctorCheck {
	store := ss.Store()
	if store == nil {
		return DoctorCheck{
			Name:    "Dependency Keys",
			Status:  StatusOK,
			Message: "No database yet",
		}
	}
	return checkDependencyKeysWithStore(store)
}

func checkDependencyKeysWithStore(store *dolt.DoltStore) DoctorCheck {
	anomalies, err := fix.ScanDependencyKeys(context.Background(), store.UnderlyingDB())
	if err != nil {
		return DoctorCheck{
			Name:    "Dependency Keys",
			Status:  StatusWarning,
			Message: "Unable to scan dependency keys",
			Detail:  err.Error(),
		}
	}
	if len(anomalies) == 0 {
		return DoctorCheck{
			Name:    "Dependency Keys",
			Status:  StatusOK,
			Message: "All dependency ids deterministic",
		}
	}

	var parts []string
	var misKeyed, nullTarget int
	for _, a := range anomalies {
		if n := len(a.MisKeyed); n > 0 {
			misKeyed += n
			parts = append(parts, fmt.Sprintf("%s: %d non-deterministic id(s)", a.Table, n))
		}
		if n := len(a.NullTarget); n > 0 {
			nullTarget += n
			parts = append(parts, fmt.Sprintf("%s: %d row(s) with no target", a.Table, n))
		}
	}

	var msg string
	switch {
	case misKeyed > 0 && nullTarget > 0:
		msg = fmt.Sprintf("%d randomly-keyed and %d targetless dependency row(s) — random keys break 'bd dolt pull' across clones", misKeyed, nullTarget)
	case misKeyed > 0:
		msg = fmt.Sprintf("%d randomly-keyed dependency row(s) — random keys break 'bd dolt pull' across clones", misKeyed)
	default:
		msg = fmt.Sprintf("%d dependency row(s) with no target", nullTarget)
	}

	return DoctorCheck{
		Name:    "Dependency Keys",
		Status:  StatusWarning,
		Message: msg,
		Detail:  strings.Join(parts, "; "),
		Fix:     "Run: bd doctor --fix (re-keys to deterministic ids, removes targetless rows)",
	}
}
