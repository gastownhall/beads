package doctor

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/types"
)

// BlockedStatusCheckName is the doctor check name; applyFixList dispatches on
// this exact string to the manual-review guidance (it has no repair).
const BlockedStatusCheckName = "Blocked Status"

// maxBlockedStatusExamples caps how many issue IDs the detail line names, so a
// database with many of them stays readable.
const maxBlockedStatusExamples = 5

// IssueSearcher is the one store method CheckBlockedStatus reads. The shared
// store's *dolt.DoltStore and the embedded store bd doctor --check=conventions
// opens both satisfy it.
type IssueSearcher interface {
	SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error)
}

// CheckBlockedStatusWithStore is CheckBlockedStatus over the shared store.
func CheckBlockedStatusWithStore(ss *SharedStore) DoctorCheck {
	store := ss.Store()
	if store == nil {
		return CheckBlockedStatus(context.Background(), nil)
	}
	return CheckBlockedStatus(context.Background(), store)
}

// CheckBlockedStatus reports issues and wisps whose explicit status is
// 'blocked' while their stored dependency-blocked flag (is_blocked) is clear
// (bd-e5d8fd). bd close --suggest-next lists such an issue as newly unblocked
// when its last blocker closes, but a close never changes a dependent's own
// status, and an issue can be set blocked with no dependency at all (bd
// update --status blocked, or a tracker sync mapping its own blocked status).
// bd ready leaves out every status-blocked issue and bd blocked selects only
// is_blocked=1, so such an issue is in neither view and only bd list finds it.
//
// It is advisory and never repaired: nothing in the dependency graph tells a
// hold that is still meant (waiting on a person, a decision or a tracker) from
// one left behind, so the fix is for the user to review each issue and release
// it with bd update. It reads the stored flags: a stale is_blocked (see the
// Blocked State check) can add a candidate or hide one, so after that check's
// repair this one is worth rerunning before acting on its list.
func CheckBlockedStatus(ctx context.Context, searcher IssueSearcher) DoctorCheck {
	if searcher == nil {
		return DoctorCheck{
			Name:    BlockedStatusCheckName,
			Status:  StatusOK,
			Message: "No database yet",
		}
	}
	blocked := types.StatusBlocked
	notBlocked := false
	issues, err := searcher.SearchIssues(ctx, "", types.IssueFilter{Status: &blocked, IsBlocked: &notBlocked})
	if err != nil {
		return DoctorCheck{
			Name:    BlockedStatusCheckName,
			Status:  StatusWarning,
			Message: "Unable to check blocked statuses",
			Detail:  err.Error(),
		}
	}
	if len(issues) == 0 {
		return DoctorCheck{
			Name:    BlockedStatusCheckName,
			Status:  StatusOK,
			Message: "Every issue in status 'blocked' is held by a dependency or none is blocked",
		}
	}

	ids := make([]string, len(issues))
	for i, issue := range issues {
		ids[i] = issue.ID
	}
	sort.Strings(ids)
	examples := make([]string, 0, maxBlockedStatusExamples)
	for _, id := range ids {
		if len(examples) == maxBlockedStatusExamples {
			break
		}
		examples = append(examples, remedy.PrintedWord(id))
	}
	detail := strings.Join(examples, ", ")
	if len(ids) > len(examples) {
		detail += fmt.Sprintf(" (+%d more)", len(ids)-len(examples))
	}
	detail += ". They appear in neither 'bd ready' nor 'bd blocked'; a status set on purpose (a hold waiting on a person, a decision or a tracker) is left as it is"

	return DoctorCheck{
		Name:    BlockedStatusCheckName,
		Status:  StatusWarning,
		Message: fmt.Sprintf("%d issue(s) remain in status 'blocked' while their stored dependency-blocked flag is clear", len(ids)),
		Detail:  detail,
		Fix: "Review each issue; if nothing holds it, release it with: bd update <id>... --status open. " +
			"If a blocker is missing, record it with: bd dep add <id> <blocker-id>. " +
			"A hold a tracker manages is released in that tracker. " +
			"If the Blocked State check reports stale flags, repair them first (bd recompute-blocked) and rerun bd doctor",
	}
}
