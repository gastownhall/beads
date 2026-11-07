package doctor

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/types"
)

// BlockedStatusCheckName is the doctor check name; applyFixList dispatches
// this check's manual-review guidance (never a repair) on this exact string.
const BlockedStatusCheckName = "Blocked Status"

// maxBlockedStatusExamples caps how many held rows the detail line names, so
// a database with thousands of them stays readable.
const maxBlockedStatusExamples = 5

// BlockedStatusSearcher is the one read the Blocked Status check needs. Every
// storage.DoltStorage satisfies it — the server-mode store the full doctor
// opens and the embedded store `doctor --check=conventions` runs on alike —
// so both surfaces share one check without a new storage API. Unlike Blocked
// State, which reads the repair target its --fix recomputes (fix.OpenScanDB),
// this advisory reports whatever store it is handed.
type BlockedStatusSearcher interface {
	SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error)
}

// CheckBlockedStatusWithStore runs CheckBlockedStatus on the shared store the
// full doctor already holds open, reporting "No database yet" when there is
// none to read.
func CheckBlockedStatusWithStore(ss *SharedStore) DoctorCheck {
	store := ss.Store()
	if store == nil {
		return DoctorCheck{
			Name:    BlockedStatusCheckName,
			Status:  StatusOK,
			Message: "No database yet",
		}
	}
	return CheckBlockedStatus(context.Background(), store)
}

// CheckBlockedStatus reports issues and wisps whose status is 'blocked'
// while their stored dependency-blocked flag (is_blocked) is clear: a hold a
// hand or a tracker import set that the dependency graph no longer shares —
// typically the status outliving the last blocker's close. Such a row is
// easy to lose: 'bd ready' excludes the blocked category, and 'bd blocked'
// lists stored is_blocked=1 rows, so it appears in neither. The hold may
// also be intentional, so the check only advises: it never changes a status,
// and 'bd doctor --fix' does not either — the release is a reviewed
// 'bd update <id> --status open'.
//
// It reports stored flags, not the graph: a stale is_blocked can hide a
// candidate here (or, held the other way, add one to 'bd blocked'), which
// the Blocked State check reports separately — recompute first and re-run
// before acting on this list.
func CheckBlockedStatus(ctx context.Context, searcher BlockedStatusSearcher) DoctorCheck {
	if searcher == nil {
		return DoctorCheck{
			Name:    BlockedStatusCheckName,
			Status:  StatusOK,
			Message: "No database yet",
		}
	}
	blockedStatus := types.StatusBlocked
	clearFlag := false
	rows, err := searcher.SearchIssues(ctx, "", types.IssueFilter{
		Status:    &blockedStatus,
		IsBlocked: &clearFlag,
	})
	if err != nil {
		return DoctorCheck{
			Name:    BlockedStatusCheckName,
			Status:  StatusWarning,
			Message: "Unable to check blocked-status holds",
			Detail:  err.Error(),
		}
	}
	if len(rows) == 0 {
		return DoctorCheck{
			Name:    BlockedStatusCheckName,
			Status:  StatusOK,
			Message: "no rows hold status 'blocked' with a clear stored dependency-blocked flag",
		}
	}

	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	sort.Strings(ids) // deterministic examples, whatever the store's order

	examples := make([]string, 0, min(maxBlockedStatusExamples, len(ids)))
	for _, id := range ids[:min(maxBlockedStatusExamples, len(ids))] {
		examples = append(examples, remedy.PrintedWord(id))
	}
	detail := strings.Join(examples, ", ")
	if remainder := len(ids) - len(examples); remainder > 0 {
		detail += fmt.Sprintf(" (+%d more)", remainder)
	}

	return DoctorCheck{
		Name:    BlockedStatusCheckName,
		Status:  StatusWarning,
		Message: fmt.Sprintf("%d issue(s) remain in status 'blocked' while their stored dependency-blocked flag is clear", len(rows)),
		Detail:  detail,
		Fix:     "Review each row — the hold may be intentional, and a tracker-imported one is its tracker's to manage. Release with: bd update <id> --status open; a missing edge is recorded with: bd dep add <id> <blocker-id>. Stored flags are reported as they are: if Blocked State reports staleness, run 'bd doctor --fix' first and re-check. No status is changed for you here, including under --fix.",
	}
}
