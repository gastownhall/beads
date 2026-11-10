package tracker

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// recordingLifecycleTx captures the update map a pull hands to the funnel.
type recordingLifecycleTx struct {
	storage.IssueLifecycleTransaction
	updates map[string]interface{}
}

func (t *recordingLifecycleTx) UpdateIssue(_ context.Context, _ string, updates map[string]interface{}, _ string) error {
	t.updates = updates
	return nil
}

// TestApplyPullIssueFieldsAlwaysForcesClosePolicy pins the sync-pull decision.
// The remote tracker is authoritative for the status it reports and cannot see
// local-only children or blockers, so a pull must never be refused by close
// policy — a single upstream close would otherwise wedge every later sync. Both
// the pull and the conflict reimport route through this one function, so the
// key has to be unconditional here rather than argued about per caller.
func TestApplyPullIssueFieldsAlwaysForcesClosePolicy(t *testing.T) {
	for _, updates := range []map[string]interface{}{
		{"title": "from remote", "status": string(types.StatusClosed)},
		{"title": "from remote", "status": string(types.StatusOpen)},
		{"title": "from remote"},
	} {
		tx := &recordingLifecycleTx{}
		if err := applyPullIssueFields(context.Background(), tx, "bd-pull", updates, "sync"); err != nil {
			t.Fatalf("applyPullIssueFields: %v", err)
		}
		if got := tx.updates[issueops.OpForceClosePolicy]; got != true {
			t.Errorf("updates[%q] = %v for %v, want true", issueops.OpForceClosePolicy, got, updates)
		}
		if tx.updates["title"] != "from remote" {
			t.Errorf("the pulled fields did not survive: %v", tx.updates)
		}
	}
}
