package main

import (
	"fmt"

	"github.com/steveyegge/beads/internal/audit"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// auditUpdateFieldChanges journals an update's key field changes to the
// optional interactions sidecar (internal/audit), for both routes of
// `bd update` (GH#6146: claims moved rows without leaving a record).
//
// Explicit patch fields keep their existing precedence and their existing
// explicit-only shape: each of status/assignee emits at most one record —
// the explicit value winning over whatever a claim implied — and priority
// stays explicit-only. A claim's implied status/assignee move is journaled
// only when the update reported a change, from the command's advisory
// pre-read and the mutation's returned post-state.
//
// The pre-read happened one transaction earlier than the write, so its old
// values are ADVISORY: a concurrent writer's move between the read and the
// commit is journaled as this command's old value. `changed` describes the
// whole update, not its claim half, so a claim riding a changed patch also
// journals — this stays best-effort journaling, not a transactional record;
// the database events tables are that.
func auditUpdateFieldChanges(issueID string, before, after *types.Issue, patch issueops.IssuePatch, claim, changed bool, actor string) {
	if issueID == "" || before == nil {
		// No row to hang the record on, and no snapshot to take an old
		// value from: never fabricate one.
		return
	}
	if patch.Status.Set {
		audit.LogFieldChange(issueID, "status", string(before.Status), string(patch.Status.Value), actor, "")
	} else if claim && changed && after != nil {
		audit.LogFieldChange(issueID, "status", string(before.Status), string(after.Status), actor, "")
	}
	if patch.Assignee.Set {
		audit.LogFieldChange(issueID, "assignee", before.Assignee, patch.Assignee.Value, actor, "")
	} else if claim && changed && after != nil {
		audit.LogFieldChange(issueID, "assignee", before.Assignee, after.Assignee, actor, "")
	}
	if patch.Priority.Set {
		audit.LogFieldChange(issueID, "priority", fmt.Sprintf("%d", before.Priority), fmt.Sprintf("%d", patch.Priority.Value), actor, "")
	}
}
