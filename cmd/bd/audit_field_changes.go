package main

import (
	"fmt"

	"github.com/steveyegge/beads/internal/audit"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// updateAuditChange is one field_change row a successful `bd update` writes to
// the optional interactions.jsonl sidecar.
type updateAuditChange struct {
	field    string
	oldValue string
	newValue string
}

// updateAuditChanges decides the sidecar field_change rows for one successful
// update, in the order status, assignee, priority, at most one per field.
//
// An explicit --status/--assignee/--priority value wins, recorded against the
// pre-read as it always was. A --claim moves status and assignee without
// putting them in the patch (the claim rides the request's own compare-and-
// set), so for a field the patch leaves unset a claim that changed something
// records the pre-read value against the returned post-state (GH#6146).
// Priority is never claim-derived.
//
// before is the command's own advisory pre-read, one transaction earlier than
// the write, and changed describes the whole update, not the claim alone: the
// rows are best-effort capture, not a transactional pre-image. A missing
// snapshot yields no claim-derived row rather than an invented one. Rows whose
// old and new values match are dropped by audit.LogFieldChange.
func updateAuditChanges(patch issueops.IssuePatch, claim, changed bool, before, after *types.Issue) []updateAuditChange {
	var changes []updateAuditChange
	claimed := claim && changed && before != nil && after != nil
	oldStatus, oldAssignee, oldPriority := "", "", ""
	if before != nil {
		oldStatus = string(before.Status)
		oldAssignee = before.Assignee
		oldPriority = fmt.Sprintf("%d", before.Priority)
	}
	switch {
	case patch.Status.Set:
		changes = append(changes, updateAuditChange{"status", oldStatus, string(patch.Status.Value)})
	case claimed:
		changes = append(changes, updateAuditChange{"status", oldStatus, string(after.Status)})
	}
	switch {
	case patch.Assignee.Set:
		changes = append(changes, updateAuditChange{"assignee", oldAssignee, patch.Assignee.Value})
	case claimed:
		changes = append(changes, updateAuditChange{"assignee", oldAssignee, after.Assignee})
	}
	if patch.Priority.Set {
		changes = append(changes, updateAuditChange{"priority", oldPriority, fmt.Sprintf("%d", patch.Priority.Value)})
	}
	return changes
}

// logUpdateAuditChanges writes updateAuditChanges' rows for issueID. Like
// every sidecar write it is best-effort and a no-op while the sidecar is off.
func logUpdateAuditChanges(issueID, actor string, changes []updateAuditChange) {
	for _, c := range changes {
		audit.LogFieldChange(issueID, c.field, c.oldValue, c.newValue, actor, "")
	}
}
